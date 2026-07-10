/*
Copyright © 2024 netcfg authors

NDP 代答器在 daemon 里的管理：把各接口 ndp-proxy.rules 起成 ndpproxy.Responder
goroutine（设 allmulti），SIGHUP 重载、退出时停。见 docs/ndp-responder-design.md。
*/

package cmd

import (
	"context"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"sync"

	"github.com/netcfg/netcfg/config"
	"github.com/netcfg/netcfg/ndpproxy"
	nl "github.com/netcfg/netcfg/netlink"
	"github.com/netcfg/netcfg/vpp"
)

// ndpManager 管理运行中的 NDP 代答器（按接口）。
type ndpManager struct {
	mu        sync.Mutex
	running   map[string]context.CancelFunc // iface → cancel
	configs   map[string]ndpproxy.Config    // iface → 最近一次配置（供 tap 重建后重启）
	tapSpecs  map[string]ndpTapSpec         // VPP tap 内核名 → 重建信息（自愈用）
	apiSocket string                        // VPP API socket（tap 重建用）
	watchStop context.CancelFunc            // link 监听 goroutine 的取消（非 nil=已在跑）
}

func newNDPManager() *ndpManager {
	return &ndpManager{running: map[string]context.CancelFunc{}, configs: map[string]ndpproxy.Config{}}
}

// Reload 简单策略：全停后按新配置全启（响应器轻量，重启代价小）。
func (m *ndpManager) Reload(cfg *config.Config) {
	m.Stop()
	m.mu.Lock()
	m.configs = map[string]ndpproxy.Config{}
	for _, c := range ndpResponderConfigs(cfg) {
		m.configs[c.Iface] = c
		m.startLocked(c)
	}
	m.tapSpecs, m.apiSocket = ndpTapSpecs(cfg)
	m.mu.Unlock()
	m.ensureWatcher() // 有 VPP tap 时启动强删自愈监听（只启一次）
}

// startLocked 启动一个响应器（须持有 m.mu）：设 allmulti + 起 goroutine。
func (m *ndpManager) startLocked(c ndpproxy.Config) {
	setAllmulti(c.Iface, true)
	resp, err := ndpproxy.New(c)
	if err != nil {
		slog.Error("ndp-proxy responder start failed", "interface", c.Iface, "error", err)
		setAllmulti(c.Iface, false)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.running[c.Iface] = cancel
	go func(r *ndpproxy.Responder) {
		if err := r.Run(ctx); err != nil {
			slog.Warn("ndp-proxy responder exited", "error", err)
		}
	}(resp)
}

// restart 停掉并按最近配置重启某接口的响应器（tap 重建后用）。
func (m *ndpManager) restart(iface string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cancel, ok := m.running[iface]; ok {
		cancel()
		delete(m.running, iface)
	}
	if c, ok := m.configs[iface]; ok {
		m.startLocked(c)
	}
}

// Stop 停止所有响应器并关 allmulti。
func (m *ndpManager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for iface, cancel := range m.running {
		cancel()
		setAllmulti(iface, false)
		delete(m.running, iface)
	}
}

func setAllmulti(iface string, on bool) {
	mgr, err := nl.New()
	if err != nil {
		return
	}
	defer mgr.Close()
	if err := mgr.SetAllmulti(iface, on); err != nil {
		slog.Warn("ndp-proxy set allmulti failed", "interface", iface, "on", on, "error", err)
	}
}

// ndpResponderConfigs 从配置收集所有带 rules 的接口的响应器配置（default ns）。
// 内核设备：响应器直接绑设备。VPP 管的 bridge：响应器绑该 bridge 的托管 tap
// （apply 已把 tap 建进 BD），且只承载 external-MAC 静态规则。VPP 管的
// ethernet/vlan/bond 上的 rules 无对应机制，告警忽略。
func ndpResponderConfigs(cfg *config.Config) []ndpproxy.Config {
	var out []ndpproxy.Config

	// addOn 在 iface 上按 rules 建一个响应器配置；staticExternalOnly=true 时只取显式
	// neighbor 的静态规则（VPP-tap 用）。
	addOn := func(iface, scope string, n *config.NDProxy, staticExternalOnly bool) {
		if n == nil || len(n.Rules) == 0 {
			return
		}
		router := true // 跟 ndppd 默认一致
		if n.Router != nil {
			router = *n.Router
		}
		var rules []ndpproxy.Rule
		for _, r := range n.Rules {
			pfx, err := netip.ParsePrefix(r.Prefix)
			if err != nil {
				slog.Warn("ndp-proxy skip rule: bad prefix", "interface", scope, "prefix", r.Prefix, "error", err)
				continue
			}
			var mac net.HardwareAddr
			if r.Neighbor != "" {
				if mac, err = net.ParseMAC(r.Neighbor); err != nil {
					slog.Warn("ndp-proxy skip rule: bad neighbor MAC", "interface", scope, "mac", r.Neighbor, "error", err)
					continue
				}
			}
			if staticExternalOnly && (mac == nil || strings.EqualFold(r.Mode, "auto")) {
				continue // VPP-tap 上跳过 hairpin/auto（apply 已告警）
			}
			rules = append(rules, ndpproxy.Rule{Prefix: pfx, Neighbor: mac, Auto: strings.EqualFold(r.Mode, "auto")})
		}
		if len(rules) > 0 {
			out = append(out, ndpproxy.Config{Iface: iface, Router: router, Rules: rules})
		}
	}

	n := &cfg.Network
	g := n.Renderer
	// ethernet/vlan/bond：内核设备直接绑；VPP 管的告警忽略（tap 方案只覆盖 bridge）。
	addDev := func(name string, ndp *config.NDProxy, vppManaged bool) {
		if vppManaged {
			if ndp != nil && len(ndp.Rules) > 0 {
				slog.Warn("ndp-proxy rules ignored on VPP-managed non-bridge device (put the segment in a VPP bridge to use the managed responder tap)", "interface", name)
			}
			return
		}
		addOn(name, name, ndp, false)
	}
	for name, d := range n.Ethernets {
		addDev(name, d.NDProxy, config.VPPManaged(d.VPP, d.Renderer, g))
	}
	for name, d := range n.Vlans {
		addDev(name, d.NDProxy, config.VPPManaged(d.VPP, d.Renderer, g))
	}
	for name, d := range n.Bonds {
		addDev(name, d.NDProxy, config.VPPManaged(d.VPP, d.Renderer, g))
	}
	// bridge：内核 bridge 直接绑；VPP bridge 绑其托管 tap（external-MAC 静态规则）。
	for name, d := range n.Bridges {
		if config.VPPManaged(d.VPP, d.Renderer, g) {
			addOn(vpp.NDPTapName(name), name, d.NDProxy, true)
		} else {
			addOn(name, name, d.NDProxy, false)
		}
	}
	return out
}

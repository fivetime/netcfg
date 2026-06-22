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
	"sync"

	"github.com/netcfg/netcfg/config"
	"github.com/netcfg/netcfg/ndpproxy"
	nl "github.com/netcfg/netcfg/netlink"
)

// ndpManager 管理运行中的 NDP 代答器（按接口）。
type ndpManager struct {
	mu      sync.Mutex
	running map[string]context.CancelFunc // iface → cancel
}

func newNDPManager() *ndpManager {
	return &ndpManager{running: map[string]context.CancelFunc{}}
}

// Reload 简单策略：全停后按新配置全启（响应器轻量，重启代价小）。
func (m *ndpManager) Reload(cfg *config.Config) {
	m.Stop()
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range ndpResponderConfigs(cfg) {
		setAllmulti(c.Iface, true)
		resp, err := ndpproxy.New(c)
		if err != nil {
			slog.Error("ndp-proxy responder start failed", "interface", c.Iface, "error", err)
			setAllmulti(c.Iface, false)
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		m.running[c.Iface] = cancel
		go func(r *ndpproxy.Responder) {
			if err := r.Run(ctx); err != nil {
				slog.Warn("ndp-proxy responder exited", "error", err)
			}
		}(resp)
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
func ndpResponderConfigs(cfg *config.Config) []ndpproxy.Config {
	var out []ndpproxy.Config
	add := func(name string, n *config.NDProxy) {
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
				slog.Warn("ndp-proxy skip rule: bad prefix", "interface", name, "prefix", r.Prefix, "error", err)
				continue
			}
			var mac net.HardwareAddr
			if r.Neighbor != "" {
				if mac, err = net.ParseMAC(r.Neighbor); err != nil {
					slog.Warn("ndp-proxy skip rule: bad neighbor MAC", "interface", name, "mac", r.Neighbor, "error", err)
					continue
				}
			}
			rules = append(rules, ndpproxy.Rule{Prefix: pfx, Neighbor: mac, Auto: r.Mode == "auto"})
		}
		if len(rules) > 0 {
			out = append(out, ndpproxy.Config{Iface: name, Router: router, Rules: rules})
		}
	}
	n := &cfg.Network
	for name, d := range n.Ethernets {
		add(name, d.NDProxy)
	}
	for name, d := range n.Vlans {
		add(name, d.NDProxy)
	}
	for name, d := range n.Bridges {
		add(name, d.NDProxy)
	}
	for name, d := range n.Bonds {
		add(name, d.NDProxy)
	}
	return out
}

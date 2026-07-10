/*
Copyright © 2024 netcfg authors

NDP 代答 tap 的 daemon 侧自愈：托管的 tap（ncndp<hash>）被 `ip link del` 强删时，
netcfg daemon 监听内核 RTM_DELLINK，重连 VPP 重建 tap（清残留 + 重新入 BD + 重打 ifalias），
再重启该 tap 上的响应器。配置里删掉 ndp-proxy 则不再登记该 tap，删除后不重建。
见 docs/ndp-responder-design.md、docs/vpp-backend-design.md。
*/

package cmd

import (
	"context"
	"log/slog"

	"github.com/netcfg/netcfg/config"
	"github.com/netcfg/netcfg/vpp"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// ndpTapSpec 重建一根 NDP 代答 tap 所需的信息。
type ndpTapSpec struct {
	bridge string
	bdID   uint32
}

// ndpTapSpecs 收集当前配置里所有 VPP 托管 NDP 代答 tap（tap 内核名 → 重建信息），
// 以及 VPP API socket。只登记带 external-MAC 静态 rules 的 VPP bridge。
func ndpTapSpecs(cfg *config.Config) (map[string]ndpTapSpec, string) {
	specs := map[string]ndpTapSpec{}
	n := &cfg.Network
	g := n.Renderer
	for name, b := range n.Bridges {
		if b == nil || !config.VPPManaged(b.VPP, b.Renderer, g) {
			continue
		}
		if rules, _ := vppNDPTapRules(b.NDProxy); len(rules) == 0 {
			continue
		}
		specs[vpp.NDPTapName(name)] = ndpTapSpec{bridge: name, bdID: ndpTapBdID(name, b)}
	}
	sock := ""
	if n.VPP != nil {
		sock = n.VPP.APISocket
	}
	return specs, sock
}

// ensureWatcher 在有 VPP tap 需要看护时启动 link 监听 goroutine（只启一次，跨 reload 存活）。
func (m *ndpManager) ensureWatcher() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.watchStop != nil || len(m.tapSpecs) == 0 {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.watchStop = cancel
	go m.watchTaps(ctx)
}

// watchTaps 监听内核 link 事件，托管 tap 被强删时重建 + 重启响应器。
func (m *ndpManager) watchTaps(ctx context.Context) {
	ch := make(chan netlink.LinkUpdate)
	done := make(chan struct{})
	go func() {
		<-ctx.Done()
		close(done)
	}()
	if err := netlink.LinkSubscribe(ch, done); err != nil {
		slog.Warn("ndp-proxy tap watcher: subscribe failed", "error", err)
		return
	}
	slog.Info("ndp-proxy tap watcher started")
	for {
		select {
		case <-ctx.Done():
			return
		case u, ok := <-ch:
			if !ok {
				return
			}
			if u.Header.Type != unix.RTM_DELLINK {
				continue
			}
			name := u.Link.Attrs().Name
			m.mu.Lock()
			_, isOurs := m.tapSpecs[name]
			m.mu.Unlock()
			if !isOurs {
				continue
			}
			// 以磁盘上的最新配置为准，区分「管理员强删（配置没变→重建）」和「apply 删掉
			// 了 ndp-proxy（配置已变→不重建）」，避免 apply 删 tap 时 daemon 又建回来。
			spec, sock, wanted := m.wantedTapSpec(name)
			if !wanted {
				continue
			}
			slog.Warn("ndp-proxy tap was deleted; rebuilding", "tap", name, "bridge", spec.bridge)
			if err := rebuildNDPTap(sock, spec.bridge, spec.bdID); err != nil {
				slog.Error("ndp-proxy tap rebuild failed", "tap", name, "bridge", spec.bridge, "error", err)
				continue
			}
			setNDPTapAlias(name, spec.bridge)
			m.restart(name)
			slog.Info("ndp-proxy tap rebuilt", "tap", name, "bridge", spec.bridge)
		}
	}
}

// wantedTapSpec 重读磁盘配置，判断某 tap 是否仍应存在，并返回其最新重建信息与 VPP socket。
// 不再需要时同步从 m.tapSpecs 抹掉（免得后续事件再触发）。
func (m *ndpManager) wantedTapSpec(name string) (ndpTapSpec, string, bool) {
	cfg, err := config.LoadConfig(configDir)
	if err != nil {
		// 读不到配置：保守用内存里的旧值重建（宁可多建也别把该在的 tap 弄没）。
		m.mu.Lock()
		spec, ok := m.tapSpecs[name]
		sock := m.apiSocket
		m.mu.Unlock()
		return spec, sock, ok
	}
	specs, sock := ndpTapSpecs(cfg)
	spec, ok := specs[name]
	m.mu.Lock()
	m.tapSpecs = specs
	m.apiSocket = sock
	m.mu.Unlock()
	return spec, sock, ok
}

// rebuildNDPTap 重连 VPP 重建一根被强删的 tap：先删 VPP 侧残留（强删只去掉内核 netdev，
// VPP 侧仍挂着 tag），再重新创建 + 入 BD，让内核 netdev 重新出现。
func rebuildNDPTap(sock, bridge string, bdID uint32) error {
	c, err := vpp.Connect(sock)
	if err != nil {
		return err
	}
	defer c.Close()
	a := vpp.NewApplier(c)
	ctx := context.Background()
	if err := a.DeleteNDPTap(ctx, vpp.NDPTapName(bridge)); err != nil {
		slog.Warn("ndp-proxy rebuild: delete dangling tap failed", "bridge", bridge, "error", err)
	}
	_, err = a.EnsureNDPTap(ctx, bridge, bdID)
	return err
}

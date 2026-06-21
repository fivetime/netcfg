/*
Copyright © 2024 netcfg authors

VPP 后端在 apply 中的分流（V0 骨架）：把归 VPP 管的设备从内核路径拆出，交给
VPP applier。当前 V0 仅连接 VPP + 兼容性自检 + 列出待管理设备，不下发流量（V1a 实现）。
见 docs/vpp-backend-design.md。
*/

package cmd

import (
	"context"
	"log/slog"
	"os"
	"sort"
	"strings"

	"github.com/netcfg/netcfg/config"
	"github.com/netcfg/netcfg/vpp"
)

const vppStartupConfPath = "/etc/vpp/startup.conf"

// generateStartupConf 在存在 dpdk 独占设备或显式 vpp.startup 时，生成
// /etc/vpp/startup.conf（开机持久；改动需 VPP 重启才生效）。
func generateStartupConf(global *config.VPPGlobal, v *vppSet) {
	var dpdk []config.DpdkDev
	for _, name := range sortedKeys(v.ethernets) {
		e := v.ethernets[name]
		if e.VPP != nil && strings.EqualFold(e.VPP.Mode, "dpdk") && e.VPP.PCI != "" {
			dpdk = append(dpdk, config.DpdkDev{Name: name, PCI: e.VPP.PCI})
		}
	}
	hasStartup := global != nil && global.Startup != nil
	if len(dpdk) == 0 && !hasStartup {
		return
	}
	conf := config.GenerateStartupConf(global, dpdk)
	if err := os.WriteFile(vppStartupConfPath, []byte(conf), 0644); err != nil {
		slog.Warn("failed to write VPP startup.conf", "path", vppStartupConfPath, "error", err)
		return
	}
	slog.Info("generated VPP startup.conf (restart VPP to apply dpdk/cpu changes)", "path", vppStartupConfPath, "dpdk_devices", len(dpdk))
}

// vppSet 收集归 VPP 管的设备（按类型）。
type vppSet struct {
	ethernets map[string]*config.Ethernet
	bridges   map[string]*config.Bridge
	bonds     map[string]*config.Bond
	vlans     map[string]*config.Vlan
	vxlans    map[string]*config.Vxlan  // tunnels:mode:vxlan 经 Normalize 转入
	tunnels   map[string]*config.Tunnel // 非 vxlan 隧道（gre/ipip…，V1b 暂延后）
}

func (v *vppSet) empty() bool {
	return len(v.ethernets)+len(v.bridges)+len(v.bonds)+len(v.vlans)+len(v.vxlans)+len(v.tunnels) == 0
}

func (v *vppSet) names() []string {
	var ns []string
	for n := range v.ethernets {
		ns = append(ns, n)
	}
	for n := range v.bridges {
		ns = append(ns, n)
	}
	for n := range v.bonds {
		ns = append(ns, n)
	}
	for n := range v.vlans {
		ns = append(ns, n)
	}
	for n := range v.tunnels {
		ns = append(ns, n)
	}
	sort.Strings(ns)
	return ns
}

// splitVPPDevices 把命名空间里归 VPP 管的设备拆出，返回（仅内核设备的命名空间副本，
// VPP 设备集合）。不修改原 maps（ToNamespace 与 cfg.Network 共享底层 map）。
func splitVPPDevices(ns *config.Namespace, globalRenderer string) (*config.Namespace, *vppSet) {
	v := &vppSet{
		ethernets: map[string]*config.Ethernet{},
		bridges:   map[string]*config.Bridge{},
		bonds:     map[string]*config.Bond{},
		vlans:     map[string]*config.Vlan{},
		vxlans:    map[string]*config.Vxlan{},
		tunnels:   map[string]*config.Tunnel{},
	}
	kernel := *ns // 浅拷贝；下面替换被分流的几个 map 为过滤后的新 map

	kEth := map[string]*config.Ethernet{}
	for name, e := range ns.Ethernets {
		if e != nil && config.VPPManaged(e.VPP, e.Renderer, globalRenderer) {
			v.ethernets[name] = e
		} else {
			kEth[name] = e
		}
	}
	kernel.Ethernets = kEth

	kBr := map[string]*config.Bridge{}
	for name, b := range ns.Bridges {
		if b != nil && config.VPPManaged(b.VPP, b.Renderer, globalRenderer) {
			v.bridges[name] = b
		} else {
			kBr[name] = b
		}
	}
	kernel.Bridges = kBr

	kBond := map[string]*config.Bond{}
	for name, b := range ns.Bonds {
		if b != nil && config.VPPManaged(b.VPP, b.Renderer, globalRenderer) {
			v.bonds[name] = b
		} else {
			kBond[name] = b
		}
	}
	kernel.Bonds = kBond

	kVlan := map[string]*config.Vlan{}
	for name, vl := range ns.Vlans {
		if vl != nil && config.VPPManaged(vl.VPP, vl.Renderer, globalRenderer) {
			v.vlans[name] = vl
		} else {
			kVlan[name] = vl
		}
	}
	kernel.Vlans = kVlan

	kTun := map[string]*config.Tunnel{}
	for name, t := range ns.Tunnels {
		if t != nil && config.VPPManaged(t.VPP, t.Renderer, globalRenderer) {
			v.tunnels[name] = t
		} else {
			kTun[name] = t
		}
	}
	kernel.Tunnels = kTun

	// vxlan（tunnels:mode:vxlan 经 Normalize 转入 Vxlans；内部结构无 Renderer，
	// 归属看 vpp 块或全局 renderer）
	kVx := map[string]*config.Vxlan{}
	for name, vx := range ns.Vxlans {
		if vx != nil && config.VPPManaged(vx.VPP, "", globalRenderer) {
			v.vxlans[name] = vx
		} else {
			kVx[name] = vx
		}
	}
	kernel.Vxlans = kVx

	return &kernel, v
}

// setupVPP 处理 VPP 设备：连接 + 兼容性自检后，把 ethernet 设备下发到 VPP（V1a）。
// bridge/bond/vlan/tunnel 暂记录为延后（V1b）。
func setupVPP(global *config.VPPGlobal, v *vppSet) error {
	// 生成 startup.conf（dpdk/cpu，开机持久；需重启 VPP 生效）
	generateStartupConf(global, v)

	sock := ""
	if global != nil {
		sock = global.APISocket
	}
	c, err := vpp.Connect(sock)
	if err != nil {
		return err
	}
	defer c.Close()

	a := vpp.NewApplier(c)
	ctx := context.Background()

	// 依赖顺序：ethernet → bond（成员=ethernet）→ vlan（父=ethernet/bond）
	// → vxlan → bridge（成员=任意，须最后）。
	for _, name := range sortedKeys(v.ethernets) {
		if err := a.ApplyEthernet(ctx, name, v.ethernets[name]); err != nil {
			slog.Error("vpp apply ethernet failed", "device", name, "error", err)
		}
	}
	for _, name := range sortedKeys(v.bonds) {
		if err := a.ApplyBond(ctx, name, v.bonds[name]); err != nil {
			slog.Error("vpp apply bond failed", "device", name, "error", err)
		}
	}
	for _, name := range sortedKeys(v.vlans) {
		if err := a.ApplyVlan(ctx, name, v.vlans[name]); err != nil {
			slog.Error("vpp apply vlan failed", "device", name, "error", err)
		}
	}
	for _, name := range sortedKeys(v.vxlans) {
		if err := a.ApplyVxlan(ctx, name, v.vxlans[name]); err != nil {
			slog.Error("vpp apply vxlan failed", "device", name, "error", err)
		}
	}
	for _, name := range sortedKeys(v.tunnels) {
		slog.Warn("vpp non-vxlan tunnel not yet implemented (deferred); skipping", "device", name, "mode", v.tunnels[name].Mode)
	}
	for _, name := range sortedKeys(v.bridges) {
		if err := a.ApplyBridge(ctx, name, v.bridges[name]); err != nil {
			slog.Error("vpp apply bridge failed", "device", name, "error", err)
		}
	}
	return nil
}

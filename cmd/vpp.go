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
	"sort"

	"github.com/netcfg/netcfg/config"
	"github.com/netcfg/netcfg/vpp"
)

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

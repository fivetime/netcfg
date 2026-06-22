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
	prev := vpp.LoadState() // 上次 apply 创建的 VPP 设备（用于回收孤儿）

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

	// NAT（在设备就绪后应用，接口角色需引用已建接口）
	if global != nil && global.NAT != nil {
		a.ApplyNat(ctx, global.NAT)
	}

	// NDP 代理（接口就绪后，逐设备的 vpp.nd-proxy 地址）
	ndp := ndProxyFromSet(v)
	for _, name := range sortedKeys(ndp) {
		if err := a.ApplyNDProxy(ctx, name, ndp[name]); err != nil {
			slog.Error("vpp apply nd-proxy failed", "device", name, "error", err)
		}
	}

	// 增量回收：删除上次创建、本次配置中已不存在的 VPP 设备 + NAT 规则 + NDP 代理（孤儿）。
	desired := buildDesiredVPPState(v)
	if global != nil && global.NAT != nil {
		desired.Nat = natItemsFromConfig(global.NAT)
	}
	desired.NDProxy = ndProxyItems(ndp)
	reapVPPOrphans(ctx, a, prev, desired)
	reapNatOrphans(ctx, a, prev, desired)
	reapVPPNDProxyOrphans(ctx, a, prev, desired)
	if err := desired.Save(); err != nil {
		slog.Warn("failed to save VPP state", "error", err)
	}
	return nil
}

// natItemsFromConfig 把 NAT 配置展开为可回收的 NatItem 列表。
func natItemsFromConfig(nat *config.VPPNat) []vpp.NatItem {
	b := func(p *bool) bool { return p != nil && *p }
	enabled := func(p *bool) bool { return p == nil || *p }
	var items []vpp.NatItem
	if n := nat.Nat44; n != nil && enabled(n.Enable) {
		for _, i := range n.Interfaces {
			items = append(items, vpp.NatItem{Kind: "nat44-if", Iface: i.Name, Role: strings.ToLower(i.Role)})
		}
		for _, p := range n.Pools {
			items = append(items, vpp.NatItem{Kind: "nat44-pool", Start: p.Start, End: p.End, VRF: p.VRF, TwiceNat: b(p.TwiceNat)})
		}
		for _, s := range n.Static {
			items = append(items, vpp.NatItem{Kind: "nat44-static", Proto: strings.ToLower(s.Proto), Local: s.Local,
				LocalPort: s.LocalPort, External: s.External, ExternalIf: s.ExternalInterface,
				ExternalPort: s.ExternalPort, VRF: s.VRF, TwiceNat: b(s.TwiceNat)})
		}
	}
	if n := nat.Nat64; n != nil && enabled(n.Enable) {
		if n.Prefix != "" {
			items = append(items, vpp.NatItem{Kind: "nat64-prefix", Prefix: n.Prefix})
		}
		for _, i := range n.Interfaces {
			items = append(items, vpp.NatItem{Kind: "nat64-if", Iface: i.Name, Role: strings.ToLower(i.Role)})
		}
		for _, p := range n.Pools {
			items = append(items, vpp.NatItem{Kind: "nat64-pool", Start: p.Start, End: p.End, VRF: p.VRF})
		}
	}
	if n := nat.Nat66; n != nil {
		for _, s := range n.Static {
			items = append(items, vpp.NatItem{Kind: "nat66-static", Local: s.Local, External: s.External, VRF: s.VRF})
		}
	}
	return items
}

// reapNatOrphans 删除 prev 中有、desired 中无的 NAT 规则。
func reapNatOrphans(ctx context.Context, a *vpp.Applier, prev, desired *vpp.State) {
	want := map[string]bool{}
	for _, it := range desired.Nat {
		want[it.Key()] = true
	}
	for _, it := range prev.Nat {
		if want[it.Key()] {
			continue
		}
		if err := a.DeleteNat(ctx, it); err != nil {
			slog.Warn("vpp reap nat rule failed", "kind", it.Kind, "error", err)
		} else {
			slog.Info("vpp removed orphan nat rule", "kind", it.Kind, "local", it.Local, "iface", it.Iface, "start", it.Start)
		}
	}
}

// ndProxyFromSet 收集各 VPP 设备 vpp.nd-proxy 的地址（设备名 → IPv6 列表）。
func ndProxyFromSet(v *vppSet) map[string][]string {
	out := map[string][]string{}
	add := func(name string, d *config.VPPDevice) {
		if d != nil && len(d.NDProxy) > 0 {
			out[name] = d.NDProxy
		}
	}
	for n, e := range v.ethernets {
		add(n, e.VPP)
	}
	for n, b := range v.bonds {
		add(n, b.VPP)
	}
	for n, x := range v.vlans {
		add(n, x.VPP)
	}
	for n, x := range v.vxlans {
		add(n, x.VPP)
	}
	for n, t := range v.tunnels {
		add(n, t.VPP)
	}
	for n, b := range v.bridges {
		add(n, b.VPP)
	}
	return out
}

// ndProxyItems 把 nd-proxy 映射展开为可回收的 NDProxyItem 列表。
func ndProxyItems(ndp map[string][]string) []vpp.NDProxyItem {
	var items []vpp.NDProxyItem
	for iface, addrs := range ndp {
		for _, ip := range addrs {
			items = append(items, vpp.NDProxyItem{Iface: iface, IP: ip})
		}
	}
	return items
}

// reapNDProxyOrphans 删除 prev 中有、desired 中无的 NDP 代理条目。
func reapVPPNDProxyOrphans(ctx context.Context, a *vpp.Applier, prev, desired *vpp.State) {
	want := map[string]bool{}
	for _, it := range desired.NDProxy {
		want[it.Key()] = true
	}
	for _, it := range prev.NDProxy {
		if want[it.Key()] {
			continue
		}
		if err := a.DeleteNDProxy(ctx, it); err != nil {
			slog.Warn("vpp reap nd-proxy failed", "iface", it.Iface, "ip", it.IP, "error", err)
		} else {
			slog.Info("vpp removed orphan nd-proxy", "iface", it.Iface, "ip", it.IP)
		}
	}
}

// buildDesiredVPPState 从本次 VPP 设备集合构造期望状态（用于下次回收对比）。
func buildDesiredVPPState(v *vppSet) *vpp.State {
	s := vpp.NewState()
	for name, e := range v.ethernets {
		mode := "af-packet"
		hostif := name
		if e.VPP != nil {
			if e.VPP.Mode != "" {
				mode = strings.ToLower(e.VPP.Mode)
			}
			if e.VPP.HostIf != "" {
				hostif = e.VPP.HostIf
			}
		}
		di := vpp.DevInfo{Type: mode}
		if mode == "af-packet" {
			di.HostIf = hostif
		}
		s.Devices[name] = di
	}
	for name := range v.bonds {
		s.Devices[name] = vpp.DevInfo{Type: "bond"}
	}
	for name := range v.vlans {
		s.Devices[name] = vpp.DevInfo{Type: "vlan"}
	}
	for name, vx := range v.vxlans {
		port := vx.DestPort
		if port == 0 {
			port = vx.Port
		}
		s.Devices[name] = vpp.DevInfo{Type: "vxlan", Vni: uint32(vx.ID), Local: vx.Local, Remote: vx.Remote, Port: port}
	}
	for name, b := range v.bridges {
		bd := vpp.AutoBdID(name)
		if b.VPP != nil && b.VPP.BdID > 0 {
			bd = uint32(b.VPP.BdID)
		}
		s.Devices[name] = vpp.DevInfo{Type: "bridge", BdID: bd}
		if len(b.Addresses) > 0 {
			s.Devices[name+"-bvi"] = vpp.DevInfo{Type: "loopback"} // 带地址 bridge 的 BVI
		}
	}
	return s
}

// reapVPPOrphans 删除 prev 中有、desired 中无的 VPP 设备，按反依赖顺序。
func reapVPPOrphans(ctx context.Context, a *vpp.Applier, prev, desired *vpp.State) {
	// 先删成员/接口，最后删 bridge domain（BD 仍含成员时无法删除）。
	order := []string{"vxlan", "vlan", "bond", "loopback", "af-packet", "dpdk", "avf", "bridge"}
	for _, typ := range order {
		for name, info := range prev.Devices {
			if _, ok := desired.Devices[name]; ok || info.Type != typ {
				continue
			}
			if err := a.Delete(ctx, name, info); err != nil {
				slog.Warn("vpp reap orphan failed", "device", name, "type", info.Type, "error", err)
			} else {
				slog.Info("vpp removed orphan device", "device", name, "type", info.Type)
			}
		}
	}
}

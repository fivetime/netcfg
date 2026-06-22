/*
Copyright © 2024 netcfg authors

VPP applier（V1a）：把 ethernet 设备下发到 VPP——af-packet/loopback 接口创建、
up、mtu/mac、地址、路由。幂等通过接口 tag（= netcfg 配置名）关联：创建后打 tag，
再次 apply 按 tag 查到则复用，不重复创建。见 docs/vpp-backend-design.md。
*/

package vpp

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/netcfg/netcfg/config"

	govppapi "go.fd.io/govpp/api"
	af_packet "go.fd.io/govpp/binapi/af_packet"
	"go.fd.io/govpp/binapi/bond"
	"go.fd.io/govpp/binapi/dev"
	"go.fd.io/govpp/binapi/ethernet_types"
	"go.fd.io/govpp/binapi/fib_types"
	interfaces "go.fd.io/govpp/binapi/interface"
	"go.fd.io/govpp/binapi/interface_types"
	"go.fd.io/govpp/binapi/ip"
	ip6_nd "go.fd.io/govpp/binapi/ip6_nd"
	"go.fd.io/govpp/binapi/ip_types"
	"go.fd.io/govpp/binapi/l2"
	"go.fd.io/govpp/binapi/vxlan"
)

// Applier 基于 GoVPP RPC service client 下发配置。
type Applier struct {
	intf  interfaces.RPCService
	afp   af_packet.RPCService
	ipc   ip.RPCService
	l2c   l2.RPCService
	bondc bond.RPCService
	vxc   vxlan.RPCService
	devc  dev.RPCService      // VPP 设备框架（26.02 用于 iavf 等原生驱动）
	ndc   ip6_nd.RPCService   // IPv6 ND（NDP 代理）
	conn  govppapi.Connection // 供 NAT 等子模块按需创建 service client

	// 设备名 → sw_if_index 缓存（本次 apply 内，供 bond/vlan/bridge 引用其它接口）
	idx map[string]interface_types.InterfaceIndex
}

// NewApplier 用已连接的 Client 构造 applier。
func NewApplier(c *Client) *Applier {
	conn := c.Conn()
	return &Applier{
		conn:  conn,
		intf:  interfaces.NewServiceClient(conn),
		afp:   af_packet.NewServiceClient(conn),
		ipc:   ip.NewServiceClient(conn),
		l2c:   l2.NewServiceClient(conn),
		bondc: bond.NewServiceClient(conn),
		vxc:   vxlan.NewServiceClient(conn),
		devc:  dev.NewServiceClient(conn),
		ndc:   ip6_nd.NewServiceClient(conn),
		idx:   map[string]interface_types.InterfaceIndex{},
	}
}

// findByName 按 VPP 接口名查 sw_if_index（dpdk 启动期创建的接口无 tag，用名字匹配）。
func (a *Applier) findByName(ctx context.Context, name string) (interface_types.InterfaceIndex, bool, error) {
	stream, err := a.intf.SwInterfaceDump(ctx, &interfaces.SwInterfaceDump{})
	if err != nil {
		return 0, false, err
	}
	for {
		d, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, false, err
		}
		if d.InterfaceName == name {
			return d.SwIfIndex, true, nil
		}
	}
	return 0, false, nil
}

// ensureAvf 用原生驱动 iavf 接管 Intel VF（免 DPDK）。VPP 26.02 起经新设备框架
// （dev）：dev_attach(driver=iavf) + dev_create_port_if（旧 avf_create 已废弃）。
func (a *Applier) ensureAvf(ctx context.Context, name, pci string) (interface_types.InterfaceIndex, error) {
	if idx, ok, err := a.resolve(ctx, name); err != nil {
		return 0, err
	} else if ok {
		return idx, nil
	}
	att, err := a.devc.DevAttach(ctx, &dev.DevAttach{DeviceID: "pci/" + pci, DriverName: "iavf"})
	if err != nil {
		if strings.Contains(err.Error(), "unknown message") {
			return 0, fmt.Errorf("avf %s: VPP dev-framework binding mismatch — regenerate dev binapi for the target VPP: %w", name, err)
		}
		return 0, fmt.Errorf("dev attach %s (pci %s, iavf): %w", name, pci, err)
	}
	if att.Retval != 0 {
		return 0, fmt.Errorf("dev attach %s failed: %s", name, att.ErrorString)
	}
	rep, err := a.devc.DevCreatePortIf(ctx, &dev.DevCreatePortIf{
		DevIndex: att.DevIndex, IntfName: name, NumRxQueues: 1, NumTxQueues: 1, PortID: 0,
	})
	if err != nil {
		return 0, fmt.Errorf("dev create port-if %s: %w", name, err)
	}
	if rep.Retval != 0 {
		return 0, fmt.Errorf("dev create port-if %s failed: %s", name, rep.ErrorString)
	}
	idx := interface_types.InterfaceIndex(rep.SwIfIndex)
	if err := a.tagInterface(ctx, idx, name); err != nil {
		slog.Warn("failed to tag vpp avf interface", "device", name, "error", err)
	}
	a.idx[name] = idx
	return idx, nil
}

// ensureDpdk 复用 VPP 在启动期（按 startup.conf dpdk{dev{name}}）创建的接口。
// 找不到说明需用 netcfg 生成的 startup.conf 重启 VPP。
func (a *Applier) ensureDpdk(ctx context.Context, name string) (interface_types.InterfaceIndex, error) {
	if idx, ok, err := a.resolve(ctx, name); err != nil {
		return 0, err
	} else if ok {
		return idx, nil
	}
	idx, ok, err := a.findByName(ctx, name)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, fmt.Errorf("dpdk interface %q not present in VPP — apply generated /etc/vpp/startup.conf and restart VPP", name)
	}
	if err := a.tagInterface(ctx, idx, name); err != nil {
		slog.Warn("failed to tag vpp dpdk interface", "device", name, "error", err)
	}
	a.idx[name] = idx
	return idx, nil
}

// resolve 按名查 sw_if_index：先查本次缓存，再按 tag 查 VPP。
func (a *Applier) resolve(ctx context.Context, name string) (interface_types.InterfaceIndex, bool, error) {
	if i, ok := a.idx[name]; ok {
		return i, true, nil
	}
	i, ok, err := a.findByTag(ctx, name)
	if ok {
		a.idx[name] = i
	}
	return i, ok, err
}

// findByTag 按 tag 查找已存在接口的 sw_if_index（幂等用）。
func (a *Applier) findByTag(ctx context.Context, tag string) (interface_types.InterfaceIndex, bool, error) {
	stream, err := a.intf.SwInterfaceDump(ctx, &interfaces.SwInterfaceDump{})
	if err != nil {
		return 0, false, err
	}
	for {
		d, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, false, err
		}
		if d.Tag == tag {
			return d.SwIfIndex, true, nil
		}
	}
	return 0, false, nil
}

func (a *Applier) tagInterface(ctx context.Context, idx interface_types.InterfaceIndex, tag string) error {
	_, err := a.intf.SwInterfaceTagAddDel(ctx, &interfaces.SwInterfaceTagAddDel{
		IsAdd: true, SwIfIndex: idx, Tag: tag,
	})
	return err
}

// ensureAfPacket 创建（或复用）挂到内核网卡 hostIf 的 af-packet 接口。
func (a *Applier) ensureAfPacket(ctx context.Context, name, hostIf string) (interface_types.InterfaceIndex, error) {
	if idx, ok, err := a.resolve(ctx, name); err != nil {
		return 0, err
	} else if ok {
		return idx, nil
	}
	rep, err := a.afp.AfPacketCreateV3(ctx, &af_packet.AfPacketCreateV3{
		Mode:            af_packet.AF_PACKET_API_MODE_ETHERNET, // 0 非法，必须显式设
		HostIfName:      hostIf,
		UseRandomHwAddr: true,
		NumRxQueues:     1,
		NumTxQueues:     1,
	})
	if err != nil {
		return 0, fmt.Errorf("af_packet create %s (host-if %s): %w", name, hostIf, err)
	}
	if err := a.tagInterface(ctx, rep.SwIfIndex, name); err != nil {
		slog.Warn("failed to tag vpp interface", "device", name, "error", err)
	}
	a.idx[name] = rep.SwIfIndex
	return rep.SwIfIndex, nil
}

// ensureLoopback 创建（或复用）loopback 接口。
func (a *Applier) ensureLoopback(ctx context.Context, name string) (interface_types.InterfaceIndex, error) {
	if idx, ok, err := a.resolve(ctx, name); err != nil {
		return 0, err
	} else if ok {
		return idx, nil
	}
	rep, err := a.intf.CreateLoopback(ctx, &interfaces.CreateLoopback{})
	if err != nil {
		return 0, fmt.Errorf("create loopback %s: %w", name, err)
	}
	if err := a.tagInterface(ctx, rep.SwIfIndex, name); err != nil {
		slog.Warn("failed to tag vpp interface", "device", name, "error", err)
	}
	a.idx[name] = rep.SwIfIndex
	return rep.SwIfIndex, nil
}

func (a *Applier) setUp(ctx context.Context, idx interface_types.InterfaceIndex, up bool) error {
	var flags interface_types.IfStatusFlags
	if up {
		flags = interface_types.IF_STATUS_API_FLAG_ADMIN_UP
	}
	_, err := a.intf.SwInterfaceSetFlags(ctx, &interfaces.SwInterfaceSetFlags{SwIfIndex: idx, Flags: flags})
	return err
}

func (a *Applier) setMTU(ctx context.Context, idx interface_types.InterfaceIndex, mtu int) error {
	_, err := a.intf.SwInterfaceSetMtu(ctx, &interfaces.SwInterfaceSetMtu{
		SwIfIndex: idx,
		Mtu:       []uint32{uint32(mtu), 0, 0, 0}, // [L3, IP4, IP6, MPLS]；0=不改
	})
	return err
}

func (a *Applier) setMAC(ctx context.Context, idx interface_types.InterfaceIndex, mac string) error {
	m, err := ethernet_types.ParseMacAddress(mac)
	if err != nil {
		return fmt.Errorf("parse mac %q: %w", mac, err)
	}
	_, err = a.intf.SwInterfaceSetMacAddress(ctx, &interfaces.SwInterfaceSetMacAddress{SwIfIndex: idx, MacAddress: m})
	return err
}

func (a *Applier) addAddress(ctx context.Context, idx interface_types.InterfaceIndex, cidr string) error {
	pfx, err := ip_types.ParseAddressWithPrefix(cidr)
	if err != nil {
		return fmt.Errorf("parse address %q: %w", cidr, err)
	}
	_, err = a.intf.SwInterfaceAddDelAddress(ctx, &interfaces.SwInterfaceAddDelAddress{
		SwIfIndex: idx, IsAdd: true, Prefix: pfx,
	})
	// 幂等：地址已在本接口（-105 Address in use）视为已达期望，不报错。
	// "already present on another interface"（-127）是真实冲突，照常返回。
	if err != nil && strings.Contains(err.Error(), "Address in use") {
		return nil
	}
	return err
}

// addRoute 添加一条路由：to 为目的前缀（"default" → 按 via 族取默认路由），via 为下一跳，
// idx 为出接口（可选，0 表示不指定），table 为 VRF table id。
func (a *Applier) addRoute(ctx context.Context, to, via string, idx interface_types.InterfaceIndex, table uint32) error {
	nh, err := ip_types.ParseAddress(via)
	if err != nil {
		return fmt.Errorf("parse nexthop %q: %w", via, err)
	}
	proto := fib_types.FIB_API_PATH_NH_PROTO_IP4
	if nh.Af == ip_types.ADDRESS_IP6 {
		proto = fib_types.FIB_API_PATH_NH_PROTO_IP6
	}
	if strings.EqualFold(to, "default") {
		if nh.Af == ip_types.ADDRESS_IP6 {
			to = "::/0"
		} else {
			to = "0.0.0.0/0"
		}
	}
	dst, err := ip_types.ParsePrefix(to)
	if err != nil {
		return fmt.Errorf("parse route dst %q: %w", to, err)
	}
	_, err = a.ipc.IPRouteAddDel(ctx, &ip.IPRouteAddDel{
		IsAdd: true,
		Route: ip.IPRoute{
			TableID: table,
			Prefix:  dst,
			Paths: []fib_types.FibPath{{
				SwIfIndex: uint32(idx),
				Proto:     proto,
				Nh:        fib_types.FibPathNh{Address: nh.Un},
			}},
		},
	})
	return err
}

// ApplyEthernet 把一个 ethernet 设备下发到 VPP（V1a）。
func (a *Applier) ApplyEthernet(ctx context.Context, name string, e *config.Ethernet) error {
	mode := "af-packet"
	hostIf := name
	if e.VPP != nil {
		if e.VPP.Mode != "" {
			mode = strings.ToLower(e.VPP.Mode)
		}
		if e.VPP.HostIf != "" {
			hostIf = e.VPP.HostIf
		}
	}

	var idx interface_types.InterfaceIndex
	var err error
	switch mode {
	case "af-packet":
		idx, err = a.ensureAfPacket(ctx, name, hostIf)
	case "loopback":
		idx, err = a.ensureLoopback(ctx, name)
	case "avf":
		idx, err = a.ensureAvf(ctx, name, vppPCI(e))
	case "dpdk":
		idx, err = a.ensureDpdk(ctx, name)
	case "rdma", "memif", "tap":
		slog.Warn("vpp interface mode not yet implemented (deferred); skipping", "device", name, "mode", mode)
		return nil
	default:
		return fmt.Errorf("vpp device %s: unsupported mode %q", name, mode)
	}
	if err != nil {
		return err
	}
	slog.Info("vpp interface ready", "device", name, "mode", mode, "sw_if_index", idx)

	// MTU / MAC
	if e.MTU > 0 {
		if err := a.setMTU(ctx, idx, e.MTU); err != nil {
			slog.Warn("vpp set mtu failed", "device", name, "error", err)
		}
	}
	if e.MacAddress != "" {
		if err := a.setMAC(ctx, idx, e.MacAddress); err != nil {
			slog.Warn("vpp set mac failed", "device", name, "error", err)
		}
	}

	// activation-mode：off/manual 不 up（off 等价于保持 down）
	up := true
	switch strings.ToLower(e.ActivationMode) {
	case "off", "manual":
		up = false
	}
	if err := a.setUp(ctx, idx, up); err != nil {
		slog.Warn("vpp set link state failed", "device", name, "up", up, "error", err)
	}

	// 地址
	for _, addr := range e.Addresses {
		if err := a.addAddress(ctx, idx, addr.CIDR); err != nil {
			slog.Warn("vpp add address failed", "device", name, "address", addr.CIDR, "error", err)
		}
	}

	// 路由 + 默认网关
	for _, r := range e.Routes {
		if r == nil || r.To == "" || r.Via == "" {
			continue
		}
		if err := a.addRoute(ctx, r.To, r.Via, idx, uint32(r.Table)); err != nil {
			slog.Warn("vpp add route failed", "device", name, "to", r.To, "error", err)
		}
	}
	if e.Gateway4 != "" {
		if err := a.addRoute(ctx, "0.0.0.0/0", e.Gateway4, idx, 0); err != nil {
			slog.Warn("vpp add gateway4 failed", "device", name, "error", err)
		}
	}
	if e.Gateway6 != "" {
		if err := a.addRoute(ctx, "::/0", e.Gateway6, idx, 0); err != nil {
			slog.Warn("vpp add gateway6 failed", "device", name, "error", err)
		}
	}

	// VPP 设备上忽略的内核侧概念（不静默）
	if e.DHCP4 || e.DHCP6 {
		slog.Warn("dhcp not supported on VPP device; ignored (assign static or use a kernel tap)", "device", name)
	}
	if e.Nameservers != nil {
		slog.Warn("nameservers not applicable to VPP device; handle on the host", "device", name)
	}
	return nil
}

// configureL3 应用接口的 MTU/MAC/up/地址/路由（bond/vlan 共用；ethernet 内联了相同逻辑）。
func (a *Applier) configureL3(ctx context.Context, idx interface_types.InterfaceIndex, name string,
	mtu int, mac string, up bool, addrs []config.Address, routes []*config.Route) {
	if mtu > 0 {
		if err := a.setMTU(ctx, idx, mtu); err != nil {
			slog.Warn("vpp set mtu failed", "device", name, "error", err)
		}
	}
	if mac != "" {
		if err := a.setMAC(ctx, idx, mac); err != nil {
			slog.Warn("vpp set mac failed", "device", name, "error", err)
		}
	}
	if err := a.setUp(ctx, idx, up); err != nil {
		slog.Warn("vpp set link state failed", "device", name, "error", err)
	}
	for _, addr := range addrs {
		if err := a.addAddress(ctx, idx, addr.CIDR); err != nil {
			slog.Warn("vpp add address failed", "device", name, "address", addr.CIDR, "error", err)
		}
	}
	for _, r := range routes {
		if r == nil || r.To == "" || r.Via == "" {
			continue
		}
		if err := a.addRoute(ctx, r.To, r.Via, idx, uint32(r.Table)); err != nil {
			slog.Warn("vpp add route failed", "device", name, "to", r.To, "error", err)
		}
	}
}

// bondModeFromConfig 把 netplan bond mode 映射到 VPP bond mode。
func bondModeFromConfig(mode string) bond.BondMode {
	switch strings.ToLower(mode) {
	case "802.3ad", "lacp":
		return bond.BOND_API_MODE_LACP
	case "active-backup":
		return bond.BOND_API_MODE_ACTIVE_BACKUP
	case "balance-xor":
		return bond.BOND_API_MODE_XOR
	case "broadcast":
		return bond.BOND_API_MODE_BROADCAST
	default: // balance-rr / 未指定
		return bond.BOND_API_MODE_ROUND_ROBIN
	}
}

// ApplyBond 创建（或复用）VPP bond 并加入成员。
func (a *Applier) ApplyBond(ctx context.Context, name string, b *config.Bond) error {
	idx, ok, err := a.resolve(ctx, name)
	if err != nil {
		return err
	}
	if !ok {
		mode := ""
		if b.Parameters != nil {
			mode = b.Parameters.Mode
		}
		rep, err := a.bondc.BondCreate2(ctx, &bond.BondCreate2{
			Mode: bondModeFromConfig(mode),
			ID:   ^uint32(0), // 自动分配
		})
		if err != nil {
			return fmt.Errorf("bond create %s: %w", name, err)
		}
		idx = rep.SwIfIndex
		if err := a.tagInterface(ctx, idx, name); err != nil {
			slog.Warn("failed to tag vpp bond", "device", name, "error", err)
		}
		a.idx[name] = idx
	}
	slog.Info("vpp bond ready", "device", name, "sw_if_index", idx)

	// 加入成员（成员须为已存在的 VPP 接口）
	for _, m := range b.Interfaces {
		mIdx, mok, err := a.resolve(ctx, m)
		if err != nil || !mok {
			slog.Warn("vpp bond member not found in VPP; skipping", "bond", name, "member", m)
			continue
		}
		if _, err := a.bondc.BondAddMember(ctx, &bond.BondAddMember{SwIfIndex: mIdx, BondSwIfIndex: idx}); err != nil {
			if strings.Contains(err.Error(), "already exists") {
				continue // 幂等：成员已在 bond 中
			}
			slog.Warn("vpp bond add member failed", "bond", name, "member", m, "error", err)
		}
	}

	a.configureL3(ctx, idx, name, b.MTU, b.MacAddress, true, b.Addresses, b.Routes)
	return nil
}

// ApplyVlan 在父 VPP 接口上创建（或复用）dot1q sub-interface。
func (a *Applier) ApplyVlan(ctx context.Context, name string, vl *config.Vlan) error {
	idx, ok, err := a.resolve(ctx, name)
	if err != nil {
		return err
	}
	if !ok {
		parent, pok, err := a.resolve(ctx, vl.Link)
		if err != nil {
			return err
		}
		if !pok {
			return fmt.Errorf("vlan %s: parent %q not found in VPP", name, vl.Link)
		}
		rep, err := a.intf.CreateVlanSubif(ctx, &interfaces.CreateVlanSubif{
			SwIfIndex: parent, VlanID: uint32(vl.ID),
		})
		if err != nil {
			return fmt.Errorf("create vlan subif %s (parent %s vlan %d): %w", name, vl.Link, vl.ID, err)
		}
		idx = rep.SwIfIndex
		if err := a.tagInterface(ctx, idx, name); err != nil {
			slog.Warn("failed to tag vpp vlan", "device", name, "error", err)
		}
		a.idx[name] = idx
	}
	slog.Info("vpp vlan ready", "device", name, "vlan", vl.ID, "sw_if_index", idx)
	a.configureL3(ctx, idx, name, vl.MTU, vl.MacAddress, true, vl.Addresses, vl.Routes)
	return nil
}

// ApplyVxlan 创建（或复用）VXLAN 隧道（tunnels:mode:vxlan 经 Normalize 转入 Vxlans）。
func (a *Applier) ApplyVxlan(ctx context.Context, name string, vx *config.Vxlan) error {
	idx, ok, err := a.resolve(ctx, name)
	if err != nil {
		return err
	}
	if !ok {
		src, err := ip_types.ParseAddress(vx.Local)
		if err != nil {
			return fmt.Errorf("vxlan %s: parse local %q: %w", name, vx.Local, err)
		}
		dst, err := ip_types.ParseAddress(vx.Remote)
		if err != nil {
			return fmt.Errorf("vxlan %s: parse remote %q: %w", name, vx.Remote, err)
		}
		dstPort := uint16(4789)
		if vx.DestPort > 0 {
			dstPort = uint16(vx.DestPort)
		} else if vx.Port > 0 {
			dstPort = uint16(vx.Port)
		}
		rep, err := a.vxc.VxlanAddDelTunnelV3(ctx, &vxlan.VxlanAddDelTunnelV3{
			IsAdd:      true,
			Instance:   ^uint32(0),
			SrcAddress: src,
			DstAddress: dst,
			DstPort:    dstPort,
			Vni:        uint32(vx.ID),
		})
		if err != nil {
			return fmt.Errorf("vxlan create %s (vni %d): %w", name, vx.ID, err)
		}
		idx = rep.SwIfIndex
		if err := a.tagInterface(ctx, idx, name); err != nil {
			slog.Warn("failed to tag vpp vxlan", "device", name, "error", err)
		}
		a.idx[name] = idx
	}
	slog.Info("vpp vxlan ready", "device", name, "vni", vx.ID, "sw_if_index", idx)
	if err := a.setUp(ctx, idx, true); err != nil {
		slog.Warn("vpp set vxlan up failed", "device", name, "error", err)
	}
	return nil
}

// ApplyBridge 创建（或复用）bridge domain、加入成员；带地址时建 BVI loopback 承载 L3。
func (a *Applier) ApplyBridge(ctx context.Context, name string, b *config.Bridge) error {
	bdID := AutoBdID(name)
	if b.VPP != nil && b.VPP.BdID > 0 {
		bdID = uint32(b.VPP.BdID)
	}
	if _, err := a.l2c.BridgeDomainAddDel(ctx, &l2.BridgeDomainAddDel{
		BdID: bdID, Flood: true, UuFlood: true, Forward: true, Learn: true, IsAdd: true,
	}); err != nil && !strings.Contains(err.Error(), "already exists") {
		return fmt.Errorf("bridge domain %s (bd %d): %w", name, bdID, err)
	}
	slog.Info("vpp bridge domain ready", "device", name, "bd_id", bdID)

	for _, m := range b.Interfaces {
		mIdx, mok, err := a.resolve(ctx, m)
		if err != nil || !mok {
			slog.Warn("vpp bridge member not found in VPP; skipping", "bridge", name, "member", m)
			continue
		}
		if _, err := a.l2c.SwInterfaceSetL2Bridge(ctx, &l2.SwInterfaceSetL2Bridge{
			RxSwIfIndex: mIdx, BdID: bdID, PortType: l2.L2_API_PORT_TYPE_NORMAL, Enable: true,
		}); err != nil {
			slog.Warn("vpp bridge add member failed", "bridge", name, "member", m, "error", err)
		}
	}

	// 带地址 → 建 BVI loopback 承载 L3
	if len(b.Addresses) > 0 {
		bviName := name + "-bvi"
		bvi, err := a.ensureLoopback(ctx, bviName)
		if err != nil {
			return fmt.Errorf("bridge %s BVI: %w", name, err)
		}
		if _, err := a.l2c.SwInterfaceSetL2Bridge(ctx, &l2.SwInterfaceSetL2Bridge{
			RxSwIfIndex: bvi, BdID: bdID, PortType: l2.L2_API_PORT_TYPE_BVI, Enable: true,
		}); err != nil {
			slog.Warn("vpp set BVI failed", "bridge", name, "error", err)
		}
		a.configureL3(ctx, bvi, bviName, b.MTU, b.MacAddress, true, b.Addresses, b.Routes)
	}
	return nil
}

// Delete 从 VPP 移除一个孤儿设备（按记录的类型分派）。
func (a *Applier) Delete(ctx context.Context, name string, info DevInfo) error {
	switch info.Type {
	case "af-packet":
		if info.HostIf == "" {
			return nil
		}
		_, err := a.afp.AfPacketDelete(ctx, &af_packet.AfPacketDelete{HostIfName: info.HostIf})
		return err
	case "loopback":
		idx, ok, err := a.findByTag(ctx, name)
		if err != nil || !ok {
			return err
		}
		_, err = a.intf.DeleteLoopback(ctx, &interfaces.DeleteLoopback{SwIfIndex: idx})
		return err
	case "bond":
		idx, ok, err := a.findByTag(ctx, name)
		if err != nil || !ok {
			return err
		}
		_, err = a.bondc.BondDelete(ctx, &bond.BondDelete{SwIfIndex: idx})
		return err
	case "vlan":
		idx, ok, err := a.findByTag(ctx, name)
		if err != nil || !ok {
			return err
		}
		_, err = a.intf.DeleteSubif(ctx, &interfaces.DeleteSubif{SwIfIndex: idx})
		return err
	case "vxlan":
		src, _ := ip_types.ParseAddress(info.Local)
		dst, _ := ip_types.ParseAddress(info.Remote)
		port := uint16(4789)
		if info.Port > 0 {
			port = uint16(info.Port)
		}
		_, err := a.vxc.VxlanAddDelTunnelV3(ctx, &vxlan.VxlanAddDelTunnelV3{
			IsAdd: false, SrcAddress: src, DstAddress: dst, Vni: info.Vni, DstPort: port,
		})
		return err
	case "bridge":
		_, err := a.l2c.BridgeDomainAddDel(ctx, &l2.BridgeDomainAddDel{BdID: info.BdID, IsAdd: false})
		return err
	case "dpdk", "avf":
		// 独占接口由 startup.conf/硬件管理，运行态不删（移除需改 startup.conf 重启 VPP）
		return nil
	}
	return nil
}

// vppPCI 取设备 vpp 块的 PCI（avf/dpdk 用）。
func vppPCI(e *config.Ethernet) string {
	if e.VPP != nil {
		return e.VPP.PCI
	}
	return ""
}

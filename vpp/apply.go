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

	af_packet "go.fd.io/govpp/binapi/af_packet"
	"go.fd.io/govpp/binapi/ethernet_types"
	"go.fd.io/govpp/binapi/fib_types"
	interfaces "go.fd.io/govpp/binapi/interface"
	"go.fd.io/govpp/binapi/interface_types"
	"go.fd.io/govpp/binapi/ip"
	"go.fd.io/govpp/binapi/ip_types"
)

// Applier 基于 GoVPP RPC service client 下发配置。
type Applier struct {
	intf interfaces.RPCService
	afp  af_packet.RPCService
	ipc  ip.RPCService
}

// NewApplier 用已连接的 Client 构造 applier。
func NewApplier(c *Client) *Applier {
	conn := c.Conn()
	return &Applier{
		intf: interfaces.NewServiceClient(conn),
		afp:  af_packet.NewServiceClient(conn),
		ipc:  ip.NewServiceClient(conn),
	}
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
	if idx, ok, err := a.findByTag(ctx, name); err != nil {
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
	return rep.SwIfIndex, nil
}

// ensureLoopback 创建（或复用）loopback 接口。
func (a *Applier) ensureLoopback(ctx context.Context, name string) (interface_types.InterfaceIndex, error) {
	if idx, ok, err := a.findByTag(ctx, name); err != nil {
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
	case "dpdk", "avf", "rdma", "memif", "tap":
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

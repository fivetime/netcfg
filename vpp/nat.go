/*
Copyright © 2024 netcfg authors

VPP NAT applier：nat44-ed（SNAT/masquerade/地址池/静态映射）、nat64、nat66。
netcfg 扩展（netplan 无对应），配置在 vpp.nat。见 docs/vpp-backend-design.md。
*/

package vpp

import (
	"context"
	"log/slog"
	"strings"

	"github.com/netcfg/netcfg/config"

	"go.fd.io/govpp/binapi/interface_types"
	"go.fd.io/govpp/binapi/ip_types"
	"go.fd.io/govpp/binapi/nat44_ed"
	"go.fd.io/govpp/binapi/nat64"
	"go.fd.io/govpp/binapi/nat66"
	"go.fd.io/govpp/binapi/nat_types"
)

// ApplyNat 应用 VPP NAT 配置（nat44/nat64/nat66）。
func (a *Applier) ApplyNat(ctx context.Context, n *config.VPPNat) {
	if n.Nat44 != nil {
		a.applyNat44(ctx, n.Nat44)
	}
	if n.Nat64 != nil {
		a.applyNat64(ctx, n.Nat64)
	}
	if n.Nat66 != nil {
		a.applyNat66(ctx, n.Nat66)
	}
}

func natProto(p string) uint8 {
	switch strings.ToLower(p) {
	case "tcp":
		return 6
	case "udp":
		return 17
	case "icmp":
		return 1
	}
	return 0
}

// swallowExists 把 "already exists/in use" 类错误当幂等成功。
func swallowExists(err error) error {
	if err == nil {
		return nil
	}
	e := err.Error()
	// 幂等：已存在/已启用/已在用 都视为已达期望状态。
	if strings.Contains(e, "exists") || strings.Contains(e, "already") ||
		strings.Contains(e, "in use") || strings.Contains(e, "VALUE_EXIST") {
		return nil
	}
	return err
}

func (a *Applier) applyNat44(ctx context.Context, n *config.Nat44) {
	if strings.EqualFold(n.Mode, "ei") {
		slog.Warn("nat44 mode 'ei' (endpoint-independent) not implemented; using endpoint-dependent (ed)")
	}
	if n.Enable != nil && !*n.Enable {
		return // 显式禁用：不启用插件（回收交给 N-5）
	}
	c := nat44_ed.NewServiceClient(a.conn)
	if _, err := c.Nat44EdPluginEnableDisable(ctx, &nat44_ed.Nat44EdPluginEnableDisable{
		Enable:     true,
		Sessions:   uint32(n.Sessions),
		InsideVrf:  uint32(n.InsideVRF),
		OutsideVrf: uint32(n.OutsideVRF),
	}); swallowExists(err) != nil {
		slog.Error("nat44 plugin enable failed", "error", err)
		return
	}
	slog.Info("nat44 enabled", "sessions", n.Sessions)

	// 接口角色
	for _, iface := range n.Interfaces {
		idx, ok, err := a.resolve(ctx, iface.Name)
		if err != nil || !ok {
			slog.Warn("nat44 interface not found in VPP; skipping", "interface", iface.Name)
			continue
		}
		switch strings.ToLower(iface.Role) {
		case "inside":
			_, err = c.Nat44InterfaceAddDelFeature(ctx, &nat44_ed.Nat44InterfaceAddDelFeature{
				IsAdd: true, Flags: nat_types.NAT_IS_INSIDE, SwIfIndex: idx})
		case "outside":
			_, err = c.Nat44InterfaceAddDelFeature(ctx, &nat44_ed.Nat44InterfaceAddDelFeature{
				IsAdd: true, Flags: nat_types.NAT_IS_OUTSIDE, SwIfIndex: idx})
		case "output":
			_, err = c.Nat44EdAddDelOutputInterface(ctx, &nat44_ed.Nat44EdAddDelOutputInterface{
				IsAdd: true, SwIfIndex: idx})
		default:
			slog.Warn("nat44 unknown interface role", "interface", iface.Name, "role", iface.Role)
			continue
		}
		if swallowExists(err) != nil {
			slog.Warn("nat44 interface feature failed", "interface", iface.Name, "role", iface.Role, "error", err)
		}
	}

	// SNAT 地址池
	for _, p := range n.Pools {
		first, err := ip_types.ParseIP4Address(p.Start)
		if err != nil {
			slog.Warn("nat44 pool: bad start", "start", p.Start, "error", err)
			continue
		}
		last := first
		if p.End != "" {
			if last, err = ip_types.ParseIP4Address(p.End); err != nil {
				slog.Warn("nat44 pool: bad end", "end", p.End, "error", err)
				continue
			}
		}
		var flags nat_types.NatConfigFlags
		if p.TwiceNat != nil && *p.TwiceNat {
			flags |= nat_types.NAT_IS_TWICE_NAT
		}
		if _, err := c.Nat44AddDelAddressRange(ctx, &nat44_ed.Nat44AddDelAddressRange{
			FirstIPAddress: first, LastIPAddress: last, VrfID: uint32(p.VRF), IsAdd: true, Flags: flags,
		}); swallowExists(err) != nil {
			slog.Warn("nat44 add address range failed", "start", p.Start, "error", err)
		}
	}

	// 静态映射 / 端口转发
	for _, s := range n.Static {
		a.applyNat44Static(ctx, c, s)
	}
}

func (a *Applier) applyNat44Static(ctx context.Context, c nat44_ed.RPCService, s *config.NatStatic) {
	local, err := ip_types.ParseIP4Address(s.Local)
	if err != nil {
		slog.Warn("nat44 static: bad local", "local", s.Local, "error", err)
		return
	}
	m := &nat44_ed.Nat44AddDelStaticMapping{
		IsAdd:             true,
		LocalIPAddress:    local,
		Protocol:          natProto(s.Proto),
		LocalPort:         uint16(s.LocalPort),
		ExternalPort:      uint16(s.ExternalPort),
		ExternalSwIfIndex: ^interface_types.InterfaceIndex(0), // 0xffffffff=无接口（用 external IP）；0 是 local0
		VrfID:             uint32(s.VRF),
	}
	var flags nat_types.NatConfigFlags
	if s.Proto == "" { // 1:1 地址映射
		flags |= nat_types.NAT_IS_ADDR_ONLY
	}
	if s.TwiceNat != nil && *s.TwiceNat {
		flags |= nat_types.NAT_IS_TWICE_NAT
	}
	m.Flags = flags

	if s.ExternalInterface != "" {
		idx, ok, err := a.resolve(ctx, s.ExternalInterface)
		if err != nil || !ok {
			slog.Warn("nat44 static: external-interface not found", "interface", s.ExternalInterface)
			return
		}
		m.ExternalSwIfIndex = idx
	} else if s.External != "" {
		ext, err := ip_types.ParseIP4Address(s.External)
		if err != nil {
			slog.Warn("nat44 static: bad external", "external", s.External, "error", err)
			return
		}
		m.ExternalIPAddress = ext
	} else {
		slog.Warn("nat44 static: need external or external-interface", "local", s.Local)
		return
	}

	if _, err := c.Nat44AddDelStaticMapping(ctx, m); swallowExists(err) != nil {
		slog.Warn("nat44 add static mapping failed", "local", s.Local, "error", err)
		return
	}
	slog.Info("nat44 static mapping added", "local", s.Local, "external", s.External+s.ExternalInterface)
}

func (a *Applier) applyNat64(ctx context.Context, n *config.Nat64) {
	if n.Enable != nil && !*n.Enable {
		return
	}
	c := nat64.NewServiceClient(a.conn)
	if _, err := c.Nat64PluginEnableDisable(ctx, &nat64.Nat64PluginEnableDisable{Enable: true}); swallowExists(err) != nil {
		slog.Error("nat64 plugin enable failed", "error", err)
		return
	}
	if n.Prefix != "" {
		pfx, err := ip_types.ParseIP6Prefix(n.Prefix)
		if err != nil {
			slog.Warn("nat64 bad prefix", "prefix", n.Prefix, "error", err)
		} else if _, err := c.Nat64AddDelPrefix(ctx, &nat64.Nat64AddDelPrefix{Prefix: pfx, IsAdd: true}); swallowExists(err) != nil {
			slog.Warn("nat64 add prefix failed", "error", err)
		}
	}
	for _, iface := range n.Interfaces {
		idx, ok, err := a.resolve(ctx, iface.Name)
		if err != nil || !ok {
			slog.Warn("nat64 interface not found", "interface", iface.Name)
			continue
		}
		var flags nat_types.NatConfigFlags
		if strings.EqualFold(iface.Role, "inside") {
			flags = nat_types.NAT_IS_INSIDE
		} else {
			flags = nat_types.NAT_IS_OUTSIDE
		}
		if _, err := c.Nat64AddDelInterface(ctx, &nat64.Nat64AddDelInterface{IsAdd: true, Flags: flags, SwIfIndex: idx}); swallowExists(err) != nil {
			slog.Warn("nat64 interface failed", "interface", iface.Name, "error", err)
		}
	}
	for _, p := range n.Pools {
		start, err := ip_types.ParseIP4Address(p.Start)
		if err != nil {
			continue
		}
		end := start
		if p.End != "" {
			end, _ = ip_types.ParseIP4Address(p.End)
		}
		if _, err := c.Nat64AddDelPoolAddrRange(ctx, &nat64.Nat64AddDelPoolAddrRange{
			StartAddr: start, EndAddr: end, VrfID: uint32(p.VRF), IsAdd: true,
		}); swallowExists(err) != nil {
			slog.Warn("nat64 pool failed", "start", p.Start, "error", err)
		}
	}
	slog.Info("nat64 enabled", "prefix", n.Prefix)
}

func (a *Applier) applyNat66(ctx context.Context, n *config.Nat66) {
	c := nat66.NewServiceClient(a.conn)
	if _, err := c.Nat66PluginEnableDisable(ctx, &nat66.Nat66PluginEnableDisable{Enable: true}); swallowExists(err) != nil {
		slog.Error("nat66 plugin enable failed", "error", err)
		return
	}
	for _, s := range n.Static {
		local, err := ip_types.ParseIP6Address(s.Local)
		if err != nil {
			slog.Warn("nat66 static bad local", "local", s.Local, "error", err)
			continue
		}
		ext, err := ip_types.ParseIP6Address(s.External)
		if err != nil {
			slog.Warn("nat66 static bad external", "external", s.External, "error", err)
			continue
		}
		if _, err := c.Nat66AddDelStaticMapping(ctx, &nat66.Nat66AddDelStaticMapping{
			IsAdd: true, LocalIPAddress: local, ExternalIPAddress: ext, VrfID: uint32(s.VRF),
		}); swallowExists(err) != nil {
			slog.Warn("nat66 static failed", "local", s.Local, "error", err)
		}
	}
	slog.Info("nat66 enabled", "mappings", len(n.Static))
}

// DeleteNat 删除一条 NAT 规则（增量回收用，IsAdd=false）。
func (a *Applier) DeleteNat(ctx context.Context, it NatItem) error {
	switch it.Kind {
	case "nat44-if":
		idx, ok, err := a.resolve(ctx, it.Iface)
		if err != nil || !ok {
			return err
		}
		c := nat44_ed.NewServiceClient(a.conn)
		switch strings.ToLower(it.Role) {
		case "output":
			_, err = c.Nat44EdAddDelOutputInterface(ctx, &nat44_ed.Nat44EdAddDelOutputInterface{IsAdd: false, SwIfIndex: idx})
		case "inside":
			_, err = c.Nat44InterfaceAddDelFeature(ctx, &nat44_ed.Nat44InterfaceAddDelFeature{IsAdd: false, Flags: nat_types.NAT_IS_INSIDE, SwIfIndex: idx})
		default:
			_, err = c.Nat44InterfaceAddDelFeature(ctx, &nat44_ed.Nat44InterfaceAddDelFeature{IsAdd: false, Flags: nat_types.NAT_IS_OUTSIDE, SwIfIndex: idx})
		}
		return err
	case "nat44-pool":
		first, err := ip_types.ParseIP4Address(it.Start)
		if err != nil {
			return err
		}
		last := first
		if it.End != "" {
			last, _ = ip_types.ParseIP4Address(it.End)
		}
		var flags nat_types.NatConfigFlags
		if it.TwiceNat {
			flags |= nat_types.NAT_IS_TWICE_NAT
		}
		c := nat44_ed.NewServiceClient(a.conn)
		_, err = c.Nat44AddDelAddressRange(ctx, &nat44_ed.Nat44AddDelAddressRange{
			FirstIPAddress: first, LastIPAddress: last, VrfID: uint32(it.VRF), IsAdd: false, Flags: flags})
		return err
	case "nat44-static":
		local, err := ip_types.ParseIP4Address(it.Local)
		if err != nil {
			return err
		}
		m := &nat44_ed.Nat44AddDelStaticMapping{
			IsAdd: false, LocalIPAddress: local, Protocol: natProto(it.Proto),
			LocalPort: uint16(it.LocalPort), ExternalPort: uint16(it.ExternalPort),
			ExternalSwIfIndex: ^interface_types.InterfaceIndex(0), VrfID: uint32(it.VRF),
		}
		var flags nat_types.NatConfigFlags
		if it.Proto == "" {
			flags |= nat_types.NAT_IS_ADDR_ONLY
		}
		if it.TwiceNat {
			flags |= nat_types.NAT_IS_TWICE_NAT
		}
		m.Flags = flags
		if it.ExternalIf != "" {
			idx, ok, err := a.resolve(ctx, it.ExternalIf)
			if err != nil || !ok {
				return err
			}
			m.ExternalSwIfIndex = idx
		} else if it.External != "" {
			if m.ExternalIPAddress, err = ip_types.ParseIP4Address(it.External); err != nil {
				return err
			}
		}
		c := nat44_ed.NewServiceClient(a.conn)
		_, err = c.Nat44AddDelStaticMapping(ctx, m)
		return err
	case "nat64-if":
		idx, ok, err := a.resolve(ctx, it.Iface)
		if err != nil || !ok {
			return err
		}
		flags := nat_types.NAT_IS_OUTSIDE
		if strings.EqualFold(it.Role, "inside") {
			flags = nat_types.NAT_IS_INSIDE
		}
		c := nat64.NewServiceClient(a.conn)
		_, err = c.Nat64AddDelInterface(ctx, &nat64.Nat64AddDelInterface{IsAdd: false, Flags: flags, SwIfIndex: idx})
		return err
	case "nat64-prefix":
		pfx, err := ip_types.ParseIP6Prefix(it.Prefix)
		if err != nil {
			return err
		}
		c := nat64.NewServiceClient(a.conn)
		_, err = c.Nat64AddDelPrefix(ctx, &nat64.Nat64AddDelPrefix{Prefix: pfx, VrfID: uint32(it.VRF), IsAdd: false})
		return err
	case "nat64-pool":
		start, err := ip_types.ParseIP4Address(it.Start)
		if err != nil {
			return err
		}
		end := start
		if it.End != "" {
			end, _ = ip_types.ParseIP4Address(it.End)
		}
		c := nat64.NewServiceClient(a.conn)
		_, err = c.Nat64AddDelPoolAddrRange(ctx, &nat64.Nat64AddDelPoolAddrRange{StartAddr: start, EndAddr: end, VrfID: uint32(it.VRF), IsAdd: false})
		return err
	case "nat66-static":
		local, err := ip_types.ParseIP6Address(it.Local)
		if err != nil {
			return err
		}
		ext, err := ip_types.ParseIP6Address(it.External)
		if err != nil {
			return err
		}
		c := nat66.NewServiceClient(a.conn)
		_, err = c.Nat66AddDelStaticMapping(ctx, &nat66.Nat66AddDelStaticMapping{IsAdd: false, LocalIPAddress: local, ExternalIPAddress: ext, VrfID: uint32(it.VRF)})
		return err
	}
	return nil
}

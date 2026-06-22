/*
Copyright © 2024 netcfg authors

内核态 SRv6 (seg6)：seg6_enabled sysctl + 本地 SID（seg6local）下发。
见 docs/srv6-design.md。底层用 vishvananda/netlink 的 SEG6LocalEncap（含 VrfTable）。
*/

package netlink

import (
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netlink/nl"
)

// EnableSeg6 设置 net.ipv6.conf.<iface>.seg6_enabled=1（iface 可为 "all"）。
func (m *NetlinkManager) EnableSeg6(iface string) error {
	return writeSysctl(iface, "seg6_enabled", "1")
}

// EnableVRFStrictMode 设置 net.vrf.strict_mode=1。
// seg6local End.DT4/DT46 经 vrftable 解封时内核要求开启（否则 EPERM "Strict mode for VRF is disabled"）。
func (m *NetlinkManager) EnableVRFStrictMode() error {
	return os.WriteFile("/proc/sys/net/vrf/strict_mode", []byte("1"), 0644)
}

// SRv6LocalSIDOpts 描述一条本地 SID（seg6local endpoint）。
type SRv6LocalSIDOpts struct {
	SID      string // IPv6（带或不带 /128）
	Action   string // End / End.X / End.DT6 ...
	Table    int
	VRFTable int
	NH4      string
	NH6      string
	IIF      string
	OIF      string
	Segments []string
}

// srv6ActionCode 把 action 字符串映射到 nl 常量。
var srv6ActionCode = map[string]int{
	"End":           nl.SEG6_LOCAL_ACTION_END,
	"End.X":         nl.SEG6_LOCAL_ACTION_END_X,
	"End.T":         nl.SEG6_LOCAL_ACTION_END_T,
	"End.DX2":       nl.SEG6_LOCAL_ACTION_END_DX2,
	"End.DX4":       nl.SEG6_LOCAL_ACTION_END_DX4,
	"End.DX6":       nl.SEG6_LOCAL_ACTION_END_DX6,
	"End.DT4":       nl.SEG6_LOCAL_ACTION_END_DT4,
	"End.DT6":       nl.SEG6_LOCAL_ACTION_END_DT6,
	"End.DT46":      nl.SEG6_LOCAL_ACTION_END_DT46,
	"End.B6":        nl.SEG6_LOCAL_ACTION_END_B6,
	"End.B6.Encaps": nl.SEG6_LOCAL_ACTION_END_B6_ENCAPS,
}

// sidToIPNet 把 SID 字符串（带或不带前缀）解析为 /128 的 *net.IPNet。
func sidToIPNet(sid string) (*net.IPNet, error) {
	s := sid
	if i := strings.IndexByte(s, '/'); i >= 0 {
		s = s[:i]
	}
	ip := net.ParseIP(s)
	if ip == nil || ip.To4() != nil {
		return nil, fmt.Errorf("invalid IPv6 SID %q", sid)
	}
	return &net.IPNet{IP: ip, Mask: net.CIDRMask(128, 128)}, nil
}

// AddLocalSID 幂等下发一条本地 SID（seg6local 路由，RouteReplace），锚定在 dev 上。
// 关键1：SEG6LocalEncap 靠 Flags 数组驱动，每个填的字段都要置位，漏置静默丢弃。
// 关键2：seg6local 必须挂在真实设备上——内核会静默丢弃 loopback(lo) 上的 seg6local 封装。
func (m *NetlinkManager) AddLocalSID(dev string, o *SRv6LocalSIDOpts) error {
	code, ok := srv6ActionCode[o.Action]
	if !ok {
		return fmt.Errorf("unknown seg6local action %q", o.Action)
	}
	dst, err := sidToIPNet(o.SID)
	if err != nil {
		return err
	}

	var flags [nl.SEG6_LOCAL_MAX]bool
	flags[nl.SEG6_LOCAL_ACTION] = true
	enc := &netlink.SEG6LocalEncap{Action: code}

	if o.Table > 0 {
		enc.Table = o.Table
		flags[nl.SEG6_LOCAL_TABLE] = true
	}
	if o.VRFTable > 0 {
		enc.VrfTable = o.VRFTable
		flags[nl.SEG6_LOCAL_VRFTABLE] = true
	}
	if o.NH4 != "" {
		ip := net.ParseIP(o.NH4)
		if ip == nil || ip.To4() == nil {
			return fmt.Errorf("invalid nh4 %q", o.NH4)
		}
		enc.InAddr = ip.To4()
		flags[nl.SEG6_LOCAL_NH4] = true
	}
	if o.NH6 != "" {
		ip := net.ParseIP(o.NH6)
		if ip == nil {
			return fmt.Errorf("invalid nh6 %q", o.NH6)
		}
		enc.In6Addr = ip
		flags[nl.SEG6_LOCAL_NH6] = true
	}
	if o.OIF != "" {
		link, err := m.handle.LinkByName(o.OIF)
		if err != nil {
			return fmt.Errorf("seg6local oif %s: %w", o.OIF, err)
		}
		enc.Oif = link.Attrs().Index
		flags[nl.SEG6_LOCAL_OIF] = true
	}
	if o.IIF != "" {
		link, err := m.handle.LinkByName(o.IIF)
		if err != nil {
			return fmt.Errorf("seg6local iif %s: %w", o.IIF, err)
		}
		enc.Iif = link.Attrs().Index
		flags[nl.SEG6_LOCAL_IIF] = true
	}
	if len(o.Segments) > 0 {
		segs := make([]net.IP, 0, len(o.Segments))
		for _, s := range o.Segments {
			ip := net.ParseIP(s)
			if ip == nil {
				return fmt.Errorf("invalid segment %q", s)
			}
			segs = append(segs, ip)
		}
		enc.Segments = segs
		flags[nl.SEG6_LOCAL_SRH] = true
	}
	enc.Flags = flags

	// 锚定设备（必须真实设备，非 lo）
	link, err := m.handle.LinkByName(dev)
	if err != nil {
		return fmt.Errorf("seg6local %s: resolve dev %s: %w", o.SID, dev, err)
	}

	return m.handle.RouteReplace(&netlink.Route{
		LinkIndex: link.Attrs().Index,
		Dst:       dst,
		Encap:     enc,
	})
}

// DeleteLocalSID 删除一条本地 SID（按 /128 dst）。
func (m *NetlinkManager) DeleteLocalSID(sid string) error {
	dst, err := sidToIPNet(sid)
	if err != nil {
		return err
	}
	return m.handle.RouteDel(&netlink.Route{Dst: dst})
}

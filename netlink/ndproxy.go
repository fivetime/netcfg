/*
Copyright © 2024 netcfg authors

内核态 NDP 代理（IPv6 邻居发现代理）：proxy_ndp sysctl + NTF_PROXY 邻居条目，
等价 `sysctl net.ipv6.conf.<dev>.proxy_ndp=1` + `ip -6 neigh add proxy <ip> dev <dev>`。
netcfg 扩展（非 netplan），配置在设备的 nd-proxy 列表。
*/

package netlink

import (
	"fmt"
	"net"

	"github.com/vishvananda/netlink"
)

// EnableProxyNDP 开启接口的 net.ipv6.conf.<iface>.proxy_ndp。
func (m *NetlinkManager) EnableProxyNDP(iface string) error {
	return writeSysctl(iface, "proxy_ndp", "1")
}

// SetAllmulti 开/关接口的 all-multicast（收全 IPv6 组播，供 NDP 响应器收任意
// solicited-node NS）。
func (m *NetlinkManager) SetAllmulti(iface string, on bool) error {
	link, err := m.handle.LinkByName(iface)
	if err != nil {
		return fmt.Errorf("allmulti %s: %w", iface, err)
	}
	if on {
		return m.handle.LinkSetAllmulticastOn(link)
	}
	return m.handle.LinkSetAllmulticastOff(link)
}

// SetAlias 设接口的 ifalias（`ip -d link show` 可见），用于给 netcfg 托管的
// NDP tap 打上「managed, do not delete」提示。接口不存在时报错由调用方决定处理。
func (m *NetlinkManager) SetAlias(iface, alias string) error {
	link, err := m.handle.LinkByName(iface)
	if err != nil {
		return fmt.Errorf("alias %s: %w", iface, err)
	}
	return m.handle.LinkSetAlias(link, alias)
}

// proxyNDPNeigh 构造一条 IPv6 NDP 代理邻居条目。
func (m *NetlinkManager) proxyNDPNeigh(iface, ipStr string) (*netlink.Neigh, error) {
	link, err := m.handle.LinkByName(iface)
	if err != nil {
		return nil, err
	}
	ip := net.ParseIP(ipStr)
	if ip == nil || ip.To4() != nil {
		return nil, fmt.Errorf("nd-proxy: %q is not a valid IPv6 address", ipStr)
	}
	return &netlink.Neigh{
		LinkIndex: link.Attrs().Index,
		Family:    netlink.FAMILY_V6,
		IP:        ip,
		Flags:     netlink.NTF_PROXY,
	}, nil
}

// AddProxyNDP 幂等添加一条 IPv6 NDP 代理条目（NeighSet=REPLACE）。
func (m *NetlinkManager) AddProxyNDP(iface, ipStr string) error {
	neigh, err := m.proxyNDPNeigh(iface, ipStr)
	if err != nil {
		return fmt.Errorf("nd-proxy add %s on %s: %w", ipStr, iface, err)
	}
	return m.handle.NeighSet(neigh)
}

// DeleteProxyNDP 删除一条 IPv6 NDP 代理条目；接口已不存在时视为已移除。
func (m *NetlinkManager) DeleteProxyNDP(iface, ipStr string) error {
	neigh, err := m.proxyNDPNeigh(iface, ipStr)
	if err != nil {
		// 接口已删除 → 条目随之消失；地址非法才算错误
		if _, e := m.handle.LinkByName(iface); e != nil {
			return nil
		}
		return err
	}
	return m.handle.NeighDel(neigh)
}

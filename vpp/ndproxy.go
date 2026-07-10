/*
Copyright © 2024 netcfg authors

VPP NDP 代理（IPv6 邻居发现代理）：在接口上为指定 IPv6 地址应答 NS（ip6nd_proxy，
逐 /128、本接口 MAC）。等价 CLI `set ip6 nd <intf> proxy <addr>`。
配置来自设备统一 ndp-proxy 块的 addresses（与内核 proxy_ndp 同语义）；
rules（按前缀/外部 MAC 代答）VPP 数据面无对应机制，在 cmd 层告警忽略。
见 docs/ndp-responder-design.md。
*/

package vpp

import (
	"context"
	"fmt"

	"go.fd.io/govpp/binapi/ip6_nd"
	"go.fd.io/govpp/binapi/ip_types"
)

// ApplyNDProxy 在接口 iface 上为每个 IPv6 地址添加 NDP 代理条目（幂等）。
func (a *Applier) ApplyNDProxy(ctx context.Context, iface string, addrs []string) error {
	if len(addrs) == 0 {
		return nil
	}
	idx, ok, err := a.resolve(ctx, iface)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("nd-proxy: interface %q not found in VPP", iface)
	}
	for _, s := range addrs {
		ip, err := ip_types.ParseIP6Address(s)
		if err != nil {
			return fmt.Errorf("nd-proxy: invalid IPv6 %q: %w", s, err)
		}
		_, err = a.ndc.IP6ndProxyAddDel(ctx, &ip6_nd.IP6ndProxyAddDel{
			SwIfIndex: idx, IsAdd: true, IP: ip,
		})
		if err := swallowExists(err); err != nil {
			return fmt.Errorf("nd-proxy add %s on %s: %w", s, iface, err)
		}
	}
	return nil
}

// DeleteNDProxy 删除一条 NDP 代理条目。接口已不存在时视为已随接口移除。
func (a *Applier) DeleteNDProxy(ctx context.Context, item NDProxyItem) error {
	idx, ok, err := a.resolve(ctx, item.Iface)
	if err != nil {
		return err
	}
	if !ok {
		return nil // 接口已删除，代理条目随之消失
	}
	ip, err := ip_types.ParseIP6Address(item.IP)
	if err != nil {
		return fmt.Errorf("nd-proxy del: invalid IPv6 %q: %w", item.IP, err)
	}
	_, err = a.ndc.IP6ndProxyAddDel(ctx, &ip6_nd.IP6ndProxyAddDel{
		SwIfIndex: idx, IsAdd: false, IP: ip,
	})
	return err
}

/*
Copyright © 2024 netcfg authors

VPP NDP 代答 tap：VPP 数据面无法做「按前缀 + 外部 MAC」的 ND 代答（ip6nd_proxy 只逐 /128、
本接口 MAC）。对归 VPP 管的 bridge 上带外部 MAC 静态 rules 的 ndp-proxy 块，netcfg 往该
bridge 的 bridge-domain 生一根内核 tap：线上的 NS 经 BD 泛洪到达 tap，netcfg daemon 的
纯 Go 响应器在 tap 上代答（TLLA=外部 MAC），NA 经 BD 转回线路。响应器发帧的二层源 MAC
是 tap 自己的 MAC（cooked 模式），外部 MAC 只在 NDP 载荷里，故 BD 不会学错。
见 docs/ndp-responder-design.md、docs/vpp-backend-design.md。
*/

package vpp

import (
	"context"
	"fmt"
	"hash/fnv"
	"log/slog"

	"go.fd.io/govpp/binapi/l2"
	"go.fd.io/govpp/binapi/tapv2"
)

// NDPTapName 从 bridge 名派生确定性的 tap 内核名（≤ IFNAMSIZ 15）。apply 与 daemon
// 各自独立算出同一个名字，无需共享状态。名字刻意短，人类可读的说明放在 ifalias 里。
func NDPTapName(bridge string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(bridge))
	return fmt.Sprintf("ncndp%08x", h.Sum32()) // 5 + 8 = 13 字符
}

// EnsureNDPTap 幂等地创建（或复用）挂在 bridge 的 BD 上的 NDP 代答 tap，返回其内核名。
// tag = 内核名（确定性、唯一），供幂等复用与回收。
func (a *Applier) EnsureNDPTap(ctx context.Context, bridge string, bdID uint32) (string, error) {
	hostIf := NDPTapName(bridge)

	idx, ok, err := a.findByTag(ctx, hostIf)
	if err != nil {
		return "", err
	}
	if !ok {
		rep, err := a.tapc.TapCreateV3(ctx, &tapv2.TapCreateV3{
			ID:            ^uint32(0), // 自动分配
			UseRandomMac:  true,
			NumRxQueues:   1,
			NumTxQueues:   1,
			HostIfNameSet: true,
			HostIfName:    hostIf,
			Tag:           hostIf,
		})
		if err != nil {
			return "", fmt.Errorf("ndp-tap create %s (bridge %s): %w", hostIf, bridge, err)
		}
		idx = rep.SwIfIndex
	}

	// 加进 bridge 的 BD（幂等：已在则 VPP 视为设置成功）。
	if _, err := a.l2c.SwInterfaceSetL2Bridge(ctx, &l2.SwInterfaceSetL2Bridge{
		RxSwIfIndex: idx, BdID: bdID, PortType: l2.L2_API_PORT_TYPE_NORMAL, Enable: true,
	}); err != nil {
		return "", fmt.Errorf("ndp-tap %s join bd %d: %w", hostIf, bdID, err)
	}
	if err := a.setUp(ctx, idx, true); err != nil {
		slog.Warn("vpp ndp-tap set up failed", "tap", hostIf, "error", err)
	}
	return hostIf, nil
}

// DeleteNDPTap 删除一根 NDP 代答 tap（内核侧 netdev 随之消失）。已不存在时视为已移除。
func (a *Applier) DeleteNDPTap(ctx context.Context, hostIf string) error {
	idx, ok, err := a.findByTag(ctx, hostIf)
	if err != nil || !ok {
		return err
	}
	_, err = a.tapc.TapDeleteV2(ctx, &tapv2.TapDeleteV2{SwIfIndex: idx})
	return err
}

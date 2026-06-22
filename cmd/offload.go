/*
Copyright © 2024 netcfg authors

网卡 offload / Wake-on-LAN：经 ethtool ioctl（netlink 层封装 github.com/safchain/ethtool，
纯 Go，无需 ethtool 命令）。offload 的 netplan 字段 → 内核 feature 名映射在此完成；group 类
（tx-checksum/tso）按设备实际 feature 列表展开，等价 ethtool CLI 的分组行为。
*/

package cmd

import (
	"log/slog"
	"os"
	"strings"

	"github.com/netcfg/netcfg/config"
	nl "github.com/netcfg/netcfg/netlink"
)

// applyEthernetExtras 应用其它物理网卡杂项：wakeonlan / infiniband-mode / emit-lldp。
func applyEthernetExtras(mgr *nl.NetlinkManager, name string, cfg *config.Ethernet) {
	if cfg.Wakeonlan {
		if err := mgr.SetWakeOnLanMagic(name); err != nil {
			slog.Warn("failed to set wakeonlan", "device", name, "error", err)
		} else {
			slog.Info("enabled wake-on-lan", "device", name)
		}
	}
	if cfg.InfinibandMode != "" {
		applyInfinibandMode(name, cfg.InfinibandMode)
	}
	if cfg.EmitLLDP != nil && *cfg.EmitLLDP {
		// netplan 自身亦注明 emit-lldp 仅 networkd 后端支持；内核无 LLDP 发送开关，
		// 需 LLDP daemon。netcfg 不实现，显式告警避免静默。
		slog.Warn("emit-lldp not honored: requires an LLDP daemon (e.g. networkd/lldpd); netcfg does not emit LLDP",
			"device", name)
	}
}

// applyInfinibandMode 经 sysfs 设置 IPoIB 模式（connected/datagram）。
func applyInfinibandMode(name, mode string) {
	path := "/sys/class/net/" + name + "/mode"
	if err := os.WriteFile(path, []byte(mode), 0644); err != nil {
		slog.Warn("failed to set infiniband-mode (not an IPoIB device?)",
			"device", name, "mode", mode, "error", err)
		return
	}
	slog.Info("set infiniband mode", "device", name, "mode", mode)
}

// applyOffload 按 cfg 中已设置（非 nil）的 offload 字段经 ethtool ioctl 下发。
// netplan 字段 → 内核 feature 名：单 feature 直接匹配；group（tx-checksum/tso）按设备
// 实际拥有的 feature 名展开（等价 ethtool -K 的分组），只设设备真实存在的 feature。
func applyOffload(mgr *nl.NetlinkManager, name string, cfg *config.Ethernet) {
	rules := []struct {
		val   *bool
		match func(string) bool
	}{
		{cfg.ReceiveChecksumOffload, eqFeat("rx-checksum")},
		{cfg.TransmitChecksumOffload, prefixFeat("tx-checksum-")}, // group
		{cfg.TCPSegmentationOffload, inFeat( // tso（IPv4 各变体）
			"tx-tcp-segmentation", "tx-tcp-ecn-segmentation", "tx-tcp-mangleid-segmentation")},
		{cfg.TCP6SegmentationOffload, eqFeat("tx-tcp6-segmentation")},
		{cfg.GenericSegmentationOffload, eqFeat("tx-generic-segmentation")},
		{cfg.GenericReceiveOffload, eqFeat("rx-gro")},
		{cfg.LargeReceiveOffload, eqFeat("rx-lro")},
	}
	// 是否有任何 offload 字段被设置
	any := false
	for _, r := range rules {
		if r.val != nil {
			any = true
			break
		}
	}
	if !any {
		return
	}

	cur, err := mgr.EthtoolFeatures(name)
	if err != nil {
		slog.Warn("offload skipped: cannot read NIC features", "device", name, "error", err)
		return
	}
	want := map[string]bool{}
	for _, r := range rules {
		if r.val == nil {
			continue
		}
		for feat := range cur {
			if r.match(feat) {
				want[feat] = *r.val
			}
		}
	}
	if len(want) == 0 {
		return
	}
	slog.Info("applying offload settings", "device", name, "features", offloadSummary(want))
	if err := mgr.EthtoolChange(name, want); err != nil {
		// 虚拟设备/固定 feature 会失败——非致命，告警即可
		slog.Warn("failed to apply offload settings", "device", name, "error", err)
	}
}

func eqFeat(s string) func(string) bool { return func(f string) bool { return f == s } }
func prefixFeat(p string) func(string) bool {
	return func(f string) bool { return strings.HasPrefix(f, p) }
}
func inFeat(names ...string) func(string) bool {
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	return func(f string) bool { return set[f] }
}

func offloadSummary(want map[string]bool) string {
	parts := make([]string, 0, len(want))
	for f, v := range want {
		parts = append(parts, f+"="+onOff(v))
	}
	return strings.Join(parts, " ")
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

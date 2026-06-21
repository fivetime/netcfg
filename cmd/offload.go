/*
Copyright © 2024 netcfg authors

网卡 offload 配置：把 netplan 的 offload 字段经 ethtool -K 下发。
内核 offload feature API 有分组/版本差异（如 tx-checksum 是一组），ethtool 的
友好名映射最稳；ethtool 是标配小工具，仅在配置了 offload 时调用、缺失则告警跳过
（best-effort，与外部 DHCP 客户端/iw 一致，不引入硬依赖）。
*/

package cmd

import (
	"log/slog"
	"os/exec"
	"strings"

	"github.com/netcfg/netcfg/config"
)

// applyOffload 按 cfg 中已设置（非 nil）的 offload 字段调用 ethtool -K。
// 一次性把所有字段拼成一条 ethtool 命令下发。
func applyOffload(name string, cfg *config.Ethernet) {
	// netplan 字段 -> ethtool -K feature 名（短名优先；tcp6 无短名用完整 feature 名）
	feats := []struct {
		val  *bool
		flag string
	}{
		{cfg.ReceiveChecksumOffload, "rx"},
		{cfg.TransmitChecksumOffload, "tx"},
		{cfg.TCPSegmentationOffload, "tso"},
		{cfg.TCP6SegmentationOffload, "tx-tcp6-segmentation"},
		{cfg.GenericSegmentationOffload, "gso"},
		{cfg.GenericReceiveOffload, "gro"},
		{cfg.LargeReceiveOffload, "lro"},
	}

	args := []string{"-K", name}
	for _, f := range feats {
		if f.val == nil {
			continue
		}
		args = append(args, f.flag, onOff(*f.val))
	}
	if len(args) == 2 { // 只有 "-K name"，没有任何 feature
		return
	}

	if _, err := exec.LookPath("ethtool"); err != nil {
		slog.Warn("ethtool not found; offload settings skipped (install ethtool)", "device", name)
		return
	}

	slog.Info("applying offload settings", "device", name, "args", strings.Join(args[2:], " "))
	if out, err := exec.Command("ethtool", args...).CombinedOutput(); err != nil {
		// 虚拟设备/不支持的 feature 会失败——非致命，告警即可
		slog.Warn("failed to apply offload settings", "device", name,
			"error", err, "output", strings.TrimSpace(string(out)))
	}
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

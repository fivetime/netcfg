/*
Copyright © 2024 netcfg authors

网卡 offload 配置：把 netplan 的 offload 字段经 ethtool -K 下发。
内核 offload feature API 有分组/版本差异（如 tx-checksum 是一组），ethtool 的
友好名映射最稳；ethtool 是标配小工具，仅在配置了 offload 时调用、缺失则告警跳过
（best-effort，与外部 DHCP 客户端/iw 一致，不引入硬依赖）。
*/

package cmd

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"

	"github.com/netcfg/netcfg/config"
)

// applyEthernetExtras 应用其它物理网卡杂项：wakeonlan / infiniband-mode / emit-lldp。
func applyEthernetExtras(name string, cfg *config.Ethernet) {
	if cfg.Wakeonlan {
		applyWakeonlan(name)
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

// applyWakeonlan 经 ethtool 启用 Wake-on-LAN（magic packet）。
func applyWakeonlan(name string) {
	if _, err := exec.LookPath("ethtool"); err != nil {
		slog.Warn("ethtool not found; wakeonlan skipped", "device", name)
		return
	}
	if out, err := exec.Command("ethtool", "-s", name, "wol", "g").CombinedOutput(); err != nil {
		slog.Warn("failed to set wakeonlan", "device", name,
			"error", err, "output", strings.TrimSpace(string(out)))
		return
	}
	slog.Info("enabled wake-on-lan", "device", name)
}

// applyInfinibandMode 经 sysfs 设置 IPoIB 模式（connected/datagram）。
func applyInfinibandMode(name, mode string) {
	path := fmt.Sprintf("/sys/class/net/%s/mode", name)
	if err := os.WriteFile(path, []byte(mode), 0644); err != nil {
		slog.Warn("failed to set infiniband-mode (not an IPoIB device?)",
			"device", name, "mode", mode, "error", err)
		return
	}
	slog.Info("set infiniband mode", "device", name, "mode", mode)
}

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

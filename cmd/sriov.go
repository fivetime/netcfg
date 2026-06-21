/*
Copyright © 2024 netcfg authors

SR-IOV 配置（本地 sysfs / devlink）：
  - virtual-function-count → /sys/class/net/<pf>/device/sriov_numvfs（创建 VF）
  - embedded-switch-mode   → devlink dev eswitch set pci/<addr> mode <mode>（best-effort）

注意：写 sriov_numvfs 后，VF netdev 由内核/udev 异步出现并命名；其上的地址等
配置由各自的 ethernet 条目处理，可能需要 VF 就绪后（或再次 apply）才生效——这是
SR-IOV 的固有时序，netplan 也用 `rebind` 命令处理（见 TODO P3-5）。
非 SR-IOV 设备上相关写入会失败，按 best-effort 告警跳过，不中断 apply。
*/

package cmd

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/netcfg/netcfg/config"
)

// applySRIOV 在 PF 上应用 SR-IOV 设置（VF 数量 + eswitch 模式）。
func applySRIOV(pf string, cfg *config.Ethernet) {
	if cfg.VirtualFunctionCount > 0 {
		setNumVFs(pf, cfg.VirtualFunctionCount)
	}
	if cfg.EmbeddedSwitchMode != "" {
		setEswitchMode(pf, cfg.EmbeddedSwitchMode)
	}
	if cfg.DelayVFRebind != nil && *cfg.DelayVFRebind {
		// VF 驱动的解绑/重绑延迟由 `netcfg rebind`（待实现，P3-5）负责，这里仅提示。
		slog.Info("delay-virtual-functions-rebind set; VF driver rebind is deferred (use rebind when implemented)",
			"device", pf)
	}
}

// setNumVFs 通过 sysfs 设置 PF 的 VF 数量。多数驱动要求改动前先归零。
func setNumVFs(pf string, n int) {
	path := fmt.Sprintf("/sys/class/net/%s/device/sriov_numvfs", pf)
	if cur, err := os.ReadFile(path); err == nil {
		if v := strings.TrimSpace(string(cur)); v != "0" && v != strconv.Itoa(n) {
			_ = os.WriteFile(path, []byte("0"), 0644)
		}
	}
	if err := os.WriteFile(path, []byte(strconv.Itoa(n)), 0644); err != nil {
		slog.Warn("failed to set SR-IOV VF count (device may not support SR-IOV)",
			"device", pf, "count", n, "error", err)
		return
	}
	slog.Info("set SR-IOV VF count", "device", pf, "count", n)
}

// setEswitchMode 通过 devlink 设置 PF 的 eswitch 模式（legacy/switchdev）。
func setEswitchMode(pf, mode string) {
	target, err := os.Readlink(fmt.Sprintf("/sys/class/net/%s/device", pf))
	if err != nil {
		slog.Warn("cannot resolve PCI address for embedded-switch-mode", "device", pf, "error", err)
		return
	}
	addr := filepath.Base(target)

	if _, err := exec.LookPath("devlink"); err != nil {
		slog.Warn("devlink not found; embedded-switch-mode skipped (install iproute2)", "device", pf)
		return
	}
	if out, err := exec.Command("devlink", "dev", "eswitch", "set", "pci/"+addr, "mode", mode).CombinedOutput(); err != nil {
		slog.Warn("failed to set embedded-switch-mode", "device", pf, "mode", mode,
			"error", err, "output", strings.TrimSpace(string(out)))
		return
	}
	slog.Info("set embedded-switch-mode", "device", pf, "mode", mode, "pci", addr)
}

/*
Copyright © 2024 netcfg authors

SR-IOV 配置（本地 sysfs / devlink）：
  - virtual-function-count → /sys/class/net/<pf>/device/sriov_numvfs（创建 VF）
  - embedded-switch-mode   → devlink dev eswitch set pci/<addr> mode <mode>（best-effort）

注意：写 sriov_numvfs 后，VF netdev 由内核/udev 异步出现并命名；其上的地址等
配置由各自的 ethernet 条目处理，可能需要 VF 就绪后（或再次 apply）才生效——这是
SR-IOV 的固有时序，可用 `netcfg rebind <pf>` 解绑/重绑 VF（见 cmd/rebind.go）。
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
		// VF 驱动的解绑/重绑延迟由 `netcfg rebind <pf>` 负责，这里仅提示。
		slog.Info("delay-virtual-functions-rebind set; run `netcfg rebind` to (re)bind VFs",
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

// rebindVFs 解绑并重新绑定 PF 的所有 VF 到各自驱动（配合 delay-virtual-functions-rebind /
// eswitch 切换后让 VF/representor 重新就绪）。纯 sysfs/PCI 操作。
// 先全部 unbind 再全部 bind（switchdev 下 representor 顺序要求）。
func rebindVFs(pf string) {
	base := fmt.Sprintf("/sys/class/net/%s/device", pf)

	// 枚举 virtfn0,virtfn1,... → VF PCI 地址
	type vfDev struct{ pci, driver string }
	var vfs []vfDev
	for i := 0; ; i++ {
		target, err := os.Readlink(fmt.Sprintf("%s/virtfn%d", base, i))
		if err != nil {
			break
		}
		pci := filepath.Base(target)
		driver := ""
		if d, err := os.Readlink(fmt.Sprintf("/sys/bus/pci/devices/%s/driver", pci)); err == nil {
			driver = filepath.Base(d)
		}
		vfs = append(vfs, vfDev{pci, driver})
	}

	if len(vfs) == 0 {
		slog.Warn("no VFs to rebind (not an SR-IOV PF or no VFs created)", "pf", pf)
		return
	}

	// 1) 全部 unbind
	for _, vf := range vfs {
		if vf.driver == "" {
			continue
		}
		path := fmt.Sprintf("/sys/bus/pci/drivers/%s/unbind", vf.driver)
		if err := os.WriteFile(path, []byte(vf.pci), 0200); err != nil {
			slog.Warn("failed to unbind VF", "pf", pf, "vf", vf.pci, "driver", vf.driver, "error", err)
		}
	}
	// 2) 全部 bind
	for _, vf := range vfs {
		if vf.driver == "" {
			continue
		}
		path := fmt.Sprintf("/sys/bus/pci/drivers/%s/bind", vf.driver)
		if err := os.WriteFile(path, []byte(vf.pci), 0200); err != nil {
			slog.Warn("failed to bind VF", "pf", pf, "vf", vf.pci, "driver", vf.driver, "error", err)
			continue
		}
		slog.Info("rebound VF", "pf", pf, "vf", vf.pci, "driver", vf.driver)
	}
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

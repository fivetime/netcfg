/*
Copyright © 2024 netcfg authors

netcfg rebind — 重新绑定 SR-IOV 物理功能(PF)的虚拟功能(VF)到各自驱动。
对应 netplan 的 rebind 命令，配合 delay-virtual-functions-rebind 使用：apply 时
延迟 VF 驱动绑定（或切换 eswitch 模式）后，用本命令让 VF/representor 重新就绪。
纯 sysfs/PCI 操作，无外部依赖。
*/

package cmd

import (
	"fmt"
	"log/slog"
	"sort"

	"github.com/netcfg/netcfg/config"
	"github.com/spf13/cobra"
)

var rebindCmd = &cobra.Command{
	Use:   "rebind [interface...]",
	Short: "Rebind SR-IOV virtual functions of the given physical functions",
	Long: `Rebind the SR-IOV virtual functions (VFs) of the given physical function (PF)
interfaces to their drivers.

With no arguments, rebinds the VFs of all PFs that set
delay-virtual-functions-rebind in the configuration.`,
	RunE: runRebind,
}

func init() {
	rootCmd.AddCommand(rebindCmd)
}

func runRebind(cmd *cobra.Command, args []string) error {
	pfs := args
	if len(pfs) == 0 {
		// 无参：取配置中设置了 delay-virtual-functions-rebind 的 PF
		cfg, err := config.LoadConfig(configDir)
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}
		pfs = pfsWithDelayRebind(cfg)
		if len(pfs) == 0 {
			fmt.Println("no interfaces to rebind (specify PF names, or set delay-virtual-functions-rebind)")
			return nil
		}
	}

	for _, pf := range pfs {
		slog.Info("rebinding SR-IOV VFs", "pf", pf)
		rebindVFs(pf)
	}
	return nil
}

// pfsWithDelayRebind 返回 default namespace 中设置了 delay-virtual-functions-rebind
// 的 ethernet（PF）名，排序后返回。
func pfsWithDelayRebind(cfg *config.Config) []string {
	var pfs []string
	for name, eth := range cfg.Network.Ethernets {
		if eth != nil && eth.DelayVFRebind != nil && *eth.DelayVFRebind {
			pfs = append(pfs, name)
		}
	}
	sort.Strings(pfs)
	return pfs
}

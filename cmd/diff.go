/*
Copyright © 2024 netcfg authors

diff command - show configuration changes without applying.
*/

package cmd

import (
	"fmt"

	"github.com/netcfg/netcfg/config"
	"github.com/netcfg/netcfg/state"
	"github.com/spf13/cobra"
)

var diffCmd = &cobra.Command{
	Use:   "diff",
	Short: "Show configuration changes",
	Long: `Show what changes would be made without applying them.

This compares the current configuration files with the last applied state
and shows what would be added, removed, or modified.`,
	RunE: runDiff,
}

func init() {
	rootCmd.AddCommand(diffCmd)
}

func runDiff(cmd *cobra.Command, args []string) error {
	// 加载配置
	cfg, err := config.LoadConfig(configDir)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// 加载上次状态
	oldState, err := state.Load()
	if err != nil {
		oldState = state.NewState()
	}

	// 构建新状态
	newState := buildStateFromConfig(cfg)

	// 计算差异
	diff := state.ComputeDiff(oldState, newState)

	if diff.IsEmpty() {
		fmt.Println("No changes detected.")
		return nil
	}

	fmt.Println("The following changes would be applied:")
	fmt.Println()

	// 显示要删除的 namespace
	for _, ns := range diff.NsToRemove {
		fmt.Printf("  - Remove namespace: %s\n", ns)
	}

	// 显示要添加的 namespace
	for _, ns := range diff.NsToAdd {
		fmt.Printf("  + Add namespace: %s\n", ns)
	}

	// 显示要删除的设备
	for ns, devs := range diff.DevicesToRemove {
		nsLabel := ns
		if nsLabel == "" {
			nsLabel = "default"
		}
		for _, dev := range devs {
			fmt.Printf("  - Remove device %s (in %s)\n", dev, nsLabel)
		}
	}

	// 显示要添加的设备
	for ns, devs := range diff.DevicesToAdd {
		nsLabel := ns
		if nsLabel == "" {
			nsLabel = "default"
		}
		for _, dev := range devs {
			fmt.Printf("  + Add device %s (in %s)\n", dev, nsLabel)
		}
	}

	// 显示要删除的地址
	for ns, devAddrs := range diff.AddressesToRemove {
		nsLabel := ns
		if nsLabel == "" {
			nsLabel = "default"
		}
		for dev, addrs := range devAddrs {
			for _, addr := range addrs {
				fmt.Printf("  - Remove address %s from %s (in %s)\n", addr, dev, nsLabel)
			}
		}
	}

	// 显示要添加的地址
	for ns, devAddrs := range diff.AddressesToAdd {
		nsLabel := ns
		if nsLabel == "" {
			nsLabel = "default"
		}
		for dev, addrs := range devAddrs {
			for _, addr := range addrs {
				fmt.Printf("  + Add address %s to %s (in %s)\n", addr, dev, nsLabel)
			}
		}
	}

	// 显示要删除的路由
	for ns, devRoutes := range diff.RoutesToRemove {
		nsLabel := ns
		if nsLabel == "" {
			nsLabel = "default"
		}
		for dev, routes := range devRoutes {
			for _, route := range routes {
				fmt.Printf("  - Remove route %s from %s (in %s)\n", route, dev, nsLabel)
			}
		}
	}

	// 显示要添加的路由
	for ns, devRoutes := range diff.RoutesToAdd {
		nsLabel := ns
		if nsLabel == "" {
			nsLabel = "default"
		}
		for dev, routes := range devRoutes {
			for _, route := range routes {
				fmt.Printf("  + Add route %s to %s (in %s)\n", route, dev, nsLabel)
			}
		}
	}

	return nil
}

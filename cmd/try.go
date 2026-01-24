/*
Copyright © 2024 netcfg authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package cmd

import (
	"bufio"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/netcfg/netcfg/config"
	nl "github.com/netcfg/netcfg/netlink"
	"github.com/netcfg/netcfg/state"
	"github.com/spf13/cobra"
)

var tryTimeout int

var tryCmd = &cobra.Command{
	Use:   "try",
	Short: "Try network configuration with automatic rollback",
	Long: `Apply network configuration temporarily and wait for confirmation.

If not confirmed within the timeout period (default 120 seconds),
the configuration will be rolled back automatically.

This is useful for testing network changes remotely without
risking losing connectivity.`,
	RunE: runTry,
}

// 保存回滚状态
var rollbackState *state.State

func init() {
	rootCmd.AddCommand(tryCmd)
	tryCmd.Flags().IntVarP(&tryTimeout, "timeout", "t", 120, "Timeout in seconds before rollback")
}

func runTry(cmd *cobra.Command, args []string) error {
	cfg, err := config.LoadConfig(configDir)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// 保存当前状态用于回滚
	rollbackState, err = state.Load()
	if err != nil {
		slog.Warn("failed to load current state for rollback", "error", err)
		rollbackState = state.NewState()
	}

	fmt.Println("Applying configuration...")
	if err := Apply(cfg); err != nil {
		return fmt.Errorf("failed to apply config: %w", err)
	}

	fmt.Printf("\nConfiguration applied successfully.\n")
	fmt.Printf("Press ENTER within %d seconds to confirm, or wait for automatic rollback.\n", tryTimeout)

	// 创建确认通道
	confirmed := make(chan bool)

	go func() {
		reader := bufio.NewReader(os.Stdin)
		for {
			input, _ := reader.ReadString('\n')
			input = strings.TrimSpace(strings.ToLower(input))
			if input == "" || input == "y" || input == "yes" {
				confirmed <- true
				return
			}
			if input == "n" || input == "no" || input == "revert" {
				confirmed <- false
				return
			}
			fmt.Print("Press ENTER to confirm or type 'revert' to rollback: ")
		}
	}()

	// 等待确认或超时
	select {
	case ok := <-confirmed:
		if ok {
			fmt.Println("Configuration confirmed and saved.")
			return nil
		} else {
			fmt.Println("Rolling back configuration...")
			return rollback()
		}
	case <-time.After(time.Duration(tryTimeout) * time.Second):
		fmt.Println("\nTimeout reached. Rolling back configuration...")
		return rollback()
	}
}

func rollback() error {
	if rollbackState == nil {
		fmt.Println("No rollback state available.")
		return nil
	}

	// 加载当前（刚应用的）状态
	currentState, err := state.Load()
	if err != nil {
		return fmt.Errorf("failed to load current state: %w", err)
	}

	// 计算反向差异：从当前状态回到旧状态
	diff := state.ComputeDiff(currentState, rollbackState)

	fmt.Println("Reverting changes...")

	// 应用删除操作（删除新添加的）
	if err := applyRollbackRemovals(diff); err != nil {
		slog.Warn("failed to apply some rollback removals", "error", err)
	}

	// 恢复旧的配置
	if err := applyRollbackAdditions(diff, rollbackState); err != nil {
		slog.Warn("failed to apply some rollback additions", "error", err)
	}

	// 恢复旧状态文件
	if err := rollbackState.Save(); err != nil {
		slog.Warn("failed to save rollback state", "error", err)
	}

	fmt.Println("Rollback completed.")
	return nil
}

// applyRollbackRemovals 删除新添加的资源
func applyRollbackRemovals(diff *state.Diff) error {
	// 删除新添加的地址
	for ns, devAddrs := range diff.AddressesToAdd {
		var mgr *nl.NetlinkManager
		var err error
		if ns == "" {
			mgr, err = nl.New()
		} else {
			if !nl.NetnsExists(ns) {
				continue
			}
			mgr, err = nl.NewWithNetns(ns)
		}
		if err != nil {
			continue
		}

		for dev, addrs := range devAddrs {
			if !mgr.LinkExists(dev) {
				continue
			}
			for _, addr := range addrs {
				slog.Info("rollback: removing address", "device", dev, "address", addr)
				mgr.DeleteAddress(dev, addr)
			}
		}
		mgr.Close()
	}

	// 删除新添加的设备
	for ns, devs := range diff.DevicesToAdd {
		var mgr *nl.NetlinkManager
		var err error
		if ns == "" {
			mgr, err = nl.New()
		} else {
			if !nl.NetnsExists(ns) {
				continue
			}
			mgr, err = nl.NewWithNetns(ns)
		}
		if err != nil {
			continue
		}

		for _, dev := range devs {
			if mgr.LinkExists(dev) {
				slog.Info("rollback: removing device", "device", dev)
				mgr.DeleteLink(dev)
			}
		}
		mgr.Close()
	}

	// 删除新添加的 namespace
	for _, ns := range diff.NsToAdd {
		if ns != "" && nl.NetnsExists(ns) {
			slog.Info("rollback: removing netns", "name", ns)
			nl.DeleteNetns(ns)
		}
	}

	return nil
}

// applyRollbackAdditions 恢复被删除的资源
func applyRollbackAdditions(diff *state.Diff, oldState *state.State) error {
	// 恢复被删除的地址
	for ns, devAddrs := range diff.AddressesToRemove {
		var mgr *nl.NetlinkManager
		var err error
		if ns == "" {
			mgr, err = nl.New()
		} else {
			if !nl.NetnsExists(ns) {
				continue
			}
			mgr, err = nl.NewWithNetns(ns)
		}
		if err != nil {
			continue
		}

		for dev, addrs := range devAddrs {
			if !mgr.LinkExists(dev) {
				continue
			}
			for _, addr := range addrs {
				slog.Info("rollback: restoring address", "device", dev, "address", addr)
				mgr.AddAddress(dev, addr)
			}
		}
		mgr.Close()
	}

	return nil
}

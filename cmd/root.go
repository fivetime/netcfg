/*
Copyright © 2024 netcfg authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package cmd

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/netcfg/netcfg/version"
	"github.com/spf13/cobra"
)

var (
	configDir string
	debug     bool
)

var rootCmd = &cobra.Command{
	Use:   "netcfg",
	Short: "Network configuration tool with netns support",
	Long: `netcfg is a network configuration tool that is compatible with netplan syntax
and adds native support for network namespaces (netns).

It uses netlink directly for better performance and security.

Configuration files are read from /etc/netplan/ by default (for netplan compatibility)
or from /etc/netcfg/.

Examples:
  netcfg apply              # Apply network configuration
  netcfg generate           # Show what would be configured (dry-run)
  netcfg status             # Show current network status
  netcfg destroy            # Remove all configured netns`,
	Version: version.Version,
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		// 设置日志级别
		level := slog.LevelInfo
		if debug {
			level = slog.LevelDebug
		}

		handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
			Level: level,
		})
		slog.SetDefault(slog.New(handler))
	},
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	// 默认配置目录：仅当 /etc/netcfg 下确有 YAML 配置时才用它，否则回退 /etc/netplan
	// （避免空的 /etc/netcfg 目录遮蔽真正的 netplan 配置——netcfg 以 netplan 兼容为默认）。
	defaultConfigDir := "/etc/netplan"
	if dirHasYAML("/etc/netcfg") {
		defaultConfigDir = "/etc/netcfg"
	}

	rootCmd.PersistentFlags().StringVarP(&configDir, "config-dir", "c", defaultConfigDir, "Configuration directory")
	rootCmd.PersistentFlags().BoolVarP(&debug, "debug", "d", false, "Enable debug logging")
}

// dirHasYAML 判断目录下是否存在 *.yaml / *.yml 配置文件。
func dirHasYAML(dir string) bool {
	for _, pat := range []string{"*.yaml", "*.yml"} {
		if m, _ := filepath.Glob(filepath.Join(dir, pat)); len(m) > 0 {
			return true
		}
	}
	return false
}

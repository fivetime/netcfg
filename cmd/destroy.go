/*
Copyright © 2024 netcfg authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package cmd

import (
	"fmt"
	"log/slog"

	"github.com/netcfg/netcfg/config"
	nl "github.com/netcfg/netcfg/netlink"
	"github.com/spf13/cobra"
)

var (
	destroyAll bool
)

var destroyCmd = &cobra.Command{
	Use:   "destroy [netns-name...]",
	Short: "Destroy network namespaces",
	Long: `Destroy network namespaces created by netcfg.

Without arguments, destroys all namespaces defined in the configuration.
With arguments, destroys only the specified namespaces.
With -a/--all, destroys all namespaces in /var/run/netns/.`,
	RunE: runDestroy,
}

func init() {
	rootCmd.AddCommand(destroyCmd)
	destroyCmd.Flags().BoolVarP(&destroyAll, "all", "a", false, "Destroy all namespaces")
}

func runDestroy(cmd *cobra.Command, args []string) error {
	var namespaces []string

	if destroyAll {
		// 删除所有 netns
		var err error
		namespaces, err = nl.ListNetns()
		if err != nil {
			return fmt.Errorf("failed to list netns: %w", err)
		}
	} else if len(args) > 0 {
		// 删除指定的 netns
		namespaces = args
	} else {
		// 删除配置中定义的 netns
		cfg, err := config.LoadConfig(configDir)
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}
		namespaces = cfg.GetNetnsNames()
	}

	if len(namespaces) == 0 {
		fmt.Println("No namespaces to destroy")
		return nil
	}

	for _, ns := range namespaces {
		if !nl.NetnsExists(ns) {
			slog.Warn("netns does not exist", "name", ns)
			continue
		}

		slog.Info("destroying netns", "name", ns)
		if err := nl.DeleteNetns(ns); err != nil {
			slog.Error("failed to destroy netns", "name", ns, "error", err)
		} else {
			fmt.Printf("Destroyed netns: %s\n", ns)
		}
	}

	return nil
}

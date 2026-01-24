/*
Copyright © 2024 netcfg authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package cmd

import (
	"fmt"
	"net"
	"os"
	"strings"
	"text/tabwriter"

	nl "github.com/netcfg/netcfg/netlink"
	"github.com/spf13/cobra"
	"github.com/vishvananda/netlink"
)

var (
	statusAll   bool
	statusNetns string
)

var statusCmd = &cobra.Command{
	Use:   "status [interface...]",
	Short: "Show network status",
	Long: `Show current network interface status.

Without arguments, shows all interfaces in the default namespace.
With -a/--all, shows interfaces in all namespaces.
With -n/--netns, shows interfaces in the specified namespace.`,
	RunE: runStatus,
}

func init() {
	rootCmd.AddCommand(statusCmd)
	statusCmd.Flags().BoolVarP(&statusAll, "all", "a", false, "Show all namespaces")
	statusCmd.Flags().StringVarP(&statusNetns, "netns", "n", "", "Show specific namespace")
}

func runStatus(cmd *cobra.Command, args []string) error {
	if statusAll {
		return showAllNamespaces(args)
	}

	if statusNetns != "" {
		return showNamespace(statusNetns, args)
	}

	return showNamespace("", args)
}

func showAllNamespaces(filterInterfaces []string) error {
	// 先显示默认 namespace
	fmt.Println("=== Default Namespace ===")
	if err := showNamespace("", filterInterfaces); err != nil {
		return err
	}

	// 列出所有 netns
	namespaces, err := nl.ListNetns()
	if err != nil {
		return fmt.Errorf("failed to list netns: %w", err)
	}

	for _, ns := range namespaces {
		fmt.Printf("\n=== Namespace: %s ===\n", ns)
		if err := showNamespace(ns, filterInterfaces); err != nil {
			fmt.Printf("Error: %v\n", err)
		}
	}

	return nil
}

func showNamespace(nsName string, filterInterfaces []string) error {
	var mgr *nl.NetlinkManager
	var err error

	if nsName == "" {
		mgr, err = nl.New()
	} else {
		mgr, err = nl.NewWithNetns(nsName)
	}
	if err != nil {
		return fmt.Errorf("failed to create netlink manager: %w", err)
	}
	defer mgr.Close()

	links, err := mgr.ListLinks()
	if err != nil {
		return fmt.Errorf("failed to list links: %w", err)
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "INTERFACE\tSTATE\tMAC\tADDRESSES")

	for _, link := range links {
		name := link.Attrs().Name

		// 过滤接口
		if len(filterInterfaces) > 0 && !contains(filterInterfaces, name) {
			continue
		}

		state := "DOWN"
		if link.Attrs().Flags&net.FlagUp != 0 {
			state = "UP"
		}

		mac := link.Attrs().HardwareAddr.String()
		if mac == "" {
			mac = "-"
		}

		// 获取地址
		addrs, err := mgr.ListAddresses(name)
		if err != nil {
			addrs = nil
		}

		addrStrs := make([]string, 0)
		for _, addr := range addrs {
			addrStrs = append(addrStrs, addr.IPNet.String())
		}
		addrStr := strings.Join(addrStrs, ", ")
		if addrStr == "" {
			addrStr = "-"
		}

		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", name, state, mac, addrStr)
	}

	w.Flush()
	return nil
}

// showCmd 显示详细信息（兼容 netplan）
var showCmd = &cobra.Command{
	Use:     "show [interface]",
	Aliases: []string{"info"},
	Short:   "Show detailed interface information",
	RunE:    runShow,
}

func init() {
	rootCmd.AddCommand(showCmd)
}

func runShow(cmd *cobra.Command, args []string) error {
	mgr, err := nl.New()
	if err != nil {
		return err
	}
	defer mgr.Close()

	if len(args) == 0 {
		// 显示所有接口
		links, err := mgr.ListLinks()
		if err != nil {
			return err
		}

		for _, link := range links {
			showLinkDetails(mgr, link)
			fmt.Println()
		}
	} else {
		// 显示指定接口
		for _, name := range args {
			link, err := mgr.GetLink(name)
			if err != nil {
				fmt.Printf("Interface %s not found\n", name)
				continue
			}
			showLinkDetails(mgr, link)
			fmt.Println()
		}
	}

	return nil
}

func showLinkDetails(mgr *nl.NetlinkManager, link netlink.Link) {
	attrs := link.Attrs()
	fmt.Printf("Interface: %s\n", attrs.Name)
	fmt.Printf("  Type: %s\n", link.Type())
	fmt.Printf("  State: %s\n", attrs.OperState.String())
	fmt.Printf("  MAC: %s\n", attrs.HardwareAddr.String())
	fmt.Printf("  MTU: %d\n", attrs.MTU)
	fmt.Printf("  Index: %d\n", attrs.Index)

	if attrs.MasterIndex > 0 {
		fmt.Printf("  Master Index: %d\n", attrs.MasterIndex)
	}

	// 显示地址
	addrs, err := mgr.ListAddresses(attrs.Name)
	if err == nil && len(addrs) > 0 {
		fmt.Println("  Addresses:")
		for _, addr := range addrs {
			fmt.Printf("    - %s\n", addr.IPNet.String())
		}
	}

	// 显示路由
	routes, err := mgr.ListRoutes(attrs.Name)
	if err == nil && len(routes) > 0 {
		fmt.Println("  Routes:")
		for _, route := range routes {
			dst := "default"
			if route.Dst != nil {
				dst = route.Dst.String()
			}
			gw := ""
			if route.Gw != nil {
				gw = " via " + route.Gw.String()
			}
			fmt.Printf("    - %s%s\n", dst, gw)
		}
	}
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

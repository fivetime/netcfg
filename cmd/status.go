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
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/netcfg/netcfg/config"
	nl "github.com/netcfg/netcfg/netlink"
	"github.com/spf13/cobra"
	"github.com/vishvananda/netlink"
)

var (
	statusAll         bool
	statusNetns       string
	statusWait        bool
	statusWaitTimeout int
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
	statusCmd.Flags().BoolVar(&statusWait, "wait", false, "Wait until all required (non-optional) interfaces are up, then exit")
	statusCmd.Flags().IntVar(&statusWaitTimeout, "wait-timeout", 120, "Timeout in seconds for --wait")
}

func runStatus(cmd *cobra.Command, args []string) error {
	if statusWait {
		return waitOnline()
	}

	if statusAll {
		return showAllNamespaces(args)
	}

	if statusNetns != "" {
		return showNamespace(statusNetns, args)
	}

	return showNamespace("", args)
}

// waitOnline 阻塞直到 default namespace 中所有「必需」(未标记 optional) 的
// ethernet 接口就绪，或超时。用于 netcfg-wait-online.service / network-online.target。
// optional:true 的接口不参与等待（缺失/未就绪不阻塞启动）。
func waitOnline() error {
	cfg, err := config.LoadConfig(configDir)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	required := make([]string, 0)
	for name, eth := range cfg.Network.Ethernets {
		if eth != nil && !eth.Optional {
			required = append(required, name)
		}
	}
	sort.Strings(required)

	if len(required) == 0 {
		fmt.Println("no required interfaces to wait for")
		return nil
	}

	mgr, err := nl.New()
	if err != nil {
		return err
	}
	defer mgr.Close()

	deadline := time.Now().Add(time.Duration(statusWaitTimeout) * time.Second)
	for {
		pending := make([]string, 0)
		for _, name := range required {
			if !linkReady(mgr, name) {
				pending = append(pending, name)
			}
		}
		if len(pending) == 0 {
			fmt.Printf("all required interfaces up: %s\n", strings.Join(required, ", "))
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %ds waiting for interfaces: %s",
				statusWaitTimeout, strings.Join(pending, ", "))
		}
		time.Sleep(time.Second)
	}
}

// linkReady 判断接口是否已就绪：存在、管理态 UP、且 operational 状态为 up
// (OperUnknown 视为就绪，许多虚拟/无 carrier 语义的设备报告 unknown)。
func linkReady(mgr *nl.NetlinkManager, name string) bool {
	link, err := mgr.GetLink(name)
	if err != nil {
		return false
	}
	attrs := link.Attrs()
	if attrs.Flags&net.FlagUp == 0 {
		return false
	}
	return attrs.OperState == netlink.OperUp || attrs.OperState == netlink.OperUnknown
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

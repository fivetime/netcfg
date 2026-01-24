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
	"os/exec"
	"sort"
	"strings"

	"github.com/netcfg/netcfg/config"
	nl "github.com/netcfg/netcfg/netlink"
	"github.com/netcfg/netcfg/state"
	"github.com/spf13/cobra"
)

var applyCmd = &cobra.Command{
	Use:   "apply",
	Short: "Apply network configuration",
	Long:  `Apply network configuration from YAML files to the running system.`,
	RunE:  runApply,
}

func init() {
	rootCmd.AddCommand(applyCmd)
}

func runApply(cmd *cobra.Command, args []string) error {
	cfg, err := config.LoadConfig(configDir)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	return Apply(cfg)
}

// Apply 应用配置（支持增量更新）
func Apply(cfg *config.Config) error {
	// 加载上次的状态
	oldState, err := state.Load()
	if err != nil {
		slog.Warn("failed to load previous state", "error", err)
		oldState = state.NewState()
	}

	// 构建新状态
	newState := buildStateFromConfig(cfg)

	// 计算差异
	diff := state.ComputeDiff(oldState, newState)
	if !diff.IsEmpty() {
		slog.Info("configuration changes detected")
		slog.Debug("diff summary", "diff", diff.Summary())
	}

	// 1. 先清理不再需要的资源（逆序）
	if err := applyRemovals(diff); err != nil {
		slog.Warn("failed to apply some removals", "error", err)
	}

	// 2. 创建所有 netns
	for nsName := range cfg.Network.Netns {
		if nl.NetnsExists(nsName) {
			slog.Debug("netns already exists", "name", nsName)
		} else {
			slog.Info("creating netns", "name", nsName)
			if err := nl.CreateNetns(nsName); err != nil {
				return fmt.Errorf("failed to create netns %s: %w", nsName, err)
			}
		}
	}

	// 3. 处理 default namespace 的设备
	if cfg.HasDefaultNamespaceConfig() {
		slog.Info("configuring default namespace")
		if err := applyNamespaceConfig("", cfg.Network.ToNamespace()); err != nil {
			return fmt.Errorf("failed to configure default namespace: %w", err)
		}
	}

	// 4. 处理各个 netns
	nsNames := cfg.GetNetnsNames()
	for _, nsName := range nsNames {
		nsCfg := cfg.Network.Netns[nsName]
		slog.Info("configuring netns", "name", nsName)
		if err := applyNamespaceConfig(nsName, nsCfg); err != nil {
			return fmt.Errorf("failed to configure netns %s: %w", nsName, err)
		}
	}

	// 5. 保存新状态
	if err := newState.Save(); err != nil {
		slog.Warn("failed to save state", "error", err)
	}

	return nil
}

// buildStateFromConfig 从配置构建状态
func buildStateFromConfig(cfg *config.Config) *state.State {
	s := state.NewState()

	// default namespace
	if cfg.HasDefaultNamespaceConfig() {
		ns := cfg.Network.ToNamespace()
		s.SetNamespace("", buildNsState(ns))
	}

	// 其他 netns
	for nsName, nsCfg := range cfg.Network.Netns {
		s.SetNamespace(nsName, buildNsState(nsCfg))
	}

	return s
}

// buildNsState 构建单个 namespace 的状态
func buildNsState(ns *config.Namespace) *state.NsState {
	nsState := &state.NsState{
		Devices: make(map[string]*state.DeviceState),
	}

	// Ethernets
	for name, cfg := range ns.Ethernets {
		nsState.Devices[name] = &state.DeviceState{
			Type:      "ethernet",
			Addresses: cfg.Addresses,
			Routes:    routesToStrings(cfg.Routes),
			CreatedBy: "system", // 物理设备
		}
	}

	// Dummy
	for name, cfg := range ns.DummyDevices {
		nsState.Devices[name] = &state.DeviceState{
			Type:      "dummy",
			Addresses: cfg.Addresses,
			Routes:    routesToStrings(cfg.Routes),
			CreatedBy: "netcfg",
		}
	}

	// Bridges
	for name, cfg := range ns.Bridges {
		nsState.Devices[name] = &state.DeviceState{
			Type:      "bridge",
			Addresses: cfg.Addresses,
			Routes:    routesToStrings(cfg.Routes),
			CreatedBy: "netcfg",
		}
	}

	// Bonds
	for name, cfg := range ns.Bonds {
		nsState.Devices[name] = &state.DeviceState{
			Type:      "bond",
			Addresses: cfg.Addresses,
			Routes:    routesToStrings(cfg.Routes),
			CreatedBy: "netcfg",
		}
	}

	// VLANs
	for name, cfg := range ns.Vlans {
		nsState.Devices[name] = &state.DeviceState{
			Type:      "vlan",
			Addresses: cfg.Addresses,
			Routes:    routesToStrings(cfg.Routes),
			CreatedBy: "netcfg",
		}
	}

	// VXLANs
	for name, cfg := range ns.Vxlans {
		nsState.Devices[name] = &state.DeviceState{
			Type:      "vxlan",
			Addresses: cfg.Addresses,
			Routes:    routesToStrings(cfg.Routes),
			CreatedBy: "netcfg",
		}
	}

	// VRFs
	for name, cfg := range ns.Vrfs {
		nsState.Devices[name] = &state.DeviceState{
			Type:      "vrf",
			Routes:    routesToStrings(cfg.Routes),
			CreatedBy: "netcfg",
		}
	}

	// Tunnels
	for name, cfg := range ns.Tunnels {
		nsState.Devices[name] = &state.DeviceState{
			Type:      "tunnel",
			Addresses: cfg.Addresses,
			Routes:    routesToStrings(cfg.Routes),
			CreatedBy: "netcfg",
		}
	}

	// WireGuard
	for name, cfg := range ns.Wireguards {
		nsState.Devices[name] = &state.DeviceState{
			Type:      "wireguard",
			Addresses: cfg.Addresses,
			Routes:    routesToStrings(cfg.Routes),
			CreatedBy: "netcfg",
		}
	}

	// Veth
	for name, cfg := range ns.VethDevices {
		nsState.Devices[name] = &state.DeviceState{
			Type:      "veth",
			Addresses: cfg.Addresses,
			Routes:    routesToStrings(cfg.Routes),
			CreatedBy: "netcfg",
		}
	}

	// Macvlan
	for name, cfg := range ns.MacvlanDevices {
		nsState.Devices[name] = &state.DeviceState{
			Type:      "macvlan",
			Addresses: cfg.Addresses,
			Routes:    routesToStrings(cfg.Routes),
			CreatedBy: "netcfg",
		}
	}

	// Ipvlan
	for name, cfg := range ns.IpvlanDevices {
		nsState.Devices[name] = &state.DeviceState{
			Type:      "ipvlan",
			Addresses: cfg.Addresses,
			Routes:    routesToStrings(cfg.Routes),
			CreatedBy: "netcfg",
		}
	}

	// TUN
	for name, cfg := range ns.TunDevices {
		nsState.Devices[name] = &state.DeviceState{
			Type:      "tun",
			Addresses: cfg.Addresses,
			Routes:    routesToStrings(cfg.Routes),
			CreatedBy: "netcfg",
		}
	}

	// TAP
	for name, cfg := range ns.TapDevices {
		nsState.Devices[name] = &state.DeviceState{
			Type:      "tap",
			Addresses: cfg.Addresses,
			Routes:    routesToStrings(cfg.Routes),
			CreatedBy: "netcfg",
		}
	}

	return nsState
}

// routesToStrings 将路由转换为字符串列表
func routesToStrings(routes []*config.Route) []string {
	var result []string
	for _, r := range routes {
		s := r.To
		if r.Via != "" {
			s += " via " + r.Via
		}
		if r.Table > 0 {
			s += fmt.Sprintf(" table %d", r.Table)
		}
		result = append(result, s)
	}
	return result
}

// applyRemovals 应用删除操作
func applyRemovals(diff *state.Diff) error {
	// 删除地址
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
			slog.Warn("failed to get manager for address removal", "ns", ns, "error", err)
			continue
		}

		for dev, addrs := range devAddrs {
			if !mgr.LinkExists(dev) {
				continue
			}
			for _, addr := range addrs {
				slog.Info("removing address", "device", dev, "address", addr, "netns", ns)
				if err := mgr.DeleteAddress(dev, addr); err != nil {
					slog.Warn("failed to remove address", "device", dev, "address", addr, "error", err)
				}
			}
		}
		mgr.Close()
	}

	// 删除路由
	for ns, devRoutes := range diff.RoutesToRemove {
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
			slog.Warn("failed to get manager for route removal", "ns", ns, "error", err)
			continue
		}

		for dev, routes := range devRoutes {
			for _, route := range routes {
				// 解析路由字符串 "to via gateway table N"
				dst, gw, table := parseRouteString(route)
				slog.Info("removing route", "dst", dst, "via", gw, "device", dev, "netns", ns)
				if err := mgr.DeleteRoute(dst, gw, dev, table); err != nil {
					slog.Warn("failed to remove route", "route", route, "error", err)
				}
			}
		}
		mgr.Close()
	}

	// 删除设备（netcfg 创建的）
	for ns, devs := range diff.DevicesToRemove {
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
			slog.Warn("failed to get manager for device removal", "ns", ns, "error", err)
			continue
		}

		for _, dev := range devs {
			if !mgr.LinkExists(dev) {
				continue
			}
			slog.Info("removing device", "device", dev, "netns", ns)
			if err := mgr.DeleteLink(dev); err != nil {
				slog.Warn("failed to remove device", "device", dev, "error", err)
			}
		}
		mgr.Close()
	}

	// 删除 namespace
	for _, ns := range diff.NsToRemove {
		if ns == "" {
			continue // 不能删除 default namespace
		}
		if nl.NetnsExists(ns) {
			slog.Info("removing netns", "name", ns)
			if err := nl.DeleteNetns(ns); err != nil {
				slog.Warn("failed to remove netns", "name", ns, "error", err)
			}
		}
	}

	return nil
}

// parseRouteString 解析路由字符串
func parseRouteString(s string) (dst, gw string, table int) {
	parts := strings.Fields(s)
	if len(parts) > 0 {
		dst = parts[0]
	}
	for i := 0; i < len(parts)-1; i++ {
		if parts[i] == "via" {
			gw = parts[i+1]
		}
		if parts[i] == "table" {
			_, _ = fmt.Sscanf(parts[i+1], "%d", &table)
		}
	}
	return
}

// applyNamespaceConfig 应用单个 namespace 的配置
func applyNamespaceConfig(nsName string, cfg *config.Namespace) error {
	if cfg == nil {
		return nil
	}

	// 获取管理器
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

	// 处理 loopback
	if nsName != "" {
		if err := setupLoopback(mgr, cfg.Loopback); err != nil {
			return fmt.Errorf("failed to setup loopback: %w", err)
		}
	}

	// 按顺序处理各类设备
	// 1. 物理设备（移入 netns）
	if err := setupEthernets(mgr, nsName, cfg.Ethernets); err != nil {
		return fmt.Errorf("failed to setup ethernets: %w", err)
	}

	// 2. Dummy 设备
	if err := setupDummyDevices(mgr, nsName, cfg.DummyDevices); err != nil {
		return fmt.Errorf("failed to setup dummy devices: %w", err)
	}

	// 3. Macvlan/Macvtap 设备
	if err := setupMacvlanDevices(mgr, nsName, cfg.MacvlanDevices, false); err != nil {
		return fmt.Errorf("failed to setup macvlan devices: %w", err)
	}
	if err := setupMacvlanDevices(mgr, nsName, cfg.MacvtapDevices, true); err != nil {
		return fmt.Errorf("failed to setup macvtap devices: %w", err)
	}

	// 3.5. Ipvlan 设备
	if err := setupIpvlanDevices(mgr, nsName, cfg.IpvlanDevices); err != nil {
		return fmt.Errorf("failed to setup ipvlan devices: %w", err)
	}

	// 4. VLAN 设备
	if err := setupVlans(mgr, nsName, cfg.Vlans); err != nil {
		return fmt.Errorf("failed to setup vlans: %w", err)
	}

	// 5. Vxlan 设备
	if err := setupVxlans(mgr, nsName, cfg.Vxlans); err != nil {
		return fmt.Errorf("failed to setup vxlans: %w", err)
	}

	// 6. Bond 设备
	if err := setupBonds(mgr, nsName, cfg.Bonds); err != nil {
		return fmt.Errorf("failed to setup bonds: %w", err)
	}

	// 7. Bridge 设备
	if err := setupBridges(mgr, nsName, cfg.Bridges); err != nil {
		return fmt.Errorf("failed to setup bridges: %w", err)
	}

	// 8. VRF 设备
	if err := setupVrfs(mgr, nsName, cfg.Vrfs); err != nil {
		return fmt.Errorf("failed to setup vrfs: %w", err)
	}

	// 8.5. Tunnel 设备
	if err := setupTunnels(mgr, nsName, cfg.Tunnels); err != nil {
		return fmt.Errorf("failed to setup tunnels: %w", err)
	}

	// 8.6. WireGuard 设备
	if err := setupWireguards(mgr, nsName, cfg.Wireguards); err != nil {
		return fmt.Errorf("failed to setup wireguards: %w", err)
	}

	// 8.7. TUN/TAP 设备
	if err := setupTunTapDevices(mgr, nsName, cfg.TunDevices, false); err != nil {
		return fmt.Errorf("failed to setup tun devices: %w", err)
	}
	if err := setupTunTapDevices(mgr, nsName, cfg.TapDevices, true); err != nil {
		return fmt.Errorf("failed to setup tap devices: %w", err)
	}

	// 9. Veth 设备（需要特殊处理跨 netns）
	if err := setupVethDevices(mgr, nsName, cfg.VethDevices); err != nil {
		return fmt.Errorf("failed to setup veth devices: %w", err)
	}

	// 10. 后置脚本
	if nsName != "" && cfg.PostScript != "" {
		if err := runPostScript(nsName, cfg.PostScript); err != nil {
			return fmt.Errorf("failed to run post-script: %w", err)
		}
	}

	return nil
}

// setupLoopback 配置 loopback 设备
func setupLoopback(mgr *nl.NetlinkManager, cfg *config.Ethernet) error {
	// 启用 lo
	if err := mgr.SetLinkUp("lo"); err != nil {
		slog.Warn("failed to set lo up", "error", err)
	}

	if cfg == nil {
		return nil
	}

	return setupDevice(mgr, "lo", cfg.Addresses, cfg.Routes, 0, "")
}

// setupEthernets 配置以太网设备
func setupEthernets(mgr *nl.NetlinkManager, nsName string, devices map[string]*config.Ethernet) error {
	// 按名称排序确保顺序一致
	names := sortedKeys(devices)

	for _, name := range names {
		cfg := devices[name]
		slog.Debug("setting up ethernet", "name", name, "netns", nsName)

		// 如果在 netns 中且设备不存在，尝试从 default ns 移入
		if nsName != "" && !mgr.LinkExists(name) {
			defaultMgr, err := nl.New()
			if err != nil {
				return err
			}

			if defaultMgr.LinkExists(name) {
				slog.Info("moving device to netns", "name", name, "netns", nsName)
				if err := defaultMgr.SetLinkNetns(name, nsName); err != nil {
					defaultMgr.Close()
					return fmt.Errorf("failed to move %s to netns %s: %w", name, nsName, err)
				}
			}
			defaultMgr.Close()
		}

		// 使用支持 DHCP 的配置函数
		if err := setupDeviceWithDHCP(mgr, name, cfg); err != nil {
			return fmt.Errorf("failed to setup ethernet %s: %w", name, err)
		}
	}

	return nil
}

// setupDummyDevices 配置 dummy 设备
func setupDummyDevices(mgr *nl.NetlinkManager, nsName string, devices map[string]*config.Ethernet) error {
	names := sortedKeys(devices)

	for _, name := range names {
		cfg := devices[name]
		slog.Debug("setting up dummy device", "name", name, "netns", nsName)

		if !mgr.LinkExists(name) {
			slog.Info("creating dummy device", "name", name, "netns", nsName)
			if err := mgr.AddDummyDevice(name); err != nil {
				return fmt.Errorf("failed to create dummy %s: %w", name, err)
			}
		}

		if err := setupDevice(mgr, name, cfg.Addresses, cfg.Routes, cfg.MTU, cfg.MacAddress); err != nil {
			return fmt.Errorf("failed to setup dummy %s: %w", name, err)
		}
	}

	return nil
}

// setupMacvlanDevices 配置 macvlan/macvtap 设备
func setupMacvlanDevices(mgr *nl.NetlinkManager, nsName string, devices map[string]*config.MacvlanDevice, isMacvtap bool) error {
	names := sortedKeys(devices)
	deviceType := "macvlan"
	if isMacvtap {
		deviceType = "macvtap"
	}

	for _, name := range names {
		cfg := devices[name]
		slog.Debug("setting up "+deviceType+" device", "name", name, "netns", nsName, "link", cfg.Link)

		if !mgr.LinkExists(name) {
			// macvlan/macvtap 需要在父设备所在的 namespace 创建，然后移入目标 namespace
			if nsName != "" {
				// 在 default namespace 创建
				defaultMgr, err := nl.New()
				if err != nil {
					return err
				}

				slog.Info("creating "+deviceType+" device in default namespace", "name", name, "link", cfg.Link)
				if isMacvtap {
					err = defaultMgr.AddMacvtapDevice(name, cfg.Link, cfg.Mode)
				} else {
					err = defaultMgr.AddMacvlanDevice(name, cfg.Link, cfg.Mode)
				}
				if err != nil {
					defaultMgr.Close()
					return fmt.Errorf("failed to create %s %s: %w", deviceType, name, err)
				}

				// 移入目标 netns
				slog.Info("moving "+deviceType+" to netns", "name", name, "netns", nsName)
				if err := defaultMgr.SetLinkNetns(name, nsName); err != nil {
					defaultMgr.Close()
					return fmt.Errorf("failed to move %s to netns %s: %w", name, nsName, err)
				}
				defaultMgr.Close()
			} else {
				// 在 default namespace 直接创建
				slog.Info("creating "+deviceType+" device", "name", name, "link", cfg.Link)
				var err error
				if isMacvtap {
					err = mgr.AddMacvtapDevice(name, cfg.Link, cfg.Mode)
				} else {
					err = mgr.AddMacvlanDevice(name, cfg.Link, cfg.Mode)
				}
				if err != nil {
					return fmt.Errorf("failed to create %s %s: %w", deviceType, name, err)
				}
			}
		}

		if err := setupDevice(mgr, name, cfg.Addresses, cfg.Routes, cfg.MTU, cfg.MacAddress); err != nil {
			return fmt.Errorf("failed to setup %s %s: %w", deviceType, name, err)
		}
	}

	return nil
}

// setupVlans 配置 VLAN 设备
func setupVlans(mgr *nl.NetlinkManager, nsName string, devices map[string]*config.Vlan) error {
	names := sortedKeys(devices)

	for _, name := range names {
		cfg := devices[name]
		slog.Debug("setting up vlan", "name", name, "netns", nsName, "id", cfg.ID)

		if !mgr.LinkExists(name) {
			slog.Info("creating vlan device", "name", name, "link", cfg.Link, "id", cfg.ID)
			if err := mgr.AddVlan(name, cfg.Link, cfg.ID); err != nil {
				return fmt.Errorf("failed to create vlan %s: %w", name, err)
			}
		}

		if err := setupDevice(mgr, name, cfg.Addresses, cfg.Routes, cfg.MTU, cfg.MacAddress); err != nil {
			return fmt.Errorf("failed to setup vlan %s: %w", name, err)
		}
	}

	return nil
}

// setupVxlans 配置 VXLAN 设备
func setupVxlans(mgr *nl.NetlinkManager, nsName string, devices map[string]*config.Vxlan) error {
	names := sortedKeys(devices)

	for _, name := range names {
		cfg := devices[name]
		slog.Debug("setting up vxlan", "name", name, "netns", nsName, "vni", cfg.ID)

		if !mgr.LinkExists(name) {
			slog.Info("creating vxlan device", "name", name, "vni", cfg.ID, "external", cfg.External)
			opts := &nl.VxlanOptions{
				Link:           cfg.Link,
				Local:          cfg.Local,
				Remote:         cfg.Remote,
				Group:          cfg.Group,
				Port:           cfg.Port,
				DestPort:       cfg.DestPort,
				TTL:            cfg.TTL,
				TOS:            cfg.TOS,
				Age:            cfg.Ageing,
				Limit:          cfg.Limit,
				Learning:       cfg.Learning,
				Proxy:          cfg.ARPProxy,
				RSC:            cfg.RSC,
				L2miss:         cfg.L2miss,
				L3miss:         cfg.L3miss,
				NoAge:          cfg.NoAge,
				GBP:            cfg.GBP,
				FlowBased:      cfg.External,
				UDPCSum:        cfg.UDPChecksum,
				UDP6ZeroCSumTx: cfg.UDP6ZeroCSumTx,
				UDP6ZeroCSumRx: cfg.UDP6ZeroCSumRx,
			}
			// 源端口范围
			if len(cfg.PortRange) == 2 {
				opts.PortLow = cfg.PortRange[0]
				opts.PortHigh = cfg.PortRange[1]
			}
			if err := mgr.AddVxlan(name, cfg.ID, opts); err != nil {
				return fmt.Errorf("failed to create vxlan %s: %w", name, err)
			}
		}

		if err := setupDevice(mgr, name, cfg.Addresses, cfg.Routes, cfg.MTU, cfg.MacAddress); err != nil {
			return fmt.Errorf("failed to setup vxlan %s: %w", name, err)
		}

		// EVPN: 设置 neigh suppress
		if cfg.NeighSuppress != nil && *cfg.NeighSuppress {
			slog.Info("enabling neigh suppress", "device", name)
			if err := mgr.SetLinkNeighSuppress(name, true); err != nil {
				slog.Warn("failed to set neigh suppress", "device", name, "error", err)
			}
		}

		// EVPN: 添加静态 FDB 条目
		for _, fdb := range cfg.FDB {
			slog.Info("adding FDB entry", "device", name, "mac", fdb.MAC, "dst", fdb.Dst)
			entry := &nl.FDBEntry{
				MAC:    fdb.MAC,
				Ifname: name,
				Dst:    fdb.Dst,
				VNI:    fdb.VNI,
				State:  fdb.State,
			}
			if err := mgr.AddFDBEntry(entry); err != nil {
				slog.Warn("failed to add FDB entry", "mac", fdb.MAC, "error", err)
			}
		}

		// EVPN: 添加静态 Neighbor 条目
		for _, neigh := range cfg.Neighbors {
			slog.Info("adding neighbor entry", "device", name, "ip", neigh.IP, "mac", neigh.MAC)
			entry := &nl.NeighEntry{
				IP:     neigh.IP,
				MAC:    neigh.MAC,
				Ifname: name,
				State:  neigh.State,
			}
			if err := mgr.AddNeighEntry(entry); err != nil {
				slog.Warn("failed to add neighbor entry", "ip", neigh.IP, "error", err)
			}
		}
	}

	return nil
}

// setupBonds 配置 bond 设备
func setupBonds(mgr *nl.NetlinkManager, nsName string, devices map[string]*config.Bond) error {
	names := sortedKeys(devices)

	for _, name := range names {
		cfg := devices[name]
		slog.Debug("setting up bond", "name", name, "netns", nsName)

		if !mgr.LinkExists(name) {
			mode := nl.ParseBondMode("balance-rr")
			if cfg.Parameters != nil && cfg.Parameters.Mode != "" {
				mode = nl.ParseBondMode(cfg.Parameters.Mode)
			}

			slog.Info("creating bond device", "name", name, "mode", mode)
			if err := mgr.AddBond(name, mode); err != nil {
				return fmt.Errorf("failed to create bond %s: %w", name, err)
			}
		}

		// 添加接口到 bond
		for _, iface := range cfg.Interfaces {
			slog.Debug("adding interface to bond", "interface", iface, "bond", name)
			if err := mgr.SetBondSlave(iface, name); err != nil {
				slog.Warn("failed to add interface to bond", "interface", iface, "bond", name, "error", err)
			}
		}

		if err := setupDevice(mgr, name, cfg.Addresses, cfg.Routes, cfg.MTU, cfg.MacAddress); err != nil {
			return fmt.Errorf("failed to setup bond %s: %w", name, err)
		}
	}

	return nil
}

// setupBridges 配置 bridge 设备
func setupBridges(mgr *nl.NetlinkManager, nsName string, devices map[string]*config.Bridge) error {
	names := sortedKeys(devices)

	for _, name := range names {
		cfg := devices[name]
		slog.Debug("setting up bridge", "name", name, "netns", nsName)

		if !mgr.LinkExists(name) {
			slog.Info("creating bridge device", "name", name)
			if err := mgr.AddBridge(name); err != nil {
				return fmt.Errorf("failed to create bridge %s: %w", name, err)
			}
		}

		// EVPN: 设置 VLAN 过滤
		if cfg.VlanFiltering != nil {
			slog.Info("setting vlan filtering", "bridge", name, "enabled", *cfg.VlanFiltering)
			if err := mgr.SetBridgeVlanFiltering(name, *cfg.VlanFiltering); err != nil {
				slog.Warn("failed to set vlan filtering", "bridge", name, "error", err)
			}
		}

		// 添加接口到 bridge
		for _, iface := range cfg.Interfaces {
			slog.Debug("adding interface to bridge", "interface", iface, "bridge", name)
			if err := mgr.SetBridgeMaster(iface, name); err != nil {
				slog.Warn("failed to add interface to bridge", "interface", iface, "bridge", name, "error", err)
			}
		}

		if err := setupDevice(mgr, name, cfg.Addresses, cfg.Routes, cfg.MTU, cfg.MacAddress); err != nil {
			return fmt.Errorf("failed to setup bridge %s: %w", name, err)
		}

		// EVPN: 添加静态 FDB 条目
		for _, fdb := range cfg.FDB {
			slog.Info("adding FDB entry to bridge", "bridge", name, "mac", fdb.MAC, "dst", fdb.Dst)
			entry := &nl.FDBEntry{
				MAC:    fdb.MAC,
				Ifname: name,
				Dst:    fdb.Dst,
				State:  fdb.State,
			}
			if err := mgr.AddFDBEntry(entry); err != nil {
				slog.Warn("failed to add FDB entry", "mac", fdb.MAC, "error", err)
			}
		}

		// EVPN: 添加静态 Neighbor 条目
		for _, neigh := range cfg.Neighbors {
			slog.Info("adding neighbor entry to bridge", "bridge", name, "ip", neigh.IP, "mac", neigh.MAC)
			entry := &nl.NeighEntry{
				IP:     neigh.IP,
				MAC:    neigh.MAC,
				Ifname: name,
				State:  neigh.State,
			}
			if err := mgr.AddNeighEntry(entry); err != nil {
				slog.Warn("failed to add neighbor entry", "ip", neigh.IP, "error", err)
			}
		}
	}

	return nil
}

// setupVrfs 配置 VRF 设备
func setupVrfs(mgr *nl.NetlinkManager, nsName string, devices map[string]*config.Vrf) error {
	names := sortedKeys(devices)

	for _, name := range names {
		cfg := devices[name]
		slog.Debug("setting up vrf", "name", name, "netns", nsName, "table", cfg.Table)

		if !mgr.LinkExists(name) {
			slog.Info("creating vrf device", "name", name, "table", cfg.Table)
			if err := mgr.AddVrf(name, cfg.Table); err != nil {
				return fmt.Errorf("failed to create vrf %s: %w", name, err)
			}
		}

		// 启用 VRF
		if err := mgr.SetLinkUp(name); err != nil {
			return fmt.Errorf("failed to set vrf %s up: %w", name, err)
		}

		// 添加接口到 VRF
		for _, iface := range cfg.Interfaces {
			slog.Debug("adding interface to vrf", "interface", iface, "vrf", name)
			if err := mgr.SetVrfMaster(iface, name); err != nil {
				slog.Warn("failed to add interface to vrf", "interface", iface, "vrf", name, "error", err)
			}
		}

		// 添加路由
		for _, route := range cfg.Routes {
			if err := addRoute(mgr, name, route); err != nil {
				slog.Warn("failed to add route", "device", name, "route", route.To, "error", err)
			}
		}
	}

	return nil
}

// setupVethDevices 配置 veth 设备
func setupVethDevices(mgr *nl.NetlinkManager, nsName string, devices map[string]*config.VethDevice) error {
	names := sortedKeys(devices)

	for _, name := range names {
		cfg := devices[name]
		if cfg.Peer == nil {
			return fmt.Errorf("veth %s: peer is required", name)
		}

		peerName := cfg.Peer.Name
		peerNs := cfg.Peer.Netns

		slog.Debug("setting up veth", "name", name, "peer", peerName, "netns", nsName, "peer_netns", peerNs)

		// 检查是否需要创建 veth pair
		needCreate := !mgr.LinkExists(name)

		if needCreate {
			// 在 default namespace 创建 veth pair
			defaultMgr, err := nl.New()
			if err != nil {
				return err
			}

			if !defaultMgr.LinkExists(name) && !defaultMgr.LinkExists(peerName) {
				slog.Info("creating veth pair", "name", name, "peer", peerName)
				if err := defaultMgr.AddVethPair(name, peerName); err != nil {
					defaultMgr.Close()
					return fmt.Errorf("failed to create veth pair %s/%s: %w", name, peerName, err)
				}
			}

			// 移动 veth 到目标 netns
			if nsName != "" {
				slog.Info("moving veth to netns", "name", name, "netns", nsName)
				if err := defaultMgr.SetLinkNetns(name, nsName); err != nil {
					defaultMgr.Close()
					return fmt.Errorf("failed to move %s to netns %s: %w", name, nsName, err)
				}
			}

			// 移动 peer 到目标 netns
			if peerNs != "" {
				// 确保目标 netns 存在
				if !nl.NetnsExists(peerNs) {
					slog.Info("creating netns for peer", "netns", peerNs)
					if err := nl.CreateNetns(peerNs); err != nil {
						defaultMgr.Close()
						return fmt.Errorf("failed to create netns %s: %w", peerNs, err)
					}
				}

				slog.Info("moving veth peer to netns", "name", peerName, "netns", peerNs)
				if err := defaultMgr.SetLinkNetns(peerName, peerNs); err != nil {
					defaultMgr.Close()
					return fmt.Errorf("failed to move %s to netns %s: %w", peerName, peerNs, err)
				}
			}

			defaultMgr.Close()
		}

		// 配置本端
		if err := setupDevice(mgr, name, cfg.Addresses, cfg.Routes, cfg.MTU, cfg.MacAddress); err != nil {
			return fmt.Errorf("failed to setup veth %s: %w", name, err)
		}

		// 配置对端
		if peerNs != "" {
			peerMgr, err := nl.NewWithNetns(peerNs)
			if err != nil {
				return fmt.Errorf("failed to get netns %s for peer: %w", peerNs, err)
			}

			if err := setupDevice(peerMgr, peerName, cfg.Peer.Addresses, cfg.Peer.Routes, cfg.Peer.MTU, cfg.Peer.MacAddress); err != nil {
				peerMgr.Close()
				return fmt.Errorf("failed to setup veth peer %s: %w", peerName, err)
			}
			peerMgr.Close()
		} else if nsName == "" {
			// 对端在同一个 namespace
			if err := setupDevice(mgr, peerName, cfg.Peer.Addresses, cfg.Peer.Routes, cfg.Peer.MTU, cfg.Peer.MacAddress); err != nil {
				return fmt.Errorf("failed to setup veth peer %s: %w", peerName, err)
			}
		}
	}

	return nil
}

// setupDevice 配置设备的通用函数
func setupDevice(mgr *nl.NetlinkManager, name string, addresses []string, routes []*config.Route, mtu int, mac string) error {
	// 设置 MTU
	if mtu > 0 {
		if err := mgr.SetLinkMTU(name, mtu); err != nil {
			slog.Warn("failed to set mtu", "device", name, "mtu", mtu, "error", err)
		}
	}

	// 设置 MAC 地址
	if mac != "" {
		if err := mgr.SetLinkMacAddress(name, mac); err != nil {
			slog.Warn("failed to set mac", "device", name, "mac", mac, "error", err)
		}
	}

	// 启用设备
	if err := mgr.SetLinkUp(name); err != nil {
		return fmt.Errorf("failed to set %s up: %w", name, err)
	}

	// 添加地址
	for _, addr := range addresses {
		// 检查地址是否已存在
		hasAddr, err := mgr.HasAddress(name, addr)
		if err != nil {
			slog.Warn("failed to check address", "device", name, "address", addr, "error", err)
			continue
		}

		if !hasAddr {
			slog.Info("adding address", "device", name, "address", addr)
			if err := mgr.AddAddress(name, addr); err != nil {
				slog.Warn("failed to add address", "device", name, "address", addr, "error", err)
			}
		}
	}

	// 添加路由
	for _, route := range routes {
		if err := addRoute(mgr, name, route); err != nil {
			slog.Warn("failed to add route", "device", name, "route", route.To, "error", err)
		}
	}

	return nil
}

// setupDeviceWithDHCP 配置设备（支持 DHCP）
func setupDeviceWithDHCP(mgr *nl.NetlinkManager, name string, cfg *config.Ethernet) error {
	// 基本配置
	if cfg.MTU > 0 {
		if err := mgr.SetLinkMTU(name, cfg.MTU); err != nil {
			slog.Warn("failed to set mtu", "device", name, "mtu", cfg.MTU, "error", err)
		}
	}

	if cfg.MacAddress != "" {
		if err := mgr.SetLinkMacAddress(name, cfg.MacAddress); err != nil {
			slog.Warn("failed to set mac", "device", name, "mac", cfg.MacAddress, "error", err)
		}
	}

	// 启用设备
	if err := mgr.SetLinkUp(name); err != nil {
		return fmt.Errorf("failed to set %s up: %w", name, err)
	}

	// 处理 IPv6 RA/SLAAC
	if cfg.AcceptRA != nil {
		if *cfg.AcceptRA {
			slog.Info("enabling SLAAC", "device", name)
			if err := nl.EnableSLAAC(name); err != nil {
				slog.Warn("failed to enable SLAAC", "device", name, "error", err)
			}
		} else {
			if err := nl.DisableSLAAC(name); err != nil {
				slog.Warn("failed to disable SLAAC", "device", name, "error", err)
			}
		}
	}

	// 处理 DHCPv4
	if cfg.DHCP4 {
		slog.Info("starting DHCPv4", "device", name)
		dhcpMgr := nl.NewDHCPManager()

		// 异步执行 DHCP，避免阻塞
		go func() {
			if _, err := dhcpMgr.RequestDHCPv4(name); err != nil {
				slog.Error("DHCPv4 failed", "device", name, "error", err)
			}
		}()
	}

	// 处理 DHCPv6
	if cfg.DHCP6 {
		slog.Info("starting DHCPv6", "device", name)
		dhcpMgr := nl.NewDHCPManager()

		go func() {
			if _, err := dhcpMgr.RequestDHCPv6(name, false); err != nil {
				slog.Error("DHCPv6 failed", "device", name, "error", err)
			}
		}()
	}

	// 静态地址
	for _, addr := range cfg.Addresses {
		hasAddr, err := mgr.HasAddress(name, addr)
		if err != nil {
			slog.Warn("failed to check address", "device", name, "address", addr, "error", err)
			continue
		}

		if !hasAddr {
			slog.Info("adding address", "device", name, "address", addr)
			if err := mgr.AddAddress(name, addr); err != nil {
				slog.Warn("failed to add address", "device", name, "address", addr, "error", err)
			}
		}
	}

	// 静态网关
	if cfg.Gateway4 != "" {
		slog.Info("adding gateway4", "device", name, "gateway", cfg.Gateway4)
		if err := mgr.AddRoute("0.0.0.0/0", cfg.Gateway4, name, 0, 0); err != nil {
			slog.Warn("failed to add gateway4", "device", name, "error", err)
		}
	}

	if cfg.Gateway6 != "" {
		slog.Info("adding gateway6", "device", name, "gateway", cfg.Gateway6)
		if err := mgr.AddRoute("::/0", cfg.Gateway6, name, 0, 0); err != nil {
			slog.Warn("failed to add gateway6", "device", name, "error", err)
		}
	}

	// 路由
	for _, route := range cfg.Routes {
		if err := addRoute(mgr, name, route); err != nil {
			slog.Warn("failed to add route", "device", name, "route", route.To, "error", err)
		}
	}

	// 路由策略
	for _, policy := range cfg.RoutingPolicy {
		if err := addRoutingPolicy(mgr, policy); err != nil {
			slog.Warn("failed to add routing policy", "device", name, "error", err)
		}
	}

	return nil
}

// addRoutingPolicy 添加路由策略
func addRoutingPolicy(mgr *nl.NetlinkManager, policy *config.RoutingPolicy) error {
	slog.Info("adding routing policy", "from", policy.From, "to", policy.To, "table", policy.Table)
	return mgr.AddRule(policy.From, policy.To, policy.Table, policy.Priority, policy.Mark)
}

// addRoute 添加路由
func addRoute(mgr *nl.NetlinkManager, dev string, route *config.Route) error {
	to := route.To
	if to == "default" {
		to = "0.0.0.0/0"
	}
	// 如果没有 / 则加上
	if !strings.Contains(to, "/") && to != "0.0.0.0/0" {
		to = to + "/32"
	}

	slog.Info("adding route", "to", to, "via", route.Via, "device", dev, "table", route.Table)
	return mgr.AddRoute(to, route.Via, dev, route.Metric, route.Table)
}

// runPostScript 运行后置脚本
func runPostScript(nsName, script string) error {
	slog.Info("running post-script", "netns", nsName)

	// 使用 ip netns exec 运行脚本
	cmd := exec.Command("ip", "netns", "exec", nsName, "/bin/bash", "-c", script)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

// setupIpvlanDevices 配置 ipvlan 设备
func setupIpvlanDevices(mgr *nl.NetlinkManager, nsName string, devices map[string]*config.IpvlanDevice) error {
	names := sortedKeys(devices)

	for _, name := range names {
		cfg := devices[name]
		slog.Debug("setting up ipvlan device", "name", name, "netns", nsName, "link", cfg.Link)

		if !mgr.LinkExists(name) {
			// ipvlan 需要在父设备所在的 namespace 创建
			if nsName != "" {
				defaultMgr, err := nl.New()
				if err != nil {
					return err
				}

				slog.Info("creating ipvlan device in default namespace", "name", name, "link", cfg.Link)
				if err := defaultMgr.AddIpvlan(name, cfg.Link, cfg.Mode); err != nil {
					defaultMgr.Close()
					return fmt.Errorf("failed to create ipvlan %s: %w", name, err)
				}

				slog.Info("moving ipvlan to netns", "name", name, "netns", nsName)
				if err := defaultMgr.SetLinkNetns(name, nsName); err != nil {
					defaultMgr.Close()
					return fmt.Errorf("failed to move %s to netns %s: %w", name, nsName, err)
				}
				defaultMgr.Close()
			} else {
				slog.Info("creating ipvlan device", "name", name, "link", cfg.Link)
				if err := mgr.AddIpvlan(name, cfg.Link, cfg.Mode); err != nil {
					return fmt.Errorf("failed to create ipvlan %s: %w", name, err)
				}
			}
		}

		if err := setupDevice(mgr, name, cfg.Addresses, cfg.Routes, cfg.MTU, ""); err != nil {
			return fmt.Errorf("failed to setup ipvlan %s: %w", name, err)
		}
	}

	return nil
}

// setupTunnels 配置隧道设备
func setupTunnels(mgr *nl.NetlinkManager, nsName string, devices map[string]*config.Tunnel) error {
	names := sortedKeys(devices)

	for _, name := range names {
		cfg := devices[name]
		slog.Debug("setting up tunnel", "name", name, "netns", nsName, "mode", cfg.Mode)

		if !mgr.LinkExists(name) {
			slog.Info("creating tunnel device", "name", name, "mode", cfg.Mode)
			opts := &nl.TunnelOptions{
				Mode:      cfg.Mode,
				Local:     cfg.Local,
				Remote:    cfg.Remote,
				TTL:       cfg.TTL,
				Key:       cfg.Key,
				InputKey:  cfg.InputKey,
				OutputKey: cfg.OutputKey,
			}
			if err := mgr.AddTunnel(name, opts); err != nil {
				return fmt.Errorf("failed to create tunnel %s: %w", name, err)
			}
		}

		if err := setupDevice(mgr, name, cfg.Addresses, cfg.Routes, cfg.MTU, ""); err != nil {
			return fmt.Errorf("failed to setup tunnel %s: %w", name, err)
		}
	}

	return nil
}

// setupWireguards 配置 WireGuard 设备
func setupWireguards(mgr *nl.NetlinkManager, nsName string, devices map[string]*config.Wireguard) error {
	names := sortedKeys(devices)

	for _, name := range names {
		cfg := devices[name]
		slog.Debug("setting up wireguard", "name", name, "netns", nsName)

		// 1. 创建 WireGuard 设备
		if !mgr.LinkExists(name) {
			slog.Info("creating wireguard device", "name", name)
			if err := mgr.AddWireguard(name); err != nil {
				return fmt.Errorf("failed to create wireguard %s: %w", name, err)
			}
		}

		// 2. 配置 WireGuard (私钥、端口、peers)
		if cfg.PrivateKey != "" || len(cfg.Peers) > 0 {
			if err := configureWireguard(name, nsName, cfg); err != nil {
				return fmt.Errorf("failed to configure wireguard %s: %w", name, err)
			}
		}

		// 3. 配置 IP 地址和路由
		if err := setupDevice(mgr, name, cfg.Addresses, cfg.Routes, cfg.MTU, ""); err != nil {
			return fmt.Errorf("failed to setup wireguard %s: %w", name, err)
		}
	}

	return nil
}

// configureWireguard 配置 WireGuard 密钥和 peers
func configureWireguard(name, nsName string, cfg *config.Wireguard) error {
	// 如果在 netns 中，需要在该 netns 中执行配置
	if nsName != "" {
		return nl.RunInNetns(nsName, func() error {
			return doConfigureWireguard(name, cfg)
		})
	}
	return doConfigureWireguard(name, cfg)
}

// doConfigureWireguard 实际执行 WireGuard 配置
func doConfigureWireguard(name string, cfg *config.Wireguard) error {
	wgMgr, err := nl.NewWireGuardManager()
	if err != nil {
		return fmt.Errorf("failed to create wireguard manager: %w", err)
	}
	defer wgMgr.Close()

	// 构建 WireGuard 配置
	wgCfg := &nl.WireGuardConfig{
		PrivateKey:   cfg.PrivateKey,
		ListenPort:   cfg.ListenPort,
		FwMark:       cfg.FwMark,
		ReplacePeers: true, // 替换所有 peers
	}

	// 添加 peers
	for _, peer := range cfg.Peers {
		wgPeer := &nl.WireGuardPeer{
			PublicKey:                   peer.PublicKey,
			PresharedKey:                peer.PresharedKey,
			Endpoint:                    peer.Endpoint,
			AllowedIPs:                  peer.AllowedIPs,
			PersistentKeepaliveInterval: peer.PersistentKeepalive,
		}
		wgCfg.Peers = append(wgCfg.Peers, wgPeer)
	}

	slog.Info("configuring wireguard", "device", name, "listen-port", cfg.ListenPort, "peers", len(cfg.Peers))
	return wgMgr.ConfigureDevice(name, wgCfg)
}

// setupTunTapDevices 配置 TUN/TAP 设备
func setupTunTapDevices(mgr *nl.NetlinkManager, nsName string, devices map[string]*config.TunTapDevice, isTap bool) error {
	names := sortedKeys(devices)
	deviceType := "tun"
	if isTap {
		deviceType = "tap"
	}

	for _, name := range names {
		cfg := devices[name]
		slog.Debug("setting up "+deviceType+" device", "name", name, "netns", nsName)

		if !mgr.LinkExists(name) {
			slog.Info("creating "+deviceType+" device", "name", name)
			var err error
			if isTap {
				err = mgr.AddTap(name)
			} else {
				err = mgr.AddTun(name)
			}
			if err != nil {
				return fmt.Errorf("failed to create %s %s: %w", deviceType, name, err)
			}
		}

		if err := setupDevice(mgr, name, cfg.Addresses, cfg.Routes, cfg.MTU, ""); err != nil {
			return fmt.Errorf("failed to setup %s %s: %w", deviceType, name, err)
		}
	}

	return nil
}

// sortedKeys 获取 map 的排序后的 key
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

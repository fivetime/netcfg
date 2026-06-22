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

	// 1.5 回收已不在配置中的 SRv6 本地 SID / NDP 代理（状态对比，不走设备 diff）
	reapSRv6Orphans(oldState, newState)
	reapNDProxyOrphans(oldState, newState)

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

	// 3. 处理 default namespace 的设备（VPP 设备分流到 VPP applier，其余走内核）
	if cfg.HasDefaultNamespaceConfig() {
		slog.Info("configuring default namespace")
		kernelNS, vppDevs := splitVPPDevices(cfg.Network.ToNamespace(), cfg.Network.Renderer)
		if !vppDevs.empty() || cfg.Network.VPP != nil {
			if err := setupVPP(cfg.Network.VPP, vppDevs); err != nil {
				return fmt.Errorf("failed to configure VPP devices: %w", err)
			}
		}
		if err := applyNamespaceConfig("", kernelNS); err != nil {
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

// reapSRv6Orphans 删除 oldState 有、newState 无的 SRv6 本地 SID（按 netns）。
func reapSRv6Orphans(oldState, newState *state.State) {
	for ns, oldNs := range oldState.Namespaces {
		if oldNs == nil || len(oldNs.SRv6SIDs) == 0 {
			continue
		}
		keep := map[string]bool{}
		if newNs := newState.Namespaces[ns]; newNs != nil {
			for _, sid := range newNs.SRv6SIDs {
				keep[sid] = true
			}
		}
		var removed []string
		for _, sid := range oldNs.SRv6SIDs {
			if !keep[sid] {
				removed = append(removed, sid)
			}
		}
		if len(removed) == 0 {
			continue
		}

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
			slog.Warn("failed to get manager for srv6 sid removal", "ns", ns, "error", err)
			continue
		}
		for _, sid := range removed {
			slog.Info("removing srv6 local sid", "sid", sid, "netns", ns)
			if err := mgr.DeleteLocalSID(sid); err != nil {
				slog.Warn("failed to remove srv6 local sid", "sid", sid, "error", err)
			}
		}
		mgr.Close()
	}
}

// reapNDProxyOrphans 删除 oldState 有、newState 无的内核 NDP 代理条目（按 netns/设备）。
func reapNDProxyOrphans(oldState, newState *state.State) {
	for ns, oldNs := range oldState.Namespaces {
		if oldNs == nil {
			continue
		}
		// 收集本 ns 新配置仍需保留的 (device,ip)
		keep := map[string]bool{}
		if newNs := newState.Namespaces[ns]; newNs != nil {
			for dev, ds := range newNs.Devices {
				for _, ip := range ds.NDProxy {
					keep[dev+"|"+ip] = true
				}
			}
		}
		// 找出需删除的
		removals := map[string][]string{} // device -> []ip
		for dev, ds := range oldNs.Devices {
			for _, ip := range ds.NDProxy {
				if !keep[dev+"|"+ip] {
					removals[dev] = append(removals[dev], ip)
				}
			}
		}
		if len(removals) == 0 {
			continue
		}

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
			slog.Warn("failed to get manager for nd-proxy removal", "ns", ns, "error", err)
			continue
		}
		for dev, ips := range removals {
			for _, ip := range ips {
				slog.Info("removing nd-proxy", "device", dev, "ip", ip, "netns", ns)
				if err := mgr.DeleteProxyNDP(dev, ip); err != nil {
					slog.Warn("failed to remove nd-proxy", "device", dev, "ip", ip, "error", err)
				}
			}
		}
		mgr.Close()
	}
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
			Addresses: addrStrings(cfg.Addresses),
			Routes:    routesToStrings(cfg.Routes),
			NDProxy:   ndProxyAddrs(cfg.NDProxy),
			CreatedBy: "system", // 物理设备
		}
	}

	// Dummy
	for name, cfg := range ns.DummyDevices {
		nsState.Devices[name] = &state.DeviceState{
			Type:      "dummy",
			Addresses: addrStrings(cfg.Addresses),
			Routes:    routesToStrings(cfg.Routes),
			CreatedBy: "netcfg",
		}
	}

	// Bridges
	for name, cfg := range ns.Bridges {
		nsState.Devices[name] = &state.DeviceState{
			Type:      "bridge",
			Addresses: addrStrings(cfg.Addresses),
			Routes:    routesToStrings(cfg.Routes),
			NDProxy:   ndProxyAddrs(cfg.NDProxy),
			CreatedBy: "netcfg",
		}
	}

	// Bonds
	for name, cfg := range ns.Bonds {
		nsState.Devices[name] = &state.DeviceState{
			Type:      "bond",
			Addresses: addrStrings(cfg.Addresses),
			Routes:    routesToStrings(cfg.Routes),
			NDProxy:   ndProxyAddrs(cfg.NDProxy),
			CreatedBy: "netcfg",
		}
	}

	// VLANs
	for name, cfg := range ns.Vlans {
		nsState.Devices[name] = &state.DeviceState{
			Type:      "vlan",
			Addresses: addrStrings(cfg.Addresses),
			Routes:    routesToStrings(cfg.Routes),
			NDProxy:   ndProxyAddrs(cfg.NDProxy),
			CreatedBy: "netcfg",
		}
	}

	// VXLANs
	for name, cfg := range ns.Vxlans {
		nsState.Devices[name] = &state.DeviceState{
			Type:      "vxlan",
			Addresses: addrStrings(cfg.Addresses),
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
			Addresses: addrStrings(cfg.Addresses),
			Routes:    routesToStrings(cfg.Routes),
			CreatedBy: "netcfg",
		}
	}

	// 注：WireGuard 设备经 tunnels:mode:wireguard 处理，已在 Tunnels 循环中跟踪。

	// WiFi（物理 wlan，CreatedBy=system，不删除）
	for name, cfg := range ns.Wifis {
		nsState.Devices[name] = &state.DeviceState{
			Type:      "wifi",
			Addresses: addrStrings(cfg.Addresses),
			Routes:    routesToStrings(cfg.Routes),
			CreatedBy: "system",
		}
	}

	// Veth
	for name, cfg := range ns.VethDevices {
		nsState.Devices[name] = &state.DeviceState{
			Type:      "veth",
			Addresses: addrStrings(cfg.Addresses),
			Routes:    routesToStrings(cfg.Routes),
			CreatedBy: "netcfg",
		}
	}

	// virtual-ethernets（netplan 标准 veth）
	for name, cfg := range ns.VirtualEthernets {
		nsState.Devices[name] = &state.DeviceState{
			Type:      "veth",
			Addresses: addrStrings(cfg.Addresses),
			Routes:    routesToStrings(cfg.Routes),
			CreatedBy: "netcfg",
		}
	}

	// Macvlan
	for name, cfg := range ns.MacvlanDevices {
		nsState.Devices[name] = &state.DeviceState{
			Type:      "macvlan",
			Addresses: addrStrings(cfg.Addresses),
			Routes:    routesToStrings(cfg.Routes),
			CreatedBy: "netcfg",
		}
	}

	// Ipvlan
	for name, cfg := range ns.IpvlanDevices {
		nsState.Devices[name] = &state.DeviceState{
			Type:      "ipvlan",
			Addresses: addrStrings(cfg.Addresses),
			Routes:    routesToStrings(cfg.Routes),
			CreatedBy: "netcfg",
		}
	}

	// TUN
	for name, cfg := range ns.TunDevices {
		nsState.Devices[name] = &state.DeviceState{
			Type:      "tun",
			Addresses: addrStrings(cfg.Addresses),
			Routes:    routesToStrings(cfg.Routes),
			CreatedBy: "netcfg",
		}
	}

	// TAP
	for name, cfg := range ns.TapDevices {
		nsState.Devices[name] = &state.DeviceState{
			Type:      "tap",
			Addresses: addrStrings(cfg.Addresses),
			Routes:    routesToStrings(cfg.Routes),
			CreatedBy: "netcfg",
		}
	}

	// SRv6 本地 SID（用于增量回收）
	if ns.SRv6 != nil {
		for _, s := range ns.SRv6.LocalSIDs {
			nsState.SRv6SIDs = append(nsState.SRv6SIDs, s.SID)
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

	// 1b. WiFi 设备（生成 wpa_supplicant 配置 + 设备级地址/路由）
	if err := setupWifis(mgr, nsName, cfg.Wifis); err != nil {
		return fmt.Errorf("failed to setup wifis: %w", err)
	}

	// 2. Dummy 设备
	if err := setupDummyDevices(mgr, nsName, cfg.DummyDevices); err != nil {
		return fmt.Errorf("failed to setup dummy devices: %w", err)
	}

	// 2.5 virtual-ethernets（netplan 标准 veth，两端互引）。
	// 须在 bond/bridge 之前创建——其端点常作为 bridge/bond 成员被 enslave。
	if err := setupVirtualEthernets(mgr, nsName, cfg.VirtualEthernets); err != nil {
		return fmt.Errorf("failed to setup virtual-ethernets: %w", err)
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

	// 7b. VXLAN neigh-suppress（brport 属性，须在 vxlan 加入 bridge 之后设置）
	applyVxlanNeighSuppress(mgr, cfg.Vxlans)

	// 7c. 成员设备 brport 属性（neigh-suppress/hairpin/port-mac-learning，须在 enslave 之后）
	applyBridgePortAttrs(mgr, cfg.Ethernets)

	// 8. VRF 设备
	if err := setupVrfs(mgr, nsName, cfg.Vrfs); err != nil {
		return fmt.Errorf("failed to setup vrfs: %w", err)
	}

	// 8.5. Tunnel 设备
	if err := setupTunnels(mgr, nsName, cfg.Tunnels); err != nil {
		return fmt.Errorf("failed to setup tunnels: %w", err)
	}

	// 注：WireGuard 走 tunnels:mode:wireguard（见 setupTunnels），无独立步骤。

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

	// 9.5 SRv6（seg6_enabled + 本地 SID）。放在所有设备之后：oif/iif 须已存在。
	if err := setupSRv6(mgr, nsName, cfg.SRv6); err != nil {
		return fmt.Errorf("failed to setup srv6: %w", err)
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

// resolveEthernetName 根据 match / set-name 解析 ethernet 配置项对应的实际设备名。
//
//   - 有 match：按 driver/mac/name 在既有设备中查找；找到后，若指定了 set-name 或
//     设备名与配置键不同，则重命名为目标名（set-name 优先，否则用配置键）。无匹配返回 ""。
//   - 无 match：配置键即设备名；若指定了 set-name 且与键不同且设备存在，则重命名。
//
// 注意：match 主要用于 default namespace 的物理网卡；driver 匹配依赖 host /sys。
func resolveEthernetName(mgr *nl.NetlinkManager, key string, cfg *config.Ethernet) (string, error) {
	if cfg.Match == nil || (cfg.Match.Name == "" && cfg.Match.MacAddress == "" && cfg.Match.Driver == "") {
		// 无 match：键即设备名，按需处理 set-name
		if cfg.SetName != "" && cfg.SetName != key && mgr.LinkExists(key) {
			slog.Info("renaming interface", "from", key, "to", cfg.SetName)
			if err := mgr.RenameLink(key, cfg.SetName); err != nil {
				return "", err
			}
			return cfg.SetName, nil
		}
		return key, nil
	}

	matched, err := mgr.FindMatchingLink(nl.MatchCriteria{
		Name:       cfg.Match.Name,
		MacAddress: cfg.Match.MacAddress,
		Driver:     cfg.Match.Driver,
	})
	if err != nil {
		return "", err
	}
	if matched == "" {
		return "", nil
	}

	target := key
	if cfg.SetName != "" {
		target = cfg.SetName
	}
	if matched != target {
		slog.Info("renaming matched interface", "from", matched, "to", target,
			"match", fmt.Sprintf("%+v", *cfg.Match))
		if err := mgr.RenameLink(matched, target); err != nil {
			return "", err
		}
	}
	return target, nil
}

// setupEthernets 配置以太网设备
func setupEthernets(mgr *nl.NetlinkManager, nsName string, devices map[string]*config.Ethernet) error {
	// 按名称排序确保顺序一致
	names := sortedKeys(devices)

	// SR-IOV 预处理：先在 PF 上创建 VF / 设 eswitch，使 VF netdev 在后续配置前尽量就绪
	for _, key := range names {
		cfg := devices[key]
		if cfg.VirtualFunctionCount > 0 || cfg.EmbeddedSwitchMode != "" || cfg.DelayVFRebind != nil {
			if mgr.LinkExists(key) {
				applySRIOV(key, cfg)
			} else {
				slog.Warn("SR-IOV PF not present; skipping", "device", key)
			}
		}
	}

	for _, key := range names {
		cfg := devices[key]
		slog.Debug("setting up ethernet", "id", key, "netns", nsName)

		// 解析实际设备名：处理 match（按 driver/mac/name 匹配既有设备）与
		// set-name（将匹配到的设备重命名）。无 match 时配置键即设备名（原行为）。
		name, err := resolveEthernetName(mgr, key, cfg)
		if err != nil {
			slog.Warn("failed to resolve ethernet device", "id", key, "error", err)
			continue
		}
		if name == "" {
			slog.Warn("no device matched; skipping ethernet config", "id", key)
			continue
		}

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

		// 物理网卡由 netcfg 无法创建：设备缺失时告警跳过，不中断整体 apply
		// （契合 netplan optional 语义；缺一块网卡不应导致其余配置全部失败）。
		if !mgr.LinkExists(name) {
			slog.Warn("ethernet device not present; skipping", "device", name, "id", key)
			continue
		}

		// 使用支持 DHCP 的配置函数（单设备失败仅告警，不中断其余设备）
		if err := setupDeviceWithDHCP(mgr, name, cfg); err != nil {
			slog.Warn("failed to setup ethernet", "device", name, "error", err)
		}

		// 网卡 offload（best-effort，经 ethtool ioctl / 纯 Go）
		applyOffload(mgr, name, cfg)

		// 其它物理网卡杂项：wakeonlan / infiniband-mode / emit-lldp
		applyEthernetExtras(mgr, name, cfg)

		// 内核 NDP 代理（proxy_ndp + NTF_PROXY 邻居）
		applyNDProxy(mgr, name, cfg.NDProxy)

		// 802.1X / EAP：生成 wpa_supplicant 配置并直接拉起（init-agnostic）
		if cfg.Auth != nil {
			setup8021x(name, cfg.Auth)
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
		applyNameservers(mgr, name, cfg.Nameservers)
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
		applyNameservers(mgr, name, cfg.Nameservers)
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
				// 父设备缺失等导致创建失败：告警跳过，不中断整体 apply
				slog.Warn("failed to create vlan; skipping", "vlan", name, "link", cfg.Link, "error", err)
				continue
			}
		}

		if err := setupDevice(mgr, name, cfg.Addresses, cfg.Routes, cfg.MTU, cfg.MacAddress); err != nil {
			slog.Warn("failed to setup vlan", "vlan", name, "error", err)
			continue
		}
		applyNameservers(mgr, name, cfg.Nameservers)
		applyNDProxy(mgr, name, cfg.NDProxy)
	}

	return nil
}

// setupVxlans 配置 VXLAN 设备
func setupVxlans(mgr *nl.NetlinkManager, nsName string, devices map[string]*config.Vxlan) error {
	for _, name := range sortedKeys(devices) {
		if err := setupOneVxlan(mgr, nsName, name, devices[name]); err != nil {
			return err
		}
	}
	return nil
}

// applyVxlanNeighSuppress 为启用了 neigh-suppress 的 VXLAN 设置 brport 属性。
// 须在 VXLAN 已加入 bridge 之后调用（neigh_suppress 是 bridge 端口属性）。
func applyVxlanNeighSuppress(mgr *nl.NetlinkManager, vxlans map[string]*config.Vxlan) {
	for _, name := range sortedKeys(vxlans) {
		cfg := vxlans[name]
		if cfg.NeighSuppress != nil && *cfg.NeighSuppress {
			slog.Info("enabling neigh suppress", "device", name)
			if err := mgr.SetLinkNeighSuppress(name, true); err != nil {
				slog.Warn("failed to set neigh suppress", "device", name, "error", err)
			}
		}
	}
}

// applyBridgePortAttrs 在成员设备 enslave 到 bridge 之后，应用其 brport 属性
// （neigh-suppress / hairpin / port-mac-learning）。须在 setupBridges 之后调用——
// brport 目录在 enslave 后才存在。仅显式设置（非 nil）的字段才下发；设在非桥接端口
// 的设备上会失败 → 告警（属用户配置错误，不静默）。
func applyBridgePortAttrs(mgr *nl.NetlinkManager, devices map[string]*config.Ethernet) {
	for _, name := range sortedKeys(devices) {
		cfg := devices[name]
		if cfg == nil {
			continue
		}
		if cfg.NeighSuppress != nil {
			if err := mgr.SetLinkNeighSuppress(name, *cfg.NeighSuppress); err != nil {
				slog.Warn("failed to set neigh-suppress (not a bridge port?)", "device", name, "error", err)
			}
		}
		if cfg.Hairpin != nil {
			if err := mgr.SetBridgePortHairpin(name, *cfg.Hairpin); err != nil {
				slog.Warn("failed to set hairpin (not a bridge port?)", "device", name, "error", err)
			}
		}
		if cfg.PortMacLearning != nil {
			if err := mgr.SetLinkLearning(name, *cfg.PortMacLearning); err != nil {
				slog.Warn("failed to set port-mac-learning (not a bridge port?)", "device", name, "error", err)
			}
		}
	}
}

// setupOneVxlan 创建并配置单个 VXLAN 设备。供 setupVxlans（netcfg 自有 vxlans:）
// 与 setupTunnels（netplan 标准 tunnels:mode:vxlan）共用。
func setupOneVxlan(mgr *nl.NetlinkManager, nsName, name string, cfg *config.Vxlan) error {
	{
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

		// 注：neigh-suppress 是 brport 属性，须在 vxlan 加入 bridge 后设置，
		// 故移至 applyNamespaceConfig 中 setupBridges 之后统一处理。

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
// bondOptionsFromConfig 把 config.BondParameters 映射为 netlink 层的 BondOptions。
// 传入 nil 时返回零值 options（AddBond 会按内核默认创建 balance-rr bond）。
func bondOptionsFromConfig(p *config.BondParameters) *nl.BondOptions {
	if p == nil {
		return &nl.BondOptions{}
	}
	return &nl.BondOptions{
		Mode:                  p.Mode,
		LacpRate:              p.LACPRate,
		MIIMonitorInterval:    p.MIIMonitorInterval,
		MinLinks:              p.MinLinks,
		TransmitHashPolicy:    p.TransmitHashPolicy,
		ADSelect:              p.ADSelect,
		AllSlavesActive:       p.AllSlavesActive,
		ARPInterval:           p.ARPInterval,
		ARPIPTargets:          p.ARPIPTargets,
		ARPValidate:           p.ARPValidate,
		ARPAllTargets:         p.ARPAllTargets,
		UpDelay:               p.UpDelay,
		DownDelay:             p.DownDelay,
		FailOverMACPolicy:     p.FailOverMACPolicy,
		GratuitousARP:         p.GratuitousARP,
		PacketsPerSlave:       p.PacketsPerSlave,
		PrimaryReselectPolicy: p.PrimaryReselectPolicy,
		ResendIGMP:            p.ResendIGMP,
		LearnPacketInterval:   p.LearnPacketInterval,
		Primary:               p.Primary,
	}
}

// bridgeOptionsFromConfig 把 config.BridgeParameters 的设备级 STP 参数映射为
// netlink 层的 BridgeOptions（不含 path-cost/port-priority 等每端口参数）。
func bridgeOptionsFromConfig(p *config.BridgeParameters) *nl.BridgeOptions {
	if p == nil {
		return &nl.BridgeOptions{}
	}
	return &nl.BridgeOptions{
		STP:          p.STP,
		ForwardDelay: p.ForwardDelay,
		HelloTime:    p.HelloTime,
		MaxAge:       p.MaxAge,
		AgeingTime:   p.AgeingTime,
		Priority:     p.Priority,
	}
}

func setupBonds(mgr *nl.NetlinkManager, nsName string, devices map[string]*config.Bond) error {
	names := sortedKeys(devices)

	for _, name := range names {
		cfg := devices[name]
		slog.Debug("setting up bond", "name", name, "netns", nsName)

		if !mgr.LinkExists(name) {
			opts := bondOptionsFromConfig(cfg.Parameters)
			slog.Info("creating bond device", "name", name, "mode", opts.Mode)
			if err := mgr.AddBond(name, opts); err != nil {
				return fmt.Errorf("failed to create bond %s: %w", name, err)
			}
		} else if cfg.Parameters != nil {
			// bond 已存在：多数参数要求在无 slave 时设置，无法安全热更新。
			// 提示用户需重建设备才能应用新参数（避免静默忽略）。
			slog.Warn("bond already exists; parameter changes require recreating the device and are not applied",
				"name", name)
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
		applyNameservers(mgr, name, cfg.Nameservers)
		applyNDProxy(mgr, name, cfg.NDProxy)
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

		// 设备级 STP 参数（sysfs 可热更新，无论设备是否新建都应用）
		if cfg.Parameters != nil {
			slog.Info("setting bridge parameters", "bridge", name)
			if err := mgr.SetBridgeParameters(name, bridgeOptionsFromConfig(cfg.Parameters)); err != nil {
				slog.Warn("failed to set bridge parameters", "bridge", name, "error", err)
			}
		}

		// 添加接口到 bridge
		for _, iface := range cfg.Interfaces {
			slog.Debug("adding interface to bridge", "interface", iface, "bridge", name)
			if err := mgr.SetBridgeMaster(iface, name); err != nil {
				slog.Warn("failed to add interface to bridge", "interface", iface, "bridge", name, "error", err)
			}
		}

		// 每端口 STP 参数（path-cost / port-priority）：须在端口加入网桥后设置
		if cfg.Parameters != nil {
			for port, cost := range cfg.Parameters.PathCost {
				slog.Info("setting bridge port path-cost", "bridge", name, "port", port, "cost", cost)
				if err := mgr.SetBridgePortPathCost(port, cost); err != nil {
					slog.Warn("failed to set port path-cost", "port", port, "error", err)
				}
			}
			for port, prio := range cfg.Parameters.PortPriority {
				slog.Info("setting bridge port priority", "bridge", name, "port", port, "priority", prio)
				if err := mgr.SetBridgePortPriority(port, prio); err != nil {
					slog.Warn("failed to set port priority", "port", port, "error", err)
				}
			}
		}

		if err := setupDevice(mgr, name, cfg.Addresses, cfg.Routes, cfg.MTU, cfg.MacAddress); err != nil {
			return fmt.Errorf("failed to setup bridge %s: %w", name, err)
		}
		applyNameservers(mgr, name, cfg.Nameservers)
		applyNDProxy(mgr, name, cfg.NDProxy)

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

		// VRF 级策略路由
		for _, policy := range cfg.RoutingPolicy {
			if err := addRoutingPolicy(mgr, policy); err != nil {
				slog.Warn("failed to add routing policy", "vrf", name, "error", err)
			}
		}
	}

	return nil
}

// setupVirtualEthernets 配置 netplan 标准的 virtual-ethernets。
// 两个端点是互相引用的顶层条目；从其中一端创建 veth pair，再分别配置两端，
// 用 done 去重避免重复创建。
func setupVirtualEthernets(mgr *nl.NetlinkManager, nsName string, devices map[string]*config.VirtualEthernet) error {
	done := make(map[string]bool)

	for _, name := range sortedKeys(devices) {
		if done[name] {
			continue
		}
		cfg := devices[name]
		peer := cfg.Peer
		if peer == "" {
			return fmt.Errorf("virtual-ethernet %s: peer is required", name)
		}

		if !mgr.LinkExists(name) {
			slog.Info("creating virtual-ethernet pair", "name", name, "peer", peer, "netns", nsName)
			if err := mgr.AddVethPair(name, peer); err != nil {
				return fmt.Errorf("failed to create virtual-ethernet %s<->%s: %w", name, peer, err)
			}
		}
		done[name] = true
		done[peer] = true

		// 配置本端
		if err := setupDevice(mgr, name, cfg.Addresses, cfg.Routes, cfg.MTU, cfg.MacAddress); err != nil {
			return fmt.Errorf("failed to setup virtual-ethernet %s: %w", name, err)
		}
		applyNameservers(mgr, name, cfg.Nameservers)

		// 配置对端（若它也有独立的配置条目）
		if peerCfg, ok := devices[peer]; ok {
			if err := setupDevice(mgr, peer, peerCfg.Addresses, peerCfg.Routes, peerCfg.MTU, peerCfg.MacAddress); err != nil {
				slog.Warn("failed to setup virtual-ethernet peer", "peer", peer, "error", err)
			}
			applyNameservers(mgr, peer, peerCfg.Nameservers)
		} else if err := mgr.SetLinkUp(peer); err != nil {
			slog.Warn("failed to bring up virtual-ethernet peer", "peer", peer, "error", err)
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
		applyNameservers(mgr, name, cfg.Nameservers)

		// 配置对端：在对端实际所处的 namespace 里配置。
		//   peerNs != ""        → 对端在具名 netns
		//   peerNs == "" 且本端在 netns → 对端在 default namespace（用 nl.New()）
		//   peerNs == "" 且本端在 default → 对端与本端同处 default（复用 mgr）
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
			// 对端在同一个 default namespace，复用 mgr
			if err := setupDevice(mgr, peerName, cfg.Peer.Addresses, cfg.Peer.Routes, cfg.Peer.MTU, cfg.Peer.MacAddress); err != nil {
				return fmt.Errorf("failed to setup veth peer %s: %w", peerName, err)
			}
		} else {
			// 本端在 netns、对端在 default namespace（peer.netns: ""）
			defaultMgr, err := nl.New()
			if err != nil {
				return fmt.Errorf("failed to get default namespace for peer %s: %w", peerName, err)
			}
			if err := setupDevice(defaultMgr, peerName, cfg.Peer.Addresses, cfg.Peer.Routes, cfg.Peer.MTU, cfg.Peer.MacAddress); err != nil {
				defaultMgr.Close()
				return fmt.Errorf("failed to setup veth peer %s: %w", peerName, err)
			}
			defaultMgr.Close()
		}
	}

	return nil
}

// setupDevice 配置设备的通用函数
// applyNameservers 应用设备的静态 DNS（nameserver 地址 + search 域）。
// ns 为 nil 或空时跳过；失败仅告警不阻塞（与其他设备配置一致）。
func applyNameservers(mgr *nl.NetlinkManager, name string, ns *config.Nameservers) {
	if ns == nil || (len(ns.Addresses) == 0 && len(ns.Search) == 0) {
		return
	}
	slog.Info("applying nameservers", "device", name, "addresses", ns.Addresses, "search", ns.Search)
	if err := nl.ApplyDNS(name, ns.Addresses, ns.Search); err != nil {
		slog.Warn("failed to apply nameservers", "device", name, "error", err)
	}
}

// addrStrings 提取地址的 CIDR 列表（用于 state 跟踪，state 仍以字符串存储地址）。
func addrStrings(addrs []config.Address) []string {
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, a.CIDR)
	}
	return out
}

// dhcpOverridesToNl 把 config.DHCPOverrides 映射为 netlink 应用侧 overrides。
// nil 时返回 netplan 默认（use-dns/use-mtu/use-routes=true，use-domains=false）。
// use-ntp/use-hostname 因 netcfg 不配置 NTP / 系统主机名而为 no-op（显式设置时告警）。
func dhcpOverridesToNl(o *config.DHCPOverrides) *nl.DHCPOverrides {
	res := &nl.DHCPOverrides{UseDNS: true, UseMTU: true, UseRoutes: true, UseDomains: false}
	if o == nil {
		return res
	}
	if o.UseDNS != nil {
		res.UseDNS = *o.UseDNS
	}
	if o.UseMTU != nil {
		res.UseMTU = *o.UseMTU
	}
	if o.UseRoutes != nil {
		res.UseRoutes = *o.UseRoutes
	}
	res.RouteMetric = o.RouteMetric
	res.UseDomains = parseUseDomains(o.UseDomains)
	if o.UseNTP != nil {
		slog.Warn("dhcp override use-ntp is not honored (netcfg does not configure NTP)")
	}
	if o.UseHostname != nil {
		slog.Warn("dhcp override use-hostname is not honored (netcfg does not set system hostname)")
	}
	return res
}

// parseUseDomains 解析 use-domains（bool 或特殊值 "route"）。
// true -> 把 DHCP 域名作为 DNS search 域；false/"route" -> 不作为 search
// （"route" 的「仅用于路由查询」语义无法用普通 resolv.conf 表达）。
func parseUseDomains(v interface{}) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return strings.EqualFold(x, "true")
	}
	return false
}

func setupDevice(mgr *nl.NetlinkManager, name string, addresses []config.Address, routes []*config.Route, mtu int, mac string) error {
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
		hasAddr, err := mgr.HasAddress(name, addr.CIDR)
		if err != nil {
			slog.Warn("failed to check address", "device", name, "address", addr.CIDR, "error", err)
			continue
		}

		if !hasAddr {
			slog.Info("adding address", "device", name, "address", addr.CIDR, "label", addr.Label, "lifetime", addr.Lifetime)
			if err := mgr.AddAddressOpts(name, addr.CIDR, addr.Label, addr.Lifetime); err != nil {
				slog.Warn("failed to add address", "device", name, "address", addr.CIDR, "error", err)
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

	// IPv6 专属 MTU（sysctl，独立于设备 MTU）
	if cfg.IPv6MTU > 0 {
		if err := nl.SetIPv6MTU(name, cfg.IPv6MTU); err != nil {
			slog.Warn("failed to set ipv6 mtu", "device", name, "ipv6-mtu", cfg.IPv6MTU, "error", err)
		}
	}

	if cfg.MacAddress != "" {
		if err := mgr.SetLinkMacAddress(name, cfg.MacAddress); err != nil {
			slog.Warn("failed to set mac", "device", name, "mac", cfg.MacAddress, "error", err)
		}
	}

	// activation-mode：manual/off 不自动 up（off 额外强制 down），交管理员控制。
	// 其余配置（地址等）仍下发——「配置但不激活」。
	switch strings.ToLower(cfg.ActivationMode) {
	case "off":
		slog.Info("activation-mode=off; forcing link down", "device", name)
		if err := mgr.SetLinkDown(name); err != nil {
			slog.Warn("failed to set link down", "device", name, "error", err)
		}
	case "manual":
		slog.Info("activation-mode=manual; not bringing link up (admin-controlled)", "device", name)
	default:
		if err := mgr.SetLinkUp(name); err != nil {
			return fmt.Errorf("failed to set %s up: %w", name, err)
		}
	}

	// ignore-carrier / critical：netcfg 直接经 netlink 下发地址，不检测 carrier，
	// 也不随 carrier 丢失/重启清除配置——这两个 networkd 概念在直接 netlink 架构下
	// 本就等价满足，无需特殊处理（记 debug，避免被误解为静默忽略）。
	if cfg.IgnoreCarrier != nil && *cfg.IgnoreCarrier {
		slog.Debug("ignore-carrier: netcfg programs addresses regardless of carrier (already equivalent)", "device", name)
	}
	if cfg.Critical != nil && *cfg.Critical {
		slog.Debug("critical: netcfg does not release addresses on carrier loss/restart (already equivalent)", "device", name)
	}
	// optional-addresses：影响 online 等待粒度（地址类型级）。netcfg 的 wait-online
	// 为链路级基本判定，暂不按地址类型细分；schema 已保留，记 debug。
	if len(cfg.OptionalAddresses) > 0 {
		slog.Debug("optional-addresses parsed; netcfg wait-online is link-level (per-address-type online not yet refined)",
			"device", name, "types", strings.Join(cfg.OptionalAddresses, ","))
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

	// ra-overrides：netcfg 使用内核 RA，无用户态 RA 客户端，无法消费 RA 的
	// DNS/域名，也无法将 RA 路由重定向到自定义表（这些是 networkd 后端特性）。
	// 显式告警，避免静默忽略；schema 已保留，待引入 RA 客户端后实现。
	if cfg.RAOverrides != nil {
		slog.Warn("ra-overrides is not honored: netcfg uses kernel RA (accept-ra) with no userspace RA client; "+
			"use-dns/use-domains/table are ignored (these require the networkd back end)", "device", name)
	}

	// 处理 IPv6 隐私扩展（临时地址）。须在 SLAAC 之后，使显式设置覆盖
	// EnableSLAAC 默认写入的 use_tempaddr=2。
	// true -> use_tempaddr=2（启用，偏好临时地址）；false -> 0（禁用）。
	if cfg.IPv6Privacy != nil {
		value := 0
		if *cfg.IPv6Privacy {
			value = 2
		}
		slog.Info("setting IPv6 privacy extensions", "device", name, "enabled", *cfg.IPv6Privacy)
		if err := nl.SetIPv6Privacy(name, value); err != nil {
			slog.Warn("failed to set IPv6 privacy", "device", name, "error", err)
		}
	}

	// 链路本地地址：netplan 默认 [ipv6]。仅在用户显式设置（含空列表）时处理。
	// IPv6 LL 通过 addr_gen_mode 控制；IPv4 LL（169.254 zeroconf）无直接 netlink/
	// sysctl 开关，显式告警不支持，避免静默忽略。
	if cfg.LinkLocal != nil {
		wantV4, wantV6 := false, false
		for _, f := range cfg.LinkLocal {
			switch strings.ToLower(f) {
			case "ipv4":
				wantV4 = true
			case "ipv6":
				wantV6 = true
			}
		}
		slog.Info("setting link-local addressing", "device", name, "ipv6", wantV6, "ipv4", wantV4)
		if err := nl.SetLinkLocalIPv6(name, wantV6); err != nil {
			slog.Warn("failed to set ipv6 link-local", "device", name, "error", err)
		}
		if wantV4 {
			slog.Warn("ipv4 link-local addressing is not supported (no direct netlink/sysctl control); ignoring", "device", name)
		}
	}

	// ipv6-address-generation / ipv6-address-token（netplan 互斥）。须在 link-local
	// 之后——generation 也写 addr_gen_mode，应覆盖 link-local 的默认值。
	if cfg.IPv6AddrToken != "" && cfg.IPv6AddrGen != "" {
		slog.Warn("ipv6-address-token and ipv6-address-generation are mutually exclusive; using token", "device", name)
	}
	if cfg.IPv6AddrToken != "" {
		slog.Info("setting ipv6 address token", "device", name, "token", cfg.IPv6AddrToken)
		if err := mgr.SetIPv6Token(name, cfg.IPv6AddrToken); err != nil {
			slog.Warn("failed to set ipv6-address-token", "device", name, "error", err)
		}
	} else if cfg.IPv6AddrGen != "" {
		slog.Info("setting ipv6 address generation mode", "device", name, "mode", cfg.IPv6AddrGen)
		if err := nl.SetIPv6AddrGenMode(name, cfg.IPv6AddrGen); err != nil {
			slog.Warn("failed to set ipv6-address-generation", "device", name, "error", err)
		}
	}

	// 处理 DHCPv4
	if cfg.DHCP4 {
		slog.Info("starting DHCPv4", "device", name)
		dhcpMgr := nl.NewDHCPManager()
		ov := dhcpOverridesToNl(cfg.DHCP4Overrides)

		// dhcp-identifier（mac/duid）：client-id 来源
		if cfg.DHCPIdentifier != "" {
			dhcpMgr.SetDHCPIdentifier(cfg.DHCPIdentifier)
		}

		// 请求侧 hostname overrides（send-hostname / hostname）
		if o := cfg.DHCP4Overrides; o != nil {
			if o.Hostname != "" {
				dhcpMgr.SetHostname(o.Hostname)
			}
			if o.SendHostname != nil && !*o.SendHostname {
				dhcpMgr.SetSendHostname(false)
			}
		}

		// 异步执行 DHCP，避免阻塞。纯 Go 客户端只返回 lease，必须显式应用。
		go func() {
			lease, err := dhcpMgr.RequestDHCPv4(name)
			if err != nil {
				slog.Error("DHCPv4 failed", "device", name, "error", err)
				return
			}
			if err := dhcpMgr.ApplyDHCPv4Lease(name, lease, ov); err != nil {
				slog.Error("failed to apply DHCPv4 lease", "device", name, "error", err)
			}
		}()
	}

	// 处理 DHCPv6
	if cfg.DHCP6 {
		slog.Info("starting DHCPv6", "device", name)
		dhcpMgr := nl.NewDHCPManager()
		ov6 := dhcpOverridesToNl(cfg.DHCP6Overrides)

		// 纯 Go 客户端只返回 lease，必须显式应用。
		go func() {
			lease, err := dhcpMgr.RequestDHCPv6(name, false)
			if err != nil {
				slog.Error("DHCPv6 failed", "device", name, "error", err)
				return
			}
			if err := dhcpMgr.ApplyDHCPv6Lease(name, lease, ov6); err != nil {
				slog.Error("failed to apply DHCPv6 lease", "device", name, "error", err)
			}
		}()
	}

	// 静态地址
	for _, addr := range cfg.Addresses {
		hasAddr, err := mgr.HasAddress(name, addr.CIDR)
		if err != nil {
			slog.Warn("failed to check address", "device", name, "address", addr.CIDR, "error", err)
			continue
		}

		if !hasAddr {
			slog.Info("adding address", "device", name, "address", addr.CIDR, "label", addr.Label, "lifetime", addr.Lifetime)
			if err := mgr.AddAddressOpts(name, addr.CIDR, addr.Label, addr.Lifetime); err != nil {
				slog.Warn("failed to add address", "device", name, "address", addr.CIDR, "error", err)
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

	// DNS
	applyNameservers(mgr, name, cfg.Nameservers)

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

	onLink := false
	if route.OnLink != nil {
		onLink = *route.OnLink
	}

	var encap *nl.SEG6EncapOpts
	if route.Encap != nil {
		if err := config.ValidateRouteEncap(route.Encap); err != nil {
			return err
		}
		encap = &nl.SEG6EncapOpts{Mode: route.Encap.Mode, Segments: route.Encap.Segments}
	}

	slog.Info("adding route", "to", to, "via", route.Via, "device", dev,
		"table", route.Table, "scope", route.Scope, "type", route.Type, "on-link", onLink,
		"seg6", encap != nil)
	return mgr.AddRouteOpts(&nl.RouteOptions{
		Dst:    to,
		Gw:     route.Via,
		Dev:    dev,
		Src:    route.From,
		Metric: route.Metric,
		Table:  route.Table,
		Scope:  route.Scope,
		Type:   route.Type,
		OnLink: onLink,
		MTU:    route.MTU,
		Encap:  encap,
	})
}

// setupSRv6 配置内核态 SRv6：seg6_enabled sysctl（all + 各接口）+ 本地 SID（seg6local）。
// 见 docs/srv6-design.md。
func setupSRv6(mgr *nl.NetlinkManager, nsName string, cfg *config.SRv6Config) error {
	if cfg == nil {
		return nil
	}

	// seg6_enabled：全局
	if cfg.Enabled != nil && *cfg.Enabled {
		if err := mgr.EnableSeg6("all"); err != nil {
			slog.Warn("failed to enable seg6 (all)", "netns", nsName, "error", err)
		}
	}
	// seg6_enabled：各接口（入向处理 SRH）。接口可能后建/外部，失败仅告警。
	for _, iface := range cfg.Interfaces {
		if err := mgr.EnableSeg6(iface); err != nil {
			slog.Warn("failed to enable seg6 on interface", "interface", iface, "netns", nsName, "error", err)
		}
	}

	// End.DT4/DT46（vrftable 解封）需要 net.vrf.strict_mode=1，否则内核 EPERM。
	needStrict := false
	for _, s := range cfg.LocalSIDs {
		if s.VRFTable > 0 {
			needStrict = true
			break
		}
	}
	if needStrict {
		if err := mgr.EnableVRFStrictMode(); err != nil {
			slog.Warn("failed to enable vrf strict_mode (needed for End.DT4/DT46)", "error", err)
		}
	}

	// 本地 SID（endpoint 行为），幂等下发。
	// 锚定设备：per-SID dev → srv6.device → 自动建 dummy "srv6"。
	// 注意：seg6local 必须挂真实设备，内核静默丢弃 lo 上的封装。
	autoDevReady := false
	for _, s := range cfg.LocalSIDs {
		dev := s.Dev
		if dev == "" {
			dev = cfg.Device
		}
		if dev == "" {
			dev = "srv6"
			if !autoDevReady {
				if err := ensureSRv6Dummy(mgr, "srv6"); err != nil {
					return fmt.Errorf("ensure srv6 anchor device: %w", err)
				}
				autoDevReady = true
			}
		}
		slog.Info("adding srv6 local sid", "sid", s.SID, "action", s.Action, "dev", dev, "netns", nsName)
		err := mgr.AddLocalSID(dev, &nl.SRv6LocalSIDOpts{
			SID: s.SID, Action: s.Action,
			Table: s.Table, VRFTable: s.VRFTable,
			NH4: s.NH4, NH6: s.NH6, IIF: s.IIF, OIF: s.OIF,
			Segments: s.Segments,
		})
		if err != nil {
			// 单条 SID 失败仅告警续做（不同内核支持的 action 不同），不阻断其余配置
			slog.Warn("failed to add srv6 local sid", "sid", s.SID, "action", s.Action, "error", err)
		}
	}
	return nil
}

// ensureSRv6Dummy 确保 SID 锚定 dummy 设备存在且 up（seg6local 不能挂 lo）。
func ensureSRv6Dummy(mgr *nl.NetlinkManager, name string) error {
	if !mgr.LinkExists(name) {
		slog.Info("creating srv6 anchor dummy device", "name", name)
		if err := mgr.AddDummyDevice(name); err != nil {
			return err
		}
	}
	return mgr.SetLinkUp(name)
}

// applyNDProxy 处理 ndp-proxy 块的内核部分（addresses=逐 /128 proxy_ndp）。
// rules（按前缀响应器）由 netcfg daemon 跑，不在一次性 apply 里。best-effort。
func applyNDProxy(mgr *nl.NetlinkManager, name string, ndp *config.NDProxy) {
	if ndp == nil || len(ndp.Addresses) == 0 {
		return
	}
	if err := mgr.EnableProxyNDP(name); err != nil {
		slog.Warn("failed to enable proxy_ndp", "device", name, "error", err)
	}
	for _, ip := range ndp.Addresses {
		if err := mgr.AddProxyNDP(name, ip); err != nil {
			slog.Warn("failed to add nd-proxy", "device", name, "ip", ip, "error", err)
		} else {
			slog.Info("added nd-proxy", "device", name, "ip", ip)
		}
	}
}

// ndProxyAddrs 取 ndp-proxy 块的内核地址列表（用于 state/回收）。
func ndProxyAddrs(n *config.NDProxy) []string {
	if n == nil {
		return nil
	}
	return n.Addresses
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
		applyNameservers(mgr, name, cfg.Nameservers)
	}

	return nil
}

// setupTunnels 配置隧道设备
func setupTunnels(mgr *nl.NetlinkManager, nsName string, devices map[string]*config.Tunnel) error {
	names := sortedKeys(devices)

	for _, name := range names {
		cfg := devices[name]
		slog.Debug("setting up tunnel", "name", name, "netns", nsName, "mode", cfg.Mode)

		// 注：mode=vxlan 已在 Normalize 阶段移入 Vxlans（在 bridge 之前创建），
		// 不会到达这里。

		if !mgr.LinkExists(name) {
			slog.Info("creating tunnel device", "name", name, "mode", cfg.Mode)
			opts := &nl.TunnelOptions{
				Mode:      cfg.Mode,
				Local:     cfg.Local,
				Remote:    cfg.Remote,
				TTL:       cfg.TTL,
				TOS:       cfg.TOS,
				Key:       cfg.Key,
				InputKey:  cfg.InputKey,
				OutputKey: cfg.OutputKey,
			}
			if err := mgr.AddTunnel(name, opts); err != nil {
				return fmt.Errorf("failed to create tunnel %s: %w", name, err)
			}
		}

		// netplan 标准：mode=wireguard 时经 wgctrl 配置私钥/端口/peers
		if strings.EqualFold(cfg.Mode, "wireguard") && (cfg.Key != "" || len(cfg.Peers) > 0) {
			if err := configureTunnelWireguard(name, nsName, cfg); err != nil {
				return fmt.Errorf("failed to configure wireguard tunnel %s: %w", name, err)
			}
		}

		if err := setupDevice(mgr, name, cfg.Addresses, cfg.Routes, cfg.MTU, ""); err != nil {
			return fmt.Errorf("failed to setup tunnel %s: %w", name, err)
		}
	}

	return nil
}

// configureTunnelWireguard 为 netplan tunnels:mode:wireguard 配置 WireGuard
// （私钥/端口/fwmark/peers），netns 中则在对应 netns 内执行。复用 wgctrl 路径。
func configureTunnelWireguard(name, nsName string, cfg *config.Tunnel) error {
	do := func() error {
		wgMgr, err := nl.NewWireGuardManager()
		if err != nil {
			return fmt.Errorf("failed to create wireguard manager: %w", err)
		}
		defer wgMgr.Close()

		wgCfg := &nl.WireGuardConfig{
			PrivateKey:   cfg.Key, // netplan: key = 私钥
			ListenPort:   cfg.Port,
			FwMark:       cfg.Mark,
			ReplacePeers: true,
		}
		for _, p := range cfg.Peers {
			wgp := &nl.WireGuardPeer{
				Endpoint:                    p.Endpoint,
				AllowedIPs:                  p.AllowedIPs,
				PersistentKeepaliveInterval: p.Keepalive,
			}
			if p.Keys != nil {
				wgp.PublicKey = p.Keys.Public
				wgp.PresharedKey = p.Keys.Shared
			}
			wgCfg.Peers = append(wgCfg.Peers, wgp)
		}

		slog.Info("configuring wireguard tunnel", "device", name, "port", cfg.Port, "peers", len(cfg.Peers))
		return wgMgr.ConfigureDevice(name, wgCfg)
	}

	if nsName != "" {
		return nl.RunInNetns(nsName, do)
	}
	return do()
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
		applyNameservers(mgr, name, cfg.Nameservers)
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

/*
Copyright © 2024 netcfg authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package netlink

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
)

const (
	NetnsRunDir = "/var/run/netns"

	// lftForever 是内核地址生命周期的「永久」值 (INFINITY_LIFE_TIME, 0xFFFFFFFF)。
	// 用于 preferred_lft=0 但 valid_lft=forever 的弃用地址场景。
	lftForever = 0xFFFFFFFF
)

// NetlinkManager netlink 操作管理器
type NetlinkManager struct {
	handle   *netlink.Handle
	nsName   string
	nsHandle netns.NsHandle
}

// New 创建一个在默认 namespace 操作的管理器
func New() (*NetlinkManager, error) {
	handle, err := netlink.NewHandle()
	if err != nil {
		return nil, fmt.Errorf("failed to create netlink handle: %w", err)
	}
	return &NetlinkManager{handle: handle}, nil
}

// NewWithNetns 创建一个在指定 netns 操作的管理器
func NewWithNetns(nsName string) (*NetlinkManager, error) {
	nsHandle, err := netns.GetFromName(nsName)
	if err != nil {
		return nil, fmt.Errorf("failed to get netns %s: %w", nsName, err)
	}

	handle, err := netlink.NewHandleAt(nsHandle)
	if err != nil {
		nsHandle.Close()
		return nil, fmt.Errorf("failed to create netlink handle at netns %s: %w", nsName, err)
	}

	return &NetlinkManager{
		handle:   handle,
		nsName:   nsName,
		nsHandle: nsHandle,
	}, nil
}

// Close 关闭管理器
func (m *NetlinkManager) Close() {
	if m.handle != nil {
		m.handle.Delete()
	}
	if m.nsHandle != 0 {
		m.nsHandle.Close()
	}
}

// NsName 返回当前 namespace 名称
func (m *NetlinkManager) NsName() string {
	return m.nsName
}

// InNetns 是否在非默认 namespace
func (m *NetlinkManager) InNetns() bool {
	return m.nsName != ""
}

// ========== Network Namespace 操作 ==========

// CreateNetns 创建网络命名空间
func CreateNetns(name string) error {
	// 确保目录存在
	if err := os.MkdirAll(NetnsRunDir, 0755); err != nil {
		return fmt.Errorf("failed to create netns dir: %w", err)
	}

	// 创建命名空间
	newNs, err := netns.NewNamed(name)
	if err != nil {
		return fmt.Errorf("failed to create netns %s: %w", name, err)
	}
	newNs.Close()

	return nil
}

// DeleteNetns 删除网络命名空间
func DeleteNetns(name string) error {
	return netns.DeleteNamed(name)
}

// NetnsExists 检查网络命名空间是否存在
func NetnsExists(name string) bool {
	_, err := netns.GetFromName(name)
	return err == nil
}

// ListNetns 列出所有网络命名空间
func ListNetns() ([]string, error) {
	files, err := os.ReadDir(NetnsRunDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("failed to read netns dir: %w", err)
	}

	var names []string
	for _, f := range files {
		if !f.IsDir() {
			names = append(names, f.Name())
		}
	}
	return names, nil
}

// RunInNetns 在指定 netns 中执行函数
func RunInNetns(nsName string, fn func() error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	// 保存当前 netns
	origNs, err := netns.Get()
	if err != nil {
		return fmt.Errorf("failed to get current netns: %w", err)
	}
	defer origNs.Close()

	// 切换到目标 netns
	targetNs, err := netns.GetFromName(nsName)
	if err != nil {
		return fmt.Errorf("failed to get netns %s: %w", nsName, err)
	}
	defer targetNs.Close()

	if err := netns.Set(targetNs); err != nil {
		return fmt.Errorf("failed to set netns %s: %w", nsName, err)
	}

	// 执行函数
	fnErr := fn()

	// 切换回原 netns
	if err := netns.Set(origNs); err != nil {
		return fmt.Errorf("failed to restore netns: %w", err)
	}

	return fnErr
}

// ========== Link 操作 ==========

// GetLink 获取链路信息
func (m *NetlinkManager) GetLink(name string) (netlink.Link, error) {
	return m.handle.LinkByName(name)
}

// LinkExists 检查链路是否存在
func (m *NetlinkManager) LinkExists(name string) bool {
	_, err := m.handle.LinkByName(name)
	return err == nil
}

// SetLinkUp 启用链路
func (m *NetlinkManager) SetLinkUp(name string) error {
	link, err := m.handle.LinkByName(name)
	if err != nil {
		return fmt.Errorf("failed to get link %s: %w", name, err)
	}
	return m.handle.LinkSetUp(link)
}

// SetLinkDown 禁用链路
func (m *NetlinkManager) SetLinkDown(name string) error {
	link, err := m.handle.LinkByName(name)
	if err != nil {
		return fmt.Errorf("failed to get link %s: %w", name, err)
	}
	return m.handle.LinkSetDown(link)
}

// SetLinkMTU 设置 MTU
func (m *NetlinkManager) SetLinkMTU(name string, mtu int) error {
	link, err := m.handle.LinkByName(name)
	if err != nil {
		return fmt.Errorf("failed to get link %s: %w", name, err)
	}
	return m.handle.LinkSetMTU(link, mtu)
}

// SetLinkMacAddress 设置 MAC 地址
func (m *NetlinkManager) SetLinkMacAddress(name string, mac string) error {
	link, err := m.handle.LinkByName(name)
	if err != nil {
		return fmt.Errorf("failed to get link %s: %w", name, err)
	}

	hwAddr, err := net.ParseMAC(mac)
	if err != nil {
		return fmt.Errorf("invalid mac address %s: %w", mac, err)
	}

	return m.handle.LinkSetHardwareAddr(link, hwAddr)
}

// SetLinkNetns 将链路移动到指定 netns
func (m *NetlinkManager) SetLinkNetns(name string, nsName string) error {
	link, err := m.handle.LinkByName(name)
	if err != nil {
		return fmt.Errorf("failed to get link %s: %w", name, err)
	}

	nsHandle, err := netns.GetFromName(nsName)
	if err != nil {
		return fmt.Errorf("failed to get netns %s: %w", nsName, err)
	}
	defer nsHandle.Close()

	return m.handle.LinkSetNsFd(link, int(nsHandle))
}

// DeleteLink 删除链路
func (m *NetlinkManager) DeleteLink(name string) error {
	link, err := m.handle.LinkByName(name)
	if err != nil {
		return fmt.Errorf("failed to get link %s: %w", name, err)
	}
	return m.handle.LinkDel(link)
}

// ListLinks 列出所有链路
func (m *NetlinkManager) ListLinks() ([]netlink.Link, error) {
	return m.handle.LinkList()
}

// MatchCriteria 物理设备匹配条件。任一非空字段都需匹配；空字段忽略。
// Name 与 Driver 支持 shell glob（如 "en*"）。
type MatchCriteria struct {
	Name       string
	MacAddress string
	Driver     string
}

// IsEmpty 判断匹配条件是否为空（无任何条件）。
func (c MatchCriteria) IsEmpty() bool {
	return c.Name == "" && c.MacAddress == "" && c.Driver == ""
}

// FindMatchingLink 在当前 namespace 的所有 link 中查找满足 criteria 的设备名。
// 无匹配返回 ""；多个匹配时返回名称排序后的第一个（保证确定性）。
func (m *NetlinkManager) FindMatchingLink(c MatchCriteria) (string, error) {
	if c.IsEmpty() {
		return "", nil
	}

	links, err := m.handle.LinkList()
	if err != nil {
		return "", fmt.Errorf("failed to list links: %w", err)
	}

	var matched []string
	for _, link := range links {
		attrs := link.Attrs()

		if c.Name != "" {
			if ok, _ := filepath.Match(c.Name, attrs.Name); !ok {
				continue
			}
		}
		if c.MacAddress != "" {
			if !strings.EqualFold(attrs.HardwareAddr.String(), c.MacAddress) {
				continue
			}
		}
		if c.Driver != "" {
			if ok, _ := filepath.Match(c.Driver, linkDriver(attrs.Name)); !ok {
				continue
			}
		}
		matched = append(matched, attrs.Name)
	}

	if len(matched) == 0 {
		return "", nil
	}
	sort.Strings(matched)
	return matched[0], nil
}

// RenameLink 重命名接口。内核要求接口处于 down 状态才能改名，故先 down 再改名；
// 原本为 up 的接口改名后恢复 up（后续配置流程通常也会再次 up）。
func (m *NetlinkManager) RenameLink(oldName, newName string) error {
	if oldName == newName {
		return nil
	}

	link, err := m.handle.LinkByName(oldName)
	if err != nil {
		return fmt.Errorf("failed to get link %s: %w", oldName, err)
	}

	wasUp := link.Attrs().Flags&net.FlagUp != 0
	if err := m.handle.LinkSetDown(link); err != nil {
		return fmt.Errorf("failed to set %s down for rename: %w", oldName, err)
	}
	if err := m.handle.LinkSetName(link, newName); err != nil {
		return fmt.Errorf("failed to rename %s to %s: %w", oldName, newName, err)
	}
	if wasUp {
		if renamed, err := m.handle.LinkByName(newName); err == nil {
			_ = m.handle.LinkSetUp(renamed)
		}
	}
	return nil
}

// linkDriver 返回接口的内核驱动名（读取 /sys/class/net/<dev>/device/driver
// 符号链接的 basename）。虚拟设备或无驱动信息时返回 ""。
func linkDriver(name string) string {
	target, err := os.Readlink(fmt.Sprintf("/sys/class/net/%s/device/driver", name))
	if err != nil {
		return ""
	}
	return filepath.Base(target)
}

// ========== 创建各类设备 ==========

// AddDummyDevice 创建 dummy 设备
func (m *NetlinkManager) AddDummyDevice(name string) error {
	dummy := &netlink.Dummy{
		LinkAttrs: netlink.LinkAttrs{Name: name},
	}
	return m.handle.LinkAdd(dummy)
}

// AddVethPair 创建 veth 对
func (m *NetlinkManager) AddVethPair(name, peerName string) error {
	veth := &netlink.Veth{
		LinkAttrs: netlink.LinkAttrs{Name: name},
		PeerName:  peerName,
	}
	return m.handle.LinkAdd(veth)
}

// AddMacvlanDevice 创建 macvlan 设备
func (m *NetlinkManager) AddMacvlanDevice(name, parentName, mode string) error {
	parent, err := m.handle.LinkByName(parentName)
	if err != nil {
		return fmt.Errorf("failed to get parent link %s: %w", parentName, err)
	}

	macvlanMode := parseMacvlanMode(mode)

	macvlan := &netlink.Macvlan{
		LinkAttrs: netlink.LinkAttrs{
			Name:        name,
			ParentIndex: parent.Attrs().Index,
		},
		Mode: macvlanMode,
	}
	return m.handle.LinkAdd(macvlan)
}

// AddMacvtapDevice 创建 macvtap 设备
func (m *NetlinkManager) AddMacvtapDevice(name, parentName, mode string) error {
	parent, err := m.handle.LinkByName(parentName)
	if err != nil {
		return fmt.Errorf("failed to get parent link %s: %w", parentName, err)
	}

	macvlanMode := parseMacvlanMode(mode)

	macvtap := &netlink.Macvtap{
		Macvlan: netlink.Macvlan{
			LinkAttrs: netlink.LinkAttrs{
				Name:        name,
				ParentIndex: parent.Attrs().Index,
			},
			Mode: macvlanMode,
		},
	}
	return m.handle.LinkAdd(macvtap)
}

func parseMacvlanMode(mode string) netlink.MacvlanMode {
	switch strings.ToLower(mode) {
	case "private":
		return netlink.MACVLAN_MODE_PRIVATE
	case "vepa":
		return netlink.MACVLAN_MODE_VEPA
	case "passthru", "passthrough":
		return netlink.MACVLAN_MODE_PASSTHRU
	case "source":
		return netlink.MACVLAN_MODE_SOURCE
	default:
		return netlink.MACVLAN_MODE_BRIDGE
	}
}

// BridgeOptions 表示网桥的设备级 STP 参数（不含 path-cost/port-priority 等
// 每端口参数，后者需在端口加入网桥后单独设置）。
// 时间类字段单位为「秒」（与 netplan 一致），写入 sysfs 时会换算为 1/100 秒。
// 字段为零值（nil / 0）表示用户未设置，沿用内核默认。
type BridgeOptions struct {
	STP          *bool
	ForwardDelay int // 秒
	HelloTime    int // 秒
	MaxAge       int // 秒
	AgeingTime   int // 秒
	Priority     int
}

// AddBridge 创建网桥
func (m *NetlinkManager) AddBridge(name string) error {
	bridge := &netlink.Bridge{
		LinkAttrs: netlink.LinkAttrs{Name: name},
	}
	return m.handle.LinkAdd(bridge)
}

// SetBridgeMaster 将链路添加到网桥
func (m *NetlinkManager) SetBridgeMaster(linkName, bridgeName string) error {
	link, err := m.handle.LinkByName(linkName)
	if err != nil {
		return fmt.Errorf("failed to get link %s: %w", linkName, err)
	}

	bridge, err := m.handle.LinkByName(bridgeName)
	if err != nil {
		return fmt.Errorf("failed to get bridge %s: %w", bridgeName, err)
	}

	return m.handle.LinkSetMaster(link, bridge)
}

// BondOptions 表示 bond 设备的可配置参数。
// 字段为零值（空字符串 / 0 / nil / false）表示用户未设置，沿用内核默认值。
type BondOptions struct {
	Mode                  string
	LacpRate              string
	MIIMonitorInterval    int
	MinLinks              int
	TransmitHashPolicy    string
	ADSelect              string
	AllSlavesActive       bool
	ARPInterval           int
	ARPIPTargets          []string
	ARPValidate           string
	ARPAllTargets         string
	UpDelay               int
	DownDelay             int
	FailOverMACPolicy     string
	GratuitousARP         int
	PacketsPerSlave       int
	PrimaryReselectPolicy string
	ResendIGMP            int
	LearnPacketInterval   int
	Primary               string
}

// AddBond 创建绑定设备并应用 bond 参数。
//
// 实现说明：netlink.NewLinkBond 会把所有参数初始化为 -1 哨兵值，底层库
// 仅在字段 >= 0（切片字段非 nil）时才下发到内核。因此这里只覆盖用户
// 显式设置的字段，未设置的字段保持哨兵值以沿用内核默认。
// 所有 bond 参数在创建时一次性下发，因为多数参数要求在 bond 尚无 slave
// 时设置（尤其是 mode）。
func (m *NetlinkManager) AddBond(name string, opts *BondOptions) error {
	if opts == nil {
		opts = &BondOptions{}
	}

	bond := netlink.NewLinkBond(netlink.LinkAttrs{Name: name})

	// Mode：ParseBondMode 对空串/未知值返回 balance-rr（保持原有默认行为）
	bond.Mode = ParseBondMode(opts.Mode)

	// 字符串枚举：拼写错误时返回错误，避免静默忽略（P0：不静默失败）
	if opts.LacpRate != "" {
		v, ok := netlink.StringToBondLacpRateMap[strings.ToLower(opts.LacpRate)]
		if !ok {
			return fmt.Errorf("bond %s: invalid lacp-rate %q (expected slow|fast)", name, opts.LacpRate)
		}
		bond.LacpRate = v
	}
	if opts.TransmitHashPolicy != "" {
		v, ok := netlink.StringToBondXmitHashPolicyMap[strings.ToLower(opts.TransmitHashPolicy)]
		if !ok {
			return fmt.Errorf("bond %s: invalid transmit-hash-policy %q", name, opts.TransmitHashPolicy)
		}
		bond.XmitHashPolicy = v
	}
	if opts.ADSelect != "" {
		v, ok := parseBondAdSelect(opts.ADSelect)
		if !ok {
			return fmt.Errorf("bond %s: invalid ad-select %q (expected stable|bandwidth|count)", name, opts.ADSelect)
		}
		bond.AdSelect = v
	}
	if opts.ARPValidate != "" {
		v, ok := parseBondArpValidate(opts.ARPValidate)
		if !ok {
			return fmt.Errorf("bond %s: invalid arp-validate %q (expected none|active|backup|all)", name, opts.ARPValidate)
		}
		bond.ArpValidate = v
	}
	if opts.ARPAllTargets != "" {
		v, ok := parseBondArpAllTargets(opts.ARPAllTargets)
		if !ok {
			return fmt.Errorf("bond %s: invalid arp-all-targets %q (expected any|all)", name, opts.ARPAllTargets)
		}
		bond.ArpAllTargets = v
	}
	if opts.FailOverMACPolicy != "" {
		v, ok := parseBondFailOverMac(opts.FailOverMACPolicy)
		if !ok {
			return fmt.Errorf("bond %s: invalid fail-over-mac-policy %q (expected none|active|follow)", name, opts.FailOverMACPolicy)
		}
		bond.FailOverMac = v
	}
	if opts.PrimaryReselectPolicy != "" {
		v, ok := parseBondPrimaryReselect(opts.PrimaryReselectPolicy)
		if !ok {
			return fmt.Errorf("bond %s: invalid primary-reselect-policy %q (expected always|better|failure)", name, opts.PrimaryReselectPolicy)
		}
		bond.PrimaryReselect = v
	}

	// 整数参数：仅在非零时设置（0 同时是内核默认值，留哨兵即可）
	if opts.MIIMonitorInterval != 0 {
		bond.Miimon = opts.MIIMonitorInterval
	}
	if opts.MinLinks != 0 {
		bond.MinLinks = opts.MinLinks
	}
	if opts.ARPInterval != 0 {
		bond.ArpInterval = opts.ARPInterval
	}
	if opts.UpDelay != 0 {
		bond.UpDelay = opts.UpDelay
	}
	if opts.DownDelay != 0 {
		bond.DownDelay = opts.DownDelay
	}
	if opts.GratuitousARP != 0 {
		bond.NumPeerNotif = opts.GratuitousARP
	}
	if opts.PacketsPerSlave != 0 {
		bond.PackersPerSlave = opts.PacketsPerSlave
	}
	if opts.ResendIGMP != 0 {
		bond.ResendIgmp = opts.ResendIGMP
	}
	if opts.LearnPacketInterval != 0 {
		bond.LpInterval = opts.LearnPacketInterval
	}
	if opts.AllSlavesActive {
		bond.AllSlavesActive = 1
	}

	// ARP IP 目标：解析为 net.IP
	if len(opts.ARPIPTargets) > 0 {
		targets := make([]net.IP, 0, len(opts.ARPIPTargets))
		for _, t := range opts.ARPIPTargets {
			ip := net.ParseIP(t)
			if ip == nil {
				return fmt.Errorf("bond %s: invalid arp-ip-target %q", name, t)
			}
			targets = append(targets, ip)
		}
		bond.ArpIpTargets = targets
	}

	// Primary：netlink 需要 ifindex，配置里是接口名。该接口为既有物理设备，
	// 若此刻尚不存在则告警跳过（非致命运行时条件）。
	if opts.Primary != "" {
		if link, err := m.handle.LinkByName(opts.Primary); err == nil {
			bond.Primary = link.Attrs().Index
		} else {
			slog.Warn("bond primary interface not found, skipping primary setting",
				"bond", name, "primary", opts.Primary, "error", err)
		}
	}

	return m.handle.LinkAdd(bond)
}

func parseBondAdSelect(s string) (netlink.BondAdSelect, bool) {
	switch strings.ToLower(s) {
	case "stable":
		return netlink.BOND_AD_SELECT_STABLE, true
	case "bandwidth":
		return netlink.BOND_AD_SELECT_BANDWIDTH, true
	case "count":
		return netlink.BOND_AD_SELECT_COUNT, true
	}
	return 0, false
}

func parseBondArpValidate(s string) (netlink.BondArpValidate, bool) {
	switch strings.ToLower(s) {
	case "none":
		return netlink.BOND_ARP_VALIDATE_NONE, true
	case "active":
		return netlink.BOND_ARP_VALIDATE_ACTIVE, true
	case "backup":
		return netlink.BOND_ARP_VALIDATE_BACKUP, true
	case "all":
		return netlink.BOND_ARP_VALIDATE_ALL, true
	}
	return 0, false
}

func parseBondArpAllTargets(s string) (netlink.BondArpAllTargets, bool) {
	switch strings.ToLower(s) {
	case "any":
		return netlink.BOND_ARP_ALL_TARGETS_ANY, true
	case "all":
		return netlink.BOND_ARP_ALL_TARGETS_ALL, true
	}
	return 0, false
}

func parseBondFailOverMac(s string) (netlink.BondFailOverMac, bool) {
	switch strings.ToLower(s) {
	case "none":
		return netlink.BOND_FAIL_OVER_MAC_NONE, true
	case "active":
		return netlink.BOND_FAIL_OVER_MAC_ACTIVE, true
	case "follow":
		return netlink.BOND_FAIL_OVER_MAC_FOLLOW, true
	}
	return 0, false
}

func parseBondPrimaryReselect(s string) (netlink.BondPrimaryReselect, bool) {
	switch strings.ToLower(s) {
	case "always":
		return netlink.BOND_PRIMARY_RESELECT_ALWAYS, true
	case "better":
		return netlink.BOND_PRIMARY_RESELECT_BETTER, true
	case "failure":
		return netlink.BOND_PRIMARY_RESELECT_FAILURE, true
	}
	return 0, false
}

// SetBondSlave 将链路添加到绑定
func (m *NetlinkManager) SetBondSlave(linkName, bondName string) error {
	link, err := m.handle.LinkByName(linkName)
	if err != nil {
		return fmt.Errorf("failed to get link %s: %w", linkName, err)
	}

	bond, err := m.handle.LinkByName(bondName)
	if err != nil {
		return fmt.Errorf("failed to get bond %s: %w", bondName, err)
	}

	return m.handle.LinkSetMaster(link, bond)
}

// AddVlan 创建 VLAN 设备
func (m *NetlinkManager) AddVlan(name, parentName string, vlanID int) error {
	parent, err := m.handle.LinkByName(parentName)
	if err != nil {
		return fmt.Errorf("failed to get parent link %s: %w", parentName, err)
	}

	vlan := &netlink.Vlan{
		LinkAttrs: netlink.LinkAttrs{
			Name:        name,
			ParentIndex: parent.Attrs().Index,
		},
		VlanId: vlanID,
	}
	return m.handle.LinkAdd(vlan)
}

// AddVxlan 创建 VXLAN 设备
func (m *NetlinkManager) AddVxlan(name string, vni int, opts *VxlanOptions) error {
	vxlan := &netlink.Vxlan{
		LinkAttrs: netlink.LinkAttrs{Name: name},
		VxlanId:   vni,
	}

	if opts != nil {
		if opts.Local != "" {
			vxlan.SrcAddr = net.ParseIP(opts.Local)
		}
		if opts.Remote != "" {
			vxlan.Group = net.ParseIP(opts.Remote)
		}
		if opts.Group != "" {
			vxlan.Group = net.ParseIP(opts.Group)
		}
		if opts.Port > 0 {
			vxlan.Port = opts.Port
		}
		if opts.DestPort > 0 {
			vxlan.Port = opts.DestPort
		}
		if opts.PortLow > 0 {
			vxlan.PortLow = opts.PortLow
		}
		if opts.PortHigh > 0 {
			vxlan.PortHigh = opts.PortHigh
		}
		if opts.TTL > 0 {
			vxlan.TTL = opts.TTL
		}
		if opts.TOS > 0 {
			vxlan.TOS = opts.TOS
		}
		if opts.Age > 0 {
			vxlan.Age = opts.Age
		}
		if opts.Limit > 0 {
			vxlan.Limit = opts.Limit
		}
		if opts.Learning != nil {
			vxlan.Learning = *opts.Learning
		}
		if opts.Proxy != nil {
			vxlan.Proxy = *opts.Proxy
		}
		if opts.RSC != nil {
			vxlan.RSC = *opts.RSC
		}
		if opts.L2miss != nil {
			vxlan.L2miss = *opts.L2miss
		}
		if opts.L3miss != nil {
			vxlan.L3miss = *opts.L3miss
		}
		if opts.NoAge {
			vxlan.NoAge = true
		}
		if opts.GBP {
			vxlan.GBP = true
		}
		if opts.FlowBased {
			vxlan.FlowBased = true
		}
		if opts.UDPCSum {
			vxlan.UDPCSum = true
		}
		if opts.UDP6ZeroCSumTx {
			vxlan.UDP6ZeroCSumTx = true
		}
		if opts.UDP6ZeroCSumRx {
			vxlan.UDP6ZeroCSumRx = true
		}
		if opts.Link != "" {
			parent, err := m.handle.LinkByName(opts.Link)
			if err == nil {
				vxlan.VtepDevIndex = parent.Attrs().Index
			}
		}
	}

	return m.handle.LinkAdd(vxlan)
}

// VxlanOptions VXLAN 选项
type VxlanOptions struct {
	Link           string
	Local          string
	Remote         string
	Group          string
	Port           int
	DestPort       int
	PortLow        int // 源端口范围低
	PortHigh       int // 源端口范围高
	TTL            int
	TOS            int
	Age            int // FDB 老化时间 (秒)
	Limit          int // FDB 条目限制
	Learning       *bool
	Proxy          *bool // ARP/ND 代理
	RSC            *bool // Route Short Circuit
	L2miss         *bool // L2 miss 通知
	L3miss         *bool // L3 miss 通知
	NoAge          bool  // 禁用 FDB 老化
	GBP            bool  // Group Based Policy
	FlowBased      bool  // external 模式 (用于 OVS/TC)
	UDPCSum        bool  // UDP checksum
	UDP6ZeroCSumTx bool  // IPv6 发送零校验和
	UDP6ZeroCSumRx bool  // IPv6 接收零校验和
}

// AddVrf 创建 VRF 设备
func (m *NetlinkManager) AddVrf(name string, tableID int) error {
	vrf := &netlink.Vrf{
		LinkAttrs: netlink.LinkAttrs{Name: name},
		Table:     uint32(tableID),
	}
	return m.handle.LinkAdd(vrf)
}

// SetVrfMaster 将链路添加到 VRF
func (m *NetlinkManager) SetVrfMaster(linkName, vrfName string) error {
	link, err := m.handle.LinkByName(linkName)
	if err != nil {
		return fmt.Errorf("failed to get link %s: %w", linkName, err)
	}

	vrf, err := m.handle.LinkByName(vrfName)
	if err != nil {
		return fmt.Errorf("failed to get vrf %s: %w", vrfName, err)
	}

	return m.handle.LinkSetMaster(link, vrf)
}

// ========== Tunnel 设备 ==========

// TunnelOptions 隧道选项
type TunnelOptions struct {
	Mode      string // gre/ipip/sit/vti/vti6/ip6gre/ip6ip6/ipip6
	Local     string
	Remote    string
	TTL       int
	TOS       int
	Key       string
	InputKey  string
	OutputKey string
	Link      string // 底层设备
	EncapType string // fou/gue
	EncapPort int
}

// AddTunnel 创建隧道设备
func (m *NetlinkManager) AddTunnel(name string, opts *TunnelOptions) error {
	if opts == nil {
		return fmt.Errorf("tunnel options required")
	}

	var link netlink.Link
	linkAttrs := netlink.LinkAttrs{Name: name}

	// 解析底层设备
	if opts.Link != "" {
		parent, err := m.handle.LinkByName(opts.Link)
		if err == nil {
			linkAttrs.ParentIndex = parent.Attrs().Index
		}
	}

	switch strings.ToLower(opts.Mode) {
	case "gre", "gretap":
		gre := &netlink.Gretap{
			LinkAttrs: linkAttrs,
		}
		if opts.Local != "" {
			gre.Local = net.ParseIP(opts.Local)
		}
		if opts.Remote != "" {
			gre.Remote = net.ParseIP(opts.Remote)
		}
		if opts.TTL > 0 {
			gre.Ttl = uint8(opts.TTL)
		}
		if opts.TOS > 0 {
			gre.Tos = uint8(opts.TOS)
		}
		link = gre

	case "ip6gre", "ip6gretap":
		// IPv6 GRE 使用 GenericLink
		gre := &netlink.GenericLink{
			LinkAttrs: linkAttrs,
			LinkType:  "ip6gre",
		}
		link = gre

	case "ipip", "ipip4":
		iptun := &netlink.Iptun{
			LinkAttrs: linkAttrs,
		}
		if opts.Local != "" {
			iptun.Local = net.ParseIP(opts.Local)
		}
		if opts.Remote != "" {
			iptun.Remote = net.ParseIP(opts.Remote)
		}
		if opts.TTL > 0 {
			iptun.Ttl = uint8(opts.TTL)
		}
		if opts.TOS > 0 {
			iptun.Tos = uint8(opts.TOS)
		}
		link = iptun

	case "ip6ip6", "ipip6", "ip6tnl":
		ip6tnl := &netlink.Ip6tnl{
			LinkAttrs: linkAttrs,
		}
		if opts.Local != "" {
			ip6tnl.Local = net.ParseIP(opts.Local)
		}
		if opts.Remote != "" {
			ip6tnl.Remote = net.ParseIP(opts.Remote)
		}
		if opts.TTL > 0 {
			ip6tnl.Ttl = uint8(opts.TTL)
		}
		link = ip6tnl

	case "sit":
		sit := &netlink.Sittun{
			LinkAttrs: linkAttrs,
		}
		if opts.Local != "" {
			sit.Local = net.ParseIP(opts.Local)
		}
		if opts.Remote != "" {
			sit.Remote = net.ParseIP(opts.Remote)
		}
		if opts.TTL > 0 {
			sit.Ttl = uint8(opts.TTL)
		}
		link = sit

	case "vti":
		vti := &netlink.Vti{
			LinkAttrs: linkAttrs,
		}
		if opts.Local != "" {
			vti.Local = net.ParseIP(opts.Local)
		}
		if opts.Remote != "" {
			vti.Remote = net.ParseIP(opts.Remote)
		}
		link = vti

	case "vti6":
		vti := &netlink.Vti{
			LinkAttrs: linkAttrs,
		}
		if opts.Local != "" {
			vti.Local = net.ParseIP(opts.Local)
		}
		if opts.Remote != "" {
			vti.Remote = net.ParseIP(opts.Remote)
		}
		link = vti

	case "wireguard", "wg":
		// WireGuard 需要内核支持，使用 ip link add type wireguard
		wg := &netlink.GenericLink{
			LinkAttrs: linkAttrs,
			LinkType:  "wireguard",
		}
		link = wg

	default:
		return fmt.Errorf("unsupported tunnel mode: %s", opts.Mode)
	}

	return m.handle.LinkAdd(link)
}

// AddWireguard 创建 WireGuard 设备
func (m *NetlinkManager) AddWireguard(name string) error {
	wg := &netlink.GenericLink{
		LinkAttrs: netlink.LinkAttrs{Name: name},
		LinkType:  "wireguard",
	}
	return m.handle.LinkAdd(wg)
}

// AddTap 创建 TAP 设备
func (m *NetlinkManager) AddTap(name string) error {
	tap := &netlink.Tuntap{
		LinkAttrs: netlink.LinkAttrs{Name: name},
		Mode:      netlink.TUNTAP_MODE_TAP,
	}
	return m.handle.LinkAdd(tap)
}

// AddTun 创建 TUN 设备
func (m *NetlinkManager) AddTun(name string) error {
	tun := &netlink.Tuntap{
		LinkAttrs: netlink.LinkAttrs{Name: name},
		Mode:      netlink.TUNTAP_MODE_TUN,
	}
	return m.handle.LinkAdd(tun)
}

// AddIpvlan 创建 ipvlan 设备
func (m *NetlinkManager) AddIpvlan(name, parentName, mode string) error {
	parent, err := m.handle.LinkByName(parentName)
	if err != nil {
		return fmt.Errorf("failed to get parent link %s: %w", parentName, err)
	}

	ipvlanMode := netlink.IPVLAN_MODE_L2
	switch strings.ToLower(mode) {
	case "l3":
		ipvlanMode = netlink.IPVLAN_MODE_L3
	case "l3s":
		ipvlanMode = netlink.IPVLAN_MODE_L3S
	}

	ipvlan := &netlink.IPVlan{
		LinkAttrs: netlink.LinkAttrs{
			Name:        name,
			ParentIndex: parent.Attrs().Index,
		},
		Mode: ipvlanMode,
	}
	return m.handle.LinkAdd(ipvlan)
}

// ========== FDB (Forwarding Database) 操作 - EVPN 需要 ==========

// FDBEntry FDB 条目
type FDBEntry struct {
	MAC    string // MAC 地址
	Ifname string // 接口名
	Dst    string // 目标 VTEP IP (VXLAN)
	VNI    int    // VXLAN VNI
	State  string // permanent/static/dynamic
}

// AddFDBEntry 添加 FDB 条目
// 用于 EVPN 场景：将远端 MAC 指向远端 VTEP
func (m *NetlinkManager) AddFDBEntry(entry *FDBEntry) error {
	link, err := m.handle.LinkByName(entry.Ifname)
	if err != nil {
		return fmt.Errorf("failed to get link %s: %w", entry.Ifname, err)
	}

	hwAddr, err := net.ParseMAC(entry.MAC)
	if err != nil {
		return fmt.Errorf("invalid MAC %s: %w", entry.MAC, err)
	}

	neigh := &netlink.Neigh{
		LinkIndex:    link.Attrs().Index,
		Family:       syscall.AF_BRIDGE, // Bridge FDB
		HardwareAddr: hwAddr,
		Flags:        netlink.NTF_SELF,
	}

	// 设置目标 VTEP IP
	if entry.Dst != "" {
		neigh.IP = net.ParseIP(entry.Dst)
	}

	// 设置状态
	switch strings.ToLower(entry.State) {
	case "permanent":
		neigh.State = netlink.NUD_PERMANENT
	case "static":
		neigh.State = netlink.NUD_NOARP
	default:
		neigh.State = netlink.NUD_REACHABLE
	}

	return m.handle.NeighAdd(neigh)
}

// DeleteFDBEntry 删除 FDB 条目
func (m *NetlinkManager) DeleteFDBEntry(entry *FDBEntry) error {
	link, err := m.handle.LinkByName(entry.Ifname)
	if err != nil {
		return fmt.Errorf("failed to get link %s: %w", entry.Ifname, err)
	}

	hwAddr, err := net.ParseMAC(entry.MAC)
	if err != nil {
		return fmt.Errorf("invalid MAC %s: %w", entry.MAC, err)
	}

	neigh := &netlink.Neigh{
		LinkIndex:    link.Attrs().Index,
		Family:       syscall.AF_BRIDGE,
		HardwareAddr: hwAddr,
	}

	if entry.Dst != "" {
		neigh.IP = net.ParseIP(entry.Dst)
	}

	return m.handle.NeighDel(neigh)
}

// ListFDBEntries 列出 FDB 条目
func (m *NetlinkManager) ListFDBEntries(ifname string) ([]netlink.Neigh, error) {
	link, err := m.handle.LinkByName(ifname)
	if err != nil {
		return nil, fmt.Errorf("failed to get link %s: %w", ifname, err)
	}

	return m.handle.NeighList(link.Attrs().Index, syscall.AF_BRIDGE)
}

// ========== Neighbor (ARP/ND) 操作 - EVPN ARP suppress 需要 ==========

// NeighEntry 邻居条目
type NeighEntry struct {
	IP     string // IP 地址
	MAC    string // MAC 地址
	Ifname string // 接口名
	State  string // permanent/static/reachable
}

// AddNeighEntry 添加邻居条目 (静态 ARP/ND)
// EVPN ARP suppress: 在 VTEP 本地回应 ARP 请求
func (m *NetlinkManager) AddNeighEntry(entry *NeighEntry) error {
	link, err := m.handle.LinkByName(entry.Ifname)
	if err != nil {
		return fmt.Errorf("failed to get link %s: %w", entry.Ifname, err)
	}

	ip := net.ParseIP(entry.IP)
	if ip == nil {
		return fmt.Errorf("invalid IP %s", entry.IP)
	}

	hwAddr, err := net.ParseMAC(entry.MAC)
	if err != nil {
		return fmt.Errorf("invalid MAC %s: %w", entry.MAC, err)
	}

	family := netlink.FAMILY_V4
	if ip.To4() == nil {
		family = netlink.FAMILY_V6
	}

	neigh := &netlink.Neigh{
		LinkIndex:    link.Attrs().Index,
		Family:       family,
		IP:           ip,
		HardwareAddr: hwAddr,
	}

	switch strings.ToLower(entry.State) {
	case "permanent":
		neigh.State = netlink.NUD_PERMANENT
	case "static":
		neigh.State = netlink.NUD_NOARP
	default:
		neigh.State = netlink.NUD_REACHABLE
	}

	return m.handle.NeighAdd(neigh)
}

// DeleteNeighEntry 删除邻居条目
func (m *NetlinkManager) DeleteNeighEntry(entry *NeighEntry) error {
	link, err := m.handle.LinkByName(entry.Ifname)
	if err != nil {
		return fmt.Errorf("failed to get link %s: %w", entry.Ifname, err)
	}

	ip := net.ParseIP(entry.IP)
	if ip == nil {
		return fmt.Errorf("invalid IP %s", entry.IP)
	}

	family := netlink.FAMILY_V4
	if ip.To4() == nil {
		family = netlink.FAMILY_V6
	}

	neigh := &netlink.Neigh{
		LinkIndex: link.Attrs().Index,
		Family:    family,
		IP:        ip,
	}

	return m.handle.NeighDel(neigh)
}

// ListNeighEntries 列出邻居条目
func (m *NetlinkManager) ListNeighEntries(ifname string) ([]netlink.Neigh, error) {
	link, err := m.handle.LinkByName(ifname)
	if err != nil {
		return nil, fmt.Errorf("failed to get link %s: %w", ifname, err)
	}

	return m.handle.NeighList(link.Attrs().Index, netlink.FAMILY_ALL)
}

// ========== Bridge VXLAN 特性 - EVPN 需要 ==========

// SetBridgeVlanFiltering 设置 Bridge VLAN 过滤
func (m *NetlinkManager) SetBridgeVlanFiltering(name string, enable bool) error {
	// 通过 sysfs 设置 VLAN 过滤
	return writeSysfsBool(fmt.Sprintf("/sys/class/net/%s/bridge/vlan_filtering", name), enable)
}

// SetLinkNeighSuppress 设置接口 ARP/ND suppress (EVPN 需要)
// 在 VXLAN 接口上启用后，Bridge 会本地回应已知的 ARP 请求
func (m *NetlinkManager) SetLinkNeighSuppress(name string, enable bool) error {
	// ip link set vxlan0 type bridge_slave neigh_suppress on 的 sysfs 等价
	return writeSysfsBool(fmt.Sprintf("/sys/class/net/%s/brport/neigh_suppress", name), enable)
}

// SetLinkLearning 设置接口学习模式
// EVPN 场景通常禁用学习，由控制平面管理
func (m *NetlinkManager) SetLinkLearning(name string, enable bool) error {
	return writeSysfsBool(fmt.Sprintf("/sys/class/net/%s/brport/learning", name), enable)
}

// SetBridgeParameters 设置网桥设备级 STP 参数。
//
// 实现说明：vishvananda/netlink v1.1.0 的 Bridge 结构仅暴露
// MulticastSnooping/HelloTime/VlanFiltering，无法通过 netlink 设置 STP/
// forward-delay/max-age/priority/ageing-time。因此沿用项目既有的 sysfs 方式
// （与 vlan-filtering/neigh-suppress 一致）写入 /sys/class/net/<br>/bridge/。
// 这些参数可在网桥运行时热更新，无需重建设备。
// 时间类参数：config 单位为秒，sysfs 单位为 1/100 秒（USER_HZ），需 ×100。
// 采用尽力而为：逐项写入，汇总失败项后一并返回（errors.Join）。
func (m *NetlinkManager) SetBridgeParameters(name string, opts *BridgeOptions) error {
	if opts == nil {
		return nil
	}

	base := fmt.Sprintf("/sys/class/net/%s/bridge", name)
	var errs []error

	if opts.STP != nil {
		errs = append(errs, writeSysfsBool(base+"/stp_state", *opts.STP))
	}
	if opts.ForwardDelay != 0 {
		errs = append(errs, writeSysfsInt(base+"/forward_delay", opts.ForwardDelay*100))
	}
	if opts.HelloTime != 0 {
		errs = append(errs, writeSysfsInt(base+"/hello_time", opts.HelloTime*100))
	}
	if opts.MaxAge != 0 {
		errs = append(errs, writeSysfsInt(base+"/max_age", opts.MaxAge*100))
	}
	if opts.AgeingTime != 0 {
		errs = append(errs, writeSysfsInt(base+"/ageing_time", opts.AgeingTime*100))
	}
	if opts.Priority != 0 {
		errs = append(errs, writeSysfsInt(base+"/priority", opts.Priority))
	}

	return errors.Join(errs...)
}

// SetBridgePortPathCost 设置网桥端口的 STP path cost（每端口参数）。
// 端口必须已加入网桥（brport 目录在 enslave 后才存在）。
func (m *NetlinkManager) SetBridgePortPathCost(port string, cost int) error {
	return writeSysfsInt(fmt.Sprintf("/sys/class/net/%s/brport/path_cost", port), cost)
}

// SetBridgePortPriority 设置网桥端口的 STP 优先级（每端口参数，0-63）。
// 端口必须已加入网桥（brport 目录在 enslave 后才存在）。
func (m *NetlinkManager) SetBridgePortPriority(port string, priority int) error {
	return writeSysfsInt(fmt.Sprintf("/sys/class/net/%s/brport/priority", port), priority)
}

// writeSysfs 向 sysfs 文件写入字符串值，失败时附带路径上下文。
func writeSysfs(path, value string) error {
	if err := os.WriteFile(path, []byte(value), 0644); err != nil {
		return fmt.Errorf("write sysfs %s: %w", path, err)
	}
	return nil
}

func writeSysfsInt(path string, v int) error {
	return writeSysfs(path, strconv.Itoa(v))
}

func writeSysfsBool(path string, b bool) error {
	if b {
		return writeSysfs(path, "1")
	}
	return writeSysfs(path, "0")
}

// ========== 地址操作 ==========

// AddAddress 添加 IP 地址
func (m *NetlinkManager) AddAddress(linkName string, cidr string) error {
	return m.AddAddressOpts(linkName, cidr, "", "")
}

// AddAddressOpts 添加 IP 地址，支持 label 与 lifetime。
//
// lifetime：""/"forever" -> 永久（不设 CACHEINFO，内核默认永久）；
// "0" -> 立即弃用（preferred_lft=0，valid_lft 仍永久），对齐 netplan/networkd
// 的 lifetime:0 / PreferredLifetime=0 语义。
// label：IPv4 地址别名标签（如 eth0:1）。
func (m *NetlinkManager) AddAddressOpts(linkName, cidr, label, lifetime string) error {
	link, err := m.handle.LinkByName(linkName)
	if err != nil {
		return fmt.Errorf("failed to get link %s: %w", linkName, err)
	}

	addr, err := netlink.ParseAddr(cidr)
	if err != nil {
		return fmt.Errorf("invalid address %s: %w", cidr, err)
	}

	if label != "" {
		addr.Label = label
	}

	switch strings.ToLower(lifetime) {
	case "", "forever":
		// 永久：保持默认（ValidLft/PreferedLft = 0，底层库不发 CACHEINFO）
	case "0":
		// 立即弃用但仍永久有效：valid_lft=forever, preferred_lft=0
		addr.ValidLft = lftForever
		addr.PreferedLft = 0
	default:
		return fmt.Errorf("invalid address lifetime %q (expected 0 or forever)", lifetime)
	}

	return m.handle.AddrAdd(link, addr)
}

// DeleteAddress 删除 IP 地址
func (m *NetlinkManager) DeleteAddress(linkName string, cidr string) error {
	link, err := m.handle.LinkByName(linkName)
	if err != nil {
		return fmt.Errorf("failed to get link %s: %w", linkName, err)
	}

	addr, err := netlink.ParseAddr(cidr)
	if err != nil {
		return fmt.Errorf("invalid address %s: %w", cidr, err)
	}

	return m.handle.AddrDel(link, addr)
}

// ListAddresses 列出链路上的地址
func (m *NetlinkManager) ListAddresses(linkName string) ([]netlink.Addr, error) {
	link, err := m.handle.LinkByName(linkName)
	if err != nil {
		return nil, fmt.Errorf("failed to get link %s: %w", linkName, err)
	}

	return m.handle.AddrList(link, netlink.FAMILY_ALL)
}

// HasAddress 检查链路是否有指定地址
func (m *NetlinkManager) HasAddress(linkName string, cidr string) (bool, error) {
	addrs, err := m.ListAddresses(linkName)
	if err != nil {
		return false, err
	}

	for _, addr := range addrs {
		if addr.IPNet.String() == cidr {
			return true, nil
		}
	}
	return false, nil
}

// ========== 路由操作 ==========

// AddRoute 添加路由
func (m *NetlinkManager) AddRoute(dst, gw, dev string, metric, table int) error {
	return m.AddRouteOpts(&RouteOptions{
		Dst:    dst,
		Gw:     gw,
		Dev:    dev,
		Metric: metric,
		Table:  table,
	})
}

// RouteOptions 表示一条路由的完整可配置项。
// 字段为零值（空字符串 / 0 / false）表示未设置，沿用内核默认。
type RouteOptions struct {
	Dst    string // 目标网络；"default"/"0.0.0.0/0" 表示默认路由
	Gw     string // 网关 (via)
	Dev    string // 出接口
	Src    string // 首选源地址 (from / prefsrc)
	Metric int
	Table  int
	Scope  string // global|link|host|site|nowhere
	Type   string // unicast|local|broadcast|anycast|multicast|blackhole|unreachable|prohibit|throw|nat
	OnLink bool
	MTU    int
}

// AddRouteOpts 按 RouteOptions 添加路由，支持 from/scope/type/on-link/mtu 等高级字段。
func (m *NetlinkManager) AddRouteOpts(opts *RouteOptions) error {
	route := &netlink.Route{}

	// 解析目标网络
	if opts.Dst == "default" || opts.Dst == "0.0.0.0/0" {
		route.Dst = nil // nil 表示默认路由
	} else {
		_, dstNet, err := net.ParseCIDR(opts.Dst)
		if err != nil {
			return fmt.Errorf("invalid destination %s: %w", opts.Dst, err)
		}
		route.Dst = dstNet
	}

	// 解析网关
	if opts.Gw != "" {
		route.Gw = net.ParseIP(opts.Gw)
		if route.Gw == nil {
			return fmt.Errorf("invalid gateway %s", opts.Gw)
		}
	}

	// 解析出接口
	if opts.Dev != "" {
		link, err := m.handle.LinkByName(opts.Dev)
		if err != nil {
			return fmt.Errorf("failed to get link %s: %w", opts.Dev, err)
		}
		route.LinkIndex = link.Attrs().Index
	}

	// 首选源地址 (from)
	if opts.Src != "" {
		route.Src = net.ParseIP(opts.Src)
		if route.Src == nil {
			return fmt.Errorf("invalid source address %s", opts.Src)
		}
	}

	if opts.Metric > 0 {
		route.Priority = opts.Metric
	}

	if opts.Table > 0 {
		route.Table = opts.Table
	}

	// 路由作用域：仅在显式指定时设置（未指定保持内核默认，避免改变既有行为）
	if opts.Scope != "" {
		sc, ok := parseRouteScope(opts.Scope)
		if !ok {
			return fmt.Errorf("invalid route scope %q (expected global|link|host|site|nowhere)", opts.Scope)
		}
		route.Scope = sc
	}

	// 路由类型 (blackhole/prohibit/...)
	if opts.Type != "" {
		t, ok := parseRouteType(opts.Type)
		if !ok {
			return fmt.Errorf("invalid route type %q", opts.Type)
		}
		route.Type = t
	}

	// on-link：网关无需可达，直接经出接口
	if opts.OnLink {
		route.Flags |= int(netlink.FLAG_ONLINK)
	}

	if opts.MTU > 0 {
		route.MTU = opts.MTU
	}

	return m.handle.RouteAdd(route)
}

func parseRouteScope(s string) (netlink.Scope, bool) {
	switch strings.ToLower(s) {
	case "global", "universe":
		return netlink.SCOPE_UNIVERSE, true
	case "site":
		return netlink.SCOPE_SITE, true
	case "link":
		return netlink.SCOPE_LINK, true
	case "host":
		return netlink.SCOPE_HOST, true
	case "nowhere":
		return netlink.SCOPE_NOWHERE, true
	}
	return 0, false
}

func parseRouteType(s string) (int, bool) {
	switch strings.ToLower(s) {
	case "unicast":
		return unix.RTN_UNICAST, true
	case "local":
		return unix.RTN_LOCAL, true
	case "broadcast":
		return unix.RTN_BROADCAST, true
	case "anycast":
		return unix.RTN_ANYCAST, true
	case "multicast":
		return unix.RTN_MULTICAST, true
	case "blackhole":
		return unix.RTN_BLACKHOLE, true
	case "unreachable":
		return unix.RTN_UNREACHABLE, true
	case "prohibit":
		return unix.RTN_PROHIBIT, true
	case "throw":
		return unix.RTN_THROW, true
	case "nat":
		return unix.RTN_NAT, true
	}
	return 0, false
}

// DeleteRoute 删除路由
func (m *NetlinkManager) DeleteRoute(dst, gw, dev string, table int) error {
	route := &netlink.Route{}

	if dst == "default" || dst == "0.0.0.0/0" {
		route.Dst = nil
	} else {
		_, dstNet, err := net.ParseCIDR(dst)
		if err != nil {
			return fmt.Errorf("invalid destination %s: %w", dst, err)
		}
		route.Dst = dstNet
	}

	if gw != "" {
		route.Gw = net.ParseIP(gw)
	}

	if dev != "" {
		link, err := m.handle.LinkByName(dev)
		if err != nil {
			return fmt.Errorf("failed to get link %s: %w", dev, err)
		}
		route.LinkIndex = link.Attrs().Index
	}

	if table > 0 {
		route.Table = table
	}

	return m.handle.RouteDel(route)
}

// ListRoutes 列出路由
func (m *NetlinkManager) ListRoutes(linkName string) ([]netlink.Route, error) {
	var link netlink.Link
	var err error

	if linkName != "" {
		link, err = m.handle.LinkByName(linkName)
		if err != nil {
			return nil, fmt.Errorf("failed to get link %s: %w", linkName, err)
		}
	}

	filter := &netlink.Route{}
	if link != nil {
		filter.LinkIndex = link.Attrs().Index
	}

	return m.handle.RouteListFiltered(netlink.FAMILY_ALL, filter, netlink.RT_FILTER_OIF)
}

// ========== 路由规则操作 ==========

// AddRule 添加路由规则
func (m *NetlinkManager) AddRule(from, to string, table, priority, mark int) error {
	rule := netlink.NewRule()

	if from != "" {
		_, srcNet, err := net.ParseCIDR(from)
		if err != nil {
			return fmt.Errorf("invalid from %s: %w", from, err)
		}
		rule.Src = srcNet
	}

	if to != "" {
		_, dstNet, err := net.ParseCIDR(to)
		if err != nil {
			return fmt.Errorf("invalid to %s: %w", to, err)
		}
		rule.Dst = dstNet
	}

	if table > 0 {
		rule.Table = table
	}

	if priority > 0 {
		rule.Priority = priority
	}

	if mark > 0 {
		rule.Mark = mark
	}

	return m.handle.RuleAdd(rule)
}

// DeleteRule 删除路由规则
func (m *NetlinkManager) DeleteRule(from, to string, table, priority int) error {
	rule := netlink.NewRule()

	if from != "" {
		_, srcNet, err := net.ParseCIDR(from)
		if err != nil {
			return fmt.Errorf("invalid from %s: %w", from, err)
		}
		rule.Src = srcNet
	}

	if to != "" {
		_, dstNet, err := net.ParseCIDR(to)
		if err != nil {
			return fmt.Errorf("invalid to %s: %w", to, err)
		}
		rule.Dst = dstNet
	}

	if table > 0 {
		rule.Table = table
	}

	if priority > 0 {
		rule.Priority = priority
	}

	return m.handle.RuleDel(rule)
}

// ========== 辅助函数 ==========

// GetNetnsPath 获取 netns 文件路径
func GetNetnsPath(name string) string {
	return filepath.Join(NetnsRunDir, name)
}

// ParseBondMode 解析绑定模式
func ParseBondMode(mode string) netlink.BondMode {
	switch strings.ToLower(mode) {
	case "balance-rr", "0":
		return netlink.BOND_MODE_BALANCE_RR
	case "active-backup", "1":
		return netlink.BOND_MODE_ACTIVE_BACKUP
	case "balance-xor", "2":
		return netlink.BOND_MODE_BALANCE_XOR
	case "broadcast", "3":
		return netlink.BOND_MODE_BROADCAST
	case "802.3ad", "4":
		return netlink.BOND_MODE_802_3AD
	case "balance-tlb", "5":
		return netlink.BOND_MODE_BALANCE_TLB
	case "balance-alb", "6":
		return netlink.BOND_MODE_BALANCE_ALB
	default:
		return netlink.BOND_MODE_BALANCE_RR
	}
}

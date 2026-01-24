/*
Copyright © 2024 netcfg authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package netlink

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
)

const (
	NetnsRunDir = "/var/run/netns"
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

// AddBond 创建绑定设备
func (m *NetlinkManager) AddBond(name string, mode netlink.BondMode) error {
	bond := netlink.NewLinkBond(netlink.LinkAttrs{Name: name})
	bond.Mode = mode
	return m.handle.LinkAdd(bond)
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
	val := "0"
	if enable {
		val = "1"
	}
	path := fmt.Sprintf("/sys/class/net/%s/bridge/vlan_filtering", name)
	return os.WriteFile(path, []byte(val), 0644)
}

// SetLinkNeighSuppress 设置接口 ARP/ND suppress (EVPN 需要)
// 在 VXLAN 接口上启用后，Bridge 会本地回应已知的 ARP 请求
func (m *NetlinkManager) SetLinkNeighSuppress(name string, enable bool) error {
	// 需要通过 sysfs 或 ip link set 实现
	// ip link set vxlan0 type bridge_slave neigh_suppress on
	val := "0"
	if enable {
		val = "1"
	}

	path := fmt.Sprintf("/sys/class/net/%s/brport/neigh_suppress", name)
	return os.WriteFile(path, []byte(val), 0644)
}

// SetLinkLearning 设置接口学习模式
// EVPN 场景通常禁用学习，由控制平面管理
func (m *NetlinkManager) SetLinkLearning(name string, enable bool) error {
	val := "0"
	if enable {
		val = "1"
	}

	path := fmt.Sprintf("/sys/class/net/%s/brport/learning", name)
	return os.WriteFile(path, []byte(val), 0644)
}

// ========== 地址操作 ==========

// AddAddress 添加 IP 地址
func (m *NetlinkManager) AddAddress(linkName string, cidr string) error {
	link, err := m.handle.LinkByName(linkName)
	if err != nil {
		return fmt.Errorf("failed to get link %s: %w", linkName, err)
	}

	addr, err := netlink.ParseAddr(cidr)
	if err != nil {
		return fmt.Errorf("invalid address %s: %w", cidr, err)
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
	route := &netlink.Route{}

	// 解析目标网络
	if dst == "default" || dst == "0.0.0.0/0" {
		route.Dst = nil // nil 表示默认路由
	} else {
		_, dstNet, err := net.ParseCIDR(dst)
		if err != nil {
			return fmt.Errorf("invalid destination %s: %w", dst, err)
		}
		route.Dst = dstNet
	}

	// 解析网关
	if gw != "" {
		route.Gw = net.ParseIP(gw)
		if route.Gw == nil {
			return fmt.Errorf("invalid gateway %s", gw)
		}
	}

	// 解析设备
	if dev != "" {
		link, err := m.handle.LinkByName(dev)
		if err != nil {
			return fmt.Errorf("failed to get link %s: %w", dev, err)
		}
		route.LinkIndex = link.Attrs().Index
	}

	if metric > 0 {
		route.Priority = metric
	}

	if table > 0 {
		route.Table = table
	}

	return m.handle.RouteAdd(route)
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

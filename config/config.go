/*
Copyright © 2024 netcfg authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

// Config 顶层配置结构
type Config struct {
	// 新格式（netplan 兼容）
	Network Network `yaml:"network,omitempty"`

	// 旧格式（原 netnsplan 兼容）
	Netns map[string]*Namespace `yaml:"netns,omitempty"`
}

// Network netplan 兼容的网络配置
type Network struct {
	Version  int    `yaml:"version,omitempty"`
	Renderer string `yaml:"renderer,omitempty"`

	// 顶层设备配置 → default namespace
	Ethernets      map[string]*Ethernet      `yaml:"ethernets,omitempty"`
	DummyDevices   map[string]*Ethernet      `yaml:"dummy-devices,omitempty"`
	VethDevices    map[string]*VethDevice    `yaml:"veth-devices,omitempty"`
	MacvlanDevices map[string]*MacvlanDevice `yaml:"macvlan-devices,omitempty"`
	MacvtapDevices map[string]*MacvlanDevice `yaml:"macvtap-devices,omitempty"`
	IpvlanDevices  map[string]*IpvlanDevice  `yaml:"ipvlan-devices,omitempty"`
	Bridges        map[string]*Bridge        `yaml:"bridges,omitempty"`
	Bonds          map[string]*Bond          `yaml:"bonds,omitempty"`
	Vlans          map[string]*Vlan          `yaml:"vlans,omitempty"`
	Vxlans         map[string]*Vxlan         `yaml:"vxlans,omitempty"`
	Tunnels        map[string]*Tunnel        `yaml:"tunnels,omitempty"`
	Wireguards     map[string]*Wireguard     `yaml:"wireguards,omitempty"`
	Vrfs           map[string]*Vrf           `yaml:"vrfs,omitempty"`
	TunDevices     map[string]*TunTapDevice  `yaml:"tun-devices,omitempty"`
	TapDevices     map[string]*TunTapDevice  `yaml:"tap-devices,omitempty"`

	// netns 配置
	Netns map[string]*Namespace `yaml:"netns,omitempty"`
}

// Namespace 网络命名空间配置
type Namespace struct {
	Loopback       *Ethernet                 `yaml:"loopback,omitempty"`
	Ethernets      map[string]*Ethernet      `yaml:"ethernets,omitempty"`
	DummyDevices   map[string]*Ethernet      `yaml:"dummy-devices,omitempty"`
	VethDevices    map[string]*VethDevice    `yaml:"veth-devices,omitempty"`
	MacvlanDevices map[string]*MacvlanDevice `yaml:"macvlan-devices,omitempty"`
	MacvtapDevices map[string]*MacvlanDevice `yaml:"macvtap-devices,omitempty"`
	IpvlanDevices  map[string]*IpvlanDevice  `yaml:"ipvlan-devices,omitempty"`
	Bridges        map[string]*Bridge        `yaml:"bridges,omitempty"`
	Bonds          map[string]*Bond          `yaml:"bonds,omitempty"`
	Vlans          map[string]*Vlan          `yaml:"vlans,omitempty"`
	Vxlans         map[string]*Vxlan         `yaml:"vxlans,omitempty"`
	Tunnels        map[string]*Tunnel        `yaml:"tunnels,omitempty"`
	Wireguards     map[string]*Wireguard     `yaml:"wireguards,omitempty"`
	Vrfs           map[string]*Vrf           `yaml:"vrfs,omitempty"`
	TunDevices     map[string]*TunTapDevice  `yaml:"tun-devices,omitempty"`
	TapDevices     map[string]*TunTapDevice  `yaml:"tap-devices,omitempty"`
	PostScript     string                    `yaml:"post-script,omitempty"`
}

// Ethernet 以太网设备配置
type Ethernet struct {
	Match         *Match           `yaml:"match,omitempty"`
	SetName       string           `yaml:"set-name,omitempty"`
	Addresses     []string         `yaml:"addresses,omitempty"`
	DHCP4         bool             `yaml:"dhcp4,omitempty"`
	DHCP6         bool             `yaml:"dhcp6,omitempty"`
	Gateway4      string           `yaml:"gateway4,omitempty"`
	Gateway6      string           `yaml:"gateway6,omitempty"`
	MTU           int              `yaml:"mtu,omitempty"`
	MacAddress    string           `yaml:"macaddress,omitempty"`
	Routes        []*Route         `yaml:"routes,omitempty"`
	RoutingPolicy []*RoutingPolicy `yaml:"routing-policy,omitempty"`
	Nameservers   *Nameservers     `yaml:"nameservers,omitempty"`
	Optional      bool             `yaml:"optional,omitempty"`
	AcceptRA      *bool            `yaml:"accept-ra,omitempty"`
	LinkLocal     []string         `yaml:"link-local,omitempty"`
	Wakeonlan     bool             `yaml:"wakeonlan,omitempty"`
}

// Match 设备匹配规则
type Match struct {
	Driver     string `yaml:"driver,omitempty"`
	MacAddress string `yaml:"macaddress,omitempty"`
	Name       string `yaml:"name,omitempty"`
}

// VethDevice veth 设备配置
type VethDevice struct {
	Addresses   []string     `yaml:"addresses,omitempty"`
	Routes      []*Route     `yaml:"routes,omitempty"`
	MTU         int          `yaml:"mtu,omitempty"`
	MacAddress  string       `yaml:"macaddress,omitempty"`
	Peer        *VethPeer    `yaml:"peer,omitempty"`
	Nameservers *Nameservers `yaml:"nameservers,omitempty"`
}

// VethPeer veth peer 配置
type VethPeer struct {
	Name       string   `yaml:"name"`
	Netns      string   `yaml:"netns,omitempty"`
	Addresses  []string `yaml:"addresses,omitempty"`
	Routes     []*Route `yaml:"routes,omitempty"`
	MTU        int      `yaml:"mtu,omitempty"`
	MacAddress string   `yaml:"macaddress,omitempty"`
}

// MacvlanDevice macvlan/macvtap 设备配置
type MacvlanDevice struct {
	Link        string       `yaml:"link"`
	Mode        string       `yaml:"mode,omitempty"` // bridge/vepa/private/passthru/source
	Addresses   []string     `yaml:"addresses,omitempty"`
	Routes      []*Route     `yaml:"routes,omitempty"`
	MTU         int          `yaml:"mtu,omitempty"`
	MacAddress  string       `yaml:"macaddress,omitempty"`
	Nameservers *Nameservers `yaml:"nameservers,omitempty"`
}

// Bridge 网桥配置
type Bridge struct {
	Interfaces    []string          `yaml:"interfaces,omitempty"`
	Addresses     []string          `yaml:"addresses,omitempty"`
	Routes        []*Route          `yaml:"routes,omitempty"`
	MTU           int               `yaml:"mtu,omitempty"`
	MacAddress    string            `yaml:"macaddress,omitempty"`
	Parameters    *BridgeParameters `yaml:"parameters,omitempty"`
	Nameservers   *Nameservers      `yaml:"nameservers,omitempty"`
	DHCP4         bool              `yaml:"dhcp4,omitempty"`
	DHCP6         bool              `yaml:"dhcp6,omitempty"`
	VlanFiltering *bool             `yaml:"vlan-filtering,omitempty"` // EVPN
	FDB           []*FDBEntry       `yaml:"fdb,omitempty"`            // 静态 FDB
	Neighbors     []*NeighEntry     `yaml:"neighbors,omitempty"`      // 静态 ARP/ND
}

// BridgeParameters 网桥参数
type BridgeParameters struct {
	STP          *bool          `yaml:"stp,omitempty"`
	ForwardDelay int            `yaml:"forward-delay,omitempty"`
	HelloTime    int            `yaml:"hello-time,omitempty"`
	MaxAge       int            `yaml:"max-age,omitempty"`
	Priority     int            `yaml:"priority,omitempty"`
	AgeingTime   int            `yaml:"ageing-time,omitempty"`
	PathCost     map[string]int `yaml:"path-cost,omitempty"`
	PortPriority map[string]int `yaml:"port-priority,omitempty"`
}

// Bond 绑定配置
type Bond struct {
	Interfaces  []string        `yaml:"interfaces,omitempty"`
	Addresses   []string        `yaml:"addresses,omitempty"`
	Routes      []*Route        `yaml:"routes,omitempty"`
	MTU         int             `yaml:"mtu,omitempty"`
	MacAddress  string          `yaml:"macaddress,omitempty"`
	Parameters  *BondParameters `yaml:"parameters,omitempty"`
	Nameservers *Nameservers    `yaml:"nameservers,omitempty"`
	DHCP4       bool            `yaml:"dhcp4,omitempty"`
	DHCP6       bool            `yaml:"dhcp6,omitempty"`
}

// BondParameters 绑定参数
type BondParameters struct {
	Mode                  string   `yaml:"mode,omitempty"`
	LACPRate              string   `yaml:"lacp-rate,omitempty"`
	MIIMonitorInterval    int      `yaml:"mii-monitor-interval,omitempty"`
	MinLinks              int      `yaml:"min-links,omitempty"`
	TransmitHashPolicy    string   `yaml:"transmit-hash-policy,omitempty"`
	ADSelect              string   `yaml:"ad-select,omitempty"`
	AllSlavesActive       bool     `yaml:"all-slaves-active,omitempty"`
	ARPInterval           int      `yaml:"arp-interval,omitempty"`
	ARPIPTargets          []string `yaml:"arp-ip-targets,omitempty"`
	ARPValidate           string   `yaml:"arp-validate,omitempty"`
	ARPAllTargets         string   `yaml:"arp-all-targets,omitempty"`
	UpDelay               int      `yaml:"up-delay,omitempty"`
	DownDelay             int      `yaml:"down-delay,omitempty"`
	FailOverMACPolicy     string   `yaml:"fail-over-mac-policy,omitempty"`
	GratuitousARP         int      `yaml:"gratuitous-arp,omitempty"`
	PacketsPerSlave       int      `yaml:"packets-per-slave,omitempty"`
	PrimaryReselectPolicy string   `yaml:"primary-reselect-policy,omitempty"`
	ResendIGMP            int      `yaml:"resend-igmp,omitempty"`
	LearnPacketInterval   int      `yaml:"learn-packet-interval,omitempty"`
	Primary               string   `yaml:"primary,omitempty"`
}

// Vlan VLAN 配置
type Vlan struct {
	ID          int          `yaml:"id"`
	Link        string       `yaml:"link"`
	Addresses   []string     `yaml:"addresses,omitempty"`
	Routes      []*Route     `yaml:"routes,omitempty"`
	MTU         int          `yaml:"mtu,omitempty"`
	MacAddress  string       `yaml:"macaddress,omitempty"`
	Nameservers *Nameservers `yaml:"nameservers,omitempty"`
	DHCP4       bool         `yaml:"dhcp4,omitempty"`
	DHCP6       bool         `yaml:"dhcp6,omitempty"`
}

// Vxlan VXLAN 配置
type Vxlan struct {
	ID             int           `yaml:"id"`
	Link           string        `yaml:"link,omitempty"`
	Local          string        `yaml:"local,omitempty"`
	Remote         string        `yaml:"remote,omitempty"`
	Group          string        `yaml:"group,omitempty"`
	Port           int           `yaml:"port,omitempty"`
	DestPort       int           `yaml:"dest-port,omitempty"`
	PortRange      []int         `yaml:"port-range,omitempty"` // [low, high] 源端口范围
	TTL            int           `yaml:"ttl,omitempty"`
	TOS            int           `yaml:"tos,omitempty"`
	Ageing         int           `yaml:"ageing,omitempty"`            // FDB 老化时间 (秒)
	Limit          int           `yaml:"limit,omitempty"`             // FDB 条目限制
	Learning       *bool         `yaml:"learning,omitempty"`          // MAC 学习
	ARPProxy       *bool         `yaml:"arp-proxy,omitempty"`         // ARP 代理
	NeighSuppress  *bool         `yaml:"neigh-suppress,omitempty"`    // ARP/ND suppress
	L2miss         *bool         `yaml:"l2miss,omitempty"`            // L2 miss 通知
	L3miss         *bool         `yaml:"l3miss,omitempty"`            // L3 miss 通知
	RSC            *bool         `yaml:"rsc,omitempty"`               // Route Short Circuit
	NoAge          bool          `yaml:"noage,omitempty"`             // 禁用 FDB 老化
	GBP            bool          `yaml:"gbp,omitempty"`               // Group Based Policy
	External       bool          `yaml:"external,omitempty"`          // external/flow-based 模式
	UDPChecksum    bool          `yaml:"udp-checksum,omitempty"`      // UDP checksum
	UDP6ZeroCSumTx bool          `yaml:"udp6-zero-csum-tx,omitempty"` // IPv6 发送零校验和
	UDP6ZeroCSumRx bool          `yaml:"udp6-zero-csum-rx,omitempty"` // IPv6 接收零校验和
	Addresses      []string      `yaml:"addresses,omitempty"`
	Routes         []*Route      `yaml:"routes,omitempty"`
	MTU            int           `yaml:"mtu,omitempty"`
	MacAddress     string        `yaml:"macaddress,omitempty"`
	FDB            []*FDBEntry   `yaml:"fdb,omitempty"`       // 静态 FDB
	Neighbors      []*NeighEntry `yaml:"neighbors,omitempty"` // 静态 ARP/ND
}

// FDBEntry FDB 条目配置 (EVPN 静态 MAC)
type FDBEntry struct {
	MAC   string `yaml:"mac"`             // MAC 地址
	Dst   string `yaml:"dst,omitempty"`   // 远端 VTEP IP
	VNI   int    `yaml:"vni,omitempty"`   // VNI (可选)
	State string `yaml:"state,omitempty"` // permanent/static
}

// NeighEntry 邻居条目配置 (静态 ARP/ND)
type NeighEntry struct {
	IP    string `yaml:"ip"`              // IP 地址
	MAC   string `yaml:"mac"`             // MAC 地址
	State string `yaml:"state,omitempty"` // permanent/static
}

// Tunnel 隧道配置
type Tunnel struct {
	Mode      string   `yaml:"mode"` // gre/ipip/sit/vti/vti6/ip6gre/ip6ip6/ipip6
	Local     string   `yaml:"local,omitempty"`
	Remote    string   `yaml:"remote,omitempty"`
	TTL       int      `yaml:"ttl,omitempty"`
	Key       string   `yaml:"key,omitempty"`
	InputKey  string   `yaml:"input-key,omitempty"`
	OutputKey string   `yaml:"output-key,omitempty"`
	Addresses []string `yaml:"addresses,omitempty"`
	Routes    []*Route `yaml:"routes,omitempty"`
	MTU       int      `yaml:"mtu,omitempty"`
}

// Vrf VRF 配置
type Vrf struct {
	Table      int      `yaml:"table"`
	Interfaces []string `yaml:"interfaces,omitempty"`
	Routes     []*Route `yaml:"routes,omitempty"`
}

// Wireguard WireGuard 配置
type Wireguard struct {
	Addresses  []string         `yaml:"addresses,omitempty"`
	MTU        int              `yaml:"mtu,omitempty"`
	ListenPort int              `yaml:"listen-port,omitempty"`
	PrivateKey string           `yaml:"private-key,omitempty"`
	FwMark     int              `yaml:"fwmark,omitempty"`
	Peers      []*WireguardPeer `yaml:"peers,omitempty"`
	Routes     []*Route         `yaml:"routes,omitempty"`
}

// WireguardPeer WireGuard Peer 配置
type WireguardPeer struct {
	PublicKey           string   `yaml:"public-key"`
	Endpoint            string   `yaml:"endpoint,omitempty"`
	AllowedIPs          []string `yaml:"allowed-ips,omitempty"`
	PresharedKey        string   `yaml:"preshared-key,omitempty"`
	PersistentKeepalive int      `yaml:"persistent-keepalive,omitempty"`
}

// IpvlanDevice ipvlan 设备配置
type IpvlanDevice struct {
	Link        string       `yaml:"link"`
	Mode        string       `yaml:"mode,omitempty"` // l2/l3/l3s
	Addresses   []string     `yaml:"addresses,omitempty"`
	Routes      []*Route     `yaml:"routes,omitempty"`
	MTU         int          `yaml:"mtu,omitempty"`
	Nameservers *Nameservers `yaml:"nameservers,omitempty"`
}

// TunTapDevice TUN/TAP 设备配置
type TunTapDevice struct {
	Addresses   []string     `yaml:"addresses,omitempty"`
	Routes      []*Route     `yaml:"routes,omitempty"`
	MTU         int          `yaml:"mtu,omitempty"`
	User        string       `yaml:"user,omitempty"`
	Group       string       `yaml:"group,omitempty"`
	MultiQueue  bool         `yaml:"multi-queue,omitempty"`
	Nameservers *Nameservers `yaml:"nameservers,omitempty"`
}

// Route 路由配置
type Route struct {
	To     string `yaml:"to"`
	Via    string `yaml:"via,omitempty"`
	From   string `yaml:"from,omitempty"`
	Metric int    `yaml:"metric,omitempty"`
	Table  int    `yaml:"table,omitempty"`
	Scope  string `yaml:"scope,omitempty"`
	Type   string `yaml:"type,omitempty"`
	OnLink *bool  `yaml:"on-link,omitempty"`
	MTU    int    `yaml:"mtu,omitempty"`
}

// RoutingPolicy 路由策略配置
type RoutingPolicy struct {
	From          string `yaml:"from,omitempty"`
	To            string `yaml:"to,omitempty"`
	Table         int    `yaml:"table,omitempty"`
	Priority      int    `yaml:"priority,omitempty"`
	Mark          int    `yaml:"mark,omitempty"`
	TypeOfService int    `yaml:"type-of-service,omitempty"`
}

// Nameservers DNS 配置
type Nameservers struct {
	Addresses []string `yaml:"addresses,omitempty"`
	Search    []string `yaml:"search,omitempty"`
}

// Normalize 标准化配置，兼容旧格式
func (c *Config) Normalize() {
	// 如果用的是旧格式（顶层 netns:），转换为新格式
	if c.Network.Version == 0 && len(c.Netns) > 0 {
		c.Network.Netns = c.Netns
		c.Netns = nil
	}

	// 设置默认版本
	if c.Network.Version == 0 {
		c.Network.Version = 2
	}
}

// HasDefaultNamespaceConfig 检查是否有 default namespace 配置
func (c *Config) HasDefaultNamespaceConfig() bool {
	n := c.Network
	return len(n.Ethernets) > 0 ||
		len(n.DummyDevices) > 0 ||
		len(n.VethDevices) > 0 ||
		len(n.MacvlanDevices) > 0 ||
		len(n.MacvtapDevices) > 0 ||
		len(n.IpvlanDevices) > 0 ||
		len(n.Bridges) > 0 ||
		len(n.Bonds) > 0 ||
		len(n.Vlans) > 0 ||
		len(n.Vxlans) > 0 ||
		len(n.Tunnels) > 0 ||
		len(n.Wireguards) > 0 ||
		len(n.Vrfs) > 0 ||
		len(n.TunDevices) > 0 ||
		len(n.TapDevices) > 0
}

// ToNamespace 将顶层配置转换为 Namespace 结构
func (n *Network) ToNamespace() *Namespace {
	return &Namespace{
		Ethernets:      n.Ethernets,
		DummyDevices:   n.DummyDevices,
		VethDevices:    n.VethDevices,
		MacvlanDevices: n.MacvlanDevices,
		MacvtapDevices: n.MacvtapDevices,
		IpvlanDevices:  n.IpvlanDevices,
		Bridges:        n.Bridges,
		Bonds:          n.Bonds,
		Vlans:          n.Vlans,
		Vxlans:         n.Vxlans,
		Tunnels:        n.Tunnels,
		Wireguards:     n.Wireguards,
		Vrfs:           n.Vrfs,
		TunDevices:     n.TunDevices,
		TapDevices:     n.TapDevices,
	}
}

// GetNetnsNames 获取所有 netns 名称（排序后）
func (c *Config) GetNetnsNames() []string {
	names := make([]string, 0, len(c.Network.Netns))
	for name := range c.Network.Netns {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// LoadConfig 从目录加载配置文件
func LoadConfig(dirPath string) (*Config, error) {
	files, err := filepath.Glob(filepath.Join(dirPath, "*.yaml"))
	if err != nil {
		return nil, fmt.Errorf("failed to glob yaml files: %w", err)
	}

	// 也支持 .yml 扩展名
	ymlFiles, err := filepath.Glob(filepath.Join(dirPath, "*.yml"))
	if err != nil {
		return nil, fmt.Errorf("failed to glob yml files: %w", err)
	}
	files = append(files, ymlFiles...)

	if len(files) == 0 {
		return nil, fmt.Errorf("no yaml files found in %s", dirPath)
	}

	// 排序确保加载顺序一致
	sort.Strings(files)

	merged := &Config{
		Network: Network{
			Ethernets:      make(map[string]*Ethernet),
			DummyDevices:   make(map[string]*Ethernet),
			VethDevices:    make(map[string]*VethDevice),
			MacvlanDevices: make(map[string]*MacvlanDevice),
			MacvtapDevices: make(map[string]*MacvlanDevice),
			IpvlanDevices:  make(map[string]*IpvlanDevice),
			Bridges:        make(map[string]*Bridge),
			Bonds:          make(map[string]*Bond),
			Vlans:          make(map[string]*Vlan),
			Vxlans:         make(map[string]*Vxlan),
			Tunnels:        make(map[string]*Tunnel),
			Wireguards:     make(map[string]*Wireguard),
			Vrfs:           make(map[string]*Vrf),
			TunDevices:     make(map[string]*TunTapDevice),
			TapDevices:     make(map[string]*TunTapDevice),
			Netns:          make(map[string]*Namespace),
		},
	}

	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("failed to read %s: %w", file, err)
		}

		var cfg Config
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("failed to parse %s: %w", file, err)
		}

		cfg.Normalize()
		mergeConfig(merged, &cfg)
	}

	return merged, nil
}

// LoadConfigFile 从单个文件加载配置
func LoadConfigFile(filePath string) (*Config, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", filePath, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse %s: %w", filePath, err)
	}

	cfg.Normalize()
	return &cfg, nil
}

// mergeConfig 合并两个配置
func mergeConfig(dst, src *Config) {
	// 合并版本（取较大值）
	if src.Network.Version > dst.Network.Version {
		dst.Network.Version = src.Network.Version
	}

	// 合并渲染器
	if src.Network.Renderer != "" {
		dst.Network.Renderer = src.Network.Renderer
	}

	// 合并各类设备配置
	mergeMap(dst.Network.Ethernets, src.Network.Ethernets)
	mergeMap(dst.Network.DummyDevices, src.Network.DummyDevices)
	mergeMap(dst.Network.VethDevices, src.Network.VethDevices)
	mergeMap(dst.Network.MacvlanDevices, src.Network.MacvlanDevices)
	mergeMap(dst.Network.MacvtapDevices, src.Network.MacvtapDevices)
	mergeMap(dst.Network.IpvlanDevices, src.Network.IpvlanDevices)
	mergeMap(dst.Network.Bridges, src.Network.Bridges)
	mergeMap(dst.Network.Bonds, src.Network.Bonds)
	mergeMap(dst.Network.Vlans, src.Network.Vlans)
	mergeMap(dst.Network.Vxlans, src.Network.Vxlans)
	mergeMap(dst.Network.Tunnels, src.Network.Tunnels)
	mergeMap(dst.Network.Wireguards, src.Network.Wireguards)
	mergeMap(dst.Network.Vrfs, src.Network.Vrfs)
	mergeMap(dst.Network.TunDevices, src.Network.TunDevices)
	mergeMap(dst.Network.TapDevices, src.Network.TapDevices)

	// 合并 netns 配置
	for name, ns := range src.Network.Netns {
		if _, exists := dst.Network.Netns[name]; !exists {
			dst.Network.Netns[name] = ns
		} else {
			mergeNamespace(dst.Network.Netns[name], ns)
		}
	}
}

// mergeMap 合并 map
func mergeMap[K comparable, V any](dst, src map[K]V) {
	for k, v := range src {
		dst[k] = v
	}
}

// mergeNamespace 合并 namespace 配置
func mergeNamespace(dst, src *Namespace) {
	if dst.Ethernets == nil {
		dst.Ethernets = make(map[string]*Ethernet)
	}
	mergeMap(dst.Ethernets, src.Ethernets)

	if dst.DummyDevices == nil {
		dst.DummyDevices = make(map[string]*Ethernet)
	}
	mergeMap(dst.DummyDevices, src.DummyDevices)

	if dst.VethDevices == nil {
		dst.VethDevices = make(map[string]*VethDevice)
	}
	mergeMap(dst.VethDevices, src.VethDevices)

	if dst.MacvlanDevices == nil {
		dst.MacvlanDevices = make(map[string]*MacvlanDevice)
	}
	mergeMap(dst.MacvlanDevices, src.MacvlanDevices)

	if dst.MacvtapDevices == nil {
		dst.MacvtapDevices = make(map[string]*MacvlanDevice)
	}
	mergeMap(dst.MacvtapDevices, src.MacvtapDevices)

	if dst.IpvlanDevices == nil {
		dst.IpvlanDevices = make(map[string]*IpvlanDevice)
	}
	mergeMap(dst.IpvlanDevices, src.IpvlanDevices)

	if dst.Bridges == nil {
		dst.Bridges = make(map[string]*Bridge)
	}
	mergeMap(dst.Bridges, src.Bridges)

	if dst.Bonds == nil {
		dst.Bonds = make(map[string]*Bond)
	}
	mergeMap(dst.Bonds, src.Bonds)

	if dst.Vlans == nil {
		dst.Vlans = make(map[string]*Vlan)
	}
	mergeMap(dst.Vlans, src.Vlans)

	if dst.Vxlans == nil {
		dst.Vxlans = make(map[string]*Vxlan)
	}
	mergeMap(dst.Vxlans, src.Vxlans)

	if dst.Tunnels == nil {
		dst.Tunnels = make(map[string]*Tunnel)
	}
	mergeMap(dst.Tunnels, src.Tunnels)

	if dst.Wireguards == nil {
		dst.Wireguards = make(map[string]*Wireguard)
	}
	mergeMap(dst.Wireguards, src.Wireguards)

	if dst.Vrfs == nil {
		dst.Vrfs = make(map[string]*Vrf)
	}
	mergeMap(dst.Vrfs, src.Vrfs)

	if dst.TunDevices == nil {
		dst.TunDevices = make(map[string]*TunTapDevice)
	}
	mergeMap(dst.TunDevices, src.TunDevices)

	if dst.TapDevices == nil {
		dst.TapDevices = make(map[string]*TunTapDevice)
	}
	mergeMap(dst.TapDevices, src.TapDevices)

	if src.PostScript != "" {
		dst.PostScript = src.PostScript
	}
}

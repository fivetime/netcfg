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
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// supportedNetworkKeys 是 network: 下 netcfg 实际处理的键集合。
// 用于对 netplan 中存在但 netcfg 不支持的配置段告警（见 warnUnsupportedConfig）。
var supportedNetworkKeys = map[string]bool{
	"version": true, "renderer": true,
	"ethernets": true, "wifis": true, "dummy-devices": true,
	"virtual-ethernets": true, "veth-devices": true,
	"macvlan-devices": true, "macvtap-devices": true, "ipvlan-devices": true,
	"bridges": true, "bonds": true, "vlans": true,
	"tunnels": true, "vrfs": true,
	"tun-devices": true, "tap-devices": true, "netns": true,
	"vpp": true, // VPP 后端全局段（docs/vpp-backend-design.md）
}

// warnUnsupportedConfig 解析原始 YAML 顶层结构，对 netcfg 不支持/会忽略的配置段
// 输出告警，避免用户误以为这些配置已生效（静默失效）。netplan 中的 wifis /
// modems / openvswitch / nm-devices 等都会在此被识别并提示。
// 仅告警，不阻断解析（netcfg 仍尽力应用其余受支持的配置）。
func warnUnsupportedConfig(data []byte, file string) {
	var raw map[string]interface{}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return // 解析错误由主流程统一报告
	}
	for k, v := range raw {
		switch k {
		case "network":
			netMap, ok := v.(map[string]interface{})
			if !ok {
				continue
			}
			for nk := range netMap {
				if !supportedNetworkKeys[nk] {
					slog.Warn("unsupported network configuration key; ignored",
						"key", "network."+nk, "file", file)
				}
			}
		case "netns":
			// 旧版顶层 netns 格式，受支持
		default:
			slog.Warn("unsupported top-level configuration key; ignored", "key", k, "file", file)
		}
	}
}

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
	Ethernets        map[string]*Ethernet        `yaml:"ethernets,omitempty"`
	Wifis            map[string]*Wifi            `yaml:"wifis,omitempty"`
	DummyDevices     map[string]*Ethernet        `yaml:"dummy-devices,omitempty"`
	VirtualEthernets map[string]*VirtualEthernet `yaml:"virtual-ethernets,omitempty"` // netplan 标准 veth
	VethDevices      map[string]*VethDevice      `yaml:"veth-devices,omitempty"`      // netcfg 扩展：跨 netns veth
	MacvlanDevices   map[string]*MacvlanDevice   `yaml:"macvlan-devices,omitempty"`
	MacvtapDevices   map[string]*MacvlanDevice   `yaml:"macvtap-devices,omitempty"`
	IpvlanDevices    map[string]*IpvlanDevice    `yaml:"ipvlan-devices,omitempty"`
	Bridges          map[string]*Bridge          `yaml:"bridges,omitempty"`
	Bonds            map[string]*Bond            `yaml:"bonds,omitempty"`
	Vlans            map[string]*Vlan            `yaml:"vlans,omitempty"`
	Vxlans           map[string]*Vxlan           `yaml:"-"` // 内部表示：tunnels:mode:vxlan 经 Normalize 填充
	Tunnels          map[string]*Tunnel          `yaml:"tunnels,omitempty"`
	Vrfs             map[string]*Vrf             `yaml:"vrfs,omitempty"`
	TunDevices       map[string]*TunTapDevice    `yaml:"tun-devices,omitempty"`
	TapDevices       map[string]*TunTapDevice    `yaml:"tap-devices,omitempty"`

	// VPP 后端全局配置（运行时/启动；netcfg 新增，见 docs/vpp-backend-design.md）
	VPP *VPPGlobal `yaml:"vpp,omitempty"`

	// netns 配置
	Netns map[string]*Namespace `yaml:"netns,omitempty"`
}

// Namespace 网络命名空间配置
type Namespace struct {
	Loopback         *Ethernet                   `yaml:"loopback,omitempty"`
	Ethernets        map[string]*Ethernet        `yaml:"ethernets,omitempty"`
	Wifis            map[string]*Wifi            `yaml:"wifis,omitempty"`
	DummyDevices     map[string]*Ethernet        `yaml:"dummy-devices,omitempty"`
	VirtualEthernets map[string]*VirtualEthernet `yaml:"virtual-ethernets,omitempty"`
	VethDevices      map[string]*VethDevice      `yaml:"veth-devices,omitempty"`
	MacvlanDevices   map[string]*MacvlanDevice   `yaml:"macvlan-devices,omitempty"`
	MacvtapDevices   map[string]*MacvlanDevice   `yaml:"macvtap-devices,omitempty"`
	IpvlanDevices    map[string]*IpvlanDevice    `yaml:"ipvlan-devices,omitempty"`
	Bridges          map[string]*Bridge          `yaml:"bridges,omitempty"`
	Bonds            map[string]*Bond            `yaml:"bonds,omitempty"`
	Vlans            map[string]*Vlan            `yaml:"vlans,omitempty"`
	Vxlans           map[string]*Vxlan           `yaml:"-"` // 内部表示：tunnels:mode:vxlan 经 Normalize 填充
	Tunnels          map[string]*Tunnel          `yaml:"tunnels,omitempty"`
	Vrfs             map[string]*Vrf             `yaml:"vrfs,omitempty"`
	TunDevices       map[string]*TunTapDevice    `yaml:"tun-devices,omitempty"`
	TapDevices       map[string]*TunTapDevice    `yaml:"tap-devices,omitempty"`
	PostScript       string                      `yaml:"post-script,omitempty"`
}

// Ethernet 以太网设备配置
type Ethernet struct {
	Match          *Match           `yaml:"match,omitempty"`
	SetName        string           `yaml:"set-name,omitempty"`
	Addresses      []Address        `yaml:"addresses,omitempty"`
	DHCP4          bool             `yaml:"dhcp4,omitempty"`
	DHCP6          bool             `yaml:"dhcp6,omitempty"`
	DHCP4Overrides *DHCPOverrides   `yaml:"dhcp4-overrides,omitempty"`
	DHCP6Overrides *DHCPOverrides   `yaml:"dhcp6-overrides,omitempty"`
	Gateway4       string           `yaml:"gateway4,omitempty"`
	Gateway6       string           `yaml:"gateway6,omitempty"`
	MTU            int              `yaml:"mtu,omitempty"`
	IPv6MTU        int              `yaml:"ipv6-mtu,omitempty"`
	MacAddress     string           `yaml:"macaddress,omitempty"`
	Routes         []*Route         `yaml:"routes,omitempty"`
	RoutingPolicy  []*RoutingPolicy `yaml:"routing-policy,omitempty"`
	Nameservers    *Nameservers     `yaml:"nameservers,omitempty"`
	Optional       bool             `yaml:"optional,omitempty"`
	AcceptRA       *bool            `yaml:"accept-ra,omitempty"`
	RAOverrides    *RAOverrides     `yaml:"ra-overrides,omitempty"`
	IPv6Privacy    *bool            `yaml:"ipv6-privacy,omitempty"`
	IPv6AddrGen    string           `yaml:"ipv6-address-generation,omitempty"` // eui64/stable-privacy（addr_gen_mode）
	IPv6AddrToken  string           `yaml:"ipv6-address-token,omitempty"`      // SLAAC 静态接口标识（与 generation 互斥）
	LinkLocal      []string         `yaml:"link-local,omitempty"`
	Wakeonlan      bool             `yaml:"wakeonlan,omitempty"`       // WoL（ethtool -s wol g）
	EmitLLDP       *bool            `yaml:"emit-lldp,omitempty"`       // 需 LLDP daemon，netcfg 不实现（告警）
	InfinibandMode string           `yaml:"infiniband-mode,omitempty"` // IPoIB: connected/datagram（sysfs）
	Auth           *Auth            `yaml:"auth,omitempty"`            // 802.1X/EAP（生成 wpa_supplicant 配置，直接 spawn）

	// SR-IOV（physical 属性）
	Link                 string `yaml:"link,omitempty"`                   // VF 所属 PF（VF ethernet 上）
	VirtualFunctionCount int    `yaml:"virtual-function-count,omitempty"` // 在 PF 上创建的 VF 数
	EmbeddedSwitchMode   string `yaml:"embedded-switch-mode,omitempty"`   // legacy/switchdev
	DelayVFRebind        *bool  `yaml:"delay-virtual-functions-rebind,omitempty"`

	// 网卡 offload（physical 属性，*bool nil=不改，经 ethtool -K 设置）
	ReceiveChecksumOffload     *bool `yaml:"receive-checksum-offload,omitempty"`
	TransmitChecksumOffload    *bool `yaml:"transmit-checksum-offload,omitempty"`
	TCPSegmentationOffload     *bool `yaml:"tcp-segmentation-offload,omitempty"`
	TCP6SegmentationOffload    *bool `yaml:"tcp6-segmentation-offload,omitempty"`
	GenericSegmentationOffload *bool `yaml:"generic-segmentation-offload,omitempty"`
	GenericReceiveOffload      *bool `yaml:"generic-receive-offload,omitempty"`
	LargeReceiveOffload        *bool `yaml:"large-receive-offload,omitempty"`

	// 通用属性（P1-6）
	ActivationMode    string   `yaml:"activation-mode,omitempty"`    // manual/off：不自动 up（off 强制 down）
	DHCPIdentifier    string   `yaml:"dhcp-identifier,omitempty"`    // mac/duid：DHCPv4 client-id 来源
	IgnoreCarrier     *bool    `yaml:"ignore-carrier,omitempty"`     // netcfg 直接 netlink 下发，本就不依赖 carrier
	Critical          *bool    `yaml:"critical,omitempty"`           // netcfg 不随 carrier/重启清配置，本就等价
	OptionalAddresses []string `yaml:"optional-addresses,omitempty"` // online 判定时不必等待的地址类型

	// bridge 端口属性（成员设备上，enslave 后经 brport sysfs 应用）
	NeighSuppress   *bool `yaml:"neigh-suppress,omitempty"`
	Hairpin         *bool `yaml:"hairpin,omitempty"`
	PortMacLearning *bool `yaml:"port-mac-learning,omitempty"`

	// VPP 后端（做法 A：带 vpp 块或 renderer:vpp → 走 VPP）。见 docs/vpp-backend-design.md
	Renderer string     `yaml:"renderer,omitempty"` // 设备级覆盖（vpp / networkd…），默认继承全局
	VPP      *VPPDevice `yaml:"vpp,omitempty"`      // VPP 设备落地细节；存在即归 VPP
}

// VPPGlobal 顶层 vpp: 段——VPP 运行时/启动配置（netplan 无对应，netcfg 新增）。
type VPPGlobal struct {
	APISocket string      `yaml:"api-socket,omitempty"` // 默认 /run/vpp/api.sock
	Reconnect *bool       `yaml:"reconnect,omitempty"`  // 默认 true
	Startup   *VPPStartup `yaml:"startup,omitempty"`    // 生成 /etc/vpp/startup.conf（V1c）
}

// VPPStartup 生成 startup.conf 的开机持久参数（改动需 VPP 重启）。
type VPPStartup struct {
	MainCore        *int     `yaml:"main-core,omitempty"`
	Workers         *int     `yaml:"workers,omitempty"`
	CorelistWorkers string   `yaml:"corelist-workers,omitempty"`
	Hugepages       int      `yaml:"hugepages,omitempty"` // 2MB 页数
	Dpdk            *VPPDpdk `yaml:"dpdk,omitempty"`
}

// VPPDpdk startup.conf 的 dpdk{} 段。
type VPPDpdk struct {
	UioDriver string   `yaml:"uio-driver,omitempty"` // vfio-pci/igb_uio/uio_pci_generic/auto
	Dev       []string `yaml:"dev,omitempty"`        // 独占设备 PCI 清单
}

// VPPDevice 设备级 vpp: 子块——后端落地细节（仿 netplan openvswitch: 子块）。
// 存在即表示该设备归 VPP 管。各字段按 mode 取用，未用字段忽略。
type VPPDevice struct {
	Mode      string `yaml:"mode,omitempty"`    // af-packet(默认)/dpdk/avf/rdma/tap/loopback/memif
	HostIf    string `yaml:"host-if,omitempty"` // af-packet/rdma/tap；默认=设备名
	PCI       string `yaml:"pci,omitempty"`     // dpdk/avf 必填
	RxQueues  int    `yaml:"rx-queues,omitempty"`
	TxQueues  int    `yaml:"tx-queues,omitempty"`
	NumRxDesc int    `yaml:"num-rx-desc,omitempty"`
	NumTxDesc int    `yaml:"num-tx-desc,omitempty"`
	HostNS    string `yaml:"host-ns,omitempty"`   // tap 内核端 netns
	Socket    string `yaml:"socket,omitempty"`    // memif socket 文件
	ID        int    `yaml:"id,omitempty"`        // memif id
	Role      string `yaml:"role,omitempty"`      // memif master/slave
	RingSize  int    `yaml:"ring-size,omitempty"` // memif 环大小
	BdID      int    `yaml:"bd-id,omitempty"`     // bridge domain 数字 id（bridge 用，缺省自动分配）
}

// Auth netplan 认证设置（802.1X 有线 / WiFi EAP）。
// netcfg 据此生成 wpa_supplicant 配置与 systemd unit，由 systemd 启动
// wpa_supplicant（内核不做 EAP，必须依赖 supplicant）。
type Auth struct {
	KeyManagement     string `yaml:"key-management,omitempty"` // none/psk/psk-sha256/eap/eap-sha256/sae/802.1x
	Password          string `yaml:"password,omitempty"`
	Method            string `yaml:"method,omitempty"` // tls/peap/leap/pwd/ttls
	Identity          string `yaml:"identity,omitempty"`
	AnonymousIdentity string `yaml:"anonymous-identity,omitempty"`
	CACertificate     string `yaml:"ca-certificate,omitempty"`
	ClientCertificate string `yaml:"client-certificate,omitempty"`
	ClientKey         string `yaml:"client-key,omitempty"`
	ClientKeyPassword string `yaml:"client-key-password,omitempty"`
	Phase2Auth        string `yaml:"phase2-auth,omitempty"`
}

// Wifi 无线设备配置（netplan wifis）。netcfg 生成 wpa_supplicant 配置 + systemd unit
// （-D nl80211），并对 wlan 设备应用常规地址/路由/DHCP（复用以太网路径）。
type Wifi struct {
	AccessPoints     map[string]*AccessPoint `yaml:"access-points,omitempty"`
	Addresses        []Address               `yaml:"addresses,omitempty"`
	DHCP4            bool                    `yaml:"dhcp4,omitempty"`
	DHCP6            bool                    `yaml:"dhcp6,omitempty"`
	Gateway4         string                  `yaml:"gateway4,omitempty"`
	Gateway6         string                  `yaml:"gateway6,omitempty"`
	MTU              int                     `yaml:"mtu,omitempty"`
	Routes           []*Route                `yaml:"routes,omitempty"`
	Nameservers      *Nameservers            `yaml:"nameservers,omitempty"`
	AcceptRA         *bool                   `yaml:"accept-ra,omitempty"`
	LinkLocal        []string                `yaml:"link-local,omitempty"`
	RegulatoryDomain string                  `yaml:"regulatory-domain,omitempty"`
	Wakeonwlan       []string                `yaml:"wakeonwlan,omitempty"`
}

// AccessPoint 单个 Wi-Fi 接入点（SSID 为 map 键）。
type AccessPoint struct {
	Password string `yaml:"password,omitempty"` // WPA-PSK 口令（等价于 auth.key-management=psk）
	Mode     string `yaml:"mode,omitempty"`     // infrastructure(默认)/ap/adhoc
	Band     string `yaml:"band,omitempty"`     // 2.4GHz/5GHz
	Channel  int    `yaml:"channel,omitempty"`
	BSSID    string `yaml:"bssid,omitempty"`
	Hidden   bool   `yaml:"hidden,omitempty"`
	Auth     *Auth  `yaml:"auth,omitempty"`
}

// RAOverrides IPv6 Router Advertisement 行为覆盖（netplan ra-overrides）。
//
// 注意：这些选项本质是 networkd 后端特性（netplan 文档亦注明 "only supported
// with networkd back end"）。netcfg 使用内核 RA（accept-ra），无用户态 RA 客户端：
// use-dns/use-domains 的 RA DNS/域名无人消费，table 也无法将内核 RA 路由重定向到
// 自定义表。因此目前仅保留 schema，并在 apply 时显式告警（避免静默忽略），待将来
// 引入用户态 RA/NDISC 客户端后再实现。
// UseDomains 取 bool 或特殊值 "route"，故用 interface{} 以兼容两种写法、不破坏解析。
type RAOverrides struct {
	UseDNS     *bool       `yaml:"use-dns,omitempty"`
	UseDomains interface{} `yaml:"use-domains,omitempty"`
	Table      int         `yaml:"table,omitempty"`
}

// DHCPOverrides 覆盖 DHCPv4/v6 的默认行为（netplan dhcp4-overrides/dhcp6-overrides）。
// *bool 字段 nil 表示未设置（采用 netplan 默认 true）。
// use-domains 取 bool 或特殊值 "route"（interface{} 兼容两种写法）。
// 说明：netcfg 在应用 lease 时真实地 honor use-dns/use-mtu/use-routes/route-metric/
// use-domains，并在请求时 honor send-hostname/hostname；use-ntp/use-hostname 因 netcfg
// 不配置 NTP / 系统主机名而为 no-op（显式设置时告警）。
type DHCPOverrides struct {
	UseDNS       *bool       `yaml:"use-dns,omitempty"`
	UseNTP       *bool       `yaml:"use-ntp,omitempty"`
	SendHostname *bool       `yaml:"send-hostname,omitempty"`
	UseHostname  *bool       `yaml:"use-hostname,omitempty"`
	UseMTU       *bool       `yaml:"use-mtu,omitempty"`
	Hostname     string      `yaml:"hostname,omitempty"`
	UseRoutes    *bool       `yaml:"use-routes,omitempty"`
	RouteMetric  int         `yaml:"route-metric,omitempty"`
	UseDomains   interface{} `yaml:"use-domains,omitempty"`
}

// Address 表示一个 IP 地址条目，支持两种 YAML 写法（兼容旧的纯字符串写法）：
//   - 纯字符串："192.168.1.10/24"
//   - 带选项的单键映射："192.168.1.10/24": {lifetime: 0, label: eth0:1}
//
// lifetime 取 "forever"（默认/永久）或 "0"（立即弃用，preferred_lft=0）；
// label 为 IPv4 地址别名标签。
type Address struct {
	CIDR     string
	Lifetime string
	Label    string
}

type addressOptions struct {
	Lifetime interface{} `yaml:"lifetime,omitempty"` // "forever" 或 0，用 interface{} 兼容裸整数
	Label    string      `yaml:"label,omitempty"`
}

// UnmarshalYAML 解析纯字符串或单键映射两种地址写法。
func (a *Address) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		a.CIDR = value.Value
		return nil
	case yaml.MappingNode:
		if len(value.Content) != 2 {
			return fmt.Errorf("address entry must have exactly one CIDR key")
		}
		a.CIDR = value.Content[0].Value
		var opts addressOptions
		if err := value.Content[1].Decode(&opts); err != nil {
			return fmt.Errorf("invalid options for address %s: %w", a.CIDR, err)
		}
		if opts.Lifetime != nil {
			a.Lifetime = fmt.Sprintf("%v", opts.Lifetime)
		}
		a.Label = opts.Label
		return nil
	default:
		return fmt.Errorf("invalid address entry (expected string or mapping)")
	}
}

// Match 设备匹配规则
type Match struct {
	Driver     string `yaml:"driver,omitempty"`
	MacAddress string `yaml:"macaddress,omitempty"`
	Name       string `yaml:"name,omitempty"`
}

// VethDevice veth 设备配置
type VethDevice struct {
	Addresses   []Address    `yaml:"addresses,omitempty"`
	Routes      []*Route     `yaml:"routes,omitempty"`
	MTU         int          `yaml:"mtu,omitempty"`
	MacAddress  string       `yaml:"macaddress,omitempty"`
	Peer        *VethPeer    `yaml:"peer,omitempty"`
	Nameservers *Nameservers `yaml:"nameservers,omitempty"`
}

// VethPeer veth peer 配置
type VethPeer struct {
	Name       string    `yaml:"name"`
	Netns      string    `yaml:"netns,omitempty"`
	Addresses  []Address `yaml:"addresses,omitempty"`
	Routes     []*Route  `yaml:"routes,omitempty"`
	MTU        int       `yaml:"mtu,omitempty"`
	MacAddress string    `yaml:"macaddress,omitempty"`
}

// VirtualEthernet netplan 标准 virtual-ethernets 设备。
// 每个 veth 端点是独立的顶层条目，通过 peer 互相引用对端名字。
type VirtualEthernet struct {
	Peer        string       `yaml:"peer"` // 对端端点名
	Addresses   []Address    `yaml:"addresses,omitempty"`
	Routes      []*Route     `yaml:"routes,omitempty"`
	MTU         int          `yaml:"mtu,omitempty"`
	MacAddress  string       `yaml:"macaddress,omitempty"`
	Nameservers *Nameservers `yaml:"nameservers,omitempty"`
}

// MacvlanDevice macvlan/macvtap 设备配置
type MacvlanDevice struct {
	Link        string       `yaml:"link"`
	Mode        string       `yaml:"mode,omitempty"` // bridge/vepa/private/passthru/source
	Addresses   []Address    `yaml:"addresses,omitempty"`
	Routes      []*Route     `yaml:"routes,omitempty"`
	MTU         int          `yaml:"mtu,omitempty"`
	MacAddress  string       `yaml:"macaddress,omitempty"`
	Nameservers *Nameservers `yaml:"nameservers,omitempty"`
}

// Bridge 网桥配置
type Bridge struct {
	Interfaces    []string          `yaml:"interfaces,omitempty"`
	Addresses     []Address         `yaml:"addresses,omitempty"`
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
	Renderer      string            `yaml:"renderer,omitempty"`       // VPP 后端：设备级覆盖
	VPP           *VPPDevice        `yaml:"vpp,omitempty"`            // VPP 后端：bridge domain（bd-id）
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
	Addresses   []Address       `yaml:"addresses,omitempty"`
	Routes      []*Route        `yaml:"routes,omitempty"`
	MTU         int             `yaml:"mtu,omitempty"`
	MacAddress  string          `yaml:"macaddress,omitempty"`
	Parameters  *BondParameters `yaml:"parameters,omitempty"`
	Nameservers *Nameservers    `yaml:"nameservers,omitempty"`
	DHCP4       bool            `yaml:"dhcp4,omitempty"`
	DHCP6       bool            `yaml:"dhcp6,omitempty"`
	Renderer    string          `yaml:"renderer,omitempty"` // VPP 后端：设备级覆盖
	VPP         *VPPDevice      `yaml:"vpp,omitempty"`      // VPP 后端：归属信号
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
	Addresses   []Address    `yaml:"addresses,omitempty"`
	Routes      []*Route     `yaml:"routes,omitempty"`
	MTU         int          `yaml:"mtu,omitempty"`
	MacAddress  string       `yaml:"macaddress,omitempty"`
	Nameservers *Nameservers `yaml:"nameservers,omitempty"`
	DHCP4       bool         `yaml:"dhcp4,omitempty"`
	DHCP6       bool         `yaml:"dhcp6,omitempty"`
	Renderer    string       `yaml:"renderer,omitempty"` // VPP 后端：设备级覆盖
	VPP         *VPPDevice   `yaml:"vpp,omitempty"`      // VPP 后端：sub-interface 归属信号
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
	Addresses      []Address     `yaml:"addresses,omitempty"`
	Routes         []*Route      `yaml:"routes,omitempty"`
	MTU            int           `yaml:"mtu,omitempty"`
	MacAddress     string        `yaml:"macaddress,omitempty"`
	FDB            []*FDBEntry   `yaml:"fdb,omitempty"`       // 静态 FDB
	Neighbors      []*NeighEntry `yaml:"neighbors,omitempty"` // 静态 ARP/ND
	VPP            *VPPDevice    `yaml:"-"`                   // VPP 后端：由 Normalize 从 Tunnel 带入
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
	Mode      string    `yaml:"mode"` // gre/ipip/sit/vti/vti6/ip6gre/ip6ip6/ipip6/wireguard
	Local     string    `yaml:"local,omitempty"`
	Remote    string    `yaml:"remote,omitempty"`
	TTL       int       `yaml:"ttl,omitempty"`
	TOS       int       `yaml:"tos,omitempty"`
	Key       string    `yaml:"key,omitempty"` // GRE key；mode=wireguard 时为 base64 私钥
	InputKey  string    `yaml:"input-key,omitempty"`
	OutputKey string    `yaml:"output-key,omitempty"`
	Addresses []Address `yaml:"addresses,omitempty"`
	Routes    []*Route  `yaml:"routes,omitempty"`
	MTU       int       `yaml:"mtu,omitempty"`

	// mode=wireguard 专用（netplan 标准 tunnels:mode:wireguard）
	Port  int                    `yaml:"port,omitempty"`  // WireGuard 监听端口 / VXLAN dest port
	Mark  int                    `yaml:"mark,omitempty"`  // fwmark
	Peers []*TunnelWireguardPeer `yaml:"peers,omitempty"` // WireGuard peers

	// mode=vxlan 专用（netplan 标准 tunnels:mode:vxlan）。
	// fdb/neighbors/l2miss/l3miss/external 等为 netcfg EVPN 扩展（netplan 无对应语法）。
	ID             int           `yaml:"id,omitempty"`   // VXLAN VNI
	Link           string        `yaml:"link,omitempty"` // 底层设备
	Group          string        `yaml:"group,omitempty"`
	DestPort       int           `yaml:"dest-port,omitempty"`
	PortRange      []int         `yaml:"port-range,omitempty"`
	MacLearning    *bool         `yaml:"mac-learning,omitempty"`
	NeighSuppress  *bool         `yaml:"neigh-suppress,omitempty"`
	Ageing         int           `yaml:"ageing,omitempty"`
	Limit          int           `yaml:"limit,omitempty"`
	ARPProxy       *bool         `yaml:"arp-proxy,omitempty"`
	L2miss         *bool         `yaml:"l2miss,omitempty"`
	L3miss         *bool         `yaml:"l3miss,omitempty"`
	RSC            *bool         `yaml:"rsc,omitempty"`
	NoAge          bool          `yaml:"noage,omitempty"`
	GBP            bool          `yaml:"gbp,omitempty"`
	External       bool          `yaml:"external,omitempty"`
	UDPChecksum    bool          `yaml:"udp-checksum,omitempty"`
	UDP6ZeroCSumTx bool          `yaml:"udp6-zero-csum-tx,omitempty"`
	UDP6ZeroCSumRx bool          `yaml:"udp6-zero-csum-rx,omitempty"`
	MacAddress     string        `yaml:"macaddress,omitempty"`
	FDB            []*FDBEntry   `yaml:"fdb,omitempty"`
	Neighbors      []*NeighEntry `yaml:"neighbors,omitempty"`

	Renderer string     `yaml:"renderer,omitempty"` // VPP 后端：设备级覆盖
	VPP      *VPPDevice `yaml:"vpp,omitempty"`      // VPP 后端：vxlan tunnel 归属信号
}

// TunnelWireguardPeer netplan tunnels:mode:wireguard 的 peer（与自有 wireguards: 的
// 扁平 peer 不同，netplan 用嵌套 keys.{public,shared} 与 keepalive）。
type TunnelWireguardPeer struct {
	Keys       *WireguardKeys `yaml:"keys,omitempty"`
	Endpoint   string         `yaml:"endpoint,omitempty"`
	AllowedIPs []string       `yaml:"allowed-ips,omitempty"`
	Keepalive  int            `yaml:"keepalive,omitempty"`
}

// WireguardKeys netplan peer 的密钥对（public 必填，shared 为预共享密钥）。
type WireguardKeys struct {
	Public string `yaml:"public,omitempty"`
	Shared string `yaml:"shared,omitempty"`
}

// Vrf VRF 配置
type Vrf struct {
	Table         int              `yaml:"table"`
	Interfaces    []string         `yaml:"interfaces,omitempty"`
	Routes        []*Route         `yaml:"routes,omitempty"`
	RoutingPolicy []*RoutingPolicy `yaml:"routing-policy,omitempty"`
}

// 注：WireGuard 配置遵循 netplan 标准 tunnels:mode:wireguard（见 Tunnel /
// TunnelWireguardPeer），不再提供自造的顶层 wireguards: 键。

// IpvlanDevice ipvlan 设备配置
type IpvlanDevice struct {
	Link        string       `yaml:"link"`
	Mode        string       `yaml:"mode,omitempty"` // l2/l3/l3s
	Addresses   []Address    `yaml:"addresses,omitempty"`
	Routes      []*Route     `yaml:"routes,omitempty"`
	MTU         int          `yaml:"mtu,omitempty"`
	Nameservers *Nameservers `yaml:"nameservers,omitempty"`
}

// TunTapDevice TUN/TAP 设备配置
type TunTapDevice struct {
	Addresses   []Address    `yaml:"addresses,omitempty"`
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
	Addresses []string `yaml:"addresses,omitempty"` // DNS 服务器 IP（纯字符串，非接口地址）
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

	// netplan 把 VXLAN 表达为 tunnels:mode:vxlan，netcfg 内部按 VXLAN 设备处理。
	// 在此把 tunnels 里的 vxlan 条目移入 Vxlans，使其在 bridge 之前创建（端点常作
	// bridge 成员）。
	normalizeTunnelVxlans(&c.Network.Tunnels, &c.Network.Vxlans)
	for _, ns := range c.Network.Netns {
		if ns != nil {
			normalizeTunnelVxlans(&ns.Tunnels, &ns.Vxlans)
		}
	}
}

// normalizeTunnelVxlans 把 tunnels 中 mode=vxlan 的条目转换为 Vxlan 并移入 vxlans。
func normalizeTunnelVxlans(tunnels *map[string]*Tunnel, vxlans *map[string]*Vxlan) {
	if *tunnels == nil {
		return
	}
	for name, t := range *tunnels {
		if t == nil || !strings.EqualFold(t.Mode, "vxlan") {
			continue
		}
		if *vxlans == nil {
			*vxlans = make(map[string]*Vxlan)
		}
		(*vxlans)[name] = t.toVxlan()
		delete(*tunnels, name)
	}
}

// toVxlan 把 netplan tunnels:mode:vxlan 条目转换为 Vxlan。
func (t *Tunnel) toVxlan() *Vxlan {
	return &Vxlan{
		ID:             t.ID,
		Link:           t.Link,
		Local:          t.Local,
		Remote:         t.Remote,
		Group:          t.Group,
		Port:           t.Port,
		DestPort:       t.DestPort,
		PortRange:      t.PortRange,
		TTL:            t.TTL,
		TOS:            t.TOS,
		Ageing:         t.Ageing,
		Limit:          t.Limit,
		Learning:       t.MacLearning,
		ARPProxy:       t.ARPProxy,
		NeighSuppress:  t.NeighSuppress,
		L2miss:         t.L2miss,
		L3miss:         t.L3miss,
		RSC:            t.RSC,
		NoAge:          t.NoAge,
		GBP:            t.GBP,
		External:       t.External,
		UDPChecksum:    t.UDPChecksum,
		UDP6ZeroCSumTx: t.UDP6ZeroCSumTx,
		UDP6ZeroCSumRx: t.UDP6ZeroCSumRx,
		MTU:            t.MTU,
		MacAddress:     t.MacAddress,
		Addresses:      t.Addresses,
		Routes:         t.Routes,
		FDB:            t.FDB,
		Neighbors:      t.Neighbors,
		VPP:            t.VPP, // 带入 VPP 后端归属（tunnels:mode:vxlan 的 vpp 块）
	}
}

// HasDefaultNamespaceConfig 检查是否有 default namespace 配置
func (c *Config) HasDefaultNamespaceConfig() bool {
	n := c.Network
	return len(n.Ethernets) > 0 ||
		len(n.Wifis) > 0 ||
		len(n.DummyDevices) > 0 ||
		len(n.VirtualEthernets) > 0 ||
		len(n.VethDevices) > 0 ||
		len(n.MacvlanDevices) > 0 ||
		len(n.MacvtapDevices) > 0 ||
		len(n.IpvlanDevices) > 0 ||
		len(n.Bridges) > 0 ||
		len(n.Bonds) > 0 ||
		len(n.Vlans) > 0 ||
		len(n.Vxlans) > 0 ||
		len(n.Tunnels) > 0 ||
		len(n.Vrfs) > 0 ||
		len(n.TunDevices) > 0 ||
		len(n.TapDevices) > 0
}

// ToNamespace 将顶层配置转换为 Namespace 结构
func (n *Network) ToNamespace() *Namespace {
	return &Namespace{
		Ethernets:        n.Ethernets,
		Wifis:            n.Wifis,
		DummyDevices:     n.DummyDevices,
		VirtualEthernets: n.VirtualEthernets,
		VethDevices:      n.VethDevices,
		MacvlanDevices:   n.MacvlanDevices,
		MacvtapDevices:   n.MacvtapDevices,
		IpvlanDevices:    n.IpvlanDevices,
		Bridges:          n.Bridges,
		Bonds:            n.Bonds,
		Vlans:            n.Vlans,
		Vxlans:           n.Vxlans,
		Tunnels:          n.Tunnels,
		Vrfs:             n.Vrfs,
		TunDevices:       n.TunDevices,
		TapDevices:       n.TapDevices,
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
			Ethernets:        make(map[string]*Ethernet),
			Wifis:            make(map[string]*Wifi),
			DummyDevices:     make(map[string]*Ethernet),
			VirtualEthernets: make(map[string]*VirtualEthernet),
			VethDevices:      make(map[string]*VethDevice),
			MacvlanDevices:   make(map[string]*MacvlanDevice),
			MacvtapDevices:   make(map[string]*MacvlanDevice),
			IpvlanDevices:    make(map[string]*IpvlanDevice),
			Bridges:          make(map[string]*Bridge),
			Bonds:            make(map[string]*Bond),
			Vlans:            make(map[string]*Vlan),
			Vxlans:           make(map[string]*Vxlan),
			Tunnels:          make(map[string]*Tunnel),
			Vrfs:             make(map[string]*Vrf),
			TunDevices:       make(map[string]*TunTapDevice),
			TapDevices:       make(map[string]*TunTapDevice),
			Netns:            make(map[string]*Namespace),
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

		warnUnsupportedConfig(data, file)
		cfg.Normalize()
		mergeConfig(merged, &cfg)
	}

	// VPP 设备合法性校验（合并后整体校验：mode/pci、pci/host-if 不重复占用）
	if err := ValidateVPP(merged); err != nil {
		return nil, err
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

	warnUnsupportedConfig(data, filePath)
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
	mergeMap(dst.Network.Wifis, src.Network.Wifis)
	mergeMap(dst.Network.DummyDevices, src.Network.DummyDevices)
	mergeMap(dst.Network.VirtualEthernets, src.Network.VirtualEthernets)
	mergeMap(dst.Network.VethDevices, src.Network.VethDevices)
	mergeMap(dst.Network.MacvlanDevices, src.Network.MacvlanDevices)
	mergeMap(dst.Network.MacvtapDevices, src.Network.MacvtapDevices)
	mergeMap(dst.Network.IpvlanDevices, src.Network.IpvlanDevices)
	mergeMap(dst.Network.Bridges, src.Network.Bridges)
	mergeMap(dst.Network.Bonds, src.Network.Bonds)
	mergeMap(dst.Network.Vlans, src.Network.Vlans)
	mergeMap(dst.Network.Vxlans, src.Network.Vxlans)
	mergeMap(dst.Network.Tunnels, src.Network.Tunnels)
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

	if dst.Wifis == nil {
		dst.Wifis = make(map[string]*Wifi)
	}
	mergeMap(dst.Wifis, src.Wifis)

	if dst.DummyDevices == nil {
		dst.DummyDevices = make(map[string]*Ethernet)
	}
	mergeMap(dst.DummyDevices, src.DummyDevices)

	if dst.VirtualEthernets == nil {
		dst.VirtualEthernets = make(map[string]*VirtualEthernet)
	}
	mergeMap(dst.VirtualEthernets, src.VirtualEthernets)

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

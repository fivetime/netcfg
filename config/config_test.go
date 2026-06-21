/*
Copyright © 2024 netcfg authors

Unit tests for configuration parsing, normalization and merging.
*/

package config

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestNormalizeLegacyFormat(t *testing.T) {
	// 旧格式：顶层 netns:，无 version
	c := &Config{
		Netns: map[string]*Namespace{
			"ns1": {Ethernets: map[string]*Ethernet{"eth1": {}}},
		},
	}
	c.Normalize()

	if c.Netns != nil {
		t.Error("legacy top-level Netns should be cleared after Normalize")
	}
	if _, ok := c.Network.Netns["ns1"]; !ok {
		t.Error("legacy netns should be migrated into Network.Netns")
	}
	if c.Network.Version != 2 {
		t.Errorf("version = %d, want default 2", c.Network.Version)
	}
}

func TestNormalizeNewFormatKeepsVersion(t *testing.T) {
	c := &Config{Network: Network{Version: 2}}
	c.Normalize()
	if c.Network.Version != 2 {
		t.Errorf("version = %d, want 2", c.Network.Version)
	}
	// 已是新格式（version != 0），顶层 netns 不应被当作 legacy 迁移
	c2 := &Config{
		Network: Network{Version: 2},
		Netns:   map[string]*Namespace{"x": {}},
	}
	c2.Normalize()
	if c2.Netns == nil {
		t.Error("with version set, top-level Netns should NOT be migrated (treated as new format)")
	}
}

func TestHasDefaultNamespaceConfig(t *testing.T) {
	empty := &Config{}
	if empty.HasDefaultNamespaceConfig() {
		t.Error("empty config should have no default namespace config")
	}

	withEth := &Config{Network: Network{Ethernets: map[string]*Ethernet{"eth0": {}}}}
	if !withEth.HasDefaultNamespaceConfig() {
		t.Error("config with ethernets should have default namespace config")
	}

	withBridge := &Config{Network: Network{Bridges: map[string]*Bridge{"br0": {}}}}
	if !withBridge.HasDefaultNamespaceConfig() {
		t.Error("config with bridges should have default namespace config")
	}
}

func TestToNamespace(t *testing.T) {
	n := &Network{
		Ethernets: map[string]*Ethernet{"eth0": {}},
		Bridges:   map[string]*Bridge{"br0": {}},
	}
	ns := n.ToNamespace()
	if _, ok := ns.Ethernets["eth0"]; !ok {
		t.Error("ToNamespace should carry ethernets")
	}
	if _, ok := ns.Bridges["br0"]; !ok {
		t.Error("ToNamespace should carry bridges")
	}
}

func TestGetNetnsNamesSorted(t *testing.T) {
	c := &Config{Network: Network{Netns: map[string]*Namespace{
		"zebra": {}, "alpha": {}, "mike": {},
	}}}
	got := c.GetNetnsNames()
	want := []string{"alpha", "mike", "zebra"}
	if len(got) != 3 {
		t.Fatalf("got %d names, want 3: %v", len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("GetNetnsNames[%d] = %q, want %q (not sorted: %v)", i, got[i], want[i], got)
		}
	}
}

func TestAddressUnmarshal(t *testing.T) {
	yml := `
- 192.168.1.10/24
- 10.0.0.1/24:
    lifetime: 0
    label: eth0:1
- 2001:db8::1/64:
    lifetime: forever
`
	var addrs []Address
	if err := yaml.Unmarshal([]byte(yml), &addrs); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if len(addrs) != 3 {
		t.Fatalf("got %d addresses, want 3", len(addrs))
	}
	// 旧的纯字符串写法
	if addrs[0].CIDR != "192.168.1.10/24" || addrs[0].Lifetime != "" || addrs[0].Label != "" {
		t.Errorf("plain string address parsed wrong: %+v", addrs[0])
	}
	// 带选项的 map 写法 + 裸整数 lifetime
	if addrs[1].CIDR != "10.0.0.1/24" || addrs[1].Lifetime != "0" || addrs[1].Label != "eth0:1" {
		t.Errorf("map address parsed wrong: %+v", addrs[1])
	}
	if addrs[2].CIDR != "2001:db8::1/64" || addrs[2].Lifetime != "forever" {
		t.Errorf("v6 address parsed wrong: %+v", addrs[2])
	}
}

func TestDHCPOverridesUnmarshal(t *testing.T) {
	yml := `
use-dns: false
use-routes: false
route-metric: 200
send-hostname: false
hostname: myhost
use-domains: route
`
	var o DHCPOverrides
	if err := yaml.Unmarshal([]byte(yml), &o); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if o.UseDNS == nil || *o.UseDNS {
		t.Errorf("use-dns = %v, want false", o.UseDNS)
	}
	if o.RouteMetric != 200 {
		t.Errorf("route-metric = %d, want 200", o.RouteMetric)
	}
	if o.Hostname != "myhost" {
		t.Errorf("hostname = %q, want myhost", o.Hostname)
	}
	if s, ok := o.UseDomains.(string); !ok || s != "route" {
		t.Errorf("use-domains = %v, want string route", o.UseDomains)
	}
}

func TestRAOverridesUnmarshalBareBool(t *testing.T) {
	// use-domains 裸 bool 不应破坏解析（interface{} 的作用）
	var o RAOverrides
	if err := yaml.Unmarshal([]byte("use-dns: true\nuse-domains: true\ntable: 100\n"), &o); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if o.Table != 100 {
		t.Errorf("table = %d, want 100", o.Table)
	}
	if o.UseDNS == nil || !*o.UseDNS {
		t.Errorf("use-dns = %v, want true", o.UseDNS)
	}
}

func TestAuthUnmarshal(t *testing.T) {
	yml := `
key-management: 802.1x
method: ttls
identity: fluffy@cisco.com
password: secret
phase2-auth: MSCHAPV2
ca-certificate: /etc/ssl/ca.pem
`
	var a Auth
	if err := yaml.Unmarshal([]byte(yml), &a); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if a.KeyManagement != "802.1x" || a.Method != "ttls" || a.Identity != "fluffy@cisco.com" {
		t.Errorf("auth parsed wrong: %+v", a)
	}
	if a.Phase2Auth != "MSCHAPV2" || a.CACertificate != "/etc/ssl/ca.pem" {
		t.Errorf("auth phase2/ca parsed wrong: %+v", a)
	}
}

func TestLoadConfigMultiFileMerge(t *testing.T) {
	dir := t.TempDir()

	writeFile(t, dir, "01-base.yaml", `
network:
  version: 2
  ethernets:
    eth0:
      addresses: [192.168.1.10/24]
`)
	writeFile(t, dir, "02-extra.yaml", `
network:
  ethernets:
    eth1:
      dhcp4: true
  bridges:
    br0:
      interfaces: [eth1]
`)

	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	if _, ok := cfg.Network.Ethernets["eth0"]; !ok {
		t.Error("merged config missing eth0 from first file")
	}
	if _, ok := cfg.Network.Ethernets["eth1"]; !ok {
		t.Error("merged config missing eth1 from second file")
	}
	if _, ok := cfg.Network.Bridges["br0"]; !ok {
		t.Error("merged config missing br0 from second file")
	}
	if cfg.Network.Version != 2 {
		t.Errorf("version = %d, want 2", cfg.Network.Version)
	}
}

func TestLoadConfigSameDeviceLastWins(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "01.yaml", "network:\n  version: 2\n  ethernets:\n    eth0:\n      mtu: 1400\n")
	writeFile(t, dir, "02.yaml", "network:\n  ethernets:\n    eth0:\n      mtu: 9000\n")

	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	// 文件按名排序加载，后加载（02）覆盖
	if got := cfg.Network.Ethernets["eth0"].MTU; got != 9000 {
		t.Errorf("eth0 MTU = %d, want 9000 (last file wins)", got)
	}
}

func TestLoadConfigNoFiles(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadConfig(dir); err == nil {
		t.Error("LoadConfig on empty dir should return error")
	}
}

func TestLoadConfigFileLegacyFormat(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "legacy.yaml", `
netns:
  ns1:
    ethernets:
      eth1:
        addresses: [10.1.0.1/24]
`)
	cfg, err := LoadConfigFile(path)
	if err != nil {
		t.Fatalf("LoadConfigFile failed: %v", err)
	}
	// Normalize 应把 legacy 顶层 netns 迁移到 Network.Netns
	if _, ok := cfg.Network.Netns["ns1"]; !ok {
		t.Errorf("legacy netns not normalized into Network.Netns: %+v", cfg)
	}
	if cfg.Network.Version != 2 {
		t.Errorf("version = %d, want 2", cfg.Network.Version)
	}
}

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write %s: %v", name, err)
	}
	return path
}

func TestVPPSchemaParse(t *testing.T) {
	yml := `
network:
  version: 2
  renderer: vpp
  vpp:
    api-socket: /run/vpp/api.sock
    startup:
      main-core: 1
      workers: 2
      hugepages: 1024
      dpdk:
        uio-driver: vfio-pci
        dev: ["0000:02:00.0"]
  ethernets:
    eth0:
      addresses: [10.0.0.1/24]
      vpp:
        mode: af-packet
        host-if: eth0
    vf0:
      vpp: { mode: dpdk, pci: "0000:03:02.0" }
  bridges:
    br0:
      interfaces: [eth0]
      vpp: { bd-id: 10 }
`
	var c Config
	if err := yaml.Unmarshal([]byte(yml), &c); err != nil {
		t.Fatalf("unmarshal vpp config: %v", err)
	}
	n := c.Network
	if n.VPP == nil || n.VPP.APISocket != "/run/vpp/api.sock" {
		t.Fatalf("global vpp section not parsed: %+v", n.VPP)
	}
	if n.VPP.Startup == nil || n.VPP.Startup.Workers == nil || *n.VPP.Startup.Workers != 2 {
		t.Fatalf("vpp.startup not parsed: %+v", n.VPP.Startup)
	}
	if n.VPP.Startup.Dpdk == nil || len(n.VPP.Startup.Dpdk.Dev) != 1 {
		t.Fatalf("vpp.startup.dpdk not parsed")
	}
	eth0 := n.Ethernets["eth0"]
	if eth0 == nil || eth0.VPP == nil || eth0.VPP.Mode != "af-packet" || eth0.VPP.HostIf != "eth0" {
		t.Fatalf("eth0 vpp block not parsed: %+v", eth0)
	}
	vf0 := n.Ethernets["vf0"]
	if vf0 == nil || vf0.VPP == nil || vf0.VPP.Mode != "dpdk" || vf0.VPP.PCI != "0000:03:02.0" {
		t.Fatalf("vf0 vpp block not parsed: %+v", vf0)
	}
	br0 := n.Bridges["br0"]
	if br0 == nil || br0.VPP == nil || br0.VPP.BdID != 10 {
		t.Fatalf("br0 vpp block not parsed: %+v", br0)
	}
}

func TestVPPManaged(t *testing.T) {
	cases := []struct {
		vpp      *VPPDevice
		devR, gR string
		want     bool
	}{
		{&VPPDevice{Mode: "af-packet"}, "", "", true}, // 有 vpp 块
		{nil, "vpp", "", true},                        // 设备级 renderer
		{nil, "", "vpp", true},                        // 全局 renderer 继承
		{nil, "networkd", "vpp", false},               // 设备级覆盖回内核
		{nil, "", "", false},                          // 默认内核
		{nil, "", "networkd", false},
	}
	for i, c := range cases {
		if got := VPPManaged(c.vpp, c.devR, c.gR); got != c.want {
			t.Errorf("case %d: VPPManaged=%v want %v", i, got, c.want)
		}
	}
}

func TestValidateVPP(t *testing.T) {
	// dpdk 缺 pci → 报错
	bad := &Config{Network: Network{Ethernets: map[string]*Ethernet{
		"vf0": {VPP: &VPPDevice{Mode: "dpdk"}},
	}}}
	if err := ValidateVPP(bad); err == nil {
		t.Fatal("expected error for dpdk without pci")
	}
	// 未知 mode → 报错
	badMode := &Config{Network: Network{Ethernets: map[string]*Ethernet{
		"x": {VPP: &VPPDevice{Mode: "bogus"}},
	}}}
	if err := ValidateVPP(badMode); err == nil {
		t.Fatal("expected error for unknown mode")
	}
	// pci 重复占用 → 报错
	dupPCI := &Config{Network: Network{Ethernets: map[string]*Ethernet{
		"a": {VPP: &VPPDevice{Mode: "dpdk", PCI: "0000:03:02.0"}},
		"b": {VPP: &VPPDevice{Mode: "dpdk", PCI: "0000:03:02.0"}},
	}}}
	if err := ValidateVPP(dupPCI); err == nil {
		t.Fatal("expected error for duplicate pci")
	}
	// 合法配置 → 通过
	ok := &Config{Network: Network{Renderer: "vpp", Ethernets: map[string]*Ethernet{
		"eth0": {VPP: &VPPDevice{Mode: "af-packet", HostIf: "eth0"}},
		"vf0":  {VPP: &VPPDevice{Mode: "dpdk", PCI: "0000:03:02.0"}},
	}}}
	if err := ValidateVPP(ok); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}

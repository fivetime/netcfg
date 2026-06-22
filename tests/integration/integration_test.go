//go:build integration

/*
Copyright © 2024 netcfg authors

netcfg 集成测试：在真实内核（建议 privileged 容器）中应用配置，再用 netlink 断言
实际结果——不只看 apply 是否报错，而是验证设备/地址/路由/enslave/netns 真正生效。

运行（见 tests/integration/run.sh）：
  GOOS=linux go test -c -tags integration -o integration.test ./tests/integration
  # 在 privileged 容器内：
  NETCFG_BIN=/netcfg ./integration.test -test.v

需要：root + Linux + netcfg 二进制（NETCFG_BIN，默认 /netcfg）。
*/

package integration

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netlink/nl"
	"github.com/vishvananda/netns"
)

func netcfgBin() string {
	if b := os.Getenv("NETCFG_BIN"); b != "" {
		return b
	}
	return "/netcfg"
}

// run 执行 netcfg 子命令，失败时输出完整日志。
func run(t *testing.T, args ...string) {
	t.Helper()
	out, err := exec.Command(netcfgBin(), args...).CombinedOutput()
	if err != nil {
		t.Fatalf("netcfg %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// apply 清空 /etc/netplan，写入给定配置后执行 apply。
func apply(t *testing.T, yaml string) {
	t.Helper()
	if err := os.MkdirAll("/etc/netplan", 0755); err != nil {
		t.Fatal(err)
	}
	old, _ := filepath.Glob("/etc/netplan/*.yaml")
	for _, f := range old {
		_ = os.Remove(f)
	}
	if err := os.WriteFile("/etc/netplan/integration.yaml", []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}
	run(t, "apply")
}

// --- 断言 helper（default ns） ---

func mustLink(t *testing.T, name string) netlink.Link {
	t.Helper()
	link, err := netlink.LinkByName(name)
	if err != nil {
		t.Fatalf("expected link %q to exist: %v", name, err)
	}
	return link
}

func assertHasAddr(t *testing.T, h *netlink.Handle, link netlink.Link, cidr string) {
	t.Helper()
	var addrs []netlink.Addr
	var err error
	if h != nil {
		addrs, err = h.AddrList(link, netlink.FAMILY_ALL)
	} else {
		addrs, err = netlink.AddrList(link, netlink.FAMILY_ALL)
	}
	if err != nil {
		t.Fatalf("AddrList(%s): %v", link.Attrs().Name, err)
	}
	for _, a := range addrs {
		if a.IPNet.String() == cidr {
			return
		}
	}
	t.Fatalf("link %s missing address %s (have %v)", link.Attrs().Name, cidr, addrs)
}

func assertUp(t *testing.T, link netlink.Link, wantUp bool) {
	t.Helper()
	isUp := link.Attrs().Flags&net.FlagUp != 0
	if isUp != wantUp {
		t.Fatalf("link %s up=%v, want %v", link.Attrs().Name, isUp, wantUp)
	}
}

// --- 测试用例 ---

func TestDummyWithAddress(t *testing.T) {
	apply(t, `
network:
  version: 2
  dummy-devices:
    itd0:
      addresses: [10.90.0.1/24]
`)
	link := mustLink(t, "itd0")
	assertHasAddr(t, nil, link, "10.90.0.1/24")
}

func TestVlan(t *testing.T) {
	apply(t, `
network:
  version: 2
  dummy-devices:
    itv0: {}
  vlans:
    itv0.42:
      id: 42
      link: itv0
      addresses: [10.91.42.1/24]
`)
	link := mustLink(t, "itv0.42")
	vlan, ok := link.(*netlink.Vlan)
	if !ok {
		t.Fatalf("itv0.42 is %T, want *netlink.Vlan", link)
	}
	if vlan.VlanId != 42 {
		t.Fatalf("vlan id = %d, want 42", vlan.VlanId)
	}
	assertHasAddr(t, nil, link, "10.91.42.1/24")
}

func TestBridgeEnslave(t *testing.T) {
	apply(t, `
network:
  version: 2
  dummy-devices:
    itbm0: {}
  bridges:
    itbr0:
      interfaces: [itbm0]
      addresses: [10.92.0.1/24]
`)
	br := mustLink(t, "itbr0")
	member := mustLink(t, "itbm0")
	if member.Attrs().MasterIndex != br.Attrs().Index {
		t.Fatalf("itbm0 master=%d, want itbr0 index %d", member.Attrs().MasterIndex, br.Attrs().Index)
	}
	assertHasAddr(t, nil, br, "10.92.0.1/24")
}

func TestBondEnslave(t *testing.T) {
	apply(t, `
network:
  version: 2
  dummy-devices:
    itsl0: {}
    itsl1: {}
  bonds:
    itbond0:
      interfaces: [itsl0, itsl1]
      parameters:
        mode: balance-rr
`)
	bond := mustLink(t, "itbond0")
	for _, s := range []string{"itsl0", "itsl1"} {
		m := mustLink(t, s)
		if m.Attrs().MasterIndex != bond.Attrs().Index {
			t.Fatalf("%s master=%d, want itbond0 index %d", s, m.Attrs().MasterIndex, bond.Attrs().Index)
		}
	}
}

func TestVxlanViaTunnels(t *testing.T) {
	apply(t, `
network:
  version: 2
  tunnels:
    itvx0:
      mode: vxlan
      id: 4242
      local: 10.93.0.1
      port: 4789
`)
	link := mustLink(t, "itvx0")
	vx, ok := link.(*netlink.Vxlan)
	if !ok {
		t.Fatalf("itvx0 is %T, want *netlink.Vxlan", link)
	}
	if vx.VxlanId != 4242 {
		t.Fatalf("vni = %d, want 4242", vx.VxlanId)
	}
}

func TestStaticRoute(t *testing.T) {
	apply(t, `
network:
  version: 2
  dummy-devices:
    itr0:
      addresses: [10.94.0.1/24]
      routes:
        - to: 10.94.99.0/24
          via: 10.94.0.254
`)
	link := mustLink(t, "itr0")
	routes, err := netlink.RouteList(link, netlink.FAMILY_V4)
	if err != nil {
		t.Fatalf("RouteList: %v", err)
	}
	found := false
	for _, r := range routes {
		if r.Dst != nil && r.Dst.String() == "10.94.99.0/24" {
			found = true
		}
	}
	if !found {
		t.Fatalf("route 10.94.99.0/24 not found on itr0 (have %v)", routes)
	}
}

func TestActivationModeOff(t *testing.T) {
	// activation-mode 作用于 ethernet 路径（physical 设备）。预创建并 up 一个设备，
	// 以 ethernet + activation-mode: off 应用，应被强制 down，但地址仍下发。
	la := netlink.NewLinkAttrs()
	la.Name = "itam0"
	_ = netlink.LinkDel(&netlink.Dummy{LinkAttrs: la}) // 清理可能的残留
	if err := netlink.LinkAdd(&netlink.Dummy{LinkAttrs: la}); err != nil {
		t.Fatalf("pre-create itam0: %v", err)
	}
	pre := mustLink(t, "itam0")
	if err := netlink.LinkSetUp(pre); err != nil {
		t.Fatalf("pre-up itam0: %v", err)
	}

	apply(t, `
network:
  version: 2
  ethernets:
    itam0:
      activation-mode: "off"
      addresses: [10.95.0.1/24]
`)
	link := mustLink(t, "itam0")
	assertUp(t, link, false)                    // off -> 强制 down
	assertHasAddr(t, nil, link, "10.95.0.1/24") // 地址仍下发（配置但不激活）
}

func TestNetnsAndCrossVeth(t *testing.T) {
	apply(t, `
network:
  version: 2
  netns:
    itns1:
      dummy-devices:
        itnsd1:
          addresses: [10.96.1.1/24]
      veth-devices:
        itveth1:
          addresses: [172.31.0.1/24]
          peer:
            name: itveth2
            netns: itns2
            addresses: [172.31.0.2/24]
    itns2:
      dummy-devices:
        itnsd2:
          addresses: [10.96.2.1/24]
`)
	// netns 存在
	for _, ns := range []string{"itns1", "itns2"} {
		h, err := netns.GetFromName(ns)
		if err != nil {
			t.Fatalf("netns %s not created: %v", ns, err)
		}
		h.Close()
	}
	// itns1 内有 itnsd1 + itveth1，地址正确
	h1, _ := netns.GetFromName("itns1")
	defer h1.Close()
	nlh1, err := netlink.NewHandleAt(h1)
	if err != nil {
		t.Fatalf("handle itns1: %v", err)
	}
	defer nlh1.Delete()
	d1, err := nlh1.LinkByName("itnsd1")
	if err != nil {
		t.Fatalf("itnsd1 not in itns1: %v", err)
	}
	assertHasAddr(t, nlh1, d1, "10.96.1.1/24")
	v1, err := nlh1.LinkByName("itveth1")
	if err != nil {
		t.Fatalf("itveth1 not in itns1: %v", err)
	}
	assertHasAddr(t, nlh1, v1, "172.31.0.1/24")
	// 跨 ns peer 在 itns2
	h2, _ := netns.GetFromName("itns2")
	defer h2.Close()
	nlh2, err := netlink.NewHandleAt(h2)
	if err != nil {
		t.Fatalf("handle itns2: %v", err)
	}
	defer nlh2.Delete()
	v2, err := nlh2.LinkByName("itveth2")
	if err != nil {
		t.Fatalf("itveth2 not in itns2: %v", err)
	}
	assertHasAddr(t, nlh2, v2, "172.31.0.2/24")
}

func TestIdempotency(t *testing.T) {
	cfg := `
network:
  version: 2
  dummy-devices:
    itidem0:
      addresses: [10.97.0.1/24]
`
	apply(t, cfg)
	// 再次 apply 相同配置应成功且地址仍在（无重复/无报错）
	run(t, "apply")
	link := mustLink(t, "itidem0")
	assertHasAddr(t, nil, link, "10.97.0.1/24")
}

// --- SRv6 (seg6) ---

// requireSeg6 探测内核是否真正支持 seg6 lwtunnel（WSL2/部分内核缺
// CONFIG_IPV6_SEG6_LWTUNNEL：sysctl 在但路由 encap 被静默丢弃）。不支持则跳过。
func requireSeg6(t *testing.T) {
	t.Helper()
	const probe = "itsg6probe"
	la := netlink.NewLinkAttrs()
	la.Name = probe
	d := &netlink.Dummy{LinkAttrs: la}
	_ = netlink.LinkDel(d)
	if err := netlink.LinkAdd(d); err != nil {
		t.Skipf("cannot create probe dummy: %v", err)
	}
	defer netlink.LinkDel(d)
	link, _ := netlink.LinkByName(probe)
	_ = netlink.LinkSetUp(link)
	_, dst, _ := net.ParseCIDR("2001:db8:dead::/64")
	r := &netlink.Route{
		LinkIndex: link.Attrs().Index,
		Dst:       dst,
		Encap:     &netlink.SEG6Encap{Mode: nl.SEG6_IPTUN_MODE_ENCAP, Segments: []net.IP{net.ParseIP("2001:db8::1")}},
	}
	if err := netlink.RouteReplace(r); err != nil {
		t.Skipf("kernel lacks seg6 lwtunnel: %v", err)
	}
	routes, _ := netlink.RouteList(link, netlink.FAMILY_V6)
	for _, rt := range routes {
		if rt.Dst != nil && rt.Dst.String() == dst.String() {
			if _, ok := rt.Encap.(*netlink.SEG6Encap); ok {
				return // 真正支持
			}
		}
	}
	t.Skip("kernel accepts but drops seg6 encap (no CONFIG_IPV6_SEG6_LWTUNNEL)")
}

// findV6Route 按目标前缀查一条 IPv6 路由。
func findV6Route(t *testing.T, cidr string) *netlink.Route {
	t.Helper()
	_, want, err := net.ParseCIDR(cidr)
	if err != nil {
		t.Fatalf("bad cidr %s: %v", cidr, err)
	}
	routes, err := netlink.RouteList(nil, netlink.FAMILY_V6)
	if err != nil {
		t.Fatalf("RouteList: %v", err)
	}
	for i := range routes {
		if routes[i].Dst != nil && routes[i].Dst.String() == want.String() {
			return &routes[i]
		}
	}
	return nil
}

func TestSRv6Transit(t *testing.T) {
	requireSeg6(t)
	apply(t, `
network:
  version: 2
  dummy-devices:
    itsr0:
      addresses: [2001:db8:1::1/64]
      routes:
        - to: 2001:db8:ff::/48
          encap: { type: seg6, mode: encap, segments: [2001:db8:a::1, 2001:db8:b::1] }
`)
	rt := findV6Route(t, "2001:db8:ff::/48")
	if rt == nil {
		t.Fatal("transit route 2001:db8:ff::/48 not found")
	}
	enc, ok := rt.Encap.(*netlink.SEG6Encap)
	if !ok {
		t.Fatalf("route encap is %T, want *netlink.SEG6Encap", rt.Encap)
	}
	if enc.Mode != nl.SEG6_IPTUN_MODE_ENCAP {
		t.Fatalf("seg6 mode = %d, want encap(%d)", enc.Mode, nl.SEG6_IPTUN_MODE_ENCAP)
	}
	if len(enc.Segments) != 2 {
		t.Fatalf("seg6 segments = %d, want 2", len(enc.Segments))
	}
}

func TestSRv6LocalSIDs(t *testing.T) {
	requireSeg6(t)
	apply(t, `
network:
  version: 2
  vrfs:
    itsrvrf: { table: 110 }
  srv6:
    enabled: true
    local-sids:
      - { sid: 2001:db8:0:a::1, action: End }
      - { sid: 2001:db8:0:a::2, action: End.X, nh6: 2001:db8:1::2 }
      - { sid: 2001:db8:0:a::3, action: End.DT6, table: 110 }
      - { sid: 2001:db8:0:a::4, action: End.DT4, vrf-table: 110 }
`)
	cases := []struct {
		sid    string
		action int
	}{
		{"2001:db8:0:a::1/128", nl.SEG6_LOCAL_ACTION_END},
		{"2001:db8:0:a::2/128", nl.SEG6_LOCAL_ACTION_END_X},
		{"2001:db8:0:a::3/128", nl.SEG6_LOCAL_ACTION_END_DT6},
		{"2001:db8:0:a::4/128", nl.SEG6_LOCAL_ACTION_END_DT4},
	}
	for _, c := range cases {
		rt := findV6Route(t, c.sid)
		if rt == nil {
			t.Errorf("local SID %s not found", c.sid)
			continue
		}
		enc, ok := rt.Encap.(*netlink.SEG6LocalEncap)
		if !ok {
			t.Errorf("%s encap is %T, want *netlink.SEG6LocalEncap", c.sid, rt.Encap)
			continue
		}
		if enc.Action != c.action {
			t.Errorf("%s action = %d, want %d", c.sid, enc.Action, c.action)
		}
		if c.action == nl.SEG6_LOCAL_ACTION_END_DT4 && enc.VrfTable != 110 {
			t.Errorf("%s vrftable = %d, want 110", c.sid, enc.VrfTable)
		}
	}
}

func TestSRv6Reap(t *testing.T) {
	requireSeg6(t)
	apply(t, `
network:
  version: 2
  srv6:
    enabled: true
    local-sids:
      - { sid: 2001:db8:0:b::1, action: End }
      - { sid: 2001:db8:0:b::2, action: End }
`)
	if findV6Route(t, "2001:db8:0:b::2/128") == nil {
		t.Fatal("SID ::2 should exist after first apply")
	}
	// 第二次只保留 ::1，::2 应被回收
	apply(t, `
network:
  version: 2
  srv6:
    enabled: true
    local-sids:
      - { sid: 2001:db8:0:b::1, action: End }
`)
	if findV6Route(t, "2001:db8:0:b::1/128") == nil {
		t.Fatal("SID ::1 should still exist")
	}
	if rt := findV6Route(t, "2001:db8:0:b::2/128"); rt != nil {
		t.Fatalf("SID ::2 should be reaped, still present: %+v", rt)
	}
}

func TestDestroyNetns(t *testing.T) {
	apply(t, `
network:
  version: 2
  netns:
    itnsdestroy:
      dummy-devices:
        itdd0:
          addresses: [10.98.0.1/24]
`)
	if _, err := netns.GetFromName("itnsdestroy"); err != nil {
		t.Fatalf("netns itnsdestroy not created: %v", err)
	}
	run(t, "destroy")
	if h, err := netns.GetFromName("itnsdestroy"); err == nil {
		h.Close()
		t.Fatalf("netns itnsdestroy still exists after destroy")
	}
}

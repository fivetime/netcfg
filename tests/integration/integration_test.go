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
	assertUp(t, link, false)                       // off -> 强制 down
	assertHasAddr(t, nil, link, "10.95.0.1/24")    // 地址仍下发（配置但不激活）
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

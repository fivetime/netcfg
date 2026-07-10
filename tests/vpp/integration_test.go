//go:build vppintegration

/*
Copyright © 2024 netcfg authors

netcfg VPP 后端集成测试：在 netcfg-vpp 容器（privileged）中启动真实 VPP，用 netcfg
二进制 apply VPP 配置，再用 vppctl 断言 VPP 实际状态。锁住 V1a/V1b 成果。

运行见 tests/vpp/run.sh：
  docker build -t netcfg-vpp tests/vpp
  GOOS=linux go test -c -tags vppintegration -o vpp.test ./tests/vpp
  docker run --rm --privileged -v <netcfg>:/netcfg -v <vpp.test>:/vpp.test netcfg-vpp \
      bash -c 'NETCFG_BIN=/netcfg /vpp.test -test.v'

需要：root + VPP 镜像（vpp/vppctl 在 PATH）+ netcfg 二进制（NETCFG_BIN，默认 /netcfg）。
*/

package vppintegration

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/netcfg/netcfg/vpp"
)

func netcfgBin() string {
	if b := os.Getenv("NETCFG_BIN"); b != "" {
		return b
	}
	return "/netcfg"
}

// TestMain：配 hugepages、建 veth、起 VPP、等 api.sock 就绪，再跑用例。
func TestMain(m *testing.M) {
	_ = exec.Command("sysctl", "-w", "vm.nr_hugepages=512").Run()
	for _, d := range []string{"/run/vpp", "/var/log/vpp", "/etc/netplan"} {
		_ = os.MkdirAll(d, 0755)
	}
	for _, i := range []string{"1", "2", "3"} {
		_ = exec.Command("ip", "link", "add", "veth"+i, "type", "veth", "peer", "name", "veth"+i+"p").Run()
		_ = exec.Command("ip", "link", "set", "veth"+i, "up").Run()
	}
	vpp := exec.Command("vpp", "-c", "/etc/vpp/startup.conf")
	if err := vpp.Start(); err != nil {
		println("failed to start vpp:", err.Error())
		os.Exit(1)
	}
	ready := false
	for i := 0; i < 30; i++ {
		if _, err := os.Stat("/run/vpp/api.sock"); err == nil {
			ready = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !ready {
		println("vpp api.sock not ready")
		os.Exit(1)
	}
	time.Sleep(time.Second) // 让 VPP 完成初始化
	os.Exit(m.Run())
}

// apply 写配置并执行 netcfg apply（失败即 fatal）。
func apply(t *testing.T, yaml string) {
	t.Helper()
	if err := os.WriteFile("/etc/netplan/vpp.yaml", []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(netcfgBin(), "apply").CombinedOutput()
	if err != nil {
		t.Fatalf("netcfg apply failed: %v\n%s", err, out)
	}
}

// vppctl 执行 vppctl 命令返回输出。
func vppctl(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("vppctl", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("vppctl %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func mustContain(t *testing.T, haystack, needle, what string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Fatalf("%s: expected to find %q in:\n%s", what, needle, haystack)
	}
}

func TestAfPacketAddressRoute(t *testing.T) {
	apply(t, `
network:
  version: 2
  renderer: vpp
  ethernets:
    veth1:
      mtu: 9000
      addresses: [10.10.0.1/24]
      routes: [{ to: 192.168.50.0/24, via: 10.10.0.254 }]
      vpp: { mode: af-packet, host-if: veth1 }
`)
	mustContain(t, vppctl(t, "show", "interface", "addr"), "10.10.0.1/24", "af-packet address")
	mustContain(t, vppctl(t, "show", "ip", "fib", "192.168.50.0/24"), "192.168.50.0/24", "static route")
}

func TestLoopback(t *testing.T) {
	apply(t, `
network:
  version: 2
  renderer: vpp
  ethernets:
    lo-it: { addresses: [10.99.0.1/32], vpp: { mode: loopback } }
`)
	mustContain(t, vppctl(t, "show", "interface", "addr"), "10.99.0.1/32", "loopback address")
}

func TestBond(t *testing.T) {
	apply(t, `
network:
  version: 2
  renderer: vpp
  ethernets:
    veth2: { vpp: { mode: af-packet, host-if: veth2 } }
    veth3: { vpp: { mode: af-packet, host-if: veth3 } }
  bonds:
    bond-it:
      interfaces: [veth2, veth3]
      parameters: { mode: 802.3ad }
      addresses: [10.20.0.1/24]
`)
	bond := vppctl(t, "show", "bond")
	mustContain(t, bond, "lacp", "bond mode lacp")
	mustContain(t, vppctl(t, "show", "interface", "addr"), "10.20.0.1/24", "bond address")
}

func TestVlan(t *testing.T) {
	apply(t, `
network:
  version: 2
  renderer: vpp
  ethernets:
    veth1: { vpp: { mode: af-packet, host-if: veth1 } }
  vlans:
    vlan-it: { id: 123, link: veth1, addresses: [10.21.0.1/24] }
`)
	mustContain(t, vppctl(t, "show", "interface"), ".123", "vlan sub-interface")
	mustContain(t, vppctl(t, "show", "interface", "addr"), "10.21.0.1/24", "vlan address")
}

func TestVxlanBridgeBVI(t *testing.T) {
	apply(t, `
network:
  version: 2
  renderer: vpp
  tunnels:
    vx-it: { mode: vxlan, id: 77, local: 10.20.0.1, remote: 10.20.0.2 }
  bridges:
    br-it:
      interfaces: [vx-it]
      addresses: [10.22.0.1/24]
`)
	mustContain(t, vppctl(t, "show", "vxlan", "tunnel"), "vni 77", "vxlan tunnel")
	mustContain(t, vppctl(t, "show", "interface", "addr"), "10.22.0.1/24", "bridge BVI address")
}

func TestIdempotent(t *testing.T) {
	cfg := `
network:
  version: 2
  renderer: vpp
  ethernets:
    veth2: { addresses: [10.30.0.1/24], vpp: { mode: af-packet, host-if: veth2 } }
`
	apply(t, cfg)
	// 第二次 apply 应成功（幂等）且地址仍在
	apply(t, cfg)
	mustContain(t, vppctl(t, "show", "interface", "addr"), "10.30.0.1/24", "address after re-apply")
}

func mustNotContain(t *testing.T, haystack, needle, what string) {
	t.Helper()
	if strings.Contains(haystack, needle) {
		t.Fatalf("%s: did not expect %q in:\n%s", what, needle, haystack)
	}
}

func TestReapOrphan(t *testing.T) {
	// 先创建一个带唯一地址的 loopback
	apply(t, `
network:
  version: 2
  renderer: vpp
  ethernets:
    veth1: { addresses: [10.40.0.1/24], vpp: { mode: af-packet, host-if: veth1 } }
    lo-reap: { addresses: [10.88.0.1/32], vpp: { mode: loopback } }
`)
	mustContain(t, vppctl(t, "show", "interface", "addr"), "10.88.0.1/32", "loopback before reap")
	// 再 apply 去掉 lo-reap → 应从 VPP 移除
	apply(t, `
network:
  version: 2
  renderer: vpp
  ethernets:
    veth1: { addresses: [10.40.0.1/24], vpp: { mode: af-packet, host-if: veth1 } }
`)
	mustNotContain(t, vppctl(t, "show", "interface", "addr"), "10.88.0.1/32", "loopback after reap (orphan removed)")
}

func TestNat44(t *testing.T) {
	apply(t, `
network:
  version: 2
  renderer: vpp
  vpp:
    nat:
      nat44:
        enable: true
        interfaces: [{ name: lan, role: inside }, { name: wan, role: outside }]
        pools: [{ start: 203.0.113.10 }]
        static: [{ proto: tcp, local: 10.0.0.5, local-port: 80, external: 203.0.113.10, external-port: 8080 }]
  ethernets:
    lan: { addresses: [10.0.0.1/24], vpp: { mode: af-packet, host-if: veth1 } }
    wan: { addresses: [203.0.113.1/24], vpp: { mode: af-packet, host-if: veth2 } }
`)
	ifs := vppctl(t, "show", "nat44", "interfaces")
	mustContain(t, ifs, "in", "nat44 inside interface")
	mustContain(t, ifs, "out", "nat44 outside interface")
	mustContain(t, vppctl(t, "show", "nat44", "addresses"), "203.0.113.10", "nat44 pool address")
	mustContain(t, vppctl(t, "show", "nat44", "static", "mappings"), "203.0.113.10:8080", "nat44 port-forward external")
	// 再 apply 应幂等（无 error）
	apply(t, `
network:
  version: 2
  renderer: vpp
  vpp:
    nat:
      nat44:
        enable: true
        interfaces: [{ name: lan, role: inside }, { name: wan, role: outside }]
        pools: [{ start: 203.0.113.10 }]
        static: [{ proto: tcp, local: 10.0.0.5, local-port: 80, external: 203.0.113.10, external-port: 8080 }]
  ethernets:
    lan: { addresses: [10.0.0.1/24], vpp: { mode: af-packet, host-if: veth1 } }
    wan: { addresses: [203.0.113.1/24], vpp: { mode: af-packet, host-if: veth2 } }
`)
}

func TestNatReap(t *testing.T) {
	full := `
network:
  version: 2
  renderer: vpp
  vpp:
    nat:
      nat44:
        enable: true
        interfaces: [{ name: lan, role: inside }, { name: wan, role: outside }]
        pools: [{ start: 203.0.113.10 }, { start: 203.0.113.20 }]
        static:
          - { proto: tcp, local: 10.0.0.5, local-port: 80, external: 203.0.113.10, external-port: 8080 }
          - { proto: tcp, local: 10.0.0.6, local-port: 443, external: 203.0.113.10, external-port: 443 }
  ethernets:
    lan: { addresses: [10.0.0.1/24], vpp: { mode: af-packet, host-if: veth1 } }
    wan: { addresses: [203.0.113.1/24], vpp: { mode: af-packet, host-if: veth2 } }
`
	apply(t, full)
	mustContain(t, vppctl(t, "show", "nat44", "addresses"), "203.0.113.20", "pool before reap")
	mustContain(t, vppctl(t, "show", "nat44", "static", "mappings"), "10.0.0.6", "static before reap")

	// 去掉一个 pool 与一个 static → 应被回收
	apply(t, `
network:
  version: 2
  renderer: vpp
  vpp:
    nat:
      nat44:
        enable: true
        interfaces: [{ name: lan, role: inside }, { name: wan, role: outside }]
        pools: [{ start: 203.0.113.10 }]
        static:
          - { proto: tcp, local: 10.0.0.5, local-port: 80, external: 203.0.113.10, external-port: 8080 }
  ethernets:
    lan: { addresses: [10.0.0.1/24], vpp: { mode: af-packet, host-if: veth1 } }
    wan: { addresses: [203.0.113.1/24], vpp: { mode: af-packet, host-if: veth2 } }
`)
	mustNotContain(t, vppctl(t, "show", "nat44", "addresses"), "203.0.113.20", "pool reaped")
	mustNotContain(t, vppctl(t, "show", "nat44", "static", "mappings"), "10.0.0.6", "static reaped")
	mustContain(t, vppctl(t, "show", "nat44", "static", "mappings"), "10.0.0.5", "kept static remains")
}

func TestNDProxy(t *testing.T) {
	// 统一 ndp-proxy 块的 addresses 在 VPP 设备上 → ip6nd_proxy（逐 /128）。
	// 代理条目会以 /128 进 ip6 FIB，用它断言。
	full := `
network:
  version: 2
  renderer: vpp
  ethernets:
    veth1:
      addresses: ["2001:db8:77::1/64"]
      vpp: { mode: af-packet, host-if: veth1 }
      ndp-proxy:
        addresses: ["2001:db8:77::99", "2001:db8:77::100"]
`
	apply(t, full)
	fib := vppctl(t, "show", "ip6", "fib")
	mustContain(t, fib, "2001:db8:77::99", "nd-proxy entry ::99")
	mustContain(t, fib, "2001:db8:77::100", "nd-proxy entry ::100")

	// 幂等：重复 apply 无 error，条目仍在
	apply(t, full)
	mustContain(t, vppctl(t, "show", "ip6", "fib"), "2001:db8:77::99", "nd-proxy after re-apply")

	// 去掉 ::100 → 应被回收，::99 保留
	apply(t, `
network:
  version: 2
  renderer: vpp
  ethernets:
    veth1:
      addresses: ["2001:db8:77::1/64"]
      vpp: { mode: af-packet, host-if: veth1 }
      ndp-proxy:
        addresses: ["2001:db8:77::99"]
`)
	fib = vppctl(t, "show", "ip6", "fib")
	mustContain(t, fib, "2001:db8:77::99", "nd-proxy ::99 remains")
	mustNotContain(t, fib, "2001:db8:77::100", "nd-proxy ::100 reaped")
}

// TestNDPProxyTap 验证 VPP bridge 上的 external-MAC 静态 rules → 往该 BD 生一根托管
// 内核 tap（VPP 数据面做不了前缀+外部 MAC 代答）：tap 入 BD、内核可见且带 ifalias，
// 去掉 ndp-proxy 后 tap 被回收。响应器本体（在 tap 上代答）另由 ndpproxy 包的 E2E 覆盖。
func TestNDPProxyTap(t *testing.T) {
	const bridge = "ndpbr"
	apply(t, `
network:
  version: 2
  renderer: vpp
  ethernets:
    veth1: { vpp: { mode: af-packet, host-if: veth1 } }
  bridges:
    ndpbr:
      interfaces: [veth1]
      ndp-proxy:
        router: true
        rules:
          - prefix: "2400:2410:ef28:2a00:1::/80"
            neighbor: "84:47:09:0b:7d:4a"
`)
	tap := vpp.NDPTapName(bridge)

	// 内核侧：tap netdev 出现，且带「managed, do not delete」ifalias（强断言，名字确定性）。
	out, err := exec.Command("ip", "-d", "link", "show", tap).CombinedOutput()
	if err != nil {
		t.Fatalf("kernel ndp tap %s missing: %v\n%s", tap, err, out)
	}
	mustContain(t, string(out), "netcfg NDP proxy", "ndp tap ifalias")

	// VPP 侧：tap 已加入该 bridge 的 bridge-domain。
	bd := fmt.Sprint(vpp.AutoBdID(bridge))
	mustContain(t, vppctl(t, "show", "bridge-domain", bd, "detail"), "tap", "ndp tap joined BD")

	// 回收：去掉 ndp-proxy → tap 从 VPP + 内核消失。
	apply(t, `
network:
  version: 2
  renderer: vpp
  ethernets:
    veth1: { vpp: { mode: af-packet, host-if: veth1 } }
  bridges:
    ndpbr:
      interfaces: [veth1]
`)
	if err := exec.Command("ip", "link", "show", tap).Run(); err == nil {
		t.Errorf("kernel ndp tap %s should be gone after reap", tap)
	}
}

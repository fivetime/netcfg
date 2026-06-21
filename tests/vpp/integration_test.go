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
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
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

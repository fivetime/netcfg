//go:build integration

/*
NDP 代答器端到端集成测试（需 root，建议 privileged 容器）：建 veth 对，一端跑响应器，
另一端发 Neighbor Solicitation，断言收到的 NA 的 TLLA = 配置的外部 MAC。
不依赖 seg6，普通内核即可（WSL2 也行）。

  GOOS=linux go test -c -tags integration -o ndpproxy.itest ./ndpproxy
  docker run --rm --privileged -v $PWD/ndpproxy.itest:/t alpine /t -test.v
*/

package ndpproxy

import (
	"context"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/mdlayher/ndp"
	"github.com/mdlayher/packet"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// solicitedNodeMcast 返回目标地址的 solicited-node 组播地址 ff02::1:ffXX:XXXX。
func solicitedNodeMcast(t netip.Addr) netip.Addr {
	a := t.As16()
	snm := [16]byte{0xff, 0x02, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 0xff, a[13], a[14], a[15]}
	return netip.AddrFrom16(snm)
}

// mcastMAC 返回 IPv6 组播地址对应的以太网组播 MAC：33:33:XX:XX:XX:XX（末 4 字节）。
func mcastMAC(a netip.Addr) net.HardwareAddr {
	b := a.As16()
	return net.HardwareAddr{0x33, 0x33, b[12], b[13], b[14], b[15]}
}

func TestResponderEndToEnd(t *testing.T) {
	const a, b = "ndpv0", "ndpv1"
	la := netlink.NewLinkAttrs()
	la.Name = a
	veth := &netlink.Veth{LinkAttrs: la, PeerName: b}
	_ = netlink.LinkDel(veth)
	if err := netlink.LinkAdd(veth); err != nil {
		t.Fatalf("create veth: %v", err)
	}
	defer netlink.LinkDel(veth)
	v0, _ := netlink.LinkByName(a)
	v1, _ := netlink.LinkByName(b)
	for _, l := range []netlink.Link{v0, v1} {
		if err := netlink.LinkSetUp(l); err != nil {
			t.Fatalf("set up: %v", err)
		}
	}
	if err := netlink.LinkSetAllmulticastOn(v0); err != nil {
		t.Fatalf("allmulti: %v", err)
	}

	foreign, _ := net.ParseMAC("84:47:09:0b:7d:4a")
	prefix := netip.MustParsePrefix("2400:2410:ef28:2a00:1::/80")
	resp, err := New(Config{Iface: a, Router: true, Rules: []Rule{{Prefix: prefix, Neighbor: foreign}}})
	if err != nil {
		t.Fatalf("responder: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = resp.Run(ctx) }()
	time.Sleep(300 * time.Millisecond) // 让响应器就绪

	// 发送端：ndpv1
	ifi1, _ := net.InterfaceByName(b)
	conn, err := packet.Listen(ifi1, packet.Datagram, unix.ETH_P_IPV6, nil)
	if err != nil {
		t.Fatalf("sender listen: %v", err)
	}
	defer conn.Close()

	target := netip.MustParseAddr("2400:2410:ef28:2a00:1::1234")
	nsSrc := netip.MustParseAddr("fe80::1")
	snm := solicitedNodeMcast(target)
	ns := &ndp.NeighborSolicitation{
		TargetAddress: target,
		Options:       []ndp.Option{&ndp.LinkLayerAddress{Direction: ndp.Source, Addr: ifi1.HardwareAddr}},
	}
	icmp, err := ndp.MarshalMessageChecksum(ns, nsSrc, snm)
	if err != nil {
		t.Fatalf("marshal NS: %v", err)
	}
	pkt := buildIPv6(nsSrc, snm, icmp)
	if _, err := conn.WriteTo(pkt, &packet.Addr{HardwareAddr: mcastMAC(snm)}); err != nil {
		t.Fatalf("send NS: %v", err)
	}

	// 读 NA，断言 TLLA = foreign
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 1500)
	for {
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			t.Fatalf("no NA received: %v", err)
		}
		p := buf[:n]
		if len(p) < 40 || p[6] != 58 {
			continue
		}
		msg, err := ndp.ParseMessage(p[40:])
		if err != nil {
			continue
		}
		na, ok := msg.(*ndp.NeighborAdvertisement)
		if !ok || na.TargetAddress != target {
			continue
		}
		var got net.HardwareAddr
		for _, o := range na.Options {
			if lla, ok := o.(*ndp.LinkLayerAddress); ok && lla.Direction == ndp.Target {
				got = lla.Addr
			}
		}
		if got == nil {
			t.Fatal("NA has no target link-layer address")
		}
		if got.String() != foreign.String() {
			t.Fatalf("NA TLLA = %s, want foreign %s", got, foreign)
		}
		if !na.Router || !na.Solicited {
			t.Errorf("NA flags R/S = %v/%v, want true/true", na.Router, na.Solicited)
		}
		return // 成功
	}
}

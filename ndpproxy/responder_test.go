package ndpproxy

import (
	"net"
	"net/netip"
	"testing"

	"github.com/mdlayher/ndp"
)

func mustMAC(s string) net.HardwareAddr {
	m, err := net.ParseMAC(s)
	if err != nil {
		panic(err)
	}
	return m
}

// parseNA 从 buildReply 产出的 IPv6 包里取出 NA。
func parseNA(t *testing.T, pkt []byte) *ndp.NeighborAdvertisement {
	t.Helper()
	if len(pkt) < 40 || pkt[6] != 58 || pkt[7] != 255 {
		t.Fatalf("not an ICMPv6/255 packet: len=%d nh=%d hop=%d", len(pkt), pkt[6], pkt[7])
	}
	msg, err := ndp.ParseMessage(pkt[40:])
	if err != nil {
		t.Fatalf("ParseMessage: %v", err)
	}
	na, ok := msg.(*ndp.NeighborAdvertisement)
	if !ok {
		t.Fatalf("got %T, want *ndp.NeighborAdvertisement", msg)
	}
	return na
}

func tlla(t *testing.T, na *ndp.NeighborAdvertisement) net.HardwareAddr {
	t.Helper()
	for _, o := range na.Options {
		if lla, ok := o.(*ndp.LinkLayerAddress); ok && lla.Direction == ndp.Target {
			return lla.Addr
		}
	}
	t.Fatal("no target link-layer address option")
	return nil
}

// 命中前缀 + 指定外部 MAC：NA 的 TLLA = 外部 MAC，flags R/S/O，单播回 NS 源 MAC。
func TestBuildReplyForeignMAC(t *testing.T) {
	foreign := mustMAC("84:47:09:0b:7d:4a")
	r := &Responder{
		cfg:    Config{Iface: "x", Router: true, Rules: []Rule{{Prefix: netip.MustParsePrefix("2400:2410:ef28:2a00:1::/80"), Neighbor: foreign}}},
		hwaddr: mustMAC("02:00:00:00:00:01"),
		ifi:    &net.Interface{Index: 1},
	}
	target := netip.MustParseAddr("2400:2410:ef28:2a00:1::1234")
	nsSrc := netip.MustParseAddr("fe80::1")
	nsSrcMAC := mustMAC("aa:bb:cc:dd:ee:ff")

	out, dstMAC, ok := r.buildReply(target, nsSrc, nsSrcMAC)
	if !ok {
		t.Fatal("expected a reply")
	}
	if dstMAC.String() != nsSrcMAC.String() {
		t.Errorf("solicited reply dstMAC=%s, want NS src %s", dstMAC, nsSrcMAC)
	}
	na := parseNA(t, out)
	if !na.Router || !na.Solicited || !na.Override {
		t.Errorf("flags R/S/O = %v/%v/%v, want all true", na.Router, na.Solicited, na.Override)
	}
	if na.TargetAddress != target {
		t.Errorf("NA target=%s, want %s", na.TargetAddress, target)
	}
	if got := tlla(t, na); got.String() != foreign.String() {
		t.Errorf("TLLA=%s, want foreign %s", got, foreign)
	}
}

// 未指定 neighbor：TLLA 退化为本接口 MAC（hairpin）。
func TestBuildReplyOwnMAC(t *testing.T) {
	own := mustMAC("02:00:00:00:00:09")
	r := &Responder{
		cfg:    Config{Iface: "x", Router: false, Rules: []Rule{{Prefix: netip.MustParsePrefix("2001:db8::/64")}}},
		hwaddr: own,
		ifi:    &net.Interface{Index: 1},
	}
	out, _, ok := r.buildReply(netip.MustParseAddr("2001:db8::5"), netip.MustParseAddr("2001:db8::1"), mustMAC("aa:bb:cc:dd:ee:ff"))
	if !ok {
		t.Fatal("expected a reply")
	}
	na := parseNA(t, out)
	if na.Router {
		t.Error("router flag should be false")
	}
	if got := tlla(t, na); got.String() != own.String() {
		t.Errorf("TLLA=%s, want own iface MAC %s", got, own)
	}
}

// 目标不在任何前缀内：不应答。
func TestBuildReplyNoMatch(t *testing.T) {
	r := &Responder{
		cfg:    Config{Iface: "x", Rules: []Rule{{Prefix: netip.MustParsePrefix("2001:db8::/64")}}},
		hwaddr: mustMAC("02:00:00:00:00:01"),
		ifi:    &net.Interface{Index: 1},
	}
	if _, _, ok := r.buildReply(netip.MustParseAddr("2001:dead::1"), netip.MustParseAddr("2001:db8::1"), nil); ok {
		t.Error("expected no reply for out-of-prefix target")
	}
}

// DAD（NS 源 = ::）：发 all-nodes 组播、不置 Solicited。
func TestBuildReplyDAD(t *testing.T) {
	r := &Responder{
		cfg:    Config{Iface: "x", Rules: []Rule{{Prefix: netip.MustParsePrefix("2001:db8::/64")}}},
		hwaddr: mustMAC("02:00:00:00:00:01"),
		ifi:    &net.Interface{Index: 1},
	}
	out, dstMAC, ok := r.buildReply(netip.MustParseAddr("2001:db8::5"), netip.IPv6Unspecified(), nil)
	if !ok {
		t.Fatal("expected a reply")
	}
	if dstMAC.String() != allNodesMAC.String() {
		t.Errorf("DAD reply dstMAC=%s, want all-nodes %s", dstMAC, allNodesMAC)
	}
	if na := parseNA(t, out); na.Solicited {
		t.Error("DAD reply should not set Solicited")
	}
}

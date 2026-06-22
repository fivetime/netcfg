/*
Copyright © 2024 netcfg authors

内置 NDP 代答器（ndppd 等价，纯 Go）：在接口上监听 Neighbor Solicitation，对落在配置
前缀内的目标地址回 Neighbor Advertisement，TLLA 可填指定（外部）MAC。见
docs/ndp-responder-design.md。用 AF_PACKET(cooked) 收发 + mdlayher/ndp 编解码 + allmulti
收全 solicited-node 组播；跑在 netcfg daemon。
*/

package ndpproxy

import (
	"context"
	"encoding/binary"
	"fmt"
	"log/slog"
	"net"
	"net/netip"

	"github.com/mdlayher/ndp"
	"github.com/mdlayher/packet"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// Rule 一条代答规则。
type Rule struct {
	Prefix   netip.Prefix     // 命中此前缀内的目标地址即代答
	Neighbor net.HardwareAddr // 回 NA 的 TLLA；nil=用本接口 MAC（hairpin）
	Auto     bool             // auto 模式：仅当目标的内核路由出口不是本接口时才代答
}

// Config 单个接口的代答配置。
type Config struct {
	Iface  string
	Router bool // 回 NA 是否置 Router(R) 标志
	Rules  []Rule
}

// Responder 在一个接口上跑 NDP 代答。
type Responder struct {
	cfg    Config
	ifi    *net.Interface
	hwaddr net.HardwareAddr
	conn   *packet.Conn
}

var allNodesMAC = net.HardwareAddr{0x33, 0x33, 0x00, 0x00, 0x00, 0x01}

// New 在 cfg.Iface 上打开 AF_PACKET(cooked, ETH_P_IPV6) socket。
func New(cfg Config) (*Responder, error) {
	ifi, err := net.InterfaceByName(cfg.Iface)
	if err != nil {
		return nil, fmt.Errorf("ndp-proxy: interface %s: %w", cfg.Iface, err)
	}
	conn, err := packet.Listen(ifi, packet.Datagram, unix.ETH_P_IPV6, nil)
	if err != nil {
		return nil, fmt.Errorf("ndp-proxy: listen on %s: %w", cfg.Iface, err)
	}
	return &Responder{cfg: cfg, ifi: ifi, hwaddr: ifi.HardwareAddr, conn: conn}, nil
}

// Run 循环监听 NS 并代答，直到 ctx 取消。
func (r *Responder) Run(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		_ = r.conn.Close()
	}()
	slog.Info("ndp-proxy responder started", "interface", r.cfg.Iface, "rules", len(r.cfg.Rules))
	buf := make([]byte, 1500)
	for {
		n, from, err := r.conn.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil // 正常关闭
			}
			slog.Warn("ndp-proxy read error", "interface", r.cfg.Iface, "error", err)
			return err
		}
		r.handle(buf[:n], from)
	}
}

// handle 处理一个收到的 IPv6 包（cooked：无 L2 头）。
func (r *Responder) handle(pkt []byte, from net.Addr) {
	// 仅 ICMPv6（next-header=58），无扩展头；NDP 要求 hop limit=255。
	if len(pkt) < 40 || pkt[0]>>4 != 6 || pkt[6] != 58 || pkt[7] != 255 {
		return
	}
	srcIP, _ := netip.AddrFromSlice(pkt[8:24])
	msg, err := ndp.ParseMessage(pkt[40:])
	if err != nil {
		return
	}
	ns, ok := msg.(*ndp.NeighborSolicitation)
	if !ok {
		return
	}
	var srcMAC net.HardwareAddr
	if pa, ok := from.(*packet.Addr); ok {
		srcMAC = pa.HardwareAddr
	}
	out, dstMAC, ok := r.buildReply(ns.TargetAddress, srcIP, srcMAC)
	if !ok {
		return
	}
	if _, err := r.conn.WriteTo(out, &packet.Addr{HardwareAddr: dstMAC}); err != nil {
		slog.Warn("ndp-proxy send NA failed", "target", ns.TargetAddress, "error", err)
		return
	}
	slog.Debug("ndp-proxy answered", "interface", r.cfg.Iface, "target", ns.TargetAddress, "dst_mac", dstMAC)
}

// buildReply 按规则为 target 构造应答 IPv6 包 + 目的 MAC。无命中规则（或 auto 不该答）
// 返回 ok=false。纯逻辑，便于单测。srcIP/srcMAC 为收到的 NS 源（DAD 时 srcIP 为 ::）。
func (r *Responder) buildReply(target, srcIP netip.Addr, srcMAC net.HardwareAddr) ([]byte, net.HardwareAddr, bool) {
	rule, ok := r.match(target)
	if !ok {
		return nil, nil, false
	}
	if rule.Auto && !routeElsewhere(target, r.ifi.Index) {
		return nil, nil, false // auto：目标路由就在本接口（或无路由）→ 不代答，避免环路
	}

	tlla := r.hwaddr
	if rule.Neighbor != nil {
		tlla = rule.Neighbor
	}

	// 应答目的：单播回 NS 源；DAD（源=::）则发 all-nodes 组播、不置 Solicited。
	solicited := srcIP.IsValid() && !srcIP.IsUnspecified()
	dstIP := srcIP
	dstMAC := allNodesMAC
	if solicited {
		if len(srcMAC) == 6 {
			dstMAC = srcMAC
		}
	} else {
		dstIP = netip.MustParseAddr("ff02::1")
	}

	na := &ndp.NeighborAdvertisement{
		Router:        r.cfg.Router,
		Solicited:     solicited,
		Override:      true,
		TargetAddress: target,
		Options:       []ndp.Option{&ndp.LinkLayerAddress{Direction: ndp.Target, Addr: tlla}},
	}
	// NA 的 IPv6 源 = 被通告的目标地址（与校验和一致）。
	icmp, err := ndp.MarshalMessageChecksum(na, target, dstIP)
	if err != nil {
		slog.Warn("ndp-proxy marshal NA failed", "target", target, "error", err)
		return nil, nil, false
	}
	return buildIPv6(target, dstIP, icmp), dstMAC, true
}

// match 返回命中目标地址的第一条规则。
func (r *Responder) match(t netip.Addr) (Rule, bool) {
	for _, rl := range r.cfg.Rules {
		if rl.Prefix.Contains(t) {
			return rl, true
		}
	}
	return Rule{}, false
}

// routeElsewhere 判断目标地址的内核路由出口是否为「非 ourIndex」的接口。
func routeElsewhere(t netip.Addr, ourIndex int) bool {
	routes, err := netlink.RouteGet(net.IP(t.AsSlice()))
	if err != nil {
		return false
	}
	for _, rt := range routes {
		if rt.LinkIndex != 0 && rt.LinkIndex != ourIndex {
			return true
		}
	}
	return false
}

// buildIPv6 构造 IPv6 包（40 字节头 + ICMPv6 载荷），hop limit=255。
func buildIPv6(src, dst netip.Addr, payload []byte) []byte {
	b := make([]byte, 40+len(payload))
	b[0] = 0x60 // version 6
	binary.BigEndian.PutUint16(b[4:6], uint16(len(payload)))
	b[6] = 58  // next header = ICMPv6
	b[7] = 255 // hop limit（NDP 要求 255）
	copy(b[8:24], src.AsSlice())
	copy(b[24:40], dst.AsSlice())
	copy(b[40:], payload)
	return b
}

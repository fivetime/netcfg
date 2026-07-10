/*
Copyright © 2024 netcfg authors

VPP 后端连接客户端：通过 GoVPP 的 binary API socket 连接 VPP，并对所有用到的
绑定包做兼容性自检（CRC）。见 docs/vpp-backend-design.md。
*/

package vpp

import (
	"fmt"
	"log/slog"

	"go.fd.io/govpp"
	"go.fd.io/govpp/adapter/socketclient"
	"go.fd.io/govpp/api"
	"go.fd.io/govpp/core"

	af_packet "go.fd.io/govpp/binapi/af_packet"
	"go.fd.io/govpp/binapi/bond"
	interfaces "go.fd.io/govpp/binapi/interface"
	"go.fd.io/govpp/binapi/ip"
	"go.fd.io/govpp/binapi/ip6_nd"
	"go.fd.io/govpp/binapi/l2"
	"go.fd.io/govpp/binapi/tapv2"
	"go.fd.io/govpp/binapi/vxlan"
)

// DefaultAPISocket 是 VPP binary API 的默认 socket 路径。
const DefaultAPISocket = socketclient.DefaultSocketName // /run/vpp/api.sock

// Client 封装到 VPP 的连接与 API channel。
type Client struct {
	conn *core.Connection
	ch   api.Channel
}

// Connect 连接 VPP 并做绑定兼容性自检。sock 为空时用默认 socket。
func Connect(sock string) (*Client, error) {
	if sock == "" {
		sock = DefaultAPISocket
	}
	conn, err := govpp.Connect(sock)
	if err != nil {
		return nil, fmt.Errorf("connect VPP at %s: %w", sock, err)
	}
	ch, err := conn.NewAPIChannel()
	if err != nil {
		conn.Disconnect()
		return nil, fmt.Errorf("open VPP API channel: %w", err)
	}
	c := &Client{conn: conn, ch: ch}
	if err := c.checkCompatibility(); err != nil {
		c.Close()
		return nil, err
	}
	slog.Info("connected to VPP", "socket", sock)
	return c, nil
}

// checkCompatibility 对所有用到的绑定包做 CRC 兼容性自检，不匹配立即报错——
// 避免运行中途出现 "unknown message"。绑定针对某 VPP 版本，运行的 VPP 版本不同
// 且某消息布局变化时会在此被发现。
func (c *Client) checkCompatibility() error {
	var msgs []api.Message
	msgs = append(msgs, interfaces.AllMessages()...)
	msgs = append(msgs, ip.AllMessages()...)
	msgs = append(msgs, l2.AllMessages()...)
	msgs = append(msgs, vxlan.AllMessages()...)
	msgs = append(msgs, af_packet.AllMessages()...)
	msgs = append(msgs, tapv2.AllMessages()...)
	msgs = append(msgs, bond.AllMessages()...)
	msgs = append(msgs, ip6_nd.AllMessages()...)
	if err := c.ch.CheckCompatiblity(msgs...); err != nil {
		return fmt.Errorf("VPP API bindings incompatible with running VPP (regenerate binapi for the target VPP version): %w", err)
	}
	return nil
}

// Conn 返回底层连接（供生成的 RPC service client 使用）。
func (c *Client) Conn() api.Connection { return c.conn }

// Channel 返回 API channel。
func (c *Client) Channel() api.Channel { return c.ch }

// Close 关闭 channel 与连接。
func (c *Client) Close() {
	if c.ch != nil {
		c.ch.Close()
	}
	if c.conn != nil {
		c.conn.Disconnect()
	}
}

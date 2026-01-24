//go:build purego
// +build purego

/*
Copyright © 2024 netcfg authors

DHCP Relay Agent implementation.
Supports DHCPv4 (Option 82) and DHCPv6 (Relay-Forward/Reply).

To enable this module:
  1. go get github.com/insomniacslk/dhcp@latest
  2. go build -tags purego -o netcfg .
*/

package purego

import (
	"context"
	"fmt"
	"log/slog"
	"net"

	"github.com/insomniacslk/dhcp/dhcpv4"
	"github.com/insomniacslk/dhcp/dhcpv6"
)

// ============== DHCPv4 Relay ==============

// DHCPv4RelayConfig DHCPv4 Relay 配置
type DHCPv4RelayConfig struct {
	ListenAddr  net.IP   // 监听地址
	ServerAddrs []net.IP // DHCP 服务器地址
	CircuitID   string   // Option 82 Sub-option 1: Circuit ID
	RemoteID    string   // Option 82 Sub-option 2: Remote ID
	MaxHopCount int      // 最大跳数
	GatewayIP   net.IP   // giaddr
}

// DHCPv4Relay DHCPv4 中继代理
type DHCPv4Relay struct {
	config *DHCPv4RelayConfig
}

// NewDHCPv4Relay 创建 DHCPv4 Relay
func NewDHCPv4Relay(config *DHCPv4RelayConfig) *DHCPv4Relay {
	if config.MaxHopCount == 0 {
		config.MaxHopCount = 10
	}
	return &DHCPv4Relay{config: config}
}

// RelayToServer 将客户端请求中继到服务器
func (r *DHCPv4Relay) RelayToServer(packet *dhcpv4.DHCPv4, sourceAddr net.IP) (*dhcpv4.DHCPv4, error) {
	// 检查跳数
	if int(packet.HopCount) >= r.config.MaxHopCount {
		return nil, fmt.Errorf("max hop count %d exceeded", r.config.MaxHopCount)
	}

	// 复制并修改包
	relayed := packet.Clone()
	relayed.HopCount++

	// 设置 giaddr（如果尚未设置）
	if relayed.GatewayIPAddr == nil || relayed.GatewayIPAddr.IsUnspecified() {
		relayed.GatewayIPAddr = r.config.GatewayIP
	}

	// 添加 Option 82（Relay Agent Information）
	if r.config.CircuitID != "" || r.config.RemoteID != "" {
		opt82 := &dhcpv4.RelayOptions{}
		if r.config.CircuitID != "" {
			opt82.Options = append(opt82.Options, dhcpv4.OptGeneric(
				dhcpv4.AgentCircuitIDSubOption,
				[]byte(r.config.CircuitID),
			))
		}
		if r.config.RemoteID != "" {
			opt82.Options = append(opt82.Options, dhcpv4.OptGeneric(
				dhcpv4.AgentRemoteIDSubOption,
				[]byte(r.config.RemoteID),
			))
		}
		relayed.UpdateOption(dhcpv4.OptRelayAgentInfo(*opt82))
	}

	slog.Debug("DHCPv4 relay to server",
		"from", sourceAddr,
		"giaddr", relayed.GatewayIPAddr,
		"hop", relayed.HopCount,
		"type", relayed.MessageType())

	return relayed, nil
}

// RelayToClient 将服务器响应中继到客户端
func (r *DHCPv4Relay) RelayToClient(packet *dhcpv4.DHCPv4) *dhcpv4.DHCPv4 {
	// 移除 Option 82
	stripped := packet.Clone()
	stripped.Options.Del(dhcpv4.OptionRelayAgentInformation)

	slog.Debug("DHCPv4 relay to client",
		"yiaddr", stripped.YourIPAddr,
		"type", stripped.MessageType())

	return stripped
}

// GetOption82 从包中提取 Option 82 信息
func (r *DHCPv4Relay) GetOption82(packet *dhcpv4.DHCPv4) (circuitID, remoteID string) {
	opt82 := packet.RelayAgentInfo()
	if opt82 == nil {
		return "", ""
	}

	for _, subopt := range opt82.Options {
		switch subopt.Code {
		case dhcpv4.AgentCircuitIDSubOption:
			circuitID = string(subopt.Value)
		case dhcpv4.AgentRemoteIDSubOption:
			remoteID = string(subopt.Value)
		}
	}
	return
}

// ============== DHCPv6 Relay ==============

// DHCPv6RelayConfig DHCPv6 Relay 配置
type DHCPv6RelayConfig struct {
	ListenAddr  net.IP   // 监听地址（link-local）
	ServerAddrs []net.IP // DHCP 服务器地址
	InterfaceID string   // Interface ID option
	RemoteID    string   // Remote ID option
	MaxHopCount int      // 最大跳数
}

// DHCPv6Relay DHCPv6 中继代理
type DHCPv6Relay struct {
	config *DHCPv6RelayConfig
}

// NewDHCPv6Relay 创建 DHCPv6 Relay
func NewDHCPv6Relay(config *DHCPv6RelayConfig) *DHCPv6Relay {
	if config.MaxHopCount == 0 {
		config.MaxHopCount = 32
	}
	return &DHCPv6Relay{config: config}
}

// EncapsulateToServer 将客户端消息封装为 Relay-Forward
func (r *DHCPv6Relay) EncapsulateToServer(msg dhcpv6.DHCPv6, peerAddr, linkAddr net.IP) (*dhcpv6.RelayMessage, error) {
	// 检查是否已经是 Relay 消息
	if relay, ok := msg.(*dhcpv6.RelayMessage); ok {
		if relay.HopCount >= uint8(r.config.MaxHopCount) {
			return nil, fmt.Errorf("max hop count %d exceeded", r.config.MaxHopCount)
		}
	}

	relay, err := dhcpv6.NewRelayMessage()
	if err != nil {
		return nil, err
	}

	relay.MessageType = dhcpv6.MessageTypeRelayForward
	relay.HopCount = 0
	relay.LinkAddr = linkAddr
	relay.PeerAddr = peerAddr

	// 如果内层是 Relay 消息，增加 hop count
	if innerRelay, ok := msg.(*dhcpv6.RelayMessage); ok {
		relay.HopCount = innerRelay.HopCount + 1
	}

	// 封装原始消息
	relay.AddOption(dhcpv6.OptRelayMessage(msg))

	// 添加 Interface ID
	if r.config.InterfaceID != "" {
		relay.AddOption(&dhcpv6.OptInterfaceID{
			ID: []byte(r.config.InterfaceID),
		})
	}

	// 添加 Remote ID
	if r.config.RemoteID != "" {
		relay.AddOption(&dhcpv6.OptRemoteID{
			EnterpriseNumber: 0,
			RemoteID:         []byte(r.config.RemoteID),
		})
	}

	slog.Debug("DHCPv6 relay forward",
		"peer", peerAddr,
		"link", linkAddr,
		"hop", relay.HopCount)

	return relay, nil
}

// DecapsulateToClient 从 Relay-Reply 解包服务器响应
func (r *DHCPv6Relay) DecapsulateToClient(relay *dhcpv6.RelayMessage) (dhcpv6.DHCPv6, net.IP, error) {
	if relay.MessageType != dhcpv6.MessageTypeRelayReply {
		return nil, nil, fmt.Errorf("expected Relay-Reply, got %s", relay.MessageType)
	}

	// 获取内层消息
	inner := relay.Options.RelayMessage()
	if inner == nil {
		return nil, nil, fmt.Errorf("no relay message option found")
	}

	peerAddr := relay.PeerAddr

	// 如果内层还是 Relay-Reply，递归解包
	if innerRelay, ok := inner.(*dhcpv6.RelayMessage); ok {
		return r.DecapsulateToClient(innerRelay)
	}

	slog.Debug("DHCPv6 relay reply",
		"peer", peerAddr,
		"type", inner.Type())

	return inner, peerAddr, nil
}

// GetInterfaceID 从 Relay 消息中提取 Interface ID
func (r *DHCPv6Relay) GetInterfaceID(relay *dhcpv6.RelayMessage) string {
	for _, opt := range relay.Options.Options {
		if ifid, ok := opt.(*dhcpv6.OptInterfaceID); ok {
			return string(ifid.ID)
		}
	}
	return ""
}

// ============== Relay Server 框架 ==============

// RelayServer 通用 Relay 服务器框架
type RelayServer struct {
	v4Relay *DHCPv4Relay
	v6Relay *DHCPv6Relay
}

// NewRelayServer 创建 Relay 服务器
func NewRelayServer(v4Config *DHCPv4RelayConfig, v6Config *DHCPv6RelayConfig) *RelayServer {
	s := &RelayServer{}
	if v4Config != nil {
		s.v4Relay = NewDHCPv4Relay(v4Config)
	}
	if v6Config != nil {
		s.v6Relay = NewDHCPv6Relay(v6Config)
	}
	return s
}

// Run 运行 Relay 服务器
func (s *RelayServer) Run(ctx context.Context) error {
	slog.Info("DHCP relay server starting")

	// 这里需要实现完整的 UDP 监听逻辑
	// DHCPv4: 监听 67 端口
	// DHCPv6: 监听 547 端口

	// 伪代码框架：
	// 1. 监听 UDP 端口
	// 2. 接收客户端请求
	// 3. 调用 RelayToServer / EncapsulateToServer
	// 4. 转发到 DHCP 服务器
	// 5. 接收服务器响应
	// 6. 调用 RelayToClient / DecapsulateToClient
	// 7. 转发给客户端

	<-ctx.Done()
	return ctx.Err()
}

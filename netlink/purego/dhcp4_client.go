//go:build purego
// +build purego

/*
Copyright © 2024 netcfg authors

Pure Go DHCPv4 client using insomniacslk/dhcp library.
Supports: DORA handshake, renewal, release, inform, decline.

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
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4"
	"github.com/insomniacslk/dhcp/dhcpv4/nclient4"
)

// DHCPv4Lease DHCPv4 租约信息
type DHCPv4Lease struct {
	IP         net.IP
	Netmask    net.IPMask
	Gateway    net.IP
	DNS        []net.IP
	Domain     string
	LeaseTime  time.Duration
	ServerIP   net.IP
	MTU        int
	Routes     []*dhcpv4.Route // Classless static routes
	NTPServers []net.IP
	RenewTime  time.Duration // T1
	RebindTime time.Duration // T2
}

// DHCPv4Client 纯 Go DHCPv4 客户端
type DHCPv4Client struct {
	ifaceName  string
	iface      *net.Interface
	timeout    time.Duration
	retries    int
	hostname   string
	clientID   []byte
	requestIPs []net.IP
	options    []dhcpv4.Option
}

// NewDHCPv4Client 创建 DHCPv4 客户端
func NewDHCPv4Client(ifaceName string) (*DHCPv4Client, error) {
	iface, err := net.InterfaceByName(ifaceName)
	if err != nil {
		return nil, fmt.Errorf("interface %s not found: %w", ifaceName, err)
	}
	return &DHCPv4Client{
		ifaceName: ifaceName,
		iface:     iface,
		timeout:   10 * time.Second,
		retries:   3,
	}, nil
}

// SetTimeout 设置超时
func (c *DHCPv4Client) SetTimeout(d time.Duration) { c.timeout = d }

// SetRetries 设置重试次数
func (c *DHCPv4Client) SetRetries(n int) { c.retries = n }

// SetHostname 设置主机名
func (c *DHCPv4Client) SetHostname(name string) { c.hostname = name }

// SetClientID 设置客户端 ID
func (c *DHCPv4Client) SetClientID(id []byte) { c.clientID = id }

// SetRequestIP 设置请求的 IP
func (c *DHCPv4Client) SetRequestIP(ip net.IP) { c.requestIPs = []net.IP{ip} }

// AddOption 添加自定义选项
func (c *DHCPv4Client) AddOption(opt dhcpv4.Option) { c.options = append(c.options, opt) }

// Request 执行完整的 DHCP 请求 (DORA)
func (c *DHCPv4Client) Request(ctx context.Context) (*DHCPv4Lease, error) {
	slog.Info("DHCPv4 request starting", "interface", c.ifaceName, "mac", c.iface.HardwareAddr)

	client, err := nclient4.New(c.ifaceName,
		nclient4.WithTimeout(c.timeout),
		nclient4.WithRetry(c.retries),
	)
	if err != nil {
		return nil, fmt.Errorf("create DHCP client: %w", err)
	}
	defer client.Close()

	// 构建修改器
	var mods []dhcpv4.Modifier
	if c.hostname != "" {
		mods = append(mods, dhcpv4.WithOption(dhcpv4.OptHostName(c.hostname)))
	}
	if c.clientID != nil {
		mods = append(mods, dhcpv4.WithOption(dhcpv4.OptClientIdentifier(c.clientID)))
	}
	for _, opt := range c.options {
		mods = append(mods, dhcpv4.WithOption(opt))
	}

	// 请求参数列表
	mods = append(mods, dhcpv4.WithRequestedOptions(
		dhcpv4.OptionSubnetMask,
		dhcpv4.OptionRouter,
		dhcpv4.OptionDomainNameServer,
		dhcpv4.OptionDomainName,
		dhcpv4.OptionInterfaceMTU,
		dhcpv4.OptionClasslessStaticRoute,
		dhcpv4.OptionNTPServers,
	))

	// 执行 DORA
	lease, err := client.Request(ctx, mods...)
	if err != nil {
		return nil, fmt.Errorf("DHCP request failed: %w", err)
	}

	return c.parseLeaseFromACK(lease.ACK), nil
}

// Renew 续约
func (c *DHCPv4Client) Renew(ctx context.Context, currentLease *DHCPv4Lease) (*DHCPv4Lease, error) {
	slog.Info("DHCPv4 renew", "interface", c.ifaceName, "ip", currentLease.IP)

	client, err := nclient4.New(c.ifaceName,
		nclient4.WithTimeout(c.timeout),
	)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	// 构建 REQUEST 消息用于续约
	req, err := dhcpv4.NewRequestFromOffer(c.createOfferFromLease(currentLease))
	if err != nil {
		return nil, err
	}

	// 发送到服务器（单播）
	resp, err := client.SendAndRead(ctx,
		&net.UDPAddr{IP: currentLease.ServerIP, Port: 67},
		req, nil)
	if err != nil {
		return nil, fmt.Errorf("renew failed: %w", err)
	}

	if resp.MessageType() != dhcpv4.MessageTypeAck {
		return nil, fmt.Errorf("renew got %s instead of ACK", resp.MessageType())
	}

	return c.parseLeaseFromACK(resp), nil
}

// Release 释放地址
func (c *DHCPv4Client) Release(ctx context.Context, lease *DHCPv4Lease) error {
	slog.Info("DHCPv4 release", "interface", c.ifaceName, "ip", lease.IP)

	client, err := nclient4.New(c.ifaceName)
	if err != nil {
		return err
	}
	defer client.Close()

	release, err := dhcpv4.New(
		dhcpv4.WithMessageType(dhcpv4.MessageTypeRelease),
		dhcpv4.WithHwAddr(c.iface.HardwareAddr),
		dhcpv4.WithClientIP(lease.IP),
		dhcpv4.WithOption(dhcpv4.OptServerIdentifier(lease.ServerIP)),
	)
	if err != nil {
		return err
	}

	// 发送 RELEASE（单播到服务器，不期望响应）
	client.SendAndRead(ctx,
		&net.UDPAddr{IP: lease.ServerIP, Port: 67},
		release, nil)
	return nil
}

// Decline 拒绝地址（地址冲突时使用）
func (c *DHCPv4Client) Decline(ctx context.Context, ip net.IP, serverIP net.IP) error {
	slog.Info("DHCPv4 decline", "interface", c.ifaceName, "ip", ip)

	client, err := nclient4.New(c.ifaceName)
	if err != nil {
		return err
	}
	defer client.Close()

	decline, err := dhcpv4.New(
		dhcpv4.WithMessageType(dhcpv4.MessageTypeDecline),
		dhcpv4.WithHwAddr(c.iface.HardwareAddr),
		dhcpv4.WithOption(dhcpv4.OptRequestedIPAddress(ip)),
		dhcpv4.WithOption(dhcpv4.OptServerIdentifier(serverIP)),
	)
	if err != nil {
		return err
	}

	client.SendAndRead(ctx, nclient4.DefaultServers, decline, nil)
	return nil
}

// Inform 发送 DHCPINFORM（已有 IP，仅获取配置）
func (c *DHCPv4Client) Inform(ctx context.Context, clientIP net.IP) (*DHCPv4Lease, error) {
	slog.Info("DHCPv4 inform", "interface", c.ifaceName, "ip", clientIP)

	client, err := nclient4.New(c.ifaceName)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	inform, err := dhcpv4.New(
		dhcpv4.WithMessageType(dhcpv4.MessageTypeInform),
		dhcpv4.WithHwAddr(c.iface.HardwareAddr),
		dhcpv4.WithClientIP(clientIP),
		dhcpv4.WithRequestedOptions(
			dhcpv4.OptionDomainNameServer,
			dhcpv4.OptionDomainName,
			dhcpv4.OptionNTPServers,
		),
	)
	if err != nil {
		return nil, err
	}

	resp, err := client.SendAndRead(ctx, nclient4.DefaultServers, inform, nil)
	if err != nil {
		return nil, err
	}

	lease := c.parseLeaseFromACK(resp)
	lease.IP = clientIP
	return lease, nil
}

func (c *DHCPv4Client) parseLeaseFromACK(ack *dhcpv4.DHCPv4) *DHCPv4Lease {
	lease := &DHCPv4Lease{
		IP:       ack.YourIPAddr,
		ServerIP: ack.ServerIPAddr,
	}

	// 子网掩码
	if mask := ack.SubnetMask(); mask != nil {
		lease.Netmask = mask
	} else {
		lease.Netmask = net.CIDRMask(24, 32)
	}

	// 网关
	if routers := ack.Router(); len(routers) > 0 {
		lease.Gateway = routers[0]
	}

	// DNS
	lease.DNS = ack.DNS()

	// 域名
	if domain := ack.DomainName(); domain != "" {
		lease.Domain = domain
	}

	// 租约时间
	if leaseTime := ack.IPAddressLeaseTime(0); leaseTime > 0 {
		lease.LeaseTime = leaseTime
	} else {
		lease.LeaseTime = 24 * time.Hour
	}

	// MTU
	if mtu, err := dhcpv4.GetUint16(dhcpv4.OptionInterfaceMTU, ack.Options); err == nil {
		lease.MTU = int(mtu)
	}

	// 静态路由
	if routes := ack.ClasslessStaticRoute(); routes != nil {
		lease.Routes = routes
	}

	// NTP 服务器
	if ntpOpt := ack.Options.Get(dhcpv4.OptionNTPServers); ntpOpt != nil {
		lease.NTPServers = dhcpv4.GetIPs(dhcpv4.OptionNTPServers, ack.Options)
	}

	// Server Identifier
	if serverID := ack.ServerIdentifier(); serverID != nil {
		lease.ServerIP = serverID
	}

	// T1 / T2
	if t1 := ack.IPAddressRenewalTime(0); t1 > 0 {
		lease.RenewTime = t1
	} else {
		lease.RenewTime = lease.LeaseTime / 2
	}
	if t2 := ack.IPAddressRebindingTime(0); t2 > 0 {
		lease.RebindTime = t2
	} else {
		lease.RebindTime = lease.LeaseTime * 7 / 8
	}

	return lease
}

func (c *DHCPv4Client) createOfferFromLease(lease *DHCPv4Lease) *dhcpv4.DHCPv4 {
	offer, _ := dhcpv4.New(
		dhcpv4.WithMessageType(dhcpv4.MessageTypeOffer),
		dhcpv4.WithHwAddr(c.iface.HardwareAddr),
		dhcpv4.WithYourIP(lease.IP),
		dhcpv4.WithServerIP(lease.ServerIP),
		dhcpv4.WithNetmask(lease.Netmask),
		dhcpv4.WithRouter(lease.Gateway),
		dhcpv4.WithDNS(lease.DNS...),
	)
	return offer
}

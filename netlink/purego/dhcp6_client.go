/*
Copyright © 2024 netcfg authors

Pure Go DHCPv6 client using insomniacslk/dhcp library.
Supports: Solicit/Advertise/Request/Reply, Rapid Commit, IA_NA, IA_PD, Renew, Release.

Compiled by default and used as the primary DHCP path (netlink.DHCPManager
falls back to external clients when the pure-Go path fails).
*/

package purego

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv6"
	"github.com/insomniacslk/dhcp/dhcpv6/nclient6"
)

// DHCPv6Lease DHCPv6 租约信息
type DHCPv6Lease struct {
	Addresses  []net.IP
	Prefixes   []net.IPNet
	DNS        []net.IP
	Domains    []string
	LeaseTime  time.Duration
	ServerDUID dhcpv6.DUID
	IAID       [4]byte
}

// DHCPv6Client 纯 Go DHCPv6 客户端
type DHCPv6Client struct {
	ifaceName   string
	iface       *net.Interface
	timeout     time.Duration
	retries     int
	rapidCommit bool
	requestPD   bool
	requestNA   bool
	pdHint      uint8
	options     []dhcpv6.Option
}

// NewDHCPv6Client 创建 DHCPv6 客户端
func NewDHCPv6Client(ifaceName string) (*DHCPv6Client, error) {
	iface, err := net.InterfaceByName(ifaceName)
	if err != nil {
		return nil, fmt.Errorf("interface %s not found: %w", ifaceName, err)
	}
	return &DHCPv6Client{
		ifaceName:   ifaceName,
		iface:       iface,
		timeout:     10 * time.Second,
		retries:     3,
		requestNA:   true,
		rapidCommit: false,
	}, nil
}

// SetTimeout 设置超时
func (c *DHCPv6Client) SetTimeout(d time.Duration) { c.timeout = d }

// SetRetries 设置重试次数
func (c *DHCPv6Client) SetRetries(n int) { c.retries = n }

// SetRapidCommit 启用快速提交（2-way handshake）
func (c *DHCPv6Client) SetRapidCommit(enable bool) { c.rapidCommit = enable }

// SetRequestPD 请求前缀委派
func (c *DHCPv6Client) SetRequestPD(enable bool, prefixLen uint8) {
	c.requestPD = enable
	c.pdHint = prefixLen
}

// SetRequestNA 请求非临时地址
func (c *DHCPv6Client) SetRequestNA(enable bool) { c.requestNA = enable }

// AddOption 添加自定义选项
func (c *DHCPv6Client) AddOption(opt dhcpv6.Option) { c.options = append(c.options, opt) }

// Request 执行完整的 DHCPv6 请求
func (c *DHCPv6Client) Request(ctx context.Context) (*DHCPv6Lease, error) {
	slog.Info("DHCPv6 request starting", "interface", c.ifaceName, "mac", c.iface.HardwareAddr)

	client, err := nclient6.New(c.ifaceName,
		nclient6.WithTimeout(c.timeout),
		nclient6.WithRetry(c.retries),
	)
	if err != nil {
		return nil, fmt.Errorf("create DHCPv6 client: %w", err)
	}
	defer client.Close()

	// 构建修改器
	var mods []dhcpv6.Modifier

	// 请求选项
	mods = append(mods, dhcpv6.WithRequestedOptions(
		dhcpv6.OptionDNSRecursiveNameServer,
		dhcpv6.OptionDomainSearchList,
	))

	// IA_PD（前缀委派）
	if c.requestPD {
		iaid := [4]byte{0, 0, 0, 2}
		mods = append(mods, dhcpv6.WithIAPD(iaid))
	}

	// 添加自定义选项
	for _, opt := range c.options {
		mods = append(mods, dhcpv6.WithOption(opt))
	}

	var reply *dhcpv6.Message

	if c.rapidCommit {
		// 快速提交: Solicit (with Rapid Commit) -> Reply
		mods = append(mods, dhcpv6.WithRapidCommit)
		reply, err = client.RapidSolicit(ctx, mods...)
	} else {
		// 标准 4-way: Solicit -> Advertise -> Request -> Reply。
		// 注意：adv 用 var 声明，避免 := 在 else 块内遮蔽外层 err，
		// 导致下面 client.Request 的错误被吞（外层 if err 检查不到）。
		var adv *dhcpv6.Message
		adv, err = client.Solicit(ctx, mods...)
		if err != nil {
			return nil, fmt.Errorf("solicit failed: %w", err)
		}
		reply, err = client.Request(ctx, adv, mods...)
	}

	if err != nil {
		return nil, fmt.Errorf("DHCPv6 request failed: %w", err)
	}

	return c.parseLeaseFromReply(reply), nil
}

// Renew 续约
func (c *DHCPv6Client) Renew(ctx context.Context, currentLease *DHCPv6Lease) (*DHCPv6Lease, error) {
	slog.Info("DHCPv6 renew", "interface", c.ifaceName)

	client, err := nclient6.New(c.ifaceName,
		nclient6.WithTimeout(c.timeout),
	)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	// 构建 Renew 消息
	renew, err := dhcpv6.NewMessage()
	if err != nil {
		return nil, err
	}
	renew.MessageType = dhcpv6.MessageTypeRenew

	// Client ID
	duid := &dhcpv6.DUIDLL{
		HWType:        6, // Ethernet
		LinkLayerAddr: c.iface.HardwareAddr,
	}
	renew.AddOption(dhcpv6.OptClientID(duid))

	// Server ID
	if currentLease.ServerDUID != nil {
		renew.AddOption(dhcpv6.OptServerID(currentLease.ServerDUID))
	}

	// IA_NA
	for _, addr := range currentLease.Addresses {
		iana := &dhcpv6.OptIANA{
			IaId: currentLease.IAID,
			Options: dhcpv6.IdentityOptions{
				Options: []dhcpv6.Option{
					&dhcpv6.OptIAAddress{
						IPv6Addr: addr,
					},
				},
			},
		}
		renew.AddOption(iana)
	}

	// 发送
	resp, err := client.SendAndRead(ctx, nclient6.AllDHCPRelayAgentsAndServers, renew, nil)
	if err != nil {
		return nil, fmt.Errorf("renew failed: %w", err)
	}

	return c.parseLeaseFromReply(resp), nil
}

// Rebind 重绑定（T2 时间后，向所有服务器广播）
func (c *DHCPv6Client) Rebind(ctx context.Context, currentLease *DHCPv6Lease) (*DHCPv6Lease, error) {
	slog.Info("DHCPv6 rebind", "interface", c.ifaceName)

	client, err := nclient6.New(c.ifaceName,
		nclient6.WithTimeout(c.timeout),
	)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	// 构建 Rebind 消息
	rebind, err := dhcpv6.NewMessage()
	if err != nil {
		return nil, err
	}
	rebind.MessageType = dhcpv6.MessageTypeRebind

	// Client ID
	duid := &dhcpv6.DUIDLL{
		HWType:        6,
		LinkLayerAddr: c.iface.HardwareAddr,
	}
	rebind.AddOption(dhcpv6.OptClientID(duid))

	// IA_NA（不包含 Server ID，向所有服务器广播）
	for _, addr := range currentLease.Addresses {
		iana := &dhcpv6.OptIANA{
			IaId: currentLease.IAID,
			Options: dhcpv6.IdentityOptions{
				Options: []dhcpv6.Option{
					&dhcpv6.OptIAAddress{
						IPv6Addr: addr,
					},
				},
			},
		}
		rebind.AddOption(iana)
	}

	resp, err := client.SendAndRead(ctx, nclient6.AllDHCPRelayAgentsAndServers, rebind, nil)
	if err != nil {
		return nil, fmt.Errorf("rebind failed: %w", err)
	}

	return c.parseLeaseFromReply(resp), nil
}

// Release 释放地址
func (c *DHCPv6Client) Release(ctx context.Context, lease *DHCPv6Lease) error {
	slog.Info("DHCPv6 release", "interface", c.ifaceName)

	client, err := nclient6.New(c.ifaceName)
	if err != nil {
		return err
	}
	defer client.Close()

	release, err := dhcpv6.NewMessage()
	if err != nil {
		return err
	}
	release.MessageType = dhcpv6.MessageTypeRelease

	// Client ID
	duid := &dhcpv6.DUIDLL{
		HWType:        6,
		LinkLayerAddr: c.iface.HardwareAddr,
	}
	release.AddOption(dhcpv6.OptClientID(duid))

	// Server ID
	if lease.ServerDUID != nil {
		release.AddOption(dhcpv6.OptServerID(lease.ServerDUID))
	}

	// 要释放的地址
	for _, addr := range lease.Addresses {
		iana := &dhcpv6.OptIANA{
			IaId: lease.IAID,
			Options: dhcpv6.IdentityOptions{
				Options: []dhcpv6.Option{
					&dhcpv6.OptIAAddress{
						IPv6Addr: addr,
					},
				},
			},
		}
		release.AddOption(iana)
	}

	// 发送（不期望响应；best-effort，忽略返回）
	_, _ = client.SendAndRead(ctx, nclient6.AllDHCPRelayAgentsAndServers, release, nil)
	return nil
}

// Decline 拒绝地址（地址冲突时使用）
func (c *DHCPv6Client) Decline(ctx context.Context, lease *DHCPv6Lease, conflictAddrs []net.IP) error {
	slog.Info("DHCPv6 decline", "interface", c.ifaceName, "addresses", conflictAddrs)

	client, err := nclient6.New(c.ifaceName)
	if err != nil {
		return err
	}
	defer client.Close()

	decline, err := dhcpv6.NewMessage()
	if err != nil {
		return err
	}
	decline.MessageType = dhcpv6.MessageTypeDecline

	// Client ID
	duid := &dhcpv6.DUIDLL{
		HWType:        6,
		LinkLayerAddr: c.iface.HardwareAddr,
	}
	decline.AddOption(dhcpv6.OptClientID(duid))

	// Server ID
	if lease.ServerDUID != nil {
		decline.AddOption(dhcpv6.OptServerID(lease.ServerDUID))
	}

	// 冲突的地址
	for _, addr := range conflictAddrs {
		iana := &dhcpv6.OptIANA{
			IaId: lease.IAID,
			Options: dhcpv6.IdentityOptions{
				Options: []dhcpv6.Option{
					&dhcpv6.OptIAAddress{
						IPv6Addr: addr,
					},
				},
			},
		}
		decline.AddOption(iana)
	}

	// best-effort，忽略返回
	_, _ = client.SendAndRead(ctx, nclient6.AllDHCPRelayAgentsAndServers, decline, nil)
	return nil
}

// InformationRequest 仅请求配置信息（无状态 DHCPv6）
func (c *DHCPv6Client) InformationRequest(ctx context.Context) (*DHCPv6Lease, error) {
	slog.Info("DHCPv6 information-request", "interface", c.ifaceName)

	client, err := nclient6.New(c.ifaceName,
		nclient6.WithTimeout(c.timeout),
	)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	infoReq, err := dhcpv6.NewMessage()
	if err != nil {
		return nil, err
	}
	infoReq.MessageType = dhcpv6.MessageTypeInformationRequest

	// Client ID
	duid := &dhcpv6.DUIDLL{
		HWType:        6,
		LinkLayerAddr: c.iface.HardwareAddr,
	}
	infoReq.AddOption(dhcpv6.OptClientID(duid))

	// 请求选项
	infoReq.AddOption(dhcpv6.OptRequestedOption(
		dhcpv6.OptionDNSRecursiveNameServer,
		dhcpv6.OptionDomainSearchList,
		dhcpv6.OptionBootfileURL,
		dhcpv6.OptionSNTPServerList,
	))

	resp, err := client.SendAndRead(ctx, nclient6.AllDHCPRelayAgentsAndServers, infoReq, nil)
	if err != nil {
		return nil, err
	}

	return c.parseLeaseFromReply(resp), nil
}

func (c *DHCPv6Client) parseLeaseFromReply(reply *dhcpv6.Message) *DHCPv6Lease {
	lease := &DHCPv6Lease{}

	// IA_NA（非临时地址）
	for _, iana := range reply.Options.IANA() {
		lease.IAID = iana.IaId
		for _, iaaddr := range iana.Options.Addresses() {
			lease.Addresses = append(lease.Addresses, iaaddr.IPv6Addr)
			if iaaddr.ValidLifetime > 0 {
				lease.LeaseTime = iaaddr.ValidLifetime
			}
		}
	}

	// IA_PD（前缀委派）
	for _, iapd := range reply.Options.IAPD() {
		for _, prefix := range iapd.Options.Prefixes() {
			lease.Prefixes = append(lease.Prefixes, net.IPNet{
				IP:   prefix.Prefix.IP,
				Mask: prefix.Prefix.Mask,
			})
		}
	}

	// DNS
	if dns := reply.Options.DNS(); len(dns) > 0 {
		lease.DNS = dns
	}

	// 域名搜索列表
	if domains := reply.Options.DomainSearchList(); domains != nil {
		lease.Domains = domains.Labels
	}

	// Server ID
	if serverID := reply.Options.ServerID(); serverID != nil {
		lease.ServerDUID = serverID
	}

	// 默认租约时间
	if lease.LeaseTime == 0 {
		lease.LeaseTime = 24 * time.Hour
	}

	return lease
}

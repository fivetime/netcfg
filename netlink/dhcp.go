/*
Copyright © 2024 netcfg authors

DHCP module - DHCPv4, DHCPv6, SLAAC support.

Current implementation uses external DHCP clients (dhclient/dhcpcd/udhcpc).
Pure Go implementation using insomniacslk/dhcp is available but requires
vendoring the library when network access is available.

TODO: Vendor insomniacslk/dhcp for pure Go implementation:
  go get github.com/insomniacslk/dhcp@latest
  go mod vendor
*/

package netlink

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/vishvananda/netlink"
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
	NTPServers []net.IP
}

// DHCPv6Lease DHCPv6 租约信息
type DHCPv6Lease struct {
	Addresses  []net.IP
	Prefixes   []net.IPNet
	DNS        []net.IP
	Domains    []string
	LeaseTime  time.Duration
	ServerDUID interface{}
}

// DHCPManager DHCP 管理器
type DHCPManager struct {
	timeout    time.Duration
	retries    int
	hostname   string
	externalV4 string
	externalV6 string
}

// NewDHCPManager 创建 DHCP 管理器
func NewDHCPManager() *DHCPManager {
	m := &DHCPManager{
		timeout: 30 * time.Second,
		retries: 3,
	}
	m.detectExternalClients()
	if h, err := os.Hostname(); err == nil {
		m.hostname = h
	}
	return m
}

func (m *DHCPManager) detectExternalClients() {
	for _, name := range []string{"dhclient", "dhcpcd", "udhcpc"} {
		if path, err := exec.LookPath(name); err == nil {
			m.externalV4 = path
			break
		}
	}
	for _, name := range []string{"dhclient", "dhcpcd", "dhcp6c"} {
		if path, err := exec.LookPath(name); err == nil {
			m.externalV6 = path
			break
		}
	}
}

func (m *DHCPManager) SetTimeout(d time.Duration) { m.timeout = d }
func (m *DHCPManager) SetRetries(n int)           { m.retries = n }
func (m *DHCPManager) HasClient() (v4, v6 bool) {
	return m.externalV4 != "", m.externalV6 != ""
}

// RequestDHCPv4 请求 DHCPv4 地址
func (m *DHCPManager) RequestDHCPv4(ifaceName string) (*DHCPv4Lease, error) {
	return m.RequestDHCPv4WithContext(context.Background(), ifaceName)
}

func (m *DHCPManager) RequestDHCPv4WithContext(ctx context.Context, ifaceName string) (*DHCPv4Lease, error) {
	slog.Info("requesting DHCPv4", "interface", ifaceName)

	if m.externalV4 == "" {
		return nil, fmt.Errorf("no DHCPv4 client (install dhclient/dhcpcd/udhcpc)")
	}

	clientName := filepath.Base(m.externalV4)
	pidFile := fmt.Sprintf("/run/netcfg-dhcp4-%s.pid", ifaceName)
	leaseFile := fmt.Sprintf("/var/lib/netcfg/dhcp4-%s.lease", ifaceName)
	os.MkdirAll("/var/lib/netcfg", 0755)

	var cmd *exec.Cmd
	switch clientName {
	case "dhclient":
		args := []string{"-v", "-1", "-pf", pidFile, "-lf", leaseFile}
		if m.hostname != "" {
			args = append(args, "-H", m.hostname)
		}
		args = append(args, ifaceName)
		cmd = exec.CommandContext(ctx, m.externalV4, args...)
	case "dhcpcd":
		cmd = exec.CommandContext(ctx, m.externalV4, "-w", "-1", "-4", ifaceName)
	case "udhcpc":
		args := []string{"-i", ifaceName, "-f", "-q", "-n"}
		if m.hostname != "" {
			args = append(args, "-H", m.hostname)
		}
		cmd = exec.CommandContext(ctx, m.externalV4, args...)
	}

	if output, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("DHCPv4 failed: %w\n%s", err, output)
	}

	return m.getLease4FromInterface(ifaceName)
}

func (m *DHCPManager) RenewDHCPv4(ctx context.Context, ifaceName string, lease *DHCPv4Lease) (*DHCPv4Lease, error) {
	// 外部客户端通常自己处理续约
	return m.RequestDHCPv4WithContext(ctx, ifaceName)
}

func (m *DHCPManager) ReleaseDHCPv4(ifaceName string) error {
	slog.Info("DHCPv4 release", "interface", ifaceName)
	if m.externalV4 == "" {
		return nil
	}
	pidFile := fmt.Sprintf("/run/netcfg-dhcp4-%s.pid", ifaceName)
	switch filepath.Base(m.externalV4) {
	case "dhclient":
		exec.Command(m.externalV4, "-r", "-pf", pidFile, ifaceName).Run()
	case "dhcpcd":
		exec.Command(m.externalV4, "-k", "-4", ifaceName).Run()
	}
	return nil
}

// RequestDHCPv6 请求 DHCPv6 地址
func (m *DHCPManager) RequestDHCPv6(ifaceName string, rapidCommit bool) (*DHCPv6Lease, error) {
	return m.RequestDHCPv6WithContext(context.Background(), ifaceName, rapidCommit)
}

func (m *DHCPManager) RequestDHCPv6WithContext(ctx context.Context, ifaceName string, rapidCommit bool) (*DHCPv6Lease, error) {
	slog.Info("requesting DHCPv6", "interface", ifaceName, "rapid_commit", rapidCommit)

	if m.externalV6 == "" {
		return nil, fmt.Errorf("no DHCPv6 client (install dhclient/dhcpcd)")
	}

	clientName := filepath.Base(m.externalV6)
	pidFile := fmt.Sprintf("/run/netcfg-dhcp6-%s.pid", ifaceName)
	leaseFile := fmt.Sprintf("/var/lib/netcfg/dhcp6-%s.lease", ifaceName)
	os.MkdirAll("/var/lib/netcfg", 0755)

	var cmd *exec.Cmd
	switch clientName {
	case "dhclient":
		cmd = exec.CommandContext(ctx, m.externalV6, "-6", "-v", "-1", "-pf", pidFile, "-lf", leaseFile, ifaceName)
	case "dhcpcd":
		cmd = exec.CommandContext(ctx, m.externalV6, "-w", "-1", "-6", ifaceName)
	}

	if output, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("DHCPv6 failed: %w\n%s", err, output)
	}

	return m.getLease6FromInterface(ifaceName)
}

func (m *DHCPManager) ReleaseDHCPv6(ifaceName string) error {
	slog.Info("DHCPv6 release", "interface", ifaceName)
	if m.externalV6 == "" {
		return nil
	}
	pidFile := fmt.Sprintf("/run/netcfg-dhcp6-%s.pid", ifaceName)
	switch filepath.Base(m.externalV6) {
	case "dhclient":
		exec.Command(m.externalV6, "-6", "-r", "-pf", pidFile, ifaceName).Run()
	case "dhcpcd":
		exec.Command(m.externalV6, "-k", "-6", ifaceName).Run()
	}
	return nil
}

// EnableSLAAC 启用 IPv6 SLAAC
func EnableSLAAC(ifaceName string) error {
	slog.Info("enabling SLAAC", "interface", ifaceName)
	writeSysctl(ifaceName, "accept_ra", "2")
	writeSysctl(ifaceName, "autoconf", "1")
	writeSysctl(ifaceName, "use_tempaddr", "2")
	return nil
}

// DisableSLAAC 禁用 IPv6 SLAAC
func DisableSLAAC(ifaceName string) error {
	writeSysctl(ifaceName, "accept_ra", "0")
	writeSysctl(ifaceName, "autoconf", "0")
	return nil
}

func SetAcceptRA(ifaceName string, value int) error {
	return writeSysctl(ifaceName, "accept_ra", fmt.Sprintf("%d", value))
}

func SetIPv6Privacy(ifaceName string, value int) error {
	return writeSysctl(ifaceName, "use_tempaddr", fmt.Sprintf("%d", value))
}

// ApplyDHCPv4Lease 应用 DHCPv4 租约
func (m *DHCPManager) ApplyDHCPv4Lease(ifaceName string, lease *DHCPv4Lease) error {
	link, err := netlink.LinkByName(ifaceName)
	if err != nil {
		return err
	}
	addrs, _ := netlink.AddrList(link, netlink.FAMILY_V4)
	for _, a := range addrs {
		netlink.AddrDel(link, &a)
	}
	ones, _ := lease.Netmask.Size()
	addr := &netlink.Addr{IPNet: &net.IPNet{IP: lease.IP, Mask: lease.Netmask}}
	if err := netlink.AddrAdd(link, addr); err != nil {
		return fmt.Errorf("add addr %s/%d: %w", lease.IP, ones, err)
	}
	if lease.Gateway != nil {
		netlink.RouteAdd(&netlink.Route{LinkIndex: link.Attrs().Index, Gw: lease.Gateway})
	}
	if lease.MTU > 0 {
		netlink.LinkSetMTU(link, lease.MTU)
	}
	if len(lease.DNS) > 0 {
		UpdateResolvConf(lease.DNS, lease.Domain)
	}
	slog.Info("DHCPv4 lease applied", "interface", ifaceName, "ip", lease.IP)
	return nil
}

func (m *DHCPManager) getLease4FromInterface(ifaceName string) (*DHCPv4Lease, error) {
	iface, err := net.InterfaceByName(ifaceName)
	if err != nil {
		return nil, err
	}
	addrs, _ := iface.Addrs()
	lease := &DHCPv4Lease{MTU: iface.MTU}
	for _, addr := range addrs {
		if ipNet, ok := addr.(*net.IPNet); ok {
			if ip4 := ipNet.IP.To4(); ip4 != nil {
				lease.IP, lease.Netmask = ip4, ipNet.Mask
				break
			}
		}
	}
	if lease.IP == nil {
		return nil, fmt.Errorf("no IPv4 on %s", ifaceName)
	}
	lease.Gateway = getDefaultGateway4()
	lease.DNS, lease.Domain = getDNSConfig()
	return lease, nil
}

func (m *DHCPManager) getLease6FromInterface(ifaceName string) (*DHCPv6Lease, error) {
	iface, err := net.InterfaceByName(ifaceName)
	if err != nil {
		return nil, err
	}
	addrs, _ := iface.Addrs()
	lease := &DHCPv6Lease{}
	for _, addr := range addrs {
		if ipNet, ok := addr.(*net.IPNet); ok {
			if ip6 := ipNet.IP.To16(); ip6 != nil && ipNet.IP.To4() == nil && !ipNet.IP.IsLinkLocalUnicast() {
				lease.Addresses = append(lease.Addresses, ip6)
			}
		}
	}
	if len(lease.Addresses) == 0 {
		return nil, fmt.Errorf("no IPv6 on %s", ifaceName)
	}
	return lease, nil
}

func writeSysctl(ifaceName, key, value string) error {
	return os.WriteFile(fmt.Sprintf("/proc/sys/net/ipv6/conf/%s/%s", ifaceName, key), []byte(value), 0644)
}

func getDefaultGateway4() net.IP {
	data, _ := os.ReadFile("/proc/net/route")
	for _, line := range strings.Split(string(data), "\n")[1:] {
		fields := strings.Fields(line)
		if len(fields) >= 3 && fields[1] == "00000000" {
			var gw [4]byte
			fmt.Sscanf(fields[2], "%02x%02x%02x%02x", &gw[3], &gw[2], &gw[1], &gw[0])
			return net.IPv4(gw[0], gw[1], gw[2], gw[3])
		}
	}
	return nil
}

func getDNSConfig() ([]net.IP, string) { return parseDNSFromResolv("/etc/resolv.conf") }

func parseDNSFromResolv(path string) ([]net.IP, string) {
	data, _ := os.ReadFile(path)
	var dns []net.IP
	var domain string
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(strings.TrimSpace(line))
		if len(f) < 2 || strings.HasPrefix(f[0], "#") {
			continue
		}
		switch f[0] {
		case "nameserver":
			if ip := net.ParseIP(f[1]); ip != nil {
				dns = append(dns, ip)
			}
		case "search", "domain":
			domain = f[1]
		}
	}
	return dns, domain
}

func UpdateResolvConf(dns []net.IP, domain string) error {
	if _, err := os.Stat("/run/systemd/resolve/stub-resolv.conf"); err == nil {
		return nil // systemd-resolved 管理
	}
	var b strings.Builder
	b.WriteString("# Generated by netcfg\n")
	if domain != "" {
		b.WriteString("search " + domain + "\n")
	}
	for _, d := range dns {
		b.WriteString("nameserver " + d.String() + "\n")
	}
	return os.WriteFile("/etc/resolv.conf", []byte(b.String()), 0644)
}

func HasDHCPClient() (v4, v6 bool) { m := NewDHCPManager(); return m.HasClient() }

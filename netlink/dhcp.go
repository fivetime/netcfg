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

	"github.com/netcfg/netcfg/netlink/purego"
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
	dhcpIdent  string // dhcp-identifier: "mac" 用 MAC，其余/空走客户端默认
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

// SetHostname 覆盖请求时发送的主机名（DHCP option 12 / hostname override）。
func (m *DHCPManager) SetHostname(h string) { m.hostname = h }

// SetSendHostname 控制是否发送主机名；false 时清空 hostname（不发送 option 12）。
func (m *DHCPManager) SetSendHostname(send bool) {
	if !send {
		m.hostname = ""
	}
}

// SetDHCPIdentifier 设置 DHCPv4 client-id 来源（netplan dhcp-identifier）。
// "mac" 用接口 MAC 作为 client-id；其余/空走客户端默认（DUID）。
func (m *DHCPManager) SetDHCPIdentifier(id string) { m.dhcpIdent = strings.ToLower(id) }
func (m *DHCPManager) HasClient() (v4, v6 bool) {
	// 纯 Go 客户端始终内建可用（运行期需 CAP_NET_RAW）；外部客户端作为回退。
	return true, true
}

// RequestDHCPv4 请求 DHCPv4 地址
func (m *DHCPManager) RequestDHCPv4(ifaceName string) (*DHCPv4Lease, error) {
	return m.RequestDHCPv4WithContext(context.Background(), ifaceName)
}

func (m *DHCPManager) RequestDHCPv4WithContext(ctx context.Context, ifaceName string) (*DHCPv4Lease, error) {
	slog.Info("requesting DHCPv4", "interface", ifaceName)

	// 首选纯 Go 实现；失败时回退到外部客户端（若有）。
	lease, err := m.requestDHCPv4PureGo(ctx, ifaceName)
	if err == nil {
		return lease, nil
	}
	slog.Warn("pure-Go DHCPv4 failed; trying external client", "interface", ifaceName, "error", err)
	if m.externalV4 == "" {
		return nil, fmt.Errorf("DHCPv4 failed (pure-Go: %v; no external client available)", err)
	}
	return m.requestDHCPv4External(ctx, ifaceName)
}

// requestDHCPv4PureGo 使用纯 Go 客户端获取 DHCPv4 租约。
func (m *DHCPManager) requestDHCPv4PureGo(ctx context.Context, ifaceName string) (*DHCPv4Lease, error) {
	client, err := purego.NewDHCPv4Client(ifaceName)
	if err != nil {
		return nil, err
	}
	client.SetTimeout(m.timeout)
	client.SetRetries(m.retries)
	if m.hostname != "" {
		client.SetHostname(m.hostname)
	}
	// dhcp-identifier=mac：用接口 MAC 作为 client-id（option 61: htype=1 + MAC）。
	if m.dhcpIdent == "mac" {
		if iface, err := net.InterfaceByName(ifaceName); err == nil && len(iface.HardwareAddr) > 0 {
			client.SetClientID(append([]byte{0x01}, iface.HardwareAddr...))
		}
	}
	p, err := client.Request(ctx)
	if err != nil {
		return nil, err
	}
	return purego4ToLease(p), nil
}

// purego4ToLease 把 purego 的 DHCPv4 租约映射为本包的 DHCPv4Lease。
func purego4ToLease(p *purego.DHCPv4Lease) *DHCPv4Lease {
	return &DHCPv4Lease{
		IP:         p.IP,
		Netmask:    p.Netmask,
		Gateway:    p.Gateway,
		DNS:        p.DNS,
		Domain:     p.Domain,
		LeaseTime:  p.LeaseTime,
		ServerIP:   p.ServerIP,
		MTU:        p.MTU,
		NTPServers: p.NTPServers,
	}
}

func (m *DHCPManager) requestDHCPv4External(ctx context.Context, ifaceName string) (*DHCPv4Lease, error) {
	clientName := filepath.Base(m.externalV4)
	pidFile := fmt.Sprintf("/run/netcfg-dhcp4-%s.pid", ifaceName)
	leaseFile := fmt.Sprintf("/var/lib/netcfg/dhcp4-%s.lease", ifaceName)
	_ = os.MkdirAll("/var/lib/netcfg", 0755)

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
	// 纯 Go T1 单播续约：向原服务器 REQUEST 当前地址。
	if lease != nil && lease.ServerIP != nil {
		if client, err := purego.NewDHCPv4Client(ifaceName); err == nil {
			client.SetTimeout(m.timeout)
			if m.hostname != "" {
				client.SetHostname(m.hostname)
			}
			cur := &purego.DHCPv4Lease{
				IP:        lease.IP,
				Netmask:   lease.Netmask,
				Gateway:   lease.Gateway,
				ServerIP:  lease.ServerIP,
				LeaseTime: lease.LeaseTime,
			}
			if renewed, rerr := client.Renew(ctx, cur); rerr == nil {
				slog.Info("DHCPv4 lease renewed (T1 unicast)", "interface", ifaceName)
				return purego4ToLease(renewed), nil
			} else {
				slog.Warn("pure-Go DHCPv4 renew failed; falling back to full request", "interface", ifaceName, "error", rerr)
			}
		}
	}
	// 回退：重新完整请求（DORA）
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
		_ = exec.Command(m.externalV4, "-r", "-pf", pidFile, ifaceName).Run()
	case "dhcpcd":
		_ = exec.Command(m.externalV4, "-k", "-4", ifaceName).Run()
	}
	return nil
}

// RequestDHCPv6 请求 DHCPv6 地址
func (m *DHCPManager) RequestDHCPv6(ifaceName string, rapidCommit bool) (*DHCPv6Lease, error) {
	return m.RequestDHCPv6WithContext(context.Background(), ifaceName, rapidCommit)
}

func (m *DHCPManager) RequestDHCPv6WithContext(ctx context.Context, ifaceName string, rapidCommit bool) (*DHCPv6Lease, error) {
	slog.Info("requesting DHCPv6", "interface", ifaceName, "rapid_commit", rapidCommit)

	// 首选纯 Go 实现；失败时回退到外部客户端（若有）。
	lease, err := m.requestDHCPv6PureGo(ctx, ifaceName, rapidCommit)
	if err == nil {
		return lease, nil
	}
	slog.Warn("pure-Go DHCPv6 failed; trying external client", "interface", ifaceName, "error", err)
	if m.externalV6 == "" {
		return nil, fmt.Errorf("DHCPv6 failed (pure-Go: %v; no external client available)", err)
	}
	return m.requestDHCPv6External(ctx, ifaceName)
}

// requestDHCPv6PureGo 使用纯 Go 客户端获取 DHCPv6 租约。
func (m *DHCPManager) requestDHCPv6PureGo(ctx context.Context, ifaceName string, rapidCommit bool) (*DHCPv6Lease, error) {
	client, err := purego.NewDHCPv6Client(ifaceName)
	if err != nil {
		return nil, err
	}
	client.SetTimeout(m.timeout)
	client.SetRetries(m.retries)
	client.SetRapidCommit(rapidCommit)
	client.SetRequestNA(true)
	p, err := client.Request(ctx)
	if err != nil {
		return nil, err
	}
	return purego6ToLease(p), nil
}

// purego6ToLease 把 purego 的 DHCPv6 租约映射为本包的 DHCPv6Lease。
func purego6ToLease(p *purego.DHCPv6Lease) *DHCPv6Lease {
	return &DHCPv6Lease{
		Addresses:  p.Addresses,
		Prefixes:   p.Prefixes,
		DNS:        p.DNS,
		Domains:    p.Domains,
		LeaseTime:  p.LeaseTime,
		ServerDUID: p.ServerDUID,
	}
}

func (m *DHCPManager) requestDHCPv6External(ctx context.Context, ifaceName string) (*DHCPv6Lease, error) {
	clientName := filepath.Base(m.externalV6)
	pidFile := fmt.Sprintf("/run/netcfg-dhcp6-%s.pid", ifaceName)
	leaseFile := fmt.Sprintf("/var/lib/netcfg/dhcp6-%s.lease", ifaceName)
	_ = os.MkdirAll("/var/lib/netcfg", 0755)

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
		_ = exec.Command(m.externalV6, "-6", "-r", "-pf", pidFile, ifaceName).Run()
	case "dhcpcd":
		_ = exec.Command(m.externalV6, "-k", "-6", ifaceName).Run()
	}
	return nil
}

// EnableSLAAC 启用 IPv6 SLAAC
func EnableSLAAC(ifaceName string) error {
	slog.Info("enabling SLAAC", "interface", ifaceName)
	_ = writeSysctl(ifaceName, "accept_ra", "2")
	_ = writeSysctl(ifaceName, "autoconf", "1")
	_ = writeSysctl(ifaceName, "use_tempaddr", "2")
	return nil
}

// DisableSLAAC 禁用 IPv6 SLAAC
func DisableSLAAC(ifaceName string) error {
	_ = writeSysctl(ifaceName, "accept_ra", "0")
	_ = writeSysctl(ifaceName, "autoconf", "0")
	return nil
}

func SetAcceptRA(ifaceName string, value int) error {
	return writeSysctl(ifaceName, "accept_ra", fmt.Sprintf("%d", value))
}

func SetIPv6Privacy(ifaceName string, value int) error {
	return writeSysctl(ifaceName, "use_tempaddr", fmt.Sprintf("%d", value))
}

// SetIPv6MTU 单独设置接口的 IPv6 MTU（sysctl，不影响设备整体 MTU）。
func SetIPv6MTU(ifaceName string, mtu int) error {
	return writeSysctl(ifaceName, "mtu", fmt.Sprintf("%d", mtu))
}

// SetLinkLocalIPv6 控制接口是否生成 IPv6 链路本地地址（通过 addr_gen_mode）。
// enable=true -> addr_gen_mode=0（EUI64 生成，netplan 默认）；false -> 1（none）。
// 注意：addr_gen_mode 需在 LL 地址生成前设置才彻底生效，对已存在 LL 地址的接口
// 不会移除既有地址。
func SetLinkLocalIPv6(ifaceName string, enable bool) error {
	val := "1" // none
	if enable {
		val = "0" // eui64
	}
	return writeSysctl(ifaceName, "addr_gen_mode", val)
}

// ApplyDHCPv4Lease 应用 DHCPv4 租约
// DHCPOverrides 控制应用 DHCP 租约时的行为（对应 netplan dhcp4/6-overrides 中
// 可在「直接 netlink」下生效的子集）。零值结构表示全部启用（netplan 默认）。
type DHCPOverrides struct {
	UseDNS      bool // 应用 lease 的 DNS
	UseMTU      bool // 应用 lease 的 MTU
	UseRoutes   bool // 安装 lease 的网关/路由
	RouteMetric int  // 自动添加路由的 metric（0 = 不设）
	UseDomains  bool // 把 lease 的 domain 作为 DNS search 域
}

// defaultDHCPOverrides 返回 netplan 默认（除 use-domains 外均启用）。
func defaultDHCPOverrides() *DHCPOverrides {
	return &DHCPOverrides{UseDNS: true, UseMTU: true, UseRoutes: true, UseDomains: false}
}

func (m *DHCPManager) ApplyDHCPv4Lease(ifaceName string, lease *DHCPv4Lease, ov *DHCPOverrides) error {
	if ov == nil {
		ov = defaultDHCPOverrides()
	}

	link, err := netlink.LinkByName(ifaceName)
	if err != nil {
		return err
	}
	addrs, _ := netlink.AddrList(link, netlink.FAMILY_V4)
	for _, a := range addrs {
		_ = netlink.AddrDel(link, &a)
	}
	ones, _ := lease.Netmask.Size()
	addr := &netlink.Addr{IPNet: &net.IPNet{IP: lease.IP, Mask: lease.Netmask}}
	if err := netlink.AddrAdd(link, addr); err != nil {
		return fmt.Errorf("add addr %s/%d: %w", lease.IP, ones, err)
	}
	if lease.Gateway != nil && ov.UseRoutes {
		route := &netlink.Route{LinkIndex: link.Attrs().Index, Gw: lease.Gateway}
		if ov.RouteMetric > 0 {
			route.Priority = ov.RouteMetric
		}
		_ = netlink.RouteAdd(route)
	}
	if lease.MTU > 0 && ov.UseMTU {
		_ = netlink.LinkSetMTU(link, lease.MTU)
	}
	if len(lease.DNS) > 0 && ov.UseDNS {
		domain := lease.Domain
		if !ov.UseDomains {
			domain = ""
		}
		_ = UpdateResolvConf(lease.DNS, domain)
	}
	slog.Info("DHCPv4 lease applied", "interface", ifaceName, "ip", lease.IP)
	return nil
}

// ApplyDHCPv6Lease 应用 DHCPv6 租约。与 v4 不同：
//   - 逐个添加 IA_NA 地址（/128），不 flush 现有地址（v6 的 link-local/SLAAC 须保留）
//   - DHCPv6 不下发网关/MTU（由 RA 提供），故 use-routes/use-mtu/route-metric 对 v6
//     天然不适用，仅 honor use-dns / use-domains
//   - IA_PD 委派前缀仅记录，不自动分配（需指定下游接口，超出本函数职责）
func (m *DHCPManager) ApplyDHCPv6Lease(ifaceName string, lease *DHCPv6Lease, ov *DHCPOverrides) error {
	if ov == nil {
		ov = defaultDHCPOverrides()
	}

	link, err := netlink.LinkByName(ifaceName)
	if err != nil {
		return err
	}

	for _, ip := range lease.Addresses {
		addr := &netlink.Addr{IPNet: &net.IPNet{IP: ip, Mask: net.CIDRMask(128, 128)}}
		if err := netlink.AddrAdd(link, addr); err != nil {
			// 续约时地址通常已存在（EEXIST），非致命
			slog.Warn("failed to add DHCPv6 address", "interface", ifaceName, "ip", ip, "error", err)
		}
	}

	for _, p := range lease.Prefixes {
		slog.Info("DHCPv6 delegated prefix received (not auto-assigned; configure downstream manually)",
			"interface", ifaceName, "prefix", p.String())
	}

	if len(lease.DNS) > 0 && ov.UseDNS {
		domain := ""
		if ov.UseDomains && len(lease.Domains) > 0 {
			domain = lease.Domains[0]
		}
		_ = UpdateResolvConf(lease.DNS, domain)
	}

	slog.Info("DHCPv6 lease applied", "interface", ifaceName, "addresses", lease.Addresses)
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
			_, _ = fmt.Sscanf(fields[2], "%02x%02x%02x%02x", &gw[3], &gw[2], &gw[1], &gw[0])
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

// usingSystemdResolved 判断系统的 /etc/resolv.conf 是否由 systemd-resolved 管理。
func usingSystemdResolved() bool {
	_, err := os.Stat("/run/systemd/resolve/stub-resolv.conf")
	return err == nil
}

// ApplyDNS 为指定接口配置静态 DNS（nameserver 地址 + search 域）。
//
// 当系统由 systemd-resolved 管理时，通过 resolvectl 按接口下发（per-link DNS，
// 与 netplan 行为一致）；否则回退写入 /etc/resolv.conf。
// 注意：resolv.conf 回退是全局的，多个接口各自配置 DNS 时后者会覆盖前者。
func ApplyDNS(ifaceName string, addresses, search []string) error {
	if len(addresses) == 0 && len(search) == 0 {
		return nil
	}

	if usingSystemdResolved() {
		return applyDNSResolvectl(ifaceName, addresses, search)
	}
	return writeResolvConf(addresses, search)
}

func applyDNSResolvectl(ifaceName string, addresses, search []string) error {
	bin, err := exec.LookPath("resolvectl")
	if err != nil {
		// systemd-resolved 在运行但找不到 resolvectl：退回写 resolv.conf 并告警，
		// 避免静默丢弃 DNS 配置。
		slog.Warn("systemd-resolved detected but resolvectl not found; falling back to /etc/resolv.conf",
			"interface", ifaceName)
		return writeResolvConf(addresses, search)
	}

	if len(addresses) > 0 {
		args := append([]string{"dns", ifaceName}, addresses...)
		if out, err := exec.Command(bin, args...).CombinedOutput(); err != nil {
			return fmt.Errorf("resolvectl dns %s: %w (%s)", ifaceName, err, strings.TrimSpace(string(out)))
		}
	}
	if len(search) > 0 {
		args := append([]string{"domain", ifaceName}, search...)
		if out, err := exec.Command(bin, args...).CombinedOutput(); err != nil {
			return fmt.Errorf("resolvectl domain %s: %w (%s)", ifaceName, err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

func writeResolvConf(addresses, search []string) error {
	var b strings.Builder
	b.WriteString("# Generated by netcfg\n")
	if len(search) > 0 {
		b.WriteString("search " + strings.Join(search, " ") + "\n")
	}
	for _, a := range addresses {
		b.WriteString("nameserver " + a + "\n")
	}
	return os.WriteFile("/etc/resolv.conf", []byte(b.String()), 0644)
}

func HasDHCPClient() (v4, v6 bool) { m := NewDHCPManager(); return m.HasClient() }

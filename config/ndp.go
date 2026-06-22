/*
Copyright © 2024 netcfg authors

ndp-proxy 块校验：addresses（内核 proxy_ndp，IPv6）+ rules（响应器，前缀 CIDR + 可选 MAC）。
见 docs/ndp-responder-design.md。
*/

package config

import (
	"fmt"
	"net"
)

// validateNDProxyBlock 校验单个设备的 ndp-proxy 块。
func validateNDProxyBlock(scope string, n *NDProxy) error {
	if n == nil {
		return nil
	}
	for _, a := range n.Addresses {
		if !isIPv6(a) {
			return fmt.Errorf("%s: ndp-proxy address %q is not a valid IPv6 address", scope, a)
		}
	}
	for _, r := range n.Rules {
		ip, _, err := net.ParseCIDR(r.Prefix)
		if err != nil || ip.To4() != nil {
			return fmt.Errorf("%s: ndp-proxy rule prefix %q is not a valid IPv6 CIDR", scope, r.Prefix)
		}
		if r.Neighbor != "" {
			if _, err := net.ParseMAC(r.Neighbor); err != nil {
				return fmt.Errorf("%s: ndp-proxy rule neighbor %q is not a valid MAC: %w", scope, r.Neighbor, err)
			}
		}
	}
	return nil
}

// ndProxyDevices 校验一组设备（ethernet/vlan/bridge/bond）的 ndp-proxy 块。
func ndProxyDevices(eth map[string]*Ethernet, vl map[string]*Vlan, br map[string]*Bridge, bo map[string]*Bond, scope string) error {
	for n, d := range eth {
		if err := validateNDProxyBlock(scope+" ethernet "+n, d.NDProxy); err != nil {
			return err
		}
	}
	for n, d := range vl {
		if err := validateNDProxyBlock(scope+" vlan "+n, d.NDProxy); err != nil {
			return err
		}
	}
	for n, d := range br {
		if err := validateNDProxyBlock(scope+" bridge "+n, d.NDProxy); err != nil {
			return err
		}
	}
	for n, d := range bo {
		if err := validateNDProxyBlock(scope+" bond "+n, d.NDProxy); err != nil {
			return err
		}
	}
	return nil
}

// ValidateNDProxy 校验整份配置的 ndp-proxy 块（顶层 default + 各 netns）。
func ValidateNDProxy(cfg *Config) error {
	n := &cfg.Network
	if err := ndProxyDevices(n.Ethernets, n.Vlans, n.Bridges, n.Bonds, "ndp-proxy"); err != nil {
		return err
	}
	for name, ns := range n.Netns {
		if ns == nil {
			continue
		}
		if err := ndProxyDevices(ns.Ethernets, ns.Vlans, ns.Bridges, ns.Bonds, "netns "+name); err != nil {
			return err
		}
	}
	return nil
}

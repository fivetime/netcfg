/*
Copyright © 2024 netcfg authors

SRv6 (seg6) 配置校验。SRv6 是 netcfg 扩展（非 netplan 标准），见 docs/srv6-design.md。
*/

package config

import (
	"fmt"
	"net"
	"strings"
)

// srv6Actions 是支持的 seg6local endpoint 行为及其必填字段需求。
// 值为该 action 必填的字段集合（"table"/"vrf-table"/"nh4"/"nh6"/"oif"/"segments"）。
var srv6Actions = map[string][]string{
	"End":           {},
	"End.X":         {"nh6"},
	"End.T":         {"table"},
	"End.DX2":       {"oif"},
	"End.DX4":       {"nh4"},
	"End.DX6":       {"nh6"},
	"End.DT4":       {"vrf-table"},
	"End.DT6":       {"table"},
	"End.DT46":      {"vrf-table"},
	"End.B6":        {"segments"},
	"End.B6.Encaps": {"segments"},
}

// isIPv6 / isIPv4 判断地址族（地址本身需合法）。
func isIPv6(s string) bool { ip := net.ParseIP(s); return ip != nil && ip.To4() == nil }
func isIPv4(s string) bool { ip := net.ParseIP(s); return ip != nil && ip.To4() != nil }

// ValidateRouteEncap 校验路由封装（SRv6 transit）。供 LoadConfig 与应用层共用。
func ValidateRouteEncap(e *RouteEncap) error {
	if e == nil {
		return nil
	}
	if !strings.EqualFold(e.Type, "seg6") {
		return fmt.Errorf("route encap type %q unsupported (only 'seg6')", e.Type)
	}
	switch strings.ToLower(e.Mode) {
	case "", "encap", "inline":
	default:
		return fmt.Errorf("seg6 encap mode %q invalid (encap|inline)", e.Mode)
	}
	if len(e.Segments) == 0 {
		return fmt.Errorf("seg6 encap requires at least one segment")
	}
	for _, s := range e.Segments {
		if !isIPv6(s) {
			return fmt.Errorf("seg6 segment %q is not a valid IPv6 address", s)
		}
	}
	return nil
}

// validateLocalSID 校验一条本地 SID。
func validateLocalSID(s *SRv6LocalSID) error {
	if s.SID == "" {
		return fmt.Errorf("srv6 local-sid missing sid")
	}
	sid := s.SID
	if i := strings.IndexByte(sid, '/'); i >= 0 {
		sid = sid[:i] // 允许显式 /128
	}
	if !isIPv6(sid) {
		return fmt.Errorf("srv6 local-sid sid %q is not a valid IPv6 address", s.SID)
	}
	req, ok := srv6Actions[s.Action]
	if !ok {
		return fmt.Errorf("srv6 local-sid %s: unknown action %q", s.SID, s.Action)
	}
	for _, field := range req {
		switch field {
		case "table":
			if s.Table <= 0 {
				return fmt.Errorf("srv6 %s (%s) requires table > 0", s.SID, s.Action)
			}
		case "vrf-table":
			if s.VRFTable <= 0 {
				return fmt.Errorf("srv6 %s (%s) requires vrf-table > 0", s.SID, s.Action)
			}
		case "nh4":
			if !isIPv4(s.NH4) {
				return fmt.Errorf("srv6 %s (%s) requires nh4 to be a valid IPv4 address", s.SID, s.Action)
			}
		case "nh6":
			if !isIPv6(s.NH6) {
				return fmt.Errorf("srv6 %s (%s) requires nh6 to be a valid IPv6 address", s.SID, s.Action)
			}
		case "oif":
			if s.OIF == "" {
				return fmt.Errorf("srv6 %s (%s) requires oif", s.SID, s.Action)
			}
		case "segments":
			if len(s.Segments) == 0 {
				return fmt.Errorf("srv6 %s (%s) requires segments", s.SID, s.Action)
			}
		}
	}
	// 已填的可选地址也校验族
	if s.NH4 != "" && !isIPv4(s.NH4) {
		return fmt.Errorf("srv6 %s: nh4 %q is not a valid IPv4 address", s.SID, s.NH4)
	}
	if s.NH6 != "" && !isIPv6(s.NH6) {
		return fmt.Errorf("srv6 %s: nh6 %q is not a valid IPv6 address", s.SID, s.NH6)
	}
	for _, seg := range s.Segments {
		if !isIPv6(seg) {
			return fmt.Errorf("srv6 %s: segment %q is not a valid IPv6 address", s.SID, seg)
		}
	}
	return nil
}

// validateSRv6Config 校验一个 srv6 段（顶层或某 netns）。
func validateSRv6Config(c *SRv6Config, scope string) error {
	if c == nil {
		return nil
	}
	seen := map[string]bool{}
	for _, s := range c.LocalSIDs {
		if err := validateLocalSID(s); err != nil {
			return fmt.Errorf("%s: %w", scope, err)
		}
		key := strings.SplitN(s.SID, "/", 2)[0]
		if seen[key] {
			return fmt.Errorf("%s: duplicate srv6 local-sid %s", scope, s.SID)
		}
		seen[key] = true
	}
	return nil
}

// ValidateSRv6 校验整份配置的 SRv6 段（顶层 default + 各 netns）。
func ValidateSRv6(cfg *Config) error {
	if err := validateSRv6Config(cfg.Network.SRv6, "srv6"); err != nil {
		return err
	}
	for name, ns := range cfg.Network.Netns {
		if ns == nil {
			continue
		}
		if err := validateSRv6Config(ns.SRv6, "netns "+name+" srv6"); err != nil {
			return err
		}
	}
	return nil
}

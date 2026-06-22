/*
Copyright © 2024 netcfg authors

VPP 后端状态：记录 netcfg 在 VPP 上创建的设备，供增量 apply 时回收孤儿（配置里
删掉的设备 → 从 VPP 移除）。持久化到 /var/lib/netcfg/vpp-state.json。
*/

package vpp

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
)

const stateFile = "/var/lib/netcfg/vpp-state.json"

// DevInfo 记录删除一个 VPP 设备所需的信息。
type DevInfo struct {
	Type   string `json:"type"` // af-packet|loopback|dpdk|avf|bond|vlan|vxlan|bridge
	HostIf string `json:"host_if,omitempty"`
	BdID   uint32 `json:"bd_id,omitempty"`
	Vni    uint32 `json:"vni,omitempty"`
	Local  string `json:"local,omitempty"`
	Remote string `json:"remote,omitempty"`
	Port   int    `json:"port,omitempty"`
}

// NatItem 记录一条 NAT 规则（用于增量回收）。Kind 决定其余字段含义：
// nat44-if/nat64-if（Iface,Role）、nat44-pool/nat64-pool（Start,End,VRF,TwiceNat）、
// nat44-static（Proto,Local,LocalPort,External,ExternalIf,ExternalPort,VRF,TwiceNat）、
// nat64-prefix（Prefix,VRF）、nat66-static（Local,External,VRF）。
type NatItem struct {
	Kind         string `json:"kind"`
	Iface        string `json:"iface,omitempty"`
	Role         string `json:"role,omitempty"`
	Proto        string `json:"proto,omitempty"`
	Local        string `json:"local,omitempty"`
	LocalPort    int    `json:"local_port,omitempty"`
	External     string `json:"external,omitempty"`
	ExternalIf   string `json:"external_if,omitempty"`
	ExternalPort int    `json:"external_port,omitempty"`
	Start        string `json:"start,omitempty"`
	End          string `json:"end,omitempty"`
	Prefix       string `json:"prefix,omitempty"`
	VRF          int    `json:"vrf,omitempty"`
	TwiceNat     bool   `json:"twice_nat,omitempty"`
}

// Key 返回 NatItem 的规范化标识（用于 diff 去重）。
func (i NatItem) Key() string {
	return fmt.Sprintf("%s|%s|%s|%s|%s|%d|%s|%s|%d|%s|%s|%d|%t",
		i.Kind, i.Iface, i.Role, i.Proto, i.Local, i.LocalPort,
		i.External, i.ExternalIf, i.ExternalPort, i.Start, i.End, i.VRF, i.TwiceNat)
}

// State 是 netcfg 在 VPP 上创建的设备集合（name → DevInfo）+ NAT 规则 + NDP 代理。
type State struct {
	Devices map[string]DevInfo `json:"devices"`
	Nat     []NatItem          `json:"nat,omitempty"`
}

// NewState 返回空状态。
func NewState() *State { return &State{Devices: map[string]DevInfo{}} }

// LoadState 读取持久化的 VPP 状态（不存在时返回空状态）。
func LoadState() *State {
	s := NewState()
	data, err := os.ReadFile(stateFile)
	if err != nil {
		return s
	}
	_ = json.Unmarshal(data, s)
	if s.Devices == nil {
		s.Devices = map[string]DevInfo{}
	}
	return s
}

// Save 持久化 VPP 状态。
func (s *State) Save() error {
	if err := os.MkdirAll(filepath.Dir(stateFile), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(stateFile, data, 0644)
}

// AutoBdID 从 bridge 名派生确定性 bridge-domain id（与 applier 一致）。
func AutoBdID(name string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(name))
	return h.Sum32()%16_000_000 + 1
}

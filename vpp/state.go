/*
Copyright © 2024 netcfg authors

VPP 后端状态：记录 netcfg 在 VPP 上创建的设备，供增量 apply 时回收孤儿（配置里
删掉的设备 → 从 VPP 移除）。持久化到 /var/lib/netcfg/vpp-state.json。
*/

package vpp

import (
	"encoding/json"
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

// State 是 netcfg 在 VPP 上创建的设备集合（name → DevInfo）。
type State struct {
	Devices map[string]DevInfo `json:"devices"`
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

/*
Copyright © 2024 netcfg authors

State management - tracks applied configuration for incremental updates.
*/

package state

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	stateDir  = "/var/lib/netcfg"
	stateFile = "state.json"
)

// State 网络配置状态
type State struct {
	Version    int                 `json:"version"`
	AppliedAt  time.Time           `json:"applied_at"`
	ConfigHash string              `json:"config_hash,omitempty"`
	Namespaces map[string]*NsState `json:"namespaces"` // "" = default ns
	mu         sync.RWMutex
}

// NsState 单个 namespace 的状态
type NsState struct {
	Devices  map[string]*DeviceState `json:"devices"`
	SRv6SIDs []string                `json:"srv6_sids,omitempty"` // 本 ns 下发的 SRv6 本地 SID（增量回收用）
}

// DeviceState 设备状态
type DeviceState struct {
	Type      string   `json:"type"` // ethernet, bridge, vxlan, etc.
	Addresses []string `json:"addresses,omitempty"`
	Routes    []string `json:"routes,omitempty"`     // "to via dev" 格式
	Master    string   `json:"master,omitempty"`     // bridge/bond/vrf
	CreatedBy string   `json:"created_by,omitempty"` // "netcfg" or "system"
}

// NewState 创建新状态
func NewState() *State {
	return &State{
		Version:    1,
		Namespaces: make(map[string]*NsState),
	}
}

// Load 从文件加载状态
func Load() (*State, error) {
	path := filepath.Join(stateDir, stateFile)

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return NewState(), nil
		}
		return nil, err
	}

	state := &State{}
	if err := json.Unmarshal(data, state); err != nil {
		slog.Warn("failed to parse state file, starting fresh", "error", err)
		return NewState(), nil
	}

	if state.Namespaces == nil {
		state.Namespaces = make(map[string]*NsState)
	}

	return state, nil
}

// Save 保存状态到文件
func (s *State) Save() error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return err
	}

	s.AppliedAt = time.Now()

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}

	path := filepath.Join(stateDir, stateFile)
	return os.WriteFile(path, data, 0644)
}

// GetNamespace 获取 namespace 状态
func (s *State) GetNamespace(ns string) *NsState {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if nsState, ok := s.Namespaces[ns]; ok {
		return nsState
	}
	return nil
}

// SetNamespace 设置 namespace 状态
func (s *State) SetNamespace(ns string, nsState *NsState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Namespaces[ns] = nsState
}

// GetDevice 获取设备状态
func (s *State) GetDevice(ns, name string) *DeviceState {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if nsState, ok := s.Namespaces[ns]; ok {
		if dev, ok := nsState.Devices[name]; ok {
			return dev
		}
	}
	return nil
}

// SetDevice 设置设备状态
func (s *State) SetDevice(ns, name string, dev *DeviceState) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.Namespaces[ns]; !ok {
		s.Namespaces[ns] = &NsState{
			Devices: make(map[string]*DeviceState),
		}
	}
	s.Namespaces[ns].Devices[name] = dev
}

// RemoveDevice 删除设备状态
func (s *State) RemoveDevice(ns, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if nsState, ok := s.Namespaces[ns]; ok {
		delete(nsState.Devices, name)
	}
}

// ListDevices 列出 namespace 中所有设备
func (s *State) ListDevices(ns string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var devices []string
	if nsState, ok := s.Namespaces[ns]; ok {
		for name := range nsState.Devices {
			devices = append(devices, name)
		}
	}
	return devices
}

// ListNamespaces 列出所有 namespace
func (s *State) ListNamespaces() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var namespaces []string
	for ns := range s.Namespaces {
		namespaces = append(namespaces, ns)
	}
	return namespaces
}

// Diff 计算状态差异
type Diff struct {
	// 需要删除的（旧配置有，新配置没有）
	DevicesToRemove   map[string][]string            // ns -> []device
	AddressesToRemove map[string]map[string][]string // ns -> device -> []addr
	RoutesToRemove    map[string]map[string][]string // ns -> device -> []route
	NsToRemove        []string

	// 需要添加的（新配置有，旧配置没有）
	DevicesToAdd   map[string][]string
	AddressesToAdd map[string]map[string][]string
	RoutesToAdd    map[string]map[string][]string
	NsToAdd        []string
}

// NewDiff 创建空差异
func NewDiff() *Diff {
	return &Diff{
		DevicesToRemove:   make(map[string][]string),
		AddressesToRemove: make(map[string]map[string][]string),
		RoutesToRemove:    make(map[string]map[string][]string),
		DevicesToAdd:      make(map[string][]string),
		AddressesToAdd:    make(map[string]map[string][]string),
		RoutesToAdd:       make(map[string]map[string][]string),
	}
}

// ComputeDiff 计算新旧状态的差异
func ComputeDiff(oldState, newState *State) *Diff {
	diff := NewDiff()

	// 检查需要删除的 namespace
	for ns := range oldState.Namespaces {
		if _, ok := newState.Namespaces[ns]; !ok {
			diff.NsToRemove = append(diff.NsToRemove, ns)
		}
	}

	// 检查需要添加的 namespace
	for ns := range newState.Namespaces {
		if _, ok := oldState.Namespaces[ns]; !ok {
			diff.NsToAdd = append(diff.NsToAdd, ns)
		}
	}

	// 对每个 namespace 检查设备差异
	for ns, oldNs := range oldState.Namespaces {
		newNs, nsExists := newState.Namespaces[ns]

		for devName, oldDev := range oldNs.Devices {
			if !nsExists {
				// 整个 ns 被删除
				diff.DevicesToRemove[ns] = append(diff.DevicesToRemove[ns], devName)
				continue
			}

			newDev, devExists := newNs.Devices[devName]
			if !devExists {
				// 设备被删除
				if oldDev.CreatedBy == "netcfg" {
					diff.DevicesToRemove[ns] = append(diff.DevicesToRemove[ns], devName)
				}
				continue
			}

			// 检查地址差异
			addrDiff := diffStrings(oldDev.Addresses, newDev.Addresses)
			if len(addrDiff.removed) > 0 {
				if diff.AddressesToRemove[ns] == nil {
					diff.AddressesToRemove[ns] = make(map[string][]string)
				}
				diff.AddressesToRemove[ns][devName] = addrDiff.removed
			}
			if len(addrDiff.added) > 0 {
				if diff.AddressesToAdd[ns] == nil {
					diff.AddressesToAdd[ns] = make(map[string][]string)
				}
				diff.AddressesToAdd[ns][devName] = addrDiff.added
			}

			// 检查路由差异
			routeDiff := diffStrings(oldDev.Routes, newDev.Routes)
			if len(routeDiff.removed) > 0 {
				if diff.RoutesToRemove[ns] == nil {
					diff.RoutesToRemove[ns] = make(map[string][]string)
				}
				diff.RoutesToRemove[ns][devName] = routeDiff.removed
			}
			if len(routeDiff.added) > 0 {
				if diff.RoutesToAdd[ns] == nil {
					diff.RoutesToAdd[ns] = make(map[string][]string)
				}
				diff.RoutesToAdd[ns][devName] = routeDiff.added
			}
		}
	}

	// 检查新增设备
	for ns, newNs := range newState.Namespaces {
		oldNs, nsExists := oldState.Namespaces[ns]

		for devName := range newNs.Devices {
			if !nsExists || oldNs.Devices[devName] == nil {
				diff.DevicesToAdd[ns] = append(diff.DevicesToAdd[ns], devName)
			}
		}
	}

	return diff
}

type stringDiff struct {
	added   []string
	removed []string
}

func diffStrings(old, new []string) stringDiff {
	oldSet := make(map[string]bool)
	newSet := make(map[string]bool)

	for _, s := range old {
		oldSet[s] = true
	}
	for _, s := range new {
		newSet[s] = true
	}

	var diff stringDiff

	// 找出被删除的
	for s := range oldSet {
		if !newSet[s] {
			diff.removed = append(diff.removed, s)
		}
	}

	// 找出新增的
	for s := range newSet {
		if !oldSet[s] {
			diff.added = append(diff.added, s)
		}
	}

	return diff
}

// IsEmpty 检查差异是否为空
func (d *Diff) IsEmpty() bool {
	return len(d.DevicesToRemove) == 0 &&
		len(d.AddressesToRemove) == 0 &&
		len(d.RoutesToRemove) == 0 &&
		len(d.NsToRemove) == 0 &&
		len(d.DevicesToAdd) == 0 &&
		len(d.AddressesToAdd) == 0 &&
		len(d.RoutesToAdd) == 0 &&
		len(d.NsToAdd) == 0
}

// Summary 返回差异摘要
func (d *Diff) Summary() string {
	var result string

	if len(d.NsToRemove) > 0 {
		result += "Namespaces to remove: " + join(d.NsToRemove) + "\n"
	}
	if len(d.NsToAdd) > 0 {
		result += "Namespaces to add: " + join(d.NsToAdd) + "\n"
	}

	for ns, devs := range d.DevicesToRemove {
		nsLabel := ns
		if nsLabel == "" {
			nsLabel = "default"
		}
		result += "Devices to remove in " + nsLabel + ": " + join(devs) + "\n"
	}

	for ns, devs := range d.DevicesToAdd {
		nsLabel := ns
		if nsLabel == "" {
			nsLabel = "default"
		}
		result += "Devices to add in " + nsLabel + ": " + join(devs) + "\n"
	}

	for ns, devAddrs := range d.AddressesToRemove {
		nsLabel := ns
		if nsLabel == "" {
			nsLabel = "default"
		}
		for dev, addrs := range devAddrs {
			result += "Addresses to remove from " + dev + " in " + nsLabel + ": " + join(addrs) + "\n"
		}
	}

	for ns, devRoutes := range d.RoutesToRemove {
		nsLabel := ns
		if nsLabel == "" {
			nsLabel = "default"
		}
		for dev, routes := range devRoutes {
			result += "Routes to remove from " + dev + " in " + nsLabel + ": " + join(routes) + "\n"
		}
	}

	return result
}

func join(s []string) string {
	result := ""
	for i, v := range s {
		if i > 0 {
			result += ", "
		}
		result += v
	}
	return result
}

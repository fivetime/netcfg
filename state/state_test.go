/*
Copyright © 2024 netcfg authors

Unit tests for state tracking and diff computation.
*/

package state

import (
	"sort"
	"strings"
	"testing"
)

// nsWithDevices 构造一个含指定设备的 NsState（用于测试）。
func nsWithDevices(devs map[string]*DeviceState) *NsState {
	if devs == nil {
		devs = map[string]*DeviceState{}
	}
	return &NsState{Devices: devs}
}

func TestNewState(t *testing.T) {
	s := NewState()
	if s.Version != 1 {
		t.Errorf("version = %d, want 1", s.Version)
	}
	if s.Namespaces == nil {
		t.Error("Namespaces should be initialized")
	}
}

func TestSetGetRemoveDevice(t *testing.T) {
	s := NewState()
	dev := &DeviceState{Type: "bridge", CreatedBy: "netcfg"}
	s.SetDevice("", "br0", dev)

	if got := s.GetDevice("", "br0"); got != dev {
		t.Fatalf("GetDevice = %v, want %v", got, dev)
	}
	// SetDevice 应自动创建 namespace
	if s.GetNamespace("") == nil {
		t.Error("SetDevice should auto-create namespace")
	}

	if devs := s.ListDevices(""); len(devs) != 1 || devs[0] != "br0" {
		t.Errorf("ListDevices = %v, want [br0]", devs)
	}

	s.RemoveDevice("", "br0")
	if got := s.GetDevice("", "br0"); got != nil {
		t.Errorf("after remove, GetDevice = %v, want nil", got)
	}
}

func TestGetDeviceMissing(t *testing.T) {
	s := NewState()
	if got := s.GetDevice("nope", "nope"); got != nil {
		t.Errorf("GetDevice on empty state = %v, want nil", got)
	}
}

func TestListNamespaces(t *testing.T) {
	s := NewState()
	s.SetDevice("", "eth0", &DeviceState{Type: "ethernet"})
	s.SetDevice("vpn", "veth0", &DeviceState{Type: "veth"})

	got := s.ListNamespaces()
	sort.Strings(got)
	want := []string{"", "vpn"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("ListNamespaces = %v, want %v", got, want)
	}
}

func TestDiffStrings(t *testing.T) {
	d := diffStrings(
		[]string{"a", "b", "c"},
		[]string{"b", "c", "d"},
	)
	sort.Strings(d.added)
	sort.Strings(d.removed)
	if strings.Join(d.added, ",") != "d" {
		t.Errorf("added = %v, want [d]", d.added)
	}
	if strings.Join(d.removed, ",") != "a" {
		t.Errorf("removed = %v, want [a]", d.removed)
	}
}

func TestDiffStringsIdentical(t *testing.T) {
	d := diffStrings([]string{"x", "y"}, []string{"y", "x"})
	if len(d.added) != 0 || len(d.removed) != 0 {
		t.Errorf("identical sets should yield no diff, got added=%v removed=%v", d.added, d.removed)
	}
}

func TestComputeDiffEmpty(t *testing.T) {
	if d := ComputeDiff(NewState(), NewState()); !d.IsEmpty() {
		t.Errorf("diff of two empty states should be empty, got %s", d.Summary())
	}
}

func TestComputeDiffNamespaceAddRemove(t *testing.T) {
	old := NewState()
	old.SetDevice("gone", "x", &DeviceState{Type: "dummy", CreatedBy: "netcfg"})

	newState := NewState()
	newState.SetDevice("fresh", "y", &DeviceState{Type: "dummy", CreatedBy: "netcfg"})

	d := ComputeDiff(old, newState)

	if !contains(d.NsToRemove, "gone") {
		t.Errorf("NsToRemove = %v, want to contain 'gone'", d.NsToRemove)
	}
	if !contains(d.NsToAdd, "fresh") {
		t.Errorf("NsToAdd = %v, want to contain 'fresh'", d.NsToAdd)
	}
}

// TestComputeDiffDeviceRemovalRespectsCreatedBy 是核心行为：
// netcfg 创建的设备删除时进入 DevicesToRemove；system 设备（物理网卡）不删除。
func TestComputeDiffDeviceRemovalRespectsCreatedBy(t *testing.T) {
	old := NewState()
	old.SetDevice("", "eth0", &DeviceState{Type: "ethernet", CreatedBy: "system"})
	old.SetDevice("", "br0", &DeviceState{Type: "bridge", CreatedBy: "netcfg"})

	// 新状态保留 default ns（存在）但移除两个设备
	newState := NewState()
	newState.SetNamespace("", nsWithDevices(nil))

	d := ComputeDiff(old, newState)

	rm := d.DevicesToRemove[""]
	if contains(rm, "eth0") {
		t.Errorf("system device eth0 should NOT be removed, got %v", rm)
	}
	if !contains(rm, "br0") {
		t.Errorf("netcfg device br0 should be removed, got %v", rm)
	}
}

func TestComputeDiffDeviceAdd(t *testing.T) {
	old := NewState()
	old.SetNamespace("", nsWithDevices(nil))

	newState := NewState()
	newState.SetDevice("", "vxlan100", &DeviceState{Type: "vxlan", CreatedBy: "netcfg"})

	d := ComputeDiff(old, newState)
	if !contains(d.DevicesToAdd[""], "vxlan100") {
		t.Errorf("DevicesToAdd = %v, want to contain vxlan100", d.DevicesToAdd[""])
	}
}

func TestComputeDiffAddressAndRoute(t *testing.T) {
	old := NewState()
	old.SetDevice("", "eth0", &DeviceState{
		Type:      "ethernet",
		CreatedBy: "system",
		Addresses: []string{"192.168.1.10/24", "10.0.0.1/24"},
		Routes:    []string{"0.0.0.0/0 via 192.168.1.1"},
	})

	newState := NewState()
	newState.SetDevice("", "eth0", &DeviceState{
		Type:      "ethernet",
		CreatedBy: "system",
		Addresses: []string{"192.168.1.10/24", "172.16.0.1/24"}, // 删 10.0.0.1，加 172.16.0.1
		Routes:    []string{"10.10.0.0/16 via 192.168.1.254"},   // 路由全换
	})

	d := ComputeDiff(old, newState)

	if got := d.AddressesToRemove[""]["eth0"]; !contains(got, "10.0.0.1/24") {
		t.Errorf("AddressesToRemove = %v, want to contain 10.0.0.1/24", got)
	}
	if got := d.AddressesToAdd[""]["eth0"]; !contains(got, "172.16.0.1/24") {
		t.Errorf("AddressesToAdd = %v, want to contain 172.16.0.1/24", got)
	}
	if got := d.RoutesToRemove[""]["eth0"]; !contains(got, "0.0.0.0/0 via 192.168.1.1") {
		t.Errorf("RoutesToRemove = %v, want old route", got)
	}
	if got := d.RoutesToAdd[""]["eth0"]; !contains(got, "10.10.0.0/16 via 192.168.1.254") {
		t.Errorf("RoutesToAdd = %v, want new route", got)
	}
	if d.IsEmpty() {
		t.Error("diff with address/route changes should not be empty")
	}
}

func TestComputeDiffNoChange(t *testing.T) {
	mk := func() *State {
		s := NewState()
		s.SetDevice("", "eth0", &DeviceState{
			Type:      "ethernet",
			CreatedBy: "system",
			Addresses: []string{"192.168.1.10/24"},
		})
		return s
	}
	d := ComputeDiff(mk(), mk())
	if !d.IsEmpty() {
		t.Errorf("identical states should produce empty diff, got %s", d.Summary())
	}
}

func TestDiffSummary(t *testing.T) {
	old := NewState()
	old.SetDevice("", "br0", &DeviceState{Type: "bridge", CreatedBy: "netcfg"})
	newState := NewState()
	newState.SetNamespace("", nsWithDevices(nil))

	s := ComputeDiff(old, newState).Summary()
	if !strings.Contains(s, "br0") {
		t.Errorf("Summary should mention removed device br0, got: %q", s)
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

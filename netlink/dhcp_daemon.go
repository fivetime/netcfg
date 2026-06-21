/*
Copyright © 2024 netcfg authors

DHCP daemon - manages DHCP leases with automatic renewal.
*/

package netlink

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	leaseDir = "/var/lib/netcfg/leases"
)

// LeaseState 租约状态
type LeaseState struct {
	Interface   string         `json:"interface"`
	IPv4        *DHCPv4Lease   `json:"ipv4,omitempty"`
	IPv6        *DHCPv6Lease   `json:"ipv6,omitempty"`
	ObtainedAt  time.Time      `json:"obtained_at"`
	RenewAt     time.Time      `json:"renew_at"`
	ExpireAt    time.Time      `json:"expire_at"`
	V4Overrides *DHCPOverrides `json:"v4_overrides,omitempty"` // 续约时复用，保证 overrides 一致
}

// DHCPDaemon DHCP 守护进程
type DHCPDaemon struct {
	manager *DHCPManager
	leases  map[string]*LeaseState // interface -> lease
	mu      sync.RWMutex
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// NewDHCPDaemon 创建 DHCP 守护进程
func NewDHCPDaemon() *DHCPDaemon {
	ctx, cancel := context.WithCancel(context.Background())
	return &DHCPDaemon{
		manager: NewDHCPManager(),
		leases:  make(map[string]*LeaseState),
		ctx:     ctx,
		cancel:  cancel,
	}
}

// Start 启动守护进程
func (d *DHCPDaemon) Start() error {
	slog.Info("DHCP daemon starting")
	_ = os.MkdirAll(leaseDir, 0755)

	// 加载已有租约
	d.loadLeases()

	// 启动续期检查
	d.wg.Add(1)
	go d.renewalLoop()

	return nil
}

// Stop 停止守护进程
func (d *DHCPDaemon) Stop() {
	slog.Info("DHCP daemon stopping")
	d.cancel()
	d.wg.Wait()
	d.saveLeases()
}

// RequestLease 请求新租约
func (d *DHCPDaemon) RequestLease(ifaceName string, wantV4, wantV6 bool, v4ov, v6ov *DHCPOverrides) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	state := &LeaseState{
		Interface:   ifaceName,
		ObtainedAt:  time.Now(),
		V4Overrides: v4ov,
	}

	// DHCPv4
	if wantV4 {
		lease, err := d.manager.RequestDHCPv4(ifaceName)
		if err != nil {
			slog.Error("DHCPv4 request failed", "interface", ifaceName, "error", err)
		} else {
			state.IPv4 = lease
			// 计算续期和过期时间
			// T1 (renew) 通常是租约时间的 50%
			// T2 (rebind) 通常是租约时间的 87.5%
			if lease.LeaseTime > 0 {
				state.RenewAt = state.ObtainedAt.Add(lease.LeaseTime / 2)
				state.ExpireAt = state.ObtainedAt.Add(lease.LeaseTime)
			} else {
				// 默认 24 小时
				state.RenewAt = state.ObtainedAt.Add(12 * time.Hour)
				state.ExpireAt = state.ObtainedAt.Add(24 * time.Hour)
			}

			// 应用租约（honor dhcp4-overrides）
			if err := d.manager.ApplyDHCPv4Lease(ifaceName, lease, v4ov); err != nil {
				slog.Error("failed to apply DHCPv4 lease", "interface", ifaceName, "error", err)
			}
			slog.Info("DHCPv4 lease obtained",
				"interface", ifaceName,
				"ip", lease.IP,
				"lease_time", lease.LeaseTime,
				"renew_at", state.RenewAt)
		}
	}

	// DHCPv6
	if wantV6 {
		lease, err := d.manager.RequestDHCPv6(ifaceName, false)
		if err != nil {
			slog.Error("DHCPv6 request failed", "interface", ifaceName, "error", err)
		} else {
			state.IPv6 = lease
			// 应用租约（honor dhcp6-overrides 的 use-dns/use-domains）
			if err := d.manager.ApplyDHCPv6Lease(ifaceName, lease, v6ov); err != nil {
				slog.Error("failed to apply DHCPv6 lease", "interface", ifaceName, "error", err)
			}
			slog.Info("DHCPv6 lease obtained",
				"interface", ifaceName,
				"addresses", lease.Addresses)
		}
	}

	if state.IPv4 != nil || state.IPv6 != nil {
		d.leases[ifaceName] = state
		d.saveLease(ifaceName, state)
	}

	return nil
}

// ReleaseLease 释放租约
func (d *DHCPDaemon) ReleaseLease(ifaceName string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	state, exists := d.leases[ifaceName]
	if !exists {
		return nil
	}

	if state.IPv4 != nil {
		_ = d.manager.ReleaseDHCPv4(ifaceName)
	}
	if state.IPv6 != nil {
		_ = d.manager.ReleaseDHCPv6(ifaceName)
	}

	delete(d.leases, ifaceName)
	_ = os.Remove(filepath.Join(leaseDir, ifaceName+".json"))

	slog.Info("lease released", "interface", ifaceName)
	return nil
}

// GetLease 获取租约信息
func (d *DHCPDaemon) GetLease(ifaceName string) *LeaseState {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.leases[ifaceName]
}

// renewalLoop 续期循环
func (d *DHCPDaemon) renewalLoop() {
	defer d.wg.Done()

	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-d.ctx.Done():
			return
		case <-ticker.C:
			d.checkRenewals()
		}
	}
}

func (d *DHCPDaemon) checkRenewals() {
	d.mu.Lock()
	defer d.mu.Unlock()

	now := time.Now()

	for ifaceName, state := range d.leases {
		// 检查是否需要续期
		if state.IPv4 != nil && now.After(state.RenewAt) {
			slog.Info("renewing DHCPv4 lease", "interface", ifaceName)

			newLease, err := d.manager.RenewDHCPv4(d.ctx, ifaceName, state.IPv4)
			if err != nil {
				slog.Error("DHCPv4 renewal failed", "interface", ifaceName, "error", err)

				// 如果续期失败且接近过期，尝试重新请求
				if now.After(state.ExpireAt.Add(-5 * time.Minute)) {
					slog.Warn("lease expiring, requesting new lease", "interface", ifaceName)
					newLease, err = d.manager.RequestDHCPv4(ifaceName)
					if err != nil {
						slog.Error("DHCPv4 re-request failed", "interface", ifaceName, "error", err)
						continue
					}
				} else {
					continue
				}
			}

			// 更新租约
			state.IPv4 = newLease
			state.ObtainedAt = now
			if newLease.LeaseTime > 0 {
				state.RenewAt = now.Add(newLease.LeaseTime / 2)
				state.ExpireAt = now.Add(newLease.LeaseTime)
			}

			if err := d.manager.ApplyDHCPv4Lease(ifaceName, newLease, state.V4Overrides); err != nil {
				slog.Error("failed to apply renewed lease", "interface", ifaceName, "error", err)
			}
			d.saveLease(ifaceName, state)

			slog.Info("DHCPv4 lease renewed",
				"interface", ifaceName,
				"ip", newLease.IP,
				"next_renew", state.RenewAt)
		}
	}
}

func (d *DHCPDaemon) saveLease(ifaceName string, state *LeaseState) {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(leaseDir, ifaceName+".json"), data, 0644)
}

func (d *DHCPDaemon) loadLeases() {
	files, err := filepath.Glob(filepath.Join(leaseDir, "*.json"))
	if err != nil {
		return
	}

	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}

		var state LeaseState
		if err := json.Unmarshal(data, &state); err != nil {
			continue
		}

		// 检查租约是否过期
		if time.Now().After(state.ExpireAt) {
			slog.Info("lease expired, removing", "interface", state.Interface)
			_ = os.Remove(file)
			continue
		}

		d.leases[state.Interface] = &state
		slog.Info("loaded lease", "interface", state.Interface, "expire_at", state.ExpireAt)
	}
}

func (d *DHCPDaemon) saveLeases() {
	for ifaceName, state := range d.leases {
		d.saveLease(ifaceName, state)
	}
}

// Status 获取守护进程状态
func (d *DHCPDaemon) Status() map[string]interface{} {
	d.mu.RLock()
	defer d.mu.RUnlock()

	leaseInfo := make(map[string]interface{})
	for name, state := range d.leases {
		info := map[string]interface{}{
			"obtained_at": state.ObtainedAt,
			"renew_at":    state.RenewAt,
			"expire_at":   state.ExpireAt,
		}
		if state.IPv4 != nil {
			info["ipv4"] = state.IPv4.IP.String()
		}
		if state.IPv6 != nil {
			info["ipv6"] = fmt.Sprintf("%v", state.IPv6.Addresses)
		}
		leaseInfo[name] = info
	}

	return map[string]interface{}{
		"running": true,
		"leases":  leaseInfo,
	}
}

// 全局守护进程实例
var globalDaemon *DHCPDaemon
var daemonOnce sync.Once

// GetDHCPDaemon 获取全局守护进程
func GetDHCPDaemon() *DHCPDaemon {
	daemonOnce.Do(func() {
		globalDaemon = NewDHCPDaemon()
	})
	return globalDaemon
}

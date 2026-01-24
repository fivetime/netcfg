/*
Copyright © 2024 netcfg authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package netlink

import (
	"fmt"
	"net"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// WireGuardConfig WireGuard 配置
type WireGuardConfig struct {
	PrivateKey   string           // Base64 编码的私钥
	ListenPort   int              // 监听端口
	FwMark       int              // 防火墙标记
	ReplacePeers bool             // 是否替换所有 peers
	Peers        []*WireGuardPeer // Peer 列表
}

// WireGuardPeer WireGuard Peer 配置
type WireGuardPeer struct {
	PublicKey                   string   // Base64 编码的公钥
	PresharedKey                string   // Base64 编码的预共享密钥 (可选)
	Endpoint                    string   // 端点地址 (host:port)
	AllowedIPs                  []string // 允许的 IP 范围 (CIDR)
	PersistentKeepaliveInterval int      // 持久保活间隔 (秒)
	Remove                      bool     // 是否删除此 peer
}

// WireGuardManager WireGuard 管理器
type WireGuardManager struct {
	client *wgctrl.Client
}

// NewWireGuardManager 创建 WireGuard 管理器
func NewWireGuardManager() (*WireGuardManager, error) {
	client, err := wgctrl.New()
	if err != nil {
		return nil, fmt.Errorf("failed to create wgctrl client: %w", err)
	}
	return &WireGuardManager{client: client}, nil
}

// Close 关闭管理器
func (m *WireGuardManager) Close() error {
	if m.client != nil {
		return m.client.Close()
	}
	return nil
}

// ConfigureDevice 配置 WireGuard 设备
func (m *WireGuardManager) ConfigureDevice(name string, cfg *WireGuardConfig) error {
	if cfg == nil {
		return nil
	}

	wgCfg := wgtypes.Config{}

	// 设置私钥
	if cfg.PrivateKey != "" {
		key, err := parseKey(cfg.PrivateKey)
		if err != nil {
			return fmt.Errorf("invalid private key: %w", err)
		}
		wgCfg.PrivateKey = &key
	}

	// 设置监听端口
	if cfg.ListenPort > 0 {
		wgCfg.ListenPort = &cfg.ListenPort
	}

	// 设置防火墙标记
	if cfg.FwMark > 0 {
		wgCfg.FirewallMark = &cfg.FwMark
	}

	// 是否替换所有 peers
	wgCfg.ReplacePeers = cfg.ReplacePeers

	// 配置 peers
	for _, peer := range cfg.Peers {
		peerCfg, err := buildPeerConfig(peer)
		if err != nil {
			return fmt.Errorf("invalid peer config: %w", err)
		}
		wgCfg.Peers = append(wgCfg.Peers, *peerCfg)
	}

	return m.client.ConfigureDevice(name, wgCfg)
}

// GetDevice 获取 WireGuard 设备信息
func (m *WireGuardManager) GetDevice(name string) (*wgtypes.Device, error) {
	return m.client.Device(name)
}

// ListDevices 列出所有 WireGuard 设备
func (m *WireGuardManager) ListDevices() ([]*wgtypes.Device, error) {
	return m.client.Devices()
}

// AddPeer 添加 Peer
func (m *WireGuardManager) AddPeer(deviceName string, peer *WireGuardPeer) error {
	peerCfg, err := buildPeerConfig(peer)
	if err != nil {
		return err
	}

	return m.client.ConfigureDevice(deviceName, wgtypes.Config{
		Peers: []wgtypes.PeerConfig{*peerCfg},
	})
}

// RemovePeer 删除 Peer
func (m *WireGuardManager) RemovePeer(deviceName string, publicKey string) error {
	key, err := parseKey(publicKey)
	if err != nil {
		return fmt.Errorf("invalid public key: %w", err)
	}

	return m.client.ConfigureDevice(deviceName, wgtypes.Config{
		Peers: []wgtypes.PeerConfig{
			{
				PublicKey: key,
				Remove:    true,
			},
		},
	})
}

// GenerateKeyPair 生成密钥对
func GenerateKeyPair() (privateKey, publicKey string, err error) {
	privKey, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		return "", "", fmt.Errorf("failed to generate private key: %w", err)
	}

	return privKey.String(), privKey.PublicKey().String(), nil
}

// GeneratePresharedKey 生成预共享密钥
func GeneratePresharedKey() (string, error) {
	key, err := wgtypes.GenerateKey()
	if err != nil {
		return "", fmt.Errorf("failed to generate preshared key: %w", err)
	}
	return key.String(), nil
}

// PublicKeyFromPrivate 从私钥计算公钥
func PublicKeyFromPrivate(privateKey string) (string, error) {
	key, err := parseKey(privateKey)
	if err != nil {
		return "", err
	}
	return key.PublicKey().String(), nil
}

// buildPeerConfig 构建 Peer 配置
func buildPeerConfig(peer *WireGuardPeer) (*wgtypes.PeerConfig, error) {
	if peer == nil {
		return nil, fmt.Errorf("peer is nil")
	}

	cfg := &wgtypes.PeerConfig{}

	// 公钥 (必需)
	if peer.PublicKey == "" {
		return nil, fmt.Errorf("public key is required")
	}
	pubKey, err := parseKey(peer.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("invalid public key: %w", err)
	}
	cfg.PublicKey = pubKey

	// 删除标记
	if peer.Remove {
		cfg.Remove = true
		return cfg, nil
	}

	// 预共享密钥 (可选)
	if peer.PresharedKey != "" {
		psk, err := parseKey(peer.PresharedKey)
		if err != nil {
			return nil, fmt.Errorf("invalid preshared key: %w", err)
		}
		cfg.PresharedKey = &psk
	}

	// 端点 (可选)
	if peer.Endpoint != "" {
		endpoint, err := net.ResolveUDPAddr("udp", peer.Endpoint)
		if err != nil {
			return nil, fmt.Errorf("invalid endpoint %s: %w", peer.Endpoint, err)
		}
		cfg.Endpoint = endpoint
	}

	// AllowedIPs (必需)
	if len(peer.AllowedIPs) > 0 {
		cfg.ReplaceAllowedIPs = true
		for _, cidr := range peer.AllowedIPs {
			_, ipNet, err := net.ParseCIDR(cidr)
			if err != nil {
				return nil, fmt.Errorf("invalid allowed IP %s: %w", cidr, err)
			}
			cfg.AllowedIPs = append(cfg.AllowedIPs, *ipNet)
		}
	}

	// 持久保活间隔 (可选)
	if peer.PersistentKeepaliveInterval > 0 {
		interval := time.Duration(peer.PersistentKeepaliveInterval) * time.Second
		cfg.PersistentKeepaliveInterval = &interval
	}

	return cfg, nil
}

// parseKey 解析 Base64 编码的密钥
func parseKey(s string) (wgtypes.Key, error) {
	// wgtypes.ParseKey 接受 Base64 编码的字符串
	return wgtypes.ParseKey(s)
}

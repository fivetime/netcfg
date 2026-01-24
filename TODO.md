# netcfg TODO 列表

## 待实现功能

### 高优先级

- [ ] **WiFi/无线网络支持**
  - 需要集成 wpa_supplicant 或 iwd
  - 配置: `wifis.*`
  - 涉及: access-points, ssid, password, auth (WPA/WPA2/WPA3/Enterprise)

- [ ] **纯 Go DHCP 客户端**
  - 当前使用外部客户端 (dhclient/dhcpcd/udhcpc)
  - 可使用 insomniacslk/dhcp 库实现纯 Go 版本
  - 优势: 无外部依赖，更好的错误处理，支持 DHCP Options
  - 集成方法: `go get github.com/insomniacslk/dhcp@latest && go mod vendor`
  - 框架代码已准备 (`netlink/purego/`)，需要网络环境下载依赖

### 中优先级

- [ ] **Open vSwitch (OVS) 支持**
  - 需要调用 ovs-vsctl
  - 配置: `openvswitch.*`, `bridges.*.openvswitch`
  - 涉及: protocols, ports, controller, fail-mode

- [ ] **802.1X 认证**
  - 有线网络认证
  - 配置: `ethernets.*.auth`
  - 涉及: key-management, method, identity, password, certificates

- [ ] **SR-IOV 支持**
  - 需要操作 sysfs
  - 配置: `ethernets.*.embedded-switch-mode`, VF 配置
  - 涉及: 虚拟功能 (VF) 创建和配置

- [ ] **Modem/移动网络支持**
  - 需要集成 ModemManager
  - 配置: `modems.*`
  - 涉及: APN, PIN, SIM 配置

- [ ] **Bridge 参数完整支持**
  - STP 配置 (stp, forward-delay, hello-time, max-age)
  - 端口参数 (path-cost, port-priority)
  - ageing-time, priority

- [ ] **Bond 参数完整支持**
  - 当前只支持 mode
  - 需要: lacp-rate, mii-monitor-interval, min-links, transmit-hash-policy
  - 高级: arp-interval, arp-ip-targets, up-delay, down-delay, primary

### 低优先级

- [ ] **网卡 Offload 配置**
  - 需要调用 ethtool
  - 配置: `ethernets.*.receive-checksum-offload` 等
  - 涉及: TSO, GSO, GRO, LRO

- [ ] **InfiniBand 支持**
  - 配置: `ethernets.*.infiniband-mode`
  - 涉及: connected/datagram 模式

- [ ] **DHCP 高级选项**
  - `dhcp4-overrides`: route-metric, use-routes, use-dns, use-hostname
  - `dhcp6-overrides`: 类似选项
  - `dhcp-identifier`: mac/duid

- [ ] **IPv6 隐私扩展配置**
  - 配置: `ethernets.*.ipv6-privacy`
  - 当前 SLAAC 默认启用 use_tempaddr=2

- [ ] **TUN/TAP 高级配置**
  - user/group 权限
  - multi-queue 支持
  - vnet-hdr

- [ ] **Tunnel 高级配置**
  - encap (fou/gue)
  - GRE key, checksum
  - 6in4, 4in6

## 已完成功能

### 核心功能
- [x] netplan YAML 语法兼容
- [x] Network namespace (netns) 原生支持
- [x] 直接 netlink API (无需 ip 命令)
- [x] systemd 服务文件
- [x] cloud-init 渲染器集成
- [x] DHCP 租约续期守护进程 (daemon 模式)

### 设备类型 (default 和 netns 均支持)
- [x] Ethernet 物理网卡
- [x] Dummy 虚拟设备
- [x] Veth 设备（支持跨 netns）
- [x] Macvlan 设备
- [x] Macvtap 设备
- [x] Ipvlan 设备 (L2/L3/L3S)
- [x] Bridge 网桥 (含 VLAN filtering)
- [x] Bond 链路聚合
- [x] VLAN 802.1Q
- [x] VRF 路由隔离
- [x] Tunnel (GRE/IPIP/SIT/VTI)
- [x] TUN 设备
- [x] TAP 设备

### WireGuard 完整支持
- [x] WireGuard 设备创建
- [x] 私钥配置 (private-key)
- [x] 监听端口 (listen-port)
- [x] 防火墙标记 (fwmark)
- [x] Peer 配置 (public-key, endpoint, allowed-ips)
- [x] 预共享密钥 (preshared-key)
- [x] 持久保活 (persistent-keepalive)
- [x] 密钥生成工具 (genkey/pubkey/genpsk)
- [x] 状态查看 (wg show)

### VXLAN 完整支持
- [x] VXLAN 设备创建
- [x] external/flow-based 模式 (OVS/TC/OVN)
- [x] learning 开关
- [x] arp-proxy, neigh-suppress
- [x] l2miss, l3miss 通知
- [x] rsc (Route Short Circuit)
- [x] noage, ageing (FDB 老化)
- [x] limit (FDB 条目限制)
- [x] gbp (Group Based Policy)
- [x] port-range (源端口范围，ECMP)
- [x] udp-checksum, udp6-zero-csum-tx/rx
- [x] 静态 FDB 条目管理
- [x] 静态 Neighbor 条目管理

### 网络配置
- [x] 静态 IP 地址
- [x] 静态路由
- [x] 策略路由 (routing-policy)
- [x] DHCPv4 (外部客户端)
- [x] DHCPv6 (外部客户端)
- [x] IPv6 SLAAC/RA
- [x] post-script 支持

## 代码改进

- [ ] 单元测试覆盖
- [ ] 集成测试
- [ ] 配置验证和错误提示改进
- [ ] man page 文档
- [x] 配置变更检测和增量应用 - `netcfg diff` 预览变更，apply 自动清理废弃资源
- [x] 热重载支持 (SIGHUP) - daemon 模式已支持
- [x] 状态持久化和恢复 - 配置状态保存在 /var/lib/netcfg/state.json

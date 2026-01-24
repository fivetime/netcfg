# netcfg - 下一代 Linux 网络配置工具

## 项目背景

在现代数据中心和云原生环境中，网络配置变得越来越复杂。传统的网络配置工具面临诸多挑战：

- **netplan** 功能强大但不支持网络命名空间（netns），无法满足容器和多租户隔离需求
- **systemd-networkd** 配置繁琐，对高级网络功能支持有限
- **传统脚本** 难以维护，缺乏声明式配置和状态管理
- **NetworkManager** 主要面向桌面环境，不适合服务器场景

随着 EVPN/VXLAN 多租户网络、Kubernetes 网络命名空间、WireGuard VPN 等技术的普及，急需一个能够统一管理这些复杂网络配置的现代化工具。

## 项目目标

**netcfg** 旨在成为 Linux 系统上功能最完整的声明式网络配置工具：

1. **兼容性** - 100% 兼容 netplan YAML 语法，零学习成本迁移
2. **扩展性** - 原生支持网络命名空间，突破 netplan 的限制
3. **现代化** - 支持 VXLAN/EVPN、WireGuard 等现代网络技术
4. **可靠性** - 增量配置更新、状态持久化、热重载、自动回滚
5. **轻量级** - 单一静态二进制，无外部依赖，适合嵌入式和容器环境
6. **云原生** - 集成 cloud-init，支持 OpenStack/Kubernetes 等云平台

## 系统架构

```
┌─────────────────────────────────────────────────────────────────┐
│                         用户接口层                               │
├─────────────┬─────────────┬─────────────┬─────────────┬────────┤
│  netcfg     │  netcfg     │  netcfg     │  netcfg     │ netcfg │
│  apply      │  diff       │  status     │  daemon     │ wg     │
├─────────────┴─────────────┴─────────────┴─────────────┴────────┤
│                         配置管理层                               │
├────────────────────────────┬────────────────────────────────────┤
│     YAML 配置解析          │         状态管理                    │
│   (netplan 兼容语法)       │   (/var/lib/netcfg/state.json)     │
├────────────────────────────┴────────────────────────────────────┤
│                         网络操作层                               │
├──────────┬──────────┬──────────┬──────────┬──────────┬─────────┤
│ Netlink  │ WireGuard│  DHCP    │  Netns   │  Sysctl  │  Sysfs  │
│  API     │  wgctrl  │ Client   │  管理    │  配置    │  配置   │
├──────────┴──────────┴──────────┴──────────┴──────────┴─────────┤
│                       Linux 内核网络栈                           │
└─────────────────────────────────────────────────────────────────┘
```

### 核心组件

| 组件 | 说明 |
|------|------|
| **config** | YAML 配置解析，支持多文件合并和 netplan 兼容语法 |
| **netlink** | 基于 vishvananda/netlink 的网络设备管理 |
| **state** | 配置状态跟踪，支持增量更新和差异计算 |
| **wireguard** | 基于官方 wgctrl 库的 WireGuard 管理 |
| **dhcp** | DHCP 客户端集成和租约管理守护进程 |

## 功能特性

### 设备类型支持

| 类型 | 状态 | 说明 |
|------|:----:|------|
| **ethernets** | ✅ | 物理网卡配置 |
| **bridges** | ✅ | 网桥（含 VLAN filtering、STP） |
| **bonds** | ✅ | 链路聚合（所有模式） |
| **vlans** | ✅ | 802.1Q VLAN |
| **vxlans** | ✅ | VXLAN 隧道（L2VNI/L3VNI、EVPN） |
| **vrfs** | ✅ | VRF 路由隔离 |
| **wireguards** | ✅ | WireGuard VPN |
| **tunnels** | ✅ | IP 隧道（GRE/IPIP/SIT/VTI） |
| **veths** | ✅ | 虚拟以太网对 |
| **dummy** | ✅ | Dummy 设备 |
| **macvlan** | ✅ | MACVLAN |
| **macvtap** | ✅ | MACVTAP |
| **ipvlan** | ✅ | IPVLAN (L2/L3/L3S) |
| **tun** | ✅ | TUN 设备 |
| **tap** | ✅ | TAP 设备 |
| **wifi** | ⏳ | 无线网络（计划中） |
| **modems** | ⏳ | 调制解调器（计划中） |

### 网络命名空间支持

| 功能 | 状态 | 说明 |
|------|:----:|------|
| netns 创建/删除 | ✅ | 自动管理命名空间生命周期 |
| 设备跨 netns 移动 | ✅ | 物理网卡移入指定 netns |
| veth 跨 netns 连接 | ✅ | veth 对连接不同 netns |
| netns 内完整配置 | ✅ | 每个 netns 独立的完整网络栈 |
| loopback 自动配置 | ✅ | 自动启用 netns 内的 lo 设备 |
| post-up 脚本 | ✅ | netns 创建后执行自定义脚本 |

### VXLAN/EVPN 支持

| 功能 | 状态 | 说明 |
|------|:----:|------|
| 基础 VXLAN | ✅ | 点对点和组播模式 |
| VNI 配置 | ✅ | VXLAN Network Identifier |
| 本地/远端 IP | ✅ | local/remote 端点配置 |
| 组播组 | ✅ | BUM 流量组播 |
| 端口配置 | ✅ | 自定义 UDP 端口（默认 4789） |
| L2miss/L3miss | ✅ | 学习通知 |
| 静态 FDB | ✅ | 手动配置转发表 |
| 静态 ARP/NDP | ✅ | 手动配置邻居表 |
| external 模式 | ✅ | 与控制面集成（FRR BGP EVPN） |
| Bridge 绑定 | ✅ | VXLAN + Bridge + VLAN 集成 |
| VRF 绑定 | ✅ | L3VNI 路由隔离 |
| TTL/TOS | ✅ | 封装参数 |
| DF 控制 | ✅ | 分片控制 |
| 流标签 | ✅ | IPv6 流标签 |

### WireGuard 支持

| 功能 | 状态 | 说明 |
|------|:----:|------|
| 设备创建 | ✅ | 内核 WireGuard 设备 |
| 密钥管理 | ✅ | 私钥/公钥/PSK 配置 |
| Peer 管理 | ✅ | 添加/删除/更新 peers |
| AllowedIPs | ✅ | 路由配置 |
| Endpoint | ✅ | 对端地址 |
| Keepalive | ✅ | NAT 穿透保活 |
| FwMark | ✅ | 防火墙标记 |
| 密钥生成工具 | ✅ | `netcfg wg genkey/pubkey/genpsk` |
| 状态查看 | ✅ | `netcfg wg show` |

### IP 地址和路由

| 功能 | 状态 | 说明 |
|------|:----:|------|
| IPv4/IPv6 地址 | ✅ | 静态地址配置 |
| 多地址 | ✅ | 单接口多 IP |
| DHCPv4 | ✅ | 自动获取 IPv4 地址 |
| DHCPv6 | ✅ | 自动获取 IPv6 地址 |
| SLAAC | ✅ | IPv6 无状态自动配置 |
| 静态路由 | ✅ | 目标/网关/metric/表 |
| 策略路由 | ✅ | 基于源/目标/标记的路由 |
| 路由表 | ✅ | 多路由表支持 |
| 默认路由 | ✅ | 网关配置 |
| 链路本地 | ✅ | link-local 地址 |

### 高级网络功能

| 功能 | 状态 | 说明 |
|------|:----:|------|
| MTU 配置 | ✅ | 最大传输单元 |
| MAC 地址 | ✅ | 自定义硬件地址 |
| 混杂模式 | ✅ | promiscuous mode |
| Wake-on-LAN | ✅ | 远程唤醒 |
| 链路检测 | ✅ | carrier/dormant 状态 |
| optional 标记 | ✅ | 设备可选，不阻塞启动 |
| DNS 配置 | ✅ | nameservers/search domains |
| 接口重命名 | ✅ | 基于 match 规则 |

### 运维功能

| 功能 | 状态 | 说明 |
|------|:----:|------|
| 配置预览 | ✅ | `netcfg generate` 显示合并后配置 |
| 差异检测 | ✅ | `netcfg diff` 预览变更 |
| 增量应用 | ✅ | 只修改变化的部分 |
| 状态持久化 | ✅ | 重启后保持配置状态 |
| 热重载 | ✅ | SIGHUP 重新加载配置 |
| 自动回滚 | ✅ | `netcfg try` 超时自动恢复 |
| 守护进程 | ✅ | `netcfg daemon` DHCP 租约管理 |
| 状态查看 | ✅ | `netcfg status` 接口状态 |
| 详细信息 | ✅ | `netcfg show` 接口详情 |
| systemd 集成 | ✅ | 服务文件和依赖管理 |
| cloud-init 集成 | ✅ | 云平台自动化配置 |

### 命令行工具

| 命令 | 说明 |
|------|------|
| `netcfg apply` | 应用网络配置 |
| `netcfg diff` | 预览配置变更 |
| `netcfg generate` | 显示合并后的配置 |
| `netcfg status` | 查看接口状态 |
| `netcfg show <iface>` | 显示接口详细信息 |
| `netcfg try [--timeout N]` | 试用配置，超时自动回滚 |
| `netcfg daemon` | 启动 DHCP 守护进程 |
| `netcfg destroy` | 删除配置的网络命名空间 |
| `netcfg netns list` | 列出网络命名空间 |
| `netcfg netns create <name>` | 创建网络命名空间 |
| `netcfg netns delete <name>` | 删除网络命名空间 |
| `netcfg netns exec <name> <cmd>` | 在命名空间中执行命令 |
| `netcfg get <path>` | 获取配置值 |
| `netcfg set <path>=<value>` | 设置配置值 |
| `netcfg wg genkey` | 生成 WireGuard 私钥 |
| `netcfg wg pubkey` | 从私钥计算公钥 |
| `netcfg wg genpsk` | 生成预共享密钥 |
| `netcfg wg show [iface]` | 显示 WireGuard 状态 |

## 与 netplan 的对比

| 特性 | netplan | netcfg |
|------|:-------:|:------:|
| YAML 配置语法 | ✅ | ✅ 兼容 |
| 网络命名空间 | ❌ | ✅ |
| VXLAN/EVPN | 基础 | ✅ 完整 |
| WireGuard 工具 | ❌ | ✅ |
| 配置差异预览 | ❌ | ✅ |
| 增量更新 | ❌ | ✅ |
| 自动回滚 | ❌ | ✅ |
| 热重载 | ❌ | ✅ |
| 单一二进制 | ❌ | ✅ |
| cloud-init 集成 | ✅ | ✅ |
| 后端依赖 | systemd-networkd/NM | 无 |

## 典型应用场景

### 1. 数据中心 EVPN/VXLAN Overlay

```yaml
network:
  version: 2
  ethernets:
    eth0:
      addresses: [10.0.0.1/24]
  
  vxlans:
    vxlan100:
      id: 100
      local: 10.0.0.1
      port: 4789
      learning: false
      l2miss: true
      l3miss: true
  
  bridges:
    br-vxlan100:
      interfaces: [vxlan100]
      parameters:
        stp: false
      addresses: [192.168.100.1/24]
```

### 2. 多租户网络隔离

```yaml
network:
  version: 2
  
  netns:
    tenant-a:
      ethernets:
        veth-a:
          addresses: [10.1.0.1/24]
    
    tenant-b:
      ethernets:
        veth-b:
          addresses: [10.2.0.1/24]
  
  veths:
    veth-a:
      peer:
        name: veth-a-br
    veth-b:
      peer:
        name: veth-b-br
```

### 3. WireGuard VPN 网关

```yaml
network:
  version: 2
  
  wireguards:
    wg0:
      addresses: [10.10.0.1/24]
      listen-port: 51820
      private-key: "BASE64_PRIVATE_KEY"
      peers:
        - public-key: "PEER_PUBLIC_KEY"
          allowed-ips: [10.10.0.0/24, 192.168.0.0/16]
          endpoint: "vpn.example.com:51820"
          persistent-keepalive: 25
```

### 4. Kubernetes 节点网络

```yaml
network:
  version: 2
  
  ethernets:
    eth0:
      dhcp4: true
  
  bridges:
    cni0:
      addresses: [10.244.0.1/24]
      parameters:
        stp: false
        forward-delay: 0
  
  dummy-devices:
    kube-ipvs0: {}
```

## 安装方式

### 二进制安装

```bash
# 下载
wget https://github.com/yourname/netcfg/releases/latest/download/netcfg-linux-amd64.tar.gz
tar -xzf netcfg-linux-amd64.tar.gz

# 安装
sudo cp netcfg /usr/local/bin/
sudo cp systemd/*.service /lib/systemd/system/
sudo systemctl daemon-reload
```

### Debian/Ubuntu

```bash
wget https://github.com/yourname/netcfg/releases/latest/download/netcfg_amd64.deb
sudo dpkg -i netcfg_amd64.deb
```

### 从源码编译

```bash
git clone https://github.com/yourname/netcfg.git
cd netcfg
go build -o netcfg .
```

## 项目状态

- **当前版本**: v0.2.0
- **开发语言**: Go 1.22+
- **代码行数**: ~7,000 行
- **已实现功能**: 51 项
- **待实现功能**: 18 项
- **许可证**: Apache License 2.0

## 路线图

### v0.3.0 (计划中)
- [ ] WiFi/无线网络支持
- [ ] 纯 Go DHCP 客户端（移除外部依赖）
- [ ] Open vSwitch 集成
- [ ] 配置验证增强

### v1.0.0 (目标)
- [ ] 完整单元测试覆盖
- [ ] 集成测试套件
- [ ] man page 文档
- [ ] 802.1X 认证
- [ ] SR-IOV 支持

## 贡献指南

欢迎提交 Issue 和 Pull Request！

## 许可证

Apache License 2.0

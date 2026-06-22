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
| **wifi** | ✅ | 无线网络（生成 wpa_supplicant 配置 + 直接 spawn，init-agnostic） |
| **modems** | ⏳ | 调制解调器（需 ModemManager，未实现） |

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
| 静态路由 | ✅ | to/via/from/metric/table/scope/type/on-link/mtu（见 TODO P0-3） |
| 策略路由 | ✅ | 基于源/目标/标记的路由（含 VRF 级，见 TODO P0-7） |
| 路由表 | ✅ | 多路由表支持 |
| 默认路由 | ✅ | 网关配置 |
| 链路本地 | ✅ | IPv6 link-local（addr_gen_mode）；IPv4 LL 未支持 |
| IPv6 隐私扩展 | ✅ | ipv6-privacy（use_tempaddr） |
| DHCP overrides | ✅ | use-dns/use-mtu/use-routes/route-metric/use-domains、send-hostname/hostname、dhcp-identifier(mac/duid) |
| 地址 lifetime/label | ✅ | addresses[].lifetime（0/forever）、addresses[].label |
| IPv6 地址生成 | ✅ | ipv6-address-generation（eui64/stable-privacy）、ipv6-address-token |

### SRv6 (seg6) 支持（netcfg 扩展）

内核态 Segment Routing over IPv6，非 netplan 标准。语法：`routes[].encap` 做 transit
引流 + 顶层（或 per-netns）`srv6:` 段做 endpoint 行为与启用。详见 `docs/srv6-design.md`、
`example/srv6/`。

| 功能 | 状态 | 说明 |
|------|:----:|------|
| transit 引流 | ✅ | `routes[].encap`（seg6，mode encap/inline + segments）|
| endpoint 行为 | ✅ | End/End.X/End.T/End.DX2/DX4/DX6/DT4/DT6/DT46/B6/B6.Encaps |
| seg6_enabled | ✅ | `srv6.enabled`（all）+ `srv6.interfaces`（各口）|
| SID 锚定设备 | ✅ | `srv6.device`/per-SID `dev`，缺省自动建 dummy `srv6`（内核拒绝 lo 上 seg6local）|
| End.DT4/DT46 | ✅ | `vrf-table`，自动开 `net.vrf.strict_mode` |
| 幂等 + 回收 | ✅ | `RouteReplace` 幂等；移除的 SID 下次 apply 回收 |
| 真机验证 | ✅ | kernel 6.8 全量 action 通过；集成测试 `tests/integration`（无 seg6 内核自动跳过）|

> 要求内核 `CONFIG_IPV6_SEG6_LWTUNNEL`（Ubuntu/RHEL 通用内核默认开启；WSL2 未开）。

### 高级网络功能

| 功能 | 状态 | 说明 |
|------|:----:|------|
| MTU 配置 | ✅ | 最大传输单元 |
| MAC 地址 | ✅ | 自定义硬件地址 |
| 混杂模式 | ⏳ | promiscuous mode（未实现） |
| Wake-on-LAN | ✅ | 远程唤醒（ethtool wol） |
| 链路检测 | ✅ | carrier/dormant 状态（status 显示） |
| optional 标记 | ✅ | 设备可选，不阻塞启动（status --wait） |
| DNS 配置 | ✅ | nameservers/search domains（resolvectl/resolv.conf） |
| 接口重命名 | ✅ | 基于 match (name/mac/driver) 规则，set-name |
| activation-mode | ✅ | manual/off：配置但不激活（off 强制 down） |
| 802.1X / EAP（有线） | ✅ | auth 块 → 生成 wpa_supplicant 配置并直接 spawn（init-agnostic） |
| WiFi access-points | ✅ | PSK/SAE(WPA3)/EAP 企业/开放、mode/band/channel/bssid/hidden、regulatory-domain |
| 网卡 offload | ✅ | rx/tx-checksum、tso/tso6/gso/gro/lro（ethtool -K） |
| SR-IOV | ✅ | virtual-function-count、embedded-switch-mode、`netcfg rebind`（VF 重绑） |
| InfiniBand | ✅ | infiniband-mode（connected/datagram，IPoIB） |
| bridge 端口属性 | ✅ | hairpin、neigh-suppress、port-mac-learning |
| emit-lldp | ⏳ | 需 LLDP daemon（networkd-only），netcfg 不实现（显式告警） |
| ignore-carrier / critical | ✅ | 直接 netlink 下发本就等价（不依赖 carrier、不随重启清配置） |

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
| `netcfg rebind [iface]` | 重绑 SR-IOV VF 到驱动 |
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

## VPP 后端（可选）

netcfg 可用同一套 netplan 风格 YAML，把配置下发到 **VPP 用户态数据平面**（经
GoVPP），而非内核。设计见 `docs/vpp-backend-design.md`。

**归属（做法 A）**：设备带 `vpp:` 块**或**生效 `renderer` 为 `vpp` 即走 VPP；内核与
VPP 设备可共存（按设备 opt-in），一行 `renderer: vpp` 切整机。多文件可拆
`10-kernel.yaml` / `20-vpp.yaml`，用 `vpp:` 块隔离。

| 能力 | 状态 | 说明 |
|------|:----:|------|
| af-packet 接口 | ✅ | 与内核网卡共存（`mode: af-packet`，默认） |
| loopback | ✅ | VPP 软件 loopback / BVI |
| 地址 / 路由 / 默认网关 | ✅ | v4/v6，幂等 |
| VLAN sub-interface | ✅ | `vlans` → dot1q |
| bridge domain + BVI | ✅ | `bridges`，带地址自动建 BVI loopback |
| bond | ✅ | `bonds`，mode 映射（802.3ad→lacp 等） |
| VXLAN | ✅ | `tunnels:mode:vxlan` |
| dpdk / avf 独占 | 🟡 | NIC/VF 独占；生成 `startup.conf`，运行态需真机 PCI |
| startup.conf 生成 | ✅ | cpu/dpdk/uio（顶层 `vpp:` 段，改动需重启 VPP） |
| NAT (nat44/nat64/nat66) | ✅ | SNAT/masquerade/地址池/静态映射(端口转发·1:1·twice-nat)；`vpp.nat`，netcfg 扩展 |
| SR-IOV VF + bond | 🟡 | 内核建 VF(P2-3) + VF 交 VPP + VPP bond；完整链需真机 |

依赖：运行中的 VPP（API socket `/run/vpp/api.sock`）、netcfg 以 root 或 `vpp` 组运行。
apply 时连接并做绑定 CRC 兼容性自检。集成测试见 `tests/vpp/`。

## 与 netplan 的对比

| 特性 | netplan | netcfg |
|------|:-------:|:------:|
| YAML 配置语法 | ✅ | ✅ 兼容 |
| 网络命名空间 | ❌ | ✅ |
| VXLAN/EVPN | 基础 | ✅ 完整 |
| 内核 SRv6 (seg6) | ❌ | ✅ transit + endpoint |
| WireGuard 工具 | ❌ | ✅ |
| 配置差异预览 | ❌ | ✅ |
| 增量更新 | ❌ | ✅ |
| 自动回滚 | ❌ | ✅ |
| 热重载 | ❌ | ✅ |
| 单一二进制 | ❌ | ✅ |
| cloud-init 集成 | ✅ | ✅ |
| 后端依赖 | systemd-networkd/NM | 无（直接 netlink） |
| init 系统 | 偏向 systemd | init-agnostic（systemd/OpenRC/runit…） |
| 802.1x/WiFi | wpa_supplicant（经后端） | wpa_supplicant（直接 spawn，可选监督模板） |

## 典型应用场景

### 1. 数据中心 EVPN/VXLAN Overlay

```yaml
network:
  version: 2
  ethernets:
    eth0:
      addresses: [10.0.0.1/24]
  
  tunnels:
    vxlan100:
      mode: vxlan
      id: 100
      local: 10.0.0.1
      port: 4789
      mac-learning: false
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
  
  virtual-ethernets:
    veth-a:
      peer: veth-a-br
    veth-b:
      peer: veth-b-br
```

### 3. WireGuard VPN 网关

```yaml
network:
  version: 2
  
  tunnels:
    wg0:
      mode: wireguard
      addresses: [10.10.0.1/24]
      port: 51820
      key: "BASE64_PRIVATE_KEY"
      peers:
        - keys:
            public: "PEER_PUBLIC_KEY"
          allowed-ips: [10.10.0.0/24, 192.168.0.0/16]
          endpoint: "vpn.example.com:51820"
          keepalive: 25
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

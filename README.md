# netcfg

A network configuration tool compatible with netplan syntax, with native support for network namespaces (netns).

## Features

- **Netplan Compatible**: Drop-in replacement for netplan, reads `/etc/netplan/*.yaml`
- **Network Namespace Support**: Native netns creation and management
- **Direct Netlink API**: No dependency on `ip` command, better performance and security
- **Full Device Support**: ethernet, wifi, dummy, veth, macvlan, macvtap, ipvlan, bridge, bond, vlan, vxlan, vrf, tunnel (incl. wireguard via `tunnels:mode`), tun, tap
- **VXLAN/EVPN Ready**: Full VXLAN support including FDB management, ARP suppress, external mode
- **WiFi & 802.1x**: Generates wpa_supplicant config and spawns it directly (init-agnostic) — PSK/SAE(WPA3)/EAP enterprise
- **NIC tuning**: offload (ethtool), SR-IOV (VF count / eswitch mode / `rebind`), Wake-on-LAN, InfiniBand mode
- **Pure-Go DHCP**: Built-in DHCPv4/v6 client (falls back to external clients), with DHCP overrides
- **init-agnostic**: No dependency on systemd-networkd or D-Bus; runs under systemd / OpenRC / runit / etc. Optional supervision templates in `init/`
- **VPP backend (optional)**: program VPP's userspace dataplane via GoVPP using the same netplan-style YAML — interfaces (af-packet/loopback/dpdk/avf), addresses, routes, VLANs, bridges, bonds, VXLAN. Per-device opt-in; kernel and VPP devices coexist. See `docs/vpp-backend-design.md`
- **cloud-init Integration**: Works as cloud-init network renderer for VM/bare-metal automation
- **DHCP Daemon**: Built-in lease renewal
- **Cross-platform**: Linux only (uses kernel netlink API)

## Installation

### From source

```bash
git clone https://github.com/netcfg/netcfg.git
cd netcfg
make build
sudo make install

# 启用开机自启动
sudo make enable
```

### From deb package

```bash
sudo apt install ./netcfg_0.2.0_amd64.deb
```

## systemd 服务

netcfg 支持开机自动配置网络：

```bash
# 启用开机自启动
sudo systemctl enable netcfg.service

# 手动启动
sudo systemctl start netcfg.service

# 查看状态
sudo systemctl status netcfg.service

# 重新加载配置
sudo systemctl reload netcfg.service

# 停止（会销毁 netns 和创建的设备）
sudo systemctl stop netcfg.service
```

服务文件说明：
- `netcfg.service` - 主服务，开机时应用配置
- `netcfg-netns.service` - netns 专用服务（可选）
- `netcfg-wait-online.service` - 等待网络就绪（可选）

## Usage

### Basic Commands

```bash
# Apply network configuration
netcfg apply

# Preview changes without applying
netcfg diff

# Show merged configuration (dry-run)
netcfg generate

# Show network status
netcfg status
netcfg status -a        # All namespaces
netcfg status -n myns   # Specific namespace

# Show detailed interface info
netcfg show eth0

# Try configuration with automatic rollback
netcfg try --timeout 60

# Get/set configuration values
netcfg get network.ethernets.eth0.addresses
netcfg set network.ethernets.eth0.dhcp4=true

# Run as daemon (DHCP lease management + SIGHUP reload)
netcfg daemon
```

### Namespace Commands

```bash
# List namespaces
netcfg netns list

# Create namespace
netcfg netns create myns

# Delete namespace
netcfg netns delete myns

# Execute command in namespace
netcfg netns exec myns ip addr

# Destroy all configured namespaces
netcfg destroy
netcfg destroy -a       # All namespaces
```

### Incremental Updates

netcfg tracks applied configuration state and supports incremental updates:

```bash
# Preview what changes would be made
netcfg diff

# Output example:
#   - Remove address 192.168.1.101/24 from eth0 (in default)
#   - Remove device vxlan100 (in default)
#   + Add device vxlan200 (in default)

# Apply changes (automatically removes obsolete resources)
netcfg apply
```

State is stored in `/var/lib/netcfg/state.json`.

## Configuration

Configuration files are read from `/etc/netplan/` (for netplan compatibility) or `/etc/netcfg/`.

### Basic Example (netplan compatible)

```yaml
network:
  version: 2

  ethernets:
    eth0:
      addresses:
        - 192.168.1.100/24
      gateway4: 192.168.1.1
      nameservers:
        addresses:
          - 8.8.8.8

  bridges:
    br0:
      interfaces:
        - eth1
        - eth2
      addresses:
        - 10.0.0.1/24
```

### Network Namespace Example

```yaml
network:
  version: 2

  # Default namespace
  ethernets:
    eth0:
      dhcp4: true

  # Network namespaces
  netns:
    # Isolated VPN namespace
    vpn:
      macvlan-devices:
        mv0:
          link: eth0
          mode: bridge
          addresses:
            - 192.168.1.200/24
          routes:
            - to: default
              via: 192.168.1.1
      
      post-script: |
        echo "VPN namespace ready"

    # Container namespace with veth
    container:
      veth-devices:
        veth-container:
          addresses:
            - 10.100.0.2/24
          peer:
            name: veth-host
            netns: ""  # default namespace
            addresses:
              - 10.100.0.1/24
```

### Legacy netnsplan Format (also supported)

```yaml
netns:
  ns1:
    ethernets:
      eth1:
        addresses:
          - 10.1.0.1/24
    dummy-devices:
      dummy0:
        addresses:
          - 10.2.0.1/24
```

## Supported Device Types

| Device Type | YAML Key | Description |
|-------------|----------|-------------|
| Ethernet | `ethernets` | Physical NICs (offload, SR-IOV, 802.1x, Wake-on-LAN, InfiniBand) |
| WiFi | `wifis` | Wireless (wpa_supplicant; PSK/SAE/EAP) |
| Dummy | `dummy-devices` | Virtual loopback-like devices |
| Veth | `veth-devices` | Virtual ethernet pairs |
| Macvlan | `macvlan-devices` | MAC-based virtual LAN |
| Macvtap | `macvtap-devices` | Macvlan with tap interface |
| Ipvlan | `ipvlan-devices` | IP-based virtual LAN (L2/L3/L3S) |
| Bridge | `bridges` | Software bridge |
| Bond | `bonds` | Link aggregation |
| VLAN | `vlans` | 802.1Q VLAN |
| VXLAN | `tunnels` (mode: vxlan) | Virtual extensible LAN (EVPN ready) |
| Tunnel | `tunnels` | GRE/IPIP/SIT/VTI tunnels |
| WireGuard | `tunnels` (mode: wireguard) | WireGuard VPN |
| VRF | `vrfs` | Virtual routing and forwarding |
| TUN | `tun-devices` | TUN virtual device |
| TAP | `tap-devices` | TAP virtual device |

## VPP Backend (optional)

netcfg can program **VPP's userspace dataplane** (via [GoVPP](https://go.fd.io/govpp))
using the same netplan-style YAML it uses for the kernel. See
[`docs/vpp-backend-design.md`](docs/vpp-backend-design.md) for the full design.

A device is VPP-managed when it has a `vpp:` block **or** its effective
`renderer` is `vpp`. Kernel and VPP devices coexist (per-device opt-in); a single
`renderer: vpp` switches the whole config to VPP.

```yaml
network:
  version: 2
  renderer: vpp
  ethernets:
    eth0:
      addresses: [10.0.0.1/24]
      routes: [{ to: default, via: 10.0.0.254 }]
      vpp: { mode: af-packet, host-if: eth0 }   # coexist with kernel NIC
    vf0:
      vpp: { mode: dpdk, pci: "0000:03:02.0" }  # VPP owns the NIC/VF
  bonds:
    bond0:
      interfaces: [vf0, vf1]
      parameters: { mode: 802.3ad }
      addresses: [10.20.0.1/24]
  vlans:
    eth0.100: { id: 100, link: eth0 }
  tunnels:
    vx100: { mode: vxlan, id: 100, local: 10.0.0.1, remote: 10.0.0.2 }
  bridges:
    br0: { interfaces: [vx100], addresses: [10.22.0.1/24] }   # auto BVI loopback
```

Multi-file split — keep kernel and VPP configs separate (`vpp:` block marks VPP intent):

```
/etc/netplan/10-kernel.yaml   # kernel devices (no vpp: block)
/etc/netplan/20-vpp.yaml      # devices with vpp: blocks
```

Interface modes: `af-packet` (coexist, default), `dpdk`/`avf` (VPP owns the NIC/VF),
`loopback`, `tap`, `memif`. NIC-ownership (`dpdk`/`avf`) and `startup.conf`
(cpu/dpdk/hugepages, generated from the top-level `vpp:` section) require a VPP
restart to take effect; addresses/routes/VLAN/bridge/bond/VXLAN apply live.

Requirements: a running VPP (binary API socket at `/run/vpp/api.sock`), and netcfg
running as root or in the `vpp` group. netcfg connects and verifies binding
compatibility (CRC) at apply time.

### VPP NAT (netcfg extension)

VPP NAT (no netplan equivalent) is configured under `vpp.nat` — NAT44 (SNAT/
masquerade, address pools, static 1:1 and port-forward, twice-nat), NAT64, NAT66:

```yaml
network:
  version: 2
  renderer: vpp
  vpp:
    nat:
      nat44:
        enable: true
        mode: ed              # ed (endpoint-dependent, default) | ei
        sessions: 63000
        interfaces:
          - { name: lan, role: inside }
          - { name: wan, role: outside }   # or role: output for output-feature SNAT
        pools:
          - { start: 203.0.113.10, end: 203.0.113.20 }
        static:
          - { proto: tcp, local: 10.0.0.5, local-port: 80, external: 203.0.113.10, external-port: 8080 }  # port-forward
          - { local: 10.0.0.6, external: 203.0.113.11 }     # 1:1
      nat64:
        enable: true
        prefix: "64:ff9b::/96"
        interfaces: [{ name: v6lan, role: inside }, { name: wan, role: outside }]
        pools: [{ start: 203.0.113.30, end: 203.0.113.40 }]
      nat66:
        static: [{ local: "2001:db8::5", external: "2001:db8:1::5" }]
```

NAT applies live via the nat44_ed / nat64 / nat66 plugins. Re-apply is idempotent
(adding existing entries is a no-op). Note: removing a NAT entry from config is not
yet auto-reaped from VPP (re-apply is additive).

## Comparison with netplan

| Feature | netplan | netcfg |
|---------|---------|--------|
| YAML syntax | ✅ | ✅ (compatible) |
| VPP dataplane backend | ❌ | ✅ (GoVPP) |
| systemd-networkd backend | ✅ | ❌ (direct netlink) |
| NetworkManager backend | ✅ | ❌ |
| Network namespace | ❌ | ✅ |
| Direct netlink API | ❌ | ✅ |
| init system | systemd-leaning | init-agnostic (systemd/OpenRC/runit/…) |
| systemd-networkd / D-Bus dependency | required | none |
| External command dependency | ip, networkctl | none for core (optional: ethtool/devlink for tuning, wpa_supplicant for wifi/802.1x) |

## Why netcfg?

1. **Network Namespace Support**: netplan doesn't support netns, and systemd-networkd's netns support has been stalled since 2020 (systemd/systemd#14915).

2. **Direct Netlink**: No forking of external commands, better performance and security.

3. **Simpler Architecture**: No backend services required, configuration is applied directly.

4. **Drop-in Replacement**: Compatible with existing netplan configurations.

## License

Apache License 2.0

## 支持的设备类型

netcfg 支持以下所有设备类型，在 default namespace 和自定义 netns 中均可创建：

| 设备类型 | 配置键 | 说明 |
|----------|--------|------|
| ethernet | `ethernets` | 物理网卡（offload / SR-IOV / 802.1x / WoL / InfiniBand） |
| wifi | `wifis` | 无线网络（wpa_supplicant；PSK/SAE/EAP） |
| dummy | `dummy-devices` | 虚拟接口 |
| veth | `veth-devices` | 虚拟以太网对 |
| macvlan | `macvlan-devices` | MAC 虚拟化 |
| macvtap | `macvtap-devices` | MAC 虚拟化 + TAP |
| ipvlan | `ipvlan-devices` | IP 虚拟化 (L2/L3/L3S) |
| bridge | `bridges` | 软件网桥 |
| bond | `bonds` | 链路聚合 |
| vlan | `vlans` | 802.1Q VLAN |
| vxlan | `tunnels` (mode: vxlan) | VXLAN overlay (含 EVPN 支持) |
| tunnel | `tunnels` | GRE/IPIP/SIT/VTI |
| wireguard | `tunnels` (mode: wireguard) | WireGuard VPN |
| vrf | `vrfs` | VRF 路由域 |
| tun | `tun-devices` | TUN 虚拟设备 |
| tap | `tap-devices` | TAP 虚拟设备 |

## VXLAN 和 EVPN 支持

netcfg 提供完整的 VXLAN 支持，包括 EVPN 数据平面所需的全部功能：

### VXLAN 配置选项

| 选项 | 配置键 | 说明 |
|------|--------|------|
| VNI | `id` | VXLAN Network Identifier |
| external | `external: true` | flow-based 模式 (OVS/TC/OVN) |
| learning | `learning: false` | 禁用 MAC 学习 (EVPN) |
| arp-proxy | `arp-proxy: true` | ARP 代理 |
| neigh-suppress | `neigh-suppress: true` | ARP/ND suppress |
| l2miss/l3miss | `l2miss: true` | 通知控制平面 |
| ageing | `ageing: 300` | FDB 老化时间 (秒) |
| limit | `limit: 10000` | FDB 条目限制 |
| port-range | `port-range: [32768, 60999]` | 源端口范围 (ECMP) |
| udp-checksum | `udp-checksum: true` | UDP 校验和 |
| fdb | `fdb: [...]` | 静态 FDB 条目 |
| neighbors | `neighbors: [...]` | 静态 ARP/ND 条目 |

### EVPN L2VNI 配置示例

```yaml
network:
  version: 2
  
  tunnels:
    vxlan100:
      mode: vxlan
      id: 100
      local: 10.0.0.1
      dest-port: 4789
      mac-learning: false       # EVPN 控制平面学习
      neigh-suppress: true      # 本地响应 ARP
      l2miss: true              # 通知 FRR
      ageing: 300
      fdb:
        - mac: "00:11:22:33:44:55"
          dst: "10.0.0.2"       # 远端 VTEP
          state: permanent
      neighbors:
        - ip: "192.168.100.10"
          mac: "00:11:22:33:44:55"
          state: permanent
  
  bridges:
    br100:
      interfaces: [vxlan100, eth1]
      vlan-filtering: true
      addresses: [192.168.100.1/24]
```

### external 模式 (OVS/OVN)

```yaml
tunnels:
  vxlan_sys:
    mode: vxlan
    id: 0                # VNI 由 flow 规则动态设置
    external: true       # flow-based
    local: 10.0.0.1
    dest-port: 4789
    mac-learning: false
```

## cloud-init 集成

netcfg 可作为 cloud-init 的网络渲染器，用于 VM 和裸金属自动化部署：

```bash
# 安装渲染器
sudo ./cloud-init/install.sh
```

配置后，cloud-init 会自动使用 netcfg 应用网络配置，支持 NoCloud、ConfigDrive、EC2、GCE、Azure 等数据源。

详见 `cloud-init/README.md`。

## WireGuard 支持

netcfg 使用官方 `wgctrl` 库提供完整的 WireGuard 支持。

### 密钥生成

```bash
# 生成私钥
netcfg wg genkey > privatekey

# 从私钥计算公钥
netcfg wg pubkey < privatekey > publickey

# 生成预共享密钥
netcfg wg genpsk > presharedkey

# 查看 WireGuard 设备状态
netcfg wg show
netcfg wg show wg0
```

### WireGuard 配置示例

```yaml
network:
  version: 2
  
  tunnels:
    wg0:
      mode: wireguard
      addresses:
        - 10.10.0.1/24
      port: 51820
      key: "cGFzc3dvcmQxMjM0NTY3ODkwMTIzNDU2Nzg5MDEyMzQ="  # Base64 私钥
      peers:
        - keys:
            public: "cHVibGljLWtleS1vZi1wZWVyLTEyMzQ1Njc4OTAxMjM="
          endpoint: "peer.example.com:51820"
          allowed-ips:
            - 10.10.0.2/32
            - 192.168.1.0/24
          keepalive: 25
        - keys:
            public: "cHVibGljLWtleS1vZi1wZWVyLTIyMjIyMjIyMjIyMjI="
            shared: "cHJlc2hhcmVkLWtleS0xMjM0NTY3ODkwMTIzNDU2Nzg="
          allowed-ips:
            - 10.10.0.3/32
      routes:
        - to: 192.168.1.0/24
          via: 10.10.0.2
```

### WireGuard VPN 网关示例

```yaml
network:
  version: 2
  
  ethernets:
    eth0:
      dhcp4: true
  
  tunnels:
    wg0:
      mode: wireguard
      addresses: [10.0.0.1/24]
      port: 51820
      key: "YOUR_PRIVATE_KEY"
      peers:
        # 移动设备
        - keys:
            public: "MOBILE_PUBLIC_KEY"
          allowed-ips: [10.0.0.2/32]
          keepalive: 25
        # 办公室网络
        - keys:
            public: "OFFICE_PUBLIC_KEY"
          endpoint: "office.example.com:51820"
          allowed-ips: [10.0.0.3/32, 192.168.100.0/24]
```

## netns 内完整示例

```yaml
network:
  version: 2
  
  netns:
    tenant-a:
      # 在 netns 中可以创建任何设备类型
      vrfs:
        mgmt:
          table: 100
          interfaces: [eth-mgmt]
        data:
          table: 200
          interfaces: [eth-data]
      
      bridges:
        br0:
          interfaces: [veth0]
          addresses: [192.168.1.1/24]
      
      tunnels:
        vxlan100:
          mode: vxlan
          id: 100
          local: 10.0.0.1
          remote: 10.0.0.2
          addresses: [192.168.100.1/24]
        gre0:
          mode: gre
          local: 10.0.0.1
          remote: 10.0.0.2
          addresses: [172.16.0.1/30]
        wg0:
          mode: wireguard
          addresses: [10.10.0.1/24]
          port: 51820
```

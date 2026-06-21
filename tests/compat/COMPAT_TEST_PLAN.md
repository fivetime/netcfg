# netcfg ↔ netplan examples 兼容性测试计划

直接拿官方 netplan 仓库 `examples/` 下的真实配置文件，用 netcfg **实跑 apply**，验证兼容性。
发现 bug 立即修复，每个 example 通过就打勾。本文件是**持久进度记录**（抗会话压缩）。

## 环境

- **方式**：Docker 特权容器（每个 example 一个 `--rm` 容器，独立 netns，互不污染、对宿主零影响）
- **镜像**：`netcfg-compat`（`tests/compat/Dockerfile`，ubuntu:24.04 + iproute2/wireguard-tools 仅用于结果验证）
- **二进制**：宿主交叉编译 `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o netcfg.linux .`（静态 ELF），挂载进容器
- **关键坑**：Git Bash 下 docker `-v` 必须 `MSYS_NO_PATHCONV=1` + `C:/` 绝对路径，否则路径被改写、挂载失效
- **运行**：`tests/compat/run.sh <example.yaml> [需预建的 dummy 网卡...]`

## 通过标准

- ✅ **PASS**：apply 正常（退出 0 或仅非致命 warning），netcfg **应支持**的设备/地址/路由在 `ip` 输出中确实生效
- ⚠️ **PARTIAL**：受支持部分已生效，不支持部分按预期告警（D 类的预期结果）
- ❌ **FAIL**：崩溃，或**受支持**的特性没生效 → **立即修复**再复测

> 物理网卡：容器无真实物理网卡，对引用 ethX/enpX 的 example 预建同名 `dummy` 顶替，以验证其上构建的 bond/bridge/vlan/vxlan/路由等。

---

## A. 虚拟设备（核心可测）

| # | example | 预建 dummy | netcfg 应做到 | 状态 |
|---|---------|-----------|--------------|------|
| A1 | dummy-devices.yaml | — | 创建 dummy 设备 + 地址 | ✅ |
| A2 | loopback_interface.yaml | — | lo 配置 | ✅ |
| A3 | bridge.yaml | enp3s0 | 创建 br + 加入成员 + 地址 | ✅ |
| A4 | bridge_vlan.yaml | enp0s25 | bridge + vlan | ✅ |
| A5 | bonding.yaml | enp3s0 enp4s0 | bond + 成员 + mode | ✅ (修复 BUG-4) |
| A6 | bonding_router.yaml | enp1s0..6s0 | bond + 路由 | ✅ |
| A7 | vlan.yaml | dummy+mac | vlan 设备 (id/link) + match/set-name | ✅ |
| A8 | vrf.yaml | — | vrf + table + 成员 | ✅ (修复 BUG-5) |
| A9 | vxlan.yaml | — | vxlan (id/local/remote) | ✅ (修复 BUG-3/6) |
| A10 | virtual-ethernet.yaml | — | veth pair | ✅ (修复 BUG-1) |
| A11 | ipv6_tunnel.yaml | — | tunnel 设备 | ✅ (eth0 警告为容器环境) |
| A12 | wireguard.yaml | — | wg 设备 + peer | ✅ (修复 BUG-2) |

## B. 地址 / 路由（以太网，预建 dummy）

| # | example | 预建 dummy | netcfg 应做到 | 状态 |
|---|---------|-----------|--------------|------|
| B1 | static.yaml | (按需) | 静态地址 + 网关 + DNS | ⬜ |
| B2 | static_multiaddress.yaml | (按需) | 多地址 | ⬜ |
| B3 | static_singlenic_multiip_multigateway.yaml | (按需) | 多 IP 多网关 | ⬜ |
| B4 | static-routes.yaml | (按需) | 静态路由 | ⬜ |
| B5 | route_metric.yaml | (按需) | 路由 metric | ⬜ |
| B6 | source_routing.yaml | (按需) | 策略路由 (from/table) | ⬜ |
| B7 | direct_connect_gateway.yaml | (按需) | on-link 网关 | ⬜ |
| B8 | direct_connect_gateway_ipv6.yaml | (按需) | IPv6 on-link | ⬜ |

## C. DHCP（隔离 netns 无 DHCP 服务器，预期请求但拿不到租约）

| # | example | 预建 dummy | netcfg 应做到 | 状态 |
|---|---------|-----------|--------------|------|
| C1 | dhcp.yaml | (按需) | 发起 DHCP 请求不崩溃（无服务器超时属正常） | ⬜ |
| C2 | windows_dhcp_server.yaml | (按需) | 同上 | ⬜ |

## D. netcfg 架构不支持 / 字段忽略（预期：优雅告警 + 应用受支持部分，不崩溃）

| # | example | 预期 | 状态 |
|---|---------|------|------|
| D1 | offload.yaml | 忽略 offload 字段，应用以太网地址 | ⬜ |
| D2 | dhcp_wired8021x.yaml | 忽略 802.1x auth，处理 dhcp/地址 | ⬜ |
| D3 | network_manager.yaml | 忽略 renderer，应用设备 | ⬜ |
| D4 | infiniband.yaml | 不支持 IB，告警/跳过不崩溃 | ⬜ |
| D5 | wireless.yaml | warnUnsupportedConfig 告警 wifis | ⬜ |
| D6 | wireless_adhoc.yaml | 同上 | ⬜ |
| D7 | wireless_wpa3.yaml | 同上 | ⬜ |
| D8 | wpa3_enterprise.yaml | 同上 | ⬜ |
| D9 | wpa_enterprise.yaml | 同上 | ⬜ |
| D10 | modem.yaml | 告警 modems | ⬜ |
| D11 | openvswitch.yaml | 告警 openvswitch | ⬜ |
| D12 | sriov.yaml | SR-IOV 字段忽略/告警，应用受支持部分 | ⬜ |
| D13 | sriov_vlan.yaml | 同上 | ⬜ |

---

## 发现的 Bug / 修复记录

（按发现顺序追加；每个 bug 记：example、现象、根因、修复 commit/文件）

- **BUG-1**（A10 virtual-ethernet）：netplan 标准键 `virtual-ethernets:`（两端各为顶层条目、`peer:` 互相引用名字）netcfg 不识别，只支持自有的 `veth-devices:`（单条目嵌套 peer）。现象：veth 未创建，引用它的 bridge 加成员失败。修复：新增 `virtual-ethernets` schema（config）+ `setupVirtualEthernets`（从互引条目建 veth pair，done 去重）；并把它放到 bond/bridge **之前**创建（端点常作 bridge 成员，否则 enslave 失败）。`veth-devices` 作为跨 netns 扩展保留。状态：✅ 已修复并验证。
- **BUG-2**（A12 wireguard）：netplan 把 WireGuard 表达为 `tunnels: mode: wireguard`（带 key/port/mark/peers），但 netcfg 的 `Tunnel` struct 无这些字段 → 仅建 wireguard 设备、未配密钥/peer（`wg show` 空）。修复：`Tunnel` 增加 wireguard 字段（port/mark/peers，key 复用为私钥）+ netplan peer 类型 `TunnelWireguardPeer`(keys.public/shared, keepalive)；`setupTunnels` 在 mode==wireguard 时经 `configureTunnelWireguard`→wgctrl 配置。验证：`wg show wg0` 显示公钥/端口/fwmark/peer/preshared/endpoint/allowed-ips 全部生效，两端已握手。状态：✅ 已修复并验证。
  - 注：netcfg 自有顶层 `wireguards:` 是自造语法（netplan 用 tunnels:mode:wireguard），暂保留向后兼容，去留待用户决定（见对话）。
- **BUG-3**（A9 vxlan）：netplan 用 `tunnels: mode: vxlan`，netcfg 自造顶层 `vxlans:` → 不识别 tunnels 里的 vxlan。修复：`Normalize` 把 `tunnels:mode:vxlan` 翻译进 Vxlans（含 Tunnel 增 id/link/mac-learning/neigh-suppress 字段 + toVxlan），使其在 bridge **之前**创建。✅ 已修复验证。`vxlans:` 自造键同 wireguards，去留待定。
- **BUG-4**（A5 bonding）：`SetBondSlave` 直接 LinkSetMaster，但内核要求成员先 down → "operation not permitted"。修复：down→enslave→up。✅
- **BUG-5**（A8 vrf）：`AddRule` 的 from/to 用 net.ParseCIDR 拒绝裸主机 IP（netplan 允许）。修复：新增 `parseCIDROrIP`（裸 IP 按 /32 或 /128）。✅
- **BUG-6**（A9 vxlan）：neigh-suppress 是 brport 属性，在 vxlan 加入 bridge 前设置必然失败。修复：移到 setupBridges 之后的 `applyVxlanNeighSuppress` 统一处理。✅
- **顺序修复**（A10/A9）：veth/virtual-ethernets 与 tunnels-vxlan 端点常作 bridge 成员，须在 bond/bridge **之前**创建。已调整 applyNamespaceConfig 顺序 / Normalize 翻译。

## 备注

- DHCP 真实租约获取需 netns 内有 DHCP 服务器，本计划只验证"发起不崩溃"；完整 DORA 验证另列。
- netns 相关功能 netplan examples 不涉及（netplan 无 netns），不在本计划。

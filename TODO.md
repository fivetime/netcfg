# netcfg TODO 与 netplan 对齐路线图

> 本文档基于一次 netcfg ↔ netplan（官方实现，`C:/MyProjects/OpenSource/netplan`）的逐字段功能核对结果整理。
> 核对基准：`netplan/doc/netplan-yaml.md`（YAML schema 参考）+ CLI 命令 + 后端实现。
>
> **核对结论**：netcfg 实现了 netplan 的核心子集（YAML 属性级约 40% 完整），并在 netns / WireGuard / VXLAN-EVPN 等方向做了 netplan 没有的扩展。但存在三类缺口，按优先级分为 P0~P3。
>
> 任务标注说明：
> - 工作量：**S**=半天内 · **M**=1~3 天 · **L**=3 天以上
> - 状态：`[ ]` 未开始 · `[~]` 进行中 · `[x]` 已完成

---

## 里程碑总览

| 里程碑 | 目标 | 包含优先级 | 验收口径 |
|--------|------|-----------|---------|
| **M1 — 修复"假支持"** ✅ | 消除"配置可写入但不生效"的字段，让文档承诺与实际行为一致 | P0 (8/8 完成) | 已声明支持的字段必须真正下发到内核，或在解析期明确报错 |
| **M2 — 对齐常用属性** | 补齐 netplan 中高频使用但 netcfg 缺失的属性 | P1 | netplan 常见配置可平滑迁移到 netcfg |
| **M3 — 补齐设备与高级功能** | 新增缺失设备类型与高级能力 | P2 | 覆盖 wifi/modem/SR-IOV/offload 等 |
| **M4 — 架构级差异决策** | 明确 OVS / NetworkManager / networkd 后端等是否实现或显式不做 | P3 | 给出明确的「实现」或「不做（附理由）」决议 |
| **M5 — 工程化** | 测试、校验、文档 | 贯穿全程 | 单测 + 集成测试 + 配置校验 |

---

## 🔴 P0 — 修复"假支持"字段（M1，最高优先级）

> 这类字段在 `config/config.go` 中已定义 struct + yaml tag，用户写进 YAML **不会报错**，但 `cmd/apply.go` 从未把它们下发到内核 —— 属于"静默失效"，比直接缺失更危险。
> **共性验收标准**：每项要么真正生效（apply 后用 `ip`/`bridge` 命令可验证），要么在解析/校验阶段明确报"暂不支持"，禁止静默忽略。

### P0-1 Bond parameters 完整应用 · **M** · ✅ 已完成
- [x] 在 `setupBonds()` (cmd/apply.go) 中应用 `BondParameters` 全部子字段（原先仅应用 `mode`）
- 实现：`netlink/netlink.go` 新增 `BondOptions` 结构 + 重写 `AddBond(name, *BondOptions)`，复用库的 `StringToBondLacpRateMap`/`StringToBondXmitHashPolicyMap` 与 `NewLinkBond` 的 -1 哨兵机制（仅下发用户显式设置的字段）；其余 5 个枚举(ad-select/arp-validate/arp-all-targets/fail-over-mac/primary-reselect)用本地小解析器。`cmd/apply.go` 新增 `bondOptionsFromConfig` 做 config→netlink 映射
- 设计决策：① 枚举拼写错误返回错误（不静默失败）；② primary 接口名→ifindex，缺失时 warn 跳过；③ 参数在创建时一次性下发（多数参数要求 bond 无 slave 时设置）；④ bond 已存在时 warn 提示需重建才能改参数
- 映射说明：`gratuitous-arp`→`NumPeerNotif`(num_grat_arp)；`all-slaves-active` bool→0/1
- 验收：`GOOS=linux go build/vet` 通过；运行期验证 `cat /proc/net/bonding/bond0` 待真机测试（见 M5 集成测试）

### P0-2 Bridge parameters 完整应用 · **M** · ✅ 已完成
- [x] 在 `setupBridges()` (cmd/apply.go) 中应用 `BridgeParameters` 全部字段（原先仅 `vlan-filtering` 与 FDB/邻居）
- 实现：`netlink/netlink.go` 新增 `BridgeOptions` + `SetBridgeParameters`（设备级 STP）+ `SetBridgePortPathCost`/`SetBridgePortPriority`（每端口）。`cmd/apply.go` 新增 `bridgeOptionsFromConfig` 映射
- 关键约束：v1.1.0 库的 `netlink.Bridge` 仅支持 3 个字段，STP/forward-delay 等**无法走 netlink**，故沿用项目既有 sysfs 模式（写 `/sys/class/net/<br>/bridge/`）。顺带抽出 `writeSysfs`/`writeSysfsInt`/`writeSysfsBool` helper 并重构了 vlan-filtering/neigh-suppress/learning 三处重复代码
- 设计决策：① 时间参数 config 用秒、sysfs 用 1/100 秒(USER_HZ)，写入时 ×100；② bridge sysfs 可热更新，无论设备是否新建都应用（与 bond 不同）；③ path-cost/port-priority 是每端口属性，须在 enslave 之后设置；④ 逐项尽力而为 + `errors.Join` 汇总失败
- 验收：`GOOS=linux go build/vet` 通过；运行期 `ip -d link show br0` / `bridge link` 验证待真机（见 M5）

### P0-3 Route 高级字段应用 · **M** · ✅ 已完成
- [x] 在 `addRoute()` (cmd/apply.go) 与 netlink 层处理原先被忽略的字段：`from, scope, type, on-link, mtu`
- 实现：`netlink/netlink.go` 新增 `RouteOptions` + `AddRouteOpts`，并把旧 `AddRoute(dst,gw,dev,metric,table)` 改为薄包装（两处默认网关调用方不动，零重复）。新增 `parseRouteScope`/`parseRouteType` 解析器
- 字段映射：`from`→`route.Src`(RTA_PREFSRC)；`scope`→`route.Scope`(netlink.SCOPE_*)；`type`→`route.Type`(unix.RTN_*)；`on-link`→`route.Flags|=FLAG_ONLINK`；`mtu`→`route.MTU`(RTAX_MTU)
- 设计决策：① 非法 scope/type 字符串返回错误（不静默失败）；② scope 未指定时不设置，保持内核默认行为（避免回归既有 via 路由）；③ 新增 `golang.org/x/sys/unix` 直接 import（已是传递依赖，构建无需改 go.mod；未跑 `go mod tidy` 以免误引入 purego 的 DHCP 依赖）
- 验收：`GOOS=linux go build/vet` 通过；运行期 `ip route show` 验证 blackhole/scope/onlink 待真机（见 M5）

### P0-4 DNS（nameservers）实际写入 · **M** · ✅ 已完成
- [x] apply 时把各设备的 `Nameservers`（addresses + search）写入系统 DNS
- 决策（已确认）：**集成 resolvectl** —— 检测到 systemd-resolved 时用 `resolvectl dns/domain <iface>` 按接口下发（per-link，与 netplan 一致）；否则回退写 `/etc/resolv.conf`
- 实现：`netlink/dhcp.go` 新增 `ApplyDNS(iface, addresses, search)` + `usingSystemdResolved`/`applyDNSResolvectl`/`writeResolvConf`。`cmd/apply.go` 新增 `applyNameservers` helper，接入全部 8 个含 Nameservers 字段的设备类型（ethernet/dummy/vlan/bond/bridge/veth/ipvlan/macvlan+macvtap/tun+tap）
- 边界处理：resolved 在运行但无 resolvectl → 告警并回退 resolv.conf（不静默丢弃）；resolv.conf 回退是全局的，多接口场景后者覆盖前者（已在注释标明）
- 未覆盖：Vxlan/Tunnel/Wireguard/Vrf/VethPeer 无 Nameservers 字段，N/A
- 验收：`GOOS=linux go build/vet` 通过；运行期 `resolvectl status` / `cat /etc/resolv.conf` 验证待真机（见 M5）

### P0-5 optional / link-local 生效或显式拒绝 · **S** · ✅ 已完成
- [x] `optional`：实现 `netcfg status --wait`（+ `--wait-timeout`），等待 default ns 中未标记 optional 的 ethernet 就绪，optional:true 接口不阻塞。一举两得——赋予 optional 语义 + 修复潜在 bug：`netcfg-wait-online.service` 原本调用 `status --wait` 但该 flag 不存在（cobra 会因未知 flag 报错，服务必然失败）
- [x] `link-local`：IPv6 LL 通过 `addr_gen_mode` sysctl 控制（`netlink.SetLinkLocalIPv6`，enable→0/EUI64，disable→1/none）；IPv4 LL（169.254 zeroconf）无直接 netlink/sysctl 开关，**显式告警不支持**（不静默忽略）。在 `setupDeviceWithDHCP` 接入，仅用户显式设置（含空列表 `[]`）时处理
- 实现：`netlink/dhcp.go` 新增 `SetLinkLocalIPv6`；`cmd/status.go` 新增 `waitOnline`/`linkReady` + `--wait`/`--wait-timeout` flag；`cmd/apply.go` 接入 link-local
- 注意：addr_gen_mode 需在 LL 生成前设置才彻底生效，对已有 LL 的接口不移除既有地址（已注释）
- 验收：`GOOS=linux go build/vet` 通过；运行期 `systemctl start netcfg-wait-online` / `ip addr` 验证待真机（见 M5）

### P0-6 ipv6-privacy 接线 · **S** · ✅ 已完成
- [x] 选择「接入 apply」而非移除（netplan 支持 `ipv6-privacy`，`SetIPv6Privacy` 现成）
- [x] `config.Ethernet` 新增 `IPv6Privacy *bool`（config.go，nil=未设置，与 AcceptRA 同模式）
- [x] `setupDeviceWithDHCP()` (cmd/apply.go) 在 SLAAC 块之后调用 `nl.SetIPv6Privacy`，使显式设置覆盖 `EnableSLAAC` 默认写入的 use_tempaddr=2
- 映射：`true`→use_tempaddr=2（启用，偏好临时地址）；`false`→0（禁用）
- 验收：`GOOS=linux go build/vet` 通过；运行期 `sysctl net.ipv6.conf.<dev>.use_tempaddr` 验证待真机（见 M5）

### P0-7 VRF routing-policy 支持 · **S** · ✅ 已完成
- [x] `Vrf` struct 新增 `RoutingPolicy []*RoutingPolicy` 字段（config.go）
- [x] 在 `setupVrfs()` (cmd/apply.go) 中应用 VRF 级策略路由，复用既有 `addRoutingPolicy` helper + `AddRule`（与 default-ns routing-policy 同一套实现，零新增逻辑）
- 说明：策略路由规则不纳入 state 跟踪（与 default-ns routing-policy 行为一致），无需改 state.go
- 验收：`GOOS=linux go build/vet` 通过；运行期 `ip rule show` 验证待真机（见 M5）

### P0-8 文档与代码对齐 · **S** · ✅ 已完成
- [x] Bond/Bridge：P0-1/P0-2 完成后 ✅ 标注已准确，无需改回"部分"
- [x] 修正 `INTRODUCTION.md` 名不副实的 ✅ → ⏳：混杂模式(无实现)、Wake-on-LAN(字段在、未应用→P2-6)、接口重命名/match(未实现→P1-5)；同时更新已完成项描述（optional/DNS/静态路由全字段/链路本地/ipv6-privacy/VRF 策略路由）
- [x] `config` 增加未支持配置段告警：新增 `warnUnsupportedConfig`，解析时对 netplan 中存在但 netcfg 不支持的 `network.*` 段（wifis/modems/openvswitch/nm-devices 等）及未知顶层键输出 warning（仅告警不阻断）。已 smoke 验证 wifis/openvswitch/未知键均触发、ethernets 等受支持键不触发
- 说明：本次覆盖**顶层配置段**告警；字段级"已知但未实现"（如 ethernets 下 match/set-name/auth）的告警留待 P1 实现这些字段时一并处理
- 验收：`GOOS=linux go build/vet` 通过；config 包 smoke test 通过

---

## 🟡 P1 — 对齐 netplan 常用属性（M2）

### P1-1 DHCP overrides · **L** · ✅ v4+v6 完成（daemon v6 续约待补）
- [x] config schema：`DHCP4Overrides`/`DHCP6Overrides`（`DHCPOverrides` 类型；use-domains 用 `interface{}` 兼容 bool/"route"）
- [x] `netlink.DHCPOverrides` + `ApplyDHCPv4Lease(iface, lease, *DHCPOverrides)` honor **use-dns / use-mtu / use-routes / route-metric / use-domains**（应用 lease 时真实生效）
- [x] 请求侧 **send-hostname / hostname**：`DHCPManager.SetHostname`/`SetSendHostname`，apply 路径在请求前配置
- [x] daemon 路径：`RequestLease(..., v4ov, v6ov)` + `LeaseState.V4Overrides`（持久化，续约时复用，重启后仍 honor）；`cmd/daemon.go` 经 `dhcpOverridesToNl` 下传
- [x] **顺带补 P2-4 gap**：`setupDeviceWithDHCP` 原先只 `RequestDHCPv4` 丢弃 lease（外部客户端时代靠 dhclient 自己配），纯 Go 改造后必须显式 `ApplyDHCPv4Lease`——现已补上（带 overrides）
- 已 smoke 验证：use-dns/use-routes/route-metric/send-hostname/hostname/use-domains(route) 解析正确
- no-op（已显式告警）：`use-ntp`（netcfg 不配 NTP）、`use-hostname`（不设系统主机名）
- [x] **DHCPv6 lease 应用 + overrides**：新增 `ApplyDHCPv6Lease`（逐个加 IA_NA /128 地址，不 flush；honor use-dns/use-domains；IA_PD 仅记录不自动配）。apply 命令与 daemon 两条路径都接入（顺带修了 v6 与 v4 同样的「请求了却不应用 lease」gap）。注：DHCPv6 不下发网关/MTU（来自 RA），故 use-routes/use-mtu/route-metric 对 v6 天然 N/A；purego v6 client 无 SetHostname，故 v6 无请求侧 hostname；daemon 暂不续约 v6（renewal loop 仅 v4，待补）
- 注意：`ApplyDHCPv4Lease` 会 flush 设备全部 v4 地址再加 lease（既有行为）——同设备混用静态+dhcp4 时静态地址会被清，属既有限制
- 验收：`GOOS=linux go build/vet` 通过；config smoke test 通过；运行期真机（需 CAP_NET_RAW）见 M5

### P1-2 IPv6 RA overrides · **M** · 🟡 schema + 告警（完整实现需后端，延后）
- 调查结论：`ra-overrides`（use-dns/use-domains/table）**本质是 networkd 后端特性**，netplan 文档明确 "only supported with networkd back end"。netcfg 用内核 RA（accept-ra），无用户态 RA/NDISC 客户端：use-dns/use-domains 的 RA DNS/域名无人消费，table 也无 per-interface sysctl 可重定向内核 RA 路由。直接 netlink 架构下无法干净实现，与 P1-6 延后批同类。
- [x] 加 `RAOverrides` schema（config.Ethernet.RAOverrides；use-domains 用 `interface{}` 兼容 bool 与 "route"，不破坏解析；已 smoke 验证 route/true/false 均可解析）
- [x] apply 时显式告警「ra-overrides 未生效，需 networkd 后端」——把原本的静默忽略转为明确提示
- [ ] 完整实现：需引入用户态 RA/NDISC 客户端（大工程），延后
- 验收：`GOOS=linux go build/vet` 通过；config 包 smoke test 通过

### P1-3 802.1X 认证（有线） · **L** · ✅ 已完成（生成 conf + 直接 spawn，init-agnostic）
- 决策（已确认）：802.1x 物理上必须 wpa_supplicant（内核不做 EAP）。采用「生成配置 + 交给 systemd 启动」方式，不在 netcfg 进程内管理 supplicant 生命周期
- [x] config 新增 `Auth` schema（Ethernet.Auth，与未来 WiFi 共用）：key-management/method/identity/anonymous-identity/password/ca-certificate/client-certificate/client-key/client-key-password/phase2-auth
- [x] `cmd/dot1x.go` `setup8021x`：key-management 为 802.1x（→key_mgmt=IEEE8021X）或 eap*（→WPA-EAP）时，生成 `/etc/netcfg/wpa-<iface>.conf`(0600) + `/etc/systemd/system/netcfg-8021x-<iface>.service`，并 best-effort `systemctl enable --now`
- [x] setupEthernets 接入（设备 up 后，cfg.Auth 存在则调用）
- 真机验证：dhcp_wired8021x.yaml 生成的 conf（IEEE8021X/TTLS/identity/password）与 unit（wpa_supplicant -D wired）正确；缺 wpa_supplicant/systemctl 时优雅告警不中断
- 说明：psk/sae（WiFi）不在此处理，留给 P2-1 WiFi（共用 Auth 块）
- 原 P1-3 子项（未完成清单）：
- [ ] 新增 `auth` 块：`key-management, method, identity, anonymous-identity, password, ca-certificate, client-certificate, client-key, client-key-password, phase2-auth`（netplan-yaml.md:898-965）
- 影响文件：`config/config.go`、新增 `netlink/8021x.go`（需集成 wpa_supplicant 或 systemd-networkd）
- 依赖：实现机制需调研（netcfg 直接 netlink，802.1X 需用户态 supplicant）

### P1-4 地址高级选项 · **M** · 🟡 lifetime/label 完成
- [x] `addresses[].lifetime`、`addresses[].label`
- 实现：`config` 新增 `Address` 类型 + 自定义 `UnmarshalYAML`，**同一 addresses 列表内同时支持纯字符串（旧写法）与单键映射 `cidr: {lifetime, label}`**；lifetime 用 `interface{}` 兼容裸整数 `0` 与字符串 `forever`。13 个设备结构的 `addresses` 字段 `[]string`→`[]Address`（state 仍以字符串存储，`buildNsState` 经 `addrStrings` 转换）。`netlink` 新增 `AddAddressOpts`（label→`Addr.Label`；lifetime `0`→`PreferedLft=0`+`ValidLft=lftForever` 弃用语义，对齐 networkd），`AddAddress` 改薄包装。`setupDevice` 签名 `[]string`→`[]config.Address`（调用点不变）
- 已 smoke 验证：纯字符串 / map+裸整数 lifetime+label / forever 三种写法均正确解析，旧纯字符串写法向后兼容
- 注意：`Nameservers.Addresses`（DNS 服务器 IP）保持 `[]string`，不受影响
- [ ] `ipv6-address-generation`、`ipv6-address-token`（netplan-yaml.md:451-457）—— 延后（address-token 需 sysctl `stable_secret`/token 设置，address-generation 与 addr_gen_mode 相关，单独处理）
- 验收：`GOOS=linux go build/vet` 通过；config 包 smoke test 通过；运行期 `ip addr`（label/弃用 lft）待真机（见 M5）

### P1-5 物理设备 match / set-name 真正生效 · **M** · ✅ 已完成
- [x] `match`（name/macaddress/driver）按规则在既有设备中查找；name/driver 支持 glob
- [x] `set-name` 设备重命名（down→rename→恢复 up）
- 实现：`netlink/netlink.go` 新增 `MatchCriteria` + `FindMatchingLink`（多匹配时按名排序取首个，确定性）+ `RenameLink` + `linkDriver`（读 `/sys/class/net/<dev>/device/driver` symlink basename，避免引入 ethtool 依赖）。`cmd/apply.go` 新增 `resolveEthernetName`，在 `setupEthernets` 循环开头解析：有 match→查找+按 set-name/key 重命名；无 match→key 即设备名（原行为）；无匹配→告警跳过
- netplan 语义：config key 是 id；有 match 时应用到匹配设备，set-name 重命名匹配设备
- 已知限制：① match 主要面向 default ns 物理网卡，driver 匹配依赖 host /sys，netns 内匹配为边界情况；② state 跟踪按 config id 记录，set-name 重命名后地址*移除*的 diff 可能不匹配实际设备名（ethernet 为 system 设备不删除，apply 经 HasAddress 幂等，影响有限）——留待 state 重构时处理
- 验收：`GOOS=linux go build/vet` 通过；运行期 `ip link`（重命名）/ glob 匹配验证待真机（见 M5）

### P1-6 其余通用属性 · **S~M** · 🟡 部分完成
已完成（直接 netlink/sysctl 可干净落地）：
- [x] **Tunnel `tos`**：`config.Tunnel` 补 `TOS` 字段 + setupTunnels 传入（`TunnelOptions`/`AddTunnel` 本就支持，仅缺这两处接线）
- [x] **`ipv6-mtu`**：`config.Ethernet` 补 `IPv6MTU` + `netlink.SetIPv6MTU`（sysctl `/proc/sys/net/ipv6/conf/<dev>/mtu`）+ setupDeviceWithDHCP 接线

延后（需后端语义 / DHCP 客户端 / schema 设计，不适合在直接 netlink 下硬塞）：
- [ ] **bridge 端口 `neigh-suppress` / `hairpin` / `port-mac-learning`**：均为 brport sysfs 属性（neigh-suppress/learning 已有 netlink helper，hairpin 需加 `hairpin_mode` helper）。难点是 schema——netplan 把这些放在被桥接的成员设备上，需在成员 config 加字段并在 enslave 后应用。建议作为独立小特性专门做
- [ ] **`activation-mode`**（manual/off）：networkd 概念。"off"=不 up 设备可部分映射，但跨所有 setup 函数的 up/down 流程，需统一改造
- [ ] **`ignore-carrier`**：networkd ConfigureWithoutCarrier，无干净的直接 netlink 等价
- [ ] **`critical`**：networkd critical-connection（down 时不清配置），netcfg 本就不随 carrier 清配置，语义不直接对应
- [ ] **`dhcp-identifier`**（mac/duid）：需 DHCP 客户端集成，绑定到 P2-4 纯 Go DHCP
- [ ] **`optional-addresses`**：online 语义，绑定到 wait-online（P0-5 的延伸）
- 验收（已完成项）：`GOOS=linux go build/vet` 通过；运行期 `ip -d link`(tunnel tos) / `sysctl ...ipv6.conf.<dev>.mtu` 待真机（见 M5）

---

## 🟢 P2 — 补齐设备类型与高级功能（M3）

### P2-1 WiFi 无线网络 · **L** · ✅ 已完成（生成 conf + 直接 spawn，init-agnostic）
- config 新增 `wifis`（Wifi/AccessPoint，复用 802.1x 的 `auth` 块）；设备级地址/路由/DHCP 复用以太网路径
- `cmd/wifi.go` `setupWifis`：生成 `/etc/netcfg/wpa-<iface>.conf`（每 AP 一个 network 块）+ systemd unit（wpa_supplicant -D nl80211），best-effort systemctl enable；regulatory-domain 经 iw reg set
- AP 鉴权：password→PSK；auth.key-management = sae(SAE/WPA3) / eap*(WPA-EAP, 复用 writeEAPFields) / none(开放)；mode/bssid/hidden 均支持
- 真机验证：PSK / SAE / EAP-TLS / EAP-TTLS / open 四类 conf 正确，wlan 地址应用正常
- [ ] `wifis.*`：access-points（password, mode, band, channel, bssid, hidden）, auth(WPA/WPA2/WPA3/Enterprise), wakeonwlan, regulatory-domain（netplan-yaml.md:1153-1247）
- 需集成 wpa_supplicant 或 iwd

### P2-2 Modem 移动网络 · **L**
- [ ] `modems.*`：apn, auto-config, device-id, network-id, pin, sim-id, sim-operator-id, username, password, mtu, number（netplan-yaml.md:1067-1152）
- 需集成 ModemManager

### P2-3 SR-IOV 支持 · **L** · 🟡 核心完成（VF count + eswitch）
- config.Ethernet 新增 `virtual-function-count` / `embedded-switch-mode` / `delay-virtual-functions-rebind` / `link`（VF→PF）
- `cmd/sriov.go` `applySRIOV`：VF 数量经 sysfs `/sys/class/net/<pf>/device/sriov_numvfs`（改前先归零）；eswitch 模式经 `devlink dev eswitch set pci/<addr> mode <mode>`（PCI 地址从 sysfs symlink 取，best-effort 需 devlink）
- setupEthernets 加 SR-IOV 预处理 pass（先建 VF 再配置设备）
- 验证：非 SR-IOV 设备上 numvfs 写入/eswitch 均 best-effort 告警跳过、rc=0 不崩溃（实际 SR-IOV 需硬件验证）
- 已知限制/延后：① VF netdev 异步出现，其上配置可能需 VF 就绪后/再次 apply（SR-IOV 固有，netplan 用 rebind 解决→P3-5）；② 硬件 VLAN 过滤（vlan 上 `renderer: sriov`，sriov_vlan.yaml 的 vlan2_hw，即 `ip link set <pf> vf <n> vlan <id>`）未实现；③ delay-virtual-functions-rebind 仅提示，绑定 P3-5 rebind 命令
- [ ] `virtual-function-count, embedded-switch-mode, delay-virtual-functions-rebind, link`（netplan-yaml.md:1014-1056）
- [ ] 对应 `netcfg rebind` 命令（见 P3-5）
- 需操作 sysfs

### P2-4 纯 Go DHCP 客户端接入 · **M** · 🟡 客户端完成，relay 延后
- 决策（已确认）：**默认启用 + 外部兜底**（去 build tag，纯 Go 为主路径，失败回退外部客户端）
- [x] 引入依赖：`go get github.com/insomniacslk/dhcp` + `go mod tidy`（go.mod 新增 insomniacslk/dhcp + 传递依赖 mdlayher/packet、u-root/uio、pierrec/lz4；go directive 1.21→**1.23.0** + toolchain go1.24.4，为依赖所需 ⚠️ CI 需同步）
- [x] 去掉 `dhcp4_client.go`/`dhcp6_client.go` 的 `purego` build tag，默认编译（已验证对最新依赖无 API 漂移）
- [x] 接入主路径：`RequestDHCPv4/6WithContext` 先试纯 Go（`requestDHCPv4/6PureGo`），失败回退外部客户端；新增 `purego4/6ToLease` 类型映射
- [x] 真正的 T1 续约：`RenewDHCPv4` 用纯 Go `client.Renew` 单播续约（带当前 lease 的 ServerIP），失败回退完整 DORA
- [ ] **relay 延后**：`dhcp_relay.go` 仍带 `purego` tag、默认不编译——对最新 insomniacslk/dhcp 有 API 漂移（Clone/RelayOptions/sub-option），且 `RelayServer.Run()` 仅伪代码。作为独立特性后续修复
- [ ] DHCPv6 真续约（`RenewDHCPv6`）：目前 v6 仅请求路径接入，续约可后续补（purego v6 client 已有 Renew/Rebind）
- 验收：`GOOS=linux go build/vet`（默认 tag）通过；运行期真机需 CAP_NET_RAW（raw socket）验证 DORA/续约（见 M5）

### P2-5 网卡 Offload 配置 · **M** · ✅ 已完成（ethtool -K，best-effort）
- config.Ethernet 新增 7 个 *bool offload 字段（receive/transmit-checksum-offload、tcp/tcp6-segmentation-offload、generic-segmentation/receive-offload、large-receive-offload）
- `cmd/offload.go` `applyOffload`：把已设字段拼成一条 `ethtool -K <iface> rx/tx/tso/tx-tcp6-segmentation/gso/gro/lro on|off`（tcp6 无短名用完整 feature 名）；ethtool 缺失或设备不支持某 feature → best-effort 告警不中断
- setupEthernets 接入（设备 up 后）
- 真机验证：在 veth 上实跑 offload.yaml，rx/tx-checksum、tso/gso/gro 均正确生效（lro 因 veth fixed 不可改，属硬件限制）
- 选型：用 ethtool 友好名（处理 tx-checksum 分组/版本差异），与 iw / 外部 DHCP 客户端同属"标配小工具 best-effort"，仅配置 offload 时才需 ethtool
- [ ] `receive/transmit-checksum-offload, tcp/tcp6-segmentation-offload, generic-segmentation-offload, generic-receive-offload, large-receive-offload`（netplan-yaml.md:169-209）
- 需调用 ethtool（或 ethtool netlink）

### P2-6 其他设备级缺口 · **S~M** · 🟡 netplan 对齐项完成
已完成（netplan 有、本地可做）：
- [x] **wakeonlan 实际生效**：`ethtool -s <dev> wol g`（cmd/offload.go applyWakeonlan）
- [x] **infiniband-mode**：sysfs `/sys/class/net/<dev>/mode`（connected/datagram）
- [x] **emit-lldp**：netplan 自身注明"networkd back end only"，内核无 LLDP 发送开关 → schema + 显式告警（不静默）
延后/不做：
- tunnel encap(fou/gue) / GRE checksum / 6in4-4in6：**netplan tunnels 无这些字段**，按"不自造语法"不加
- tun/tap 高级(user/group/multi-queue/vnet-hdr)：netplan 无 tun/tap 设备类型（netcfg 扩展），非对齐项，低优先延后
- 验证：veth 上 wakeonlan 成功、infiniband-mode 非 IB 设备优雅告警、emit-lldp 告警，rc=0
- [ ] `emit-lldp`（netplan-yaml.md:165）
- [ ] `infiniband-mode`（connected/datagram，netplan-yaml.md:1058）
- [ ] Tunnel 高级：encap(fou/gue)、GRE key/checksum、6in4/4in6
- [ ] TUN/TAP 高级：user/group 权限、multi-queue、vnet-hdr
- [ ] `wakeonlan` 实际生效（config.go:101 已定义未应用）

---

## 🔵 P3 — 架构级差异（M4，需先决策再实施）

> netplan 是"配置生成器"（生成 networkd/NM/OVS 配置文件由后端服务应用）；netcfg 是"直接 netlink 应用器"。以下功能与 netcfg 架构存在根本冲突，**每项需先决议「实现 / 适配 / 明确不做」**。

### P3-1 OpenVSwitch 支持 · **L** · ⚠️决策
- [ ] `openvswitch.*` 全局与设备级（external-ids, other-config, lacp, fail-mode, controller, ports, protocols, ssl，netplan-yaml.md:2164+）
- 需调用 ovs-vsctl（与"直接 netlink"理念不同，但可作为独立后端模块）
- 现状：`tests/unsupported/03-openvswitch.yaml` 已标记不支持

### P3-2 NetworkManager 后端 / passthrough · ⚠️决策
- [ ] `renderer: NetworkManager`、`nm-devices` passthrough（netplan-yaml.md:2130-2163）
- 建议：明确「不做」并在文档说明（netcfg 定位为无后端依赖）。`Network.Renderer` 字段 (config.go:40) 当前定义但未使用，需决定保留语义或移除

### P3-3 systemd-networkd 后端生成 · ⚠️决策
- [ ] 是否提供"生成 .network/.netdev/.link"模式以兼容 networkd 专属选项
- 建议：默认「不做」，作为可选导出功能评估

### P3-4 D-Bus 接口 · ⚠️决策
- [ ] netplan 提供 `netplan-dbus`（src/dbus.c）。netcfg 是否需要 D-Bus API 供远程管理

### P3-5 缺失 CLI 命令对齐 · **M**
- [ ] `netcfg ip leases <iface>`（查看 DHCP 租约，对标 `netplan ip leases`）
- [ ] `netcfg info`（显示特性标志/版本，对标 `netplan info`）
- [ ] `netcfg migrate`（从 /etc/network/interfaces 迁移，对标 `netplan migrate`）
- [ ] `netcfg rebind`（SR-IOV VF 重绑定，对标 `netplan rebind`，依赖 P2-3）
- 影响文件：`cmd/`

---

## 🛠️ 工程化任务（M5，贯穿全程）

- [~] **单元测试覆盖**：
  - [x] config 解析/合并/Normalize + Address/DHCPOverrides/RAOverrides 解析（config_test.go，cover 54.9%）
  - [x] state diff（ComputeDiff 含 system/netcfg 删除规则、地址/路由 diff、namespace 增删）+ CRUD（state_test.go，cover 73.0%）
  - [ ] netlink 参数映射（bond/bridge/route/dhcp overrides 的 *OptionsFromConfig / parse* —— 这些在 netlink/cmd 包，netlink 仅 linux 可编译，需 GOOS=linux 测试环境或拆出纯函数）
- [ ] **集成测试**：基于 netns 的端到端测试（`tests/` 目录已有 supported/unsupported/netns 用例骨架，但无 `.go` 测试）
- [ ] **配置校验增强**：解析期校验字段合法性、未知字段告警（与 P0-8 联动）
- [ ] **man page 文档**
- [ ] 每个 P0/P1 任务完成时同步更新 README/INTRODUCTION 的对比表

---

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
- [x] Bridge 网桥 (含 VLAN filtering + 完整 STP parameters，见 P0-2)
- [x] Bond 链路聚合（含完整 parameters，见 P0-1）
- [x] VLAN 802.1Q
- [x] VRF 路由隔离（含 routing-policy，见 P0-7）
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

### VXLAN 完整支持（部分能力超出 netplan）
- [x] VXLAN 设备创建
- [x] external/flow-based 模式 (OVS/TC/OVN)
- [x] learning 开关
- [x] arp-proxy, neigh-suppress
- [x] l2miss, l3miss 通知
- [x] rsc (Route Short Circuit)
- [x] noage, ageing (FDB 老化)
- [x] limit (FDB 条目限制)
- [x] gbp (Group Based Policy)
- [x] port-range (源端口范围，ECMP) — netcfg 扩展
- [x] udp-checksum, udp6-zero-csum-tx/rx
- [x] 静态 FDB 条目管理 — EVPN 扩展
- [x] 静态 Neighbor 条目管理 — EVPN 扩展

### 网络配置
- [x] 静态 IP 地址
- [x] 静态路由（含 from/scope/type/on-link/mtu，见 P0-3）
- [x] 策略路由 (routing-policy)
- [x] DNS nameservers（resolvectl / resolv.conf，见 P0-4）
- [x] DHCPv4 (外部客户端)
- [x] DHCPv6 (外部客户端)
- [x] IPv6 SLAAC/RA
- [x] IPv6 隐私扩展 ipv6-privacy（见 P0-6）
- [x] post-script 支持

### 运维能力（netcfg 独有，netplan 无）
- [x] 配置变更检测和增量应用 - `netcfg diff` 预览变更，apply 自动清理废弃资源
- [x] 热重载支持 (SIGHUP) - daemon 模式已支持
- [x] 状态持久化和恢复 - 配置状态保存在 /var/lib/netcfg/state.json
- [x] netns 管理命令 (netns list/create/delete/exec)
- [x] 配置试用自动回滚 (`netcfg try --timeout`)

# netcfg 内核态 SRv6 (seg6) 设计

## 目标
为 netcfg 内核后端增加 **SRv6 (Segment Routing over IPv6)** 支持，覆盖：
1. **transit / 引流**（seg6 encap）：把匹配流量压入 SRH，按段列表转发（`encap` / `inline` 模式）。
2. **本地 SID / endpoint 行为**（seg6local）：本节点对落到某 SID 的报文执行 End/End.X/End.DT* 等行为。
3. **seg6_enabled sysctl**：全局与按接口启用 SRv6 报文处理。

SRv6 非 netplan 标准，属 netcfg「增加的功能」（与 netns/VPP/NAT 同类）。语法自定，但贴近
iproute2 (`ip route ... encap seg6 / seg6local`) 语义，并复用 netplan 既有 `routes:` 习惯。

## 依赖：切换到本地 netlink
当前 `github.com/vishvananda/netlink v1.1.0` 的 `SEG6LocalEncap` **没有 VrfTable、action 不全**。
本地 `C:/MyProjects/OpenSource/Kubernetes/netlink`（上游最新检出）支持全部 16 个 seg6local
action（含 End.DT46）+ VrfTable + bpf + 幂等 `RouteReplace`。改法（仿现有 govpp replace）：

```go
// go.mod replace 段加：
replace github.com/vishvananda/netlink => C:/MyProjects/OpenSource/Kubernetes/netlink
```
`require` 行保留 `v1.1.0`（replace 接管源码），随后 `go mod tidy`（netns 自动 v0.0.4→v0.0.5，兼容）。
**必修编译破坏**：`netlink/netlink.go:575` 的 `bond.PackersPerSlave` → `bond.PacketsPerSlave`
（新库修正了拼写；netcfg 自己的 `BondOptions.PacketsPerSlave` 拼写本就正确，仅这一行赋值用了旧名）。

## 配置 Schema（已与用户确认）

### A. transit：`routes[].encap`（seg6）
在任意设备的 `routes:` 条目上加 `encap`（扩展现有 `Route`）：
```yaml
ethernets:
  eth0:
    addresses: [2001:db8:1::1/64]
    routes:
      - to: 2001:db8:ff::/48        # 目标流量
        encap:
          type: seg6                # 目前仅 seg6
          mode: encap               # encap（外层新 IPv6+SRH）| inline（就地插 SRH）
          segments:                 # 段列表，首段在最后（按 iproute2 习惯写正序，由实现处理）
            - 2001:db8:a::1
            - 2001:db8:b::1
```

### B. endpoint + 启用：顶层 `srv6`（netcfg 扩展）
```yaml
srv6:
  enabled: true                     # net.ipv6.conf.all.seg6_enabled=1
  interfaces: [eth0, eth1]          # 各口 net.ipv6.conf.<if>.seg6_enabled=1（入向处理 SRH 需要）
  local-sids:                       # 本地 SID 表（每条 = 一条 <sid>/128 的 seg6local 路由）
    - sid: 2001:db8:a::100          # 自动按 /128 处理（也接受显式 /128）
      action: End                   # 纯中转 endpoint
    - sid: 2001:db8:a::b00
      action: End.X                 # L3 cross-connect（转发到下一跳）
      nh6: fe80::1
      oif: eth1
    - sid: 2001:db8:a::d4
      action: End.DX4               # decap + IPv4 cross-connect
      nh4: 10.0.0.1
    - sid: 2001:db8:a::d6
      action: End.DX6               # decap + IPv6 cross-connect
      nh6: 2001:db8:2::1
    - sid: 2001:db8:a::t6
      action: End.DT6               # decap + 在指定 IPv6 表查路由（per-VRF）
      table: 100                    # 经典写法：lookup table
    - sid: 2001:db8:a::t4
      action: End.DT4               # decap + IPv4 VRF 查表
      vrf-table: 100                # 新内核：vrftable
    - sid: 2001:db8:a::t46
      action: End.DT46              # decap + IPv4/IPv6 VRF 查表
      vrf-table: 100
    - sid: 2001:db8:a::b6
      action: End.B6.Encaps         # 绑定 SID：再压入新 SRH
      segments: [2001:db8:b::1, 2001:db8:c::1]
    # 其它支持的 action：End.T(table)、End.DX2(oif，L2)、End.B6(segments)
```
`srv6` 也可出现在 `netns.<name>` 下（每个 netns 独立 SID 表/启用）。

### local-sid 字段 → action 需求矩阵
| action | 必填 | 可选 |
|---|---|---|
| End | — | — |
| End.X | nh6 | oif |
| End.T | table | — |
| End.DX2 | oif | — |
| End.DX4 | nh4 | — |
| End.DX6 | nh6 | — |
| End.DT4 | vrf-table | — |
| End.DT6 | table | — |
| End.DT46 | vrf-table | — |
| End.B6 | segments | — |
| End.B6.Encaps | segments | — |

## netlink 映射
- **transit**：`route.Encap = &netlink.SEG6Encap{Mode, Segments}`，Mode = `SEG6_IPTUN_MODE_ENCAP/INLINE`。
- **local SID**：一条 `Route{LinkIndex: <anchor dev>, Dst: <sid>/128, Encap: &SEG6LocalEncap{...}}`，
  用 `RouteReplace` 幂等下发。
  - **关键坑1（Flags）**：`SEG6LocalEncap.Flags[idx]` 必须为每个填的字段置位——
    `Flags[SEG6_LOCAL_ACTION]=true` 恒置；End.X/DX6 置 `NH6`；DX4 置 `NH4`；
    End.T/DT6 置 `TABLE`；DT4/DT46 置 `VRFTABLE`；DX2/X 置 `OIF`；B6/B6.Encaps 置 `SRH`。
    漏置 → 该属性被静默丢弃。
  - **关键坑2（锚定设备，真机验出）**：seg6local 路由**必须挂在真实设备上，挂 `lo` 内核会
    静默丢弃 encap**（路由建成但没有 seg6local）。故 netcfg 用「锚定设备」：per-SID `dev`
    → `srv6.device` → 都没填则自动建并 up 一个 dummy `srv6`（类比 FRR 的 sr0）。
  - **关键坑3（VRF strict_mode，真机验出）**：End.DT4/DT46 经 `vrftable` 解封时，内核要求
    `net.vrf.strict_mode=1`，否则 EPERM「Strict mode for VRF is disabled」。netcfg 在检测到
    任一 SID 用 `vrf-table` 时自动 `echo 1 > /proc/sys/net/vrf/strict_mode`。对应 `table`
    的 VRF 设备需存在（用 `vrfs:` 定义）。
  - Dst 用 `&net.IPNet{IP: sid, Mask: /128}`；family 由 Dst 自动推断，**不要手设 Family**。
  - 单条 SID 失败仅告警续做（不同内核支持的 action 不同），不阻断其余配置。
- **sysctl**（库不提供，netcfg 写 procfs）：`net/ipv6/conf/all/seg6_enabled=1`、
  `net/ipv6/conf/<if>/seg6_enabled=1`；DT4/DT46 另需 `net/vrf/strict_mode=1`。transit 节点
  通常还需 `forwarding=1`（已由现有逻辑/用户配置覆盖，不在此自动改）。

## 校验（LoadConfig 阶段，仿 ValidateVPP）
- `encap.type` 仅 `seg6`；`mode` ∈ {encap, inline}；`segments` 非空且均为合法 IPv6。
- `local-sids[].action` ∈ 上表；按矩阵校验必填字段；`sid`/`nh4`/`nh6`/`segments` 地址族正确
  （nh4 必须 IPv4，nh6/segments/sid 必须 IPv6）；`table`/`vrf-table` > 0。
- `srv6.interfaces` 仅作 sysctl，不校验设备存在（可能是后建/外部口），缺失只告警。

## 幂等与回收（state）
- transit encap 路由：随宿主设备 `routes` 一起下发；用 `RouteReplace` 幂等。
- local SID：用 `RouteReplace` 幂等；在 netcfg 状态文件记录本次下发的 SID 集合，
  下次 apply 时删除已不在配置中的 SID（增量回收，仿 VPP reap）。state 仅存 SID 列表（按 netns）。

## 不在本期范围（本地 netlink 也不支持）
- **flavors**：PSP/USP/USD、NEXT-CSID/压缩（无 `SEG6_LOCAL_FLAVORS`）。
- **per-SID counters**（无 `SEG6_LOCAL_COUNTERS`）。
- **SRv6 HMAC**（库未提供编程接口，SRH flags 硬编码 0）。
- **End.BPF**：action 可下发但需已加载的 BPF prog fd，YAML 不便表达，暂不开放。
- 以上若将来需要：升级/扩展 netlink 库后再加，schema 预留 action 字符串可平滑扩展。

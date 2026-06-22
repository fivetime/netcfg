# 内核态 SRv6 (seg6) 示例

netcfg 扩展（非 netplan 标准），见 `docs/srv6-design.md`。配置分两层：

| 用途 | 写在哪 | 语法 |
|------|--------|------|
| **transit / 引流**（压入 SRH 转发） | 设备的 `routes[].encap` | `type: seg6` + `mode: encap\|inline` + `segments` |
| **endpoint / 本地 SID**（End.* 行为） | 顶层 `srv6.local-sids` | `sid` + `action` + 各行为参数 |
| **启用 SRH 处理** | 顶层 `srv6` | `enabled`（all）+ `interfaces`（各口 seg6_enabled） |

`srv6` 段也可写在 `netns.<name>` 下（每个 netns 独立 SID 表/启用）。

## 支持的 endpoint 行为（action）

| action | 必填 | 行为 |
|--------|------|------|
| `End` | — | 纯中转 endpoint |
| `End.X` | `nh6`（可选 `oif`） | L3 cross-connect |
| `End.T` | `table` | 在指定表查路由 |
| `End.DX2` | `oif` | decap + L2 转发 |
| `End.DX4` | `nh4` | decap + IPv4 cross-connect |
| `End.DX6` | `nh6` | decap + IPv6 cross-connect |
| `End.DT4` | `vrf-table` | decap + IPv4 VRF 查表 |
| `End.DT6` | `table` | decap + IPv6 表查路由 |
| `End.DT46` | `vrf-table` | decap + IPv4/IPv6 VRF 查表 |
| `End.B6` | `segments` | 绑定 SID：替换 SRH |
| `End.B6.Encaps` | `segments` | 绑定 SID：再压入新 SRH |

## 应用与验证
```bash
cp srv6.yaml /etc/netplan/ && netcfg apply
# transit 路由
ip -6 route show | grep seg6
# 本地 SID
ip -6 route show | grep seg6local
# sysctl
sysctl net.ipv6.conf.all.seg6_enabled net.ipv6.conf.eth0.seg6_enabled
```

## 关键行为（真机 kernel 6.8 验证得出）
- **SID 锚定设备**：seg6local **必须挂真实设备**——挂 `lo` 内核会静默丢弃封装。用 `srv6.device`
  指定（或 per-SID `dev`）；都不填则 netcfg 自动建并 up 一个 dummy `srv6`（类比 FRR 的 sr0）。
- **End.DT4 / End.DT46**：用 `vrf-table`，需对应 `table` 的 VRF 设备存在（`vrfs:` 定义），
  且内核需 `net.vrf.strict_mode=1`——netcfg 检测到 `vrf-table` 时会**自动开启**。
- 单条 SID 失败仅告警续做（不同内核支持的 action 不同），不阻断其余配置。

## 要求
- 内核需 **`CONFIG_IPV6_SEG6_LWTUNNEL=y`**（Ubuntu/RHEL 通用内核默认开启）。
  注意：WSL2 的 `microsoft-standard` 内核**未开**该选项，无法下发 seg6 路由。
- 段列表/SID/nh6 为 IPv6，nh4 为 IPv4，地址均为示例占位值，按实际替换。

## 不在范围
flavors（PSP/USP/USD、NEXT-CSID 压缩）、per-SID counters、SRv6 HMAC、End.BPF
（底层 netlink 库未提供或不便用 YAML 表达），见 `docs/srv6-design.md`。

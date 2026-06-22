# EVPN-VXLAN 示例（数据面 netcfg + 控制面 FRR）

BGP EVPN over VXLAN 的职责天然分两层，本目录各给一个示例：

| 文件 | 层 | 负责 |
|------|----|------|
| `evpn-leaf.yaml` | **数据面**（netcfg / 内核） | VTEP 源 IP、每 VNI 一个 VXLAN（learning off）、vlan-aware 网桥、VRF（L3VNI） |
| `frr.conf` | **控制面**（FRR / BGP EVPN） | 与 RR 的 EVPN 邻居、`advertise-all-vni`、L3VNI(type-5) 的 RD/RT |

## 工作方式
- 标准 FRR EVPN 内核数据面：**每个 VNI 一个 vxlan 设备**，`mac-learning: false`（关内核
  自学习）+ `neigh-suppress: true`（ARP/ND 抑制）。FDB/邻居由 **FRR EVPN** 经 netlink 下发，
  `advertise-all-vni` 自动发现并通告这些本地 VNI。
  （注：netcfg 的 `external: true`/flow-based 模式是另一种单设备多 VNI 用法，不用于此处。）
- `frr.conf` 的 `update-source lo-vtep` 对应 netcfg 里的 VTEP loopback；`vrf vrf-10 / vni 10`
  对应 netcfg 的 `vrfs.vrf-10` + L3VNI 隧道 `vni-l3-10`。
- L2VNI（`vni-10010`）做二层延伸；L3VNI（`vni-l3-10`）做对称 IRB 跨子网路由。

## 应用
```bash
# 数据面
cp evpn-leaf.yaml /etc/netplan/ && netcfg apply
# 控制面（FRR：需启用 bgpd + zebra，vrf/vni 由内核 + FRR 关联）
cp frr.conf /etc/frr/frr.conf && systemctl restart frr
# 验证
vppctl ... # （纯内核 EVPN 用 bridge fdb / ip neigh / vtysh: show bgp l2vpn evpn）
vtysh -c 'show bgp l2vpn evpn summary'
vtysh -c 'show evpn vni'
```

## 去敏说明
`frr.conf` 由真实部署精简而来——只保留**必要的 BGP EVPN 对等**：去掉了 BFD/timers、
prefix-list、route-map、底层 underlay eBGP 细节；ASN（65001/65000）、IP（192.168.255.4 /
2001:db8:ffff::1）、RD/RT（65000:10）均为**示例占位值**，按实际拓扑替换。

> 提示：完整生产部署通常还需 underlay 路由（OSPF/eBGP 让各 VTEP loopback 互通）、
> 多租户多 VNI、对称/非对称 IRB 选择等——本例为最小可读骨架。

# netcfg 配置示例

全部示例均经 `netcfg generate` 解析校验通过。把需要的文件放到 `/etc/netplan/`（或
`/etc/netcfg/`）后 `netcfg apply`。

| 文件 | 覆盖内容 |
|------|----------|
| `simple.yaml` | 最简：ethernet（静态/DHCP）、默认路由、bridge、vlan |
| `devices.yaml` | 全部设备类型：ethernet/dummy/virtual-ethernet/macvlan/macvtap/ipvlan/bond/bridge/vlan/tunnel(vxlan·gre·wireguard)/vrf/tun/tap |
| `advanced.yaml` | 以太网高级属性：match/set-name、DHCP overrides、地址 lifetime/label、IPv6(accept-ra/ra-overrides/privacy/address-generation/token/mtu)、静态路由全字段、策略路由、offload、SR-IOV、Wake-on-LAN、InfiniBand、activation-mode、bridge 端口属性 |
| `wifi-8021x.yaml` | WiFi access-points（PSK/SAE/EAP/开放/隐藏）+ 有线 802.1X（EAP-TLS）|
| `netns-example.yaml` | 网络命名空间：macvlan、路由器拓扑、跨 netns veth、post-script |
| `full-example.yaml` | netns 综合：default + 多 netns + 跨 ns veth + macvlan |
| `vpp-example.yaml` | VPP 后端：af-packet/dpdk/loopback、bond、vlan、vxlan、bridge+BVI，以及 NAT44/64/66（netcfg 扩展，见 `docs/vpp-backend-design.md`）|
| `srv6/` | 内核态 SRv6 (seg6)：transit 引流（routes.encap）+ 全部 endpoint 行为（End/End.X/DT4/DT6/DT46/DX4/DX6/B6…）+ seg6_enabled（netcfg 扩展，见 `docs/srv6-design.md`）|

另见 `evpn/` 子目录：EVPN-VXLAN 数据面（netcfg）+ 控制面（FRR，去敏精简）配对示例。

另见 `netplan/` 子目录：收录全部 35 个**官方 netplan 示例**（100% 兼容，均经
`netcfg generate` 验证），覆盖 netplan 的所有特性，可直接 `netcfg apply`。

说明：
- `vpp-example.yaml` 需要运行中的 VPP（`/run/vpp/api.sock`）；其余为内核后端（直接 netlink）。
- WiFi/802.1X 需 `wpa_supplicant`；netcfg 生成配置并直接 spawn（init-agnostic）。
- 示例中的密钥/证书/PCI 地址为占位，按实际环境替换。

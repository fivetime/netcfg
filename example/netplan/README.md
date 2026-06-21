# 官方 netplan 示例（兼容性参考）

本目录是 **upstream netplan**（`github.com/canonical/netplan`，`examples/`）的官方示例
原样收录。netcfg 的配置语法与 netplan 完全兼容——这 35 个示例**全部经 `netcfg generate`
解析通过**（也是 `tests/compat/` 兼容性测试的输入）。

直接 `netcfg apply` 即可使用（`renderer:` 字段被 netcfg 忽略——netcfg 始终直接走
netlink）。

| 示例 | 内容 |
|------|------|
| static.yaml / static_multiaddress.yaml / static_singlenic_multiip_multigateway.yaml | 静态地址 |
| dhcp.yaml / windows_dhcp_server.yaml | DHCP |
| bonding.yaml / bonding_router.yaml | 链路聚合 |
| bridge.yaml / bridge_vlan.yaml | 网桥 / 桥 + VLAN |
| vlan.yaml | VLAN |
| vxlan.yaml / ipv6_tunnel.yaml | VXLAN / IP 隧道 |
| wireguard.yaml | WireGuard |
| vrf.yaml / source_routing.yaml | VRF / 策略路由 |
| static-routes.yaml / route_metric.yaml / direct_connect_gateway*.yaml | 路由 |
| dummy-devices.yaml / virtual-ethernet.yaml / loopback_interface.yaml | 虚拟设备 |
| wireless.yaml / wireless_adhoc.yaml / wireless_wpa3.yaml / wpa_enterprise.yaml / wpa3_enterprise.yaml | WiFi |
| dhcp_wired8021x.yaml | 有线 802.1X |
| offload.yaml | 网卡 offload |
| sriov.yaml / sriov_vlan.yaml | SR-IOV |
| infiniband.yaml | InfiniBand |
| modem.yaml | 调制解调器（netcfg 暂不实现 modems，解析不报错但忽略并告警）|
| openvswitch.yaml / network_manager.yaml | OVS / NetworkManager（netcfg 不实现这些后端，相关段告警忽略）|

> netcfg 独有的扩展（netns、VPP 后端、NAT）见上一级目录的 `netns-example.yaml` /
> `full-example.yaml` / `vpp-example.yaml`。

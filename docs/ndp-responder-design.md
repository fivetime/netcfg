# netcfg 内置 NDP 代答器设计（纯 Go，ndppd 等价）

## 目标
netcfg **内置**一个纯 Go 的 NDP 代答器：在接口上监听 Neighbor Solicitation，对落在
配置前缀内的目标地址回 Neighbor Advertisement，可把 **Target Link-Layer Address 填成
指定的（外部）MAC**——即 ndppd 干的事，IPv6 版 proxy ARP。**不依赖外部 ndppd，不用
CGO/libpcap，跨发行版**，契合 netcfg 自包含理念（同内置纯 Go DHCP）。

## 为什么内置而非调 ndppd
- netcfg 价值 = 单二进制 / 不依赖外部程序 / 跨发行版。ndppd 并非所有发行版都有，调它就
  违背定位（用户明确反对）。对 DHCP 已用内置纯 Go 客户端而非 dhclient，此处同理。

## 与已有三种 NDP 能力的关系（划清，避免混淆）
| 能力 | 配置 | 机制 | 粒度 | 应答 MAC | 是否需 daemon |
|------|------|------|------|----------|---------------|
| 内核 proxy_ndp（已实现）| `nd-proxy: [addrs]` | sysctl proxy_ndp + NTF_PROXY 邻居 | 逐 /128 | **本机 MAC**（本机进转发路径）| 否（一次性）|
| VPP nd-proxy（已实现）| `vpp.nd-proxy: [addrs]` | ip6nd_proxy_add_del | 逐 /128 | VPP 接口 MAC | 否 |
| **内置响应器（本设计）** | `ndp-proxy.rules` | 用户态监听 NS / 回 NA | **按前缀** | **可指定外部 MAC** | **是** |

> 内核/VPP 都做不到「按前缀代答」和「用外部 MAC 应答」——前者表项是逐地址，后者内核只会
> 用本机 MAC。故 ndppd 式必须用户态响应器。

## 依赖（用成熟库，不造轮子）
- **`github.com/mdlayher/ndp`**：NDP 消息编解码（NS/NA + options，含 TargetLinkLayerAddress）。
  RFC 4861 完整实现，MIT，MetalLB/DigitalOcean 在用。**只用其消息编解码
  `ndp.ParseMessage`/`Message.MarshalBinary`，不用 `ndp.Conn`**（Conn 走 ICMPv6 socket +
  加 solicited-node 组播组，无法覆盖整段 /80：2²⁴ 个组播组加不全）。
- **`github.com/mdlayher/packet`**：AF_PACKET 原始 L2 socket（纯 Go，无 CGO）。**已是间接依赖**。
- **`github.com/mdlayher/ethernet`**：以太网帧编解码。
- allmulti 经 vishvananda/netlink 设（`LinkSetAllmulticastOn`）。
- 参考但**不引用** `Monviech/ndp-proxy-go`（用 libpcap/CGO）。

## 配置 Schema（统一到 `ndp-proxy` 块）
把已提交的扁平 `nd-proxy: [addrs]` 迁移进结构化 `ndp-proxy` 块，避免 `nd-proxy`/`ndp-proxy`
两个近名键并存：
```yaml
ethernets:
  enp2s0:
    ndp-proxy:
      addresses: [2001:db8::99]        # 内核 proxy_ndp（逐 /128，本机 MAC，一次性）= 旧 nd-proxy
      router: false                    # 回 NA 时是否置 Router(R) 标志（缺省 false）
      rules:                           # 内置响应器（按前缀；需 daemon）
        - prefix: 2400:2410:ef28:2a00:1::/80
          neighbor: 84:47:09:0b:7d:4a  # 回 NA 的 TLLA；缺省=本接口 MAC（本机进转发路径）
        - prefix: 2400:2410:ef28:2a00:2::/80
          neighbor: 84:47:09:0b:7d:4a
```
- 同样加到 Vlan/Bridge/Bond（与现有 nd-proxy 一致的设备集合）。
- `addresses` 子键 = 一次性内核下发（apply 即生效，无需 daemon）。
- `rules` 子键 = 响应器（apply 仅校验+持久化 + 设 allmulti；实际代答在 daemon 跑）。

## 工作原理（响应器）
1. 每个有 `rules` 的接口：设 allmulti（收全前缀的 solicited-node 组播 NS）+ 开 AF_PACKET
   socket（BPF 过滤 ICMPv6 NS=type 135，降 CPU）。
2. 读到 NS：用 mdlayher/ndp 解析，取 Target Address。
3. 若 Target 落在某 rule.prefix 内：构造 NA（type 136），TLLA option=rule.neighbor（缺省本口
   MAC），flags=Solicited+Override（R 按 router 配置），src=本机、dst=NS 源（或 DAD 时
   组播）。经 AF_PACKET 发出。
4. DAD（NS 源地址为 ::）：按需回 NA 到 all-nodes（可选，先简单处理/可关）。

## 运行模型
- `netcfg apply`（一次性）：校验 `ndp-proxy`；下发 `addresses` 的内核 proxy_ndp；为有 `rules`
  的接口设 allmulti；持久化配置。**不在 apply 里常驻**。
- `netcfg daemon`（常驻，已存在，现跑 DHCP 续约）：启动每接口一个响应器 goroutine。
  与 DHCP 续约并存。SIGHUP 重载规则。
- 用此特性的节点 netcfg daemon 须常驻——NDP 代理的物理必然（同内置 DHCP 续约）。

## 校验（LoadConfig）
- `rules[].prefix` 合法 IPv6 CIDR；`neighbor` 合法单播 MAC（若给）。
- `addresses[]` 合法 IPv6。

## 不在本期范围
- ndppd 的 `auto`（按路由表派生）/`iface`（转发 NS 到另一口探测）动态模式——本期只做
  `static`（命中前缀即代答）。
- RA 转发、per-host 路由安装（ndp-proxy-go 那套 L3 集成）。
- IPv4 proxy ARP 响应器（mdlayher/arp 以后可同法加）。

## 安全/能力
- 需 `CAP_NET_RAW`（AF_PACKET）+ allmulti。代答用外部 MAC 即 IPv6 版 proxy ARP——本机网段内
  合法网管用途；netcfg 只按用户显式配置代答。

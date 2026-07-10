# netcfg VPP 后端设计文档

> 状态：设计阶段（V0）。本文档是 VPP 后端实现的权威依据。
> 原则：**配置语法以 netplan 为准，尽量复用 netplan 习惯降低学习成本**；只有 VPP 独有、netplan 无对应的概念才新增字段（与 netns 同等待遇）。

---

## 1. 背景与目标

netcfg 当前是「直接 netlink 应用器」——把声明式配置下发到 **Linux 内核**网络栈。

本设计为 netcfg 新增**第二个后端**：通过 [GoVPP](https://go.fd.io/govpp) 的二进制 API，把同一套声明式配置下发到 **VPP（Vector Packet Processing）用户态数据平面**。

目标：
- 同一份 netplan 风格 YAML，既能配内核、也能配 VPP，**改一个标记即可切换**。
- 内核设备与 VPP 设备**可在同一主机共存**（按设备划分）。
- 支持 L2/L3 全量：接口、地址、路由、VLAN、bridge、bond、VXLAN。
- 支持 SR-IOV 多 VF + VPP bond 组合（NFV 常见场景）。

非目标（留在内核侧，VPP 无干净对应）：DHCP 客户端、DNS/nameservers、wifi、modems、accept-ra/SLAAC、wakeonlan。这些设备即使在 VPP 主机上也由内核处理。

---

## 2. 架构

```
                 /etc/netplan/*.yaml  (多文件合并，netplan 标准语义)
                          │
                    config 解析 + 设备归属判定
                          │
            ┌─────────────┴──────────────┐
            ▼                             ▼
     内核设备                         VPP 设备
   KernelApplier                    VPPApplier
  (现有 netlink，不动)            (新增，GoVPP binary API)
            │                             │
        Linux 内核                  /run/vpp/api.sock → VPP
```

抽象出 `Applier` 接口，`netcfg apply` 在解析后按**设备归属**把每个设备分流到对应 applier。内核 applier 是现有代码，零改动；VPP applier 新增。

---

## 3. 设备归属规则（做法 A）

**一个设备归 VPP 管，当且仅当满足以下任一：**
1. 该设备带 `vpp:` 子块；**或**
2. 该设备生效的 `renderer` 为 `vpp`（设备级 > 全局，netplan 标准继承）。

否则归内核（现有后端）。

### 多文件拆分（推荐用法）
netplan/netcfg 读取 `/etc/netplan/*.yaml` 全部文件、按文件名字典序**合并**为一份配置。推荐：
```
/etc/netplan/10-kernel.yaml   # 内核设备（标准 netplan，无 vpp: 块）
/etc/netplan/20-vpp.yaml      # VPP 设备（带 vpp: 块）
```
**为什么用 `vpp:` 块而非全局 `renderer: vpp` 来拆分**：合并后是一份配置、一个全局 `renderer`；若在 VPP 文件写全局 `renderer: vpp`，会污染内核文件里未写 renderer 的设备。用 `vpp:` 块标记则自包含、不依赖全局 renderer，多文件隔离最干净。

### 切换后端
- 单文件整机切 VPP：顶部写一次 `renderer: vpp`，全部设备继承。
- 把某设备从内核搬到 VPP：给它加一个 `vpp:` 块（或挪到 VPP 文件）。
- 反向：删掉 `vpp:` 块 / renderer。

### 互斥约束
同一块物理网卡，内核后端与 VPP 后端**互斥**：
- VPP 独占（dpdk/avf）→ 网卡绑 vfio-pci，**从内核消失**，内核后端不再可见/可管。
- 共存（af-packet）→ 内核保留网卡，VPP 经 AF_PACKET 挂上去。
解析期校验：同名设备不得同时出现在内核与 VPP 归属下；VPP 独占的 PCI 设备不得再被内核设备引用。

---

## 4. NIC 落地模式（设备级 `vpp:` 子块的 `mode`）

| mode | 含义 | 关键字段 | VPP API | 内核可见? |
|---|---|---|---|---|
| `af-packet`（默认） | 经 AF_PACKET 挂到内核已有网卡（共存） | `host-if`（默认=设备名） | `af_packet_create_v3` | 是 |
| `dpdk` | DPDK/vfio 独占物理网卡或 VF | `pci`（必填） | startup.conf `dpdk{dev}` / 运行态 | 否 |
| `avf` | Intel 网卡/VF 原生驱动（免 DPDK） | `pci`（必填） | `avf_create`（avf 插件） | 否 |
| `rdma` | Mellanox 原生驱动 | `host-if` 或 `pci` | rdma 插件 | 取决于配置 |
| `tap` | VPP↔内核 的 tap（控制面回内核） | `host-if`, `host-ns` | `tap_create_v3` | 是（tap 端） |
| `loopback` | VPP 软件 loopback（L3 锚点 / BVI） | 无 | `create_loopback` | 否 |
| `memif` | 共享内存接口（VPP↔容器/应用） | `socket`,`id`,`role`,`ring-size` | `memif_create_v2` | 否 |

默认：不写 `vpp:` 块但归 VPP 的设备 → `mode: af-packet`，`host-if` = 设备名。
插件类模式（avf/rdma/memif）需对应 VPP 插件存在；缺失时解析期/启动期告警。

---

## 5. 完整 Schema

### 5.1 顶层 `vpp:` 段（VPP 运行时/启动配置，netcfg 新增）

放在 `network:` 下，与 `ethernets` 平级。仅承载 netplan 无对应概念的 VPP 全局配置。**可选**——纯运行态配置（连已运行的 VPP）时可不写，用默认 socket。

```yaml
network:
  version: 2
  vpp:
    api-socket: /run/vpp/api.sock   # 默认；GoVPP 连接的 binary API socket
    reconnect: true                 # 默认 true，断线自动重连
    startup:                        # 可选；生成 /etc/vpp/startup.conf（V1c 阶段）
      main-core: 1
      workers: 0                    # worker 线程数（与 corelist 二选一）
      corelist-workers: "2-3"
      hugepages: 1024               # 2MB 页数
      dpdk:
        uio-driver: vfio-pci        # vfio-pci | igb_uio | uio_pci_generic | auto
        dev:                        # DPDK 独占的设备 PCI 清单（独占模式）
          - "0000:02:00.0"
```

| 字段 | 类型 | 默认 | 说明 |
|---|---|---|---|
| `api-socket` | string | `/run/vpp/api.sock` | binary API socket 路径 |
| `reconnect` | bool | `true` | 断线自动重连 |
| `startup.main-core` | int | — | VPP 主线程绑核 |
| `startup.workers` | int | — | worker 线程数 |
| `startup.corelist-workers` | string | — | worker 绑核列表（如 `2-3`） |
| `startup.hugepages` | int | — | hugepage 页数（2MB/页） |
| `startup.dpdk.uio-driver` | string | `vfio-pci` | NIC 绑定驱动 |
| `startup.dpdk.dev` | []string | — | DPDK 独占设备 PCI 清单 |

> `startup.*` 仅在 netcfg 负责生成 `startup.conf` 时使用（V1c）；这些是**开机持久**层、改动需 VPP 重启。若由用户自管 startup.conf，可全部省略。

### 5.2 设备级 `vpp:` 子块（每设备的后端落地细节）

放在任一设备定义内（`ethernets.<name>.vpp`、`bridges.<name>.vpp` 等）。**带此块即归 VPP 管。**

```yaml
ethernets:
  data0:
    addresses: [10.0.0.1/24]   # ← 标准 netplan 键（见 5.3）
    vpp:
      mode: af-packet          # 见 §4；默认 af-packet
      host-if: data0           # af-packet/rdma/tap；默认=设备名
      # pci: "0000:03:02.0"    # dpdk/avf 必填
      # rx-queues: 1
      # tx-queues: 1
      # num-rx-desc: 1024
      # num-tx-desc: 1024
```

| 字段 | 类型 | 适用 mode | 默认 | 说明 |
|---|---|---|---|---|
| `mode` | enum | 全部 | `af-packet` | af-packet/dpdk/avf/rdma/tap/loopback/memif |
| `host-if` | string | af-packet/rdma/tap | 设备名 | 挂接的内核网卡名 |
| `pci` | string | dpdk/avf | — | PCI 地址（如 `0000:03:02.0`） |
| `rx-queues` / `tx-queues` | int | dpdk/avf/af-packet | 1 | 队列数 |
| `num-rx-desc` / `num-tx-desc` | int | dpdk/avf | 驱动默认 | 描述符环大小 |
| `host-ns` | string | tap | — | tap 内核端所在 netns |
| `socket` | string | memif | — | memif socket 文件 |
| `id` | int | memif | — | memif id |
| `role` | enum | memif | `slave` | master/slave |
| `ring-size` | int | memif | 驱动默认 | 环大小 |
| `bd-id` | int | bridge | 自动分配 | bridge domain 数字 id |

### 5.3 netplan 标准键 → VPP 映射（设备/L2/L3）

VPP 设备**复用标准 netplan 键**，netcfg 翻译为 VPP API 调用：

| netplan 键 | VPP 对象 | VPP API |
|---|---|---|
| `ethernets.<n>`（带 vpp 块） | 接口（按 mode 落地） | `af_packet_create_v3` / dpdk / `avf_create` / `create_loopback` / `tap_create_v3` / `memif_create_v2` |
| `addresses: [...]` | 接口 IP | `sw_interface_add_del_address` |
| `routes: [{to,via,table,metric}]` | FIB 路由 | `ip_route_add_del`（FibPath：via→Nh，table→TableID，metric→Weight/preference） |
| `routes: [{to: default}]` / `gateway4/6` | 默认路由 | `ip_route_add_del`（0.0.0.0/0、::/0） |
| `mtu` | 接口 MTU | `sw_interface_set_mtu` |
| `macaddress` | MAC | `sw_interface_set_mac_address` |
| 链路 up（默认） | 管理状态 | `sw_interface_set_flags`（ADMIN_UP） |
| `activation-mode: off/manual` | 不 up（off 强制 down） | `sw_interface_set_flags`（不置 UP / 置 down） |
| `vlans.<n>: {id, link}` | dot1q sub-interface | `create_subif` / `create_vlan_subif` |
| `bridges.<n>: {interfaces}` | bridge domain + 成员 | `bridge_domain_add_del` + `sw_interface_set_l2_bridge` |
| `bridges.<n>` 带 `addresses` | bridge domain + BVI loopback | 自动建 loopback 作 BVI（`bvi_create` 或 loopback+`set_l2_bridge port_type=BVI`） |
| `bonds.<n>: {interfaces, parameters.mode}` | bond 接口 + 成员 | `bond_create2` + `bond_add_member` |
| `tunnels.<n>: {mode: vxlan, id, local, remote}` | VXLAN 隧道 | `vxlan_add_del_tunnel_v3` |
| `routes: table:` / VRF | IP table | `ip_table_add_del` + `sw_interface_set_table` |
| `<dev>.ndp-proxy.addresses` | NDP 代理（逐 /128，本接口 MAC） | `ip6nd_proxy_add_del`（ethernet/bond/vlan；bridge 落 BVI） |
| `bridges.<n>.ndp-proxy.rules`（前缀+外部 MAC） | 该 BD 内托管内核 tap + 纯 Go 响应器 | `tap_create_v3` + `sw_interface_set_l2_bridge`（VPP 数据面做不了前缀/外部 MAC，daemon 响应器代答；强删自愈、随配置回收，见 `docs/ndp-responder-design.md`） |

**bond `parameters.mode` 映射**：`802.3ad`→lacp、`active-backup`→active-backup、`balance-xor`→xor、`balance-rr`→round-robin、`broadcast`→broadcast。

**bridge domain id**：netplan bridge 无 id；默认自动分配，可用 `bridges.<n>.vpp.bd-id` 指定。bridge 带 `addresses` 时自动创建 BVI loopback 承载 L3。

**留内核侧、在 VPP 设备上忽略并告警的键**：`dhcp4/dhcp6`、`nameservers`、`wakeonlan`、`accept-ra`、`emit-lldp`、wifi/modem 相关。

---

## 6. SR-IOV 多 VF + bond 组合

两层协作：**内核建 VF（netcfg 现有 P2-3 `virtual-function-count`）→ 指定 VF 交 VPP（dpdk/avf）→ VPP 内 bond → 上层配 IP/桥/vxlan**。VF 粒度归属：同一 PF 上部分 VF 留内核、部分给 VPP。

```yaml
network:
  version: 2
  renderer: vpp                         # 全局默认走 VPP（继承，省 per-device）
  ethernets:
    enp3s0:
      renderer: networkd                # PF 例外：留内核建 VF
      virtual-function-count: 4         # 内核 sysfs（现有能力）
    vf0: { vpp: { mode: dpdk, pci: "0000:03:02.0" } }
    vf1: { vpp: { mode: dpdk, pci: "0000:03:02.1" } }
  bonds:
    bond0:                              # 继承 vpp
      interfaces: [vf0, vf1]
      parameters: { mode: 802.3ad }     # → VPP lacp
      addresses: [10.0.0.1/24]
```

顺序依赖：建 VF（内核）→ 绑 vfio-pci → VPP 接管 → VPP bond → IP。VF 驱动重绑可复用现有 `netcfg rebind`。bond 成员必须是 VPP 接口。

---

## 7. 持久化模型

VPP 运行态配置**重启即失**（无 running-config save）。netcfg 沿用其「一次性 apply」定位：
- **开机持久层** = `startup.conf`（NIC 独占/CPU/内存/hugepages）。由 `vpp.startup` 生成（V1c），改动需 VPP 重启。
- **运行态层** = 接口/地址/路由/VLAN/bridge/bond/vxlan，经 binary API 下发，**不持久**。
- **跨重启**：开机由 init（systemd/OpenRC/runit）调 `netcfg apply`，重新经 API 下发——与内核后端完全一致，init-agnostic。无需写 VPP 的 `unix{exec}` 文件。

---

## 8. 版本、绑定与连接

- **GoVPP 模块**：`go.fd.io/govpp`（go.mod 要求 **go 1.25**）。netcfg 升 toolchain 到 1.25；开发期用 `replace go.fd.io/govpp => <本地 govpp 源>` 锁定版本、离线。
- **目标 VPP**：`fivetime/vpp` 的 **26.02**（Release `pkg-v26.02` 全套 deb/rpm；GHCR 镜像 `ghcr.io/fivetime/vpp:26.02`/`:latest`，多架构公开）。
- **绑定（binapi）**：govpp 本地预生成绑定对应 25.10，**需对 26.02 重新生成**（从 26.02 容器 `/usr/share/vpp/api` 的 `.api.json` 用 `binapi-generator`）。netcfg 用到的子包（interface/interface_types/ip/ip_types/fib_types/l2/vxlan/tapv2/af_packet/bond/ethernet_types/ip6_nd，以及 NAT 的 nat44_ed/nat64/nat66、avf 的 dev）。
- **兼容性自检**：连接后对所有用到的包 `CheckCompatiblity(AllMessages()...)`，CRC 不匹配立即报「绑定针对 VPP X、实际运行 Y」并退出，避免运行中途 `unknown message`。
- **连接**：`adapter/socketclient` 连 `api-socket`，RPC service-client 风格（`xxx.NewServiceClient(conn)` + context）。
- **权限**：socket 属组 `vpp`；netcfg 需 root 或加入 `vpp` 组。
- **运行依赖**：VPP 必须已运行（`systemctl start vpp`），且配好 hugepages。

---

## 9. 分阶段实现计划

| 阶段 | 内容 | 验证 |
|---|---|---|
| **V0** | 顶层+设备级 `vpp:` schema；Applier 接口 + apply 设备分流；VPP applier 骨架（连接 + CheckCompatiblity）；归属/互斥校验 | 编译 + 连 `ghcr.io/fivetime/vpp:latest` 冒烟（连上、绑定核对通过） |
| **V1a** | af-packet 接口 + loopback + 地址 + 路由（含默认网关）+ up/mtu/mac/activation-mode | privileged 容器内端到端：apply → `vppctl show int/int addr/ip fib` 断言 |
| **V1b** | VLAN sub-if + bridge domain(+BVI) + bond + vxlan | 同上 + bridge/bond/vxlan 断言 |
| **V1c** | dpdk/avf 独占 + `startup.conf` 生成（NIC 绑定/hugepages/CPU）+ SR-IOV VF 链路 | 真机或带 VF 的环境 |
| **测试** | `tests/vpp/`：仿 integration 套件，但 apply 后用 GoVPP dump 或 `vppctl` 断言 VPP 状态 | CI 用 GHCR VPP 镜像 |

---

## 10. 未决 / 边界

- **netns × VPP**：VPP 自身有命名空间概念但与 Linux netns 不同；首版 VPP 设备只在 default 上下文，不与 netcfg 的 netns 交叉（边界，后议）。
- **bridge domain / BVI 自动管理**的命名与回收策略（diff/删除时）需细化。
- **avf/rdma/memif 插件**是否在目标镜像启用，建容器时确认。
- **状态跟踪**：VPP 侧 diff（增量 apply / 删除孤儿对象）如何与现有 state.go 统一，V1b 起细化。
- **go 1.25 升级**对 CI（`go-version-file: go.mod`）与现有构建的影响，V0 时一并处理。

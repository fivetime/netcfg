# VPP 测试环境

为 netcfg VPP 后端（见 `docs/vpp-backend-design.md`）提供真实 VPP 实例。

## 镜像

`tests/vpp/Dockerfile` 基于 **debian:trixie**（VPP 包为 trixie 构建），从
`https://fivetime.github.io/vpp/` 的 apt 源安装 **VPP 26.02-release**。

```bash
docker build -t netcfg-vpp tests/vpp
```

> 安装注意：vpp postinst 会 `sysctl --system` 配 hugepage，无特权 build 中写
> `vm.nr_hugepages` 会失败；Dockerfile 在安装期把 `/usr/sbin/sysctl` 临时顶替为
> no-op（hugepage 改运行时配），装完还原。

## 运行 VPP（需 privileged + hugepages）

```bash
docker run --rm --privileged netcfg-vpp bash -c '
  sysctl -w vm.nr_hugepages=512
  mkdir -p /run/vpp /var/log/vpp
  vpp -c /etc/vpp/startup.conf & sleep 6
  vppctl show version
'
```

默认 startup.conf 已开 socket API：
- binary API：`/run/vpp/api.sock`（属组 `vpp`；GoVPP 连这个）
- CLI：`/run/vpp/cli.sock`（`vppctl`）
- stats：`/run/vpp/stats.sock`

## binapi 重生成源

容器内 `/usr/share/vpp/api`（core + plugins，141 个 `.api.json`）是
`binapi-generator` 针对 26.02 重生成 GoVPP 绑定的来源。

## 绑定链路验证结果（2026-06-21）

用 GoVPP（本地 `replace go.fd.io/govpp => 本地源`，go 1.25）连容器内 26.02：
- 连接 `/run/vpp/api.sock` → CONNECTED
- `CheckCompatiblity(interface.AllMessages()...)` → **OK**（25.10 绑定与 26.02 在
  interface 模块 CRC 兼容）
- `CreateLoopback` 真实写调用成功（loop0, idx 1）

结论：apt 包 → VPP 26.02 运行 → GoVPP 连接 → 真实 API 调用，全链路打通。
注意：仅校验了 interface 包；V0 应对全部用到的包（ip/l2/vxlan/bond/...）跑
CheckCompatiblity，或从上述 api.json 重生成绑定以确保全模块匹配。

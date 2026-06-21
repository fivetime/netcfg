# init 集成（可选服务模板）

netcfg 本身是**一次性配置工具**：`netcfg apply` 把网络状态写入内核后即退出，状态由内核持有。**纯静态配置不需要任何常驻进程。**

只有两类东西天然需要长期运行：

1. **DHCP 租约续期** —— DHCP 协议要求按 T1 续约。由可选的 `netcfg daemon`（或外部 dhclient/dhcpcd 自续约）负责。
2. **802.1X / WiFi 的 wpa_supplicant** —— 内核不做 EAP/WiFi 关联，必须有用户态 supplicant 持续运行。

本目录提供的就是为第 2 类准备的**可选 init 服务模板**：用于在你的 init 系统下监督 wpa_supplicant（开机自启 + 崩溃自愈）。**netcfg 程序自身不做进程监督**——监督是 init 系统（systemd / OpenRC / runit / s6 …）的本职。

> netcfg 不绑定任何特定 init，也不依赖 systemd-networkd。这些模板是**通用示例**，按需取用。

---

## 两种使用模式（二选一）

### 模式 A：开箱即用（默认，无需本目录）

`netcfg apply` 在写好 `/etc/netcfg/wpa-<iface>.conf` 后，直接 `wpa_supplicant -B` 后台拉起。

- 优点：零配置、立即生效。
- 缺点：**无崩溃自愈**——wpa_supplicant 若崩溃，需下次 `netcfg apply` 才会重起。

适合：桌面/开发，或对 802.1X/WiFi 可用性要求不高的场景。

### 模式 B：受监督（用本目录模板）

由 init 系统启动并监督 wpa_supplicant（前台运行、崩溃自动重启、开机自启）。

启用步骤：

1. 让 netcfg **不要**自己 spawn supplicant（避免与受监督实例冲突）——给 netcfg 设置环境变量：
   ```
   NETCFG_SUPPLICANT_EXTERNAL=1
   ```
   （systemd 下可在 `netcfg.service` 加 `Environment=NETCFG_SUPPLICANT_EXTERNAL=1`；其它 init 在对应服务环境里设置。）
   设置后 `netcfg apply` 只写 conf，进程交给下面的服务管理。

2. 按你的 init 启用对应模板（见下）。

适合：生产/服务器，需要 802.1X/WiFi 持续可用 + 崩溃自愈。

> 两种模式**不要同时用**：否则会出现两个 wpa_supplicant 抢同一接口（第二个会因 ctrl_interface 冲突失败并告警）。用模式 B 就务必设 `NETCFG_SUPPLICANT_EXTERNAL=1`。

---

## 各 init 系统的用法

模板里 **DRIVER**：有线 802.1X 用 `wired`，WiFi 用 `nl80211`。conf 路径固定为 `/etc/netcfg/wpa-<iface>.conf`（由 `netcfg apply` 生成）。

### systemd（实例模板，`%i` = 接口名）

```bash
cp init/systemd/netcfg-8021x@.service /etc/systemd/system/   # 有线 802.1X
cp init/systemd/netcfg-wifi@.service  /etc/systemd/system/   # WiFi
systemctl daemon-reload
systemctl enable --now netcfg-8021x@eth0      # 或 netcfg-wifi@wlan0
```

### OpenRC（Alpine / Gentoo）

```bash
cp init/openrc/netcfg-supplicant /etc/init.d/netcfg-supplicant-eth0
# 编辑文件顶部的 IFACE / DRIVER
rc-update add netcfg-supplicant-eth0 default
rc-service netcfg-supplicant-eth0 start
```

### runit（Void / Artix）

```bash
cp -r init/runit/netcfg-supplicant-eth0 /etc/sv/netcfg-supplicant-eth0
# 编辑 run 里的 IFACE / DRIVER
ln -s /etc/sv/netcfg-supplicant-eth0 /var/service/
```

### s6 / 其它

参照上述任一脚本：**前台运行** `wpa_supplicant -i <iface> -D <driver> -c /etc/netcfg/wpa-<iface>.conf`（不要 `-B`），交给监督器重启即可。

---

## 说明

- 这些是**可选**模板，不安装也不影响 netcfg 核心功能。
- 受监督模式下 wpa_supplicant 必须**前台运行**（不加 `-B`），由监督器负责后台化与重启。
- `netcfg apply` 始终负责生成 `/etc/netcfg/wpa-<iface>.conf`；模板只负责"让进程一直活着"。
- 仓库根的 `systemd/` 目录是 netcfg 自身的服务（netcfg.service 等），与本目录的 supplicant 监督模板不同。

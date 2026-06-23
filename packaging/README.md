# 打包与发布

netcfg 用 GitHub Actions 自动从源码编译并发布。核心思路：netcfg 是
`CGO_ENABLED=0` 的**纯静态二进制**，没有 per-distro 的 glibc/ABI 依赖，所以
**一个包（按 CPU 架构区分）就能跨所有发行版**，无需像 VPP 那样按发行版逐个源码构建。

## 工作流

| 文件 | 触发 | 产物 |
|------|------|------|
| `.github/workflows/ci.yml` | push / PR | vet / fmt / test / 多架构 build / lint |
| `.github/workflows/release.yml` | 推送 `v*` tag | 源码静态二进制 **tar.gz + sha256** → GitHub Release |
| `.github/workflows/release-packages.yml` | 推送 `v*` tag / 手动 | **deb / rpm / apk / archlinux** → Release，并把 **签名的 apt/yum 仓库**发布到 GitHub Pages |

`release-packages.yml` 只做编排，具体步骤下沉到复合 action：

- `.github/actions/build-packages` — 每架构交叉编译 + 用 [nfpm](https://nfpm.goreleaser.com/) 出 deb/rpm/apk/archlinux（spec 见 `packaging/nfpm.yaml`）。
- `.github/actions/publish-apt-yum` — GPG 签名并把包累加发布到 `gh-pages` 分支的 apt/yum 仓库（脚本 `packaging/build-repo.sh`）。

## 一键安装（apt / yum）

发布后（owner = `fivetime`）：

```bash
# Debian / Ubuntu 及衍生
curl -fsSL https://fivetime.github.io/netcfg/netcfg-archive-keyring.asc \
  | sudo gpg --dearmor -o /usr/share/keyrings/netcfg-archive-keyring.gpg
echo "deb [signed-by=/usr/share/keyrings/netcfg-archive-keyring.gpg] https://fivetime.github.io/netcfg/apt stable main" \
  | sudo tee /etc/apt/sources.list.d/netcfg.list
sudo apt-get update && sudo apt-get install netcfg

# RHEL / Fedora / openSUSE 等
sudo rpm --import https://fivetime.github.io/netcfg/RPM-GPG-KEY-netcfg
sudo tee /etc/yum.repos.d/netcfg.repo >/dev/null <<'REPO'
[netcfg]
name=netcfg packages
baseurl=https://fivetime.github.io/netcfg/rpm/$basearch
enabled=1
gpgcheck=1
repo_gpgcheck=1
gpgkey=https://fivetime.github.io/netcfg/RPM-GPG-KEY-netcfg
REPO
sudo dnf install netcfg     # 或 zypper / yum
```

## 替换 netplan（takeover）

netcfg 的 deb/rpm/apk 会 `Conflicts/Replaces netplan.io`，安装即接管。安装时 postinstall 自动：
1. 启用 `netcfg-apply.service`（开机经 netlink 应用 `/etc/netplan/*.yaml`）；
2. 运行 `netcfg takeover`——把 `netplan generate` 留下的 systemd-networkd 后端文件
   （`/{etc,run}/systemd/network/10-netplan-*`）移到 `/var/lib/netcfg/netplan-networkd-backup`，
   否则重启时这些文件仍被 systemd-networkd 应用、与 netcfg 冲突；
3. 若确有 netplan networkd 残留，屏蔽 `systemd-networkd-wait-online`（避免开机等待超时）。

手动操作：
```bash
netcfg takeover            # 移走 netplan 的 networkd 后端文件
netcfg takeover --dry-run  # 预览
netcfg takeover --revert   # 还原（回到 netplan 接管前）
```
回到 netplan：`netcfg takeover --revert` → 重装 `netplan.io` → `netplan apply`。

## 发行版覆盖（尽力而为）

netcfg 是 **Linux netlink 工具**，只能运行在 Linux 上。下表按支持方式归类。

### Linux —— 有原生包

| 包格式 | 安装方式 | 覆盖发行版 |
|--------|----------|------------|
| **deb** (`.deb`) | apt 仓库 / 直接 `dpkg -i` | Debian, Ubuntu, Linux Mint, Pop!_OS, Kali, Raspberry Pi OS, Devuan¹, Tails |
| **rpm** (`.rpm`) | yum/dnf/zypper 仓库 / 直接安装 | Fedora, RHEL, CentOS Stream, Rocky, AlmaLinux, Oracle Linux, Amazon Linux, openSUSE Leap/Tumbleweed, SLE, Qubes(dom0) |
| **apk** (`.apk`) | `apk add --allow-untrusted ./netcfg*.apk` | Alpine Linux²（musl 上跑纯静态二进制） |
| **archlinux** (`.pkg.tar.zst`) | `pacman -U ./netcfg*.pkg.tar.zst` | Arch, Manjaro, EndeavourOS |

¹ Devuan 无 systemd：包内 systemd 单元不会被使用，直接 `netcfg apply` 即可，或自行接 sysvinit。
² apk 包发布在 GitHub Release（apt/yum 仓库不含 apk）。

### Linux —— 用静态二进制 tarball

以下发行版不提供原生包，用 `release.yml` 产出的 `netcfg-<ver>-linux-<arch>.tar.gz`
（解压后 `install -Dm755 netcfg /usr/bin/netcfg`，或 `make install`）：

Gentoo, Slackware, Void Linux, NixOS, Clear Linux, Solus —— 以及任何上面没列到的
glibc/musl Linux 发行版。源码构建：`git clone … && make build`。

### 不支持（超出范围）

netcfg 依赖 Linux netlink / netns，**无法在以下系统编译或运行**，因此不提供任何产物：

- **BSD 系**：FreeBSD, OpenBSD, NetBSD, DragonFly BSD, GhostBSD, pfSense/OPNsense, TrueNAS Core
- **Unix 系**：illumos, OpenIndiana, Oracle Solaris, AIX, HP-UX
- **macOS**（Darwin/XNU）

这些平台的网络配置请用各自原生工具。

## 发布前置配置（仓库管理员一次性）

apt/yum 仓库需要 GPG 签名（**未配置也能发布 deb/rpm/apk/arch 到 Release，仅跳过仓库**）：

1. 生成签名密钥：
   ```bash
   gpg --batch --quick-generate-key "netcfg packages <jp.zdm2008@gmail.com>" rsa4096 sign never
   gpg --armor --export-secret-keys <KEYID>   # 复制 ASCII-armored 私钥
   ```
2. 仓库 **Settings → Secrets and variables → Actions** 添加：
   - `GPG_PRIVATE_KEY`（必填，上面导出的私钥）
   - `GPG_PASSPHRASE`（可选，密钥有口令时）
3. 仓库 **Settings → Pages**：Source 选 **Deploy from a branch**，分支 `gh-pages`，目录 `/(root)`。
   （首次发布会自动创建 `gh-pages` 分支。）

## 本地验证

```bash
# 交叉编译 + 出包（需本机装 nfpm：go install github.com/goreleaser/nfpm/v2/cmd/nfpm@latest）
# 注意：二进制固定输出到 dist/netcfg（nfpm contents.src 不展开环境变量）。
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o dist/netcfg .
PKG_VERSION=0.0.0 PKG_ARCH=amd64 \
  nfpm pkg --config packaging/nfpm.yaml --packager deb --target dist/
```

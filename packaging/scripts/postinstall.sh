#!/bin/sh
# nfpm postinstall —— deb/rpm/apk 共用。建配置目录、启用开机 apply、自动接管 netplan。
set -e

# 只建 /etc/netplan（netplan 兼容默认目录）。不建 /etc/netcfg：空的 /etc/netcfg 会
# 遮蔽 /etc/netplan（netcfg 仅在 /etc/netcfg 下有 YAML 时才优先用它）。
mkdir -p /etc/netplan

# 安装前先记下是否存在 netplan 的 networkd 后端文件（用于决定是否禁用 wait-online）。
NETPLAN_NETWORKD=0
if ls /etc/systemd/network/10-netplan-* /run/systemd/network/10-netplan-* >/dev/null 2>&1; then
    NETPLAN_NETWORKD=1
fi

if command -v systemctl >/dev/null 2>&1; then
    systemctl daemon-reload || true
    # 开机自动应用静态网络配置（替代 netplan 的开机应用）。
    systemctl enable netcfg-apply.service 2>/dev/null || true
fi

# 接管 netplan：把 netplan 生成的 systemd-networkd 后端文件移到备份，避免重启时
# networkd 与 netcfg 冲突（可 'netcfg takeover --revert' 还原）。netcfg 已
# Conflicts/Replaces netplan.io，安装即视为接管；无 netplan 残留时为 no-op。
if command -v netcfg >/dev/null 2>&1; then
    netcfg takeover 2>/dev/null || true
fi

# netplan 的 networkd 后端既已移走，禁用 networkd-wait-online 避免开机等待超时
# （仅在确实存在 netplan networkd 配置时才动，避免影响纯 networkd 用户）。
if [ "$NETPLAN_NETWORKD" = 1 ] && command -v systemctl >/dev/null 2>&1; then
    systemctl mask systemd-networkd-wait-online.service 2>/dev/null || true
fi

if [ -d /etc/netplan ] && ls /etc/netplan/*.yaml >/dev/null 2>&1; then
    echo "Note: netcfg now manages /etc/netplan/*.yaml (netplan networkd backend moved to /var/lib/netcfg/netplan-networkd-backup)."
fi

echo "netcfg installed. Applies now via 'netcfg apply'; on boot via netcfg-apply.service."
echo "Revert netplan takeover: 'netcfg takeover --revert'. DHCP/NDP daemon (optional): systemctl enable --now netcfg"

#!/bin/sh
# nfpm postinstall —— deb/rpm/apk 共用。建配置目录、重载 systemd、启用开机 apply。
set -e

# 只建 /etc/netplan（netplan 兼容默认目录）。不建 /etc/netcfg：空的 /etc/netcfg 会
# 遮蔽 /etc/netplan（netcfg 仅在 /etc/netcfg 下有 YAML 时才优先用它）。
mkdir -p /etc/netplan

if command -v systemctl >/dev/null 2>&1; then
    systemctl daemon-reload || true
    # 开机自动应用静态网络配置（替代 netplan 的开机应用）。netcfg Conflicts/Replaces
    # netplan.io，安装即视为接管；空配置时 apply 为 no-op，安全。
    systemctl enable netcfg-apply.service 2>/dev/null || true
fi

if [ -d /etc/netplan ] && ls /etc/netplan/*.yaml >/dev/null 2>&1; then
    echo "Note: existing netplan configs found in /etc/netplan/ — netcfg will use them on next 'netcfg apply' / boot."
fi

echo "netcfg installed. 'netcfg apply' applies now; 'systemctl enable --now netcfg-apply' / reboot applies at boot."
echo "DHCP/NDP daemon (optional): systemctl enable --now netcfg"

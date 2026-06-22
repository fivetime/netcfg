#!/bin/sh
# nfpm postinstall —— deb/rpm 共用。安装后建配置目录、重载 systemd。
set -e

# 配置目录（netcfg 与 netplan 两套路径都建，apply 自动探测）
mkdir -p /etc/netcfg /etc/netplan

if command -v systemctl >/dev/null 2>&1; then
    systemctl daemon-reload || true
fi

if [ -d /etc/netplan ] && ls /etc/netplan/*.yaml >/dev/null 2>&1; then
    echo "Note: existing netplan configs found in /etc/netplan/ — netcfg will use them."
fi

echo "netcfg installed. Run 'netcfg apply' to apply network configuration,"
echo "or 'systemctl enable --now netcfg' for the daemon."

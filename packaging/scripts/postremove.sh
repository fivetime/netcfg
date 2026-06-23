#!/bin/sh
# nfpm postremove —— deb/rpm 共用。卸载后重载 systemd；purge 时清状态目录。
set -e

if command -v systemctl >/dev/null 2>&1; then
    systemctl daemon-reload || true
    # 取消接管时对 networkd-wait-online 的屏蔽（若装回 netplan 需要它）。
    systemctl unmask systemd-networkd-wait-online.service 2>/dev/null || true
fi

# 注：netplan 的 networkd 后端备份在 /var/lib/netcfg/netplan-networkd-backup，
# 想恢复 netplan 接管，请在卸载前先 'netcfg takeover --revert'。

# Debian purge ($1 = purge) 才清运行时状态（含上述备份）；rpm 升级/卸载不触发。
if [ "$1" = "purge" ]; then
    rm -rf /var/lib/netcfg
fi

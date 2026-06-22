#!/bin/sh
# nfpm postremove —— deb/rpm 共用。卸载后重载 systemd；purge 时清状态目录。
set -e

if command -v systemctl >/dev/null 2>&1; then
    systemctl daemon-reload || true
fi

# Debian purge ($1 = purge) 才清运行时状态；rpm 升级/卸载不触发。
if [ "$1" = "purge" ]; then
    rm -rf /var/lib/netcfg
fi

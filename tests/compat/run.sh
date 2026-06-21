#!/usr/bin/env bash
# 在隔离的特权容器中用 netcfg 应用一个 netplan example，并打印验证信息。
# 用法: run.sh <example.yaml> [dummy_nic1 dummy_nic2 ...]
# 需在 Git Bash 下运行（已处理 MSYS 路径改写）。
set -u

EX="${1:?usage: run.sh <example.yaml> [dummies...]}"; shift || true
DUMMIES="$*"

ROOT="C:/MyProjects/IaasProjects/Kubernetes/netcfg"
NETPLAN="C:/MyProjects/OpenSource/netplan/examples"

MSYS_NO_PATHCONV=1 docker run --rm --privileged \
  -v "${ROOT}/netcfg.linux:/netcfg:ro" \
  -v "${NETPLAN}:/examples:ro" \
  netcfg-compat bash -c "
    set -u
    echo '===== EXAMPLE: ${EX} ====='
    echo '----- config -----'
    cat /examples/${EX}
    for d in ${DUMMIES}; do
      ip link add \"\$d\" type dummy 2>/dev/null && ip link set \"\$d\" up && echo \"[stub] created dummy \$d\"
    done
    mkdir -p /etc/netplan
    cp /examples/${EX} /etc/netplan/
    echo '----- netcfg apply -----'
    /netcfg apply
    rc=\$?
    echo \"===== apply rc=\$rc =====\"
    echo '----- ip -br addr -----'
    ip -br addr
    echo '----- ip -br link -----'
    ip -br link
    echo '----- ip route -----'
    ip route 2>/dev/null
    echo '----- ip -6 route (filtered) -----'
    ip -6 route 2>/dev/null | grep -v '^fe80\|^::1' | head -20
    echo '----- wg (if any) -----'
    wg show 2>/dev/null | head -20 || true
  "

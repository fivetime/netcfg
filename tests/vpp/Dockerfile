# VPP 测试镜像 —— 安装 fivetime/vpp 发布的 apt 包用于 netcfg VPP 后端测试。
# 包为 Debian 13 (trixie) 构建（见 https://fivetime.github.io/vpp/），故基础镜像用 trixie。
# 用途：①取 /usr/share/vpp/api 的 .api.json 重生成 GoVPP binapi；②起 VPP 验证连接。
FROM debian:trixie

RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates curl gnupg procps iproute2 && \
    curl -fsSL https://fivetime.github.io/vpp/vpp-archive-keyring.asc \
        | gpg --dearmor -o /usr/share/keyrings/vpp-archive-keyring.gpg && \
    echo "deb [signed-by=/usr/share/keyrings/vpp-archive-keyring.gpg] https://fivetime.github.io/vpp/apt stable main" \
        > /etc/apt/sources.list.d/vpp.list && \
    apt-get update && \
    # vpp postinst 跑 `sysctl --system` 配 hugepage，在无特权 build 中写 vm.nr_hugepages
    # 会失败。dpkg 维护脚本 PATH 为 /usr/sbin:/usr/bin:/sbin:/bin，故直接顶替
    # /usr/sbin/sysctl 为 no-op 垫过安装（hugepage 改在运行时配），装完还原真 sysctl。
    mv /usr/sbin/sysctl /usr/sbin/sysctl.real && \
    printf '#!/bin/sh\nexit 0\n' > /usr/sbin/sysctl && chmod +x /usr/sbin/sysctl && \
    apt-get install -y --no-install-recommends vpp vpp-plugin-core && \
    mv -f /usr/sbin/sysctl.real /usr/sbin/sysctl && \
    rm -rf /var/lib/apt/lists/*

# 运行（需 privileged + hugepages）：
#   docker run --rm --privileged <img> vpp -c /etc/vpp/startup.conf
# 连接（另一进程）：/run/vpp/api.sock

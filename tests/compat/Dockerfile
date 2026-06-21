# 兼容性测试镜像：Ubuntu + 验证/检查工具
# netcfg 本身直接走 netlink，无需 ip 命令；这些工具仅用于测试中验证结果。
FROM ubuntu:24.04

RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        iproute2 \
        iputils-ping \
        wireguard-tools \
        ethtool \
        ca-certificates && \
    rm -rf /var/lib/apt/lists/*

# netcfg 二进制与配置在运行时挂载/复制
WORKDIR /work

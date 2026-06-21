#!/usr/bin/env bash
# netcfg 集成测试运行器
#
# 在真实内核中应用配置并用 netlink 断言结果。需 root + Linux；推荐用 privileged
# 容器隔离（每次运行独立 netns/设备）。
#
# 用法：
#   tests/integration/run.sh           # 用 Docker（privileged）跑全部
#   RUN_LOCAL=1 tests/integration/run.sh   # 直接在本机跑（需 root + Linux）
#
# 依赖镜像 netcfg-compat（见 tests/compat/Dockerfile）。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

echo "==> building linux netcfg binary + integration test binary"
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o netcfg.linux .
GOOS=linux GOARCH=amd64 go test -c -tags integration -o tests/integration/integration.test ./tests/integration

if [ "${RUN_LOCAL:-0}" = "1" ]; then
	echo "==> running locally (root required)"
	NETCFG_BIN="$ROOT/netcfg.linux" tests/integration/integration.test -test.v
	exit $?
fi

echo "==> running in privileged container (netcfg-compat)"
docker run --rm --privileged \
	-v "$ROOT/netcfg.linux:/netcfg:ro" \
	-v "$ROOT/tests/integration/integration.test:/integration.test:ro" \
	netcfg-compat \
	bash -c 'NETCFG_BIN=/netcfg /integration.test -test.v'

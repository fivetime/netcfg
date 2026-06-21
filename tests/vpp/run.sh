#!/usr/bin/env bash
# netcfg VPP 后端集成测试运行器。
# 构建 VPP 镜像 + netcfg 二进制 + 测试二进制，在 privileged 容器内对真实 VPP 断言。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"
export GOTOOLCHAIN=auto

echo "==> building VPP test image (netcfg-vpp)"
docker build -q -t netcfg-vpp tests/vpp >/dev/null

echo "==> building netcfg + vpp integration test binary"
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o netcfg.linux .
GOOS=linux GOARCH=amd64 go test -c -tags vppintegration -o tests/vpp/vpp.test ./tests/vpp

echo "==> running VPP integration tests in privileged container"
docker run --rm --privileged \
	-v "$ROOT/netcfg.linux:/netcfg:ro" \
	-v "$ROOT/tests/vpp/vpp.test:/vpp.test:ro" \
	netcfg-vpp \
	bash -c 'NETCFG_BIN=/netcfg /vpp.test -test.v'

#!/usr/bin/env bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
DEPLOY_DIR="$ROOT_DIR/deploy"
PACKAGE_FILE="$DEPLOY_DIR/openwiki-deploy-package.tar.gz"

echo "=============================================================================="
echo "  打包生产发布包 openwiki-deploy-package.tar.gz"
echo "=============================================================================="

# 1. 确保 ARM 与 x86 二进制已编译
if [ ! -f "$DEPLOY_DIR/bin/arm/orchestrator" ]; then
  echo "--> 正在编译 ARM64 二进制..."
  "$DEPLOY_DIR/scripts/build-arm.sh"
fi

if [ ! -f "$DEPLOY_DIR/bin/x86/orchestrator" ]; then
  echo "--> 正在编译 x86_64 二进制..."
  "$DEPLOY_DIR/scripts/build-x86.sh"
fi

# 创建架构兼容软链接 (支持 Docker TARGETARCH=amd64 / arm64)
cd "$DEPLOY_DIR/bin"
ln -sfn x86 amd64
ln -sfn arm arm64

# 2. 生成发布压缩包
echo "--> 正在组装全量发布包..."
cd "$DEPLOY_DIR"

tar -czf "$PACKAGE_FILE" \
  --exclude="*.log" \
  --exclude="data" \
  --exclude="test_runtime" \
  --exclude="unit1-orchestrator" \
  --exclude="unit2-portal" \
  --exclude=".DS_Store" \
  Dockerfile \
  docker-compose.yml \
  nginx.conf \
  logrotate.conf \
  entrypoint.sh \
  healthcheck.sh \
  MANUAL.md \
  bin/

echo ""
echo "=============================================================================="
echo "🎉 发布包打包完成: $PACKAGE_FILE ($(ls -lh "$PACKAGE_FILE" | awk '{print $5}'))"
echo "=============================================================================="

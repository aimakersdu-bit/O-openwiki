#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
DEPLOY_DIR="$ROOT_DIR/deploy"
TAR_NAME="${TAR_NAME:-openwiki-deploy-linux-arm64.tar.gz}"
COMPAT_TAR_NAME="openwiki-deploy-package.tar.gz"

echo "=============================================================================="
echo " OpenWiki Linux ARM64 部署包打包"
echo " 项目目录: $ROOT_DIR"
echo "=============================================================================="

"$ROOT_DIR/deploy/scripts/build-linux-arm64.sh"

cd "$ROOT_DIR"
rm -f \
    "$ROOT_DIR/$TAR_NAME" \
    "$DEPLOY_DIR/$TAR_NAME" \
    "$ROOT_DIR/$COMPAT_TAR_NAME" \
    "$DEPLOY_DIR/$COMPAT_TAR_NAME"

tar -czf "$ROOT_DIR/$TAR_NAME" \
    deploy/Dockerfile \
    deploy/docker-compose.yml \
    deploy/nginx.conf \
    deploy/logrotate.conf \
    deploy/entrypoint.sh \
    deploy/healthcheck.sh \
    deploy/MANUAL.md \
    deploy/bin/arm

cp "$ROOT_DIR/$TAR_NAME" "$DEPLOY_DIR/$TAR_NAME"
if [ "$TAR_NAME" != "$COMPAT_TAR_NAME" ]; then
    cp "$ROOT_DIR/$TAR_NAME" "$ROOT_DIR/$COMPAT_TAR_NAME"
    cp "$ROOT_DIR/$TAR_NAME" "$DEPLOY_DIR/$COMPAT_TAR_NAME"
fi

tar -tzf "$ROOT_DIR/$TAR_NAME" >/dev/null
for binary in "$ROOT_DIR/deploy/bin/arm/orchestrator" "$ROOT_DIR/deploy/bin/arm/portal"; do
    file "$binary" | grep -q "ELF 64-bit.*ARM aarch64" || {
        echo "错误: $binary 不是 Linux ARM64 ELF 文件。" >&2
        exit 1
    }
done

echo ""
echo "=============================================================================="
echo " Linux ARM64 部署包打包完成"
echo " 输出路径:"
echo "  $ROOT_DIR/$TAR_NAME"
echo "  $DEPLOY_DIR/$TAR_NAME"
echo "  $ROOT_DIR/$COMPAT_TAR_NAME"
echo "  $DEPLOY_DIR/$COMPAT_TAR_NAME"
echo "=============================================================================="

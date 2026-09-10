#!/usr/bin/env bash
set -e

# ==============================================================================
# OpenWiki 一键编译与部署包打包自动化脚本 (All-in-One Packaging Script)
# 说明：自动编译 ARM64 与 x86_64 二进制服务，打包 Dockerfile、运维手册并输出 tar.gz 发布包。
# ==============================================================================

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
DEPLOY_DIR="$ROOT_DIR/deploy"
TAR_NAME="openwiki-deploy-package.tar.gz"

echo "=============================================================================="
echo "🚀 开始构建 OpenWiki 部署全量包"
echo "项目根目录: $ROOT_DIR"
echo "=============================================================================="

export PATH="$PATH:/usr/local/bin:/opt/homebrew/bin:/usr/local/go/bin:$HOME/go/bin"

# 1. 编译 ARM64 架构 Go 二进制
echo ""
echo "=== [1/4] 编译 ARM64 二进制及组装包 ==="
"$ROOT_DIR/deploy/scripts/build-arm.sh"

# 2. 编译 x86_64 架构 Go 二进制
echo ""
echo "=== [2/4] 编译 x86_64 二进制及组装包 ==="
"$ROOT_DIR/deploy/scripts/build-x86.sh"

# 3. 打包所有部署资源
echo ""
echo "=== [3/4] 创建归档压缩包 ($TAR_NAME) ==="
cd "$ROOT_DIR"

tar -czvf "$TAR_NAME" \
  deploy/Dockerfile \
  deploy/docker-compose.yml \
  deploy/nginx.conf \
  deploy/logrotate.conf \
  deploy/entrypoint.sh \
  deploy/healthcheck.sh \
  deploy/MANUAL.md \
  deploy/bin/arm \
  deploy/bin/x86

# 复制一份到 deploy 目录下
cp "$TAR_NAME" "$DEPLOY_DIR/$TAR_NAME"

# 4. 完成总结
echo ""
echo "=============================================================================="
echo "🎉 部署包打包完成！"
echo "压缩包输出路径:"
echo " ├── $ROOT_DIR/$TAR_NAME"
echo " └── $DEPLOY_DIR/$TAR_NAME"
echo ""
echo "压缩包内容概览:"
echo " ├── Dockerfile & docker-compose.yml"
echo " ├── MANUAL.md (内网 Build 手册与运维指南)"
echo " ├── bin/arm/ (ARM64 二进制及依赖组件)"
echo " └── bin/x86/ (x86_64 二进制及依赖组件)"
echo "=============================================================================="

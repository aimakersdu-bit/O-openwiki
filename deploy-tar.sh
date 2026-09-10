#!/usr/bin/env bash
# 方便在项目根目录直接运行一键打包
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
exec "$SCRIPT_DIR/scripts/package-deploy.sh" "$@"

#!/usr/bin/env bash
# 方便在项目根目录直接运行 ARM 编译
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
exec "$SCRIPT_DIR/deploy/scripts/build-arm.sh" "$@"

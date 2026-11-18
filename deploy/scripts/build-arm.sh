#!/usr/bin/env bash
set -e

# ==============================================================================
# OpenWiki ARM64 独立部署包构建脚本
# 说明：仅编译我们独立的 Go 服务（Orchestrator 与 Portal）并打包发布资源，
#       不对 openwiki 源码进行任何 TypeScript 编译。
# ==============================================================================

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
OUTPUT_DIR="$ROOT_DIR/deploy/bin/arm"

echo "=============================================================================="
echo "  OpenWiki ARM64 发布包构建"
echo "  项目目录: $ROOT_DIR"
echo "=============================================================================="

export PATH="$PATH:/usr/local/bin:/opt/homebrew/bin:/usr/local/go/bin:$HOME/go/bin"

# 1. 清理上次构建的产物与临时文件
echo ""
echo "=== [1/3] 清理旧构建产物与中间文件 ==="
rm -rf "$OUTPUT_DIR"
mkdir -p "$OUTPUT_DIR"

# 2. 配置 Go 编译环境
echo ""
echo "=== [2/3] 配置 Go 编译环境 (ARM64) ==="
export GOARCH="${GOARCH:-arm64}"
export GOOS="${GOOS:-$(go env GOOS)}"
export CGO_ENABLED="${CGO_ENABLED:-1}"

echo "构建目标架构: OS=$GOOS, ARCH=$GOARCH, CGO_ENABLED=$CGO_ENABLED"

# 提取版本信息 (git describe + commit + 构建时间)
GIT_VERSION=$(cd "$ROOT_DIR" && git describe --tags --always --long 2>/dev/null | sed 's/-[0-9]*-g/-/g' || echo "unknown")
GIT_COMMIT=$(cd "$ROOT_DIR" && git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_TIME=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS="-s -w -X github.com/openwiki/orchestrator/internal/version.Version=${GIT_VERSION} -X github.com/openwiki/orchestrator/internal/version.GitCommit=${GIT_COMMIT} -X github.com/openwiki/orchestrator/internal/version.BuildTime=${BUILD_TIME} -X github.com/openwiki/portal/internal/version.Version=${GIT_VERSION} -X github.com/openwiki/portal/internal/version.GitCommit=${GIT_COMMIT} -X github.com/openwiki/portal/internal/version.BuildTime=${BUILD_TIME}"
echo "--> 版本: ${GIT_VERSION} (commit: ${GIT_COMMIT}, built: ${BUILD_TIME})"

# 3. 编译 Go 后端二进制
echo "--> 编译 Orchestrator ($GOOS/$GOARCH)..."
cd "$ROOT_DIR/deploy/unit1-orchestrator"
go build -v -ldflags="${LDFLAGS}" -o "$OUTPUT_DIR/orchestrator" .

echo "--> 编译 Portal ($GOOS/$GOARCH)..."
cd "$ROOT_DIR/deploy/unit2-portal"
go build -v -ldflags="${LDFLAGS}" -o "$OUTPUT_DIR/portal" .

echo "--> 编译 openwiki-migrate CLI ($GOOS/$GOARCH)..."
cd "$ROOT_DIR/deploy/unit1-orchestrator/cmd/openwiki-migrate"
go build -v -ldflags="${LDFLAGS}" -o "$OUTPUT_DIR/openwiki-migrate" .

chmod +x "$OUTPUT_DIR/orchestrator" "$OUTPUT_DIR/portal" "$OUTPUT_DIR/openwiki-migrate"

# 4. 组装部署资源 (不包含 openwiki 源码或 dist 编译产物)
echo ""
echo "=== [3/3] 组装 ARM64 部署包 ==="

# 拷贝脚本文件
mkdir -p "$OUTPUT_DIR/scripts"
if [ -d "$ROOT_DIR/deploy/unit1-orchestrator/scripts" ]; then
    cp -r "$ROOT_DIR/deploy/unit1-orchestrator/scripts/"* "$OUTPUT_DIR/scripts/"
fi
if [ -f "$ROOT_DIR/deploy/scripts/backup-sqlite.sh" ]; then
    cp "$ROOT_DIR/deploy/scripts/backup-sqlite.sh" "$OUTPUT_DIR/scripts/"
fi
chmod +x "$OUTPUT_DIR/scripts/"*.sh 2>/dev/null || true

# 拷贝 assets (包含 index.html, client.js 及离线 vendor 库)
mkdir -p "$OUTPUT_DIR/assets"
if [ -d "$ROOT_DIR/deploy/unit1-orchestrator/assets" ]; then
    cp -r "$ROOT_DIR/deploy/unit1-orchestrator/assets/"* "$OUTPUT_DIR/assets/"
fi

# 拷贝 public (Portal 网页组件)
mkdir -p "$OUTPUT_DIR/public"
if [ -d "$ROOT_DIR/deploy/unit2-portal/public" ]; then
    cp -r "$ROOT_DIR/deploy/unit2-portal/public/"* "$OUTPUT_DIR/public/"
fi

# 生成环境配置文件示例
cat << 'EOF' > "$OUTPUT_DIR/config.json"
{
  "listen_addr": ":3000",
  "openwiki_cli": "openwiki",
  "openwiki_dist_dir": "/app/dist",
  "static_output_dir": "/data/openwiki/static",
  "vendor_assets_dir": "/app/assets/vendor",
  "repos_base_dir": "/data/openwiki/repos",
  "projects_base_dir": "/data/openwiki/projects",
  "db_path": "/data/openwiki/db/orchestrator.db",
  "max_concurrent_qa": 5,
  "qa_timeout_sec": 120,
  "default_language": "zh-CN"
}
EOF

cat << 'EOF' > "$OUTPUT_DIR/config.yaml"
port: 8080
db_path: "/data/openwiki/db/portal.db"
orchestrator_url: "http://127.0.0.1:3000"
static_output_dir: "/data/openwiki/static"
session_ttl_hours: 24

ldap:
  url: "ldap://127.0.0.1:389"
  base_dn: "dc=company,dc=com"
  user_dn_format: "%s@company.com"
  bind_dn: ""
  bind_password: ""
  insecure_skip_verify: "false"
EOF

echo ""
echo "=============================================================================="
echo "🎉 ARM64 发布包组装完成 (已清除旧产物与 openwiki 编译目录)！"
echo "产物目录: $OUTPUT_DIR"
echo " ├── orchestrator (二进制)"
echo " ├── portal       (二进制)"
echo " ├── scripts/     (qa-daemon.js, json-import-shim.mjs 等)"
echo " ├── assets/      (vendor 离线依赖库)"
echo " ├── public/      (Portal 网页组件)"
echo " └── config.json / config.yaml"
echo "=============================================================================="

#!/usr/bin/env bash
set -euo pipefail

# Build the Go services inside a Linux ARM64 Go toolchain so CGO/SQLite
# produces Linux ELF binaries instead of inheriting the host OS.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
OUTPUT_DIR="$ROOT_DIR/deploy/bin/arm"
BUILDER_IMAGE="${OPENWIKI_GO_BUILDER_IMAGE:-golang:1.22-bookworm}"
GO_PROXY="${GOPROXY:-https://goproxy.cn,direct}"
BUILD_PARALLELISM="${OPENWIKI_GO_BUILD_PARALLELISM:-1}"
GO_MEMORY_LIMIT="${OPENWIKI_GO_MEMORY_LIMIT:-1GiB}"

if ! command -v docker >/dev/null 2>&1; then
    echo "错误: 未找到 docker。请启动 Docker Desktop 后重试。" >&2
    exit 1
fi

if ! docker info >/dev/null 2>&1; then
    echo "错误: Docker daemon 未运行。请启动 Docker Desktop 后重试。" >&2
    exit 1
fi

echo "=============================================================================="
echo "  OpenWiki Linux ARM64 二进制构建"
echo "  项目目录: $ROOT_DIR"
echo "  构建镜像: $BUILDER_IMAGE"
echo "=============================================================================="

rm -rf "$OUTPUT_DIR"
mkdir -p "$OUTPUT_DIR"

docker run --rm \
    --platform linux/arm64 \
    --user "$(id -u):$(id -g)" \
    --env "GOPROXY=$GO_PROXY" \
    --env "BUILD_PARALLELISM=$BUILD_PARALLELISM" \
    --env "GO_MEMORY_LIMIT=$GO_MEMORY_LIMIT" \
    --volume "$ROOT_DIR:/workspace" \
    --workdir /workspace \
    "$BUILDER_IMAGE" \
    bash -c '
        set -euo pipefail
        export PATH=/usr/local/go/bin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
        export HOME=/tmp
        export GOPATH=/tmp/go
        export GOMODCACHE=/tmp/go/pkg/mod
        export GOCACHE=/tmp/go-build
        export GOMAXPROCS=${BUILD_PARALLELISM}
        export GOMEMLIMIT=${GO_MEMORY_LIMIT}
        export GOOS=linux
        export GOARCH=arm64
        export CGO_ENABLED=1

        cd /workspace/deploy/unit1-orchestrator
        go mod download
        go build -p ${BUILD_PARALLELISM} -a -ldflags="-s -w" -o /workspace/deploy/bin/arm/orchestrator .

        cd /workspace/deploy/unit2-portal
        go mod download
        go build -p ${BUILD_PARALLELISM} -a -ldflags="-s -w" -o /workspace/deploy/bin/arm/portal .
    '

chmod +x "$OUTPUT_DIR/orchestrator" "$OUTPUT_DIR/portal"

mkdir -p "$OUTPUT_DIR/scripts" "$OUTPUT_DIR/assets" "$OUTPUT_DIR/public"
cp -R "$ROOT_DIR/deploy/unit1-orchestrator/scripts/." "$OUTPUT_DIR/scripts/"
cp -R "$ROOT_DIR/deploy/unit1-orchestrator/assets/." "$OUTPUT_DIR/assets/"
cp -R "$ROOT_DIR/deploy/unit2-portal/public/." "$OUTPUT_DIR/public/"
chmod +x "$OUTPUT_DIR/scripts/"*.sh 2>/dev/null || true

cat > "$OUTPUT_DIR/config.json" <<'EOF'
{
  "listen_addr": ":3000",
  "openwiki_cli": "openwiki",
  "openwiki_dist_dir": "",
  "static_output_dir": "/data/openwiki/static",
  "vendor_assets_dir": "/app/assets/vendor",
  "repos_base_dir": "/data/openwiki/repos",
  "db_path": "/data/openwiki/db/orchestrator.db",
  "max_concurrent_qa": 5,
  "qa_timeout_sec": 120,
  "default_language": "zh-CN"
}
EOF

cat > "$OUTPUT_DIR/config.yaml" <<'EOF'
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
  insecure_skip_verify: false
EOF

echo ""
echo "Linux ARM64 二进制构建完成:"
file "$OUTPUT_DIR/orchestrator" "$OUTPUT_DIR/portal"

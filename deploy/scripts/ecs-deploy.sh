#!/usr/bin/env bash
set -e

# ==============================================================================
# OpenWiki 阿里云 ECS Workbench 一键安装与升级脚本
# 适用环境：阿里云 ECS (Alibaba Cloud Linux / CentOS / Ubuntu / Debian)
# 架构支持：x86_64 / aarch64 (ARM64)
#
# 用法 (在 ECS 上解压发布包后执行):
#   bash scripts/ecs-deploy.sh
#
# 升级时只需替换 bin/ 目录后重新执行本脚本即可。
# ==============================================================================

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

# 默认安装目录 (可通过 OPENWIKI_INSTALL_DIR 环境变量覆盖)
INSTALL_DIR="${OPENWIKI_INSTALL_DIR:-/opt/openwiki}"
DATA_DIR="/data/openwiki"

echo "=============================================================================="
echo "  ⚡ OpenWiki 阿里云 ECS 部署/升级启动"
echo "  主机架构: $(uname -m), 系统内核: $(uname -r)"
echo "  部署目录: $INSTALL_DIR  数据目录: $DATA_DIR"
echo "=============================================================================="

# 1. 架构检测
ARCH="$(uname -m)"
TARGET_ARCH="x86"
if [ "$ARCH" = "aarch64" ] || [ "$ARCH" = "arm64" ]; then
  TARGET_ARCH="arm"
fi
echo "--> 检测到运行架构: $ARCH (对应二进制发布目录: bin/$TARGET_ARCH)"

# 2. 检查 Docker 运行时
HAS_DOCKER=false
if command -v docker >/dev/null 2>&1; then
  HAS_DOCKER=true
  echo "--> 发现 Docker 运行时: $(docker --version)"
fi

# 3. 部署方式选择
if [ "$HAS_DOCKER" = "true" ]; then
  echo ""
  echo "=== [步骤 1/2] 使用 Docker Compose 快速容器化部署 ==="
  cd "$DEPLOY_DIR"

  # 准备持久化目录
  mkdir -p /data/openwiki/{repos,static,db,logs/nginx,logs/orchestrator,logs/portal} 2>/dev/null || mkdir -p ./data/{repos,static,db,logs}

  echo "--> 停止已有 openwiki 容器..."
  docker compose down 2>/dev/null || docker rm -f openwiki 2>/dev/null || true

  echo "--> 正在构建并启动 All-in-One 镜像 (TARGETARCH=$TARGET_ARCH)..."
  docker compose build --build-arg TARGETARCH=$TARGET_ARCH
  docker compose up -d

  echo "--> 等待容器健康检查探针就绪..."
  for i in {1..30}; do
    if curl -s "http://127.0.0.1/portal/health" | grep -q "ok"; then
      echo "✅ OpenWiki 容器健康探针就绪！"
      break
    fi
    sleep 1
  done

else
  echo ""
  echo "=== [步骤 1/2] 独立二进制后台服务运行模式 ==="
  BIN_PATH="$DEPLOY_DIR/bin/$TARGET_ARCH"
  if [ ! -f "$BIN_PATH/orchestrator" ]; then
    echo "❌ 未找到对应架构的二进制服务: $BIN_PATH/orchestrator"
    exit 1
  fi

  # 准备目录
  mkdir -p "$INSTALL_DIR/bin" "$INSTALL_DIR/public" "$INSTALL_DIR/assets" "$INSTALL_DIR/scripts"
  mkdir -p "$DATA_DIR/repos" "$DATA_DIR/static" "$DATA_DIR/db" "$DATA_DIR/logs"

  # 3.1 复制二进制与资源到安装目录
  echo "--> 复制二进制到 $INSTALL_DIR/bin/ ..."
  cp -f "$BIN_PATH/orchestrator" "$BIN_PATH/portal" "$BIN_PATH/openwiki-migrate" "$INSTALL_DIR/bin/" 2>/dev/null || true
  chmod +x "$INSTALL_DIR/bin/"*

  # 3.2 复制前端静态资源
  echo "--> 复制前端资源..."
  cp -rf "$BIN_PATH/public/"* "$INSTALL_DIR/public/" 2>/dev/null || true
  cp -rf "$BIN_PATH/assets/"* "$INSTALL_DIR/assets/" 2>/dev/null || true
  cp -rf "$BIN_PATH/scripts/"* "$INSTALL_DIR/scripts/" 2>/dev/null || true
  chmod +x "$INSTALL_DIR/scripts/"*.sh 2>/dev/null || true

  # 3.3 创建 openwiki dist 软链 (可视化 graph 导出与 client-lib.js 依赖)
  OPENWIKI_PKG_DIR=""
  for candidate in \
    "$INSTALL_DIR/node22/lib/node_modules/openwiki" \
    "$(npm root -g 2>/dev/null)/openwiki" \
    "/usr/lib/node_modules/openwiki"; do
    if [ -f "$candidate/dist/visualize/graph.js" ]; then
      OPENWIKI_PKG_DIR="$candidate"
      break
    fi
  done
  if [ -n "$OPENWIKI_PKG_DIR" ]; then
    echo "--> 创建 dist 软链: $INSTALL_DIR/dist -> $OPENWIKI_PKG_DIR/dist"
    ln -sfn "$OPENWIKI_PKG_DIR/dist" "$INSTALL_DIR/dist"
  else
    echo "⚠️  未找到 openwiki npm 包的 dist 目录，Wiki 可视化导出将不可用"
    echo "    请确保已安装 openwiki CLI: npm install -g openwiki"
  fi

  # 3.4 生成或保留 orchestrator 配置
  CONFIG_JSON="$INSTALL_DIR/config.json"
  if [ ! -f "$CONFIG_JSON" ]; then
    echo "--> 生成默认 orchestrator 配置..."
    cat << 'EOF' > "$CONFIG_JSON"
{
  "listen_addr": ":3009",
  "openwiki_cli": "/opt/openwiki/node22/bin/openwiki",
  "openwiki_dist_dir": "/opt/openwiki/dist",
  "static_output_dir": "/data/openwiki/static",
  "vendor_assets_dir": "/opt/openwiki/assets/vendor",
  "repos_base_dir": "/data/openwiki/repos",
  "projects_base_dir": "/data/openwiki/projects",
  "db_path": "/data/openwiki/db/orchestrator.db",
  "max_concurrent_qa": 2,
  "qa_timeout_sec": 120,
  "default_language": "zh-CN",
  "openai_upstream_url": "http://127.0.0.1:8889/v1",
  "openwiki_api_key": "",
  "openwiki_provider": "openai-compatible",
  "openwiki_model": "glm-5.2"
}
EOF
    echo "    ⚠️  请编辑 $CONFIG_JSON 填入正确的 openwiki_api_key 和 openai_upstream_url"
  else
    echo "--> 保留已有 orchestrator 配置: $CONFIG_JSON"
  fi

  # 3.5 生成或保留 portal 配置
  CONFIG_YAML="$INSTALL_DIR/config.yaml"
  if [ ! -f "$CONFIG_YAML" ]; then
    echo "--> 生成默认 portal 配置..."
    cat << 'EOF' > "$CONFIG_YAML"
port: 8099
db_path: "/data/openwiki/db/portal.db"
orchestrator_url: "http://127.0.0.1:3009"
static_output_dir: "/data/openwiki/static"
session_ttl_hours: 24

ldap:
  enabled: false
  url: "mock"

auth:
  admin_users:
    - "admin"
    - "bigc"
    - "lisi"
EOF
  else
    echo "--> 保留已有 portal 配置: $CONFIG_YAML"
  fi

  # 3.6 安装 nginx 反向代理配置
  if command -v nginx >/dev/null 2>&1; then
    NGINX_CONF_SRC="$DEPLOY_DIR/nginx-standalone.conf"
    if [ ! -f "$NGINX_CONF_SRC" ]; then
      NGINX_CONF_SRC="$SCRIPT_DIR/../nginx-standalone.conf"
    fi
    if [ -f "$NGINX_CONF_SRC" ]; then
      echo "--> 安装 nginx 配置..."
      cp -f "$NGINX_CONF_SRC" /etc/nginx/conf.d/openwiki.conf
      nginx -t 2>&1 && nginx -s reload 2>&1 && echo "    nginx 配置已更新" || echo "    ⚠️ nginx 配置测试失败，请手动检查"
    fi
  fi

  # 3.7 停止旧进程
  echo "--> 停止已有服务进程..."
  pkill -f "$INSTALL_DIR/bin/orchestrator" 2>/dev/null || true
  pkill -f "$INSTALL_DIR/bin/portal" 2>/dev/null || true
  sleep 2

  # 3.8 启动新进程
  echo "--> 启动 Orchestrator (端口: 3009)..."
  nohup "$INSTALL_DIR/bin/orchestrator" -config "$CONFIG_JSON" > "$DATA_DIR/logs/orchestrator.log" 2>&1 &

  echo "--> 启动 Portal (端口: 8099)..."
  cd "$INSTALL_DIR"
  nohup "$INSTALL_DIR/bin/portal" -config "$CONFIG_YAML" -public "$INSTALL_DIR/public" > "$DATA_DIR/logs/portal.log" 2>&1 &

  echo "--> 等待服务启动..."
  sleep 3

  # 3.9 健康检查
  for i in {1..10}; do
    if curl -s "http://127.0.0.1:3009/api/health" | grep -q "ok" && curl -s "http://127.0.0.1:8099/portal/health" | grep -q "ok"; then
      echo "✅ Orchestrator 与 Portal 健康检查通过！"
      break
    fi
    sleep 1
  done
fi

# 4. 获取公网 IP
PUBLIC_IP=$(curl -s --connect-timeout 2 http://100.100.100.200/latest/meta-data/eipv4 2>/dev/null || curl -s --connect-timeout 2 ifconfig.me 2>/dev/null || echo "127.0.0.1")

# 5. 确定访问端口
if [ "$HAS_DOCKER" = "true" ]; then
  WEB_PORT="80"
else
  WEB_PORT="8891"
fi

echo ""
echo "=============================================================================="
echo "🎉 OpenWiki 服务已成功在 ECS 上启动运行！"
echo "=============================================================================="
echo ""
echo " 🌐 访问地址:"
echo "   - Web 控制台门户: http://$PUBLIC_IP:$WEB_PORT/portal/"
echo "   - 运维与批量调度: http://$PUBLIC_IP:$WEB_PORT/portal/admin.html"
echo "   - Wiki 可视化: http://$PUBLIC_IP:$WEB_PORT/wiki/<repo_id>/"
if [ "$HAS_DOCKER" = "true" ]; then
  echo "   - Orchestrator API: http://$PUBLIC_IP:3000/"
else
  echo "   - Orchestrator API: http://$PUBLIC_IP:3009/"
fi
echo ""
echo " 🔑 初始登录账号: admin"
echo " 🔑 初始登录密码: password (或企业 AD/LDAP 域账号)"
echo ""
echo " 📋 核心功能验证指南:"
echo "   1. 批量导入基线: 登录 admin.html 后点击【📥 批量导入基线清单】，粘贴近3年发版列表"
echo "   2. 动态限流保护: 拖动【⚡ 动态并发限流控制】滑块 (1~8) 并点击【实时应用】"
echo "   3. 进度与ETA观测: 查看实时槽位指示灯与【动态预估剩余耗时 (ETA)】"
echo "   4. 飞书报表导出: 在底部点击【📋 一键复制飞书表格数据 (TSV)】即可直接粘贴到飞书多维表格"
echo "=============================================================================="

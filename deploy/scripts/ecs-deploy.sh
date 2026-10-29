#!/usr/bin/env bash
set -e

# ==============================================================================
# OpenWiki 阿里云 ECS Workbench 一键安装与服务部署脚本
# 适用环境：阿里云 ECS (Alibaba Cloud Linux / CentOS / Ubuntu / Debian)
# 架构支持：x86_64 / aarch64 (ARM64)
# ==============================================================================

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

echo "=============================================================================="
echo "  ⚡ OpenWiki 阿里云 ECS 一键部署启动"
echo "  主机架构: $(uname -m), 系统内核: $(uname -r)"
echo "  部署目录: $DEPLOY_DIR"
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

  DATA_DIR="$DEPLOY_DIR/data"
  mkdir -p "$DATA_DIR/repos" "$DATA_DIR/static" "$DATA_DIR/db" "$DATA_DIR/logs"

  # 杀掉已有后台进程
  pkill -f "$BIN_PATH/orchestrator" 2>/dev/null || true
  pkill -f "$BIN_PATH/portal" 2>/dev/null || true

  echo "--> 启动 Orchestrator (端口: 3000)..."
  "$BIN_PATH/orchestrator" -config "$BIN_PATH/config.json" > "$DATA_DIR/logs/orchestrator.log" 2>&1 &

  echo "--> 启动 Portal (端口: 8080)..."
  cd "$BIN_PATH"
  ./portal -config "$BIN_PATH/config.yaml" > "$DATA_DIR/logs/portal.log" 2>&1 &

  echo "--> 等待服务启动..."
  sleep 3
fi

# 4. 获取公网 IP
PUBLIC_IP=$(curl -s --connect-timeout 2 http://100.100.100.200/latest/meta-data/eipv4 2>/dev/null || curl -s --connect-timeout 2 ifconfig.me 2>/dev/null || echo "127.0.0.1")

echo ""
echo "=============================================================================="
echo "🎉 OpenWiki 服务已成功在 ECS 上启动运行！"
echo "=============================================================================="
echo ""
echo " 🌐 访问地址:"
if [ "$HAS_DOCKER" = "true" ]; then
  echo "   - Web 控制台门户: http://$PUBLIC_IP/portal/"
  echo "   - 运维与批量调度: http://$PUBLIC_IP/portal/admin.html"
  echo "   - Orchestrator API: http://$PUBLIC_IP:3000/"
else
  echo "   - Web 控制台门户: http://$PUBLIC_IP:8080/portal/"
  echo "   - 运维与批量调度: http://$PUBLIC_IP:8080/portal/admin.html"
  echo "   - Orchestrator API: http://$PUBLIC_IP:3000/"
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

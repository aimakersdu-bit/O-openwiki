#!/usr/bin/env bash
set -e

# ==============================================================================
# OpenWiki All-in-One 全家桶容器启动控制脚本 (entrypoint.sh)
# 职责：
#  1. 初始化数据目录结构与配置文件
#  2. 启动后门后台服务 (Cron/Logrotate)
#  3. 后台启动 Orchestrator (3000端口) 与 Portal (8080端口)
#  4. 设置 SIGTERM / SIGINT 信号捕获实现优雅停机
#  5. 前台启动 Nginx (80端口) 进行服务托管与反向代理
# ==============================================================================

echo "=============================================================================="
echo "🚀 启动 OpenWiki All-in-One 单容器服务环境"
echo "=============================================================================="

# 1. 确保有状态持久化挂载目录存在
mkdir -p /data/openwiki/repos \
         /data/openwiki/static \
         /data/openwiki/db \
         /data/openwiki/logs/nginx \
         /data/openwiki/logs/orchestrator \
         /data/openwiki/logs/portal

# 1.5 自动初始化全局 Git HTTP 统一凭据 (若环境变量提供)
GIT_USER="${GIT_HTTP_USERNAME:-$GIT_USERNAME}"
GIT_PASS="${GIT_HTTP_PASSWORD:-$GIT_PASSWORD}"
GIT_TOK="${GIT_HTTP_TOKEN:-$GIT_TOKEN}"

if [ -n "$GIT_PASS" ] && [ -n "$GIT_USER" ]; then
    echo "--> 自动配置容器全局 Git HTTP 统一凭据 (用户: $GIT_USER)..."
    git config --global credential.helper 'store --file=/root/.git-credentials'
    echo "https://${GIT_USER}:${GIT_PASS}@${GIT_HTTP_HOST:-gitlab.company.com}" > /root/.git-credentials
elif [ -n "$GIT_TOK" ]; then
    echo "--> 自动配置容器全局 Git HTTP Token 统一凭据..."
    git config --global credential.helper 'store --file=/root/.git-credentials'
    USER_NAME="${GIT_USER:-oauth2}"
    echo "https://${USER_NAME}:${GIT_TOK}@${GIT_HTTP_HOST:-gitlab.company.com}" > /root/.git-credentials
fi

# 2. 生成 Orchestrator 默认配置文件 (如不存在)
if [ ! -f /app/config.json ]; then
    echo "--> 生成 Orchestrator 默认配置文件 config.json..."
    cat << 'EOF' > /app/config.json
{
  "listen_addr": ":3000",
  "openwiki_cli": "openwiki",
  "openwiki_dist_dir": "/app/dist",
  "static_output_dir": "/data/openwiki/static",
  "vendor_assets_dir": "/app/assets/vendor",
  "repos_base_dir": "/data/openwiki/repos",
  "db_path": "/data/openwiki/db/orchestrator.db",
  "max_concurrent_qa": 5,
  "qa_timeout_sec": 120,
  "default_language": "zh-CN"
}
EOF
fi

# 3. 生成 Portal 默认配置文件 (如不存在)
if [ ! -f /app/config.yaml ]; then
    echo "--> 生成 Portal 默认配置文件 config.yaml..."
    cat << 'EOF' > /app/config.yaml
port: 8080
db_path: "/data/openwiki/db/portal.db"
orchestrator_url: "http://127.0.0.1:3000"
static_output_dir: "/data/openwiki/static"
session_ttl_hours: 24
EOF
fi

# 4. 启动后台日志滚存定时任务 (Cron)
if command -v cron >/dev/null 2>&1; then
    echo "--> 启动日志滚存调度 Cron..."
    cron || true
fi

# 5. 优雅关机信号处理
cleanup() {
    echo ""
    echo "🛑 接收到关机信号，正在停止所有 openwiki 服务..."
    pkill -TERM portal 2>/dev/null || true
    pkill -TERM orchestrator 2>/dev/null || true
    nginx -s quit 2>/dev/null || true
    echo "✅ 服务已安全关闭"
    exit 0
}
trap cleanup SIGTERM SIGINT

# 6. 后台启动 Unit 1 Orchestrator
echo "--> 启动 Unit 1 Orchestrator (:3000)..."
/app/orchestrator -config /app/config.json > /data/openwiki/logs/orchestrator/app.log 2>&1 &
ORCH_PID=$!

# 7. 后台启动 Unit 2 Portal
echo "--> 启动 Unit 2 Portal (:8080)..."
/app/portal -config /app/config.yaml -public /app/public > /data/openwiki/logs/portal/app.log 2>&1 &
PORTAL_PID=$!

# 8. 等待后端微服务起摆准备
echo "--> 等待后端微服务初始化完成..."
sleep 2

# 检查进程健康状态
if ! kill -0 $ORCH_PID 2>/dev/null; then
    echo "❌ 错误: Orchestrator 启动失败，请检查日志 /data/openwiki/logs/orchestrator/app.log"
    cat /data/openwiki/logs/orchestrator/app.log
    exit 1
fi

if ! kill -0 $PORTAL_PID 2>/dev/null; then
    echo "❌ 错误: Portal 启动失败，请检查日志 /data/openwiki/logs/portal/app.log"
    cat /data/openwiki/logs/portal/app.log
    exit 1
fi

echo "=============================================================================="
echo "🎉 所有 openwiki 后端服务已正常启动！"
echo " Orchestrator PID: $ORCH_PID"
echo " Portal PID:       $PORTAL_PID"
echo " 前台拉起 Nginx 托管 80 端口..."
echo "=============================================================================="

# 9. 前台运行 Nginx
exec nginx -g "daemon off;"

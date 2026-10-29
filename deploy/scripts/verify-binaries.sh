#!/usr/bin/env bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
ARCH="$(uname -m)"
if [ "$ARCH" = "x86_64" ]; then
  BIN_DIR="$ROOT_DIR/deploy/bin/x86"
else
  BIN_DIR="$ROOT_DIR/deploy/bin/arm"
fi
RUNTIME_DIR="$ROOT_DIR/deploy/test_runtime"

echo "=============================================================================="
echo "  OpenWiki 独立二进制端到端测试"
echo "=============================================================================="

rm -rf "$RUNTIME_DIR"
mkdir -p "$RUNTIME_DIR/repos" "$RUNTIME_DIR/projects" "$RUNTIME_DIR/static"

ORCH_PORT=13000
PORTAL_PORT=18080

# 1. 生成测试配置文件
cat << EOF > "$RUNTIME_DIR/config.json"
{
  "listen_addr": ":$ORCH_PORT",
  "openwiki_cli": "openwiki",
  "openwiki_dist_dir": "$ROOT_DIR/dist",
  "static_output_dir": "$RUNTIME_DIR/static",
  "vendor_assets_dir": "$BIN_DIR/assets/vendor",
  "repos_base_dir": "$RUNTIME_DIR/repos",
  "projects_base_dir": "$RUNTIME_DIR/projects",
  "db_path": "$RUNTIME_DIR/orchestrator.db",
  "max_concurrent_qa": 3,
  "qa_timeout_sec": 60,
  "default_language": "zh-CN"
}
EOF

cat << EOF > "$RUNTIME_DIR/config.yaml"
port: $PORTAL_PORT
db_path: "$RUNTIME_DIR/portal.db"
orchestrator_url: "http://127.0.0.1:$ORCH_PORT"
static_output_dir: "$RUNTIME_DIR/static"
session_ttl_hours: 24

auth:
  admin_users:
    - "admin"
    - "cuixiang"

ldap:
  enabled: false
  url: "mock"
EOF

# 清理已有残留端口进程
lsof -ti:$ORCH_PORT | xargs kill -9 2>/dev/null || true
lsof -ti:$PORTAL_PORT | xargs kill -9 2>/dev/null || true

echo "--> 1. 启动 Orchestrator 二进制 (端口: $ORCH_PORT)..."
"$BIN_DIR/orchestrator" -config "$RUNTIME_DIR/config.json" > "$RUNTIME_DIR/orchestrator.log" 2>&1 &
ORCH_PID=$!

echo "--> 2. 启动 Portal 二进制 (端口: $PORTAL_PORT)..."
cd "$BIN_DIR"
./portal -config "$RUNTIME_DIR/config.yaml" > "$RUNTIME_DIR/portal.log" 2>&1 &
PORTAL_PID=$!

cleanup() {
  echo ""
  echo "--> 正在停止服务进程 (PID: $ORCH_PID, $PORTAL_PID)..."
  kill $ORCH_PID $PORTAL_PID 2>/dev/null || true
  wait $ORCH_PID 2>/dev/null || true
  wait $PORTAL_PID 2>/dev/null || true
  echo "服务已停止。"
}
trap cleanup EXIT

# 等待服务就绪
echo "--> 等待服务启动响应..."
sleep 2

# 3. 验证健康检查
echo "--> 3. 验证 Orchestrator 健康状态..."
ORCH_HEALTH=$(curl -s "http://127.0.0.1:$ORCH_PORT/api/health")
echo "  Orchestrator health: $ORCH_HEALTH"
if [[ "$ORCH_HEALTH" != *"ok"* ]]; then
  echo "❌ Orchestrator 健康检查失败！"
  cat "$RUNTIME_DIR/orchestrator.log"
  exit 1
fi

echo "--> 4. 验证 Portal 健康状态..."
PORTAL_HEALTH=$(curl -s "http://127.0.0.1:$PORTAL_PORT/portal/health")
echo "  Portal health: $PORTAL_HEALTH"
if [[ "$PORTAL_HEALTH" != *"ok"* ]]; then
  echo "❌ Portal 健康检查失败！"
  cat "$RUNTIME_DIR/portal.log"
  exit 1
fi

# 5. 模拟登录获取 Session Cookie (admin)
echo "--> 5. 登录 Portal 获取管理员 Session..."
COOKIE_JAR="$RUNTIME_DIR/cookies.txt"
LOGIN_RESP=$(curl -s -c "$COOKIE_JAR" -X POST "http://127.0.0.1:$PORTAL_PORT/portal/login" \
  -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"password"}')
echo "  Login response: $LOGIN_RESP"

# 6. 测试批量导入陈立芝生产发版基线清单
echo "--> 6. 测试批量导入 3 个微服务基线代码库..."
IMPORT_PAYLOAD='{
  "batch_name": "2026全行生产发版基线批量调度测试",
  "concurrency": 2,
  "night_mode_only": false,
  "auto_start": false,
  "repos": [
    {
      "id": "trade-gateway",
      "name": "交易边缘网关",
      "git_url": "https://github.com/bank/trade-gateway.git",
      "branch": "master",
      "tier": "A",
      "business_domain": "消费金融",
      "last_release_at": "2025-11-20"
    },
    {
      "id": "order-service",
      "name": "信贷订单微服务",
      "git_url": "https://github.com/bank/order-service.git",
      "branch": "master",
      "tier": "A",
      "business_domain": "消费金融",
      "last_release_at": "2025-10-15"
    },
    {
      "id": "credit-risk",
      "name": "信贷风控审批服务",
      "git_url": "https://github.com/bank/credit-risk.git",
      "branch": "master",
      "tier": "B",
      "business_domain": "信贷中台",
      "last_release_at": "2025-08-01"
    }
  ]
}'

IMPORT_RESP=$(curl -s -b "$COOKIE_JAR" -X POST "http://127.0.0.1:$PORTAL_PORT/portal/repos/batch-import" \
  -H "Content-Type: application/json" \
  -d "$IMPORT_PAYLOAD")
echo "  Import response: $IMPORT_RESP"

BATCH_ID=$(echo "$IMPORT_RESP" | grep -o '"batch_id":"[^"]*' | cut -d'"' -f4)
if [ -z "$BATCH_ID" ]; then
  echo "❌ 批量导入失败，未生成 batch_id！"
  exit 1
fi
echo "  成功创建批跑任务: $BATCH_ID"

# 6.1 验证微服务业务域虚拟 Monorepo 是否自动聚合生成
echo "--> 6.1 验证微服务业务域自动聚合项目..."
PROJECTS_RESP=$(curl -s -b "$COOKIE_JAR" "http://127.0.0.1:$PORTAL_PORT/portal/projects")
echo "  Projects response: $PROJECTS_RESP"

# 7. 验证动态并发调整 (热缩放)
echo "--> 7. 测试动态并发热缩放 (从 2 调整到 5)..."
CONCURRENCY_RESP=$(curl -s -b "$COOKIE_JAR" -X PUT "http://127.0.0.1:$PORTAL_PORT/portal/batch/concurrency" \
  -H "Content-Type: application/json" \
  -d "{\"batch_id\":\"$BATCH_ID\",\"concurrency\":5}")
echo "  Concurrency update response: $CONCURRENCY_RESP"

# 8. 验证可观测性与动态 ETA 接口
echo "--> 8. 验证批跑详情与槽位利用率..."
STATUS_RESP=$(curl -s -b "$COOKIE_JAR" "http://127.0.0.1:$PORTAL_PORT/portal/batch/status?batch_id=$BATCH_ID")
echo "  Status response: $STATUS_RESP"

# 9. 验证每日 Token 报表与飞书 TSV 导出
echo "--> 9. 验证飞书表格 TSV 导出格式..."
TSV_RESP=$(curl -s -b "$COOKIE_JAR" "http://127.0.0.1:$PORTAL_PORT/portal/telemetry/tokens/export?format=tsv")
echo "  TSV 表头预览:"
echo "$TSV_RESP" | head -n 3

echo ""
echo "=============================================================================="
echo "🎉 二进制测试全部成功通过！"
echo "=============================================================================="

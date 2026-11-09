#!/usr/bin/env bash
# ==============================================================================
# OpenWiki 多仓架构 §3.1 存储卷注册 + 多仓聚合/知识纳管 端到端联调脚本
#
# 用途:
#   1. 在测试环境用目录模拟 4 块 100GB 盘 (vol0-vol3), 创建三层软链目录结构
#   2. 启动 orchestrator (repos_base_dir 指向逻辑访问层 /data/disk0/links/repos)
#   3. 注册 4 个存储卷 (POST /api/storage/volumes)
#   4. 验证: 单仓注册 (G2 强制 business_domain) + 批量导入 (G2 校验)
#   5. 验证: 多仓项目 architecture_md 字段写入 + knowledge 纳管软链
#
# 不依赖真实 LLM / openwiki CLI; 构建步骤在 CLI 缺失时会失败但不会崩溃,
# 本脚本只校验我们改动的代码路径 (DB/软链/architecture.md/knowledge 软链).
# ==============================================================================
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
TMP_DIR="${TMP_DIR:-/tmp/openwiki-volume-smoke}"

ORCH_PORT="${ORCH_PORT:-3099}"
ORCH="${ORCH:-$DEPLOY_DIR/bin/orchestrator}"

# ---------- 清理 ----------
cleanup() {
  echo "--> 清理后台进程与临时目录..."
  [ -n "$ORCH_PID" ] && kill "$ORCH_PID" 2>/dev/null || true
}
trap cleanup EXIT

echo "=========================================================="
echo "  §3.1 存储卷注册 + 多仓聚合/知识纳管 端到端联调"
echo "  临时目录: $TMP_DIR   Orchestrator: :$ORCH_PORT"
echo "=========================================================="

# ---------- 1. 目录模拟多盘 ----------
echo ""
echo "== [1/6] 创建目录模拟多盘 (vol0-vol3, 100GB 模拟) =="
rm -rf "$TMP_DIR"
DATA="$TMP_DIR/data"
mkdir -p "$DATA/disk0/repos" "$DATA/disk0/links/repos"
mkdir -p "$DATA/disk1/repos" "$DATA/disk2/repos" "$DATA/disk3/repos"
mkdir -p "$DATA/openwiki/db" "$DATA/openwiki/projects" "$DATA/openwiki/static"
mkdir -p "$DATA/disk_log/builds"

echo "  /data/disk0  (repos + links/repos 逻辑访问层)"
echo "  /data/disk{1,2,3}/repos"
echo "  /data/openwiki/{db,projects,static}"

# ---------- 2. 配置 + 启动 orchestrator ----------
echo ""
echo "== [2/6] 启动 orchestrator (repos_base_dir → links/repos 逻辑访问层) =="
cat > "$TMP_DIR/orch-config.json" <<EOF
{
  "listen_addr": ":$ORCH_PORT",
  "openwiki_cli": "openwiki-not-found-for-smoke",
  "static_output_dir": "$DATA/openwiki/static",
  "vendor_assets_dir": "$TMP_DIR/vendor",
  "repos_base_dir": "$DATA/disk0/links/repos",
  "projects_base_dir": "$DATA/openwiki/projects",
  "log_base_dir": "$DATA/disk_log",
  "db_path": "$DATA/openwiki/db/orchestrator.db",
  "default_language": "zh-CN"
}
EOF
mkdir -p "$TMP_DIR/vendor"

"$ORCH" -config "$TMP_DIR/orch-config.json" > "$TMP_DIR/orch.log" 2>&1 &
ORCH_PID=$!

echo "--> 等待 orchestrator 启动..."
for i in $(seq 1 30); do
  if curl -sf "http://localhost:$ORCH_PORT/api/health" >/dev/null 2>&1; then
    echo "    orchestrator 已就绪"; break
  fi
  sleep 0.5
done
curl -sf "http://localhost:$ORCH_PORT/api/health" >/dev/null || { echo "FAILED: orchestrator 未启动"; tail -20 "$TMP_DIR/orch.log"; exit 1; }

# ---------- 3. 注册存储卷 ----------
echo ""
echo "== [3/6] 注册 vol0-vol3 (POST /api/storage/volumes) =="
for pair in "vol0:$DATA/disk0" "vol1:$DATA/disk1" "vol2:$DATA/disk2" "vol3:$DATA/disk3"; do
  vol="${pair%%:*}"; mp="${pair##*:}"
  resp=$(curl -sf -X POST "http://localhost:$ORCH_PORT/api/storage/volumes" \
    -H "Content-Type: application/json" \
    -d "{\"id\":\"$vol\",\"mount_point\":\"$mp\"}")
  echo "  $vol → $mp : $(echo "$resp" | tr -d '\n')"
done

VOLS=$(curl -sf "http://localhost:$ORCH_PORT/api/storage/volumes")
echo "  当前卷列表: $VOLS"
echo "$VOLS" | grep -q '"vol0"' || { echo "FAILED: vol0 未注册"; exit 1; }
echo "$VOLS" | grep -q '"vol3"' || { echo "FAILED: vol3 未注册"; exit 1; }
echo "  ✓ 4 卷注册成功"

# ---------- 4. G2 验证: 单仓注册强制 business_domain ----------
echo ""
echo "== [4/6] G2 校验: 单仓注册强制 business_domain =="
# 4a. 缺 business_domain → 应 400
CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "http://localhost:$ORCH_PORT/api/repos" \
  -H "Content-Type: application/json" \
  -d '{"id":"svc-no-domain","git_url":"http://gitlab/svc.git","branch":"main"}')
echo "  缺 business_domain → HTTP $CODE (期望 400)"
[ "$CODE" = "400" ] || { echo "FAILED: 缺 business_domain 应被拒绝"; exit 1; }

# 4b. 带 business_domain → 应 201
REPO=$(curl -sf -X POST "http://localhost:$ORCH_PORT/api/repos" \
  -H "Content-Type: application/json" \
  -d '{"id":"svc-payment","name":"payment-service","git_url":"http://gitlab/svc/payment.git","branch":"master","business_domain":"消金","tier":"A"}')
echo "  带 business_domain → 201 : $(echo "$REPO" | grep -o '"business_domain":"[^"]*"')"
echo "$REPO" | grep -q '"business_domain":"消金"' || { echo "FAILED: business_domain 未持久化"; exit 1; }
echo "  ✓ G2 单仓校验通过"

# ---------- 5. G2 验证: 批量导入缺 domain 的行被跳过 ----------
echo ""
echo "== [5/6] G2 校验: 批量导入缺 business_domain 行被跳过 =="
BATCH=$(curl -sf -X POST "http://localhost:$ORCH_PORT/api/repos/batch-import" \
  -H "Content-Type: application/json" \
  -d '{
    "batch_name":"smoke-batch","concurrency":1,"auto_start":false,
    "repos":[
      {"id":"svc-risk","git_url":"http://gitlab/svc/risk.git","business_domain":"风控","tier":"B"},
      {"id":"svc-no-domain-batch","git_url":"http://gitlab/svc/none.git","tier":"B"}
    ]
  }')
echo "  响应: $BATCH"
echo "$BATCH" | grep -q '"total_imported":1' || { echo "FAILED: 应仅导入 1 条"; exit 1; }
echo "$BATCH" | grep -q '"skipped":1' || { echo "FAILED: 应跳过 1 条"; exit 1; }
echo "$BATCH" | grep -q 'svc-no-domain-batch' || { echo "FAILED: 错误列表未报告缺 domain 行"; exit 1; }
echo "  ✓ G2 批量导入校验通过 (跳过+错误报告)"

# ---------- 6. P2.3/P3 验证: 多仓项目 architecture_md + knowledge 软链 ----------
echo ""
echo "== [6/6] P2.3 校验: 多仓项目 architecture_md 写入 (P3 由单测覆盖) =="
# 准备一个带 doc/knowledge 的物理仓库目录 (模拟 VolumeManager 已放置的仓库)
PHYS="$DATA/disk0/repos/svc-payment"
mkdir -p "$PHYS/doc/knowledge/sub" "$PHYS/src"
echo "# 支付领域知识\n\n交易流程说明" > "$PHYS/doc/knowledge/pay-flow.md"
echo "# 风控策略" > "$PHYS/doc/knowledge/sub/risk-policy.md"
# 逻辑层软链 (模拟 VolumeManager.CreateRepoPlacement 产物)
ln -sf "$PHYS" "$DATA/disk0/links/repos/svc-payment"

# 创建多仓项目 (带 architecture_md) —— 用 python 构造合法 JSON, 避免裸换行
python3 - "$TMP_DIR/proj-payload.json" <<'PYJSON'
import json, sys
arch = "# 消金系统架构\n\n## 服务职责\n- svc-payment: 支付核心\n## 调用关系\n支付 -> 风控"
payload = {
    "id": "proj-smoke", "name": "消金微服务集群", "description": "smoke",
    "business_domain": "消金", "architecture_md": arch,
    "repos": [{"repo_id": "svc-payment", "alias_name": "payment", "role": "core"}],
}
with open(sys.argv[1], "w") as f:
    json.dump(payload, f, ensure_ascii=False)
PYJSON
CREATE=$(curl -sf -X POST "http://localhost:$ORCH_PORT/api/projects" \
  -H "Content-Type: application/json" \
  --data-binary @"$TMP_DIR/proj-payload.json")
echo "  创建项目: $(echo "$CREATE" | grep -o '\"id\":\"[^\"]*\"')"
echo "$CREATE" | grep -q '"architecture_md"' || { echo "FAILED: 响应未含 architecture_md"; exit 1; }
echo "  ✓ P2.3 项目 architecture_md 持久化到 DB"

# 触发项目构建 (BuildProjectWorkspace 会写 architecture.md + 调 builder;
#   openwiki CLI 不存在, builder 会失败, 但 architecture.md 与 knowledge 软链在 Step 0.5 之前已落盘)
curl -sf -X POST "http://localhost:$ORCH_PORT/api/projects/build" \
  -H "Content-Type: application/json" \
  -d '{"project_id":"proj-smoke"}' >/dev/null
# 构建是异步 goroutine; 等待落盘
sleep 2

WS="$DATA/openwiki/projects/proj-smoke"
ARCH_FILE="$WS/_project_hub/openwiki/architecture.md"
if [ -f "$ARCH_FILE" ]; then
  echo "  ✓ P2.3 architecture.md 已写入: $ARCH_FILE"
  head -3 "$ARCH_FILE" | sed 's/^/    | /'
else
  echo "FAILED: architecture.md 未生成"; exit 1
fi

# 校验 quickstart.md 仍然生成 (回归)
[ -f "$WS/_project_hub/openwiki/quickstart.md" ] && echo "  ✓ 回归: quickstart.md 存在" || { echo "FAILED: quickstart.md 丢失"; exit 1; }

# 注: P3 ingestKnowledge (doc/knowledge → openwiki/knowledge 软链) 在单仓构建路径
# (BuildPipeline.Execute → Builder.BuildRepoWithBuildID Step 0.5) 触发, 多仓项目构建
# (BuildProjectWorkspace) 走 wsDir 不含 doc/knowledge, 故 P3 软链由单测
# TestIngestKnowledge_* 覆盖, 此处不重复断言.
echo "  ℹ P3 knowledge 纳管由单测 TestIngestKnowledge_* 覆盖 (单仓构建路径)"

echo ""
echo "=========================================================="
echo "  全部联调校验通过 ✓"
echo "  - 4 卷注册 (目录模拟多盘)"
echo "  - G2 单仓/批量 business_domain 强制校验"
echo "  - P2.3 architecture_md DB 持久化 + 文件落盘"
echo "  - P3 doc/knowledge → openwiki/knowledge 软链纳管 (单测覆盖)"
echo "  产物目录: $TMP_DIR"
echo "  orchestrator 日志: $TMP_DIR/orch.log"
echo "=========================================================="

#!/usr/bin/env bash
# ==============================================================================
# OpenWiki 单容器健康检查与就绪探针 (healthcheck.sh)
# 职责：
#  1. 检查 Orchestrator (:3000) 守护进程与 HTTP 接口连通性
#  2. 检查 Portal (:8080) 守护进程与 HTTP 接口连通性
#  3. 检查 Nginx (:80) 前端代理与静态文件托管连通性
# 出错时返回 exit code 1，成功返回 exit code 0
# ==============================================================================

# 1. 检查 Orchestrator (:3000)
if ! pgrep -x "orchestrator" > /dev/null; then
    echo "HEALTHCHECK FAILED: orchestrator process not running"
    exit 1
fi
ORCH_CODE=$(curl -s -o /dev/null -w "%{http_code}" http://127.0.0.1:3000/api/builds || echo "000")
if [ "$ORCH_CODE" -ne 200 ] && [ "$ORCH_CODE" -ne 401 ] && [ "$ORCH_CODE" -ne 404 ]; then
    echo "HEALTHCHECK FAILED: orchestrator HTTP code $ORCH_CODE"
    exit 1
fi

# 2. 检查 Portal (:8080)
if ! pgrep -x "portal" > /dev/null; then
    echo "HEALTHCHECK FAILED: portal process not running"
    exit 1
fi
PORTAL_CODE=$(curl -s -o /dev/null -w "%{http_code}" http://127.0.0.1:8080/api/repos || echo "000")
if [ "$PORTAL_CODE" -ne 200 ] && [ "$PORTAL_CODE" -ne 401 ]; then
    echo "HEALTHCHECK FAILED: portal HTTP code $PORTAL_CODE"
    exit 1
fi

# 3. 检查 Nginx (:80)
NGINX_CODE=$(curl -s -o /dev/null -w "%{http_code}" http://127.0.0.1:80/ || echo "000")
if [ "$NGINX_CODE" -ne 200 ] && [ "$NGINX_CODE" -ne 302 ] && [ "$NGINX_CODE" -ne 404 ]; then
    echo "HEALTHCHECK FAILED: Nginx HTTP code $NGINX_CODE"
    exit 1
fi

# 全部通过
exit 0

#!/usr/bin/env bash
set -e

echo "=========================================="
echo " Running End-to-End (E2E) Integration Tests"
echo "=========================================="

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_DIR="$(dirname "$SCRIPT_DIR")"
TMP_DIR=$(mktemp -d)

ORCH_PORT=3099
PORTAL_PORT=8099
COOKIE_FILE="$TMP_DIR/cookie.txt"

cleanup() {
    echo "--> Cleaning up background processes and temporary files..."
    if [ -n "$ORCH_PID" ]; then kill "$ORCH_PID" 2>/dev/null || true; fi
    if [ -n "$PORTAL_PID" ]; then kill "$PORTAL_PID" 2>/dev/null || true; fi
    rm -rf "$TMP_DIR"
}
trap cleanup EXIT

echo "--> Building binaries for test..."
cd "$DEPLOY_DIR/unit1-orchestrator"
go build -o "$TMP_DIR/orchestrator" .

cd "$DEPLOY_DIR/unit2-portal"
go build -o "$TMP_DIR/portal" .

echo "--> Starting Unit 1 (orchestrator) on port $ORCH_PORT..."
export LISTEN_ADDR=":$ORCH_PORT"
export DB_PATH="$TMP_DIR/orchestrator.db"
export STATIC_OUTPUT_DIR="$TMP_DIR/wiki"
export VENDOR_ASSETS_DIR="$TMP_DIR/vendor"
mkdir -p "$STATIC_OUTPUT_DIR" "$VENDOR_ASSETS_DIR"
cat <<EOF > "$TMP_DIR/orch-config.json"
{
  "listen_addr": ":$ORCH_PORT",
  "openwiki_cli": "openwiki",
  "static_output_dir": "$TMP_DIR/wiki",
  "vendor_assets_dir": "$TMP_DIR/vendor",
  "db_path": "$TMP_DIR/orchestrator.db"
}
EOF
"$TMP_DIR/orchestrator" -config "$TMP_DIR/orch-config.json" > "$TMP_DIR/orch.log" 2>&1 &
ORCH_PID=$!

echo "--> Starting Unit 2 (portal) on port $PORTAL_PORT..."
export PORT="$PORTAL_PORT"
export DB_PATH="$TMP_DIR/portal.db"
export LDAP_URL="mock"
export ORCHESTRATOR_URL="http://localhost:$ORCH_PORT"
cat <<EOF > "$TMP_DIR/portal-config.yaml"
port: $PORTAL_PORT
db_path: "$TMP_DIR/portal.db"
orchestrator_url: "http://localhost:$ORCH_PORT"
auth:
  admin_users: ["e2e_user"]
ldap:
  url: "mock"
EOF
"$TMP_DIR/portal" -config "$TMP_DIR/portal-config.yaml" > "$TMP_DIR/portal.log" 2>&1 &
PORTAL_PID=$!

echo "--> Waiting for services to initialize..."
for i in {1..20}; do
    if curl -s "http://localhost:$ORCH_PORT/api/health" > /dev/null && curl -s "http://localhost:$PORTAL_PORT/portal/health" > /dev/null; then
        echo "--> Services are UP and healthy!"
        break
    fi
    sleep 0.5
done

# --- Test Cases ---

echo "--> [Test 1] Health Check Unit 1..."
ORCH_HEALTH=$(curl -s "http://localhost:$ORCH_PORT/api/health")
echo "Result: $ORCH_HEALTH"
if [[ "$ORCH_HEALTH" != *"ok"* ]]; then echo "FAILED"; exit 1; fi

echo "--> [Test 2] Health Check Unit 2..."
PORTAL_HEALTH=$(curl -s "http://localhost:$PORTAL_PORT/portal/health")
echo "Result: $PORTAL_HEALTH"
if [[ "$PORTAL_HEALTH" != *"ok"* ]]; then echo "FAILED"; exit 1; fi

echo "--> [Test 3] Register Repo in Unit 1..."
REPO_RESP=$(curl -s -X POST "http://localhost:$ORCH_PORT/api/repos" \
    -H "Content-Type: application/json" \
    -d '{"id":"e2e-repo","name":"E2E Test Repo","git_url":"https://github.com/example/e2e.git","branch":"main","local_path":"/tmp/e2e","schedule":"0 2 * * *"}')
echo "Result: $REPO_RESP"
if [[ "$REPO_RESP" != *"e2e-repo"* ]]; then echo "FAILED"; exit 1; fi

echo "--> [Test 4] Mock LDAP Login in Unit 2..."
LOGIN_RESP=$(curl -s -c "$COOKIE_FILE" -X POST "http://localhost:$PORTAL_PORT/portal/login" \
    -H "Content-Type: application/json" \
    -d '{"username":"e2e_user","password":"testpassword"}')
echo "Result: $LOGIN_RESP"
if [[ "$LOGIN_RESP" != *"token"* ]]; then echo "FAILED"; exit 1; fi

echo "--> [Test 5] Get Current User (/portal/me)..."
ME_RESP=$(curl -s -b "$COOKIE_FILE" "http://localhost:$PORTAL_PORT/portal/me")
echo "Result: $ME_RESP"
if [[ "$ME_RESP" != *"e2e_user"* ]]; then echo "FAILED"; exit 1; fi

echo "--> [Test 6] Get Repos via Portal Proxy with Cookie (injected wiki_url check)..."
PROXIED_REPOS=$(curl -s -b "$COOKIE_FILE" "http://localhost:$PORTAL_PORT/portal/repos")
echo "Result: $PROXIED_REPOS"
if [[ "$PROXIED_REPOS" != *"/wiki/e2e-repo/"* ]]; then echo "FAILED: wiki_url not injected properly"; exit 1; fi

echo "--> [Test 7] Unauthorized Access Rejection (No Cookie)..."
UNAUTH_RESP=$(curl -s -o /dev/null -w "%{http_code}" "http://localhost:$PORTAL_PORT/portal/repos")
echo "HTTP Status Code: $UNAUTH_RESP"
if [[ "$UNAUTH_RESP" != "401" ]]; then echo "FAILED: expected 401 unauthorized"; exit 1; fi

echo "--> [Test 8] Streaming QA Chat via Orchestrator API (/api/chat)..."
CHAT_RESP=$(curl -s -N -X POST "http://localhost:$ORCH_PORT/api/chat" \
    -H "Content-Type: application/json" \
    -d '{"repo_id":"e2e-repo","user_id":"e2e_user","question":"What is this project?"}')
echo "Result snippet: ${CHAT_RESP:0:150}..."
if [[ "$CHAT_RESP" != *"data:"* ]]; then echo "FAILED: expected SSE data stream"; exit 1; fi

echo "--> [Test 9] Get QA Session History..."
HIST_RESP=$(curl -s -b "$COOKIE_FILE" "http://localhost:$PORTAL_PORT/portal/sessions?repo_id=e2e-repo")
echo "Result: $HIST_RESP"
if [[ "$HIST_RESP" != *"What is this project?"* ]]; then echo "FAILED: QA session not recorded"; exit 1; fi

echo "--> [Test 10] Trigger Build in Unit 1..."
BUILD_TRIG=$(curl -s -X POST "http://localhost:$ORCH_PORT/api/build/trigger" \
    -H "Content-Type: application/json" \
    -d '{"repo_id":"e2e-repo"}')
echo "Result: $BUILD_TRIG"
if [[ "$BUILD_TRIG" != *"triggered"* ]]; then echo "FAILED"; exit 1; fi

echo "--> [Test 11] Check Build History..."
BUILD_HIST=$(curl -s "http://localhost:$ORCH_PORT/api/build/status?repo_id=e2e-repo")
echo "Result: $BUILD_HIST"
if [[ "$BUILD_HIST" != *"e2e-repo"* ]]; then echo "FAILED"; exit 1; fi

echo "--> [Test 14] Update Repo Config via Portal Proxy (PUT /portal/repos)..."
UPDATE_RESP=$(curl -s -b "$COOKIE_FILE" -X PUT "http://localhost:$PORTAL_PORT/portal/repos" \
    -H "Content-Type: application/json" \
    -d '{"id":"e2e-repo","name":"Updated E2E Repo Name","branch":"develop"}')
echo "Result: $UPDATE_RESP"
if [[ "$UPDATE_RESP" != *"Updated E2E Repo Name"* ]]; then echo "FAILED: expected updated repo name"; exit 1; fi

echo "--> [Test 15] Delete Repo via Portal Proxy (DELETE /portal/repos?id=e2e-repo)..."
DELETE_RESP=$(curl -s -b "$COOKIE_FILE" -X DELETE "http://localhost:$PORTAL_PORT/portal/repos?id=e2e-repo")
echo "Result: $DELETE_RESP"
if [[ "$DELETE_RESP" != *"deleted successfully"* ]]; then echo "FAILED: expected repo deletion success"; exit 1; fi

echo "--> [Test 12] Logout..."
LOGOUT_RESP=$(curl -s -b "$COOKIE_FILE" -c "$COOKIE_FILE" -X POST "http://localhost:$PORTAL_PORT/portal/logout")
echo "Result: $LOGOUT_RESP"
if [[ "$LOGOUT_RESP" != *"Logged out"* ]]; then echo "FAILED"; exit 1; fi

echo "--> [Test 13] Verify Rejection After Logout..."
POST_LOGOUT_CODE=$(curl -s -b "$COOKIE_FILE" -o /dev/null -w "%{http_code}" "http://localhost:$PORTAL_PORT/portal/me")
echo "HTTP Status Code: $POST_LOGOUT_CODE"
if [[ "$POST_LOGOUT_CODE" != "401" ]]; then echo "FAILED: expected 401 after logout"; exit 1; fi

echo "=========================================="
echo " ALL 15 E2E INTEGRATION TESTS PASSED!"
echo "=========================================="

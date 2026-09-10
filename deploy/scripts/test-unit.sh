#!/usr/bin/env bash
set -e

echo "=========================================="
echo " Running Unit Tests & Compilation Check"
echo "=========================================="

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_DIR="$(dirname "$SCRIPT_DIR")"

echo "--> [1/2] Testing & Building Unit 1 (orchestrator)..."
cd "$DEPLOY_DIR/unit1-orchestrator"
go test -v ./...
go build -v -o /tmp/orchestrator-test-bin .
rm -f /tmp/orchestrator-test-bin

echo "--> [2/2] Testing & Building Unit 2 (portal)..."
cd "$DEPLOY_DIR/unit2-portal"
go test -v ./...
go build -v -o /tmp/portal-test-bin .
rm -f /tmp/portal-test-bin

echo "=========================================="
echo " ALL UNIT TESTS & BUILDS PASSED SUCCESSFULLY!"
echo "=========================================="

#!/usr/bin/env bash
set -euo pipefail

# Fixed entry point for the Linux ARM64 deployment package.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
exec "$SCRIPT_DIR/scripts/package-deploy-linux-arm64.sh" "$@"

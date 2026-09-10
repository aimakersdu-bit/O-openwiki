#!/usr/bin/env bash
set -euo pipefail

echo "================================================="
echo " Building OpenWiki Standalone Binary (Air-Gapped)"
echo "================================================="

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

cd "${ROOT_DIR}"

OUTPUT_DIR="${ROOT_DIR}/dist-bin"
BINARY_PATH="${OUTPUT_DIR}/openwiki-bin"

mkdir -p "${OUTPUT_DIR}"

echo "[1/3] Compiling TypeScript source files..."
pnpm run typecheck

echo "[2/3] Compiling Standalone Executable Binary via Bun..."
bun build --compile src/cli/cli.tsx --outfile "${BINARY_PATH}"

chmod +x "${BINARY_PATH}"

echo "[3/3] Verifying Standalone Binary..."
"${BINARY_PATH}" --help > /dev/null

echo "================================================="
echo " Build Success!"
echo " Standalone Binary: ${BINARY_PATH}"
echo " File Size: $(du -h "${BINARY_PATH}" | cut -f1)"
echo "================================================="

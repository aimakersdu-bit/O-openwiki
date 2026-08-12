#!/usr/bin/env bash
set -euo pipefail

# Script to build a self-contained offline OpenWiki bundle (.tar.gz)
# Usage: ./scripts/build-offline-bundle.sh

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

cd "${ROOT_DIR}"

VERSION="$(node -e "console.log(require('./package.json').version)")"
BUNDLE_NAME="openwiki-offline-v${VERSION}"
OUTPUT_DIR="${ROOT_DIR}/dist-offline/${BUNDLE_NAME}"
TARBALL_PATH="${ROOT_DIR}/dist-offline/${BUNDLE_NAME}.tar.gz"

echo "=========================================="
echo " Building OpenWiki Offline Bundle v${VERSION}"
echo "=========================================="

# 1. Clean previous offline build output
rm -rf "${ROOT_DIR}/dist-offline"
mkdir -p "${OUTPUT_DIR}"

# 2. Build project
echo "[1/4] Building TypeScript source..."
pnpm run build

# 3. Copy dist, skills, manifests to staging folder
echo "[2/4] Copying build artifacts..."
cp -r dist "${OUTPUT_DIR}/"
cp -r skills "${OUTPUT_DIR}/"
cp package.json "${OUTPUT_DIR}/"
cp pnpm-lock.yaml "${OUTPUT_DIR}/"
cp README.md "${OUTPUT_DIR}/"
cp LICENSE "${OUTPUT_DIR}/"

# 4. Install production-only dependencies in staging folder
echo "[3/4] Packaging production dependencies..."
cd "${OUTPUT_DIR}"
pnpm install --prod --frozen-lockfile

# Add a launcher script in bundle
cat << 'EOF' > "${OUTPUT_DIR}/bin-openwiki"
#!/usr/bin/env bash
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
export OPENWIKI_TELEMETRY_DISABLED=1
export OPENWIKI_OFFLINE=1
exec node "${SCRIPT_DIR}/dist/cli/cli.js" "$@"
EOF
chmod +x "${OUTPUT_DIR}/bin-openwiki"

# 5. Archive to .tar.gz
echo "[4/4] Creating tarball archive..."
cd "${ROOT_DIR}/dist-offline"
tar -czf "${TARBALL_PATH}" "${BUNDLE_NAME}"

echo "=========================================="
echo " SUCCESS: Offline Bundle Created!"
echo " Tarball: ${TARBALL_PATH}"
echo " Instructions for Air-Gapped Machine:"
echo " 1. Extract tarball: tar -xzf ${BUNDLE_NAME}.tar.gz"
echo " 2. Run OpenWiki: ./bin-openwiki --init (or --help)"
echo "=========================================="

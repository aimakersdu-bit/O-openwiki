#!/usr/bin/env bash
set -euo pipefail

# Script to pre-download complete offline JS libraries for intranet deployment
VENDOR_DIR="${1:-assets/vendor}"
mkdir -p "$VENDOR_DIR"

echo "Downloading offline vendor libraries into $VENDOR_DIR..."

# 1. force-graph
echo "[1/4] Downloading force-graph.min.js..."
curl -sSL -o "$VENDOR_DIR/force-graph.min.js" "https://unpkg.com/force-graph/dist/force-graph.min.js"

# 2. marked
echo "[2/4] Downloading marked.min.js..."
curl -sSL -o "$VENDOR_DIR/marked.min.js" "https://unpkg.com/marked/marked.min.js"

# 3. DOMPurify
echo "[3/4] Downloading dompurify.min.js..."
curl -sSL -o "$VENDOR_DIR/dompurify.min.js" "https://unpkg.com/dompurify/dist/purify.min.js"

# 4. mermaid (pinned to 9.4.3 UMD release for universal browser compatibility)
echo "[4/4] Downloading mermaid.min.js..."
curl -sSL -o "$VENDOR_DIR/mermaid.min.js" "https://cdnjs.cloudflare.com/ajax/libs/mermaid/9.4.3/mermaid.min.js"

echo "All vendor assets successfully downloaded for intranet offline deployment!"
ls -lh "$VENDOR_DIR"

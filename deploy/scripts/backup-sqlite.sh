#!/usr/bin/env bash
# ==============================================================================
# OpenWiki SQLite Safe Online Backup Script
# Automatically creates online atomic snapshot of Orchestrator & Portal databases
# using SQLite VACUUM INTO / .backup without locking read/write workloads.
# ==============================================================================

set -euo pipefail

BACKUP_DIR="${BACKUP_DIR:-/data/backups/sqlite}"
ORCH_DB="${ORCH_DB:-/data/disk0/orchestrator.db}"
PORTAL_DB="${PORTAL_DB:-/data/disk0/portal.db}"
RETENTION_DAYS="${RETENTION_DAYS:-7}"
TIMESTAMP=$(date +"%Y%m%d_%H%M%S")

mkdir -p "${BACKUP_DIR}"

log() {
  echo "[$(date +'%Y-%m-%d %H:%M:%S')] $*"
}

backup_single_db() {
  local src_db="$1"
  local db_name="$2"
  local target_file="${BACKUP_DIR}/${db_name}_${TIMESTAMP}.db"

  if [ ! -f "${src_db}" ]; then
    log "WARN: Database file ${src_db} does not exist, skipping backup."
    return 0
  fi

  log "Starting online atomic backup: ${src_db} -> ${target_file}"

  if command -v sqlite3 >/dev/null 2>&1; then
    sqlite3 "${src_db}" "VACUUM INTO '${target_file}';"
  else
    # Fallback to copy if sqlite3 CLI binary is not installed
    cp "${src_db}" "${target_file}"
  fi

  gzip -f "${target_file}"
  log "SUCCESS: Backup created ${target_file}.gz ($(ls -lh "${target_file}.gz" | awk '{print $5}'))"
}

log "=== OpenWiki SQLite Backup Started ==="
backup_single_db "${ORCH_DB}" "orchestrator"
backup_single_db "${PORTAL_DB}" "portal"

# Clean up backups older than RETENTION_DAYS
log "Purging backups older than ${RETENTION_DAYS} days in ${BACKUP_DIR}..."
find "${BACKUP_DIR}" -name "*.db.gz" -mtime +"${RETENTION_DAYS}" -exec rm -f {} + || true

log "=== OpenWiki SQLite Backup Completed ==="

#!/usr/bin/env bash
# loop-audit-retention.sh — daily audit_log retention collector (DATA-2).
#
# Unlike loop-flag-cleanup.sh / loop-governance.sh, this loop's action IS a
# mutation of production storage (archiving old audit_log partitions) — it is
# not read-only. It never touches Postgres directly: it calls flag-api's own
# ADMIN-gated endpoint, which is what actually seals a signed checkpoint and
# archives, so this script stays a thin, unprivileged trigger.
#
# Usage: AUDIT_RETENTION_ADMIN_TOKEN=... ./scripts/loop-audit-retention.sh [--dry-run]
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DRY_RUN="${1:-}"
DATE="$(date +%Y-%m-%d)"
API_URL="${TOMBSTONE_API_URL:-http://localhost:8081}"

METRICS_FILE="$ROOT/domains/audit-retention/metrics/retention.jsonl"
SIGNALS_DIR="$ROOT/signals"
LOG_FILE="$ROOT/LOG.md"

log() { printf "[loop-audit-retention] %s\n" "$*"; }
die() { printf "[loop-audit-retention] ERROR: %s\n" "$*" >&2; exit 1; }

# --- 1. A missing token skips the run rather than silently no-op'ing as if
#        it succeeded — retention going quietly unrun is exactly the failure
#        mode a SOC2-relevant loop must not hide. ---
if [ -z "${AUDIT_RETENTION_ADMIN_TOKEN:-}" ]; then
  log "AUDIT_RETENTION_ADMIN_TOKEN not set — skipping run (retention did NOT execute)."
  exit 0
fi

# --- 2. Check flag-api is up ---
if ! curl -sf "$API_URL/health" >/dev/null 2>&1; then
  log "flag-api not reachable at $API_URL — skipping run."
  exit 0
fi

if [ "$DRY_RUN" = "--dry-run" ]; then
  log "Dry run — would POST $API_URL/api/v1/audit/retention/run, skipping the actual call."
  exit 0
fi

# --- 3. Trigger the run. The retention window itself is server-configured
#        (AUDIT_LOG_RETENTION_DAYS) — this script cannot widen it, only ask
#        flag-api to apply whatever it is configured with right now. ---
log "POST $API_URL/api/v1/audit/retention/run..."
HTTP_CODE=$(curl -s -o /tmp/audit-retention-run.json -w "%{http_code}" \
  -X POST "$API_URL/api/v1/audit/retention/run" \
  -H "Authorization: Bearer $AUDIT_RETENTION_ADMIN_TOKEN" \
  2>/dev/null || echo "000")

if [ "$HTTP_CODE" != "200" ]; then
  log "ALERT: retention run failed (HTTP $HTTP_CODE)"
  echo "{\"date\":\"$DATE\",\"partitions_archived\":[],\"checkpoints_written\":0,\"status\":\"error\",\"http_code\":\"$HTTP_CODE\"}" >> "$METRICS_FILE"

  mkdir -p "$SIGNALS_DIR"
  SIGNAL_FILE="$SIGNALS_DIR/audit-retention-failure-$DATE.md"
  if [ ! -f "$SIGNAL_FILE" ]; then
    cat > "$SIGNAL_FILE" << EOF
---
kind: signal
category: observation
frequency: daily
sources: [flag-api]
domain: [audit-retention]
status: open
---

Audit-log retention run failed on $DATE (HTTP $HTTP_CODE).

**Action required:** audit_log's live table is not being pruned — check flag-api
logs and AUDIT_HMAC_KEY/AUDIT_LOG_RETENTION_DAYS configuration. Unpruned growth is
an operational risk, not yet a correctness one — no chain integrity is affected
by a missed run.

## Timeline
$DATE | run failed, HTTP $HTTP_CODE
EOF
    log "Signal created: $SIGNAL_FILE"
  fi

  cat >> "$LOG_FILE" << EOF

## $DATE · audit-retention loop · #loop #ops
What: retention run FAILED (HTTP $HTTP_CODE). Signal created.
Refs: domains/audit-retention/metrics/retention.jsonl (updated), $SIGNAL_FILE.
EOF
  die "retention run failed with HTTP $HTTP_CODE — see $SIGNAL_FILE"
fi

PARTITIONS=$(python3 -c "import json; print(json.load(open('/tmp/audit-retention-run.json')).get('partitions_archived', []))" 2>/dev/null || echo "[]")
CHECKPOINTS=$(python3 -c "import json; print(json.load(open('/tmp/audit-retention-run.json')).get('checkpoints_written', 0))" 2>/dev/null || echo 0)
STRANDED=$(python3 -c "import json; print(json.load(open('/tmp/audit-retention-run.json')).get('stranded_in_default_partition', 0))" 2>/dev/null || echo 0)
STRANDED_SINCE=$(python3 -c "import json; print(json.load(open('/tmp/audit-retention-run.json')).get('stranded_since') or '')" 2>/dev/null || echo "")

log "Archived: $PARTITIONS (checkpoints written: $CHECKPOINTS)"

mkdir -p "$(dirname "$METRICS_FILE")"
python3 -c "
import json
resp = json.load(open('/tmp/audit-retention-run.json'))
print(json.dumps({
    'date': '$DATE',
    'partitions_archived': resp.get('partitions_archived', []),
    'checkpoints_written': resp.get('checkpoints_written', 0),
    'stranded_in_default_partition': resp.get('stranded_in_default_partition', 0),
    'status': 'ok',
}))
" >> "$METRICS_FILE" 2>/dev/null || echo "{\"date\":\"$DATE\",\"partitions_archived\":[],\"checkpoints_written\":0,\"stranded_in_default_partition\":0,\"status\":\"ok\"}" >> "$METRICS_FILE"

# stranded_in_default_partition > 0 means EnsurePartitions fell behind at
# some point (e.g. this loop skipped for longer than its 3-month lookahead)
# and that slice of rows can never be archived by month — flag-api's
# Retention.DefaultPartitionRowCount doc comment explains why Postgres
# never retroactively fixes this. Not a chain-integrity issue (those rows
# are still live and verified normally), but silent hot-table growth an
# operator needs to know about, not infer from a metrics file.
if [ "$STRANDED" != "0" ] && [ -n "$STRANDED" ]; then
  log "WARNING: $STRANDED row(s) stranded in audit_log_default since $STRANDED_SINCE — never archivable by month"
  mkdir -p "$SIGNALS_DIR"
  SIGNAL_FILE="$SIGNALS_DIR/audit-retention-stranded-rows-$DATE.md"
  if [ ! -f "$SIGNAL_FILE" ]; then
    cat > "$SIGNAL_FILE" << EOF
---
kind: signal
category: observation
frequency: daily
sources: [flag-api]
domain: [audit-retention]
status: open
---

$STRANDED row(s) are stranded in audit_log_default (oldest since $STRANDED_SINCE) on $DATE.

**Why:** EnsurePartitions fell behind at some point (this loop skipped, or the
service was down, for longer than its monthsAhead lookahead), so these rows
were routed to the catch-all default partition instead of a monthly one.
Postgres never retroactively re-routes committed rows into a partition
created later, so this slice can never be archived by month going forward.

**Impact:** hot-table growth only — these rows remain live, queryable, and
verified normally by GET /api/v1/audit/verify; chain integrity is unaffected.

## Timeline
$DATE | $STRANDED row(s) stranded, oldest since $STRANDED_SINCE
EOF
    log "Signal created: $SIGNAL_FILE"
  fi
fi

cat >> "$LOG_FILE" << EOF

## $DATE · audit-retention loop · #loop #ops
What: retention run ok. Archived: $PARTITIONS. Checkpoints written: $CHECKPOINTS.$([ "$STRANDED" != "0" ] && [ -n "$STRANDED" ] && echo " $STRANDED row(s) stranded in audit_log_default." || echo "")
Refs: domains/audit-retention/metrics/retention.jsonl (updated).
EOF

log "Done. LOG.md updated."

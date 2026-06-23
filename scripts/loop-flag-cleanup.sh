#!/usr/bin/env bash
# loop-flag-cleanup.sh — daily collector for stale flag detection.
# Reads stale flags from intelligence service, writes metrics/stale.jsonl,
# and creates a signal when stale count exceeds threshold.
#
# SAFE: read-only API calls only. Never archives flags directly.
# All archival goes through the ship-change workflow (opens a PR, never force-merges).
#
# Usage: ./scripts/loop-flag-cleanup.sh [--dry-run]
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DRY_RUN="${1:-}"
DATE="$(date +%Y-%m-%d)"
INTELLIGENCE_URL="${INTELLIGENCE_URL:-http://localhost:8083}"
PROJECT_ID="${PROJECT_ID:-00000000-0000-0000-0000-000000000001}"
STALE_THRESHOLD=5  # create a signal if stale_count > this

METRICS_FILE="$ROOT/domains/flag-cleanup/metrics/stale.jsonl"
SIGNALS_DIR="$ROOT/signals"
LOG_FILE="$ROOT/LOG.md"

log() { printf "[loop-flag-cleanup] %s\n" "$*"; }
die() { printf "[loop-flag-cleanup] ERROR: %s\n" "$*" >&2; exit 1; }

# --- 1. Check intelligence service is up ---
if ! curl -sf "$INTELLIGENCE_URL/health" >/dev/null 2>&1; then
  log "Intelligence service not reachable at $INTELLIGENCE_URL — skipping run."
  exit 0
fi

# --- 2. Fetch stale flags (collector step — deterministic, no LLM) ---
log "Fetching stale flags from $INTELLIGENCE_URL/api/v1/stale..."
STALE_JSON=$(curl -sf "$INTELLIGENCE_URL/api/v1/stale?project_id=$PROJECT_ID" 2>/dev/null || echo '{"stale_flags":[],"count":0}')
STALE_COUNT=$(echo "$STALE_JSON" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('count',len(d.get('stale_flags',[]))))" 2>/dev/null || echo 0)

log "Stale flags found: $STALE_COUNT"

# --- 3. Write metrics (collector writes data; agents write knowledge) ---
mkdir -p "$(dirname "$METRICS_FILE")"
echo "{\"date\":\"$DATE\",\"stale_count\":$STALE_COUNT,\"archived_count\":0,\"prs_opened\":0}" >> "$METRICS_FILE"
log "Metrics written to $METRICS_FILE"

if [ "$DRY_RUN" = "--dry-run" ]; then
  log "Dry run — skipping signal creation and LOG.md update."
  exit 0
fi

# --- 4. Create signal if stale count exceeds threshold ---
if [ "$STALE_COUNT" -gt "$STALE_THRESHOLD" ]; then
  SIGNAL_FILE="$SIGNALS_DIR/stale-flag-spike-$DATE.md"
  if [ ! -f "$SIGNAL_FILE" ]; then
    cat > "$SIGNAL_FILE" << EOF
---
kind: signal
category: observation
frequency: daily
sources: [intelligence-service]
domain: [flag-cleanup]
status: open
---

Stale flag count exceeded threshold on $DATE.

**Count:** $STALE_COUNT flags at 100% rollout for 30+ days (threshold: $STALE_THRESHOLD)

**Action required:** Run /new-loop or trigger ship-change to generate cleanup PRs.
Use MCP tool \`tombstone_list_stale_flags\` for the full list.

## Timeline
$DATE | $STALE_COUNT stale flags detected (threshold: $STALE_THRESHOLD)
EOF
    log "Signal created: $SIGNAL_FILE"
  fi
fi

# --- 5. Append to LOG.md ---
cat >> "$LOG_FILE" << EOF

## $DATE · flag-cleanup loop · #loop #ops
What: $STALE_COUNT stale flags detected.$([ "$STALE_COUNT" -gt "$STALE_THRESHOLD" ] && echo " Signal created (count > threshold $STALE_THRESHOLD)." || echo "")
Refs: domains/flag-cleanup/metrics/stale.jsonl (updated)$([ "$STALE_COUNT" -gt "$STALE_THRESHOLD" ] && echo ", signals/stale-flag-spike-$DATE.md (new)" || echo "").
EOF

log "Done. LOG.md updated."

#!/usr/bin/env bash
# loop-governance.sh — weekly governance health collector.
# Reads compliance evidence + stale flag metrics, writes to health.jsonl,
# creates signal when health_score drops below threshold.
#
# SAFE: read-only API calls only. Never modifies flags or governance data.
#
# Usage: ./scripts/loop-governance.sh [--dry-run]
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DRY_RUN="${1:-}"
DATE="$(date +%Y-%m-%d)"

FLAG_API_URL="${FLAG_API_URL:-http://localhost:8081}"
INTELLIGENCE_URL="${INTELLIGENCE_URL:-http://localhost:8083}"
HEALTH_THRESHOLD=0.80
STALE_THRESHOLD=50

METRICS_FILE="$ROOT/domains/governance/metrics/health.jsonl"
SIGNALS_DIR="$ROOT/signals"
LOG_FILE="$ROOT/LOG.md"

log() { printf "[loop-governance] %s\n" "$*"; }

# --- 1. Check services ---
if ! curl -sf "$FLAG_API_URL/health" >/dev/null 2>&1; then
    log "Flag-API not reachable at $FLAG_API_URL — skipping run."
    exit 0
fi

# --- 2. Fetch compliance evidence (collector step) ---
log "Fetching compliance evidence from $FLAG_API_URL/api/v1/compliance/evidence..."
EVIDENCE_JSON=$(curl -sf "$FLAG_API_URL/api/v1/compliance/evidence" 2>/dev/null \
    || echo '{"health_score":0,"rbac_coverage":0,"break_glass_uses":0,"active_flags":0}')

HEALTH_SCORE=$(echo "$EVIDENCE_JSON" | python3 -c "import sys,json; print(json.load(sys.stdin).get('health_score',0))" 2>/dev/null || echo 0)
RBAC_COVERAGE=$(echo "$EVIDENCE_JSON" | python3 -c "import sys,json; print(json.load(sys.stdin).get('rbac_coverage',0))" 2>/dev/null || echo 0)
BREAK_GLASS=$(echo "$EVIDENCE_JSON" | python3 -c "import sys,json; print(json.load(sys.stdin).get('break_glass_uses',0))" 2>/dev/null || echo 0)
ACTIVE_FLAGS=$(echo "$EVIDENCE_JSON" | python3 -c "import sys,json; print(json.load(sys.stdin).get('active_flags',0))" 2>/dev/null || echo 0)

# --- 3. Fetch stale count ---
STALE_COUNT=0
if curl -sf "$INTELLIGENCE_URL/health" >/dev/null 2>&1; then
    STALE_JSON=$(curl -sf "$INTELLIGENCE_URL/api/v1/stale" 2>/dev/null || echo '{"count":0}')
    STALE_COUNT=$(echo "$STALE_JSON" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('count',len(d.get('stale_flags',[]))))" 2>/dev/null || echo 0)
fi

log "Health score: $HEALTH_SCORE | Stale: $STALE_COUNT | Active flags: $ACTIVE_FLAGS"

# --- 4. Write metrics (collector writes data; agents write knowledge) ---
mkdir -p "$(dirname "$METRICS_FILE")"
echo "{\"date\":\"$DATE\",\"health_score\":$HEALTH_SCORE,\"stale_count\":$STALE_COUNT,\"rbac_coverage\":$RBAC_COVERAGE,\"break_glass_uses\":$BREAK_GLASS,\"active_flags\":$ACTIVE_FLAGS}" >> "$METRICS_FILE"
log "Metrics written to $METRICS_FILE"

if [ "$DRY_RUN" = "--dry-run" ]; then
    log "Dry run — skipping signal creation and LOG.md update."
    exit 0
fi

# --- 5. Create alert signal if thresholds breached ---
mkdir -p "$SIGNALS_DIR"
NEEDS_SIGNAL=$(python3 -c "print('yes' if $HEALTH_SCORE < $HEALTH_THRESHOLD or $STALE_COUNT > $STALE_THRESHOLD else 'no')" 2>/dev/null || echo no)

if [ "$NEEDS_SIGNAL" = "yes" ]; then
    log "ALERT: health_score=$HEALTH_SCORE stale_count=$STALE_COUNT — thresholds breached (health<0.80 or stale>50)"
    if [ -n "${SLACK_WEBHOOK_URL:-}" ]; then
        curl -s -X POST "$SLACK_WEBHOOK_URL" \
            -H "Content-Type: application/json" \
            -d "{\"text\":\"Tombstone governance alert: health_score=$HEALTH_SCORE (threshold: 0.80), stale_flags=$STALE_COUNT (threshold: 50). Review: ${TOMBSTONE_API_URL:-$FLAG_API_URL}/governance\"}" \
            >/dev/null 2>&1 \
            && log "Slack alert sent." \
            || log "Slack alert failed (non-fatal)."
    else
        log "SLACK_WEBHOOK_URL not set — skipping Slack notification."
    fi

    SIGNAL_FILE="$SIGNALS_DIR/governance-alert-$DATE.md"
    if [ ! -f "$SIGNAL_FILE" ]; then
        cat > "$SIGNAL_FILE" << EOF
---
kind: signal
category: observation
frequency: weekly
sources: [flag-api, intelligence-service]
domain: [governance]
status: open
---

Governance health alert on $DATE.

- **Health score:** $HEALTH_SCORE (threshold: $HEALTH_THRESHOLD)
- **Stale flags:** $STALE_COUNT (threshold: $STALE_THRESHOLD)
- **Active flags:** $ACTIVE_FLAGS
- **RBAC coverage:** $RBAC_COVERAGE
- **Break-glass uses (7d):** $BREAK_GLASS

**Action:** Review stale flags and compliance controls. Run \`tombstone flags list --env production\`.

## Timeline
$DATE | health=$HEALTH_SCORE stale=$STALE_COUNT alert created
EOF
        log "Alert signal created: $SIGNAL_FILE"
    fi
fi

# --- 6. Append to LOG.md ---
cat >> "$LOG_FILE" << EOF

## $DATE · governance loop · #loop #governance
What: health=$HEALTH_SCORE stale=$STALE_COUNT active_flags=$ACTIVE_FLAGS.$([ "$NEEDS_SIGNAL" = "yes" ] && echo " Alert signal created." || echo "")
Refs: domains/governance/metrics/health.jsonl (updated).
EOF

log "Done. LOG.md updated."

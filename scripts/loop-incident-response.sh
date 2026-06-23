#!/usr/bin/env bash
# loop-incident-response.sh — triggered on circuit breaker trip.
# Fetches SLO data + causal correlations, writes a structured incident doc.
#
# SAFE: read-only API calls. Never modifies flags or rolls back anything.
# Rollback already happened by the time this script runs.
#
# Usage: ./scripts/loop-incident-response.sh <flag_key> <environment>
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
FLAG_KEY="${1:?Usage: loop-incident-response.sh <flag_key> <environment>}"
ENVIRONMENT="${2:-production}"

# Sanitize FLAG_KEY to prevent shell expansion in heredocs
FLAG_KEY=$(printf '%s' "$FLAG_KEY" | tr -cd 'a-zA-Z0-9._-')
ENVIRONMENT=$(printf '%s' "$ENVIRONMENT" | tr -cd 'a-zA-Z0-9._-')
[ -z "$FLAG_KEY" ] && { printf "[loop-incident-response] ERROR: FLAG_KEY became empty after sanitization — check your input\n" >&2; exit 1; }

DATE="$(date +%Y-%m-%d)"
TS="$(date +%Y-%m-%dT%H:%M:%SZ)"

EVALUATOR_URL="${EVALUATOR_URL:-http://localhost:8082}"
INTELLIGENCE_URL="${INTELLIGENCE_URL:-http://localhost:8083}"
FLAG_API_URL="${FLAG_API_URL:-http://localhost:8081}"

DOCS_DIR="$ROOT/docs"
SIGNALS_DIR="$ROOT/signals"
METRICS_FILE="$ROOT/domains/incident-response/metrics/trips.jsonl"
LOG_FILE="$ROOT/LOG.md"

log() { printf "[loop-incident-response] %s\n" "$*"; }

# --- 1. Fetch SLO data (collector step) ---
log "Fetching SLO data for $FLAG_KEY in $ENVIRONMENT..."
SLO_JSON=$(curl -sf "$EVALUATOR_URL/api/v1/flags/$FLAG_KEY/slo?window=7d" 2>/dev/null \
  || echo '{"error_rate":0,"circuit_trips":0,"slo_budget_remaining":1}')
ERROR_RATE=$(echo "$SLO_JSON" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('error_rate',0))" 2>/dev/null || echo 0)
CIRCUIT_TRIPS=$(echo "$SLO_JSON" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('circuit_trips',0))" 2>/dev/null || echo 0)

# --- 2. Fetch causal correlations (collector step) ---
log "Running incident correlation for $FLAG_KEY..."
INCIDENT_START=$(date -u -v-30M +%s 2>/dev/null || date -u --date='30 minutes ago' +%s 2>/dev/null || echo 0)
CORRELATE_JSON=$(curl -sf -X POST \
  "$INTELLIGENCE_URL/api/v1/correlate?incident_id=loop-$FLAG_KEY-$DATE&affected_service=$FLAG_KEY&incident_start_unix=$INCIDENT_START" \
  2>/dev/null || echo '{"candidates":[]}')
CORRELATED=$(echo "$CORRELATE_JSON" | python3 -c "
import sys,json
d=json.load(sys.stdin)
flags=[c.get('flag_key','') for c in d.get('candidates',[])[:3]]
print(', '.join(flags) if flags else 'none')
" 2>/dev/null || echo "none")

# --- 3. Write metrics (deterministic collector, no LLM) ---
mkdir -p "$(dirname "$METRICS_FILE")"
echo "{\"date\":\"$DATE\",\"flag_key\":\"$FLAG_KEY\",\"environment\":\"$ENVIRONMENT\",\"error_rate\":$ERROR_RATE,\"circuit_trips\":$CIRCUIT_TRIPS,\"correlated_flags\":\"$CORRELATED\"}" >> "$METRICS_FILE"

# --- 4. Write incident post-mortem doc ---
DOC_SLUG="incident-$DATE-$FLAG_KEY"
DOC_FILE="$DOCS_DIR/$DOC_SLUG.md"

cat > "$DOC_FILE" << EOF
---
kind: doc
domain: [incident-response]
status: draft
type: learning
links: []
---

# Incident: $FLAG_KEY circuit trip — $DATE

Flag **$FLAG_KEY** tripped its circuit breaker in **$ENVIRONMENT** on $DATE.
The evaluator automatically rolled back the flag. This document captures what happened.

## What happened
- **Flag key:** $FLAG_KEY
- **Environment:** $ENVIRONMENT
- **Triggered at:** $TS
- **Error rate at trip:** $ERROR_RATE
- **Circuit trips (7d):** $CIRCUIT_TRIPS
- **Correlated flags (30m window):** $CORRELATED

## SLO data
$(echo "$SLO_JSON" | python3 -m json.tool 2>/dev/null || echo "$SLO_JSON")

## Causal correlation (top candidates)
$(echo "$CORRELATE_JSON" | python3 -m json.tool 2>/dev/null || echo "$CORRELATE_JSON")

## Action items
- [ ] Review the correlated flags listed above for recent changes
- [ ] Check audit log: \`GET /api/v1/audit?flag_key=$FLAG_KEY&limit=20\`
- [ ] Determine if rollout_pct should be reduced or flag retired
- [ ] Update this doc with root cause once identified

## Timeline
$DATE | Circuit breaker trip — auto-rollback by evaluator. Error rate: $ERROR_RATE. Correlated: $CORRELATED.
EOF

log "Incident doc written: $DOC_FILE"

# --- 5. Create signal for repeat offenders ---
if [ "$CIRCUIT_TRIPS" -gt 2 ]; then
  SIGNAL_FILE="$SIGNALS_DIR/circuit-trip-$FLAG_KEY-$DATE.md"
  cat > "$SIGNAL_FILE" << EOF
---
kind: signal
category: observation
frequency: event-driven
sources: [evaluator, circuit-breaker]
domain: [incident-response]
status: open
---

Flag **$FLAG_KEY** has tripped its circuit breaker **$CIRCUIT_TRIPS** times in the past 7 days.
This is a repeat-offender pattern — likely needs investigation or retirement.

## Timeline
$DATE | Trip #$CIRCUIT_TRIPS this week. Correlated flags: $CORRELATED. See [[${DOC_SLUG}]].
EOF
  log "Repeat-offender signal created: $SIGNAL_FILE"
fi

# --- 6. Append to LOG.md ---
cat >> "$LOG_FILE" << EOF

## $DATE · incident-response: $FLAG_KEY · #loop #incident
What: Circuit trip documented. Error rate: $ERROR_RATE. Correlated: $CORRELATED.
Refs: docs/$DOC_SLUG.md (new), domains/incident-response/metrics/trips.jsonl (updated).
EOF

log "Done."

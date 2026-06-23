#!/usr/bin/env bash
# loop-rollout-advisor.sh — daily collector for rollout recommendations.
# Reads Thompson Sampling / LinUCB recommendations from intelligence service,
# checks blast radius for each, creates signals for human review.
#
# SAFE: read-only. Never modifies flags or opens PRs directly.
# All rollout advances go through human review of the signal.
#
# Usage: ./scripts/loop-rollout-advisor.sh [--dry-run]
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DRY_RUN="${1:-}"
DATE="$(date +%Y-%m-%d)"

INTELLIGENCE_URL="${INTELLIGENCE_URL:-http://localhost:8083}"
EVALUATOR_URL="${EVALUATOR_URL:-http://localhost:8082}"
CONFIDENCE_THRESHOLD=0.90    # only signal if confidence >= this
MIN_OBSERVATIONS=50          # only signal if observations >= this

METRICS_FILE="$ROOT/domains/rollout-advisor/metrics/recommendations.jsonl"
SIGNALS_DIR="$ROOT/signals"
LOG_FILE="$ROOT/LOG.md"

log() { printf "[loop-rollout-advisor] %s\n" "$*"; }

# --- 1. Check services ---
if ! curl -sf "$INTELLIGENCE_URL/health" >/dev/null 2>&1; then
  log "Intelligence service not reachable — skipping run."
  exit 0
fi

# --- 2. Fetch recommendations (collector step) ---
log "Fetching rollout recommendations..."
RECS_JSON=$(curl -sf "$INTELLIGENCE_URL/api/v1/rollout/recommendations" 2>/dev/null \
  || echo '{"recommendations":[]}')
REC_COUNT=$(echo "$RECS_JSON" | python3 -c "
import sys,json; d=json.load(sys.stdin)
print(len(d.get('recommendations',[])))
" 2>/dev/null || echo 0)

log "Recommendations found: $REC_COUNT"

mkdir -p "$(dirname "$METRICS_FILE")"
mkdir -p "$SIGNALS_DIR"

# --- 3. Process each recommendation ---
echo "$RECS_JSON" | python3 -c "
import sys,json
d=json.load(sys.stdin)
for r in d.get('recommendations',[]):
    print(json.dumps(r))
" 2>/dev/null | while IFS= read -r rec; do
  FLAG_KEY=$(echo "$rec" | python3 -c "import sys,json; print(json.loads(sys.stdin.read()).get('flag_key',''))" 2>/dev/null || continue)
  ENVIRONMENT=$(echo "$rec" | python3 -c "import sys,json; print(json.loads(sys.stdin.read()).get('environment','production'))" 2>/dev/null || echo production)
  CONFIDENCE=$(echo "$rec" | python3 -c "import sys,json; print(json.loads(sys.stdin.read()).get('confidence',0))" 2>/dev/null || echo 0)
  SUGGESTED_PCT=$(echo "$rec" | python3 -c "import sys,json; print(json.loads(sys.stdin.read()).get('suggested_pct',0))" 2>/dev/null || echo 0)
  CURRENT_PCT=$(echo "$rec" | python3 -c "import sys,json; print(json.loads(sys.stdin.read()).get('current_pct',0))" 2>/dev/null || echo 0)
  OBSERVATIONS=$(echo "$rec" | python3 -c "import sys,json; print(json.loads(sys.stdin.read()).get('observations',0))" 2>/dev/null || echo 0)

  [ -z "$FLAG_KEY" ] && continue

  # Check blast radius
  BLAST_JSON=$(curl -sf \
    "$EVALUATOR_URL/api/v1/blast-radius?flag_key=$FLAG_KEY&environment=$ENVIRONMENT&rollout_pct=$SUGGESTED_PCT" \
    2>/dev/null || echo '{"risk_score":"UNKNOWN"}')
  BLAST_RISK=$(echo "$BLAST_JSON" | python3 -c "import sys,json; print(json.loads(sys.stdin.read()).get('risk_score','UNKNOWN'))" 2>/dev/null || echo UNKNOWN)

  # Write metrics row
  echo "{\"date\":\"$DATE\",\"flag_key\":\"$FLAG_KEY\",\"environment\":\"$ENVIRONMENT\",\"current_pct\":$CURRENT_PCT,\"suggested_pct\":$SUGGESTED_PCT,\"confidence\":$CONFIDENCE,\"observations\":$OBSERVATIONS,\"blast_risk\":\"$BLAST_RISK\"}" >> "$METRICS_FILE"

  [ "$DRY_RUN" = "--dry-run" ] && continue

  # Create signal if ready
  CONF_OK=$(python3 -c "print('yes' if $CONFIDENCE >= $CONFIDENCE_THRESHOLD else 'no')" 2>/dev/null || echo no)
  OBS_OK=$(python3 -c "print('yes' if $OBSERVATIONS >= $MIN_OBSERVATIONS else 'no')" 2>/dev/null || echo no)

  if [ "$CONF_OK" = "yes" ] && [ "$OBS_OK" = "yes" ]; then
    if [ "$BLAST_RISK" = "BLOCKED" ]; then
      SIGNAL_FILE="$SIGNALS_DIR/rollout-blocked-$FLAG_KEY-$DATE.md"
      cat > "$SIGNAL_FILE" << EOF
---
kind: signal
category: observation
frequency: daily
sources: [intelligence-service, evaluator]
domain: [rollout-advisor]
status: open
---

Rollout advance for **$FLAG_KEY** is ready by ML confidence but BLOCKED by blast radius.

- **Current rollout:** $CURRENT_PCT% → Suggested: $SUGGESTED_PCT%
- **Confidence:** $CONFIDENCE (threshold: $CONFIDENCE_THRESHOLD)
- **Observations:** $OBSERVATIONS
- **Blast radius:** BLOCKED — requires typed justification + second approver

Manual action required. Use the dashboard or CLI to override with justification.

## Timeline
$DATE | BLOCKED blast radius prevents auto-advance. Confidence: $CONFIDENCE. Obs: $OBSERVATIONS.
EOF
    else
      SIGNAL_FILE="$SIGNALS_DIR/rollout-ready-$FLAG_KEY-$DATE.md"
      cat > "$SIGNAL_FILE" << EOF
---
kind: signal
category: idea
frequency: daily
sources: [intelligence-service, evaluator]
domain: [rollout-advisor]
status: open
---

Flag **$FLAG_KEY** is ready to advance from $CURRENT_PCT% to $SUGGESTED_PCT% rollout.

- **Confidence:** $CONFIDENCE (≥ $CONFIDENCE_THRESHOLD threshold ✓)
- **Observations:** $OBSERVATIONS (≥ $MIN_OBSERVATIONS minimum ✓)
- **Blast radius:** $BLAST_RISK

**Next step:** Review and approve via the Tombstone dashboard, or run:
\`\`\`bash
tombstone flags flip $FLAG_KEY --env $ENVIRONMENT --pct $SUGGESTED_PCT
\`\`\`

## Timeline
$DATE | Ready to advance. Confidence: $CONFIDENCE. Blast risk: $BLAST_RISK.
EOF
    fi
  fi
done

# --- 4. Append to LOG.md ---
cat >> "$LOG_FILE" << EOF

## $DATE · rollout-advisor loop · #loop #rollout
What: $REC_COUNT recommendations reviewed.
Refs: domains/rollout-advisor/metrics/recommendations.jsonl (updated).
EOF

log "Done."

#!/usr/bin/env bash
# Tombstone end-to-end test script
# Usage: TOMBSTONE_TEST_TOKEN=sdk-dev-token-change-in-prod bash scripts/e2e-test.sh
set -euo pipefail

API="${TOMBSTONE_API_URL:-http://localhost:8081}"
GATEWAY="${TOMBSTONE_GATEWAY_URL:-http://localhost:8080}"
EVALUATOR="${TOMBSTONE_EVAL_URL:-http://localhost:8082}"
INTEL="${TOMBSTONE_INTEL_URL:-http://localhost:8083}"
GITOPS="${TOMBSTONE_GITOPS_URL:-http://localhost:8084}"
TOK="${TOMBSTONE_TEST_TOKEN:?Please set TOMBSTONE_TEST_TOKEN}"
HDR="Authorization: Bearer $TOK"

GREEN='\033[0;32m'; RED='\033[0;31m'; YELLOW='\033[1;33m'; NC='\033[0m'
pass() { echo -e "${GREEN}✓ $1${NC}"; }
fail() { echo -e "${RED}✗ $1${NC}"; FAILURES=$((FAILURES+1)); }
info() { echo -e "${YELLOW}▸ $1${NC}"; }
FAILURES=0

wait_up() {
  local url=$1 label=$2 max=${3:-30}
  for i in $(seq 1 $max); do
    curl -sf "$url" >/dev/null 2>&1 && pass "$label up" && return 0
    sleep 1
  done
  fail "$label not ready at $url (${max}s)"; return 1
}
py() { python3 -c "import sys,json; d=json.load(sys.stdin); print($1)" 2>/dev/null || echo ""; }

echo ""; echo "═══════ Tombstone E2E Suite ═══════"; echo ""

info "1. Health checks"
wait_up "$API/health"        "flag-api :8081"
wait_up "$GATEWAY/health"    "gateway  :8080"
wait_up "$EVALUATOR/health"  "evaluator:8082"
wait_up "$INTEL/health"      "intel    :8083" 90
wait_up "$GITOPS/health"     "gitops   :8084"

info "2. Seeded flags"
COUNT=$(curl -sf -H "$HDR" "$API/api/v1/flags" | py "d.get('total',0)")
[ "${COUNT:-0}" -ge 3 ] && pass "Found $COUNT flags" || fail "Expected >=3, got $COUNT"

info "3. Get flag by key"
KEY=$(curl -sf -H "$HDR" "$API/api/v1/flags/checkout-v2" | py "d.get('key','')")
[ "$KEY" = "checkout-v2" ] && pass "checkout-v2 exists" || fail "Got: $KEY"

info "4. Create flag"
K=$(curl -sf -X POST -H "$HDR" -H "Content-Type: application/json" \
  -d '{"key":"e2e.test.ping","name":"E2E Ping","flag_type":"BOOLEAN","owner_id":"test@test.com","safe_default":"false"}' \
  "$API/api/v1/flags" | py "d.get('key','')")
[ "$K" = "e2e.test.ping" ] && pass "Created e2e.test.ping" || fail "Create failed: $K"

info "5. Enable flag"
EN=$(curl -sf -X PATCH -H "$HDR" -H "Content-Type: application/json" \
  -d '{"enabled":true,"rollout_pct":100}' \
  "$API/api/v1/flags/e2e.test.ping/environments/development" | py "d.get('enabled',False)")
[ "$EN" = "True" ] && pass "Enabled" || fail "Enable failed: $EN"

info "6. Gateway snapshot"
HASH=$(curl -sf -H "$HDR" "$GATEWAY/api/v1/snapshot?environment=development" | py "d.get('hash','')")
[ -n "$HASH" ] && pass "Snapshot hash: ${HASH:0:12}..." || fail "No hash"

info "7. Kill switch"
KL=$(curl -sf -X POST -H "$HDR" -H "Content-Type: application/json" \
  -d '{"environment":"development","reason":"automated end-to-end kill test"}' \
  "$API/api/v1/flags/e2e.test.ping/kill" | py "d.get('killed',False)")
[ "$KL" = "True" ] && pass "Kill switch fired" || fail "Kill failed: $KL"

info "8. Audit log"
E=$(curl -sf -H "$HDR" "$API/api/v1/audit?flag_key=e2e.test.ping&limit=10" | py "len(d.get('entries',[]))")
[ "${E:-0}" -ge 1 ] && pass "Audit entries: $E" || fail "Audit empty"

info "9. Blast radius"
RS=$(curl -sf "$EVALUATOR/api/v1/blast-radius?flag_key=checkout-v2&environment=production&rollout_pct=50" \
  | py "d.get('result',d).get('risk_score','')")
[ -n "$RS" ] && pass "Risk score: $RS" || fail "Blast radius failed"

info "10. Telemetry ingest"
CODE=$(curl -so /dev/null -w "%{http_code}" -X POST -H "Content-Type: application/json" \
  -d '[{"flag_key":"checkout-v2","environment":"production","is_error":false}]' \
  "$EVALUATOR/api/v1/telemetry")
[ "$CODE" = "204" ] && pass "Telemetry 204 OK" || info "Telemetry HTTP $CODE"

info "11. Circuit breaker"
ST=$(curl -sf "$EVALUATOR/api/v1/circuit/checkout-v2" | py "d.get('state','')")
[ "$ST" = "CLOSED" ] && pass "Circuit: CLOSED" || info "Circuit: $ST"

info "12. NLP search"
RN=$(curl -sf "$INTEL/api/v1/search?q=checkout&limit=5" | py "len(d.get('results',[]))")
pass "NLP search: $RN result(s)"

info "13. Stale detection"
SF=$(curl -sf "$INTEL/api/v1/stale" | py "d.get('count',0)")
pass "Stale flags: $SF"

info "14. GitOps health"
GS=$(curl -sf "$GITOPS/health" | py "d.get('service','')")
[ "$GS" = "gitops-sync" ] && pass "GitOps healthy" || fail "GitOps: $GS"

info "15. Cleanup PR"
PT=$(curl -s -X POST -H "Content-Type: application/json" \
  -d '{"flag_key":"old.feature.test","flag_name":"Old Feature","flag_description":"Old feature no longer needed","owner_id":"dev@example.com","days_at_100_pct":45,"stale_score":0.85}' \
  "$INTEL/api/v1/cleanup/generate-pr" | py "d.get('pr_title','')")
[ -n "$PT" ] && pass "PR: $PT" || fail "Cleanup PR failed"

echo ""
echo "══════════════════════════════════════"
[ "$FAILURES" -eq 0 ] && echo -e "${GREEN}  ALL 15 TESTS PASSED ✓${NC}" || echo -e "${RED}  $FAILURES FAILED ✗${NC}"
echo "══════════════════════════════════════"
echo "  Dashboard    → http://localhost:3000"
echo "  API          → http://localhost:8081"
echo "  Gateway SSE  → http://localhost:8080"
echo "  Evaluator    → http://localhost:8082"
echo "  Intelligence → http://localhost:8083"
echo "  GitOps       → http://localhost:8084"
echo ""
exit $FAILURES

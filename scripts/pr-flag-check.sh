#!/usr/bin/env bash
# Tombstone PR flag check — posts blast radius annotations on changed flag keys
# Usage: bash scripts/pr-flag-check.sh
#
# Required environment variables (set as GitHub Actions secrets/vars):
#   TOMBSTONE_API_URL        Evaluator base URL (default: http://localhost:8082)
#   TOMBSTONE_TOKEN          Bearer credential for Tombstone API
#   TOMBSTONE_ENVIRONMENT    Target environment for blast radius (default: production)
#   MIN_RISK                 Minimum risk level to annotate: LOW|MEDIUM|HIGH|BLOCKED (default: MEDIUM)
#   GITHUB_TOKEN             Injected automatically by GitHub Actions
#   GITHUB_REPOSITORY        Injected automatically by GitHub Actions (owner/repo)
#   PR_NUMBER                Set from github.event.pull_request.number in the workflow
set -euo pipefail

# ── Config ────────────────────────────────────────────────────────────────────
# Blast radius endpoint lives on the evaluator service (port 8082).
TOMBSTONE_API_URL="${TOMBSTONE_API_URL:-http://localhost:8082}"
ENVIRONMENT="${TOMBSTONE_ENVIRONMENT:-production}"
MIN_RISK="${MIN_RISK:-MEDIUM}"

# Auth credential — read exclusively from the environment, never hardcoded
TOMBSTONE_AUTH_HEADER=""
if [ -n "${TOMBSTONE_TOKEN:-}" ]; then
    TOMBSTONE_AUTH_HEADER="Authorization: Bearer ${TOMBSTONE_TOKEN}"
fi

# ── Helpers ───────────────────────────────────────────────────────────────────
GREEN='\033[0;32m'; YELLOW='\033[1;33m'; RED='\033[0;31m'; NC='\033[0m'
info()  { echo -e "${YELLOW}▸ $*${NC}"; }
ok()    { echo -e "${GREEN}✓ $*${NC}"; }
warn()  { echo -e "${RED}⚠ $*${NC}"; }

# Risk level ordering: LOW(0) < MEDIUM(1) < HIGH(2) < BLOCKED(3)
risk_gte() {
    local level="$1" threshold="$2"
    declare -A order=([LOW]=0 [MEDIUM]=1 [HIGH]=2 [BLOCKED]=3 [UNKNOWN]=-1)
    local lv="${order[$level]:--1}"
    local tv="${order[$threshold]:-1}"
    [[ "$lv" -ge "$tv" ]]
}

# ── 1. Extract flag keys from diff ────────────────────────────────────────────
info "Extracting flag keys from PR diff..."

# Try HEAD~1..HEAD first (works when full history is fetched); fall back to
# comparing against origin/develop for shallow clones (fetch-depth: 2).
DIFF=""
if git diff -U0 HEAD~1 HEAD -- '*.ts' '*.tsx' '*.js' '*.py' '*.go' '*.rb' '*.java' \
        >/dev/null 2>&1; then
    DIFF=$(git diff -U0 HEAD~1 HEAD -- '*.ts' '*.tsx' '*.js' '*.py' '*.go' '*.rb' '*.java')
else
    BASE="${BASE_BRANCH:-develop}"
    DIFF=$(git diff -U0 "origin/${BASE}" HEAD -- '*.ts' '*.tsx' '*.js' '*.py' '*.go' '*.rb' '*.java')
fi

# Extract only added/changed lines (lines starting with +, excluding +++ file headers).
# Match common feature-flag SDK call patterns across all supported languages:
#   TypeScript/JS : isEnabled("flag-key"), evaluate("flag-key"), getFlag("flag-key"),
#                   flagEnabled("flag-key"), checkFlag("flag-key")
#   Python        : is_enabled("flag-key"), get_flag("flag-key"), check_flag("flag-key")
#   Go            : IsEnabled("flag-key"), GetFlag("flag-key")
#   Ruby/Java     : same camelCase / snake_case variants
FLAG_KEYS=$(echo "$DIFF" \
    | grep '^+' \
    | grep -v '^+++' \
    | grep -oP '(?:isEnabled|is_enabled|evaluate|getFlag|get_flag|flagEnabled|checkFlag|check_flag|IsEnabled|GetFlag)\s*\(\s*["'"'"']([a-z0-9._-]+)["'"'"']' \
    | grep -oP '["'"'"'][a-z0-9._-]+["'"'"']' \
    | tr -d "\"'" \
    | sort -u \
    || true)

if [ -z "$FLAG_KEYS" ]; then
    ok "No flag keys detected in PR diff — nothing to annotate."
    exit 0
fi

FLAG_COUNT=$(echo "$FLAG_KEYS" | wc -l | tr -d ' ')
info "Detected ${FLAG_COUNT} flag key(s): $(echo "$FLAG_KEYS" | tr '\n' ' ')"

# ── 2. Query blast radius for each flag key ───────────────────────────────────
info "Querying blast radius for environment: ${ENVIRONMENT}..."

declare -a HIGH_RISK_FLAGS=()
declare -a ALL_RESULTS=()

while IFS= read -r FLAG_KEY; do
    [ -z "$FLAG_KEY" ] && continue

    # Build curl arguments — conditionally include the auth header
    CURL_EXTRA_ARGS=()
    if [ -n "$TOMBSTONE_AUTH_HEADER" ]; then
        CURL_EXTRA_ARGS+=(-H "$TOMBSTONE_AUTH_HEADER")
    fi

    RESPONSE=$(curl -sf \
        "${CURL_EXTRA_ARGS[@]+"${CURL_EXTRA_ARGS[@]}"}" \
        "${TOMBSTONE_API_URL}/api/v1/blast-radius?flag_key=${FLAG_KEY}&environment=${ENVIRONMENT}&rollout_pct=100" \
        2>/dev/null || echo '{}')

    RISK=$(echo "$RESPONSE" \
        | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('risk_score', d.get('result',{}).get('risk_score','UNKNOWN')))" \
        2>/dev/null || echo "UNKNOWN")

    AFFECTED=$(echo "$RESPONSE" \
        | python3 -c "import sys,json; d=json.load(sys.stdin); r=d.get('result',d); print(r.get('affected_users',r.get('total_evaluations','?')))" \
        2>/dev/null || echo "?")

    ALL_RESULTS+=("${FLAG_KEY}|${RISK}|${AFFECTED}")

    if risk_gte "$RISK" "$MIN_RISK"; then
        HIGH_RISK_FLAGS+=("${FLAG_KEY}:${RISK}:${AFFECTED}")
        warn "Flag '${FLAG_KEY}' — risk=${RISK}, affected=${AFFECTED}"
        # Emit GitHub Actions workflow command (appears inline in the PR Checks tab)
        echo "::warning title=Tombstone Blast Radius::Flag '${FLAG_KEY}' has ${RISK} blast radius in ${ENVIRONMENT} (affected: ${AFFECTED})"
    else
        ok "Flag '${FLAG_KEY}' — risk=${RISK}, affected=${AFFECTED}"
    fi
done <<< "$FLAG_KEYS"

# ── 3. Post summary comment via GitHub API ────────────────────────────────────
# Only runs inside GitHub Actions (all three GH_ vars must be set).
GH_REPO="${GITHUB_REPOSITORY:-}"
GH_PR="${PR_NUMBER:-}"

if [ -n "${GITHUB_TOKEN:-}" ] && [ -n "$GH_REPO" ] && [ -n "$GH_PR" ]; then
    info "Posting PR comment to ${GH_REPO}#${GH_PR}..."

    # Build the Markdown comment body
    BODY="## Tombstone Flag Blast Radius Check\n\n"
    BODY+="Environment: \`${ENVIRONMENT}\` | Threshold: \`${MIN_RISK}+\`\n\n"
    BODY+="| Flag Key | Risk | Affected Users |\n"
    BODY+="|----------|------|----------------|\n"

    for ENTRY in "${ALL_RESULTS[@]}"; do
        IFS='|' read -r K R A <<< "$ENTRY"
        EMOJI="✅"
        case "$R" in
            HIGH)    EMOJI="🔴" ;;
            BLOCKED) EMOJI="🚫" ;;
            MEDIUM)  EMOJI="🟡" ;;
            LOW)     EMOJI="🟢" ;;
            UNKNOWN) EMOJI="⚪" ;;
        esac
        BODY+="| \`${K}\` | ${EMOJI} ${R} | ${A} |\n"
    done

    if [ "${#HIGH_RISK_FLAGS[@]}" -gt 0 ]; then
        BODY+="\n> ⚠️ **${#HIGH_RISK_FLAGS[@]} flag(s) meet or exceed the \`${MIN_RISK}\` threshold.** "
        BODY+="Review blast radius before merging.\n"
    else
        BODY+="\n> ✅ All flags are below the \`${MIN_RISK}\` threshold.\n"
    fi

    FIRST_FLAG=$(echo "$FLAG_KEYS" | head -1)
    BODY+="\n_Run \`tombstone blast-radius ${FIRST_FLAG}\` for details. "
    BODY+="Powered by [Tombstone](https://github.com/${GH_REPO})._"

    # Properly escape the body as a JSON string (handles quotes, newlines, etc.)
    ESCAPED_BODY=$(echo -e "$BODY" | python3 -c "import sys,json; print(json.dumps(sys.stdin.read()))")

    # POST — failures are non-fatal (annotation output is the primary signal)
    curl -sf -X POST \
        -H "Authorization: Bearer ${GITHUB_TOKEN}" \
        -H "Content-Type: application/json" \
        -H "Accept: application/vnd.github+json" \
        "https://api.github.com/repos/${GH_REPO}/issues/${GH_PR}/comments" \
        -d "{\"body\": ${ESCAPED_BODY}}" \
        >/dev/null \
        && ok "PR comment posted." \
        || warn "Could not post PR comment (non-fatal)."
fi

# ── 4. Exit code ──────────────────────────────────────────────────────────────
# BLOCKED is a hard gate — CI fails.
# MEDIUM/HIGH produce warnings + annotations but do not break CI by default.
# Teams can enforce stricter gating via branch protection rules on this job's outcome.
for ENTRY in "${ALL_RESULTS[@]}"; do
    IFS='|' read -r K R _ <<< "$ENTRY"
    if [ "$R" = "BLOCKED" ]; then
        warn "BLOCKED flag detected: ${K} — failing CI."
        exit 1
    fi
done

ok "PR flag blast radius check complete."
exit 0

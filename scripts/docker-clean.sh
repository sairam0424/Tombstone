#!/usr/bin/env bash
# ╔══════════════════════════════════════════════════════════════════════╗
# ║  docker-clean.sh — Smart Docker space reclaimer                     ║
# ║  Usage: docker-clean.sh [--auto | --dry-run | --nuclear | --status] ║
# ║  Author: Tombstone / sairamugge                                      ║
# ╚══════════════════════════════════════════════════════════════════════╝
set -euo pipefail

# ── Configuration (override via env vars) ──────────────────────────────
WARN_THRESHOLD="${DOCKER_WARN_PCT:-70}"     # warn at 70% Docker VM usage
AUTO_THRESHOLD="${DOCKER_AUTO_PCT:-85}"     # auto-clean at 85%
NUCLEAR_THRESHOLD="${DOCKER_NUKE_PCT:-95}"  # nuclear clean at 95%
KEEP_LAST_IMAGES="${DOCKER_KEEP_IMAGES:-5}" # keep N most-recently-used images

# ── Colors ──────────────────────────────────────────────────────────────
RED='\033[0;31m'; YELLOW='\033[1;33m'; GREEN='\033[0;32m'
CYAN='\033[0;36m'; BOLD='\033[1m'; NC='\033[0m'

# ── Helpers ─────────────────────────────────────────────────────────────
log()     { echo -e "${CYAN}[docker-clean]${NC} $*"; }
warn()    { echo -e "${YELLOW}[docker-clean] ⚠ $*${NC}"; }
ok()      { echo -e "${GREEN}[docker-clean] ✓ $*${NC}"; }
error()   { echo -e "${RED}[docker-clean] ✗ $*${NC}" >&2; }
section() { echo -e "\n${BOLD}── $* ─────────────────────────────────────────${NC}"; }

MODE="${1:-}"

# ── Guard: Docker must be running ───────────────────────────────────────
check_docker() {
  if ! docker info >/dev/null 2>&1; then
    error "Docker daemon is not running. Start Docker Desktop first."
    exit 1
  fi
}

# ── Get Docker disk usage as structured data ────────────────────────────
get_docker_stats() {
  docker system df --format '{{json .}}' 2>/dev/null | python3 -c "
import sys, json
lines = [l.strip() for l in sys.stdin if l.strip()]
total_bytes = 0
reclaimable_bytes = 0
data = {}
for line in lines:
    try:
        d = json.loads(line)
        t = d.get('Type','')
        # Parse size string like '20.61GB' -> bytes
        def parse_size(s):
            s = str(s).replace(',','').strip()
            for unit, mult in [('GB',1e9),('MB',1e6),('KB',1e3),('B',1)]:
                if s.endswith(unit): return float(s[:-len(unit)]) * mult
            return 0
        size = parse_size(d.get('Size','0B'))
        reclaim_raw = d.get('Reclaimable','0B').split('(')[0].strip()
        reclaim = parse_size(reclaim_raw)
        total_bytes += size
        reclaimable_bytes += reclaim
        data[t] = {'size': size, 'reclaimable': reclaim,
                   'total': d.get('TotalCount', d.get('Active',0))}
    except: pass
def fmt(b):
    if b >= 1e9: return f'{b/1e9:.1f}GB'
    if b >= 1e6: return f'{b/1e6:.1f}MB'
    return f'{b/1e3:.1f}KB'
print(f'TOTAL={fmt(total_bytes)}')
print(f'RECLAIM={fmt(reclaimable_bytes)}')
print(f'TOTAL_BYTES={int(total_bytes)}')
print(f'RECLAIM_BYTES={int(reclaimable_bytes)}')
for k,v in data.items():
    print(f'{k}={fmt(v[\"size\"])}/{fmt(v[\"reclaimable\"])}')
" 2>/dev/null
}

# ── Get macOS disk free % ───────────────────────────────────────────────
get_mac_free_pct() {
  df -h /System/Volumes/Data 2>/dev/null | awk 'NR==2{gsub(/%/,"",$5); print 100-$5}' || echo 0
}

get_mac_avail() {
  df -h /System/Volumes/Data 2>/dev/null | awk 'NR==2{print $4}' || echo "?"
}

# ── Status / dashboard view ─────────────────────────────────────────────
show_status() {
  section "Docker Space Status"
  STATS=$(get_docker_stats)
  TOTAL=$(echo "$STATS" | grep ^TOTAL= | cut -d= -f2 | head -1)
  RECLAIM=$(echo "$STATS" | grep ^RECLAIM= | cut -d= -f2)
  RECLAIM_BYTES=$(echo "$STATS" | grep ^RECLAIM_BYTES= | cut -d= -f2 || echo 0)
  TOTAL_BYTES=$(echo "$STATS" | grep ^TOTAL_BYTES= | cut -d= -f2 || echo 0)
  MAC_FREE=$(get_mac_free_pct)
  MAC_AVAIL=$(get_mac_avail)

  echo ""
  printf "  %-22s %s\n" "Docker total usage:"   "$TOTAL"
  printf "  %-22s %s\n" "Reclaimable:"          "$RECLAIM"
  printf "  %-22s %s\n" "macOS free disk:"      "$MAC_AVAIL (${MAC_FREE}% free)"
  echo ""

  # Per-type breakdown
  while IFS='=' read -r key val; do
    case "$key" in
      Images|Containers|"Local Volumes"|"Build Cache")
        printf "  %-22s %s\n" "$key:" "$val"
        ;;
    esac
  done <<< "$STATS"
  echo ""

  # Recommendation
  if [ "$TOTAL_BYTES" -gt 0 ] && [ "$RECLAIM_BYTES" -gt 0 ]; then
    PCT=$(echo "$RECLAIM_BYTES $TOTAL_BYTES" | awk '{printf "%d", ($1/$2)*100}')
    if   [ "$PCT" -ge 80 ]; then warn "Reclaimable is ${PCT}% of total — run: docker-clean.sh --nuclear"
    elif [ "$PCT" -ge 50 ]; then warn "Reclaimable is ${PCT}% of total — run: docker-clean.sh"
    else                         ok   "Docker space looks healthy (${PCT}% reclaimable)"
    fi
  fi

  if [ "${MAC_FREE:-100}" -lt 10 ]; then
    warn "macOS disk is ${MAC_FREE}% free — consider --nuclear to compact the Docker VM"
  fi
}

# ── Dry run: show what WOULD be deleted ─────────────────────────────────
dry_run() {
  section "Dry Run — Nothing will be deleted"
  log "Would remove:"
  echo ""
  docker container ls -a --filter status=exited --filter status=dead \
    --format "  Container: {{.Names}} ({{.Status}})" 2>/dev/null | head -20
  docker images -f dangling=true --format "  Dangling image: {{.Repository}}:{{.Tag}} ({{.Size}})" 2>/dev/null | head -20
  echo ""
  docker system df 2>/dev/null
  echo ""
  RECLAIM=$(get_docker_stats | grep ^RECLAIM= | cut -d= -f2)
  warn "Estimated reclaimable: $RECLAIM"
  log "Run without --dry-run to execute."
}

# ── Level 1: Soft clean (safe, preserves all named images) ──────────────
soft_clean() {
  section "Soft Clean (stopped containers + dangling images + build cache)"
  BEFORE=$(get_docker_stats | grep ^TOTAL_BYTES= | cut -d= -f2 || echo 0)

  log "Removing stopped containers..."
  docker container prune -f 2>&1 | grep -E "^(Total|deleted)" || true

  log "Removing dangling images..."
  docker image prune -f 2>&1 | grep -E "^(Total|deleted)" || true

  log "Pruning build cache..."
  docker builder prune -f 2>&1 | grep "^Total:" || true

  log "Removing unused networks..."
  docker network prune -f 2>&1 | grep -E "^(Total|deleted)" || true

  AFTER=$(get_docker_stats | grep ^TOTAL_BYTES= | cut -d= -f2 || echo 0)
  SAVED=$(echo "$BEFORE $AFTER" | awk '{b=$1-$2; if(b>=1e9) printf "%.1fGB freed",b/1e9; else if(b>=1e6) printf "%.0fMB freed",b/1e6; else printf "%dKB freed",b/1e3}')
  ok "Soft clean complete — $SAVED"
}

# ── Level 2: Standard clean (unused images too, keeps running) ───────────
standard_clean() {
  section "Standard Clean (unused images + volumes + cache)"
  soft_clean

  log "Removing unused images (not referenced by any container)..."
  docker image prune -a -f 2>&1 | grep "^Total:" || true

  log "Removing unused volumes..."
  docker volume prune -f 2>&1 | grep "^Total:" || true

  ok "Standard clean complete."
  show_status
}

# ── Level 3: Nuclear — everything unused + VM compaction hint ────────────
nuclear_clean() {
  section "Nuclear Clean — ALL unused Docker resources"
  warn "This removes all stopped containers, ALL unused images, volumes, networks, and build cache."
  warn "Running containers and their images are preserved."

  if [ "${FORCE:-}" != "1" ]; then
    echo -n "  Type 'yes' to continue: "
    read -r CONFIRM
    [ "$CONFIRM" = "yes" ] || { log "Aborted."; exit 0; }
  fi

  BEFORE=$(get_docker_stats | grep ^TOTAL_BYTES= | cut -d= -f2 || echo 0)

  log "Running docker system prune -a --volumes..."
  docker system prune -a --volumes -f 2>&1 | grep "^Total:" || true

  AFTER=$(get_docker_stats | grep ^TOTAL_BYTES= | cut -d= -f2 || echo 0)
  SAVED=$(echo "$BEFORE $AFTER" | awk '{b=$1-$2; if(b>=1e9) printf "%.1fGB freed",b/1e9; else if(b>=1e6) printf "%.0fMB freed",b/1e6; else printf "%dKB freed",b/1e3}')
  ok "Nuclear clean complete — $SAVED"

  echo ""
  warn "To compact the Docker VM disk (return GB to macOS filesystem):"
  echo "  Docker Desktop → Settings → Resources → Advanced → 'Clean / Purge Data'"
  echo "  Or: reduce 'Virtual disk limit' slider → Apply & Restart"
  echo ""
  show_status
}

# ── Auto mode: threshold-based, no prompts ───────────────────────────────
auto_clean() {
  section "Auto Mode — threshold-based cleanup"
  STATS=$(get_docker_stats)
  TOTAL_BYTES=$(echo "$STATS" | grep ^TOTAL_BYTES= | cut -d= -f2 || echo 0)
  RECLAIM_BYTES=$(echo "$STATS" | grep ^RECLAIM_BYTES= | cut -d= -f2 || echo 0)
  MAC_FREE=$(get_mac_free_pct)

  if [ "$TOTAL_BYTES" -eq 0 ]; then
    ok "Docker has nothing to clean."; exit 0
  fi

  RECLAIM_PCT=$(echo "$RECLAIM_BYTES $TOTAL_BYTES" | awk '{printf "%d", ($1/$2)*100}')
  log "Docker reclaimable: ${RECLAIM_PCT}% | macOS free: ${MAC_FREE}%"

  # Decide level based on thresholds
  MAC_USED=$((100 - MAC_FREE))
  if   [ "$MAC_USED" -ge "$NUCLEAR_THRESHOLD" ] || [ "$RECLAIM_PCT" -ge 80 ]; then
    warn "Space critical (macOS ${MAC_USED}% used, ${RECLAIM_PCT}% reclaimable) — nuclear clean"
    FORCE=1 nuclear_clean
  elif [ "$MAC_USED" -ge "$AUTO_THRESHOLD" ] || [ "$RECLAIM_PCT" -ge 60 ]; then
    warn "Space high (macOS ${MAC_USED}% used, ${RECLAIM_PCT}% reclaimable) — standard clean"
    standard_clean
  elif [ "$MAC_USED" -ge "$WARN_THRESHOLD" ] || [ "$RECLAIM_PCT" -ge 40 ]; then
    warn "Space moderate (macOS ${MAC_USED}% used, ${RECLAIM_PCT}% reclaimable) — soft clean"
    soft_clean
  else
    ok "Space healthy (macOS ${MAC_USED}% used, ${RECLAIM_PCT}% reclaimable) — no action needed."
  fi
}

# ── LaunchAgent installer (macOS auto-run on schedule) ──────────────────
install_launchagent() {
  SCRIPT_PATH="$(realpath "$0")"
  PLIST=~/Library/LaunchAgents/com.docker-clean.plist
  section "Installing macOS LaunchAgent (weekly auto-clean)"
  cat > "$PLIST" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>         <string>com.docker-clean</string>
    <key>ProgramArguments</key>
    <array>
        <string>/bin/bash</string>
        <string>${SCRIPT_PATH}</string>
        <string>--auto</string>
    </array>
    <key>StartCalendarInterval</key>
    <dict>
        <key>Weekday</key> <integer>0</integer>
        <key>Hour</key>    <integer>3</integer>
        <key>Minute</key>  <integer>0</integer>
    </dict>
    <key>StandardOutPath</key> <string>${HOME}/.docker-clean.log</string>
    <key>StandardErrorPath</key> <string>${HOME}/.docker-clean.log</string>
    <key>RunAtLoad</key> <false/>
</dict>
</plist>
PLIST
  launchctl load "$PLIST" 2>/dev/null && ok "LaunchAgent installed — runs every Sunday 3am"
  log "Log: ~/.docker-clean.log"
  log "To uninstall: launchctl unload $PLIST && rm $PLIST"
}

# ── Help ─────────────────────────────────────────────────────────────────
usage() {
  echo ""
  echo -e "${BOLD}docker-clean.sh — Smart Docker space reclaimer${NC}"
  echo ""
  echo "  (no args)       Standard clean: cache + stopped containers + dangling images"
  echo "  --status        Show current Docker disk usage and recommendation"
  echo "  --dry-run       Preview what would be deleted, no changes made"
  echo "  --soft          Soft clean: stopped containers + dangling images + build cache only"
  echo "  --auto          Threshold-based: picks level based on actual disk usage"
  echo "  --nuclear       Remove ALL unused resources (prompt for confirmation)"
  echo "  --nuclear -f    Nuclear without prompt (CI/unattended use)"
  echo "  --install       Install macOS LaunchAgent for weekly auto-clean (Sunday 3am)"
  echo ""
  echo -e "${BOLD}Environment overrides:${NC}"
  echo "  DOCKER_WARN_PCT=70    Warn threshold % of macOS disk used (default: 70)"
  echo "  DOCKER_AUTO_PCT=85    Auto-clean threshold (default: 85)"
  echo "  DOCKER_NUKE_PCT=95    Nuclear threshold (default: 95)"
  echo ""
  echo -e "${BOLD}Examples:${NC}"
  echo "  docker-clean.sh                # standard clean, safe"
  echo "  docker-clean.sh --status       # see what's using space"
  echo "  docker-clean.sh --dry-run      # preview deletions"
  echo "  docker-clean.sh --auto         # auto-select clean level"
  echo "  docker-clean.sh --nuclear      # free everything (with confirmation)"
  echo "  DOCKER_AUTO_PCT=75 docker-clean.sh --auto  # custom threshold"
  echo ""
}

# ── Main ─────────────────────────────────────────────────────────────────
main() {
  check_docker

  case "${MODE:-}" in
    --status)           show_status ;;
    --dry-run)          dry_run ;;
    --soft)             soft_clean; show_status ;;
    --auto)             auto_clean ;;
    --nuclear)
      [ "${2:-}" = "-f" ] && FORCE=1
      nuclear_clean
      ;;
    --install)          install_launchagent ;;
    --help|-h|help)     usage ;;
    "")                 standard_clean ;;
    *)                  error "Unknown option: $MODE"; usage; exit 1 ;;
  esac
}

main "$@"

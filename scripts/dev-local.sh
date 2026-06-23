#!/usr/bin/env bash
#
# dev-local.sh — Tombstone full local dev stack.
# Wraps docker compose with status/logs/restart commands for the full Tombstone stack.
# Go services and intelligence are run via Docker Compose — not tmux windows.
#
# Usage:
#   scripts/dev-local.sh up            # start infra + all services + seed
#   scripts/dev-local.sh down          # stop docker compose stack
#   scripts/dev-local.sh down --all    # also wipe volumes
#   scripts/dev-local.sh status        # compose ps + port check
#   scripts/dev-local.sh logs <svc>    # docker compose logs -f <svc>
#   scripts/dev-local.sh attach        # print attach tips (use logs or docker attach)
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
COMPOSE="docker compose -f $ROOT/infra/docker-compose.yml"

# Host-side ports (from infra/docker-compose.yml)
GATEWAY_PORT=8080; FLAG_API_PORT=8081; EVALUATOR_PORT=8082
INTELLIGENCE_PORT=8083; DASHBOARD_PORT=3000
PG_PORT=5433; REDIS_PORT=6380

c_reset=$'\033[0m'; c_grn=$'\033[32m'; c_ylw=$'\033[33m'; c_red=$'\033[31m'; c_cyn=$'\033[36m'; c_dim=$'\033[2m'
info() { printf "${c_cyn}▸ %s${c_reset}\n" "$*"; }
ok()   { printf "${c_grn}✓ %s${c_reset}\n" "$*"; }
warn() { printf "${c_ylw}! %s${c_reset}\n" "$*"; }
die()  { printf "${c_red}✗ %s${c_reset}\n" "$*" >&2; exit 1; }
port_up() { lsof -ti :"$1" -sTCP:LISTEN >/dev/null 2>&1; }

preflight() {
  command -v docker >/dev/null 2>&1 || die "docker not found."
  docker info >/dev/null 2>&1 || die "Docker daemon not running. Start Docker Desktop."
  [ -f "$ROOT/infra/.env" ] || { warn ".env missing — copying from .env.example"; cp "$ROOT/infra/.env.example" "$ROOT/infra/.env"; }
}

cmd_up() {
  preflight
  info "Starting Tombstone stack via docker compose..."
  $COMPOSE up --build -d
  info "Waiting for postgres..."
  sleep 8
  info "Applying schema migrations..."
  $COMPOSE exec -T postgres psql -U tombstone -d tombstone < "$ROOT/services/flag-api/internal/db/schema.sql" 2>/dev/null || true
  info "Seeding sample data..."
  bash "$ROOT/scripts/seed-dev.sh" 2>/dev/null || true
  echo
  ok "Tombstone stack running:"
  printf "  ${c_dim}Gateway (SSE)  http://localhost:%s${c_reset}\n" "$GATEWAY_PORT"
  printf "  ${c_dim}Flag API       http://localhost:%s${c_reset}\n" "$FLAG_API_PORT"
  printf "  ${c_dim}Evaluator      http://localhost:%s${c_reset}\n" "$EVALUATOR_PORT"
  printf "  ${c_dim}Intelligence   http://localhost:%s${c_reset}\n" "$INTELLIGENCE_PORT"
  printf "  ${c_dim}Dashboard      http://localhost:%s${c_reset}\n" "$DASHBOARD_PORT"
  echo
  printf "  ${c_dim}Logs: scripts/dev-local.sh logs <service>${c_reset}\n"
  printf "  ${c_dim}Stop: scripts/dev-local.sh down${c_reset}\n"
}

cmd_status() {
  $COMPOSE ps 2>/dev/null || warn "Stack not running."
  echo
  info "Port status:"
  for name_port in "gateway:$GATEWAY_PORT" "flag-api:$FLAG_API_PORT" "evaluator:$EVALUATOR_PORT" \
                   "intelligence:$INTELLIGENCE_PORT" "dashboard:$DASHBOARD_PORT" \
                   "postgres:$PG_PORT" "redis:$REDIS_PORT"; do
    nm="${name_port%%:*}"; pt="${name_port##*:}"
    if port_up "$pt"; then printf "  ${c_grn}⬤${c_reset} %-16s :%s\n" "$nm" "$pt"
    else                   printf "  ${c_dim}·${c_reset} %-16s :%s\n" "$nm" "$pt"; fi
  done
}

cmd_logs()    { $COMPOSE logs -f --tail=50 "${1:?usage: logs <service>}"; }
cmd_down()    { $COMPOSE down $([ "${1:-}" = "--all" ] && echo "-v" || echo ""); ok "Stack stopped."; }
cmd_restart() { $COMPOSE restart "${1:?usage: restart <service>}"; ok "Restarted $1."; }
cmd_attach()  {
  echo "Tip: use 'scripts/dev-local.sh logs <service>' to tail a specific service."
  echo "     or 'docker attach <container>' for interactive attach."
}

case "${1:-up}" in
  up)      cmd_up ;;
  down)    cmd_down "${2:-}" ;;
  status)  cmd_status ;;
  logs)    cmd_logs "${2:-}" ;;
  restart) cmd_restart "${2:-}" ;;
  attach)  cmd_attach ;;
  -h|--help|help) grep '^#' "${BASH_SOURCE[0]}" | sed 's/^# \?//' | head -20 ;;
  *) die "Unknown command '${1}' (try: up|down|status|logs|restart|attach)" ;;
esac

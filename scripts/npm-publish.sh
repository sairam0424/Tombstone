#!/usr/bin/env bash
# Publishes all @tombstone/* packages to npm in dependency order.
# Prerequisites:
#   1. npm login (or set NPM_TOKEN env var)
#   2. 2FA enabled on your npm account
#   3. @tombstone org scope created at npmjs.com/org/tombstone
#
# Usage: bash scripts/npm-publish.sh [--dry-run]
set -euo pipefail

DRY_RUN=""
if [[ "${1:-}" == "--dry-run" ]]; then
  DRY_RUN="--dry-run"
  echo "DRY RUN — no packages will be published"
fi

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

publish_pkg() {
  local dir="$1"
  local name="$2"
  echo ""
  echo "━━━ Publishing $name ━━━"
  cd "$ROOT/$dir"
  npm run build
  npm publish --access public $DRY_RUN
  echo "✓ $name published"
}

# Order: eval first (no deps), then core (may reference eval), then rest
publish_pkg "packages/sdk-wasm"              "@tombstone/eval"
publish_pkg "packages/sdks/@flagmind/core"   "@tombstone/core"
publish_pkg "packages/sdks/@flagmind/react"  "@tombstone/react"
publish_pkg "packages/sdks/@flagmind/edge"   "@tombstone/edge"
publish_pkg "workspace-cli"                  "@tombstone/cli"
publish_pkg "workspace-mcp"                  "@tombstone/mcp"

echo ""
echo "✅ All packages published"
echo "   View at: https://www.npmjs.com/org/tombstone"

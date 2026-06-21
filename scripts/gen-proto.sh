#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PROTO_DIR="$ROOT/proto"

for domain in flags admin intelligence; do
  PROTO_FILE="$PROTO_DIR/v1/$domain"
  if [ ! -d "$PROTO_FILE" ]; then
    echo "Skipping $domain: directory not found"
    continue
  fi

  GEN_DIR="$PROTO_FILE/gen"
  mkdir -p "$GEN_DIR"

  echo "Generating Go stubs for: $domain"
  protoc \
    --proto_path="$PROTO_DIR" \
    --go_out="$GEN_DIR" \
    --go_opt=paths=source_relative \
    --go-grpc_out="$GEN_DIR" \
    --go-grpc_opt=paths=source_relative \
    "$PROTO_FILE"/*.proto
done

echo "Proto generation complete."

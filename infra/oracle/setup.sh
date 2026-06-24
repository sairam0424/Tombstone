#!/usr/bin/env bash
# One-shot setup script for Oracle Cloud Always Free ARM VM
# Run as ubuntu user AFTER cloud-init completes (check: cloud-init status --wait)
set -euo pipefail

REPO_DIR="/opt/tombstone"
ENV_FILE="$REPO_DIR/infra/.env"

echo "=== Tombstone Oracle Setup ==="

# 1. Pull latest code
cd "$REPO_DIR"
git pull origin main

# 2. Check .env is filled in
if grep -q "change-me" "$ENV_FILE"; then
    echo "ERROR: $ENV_FILE still has placeholder values."
    echo "Edit $ENV_FILE and fill in DB_URL, REDIS_URL, JWT_SECRET, etc."
    exit 1
fi

# 3. Build ARM images (multi-arch or native)
cd "$REPO_DIR"
docker compose -f infra/oracle/docker-compose.prod.yml build \
    --build-arg GOARCH=arm64 \
    --build-arg GOOS=linux

# 4. Start services
docker compose -f infra/oracle/docker-compose.prod.yml up -d

# 5. Wait for flag-api health
echo "Waiting for flag-api..."
for i in $(seq 1 30); do
    curl -sf http://localhost:8081/health >/dev/null 2>&1 && echo "flag-api: UP" && break
    sleep 2
done

# 6. Configure nginx
cp "$REPO_DIR/infra/oracle/nginx.conf" /etc/nginx/sites-available/tombstone
ln -sf /etc/nginx/sites-available/tombstone /etc/nginx/sites-enabled/tombstone
nginx -t && systemctl reload nginx

echo ""
echo "=== Setup complete ==="
echo "Next: certbot --nginx -d YOUR_DOMAIN to enable HTTPS"
echo "Then update TOMBSTONE_API_URL in GitHub Actions vars to https://YOUR_DOMAIN"

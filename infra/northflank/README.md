# Northflank Deployment — gateway + intelligence

Northflank Sandbox (free) hosts the two always-on services.

## Setup

1. Create account at northflank.com (no credit card required for Sandbox)
2. Create a new project: "tombstone"
3. Add secrets (Settings → Secret Groups):
   - SECRET_DB_URL = your Neon connection string
   - SECRET_REDIS_URL = your Upstash Redis URL
   - SECRET_FLAG_API_URL = https://your-oracle-domain/  (set after Oracle is live)
   - SECRET_BEDROCK_ACCESS_KEY_ID = (from infra/.env)
   - SECRET_BEDROCK_SECRET_ACCESS_KEY = (from infra/.env)

4. Connect GitHub repo: Settings → Git Integrations → sairam0424/Tombstone

5. Create two services using the JSON specs here:
   - gateway.json → tombstone-gateway (Sandbox plan)
   - intelligence.json → tombstone-intelligence (Sandbox plan)

6. Deploy both — they will build from Dockerfile automatically on push to main.

## After deploy

Note the Northflank service URLs (e.g. https://tombstone-gateway--xxx.northflank.app):
- Set TOMBSTONE_GATEWAY_URL in GitHub Actions repo variables
- Set TOMBSTONE_INTELLIGENCE_URL in GitHub Actions repo variables

SDK clients connect to: https://tombstone-gateway--xxx.northflank.app/api/v1/stream

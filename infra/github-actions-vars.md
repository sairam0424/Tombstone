# GitHub Actions Secrets + Variables — Tombstone

Configure these at: github.com/sairam0424/Tombstone/settings/secrets/actions

## Secrets (sensitive — never shown after save)

| Secret | Value | Used by |
|--------|-------|---------|
| `TOMBSTONE_SDK_TOKEN` | A strong random token (32+ chars) | deploy-dashboard.yml |
| `CLOUDFLARE_API_TOKEN` | CF API token with Pages:Edit permission | deploy-dashboard.yml |
| `CLOUDFLARE_ACCOUNT_ID` | From cloudflare.com/dashboard | deploy-dashboard.yml |

## Repository Variables (non-sensitive — visible in logs)

| Variable | Value | Used by |
|----------|-------|---------|
| `TOMBSTONE_API_URL` | https://your-oracle-domain | loop workflows, dashboard build |
| `TOMBSTONE_GATEWAY_URL` | https://tombstone-gateway--xxx.northflank.app | dashboard build |
| `TOMBSTONE_INTELLIGENCE_URL (optional — only if running intelligence locally)` | https://tombstone-intelligence--xxx.northflank.app | loop workflows |
| `TOMBSTONE_EVALUATOR_URL` | https://your-oracle-domain/api/v1 | loop-incident-response.yml |
| `TOMBSTONE_PROJECT_ID` | 00000000-0000-0000-0000-000000000001 | loop-flag-cleanup.yml |

## Notes

- TOMBSTONE_API_URL and TOMBSTONE_EVALUATOR_URL point to Oracle Cloud VM (flag-api + evaluator)
- TOMBSTONE_GATEWAY_URL and TOMBSTONE_INTELLIGENCE_URL (optional — only if running intelligence locally) point to Northflank services
- Loop workflows only activate when their required var is set (gated by `if: vars.TOMBSTONE_INTELLIGENCE_URL (optional — only if running intelligence locally) != ''`)
- Cloudflare secrets only needed after Cloudflare Pages project is created

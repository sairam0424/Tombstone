# GitHub Actions Secrets + Variables — Tombstone

> **Note:** These variables are for CI/CD and optional cloud deployment (v1.1+).
> For Tombstone v1.0 self-hosted local development, these are not required. See the root [README.md](../README.md) for local setup.

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

## Vercel Secrets (for deploy-dashboard-vercel.yml)

| Secret | Where to get it |
|--------|----------------|
| `VERCEL_TOKEN` | vercel.com → Settings → Tokens → Create |
| `VERCEL_ORG_ID` | Your org ID: `team_XwvXh7b4WauZwZFgYkjqz2uD` (same as Anvilry) |
| `VERCEL_PROJECT_ID` | Created when you link the Tombstone dashboard project in Vercel |

### How to get VERCEL_PROJECT_ID
1. `cd workspace-dashboard && npx vercel link`
2. Follow prompts — link to your existing Vercel account (same as Anvilry)
3. A `.vercel/project.json` will be created — the `projectId` value is your `VERCEL_PROJECT_ID`

> Note: deploy-dashboard-vercel.yml and deploy-dashboard.yml (Cloudflare) both exist.
> Only ONE needs to be active — disable the other by removing its trigger or deleting it.
> Recommended: use Vercel since you already have an account (Anvilry is deployed there).

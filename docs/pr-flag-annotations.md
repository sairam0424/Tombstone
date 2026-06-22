# PR Flag Blast Radius Annotations

Tombstone automatically scans every pull request for feature flag keys and
posts a blast-radius summary before the code lands in production.

## How it works

1. On each PR open/synchronize event the `Flag Blast Radius Check` workflow
   runs `scripts/pr-flag-check.sh`.
2. The script diffs the PR against its parent commit, extracts all flag keys
   referenced in changed lines (TypeScript, JavaScript, Python, Go, Ruby, Java),
   and queries the Tombstone evaluator's `/api/v1/blast-radius` endpoint for
   each key.
3. Results are posted as:
   - **GitHub Actions annotations** — appear inline in the PR Checks tab.
   - **PR comment** — a Markdown table summarising every detected flag, its
     risk score, and estimated affected users.

## Setup

### 1. Repository variables (Settings → Variables → Repository variables)

| Variable | Required | Description |
|----------|----------|-------------|
| `TOMBSTONE_API_URL` | Yes | Evaluator base URL, e.g. `https://eval.internal.example.com` |
| `TOMBSTONE_ENVIRONMENT` | No | Target environment for blast radius (default: `production`) |
| `TOMBSTONE_MIN_RISK` | No | Minimum risk level to annotate: `LOW`, `MEDIUM`, `HIGH`, `BLOCKED` (default: `MEDIUM`) |

### 2. Repository secrets (Settings → Secrets → Repository secrets)

| Secret | Required | Description |
|--------|----------|-------------|
| `TOMBSTONE_TOKEN` | No | Bearer credential for the evaluator API. Omit for unauthenticated deployments. |

The workflow is skipped entirely when `TOMBSTONE_API_URL` is not set, so
forks and fresh clones do not fail CI by default.

## Flag detection patterns

The script matches the following SDK call signatures in added/changed lines:

| Language | Patterns detected |
|----------|-------------------|
| TypeScript / JavaScript | `isEnabled(`, `evaluate(`, `getFlag(`, `flagEnabled(`, `checkFlag(` |
| Python | `is_enabled(`, `get_flag(`, `check_flag(` |
| Go | `IsEnabled(`, `GetFlag(` |
| Ruby / Java | same camelCase and snake_case variants above |

Flag keys must match `[a-z0-9._-]+` (lowercase alphanumeric with dots, underscores, hyphens).

## Exit behaviour

| Risk score | CI outcome |
|------------|------------|
| `BLOCKED` | **Fails** — exit 1 |
| `HIGH` | Warning annotation + PR comment row; CI passes |
| `MEDIUM` | Warning annotation + PR comment row; CI passes |
| `LOW` / `UNKNOWN` | Informational row only; CI passes |

To enforce `HIGH` as a hard gate, set `TOMBSTONE_MIN_RISK=HIGH` and add a
branch protection rule that requires the `flag-check` job to pass.

## Running locally

```bash
# Point at a local evaluator and simulate a PR diff
TOMBSTONE_API_URL=http://localhost:8082 \
TOMBSTONE_ENVIRONMENT=staging \
bash scripts/pr-flag-check.sh
```

The script falls back gracefully when the evaluator is unreachable (the API
call returns `{}` and the flag is reported as `UNKNOWN`).

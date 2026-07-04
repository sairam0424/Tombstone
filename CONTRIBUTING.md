# Contributing to Tombstone

Thank you for your interest in contributing to Tombstone! This project is a production intelligence layer for feature flags — blast radius gating, circuit-breaker auto-rollback, causal incident correlation, and automated stale-flag cleanup. Contributions of all kinds are welcome, whether you are fixing a typo, reporting a bug, or shipping a new subsystem.

---

## Ways to Contribute

- **Bug reports** — Found something broken? Open a GitHub Issue using the Bug Report template.
- **Feature requests** — Have an idea? Open a GitHub Issue using the Feature Request template.
- **Documentation** — Corrections, clarifications, new guides, and better examples all make the project more accessible.
- **Code** — Bug fixes, performance improvements, new integrations, SDK support for additional languages.

---

## Development Setup

### Prerequisites

| Tool | Version |
|------|---------|
| Go | 1.25+ |
| Python | 3.12+ |
| Node.js | 22+ |
| Docker + Docker Compose | Latest stable |

### Steps

```bash
# 1. Fork and clone
git clone https://github.com/<your-username>/Tombstone.git
cd Tombstone

# 2. Copy environment config
cp infra/.env.example infra/.env
# Edit infra/.env with local values if needed

# 3. Start the full stack (API :8081, Gateway :8080, Dashboard :3000)
make dev

# 4. Run all tests
make test
```

The `make dev` target uses Docker Compose to bring up PostgreSQL 16, Redis, Kafka, and all Tombstone services. Allow ~60 s for first-boot image pulls and DB migrations.

---

## Project Structure

```
services/          Go microservices (flag-api, gateway, evaluator, ...)
workspace-dashboard/  React 19 + Vite dashboard (TypeScript)
packages/sdks/     Client SDKs — TypeScript, Python, Ruby
infra/             Docker Compose, env templates, migrations
proto/             Protobuf definitions (source of truth for gRPC)
scripts/           Developer helper scripts
docs/              Architecture docs, API reference, runbooks
```

See `ARCHITECTURE.md` for the full system design and `CLAUDE.md` for the deep developer guide.

---

## Branch Naming

```
feat/description        New feature
fix/description         Bug fix
docs/description        Documentation only
refactor/description    Code change with no behaviour change
test/description        Test additions or corrections
chore/description       Build, tooling, deps
```

---

## Commit Messages

Follow [Conventional Commits](https://www.conventionalcommits.org/). Always include a scope from the list below.

**Format:** `<type>(<scope>): <short description>`

**Scopes:**

| Scope | What it covers |
|-------|---------------|
| `flag-api` | Flag management REST API |
| `gateway` | Evaluation gateway |
| `evaluator` | Flag evaluation engine |
| `intelligence` | Python ML/analytics service |
| `sdk` | Any client SDK |
| `dashboard` | React workspace dashboard |
| `cli` | Workspace CLI |
| `infra` | Docker Compose, migrations, env |
| `docs` | Documentation |

**Examples:**

```
feat(flag-api): add bulk flag archive endpoint
fix(evaluator): correct percentage rollout edge case at 100%
docs(sdk): add Ruby SDK quickstart guide
refactor(gateway): extract circuit-breaker state machine
```

---

## Pull Request Process

1. Fork the repository and create your branch from `main`.
2. Make your changes and add or update tests.
3. Verify all checks pass locally:
   ```bash
   make test    # all Go + TS + Python tests
   make lint    # golangci-lint + ruff + ESLint
   ```
4. Open a PR against `main`. Fill in the pull request template completely.
5. A maintainer will review within a reasonable timeframe. Please respond to review feedback promptly.
6. Once approved and CI is green, a maintainer will merge.

---

## Code Style

All style enforcement runs through `make lint`. Do not submit PRs that fail lint.

| Language | Linter | Config |
|----------|--------|--------|
| Go | `golangci-lint` | `.golangci.yml` |
| Python | `ruff` | `pyproject.toml` |
| TypeScript | ESLint + Prettier | `.eslintrc` / `prettier.config.js` |

---

## Testing

| Layer | Command |
|-------|---------|
| Go services | `cd services/<svc> && go test ./...` |
| Python intelligence | `cd services/intelligence && uv run pytest` |
| Dashboard | `cd workspace-dashboard && npm run test` |
| All | `make test` |

New code must include tests. Bug fixes must include a regression test that would have caught the bug.

---

## Getting Help

See [SUPPORT.md](SUPPORT.md) for all support channels. For questions about the codebase structure, start with `CLAUDE.md` and `ARCHITECTURE.md`.

---

## References

- `CLAUDE.md` — Deep developer guide, agent delegation, build commands
- `ARCHITECTURE.md` — System design, service boundaries, data flows
- `SECURITY.md` — Vulnerability disclosure policy
- `SUPPORT.md` — How to get help

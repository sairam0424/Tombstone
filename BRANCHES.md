# Tombstone — Branch Strategy

## Current State (v2.0.1)

| Branch | Status | HEAD |
|--------|--------|------|
| main | production (v2.0.1) | Merge pull request #40 from sairam0424/feature/v2-complete-integration |
| develop | integration (in sync with main) | same as main |

All `v2-phase/*` branches have been merged and closed. The v2.0.1 release ships
the full platform: 8 services (flag-api, gateway, evaluator, intelligence,
gitops-sync, ast-rewriter, marketplace, tombstone-operator), the v2 5-step
evaluation engine, ensemble anomaly detection, the MCP server, all SDKs
(@tombstone/core, @tombstone/react, @tombstone/edge, @tombstone/eval, Python,
Ruby, Java, .NET), Kubernetes operator, Helm multi-region charts, and SLSA
Level 2 supply-chain hardening.

## Branch Structure

```
main        <- Production releases only (tag: v2.0.1)
develop     <- Integration branch — all PRs merge here first
feature/*   <- One branch per feature or task, cut from develop
hotfix/*    <- Emergency production fixes, cut from main
```

## Workflow

### Feature development

```bash
# 1. Cut a feature branch from develop
git checkout develop && git pull origin develop
git checkout -b feature/<description>

# 2. Make atomic conventional commits
git commit -m "feat(flag-api): add scheduled-changes dry-run mode"

# 3. Push and open PR against develop
git push -u origin feature/<description>
gh pr create --base develop --title "feat(<scope>): <description>"

# 4. After review and CI green, merge to develop (squash or merge commit)
```

### Hotfix (production bug)

```bash
# 1. Cut hotfix branch from main
git checkout main && git pull origin main
git checkout -b hotfix/<description>

# 2. Fix, commit, push
git commit -m "fix(<scope>): <description>"
git push -u origin hotfix/<description>

# 3. PR to main, then backport to develop
gh pr create --base main --title "fix(<scope>): <description>"
# After main merge, open a second PR: hotfix/<description> -> develop
```

### Release

```bash
# develop -> main via PR when a release is ready
gh pr create --base main --head develop --title "chore(release): vX.Y.Z"
# After merge, tag main
git tag -a vX.Y.Z -m "Release vX.Y.Z"
git push origin vX.Y.Z
```

## Conventional Commits

Format: `type(scope): description`

| Type | Use |
|------|-----|
| feat | New feature |
| fix | Bug fix |
| chore | Tooling, config, dependency updates |
| refactor | Refactoring without behavior change |
| test | Tests only |
| docs | Documentation only |
| ci | CI/CD changes |
| perf | Performance improvement |
| revert | Revert a prior commit |

## Scopes

```
flag-api · gateway · evaluator · intelligence · gitops-sync · ast-rewriter
marketplace · operator · sdk · react · edge · eval · dashboard · mcp · cli
proto · infra · helm · terraform · auth
```

## Rules

- Never commit directly to `main` or `develop`.
- `develop` and `main` stay in sync after every release; no long-running divergence.
- All feature branches are short-lived — merge within days, not weeks.
- Hotfix branches are backported to `develop` immediately after merging to `main`.
- Branch names use kebab-case: `feature/blast-radius-ui`, `hotfix/circuit-breaker-race`.

## Exceptions

### Loop automation commits
`tombstone-loop[bot]` automated commits to `develop` for loop metrics, signals, and post-mortem docs are permitted. These are append-only data writes (JSONL metrics, markdown signals, LOG.md entries) — not code changes. They bypass the feature-branch rule because:
- They are triggered by events (circuit trips, daily crons), not human development
- Each write is idempotent and append-only
- They do NOT trigger CI runs on their own push (the `if:` guard prevents it)

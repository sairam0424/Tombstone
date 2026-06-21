# Tombstone — Branch Strategy

## Branch Structure

```
main        <- Production releases (never commit directly)
develop     <- Integration branch (all feature PRs merge here)
feature/*   <- One branch per phase group
```

## Current Branches

| Branch | HEAD Commit | Status |
|--------|------------|--------|
| main | `948177f` feat: initial Tombstone codebase | active |
| develop | `948177f` feat: initial Tombstone codebase | active (default) |

Both branches are currently at the same commit. No `feature/*` branches exist yet.

## Feature Branches -> Pull Requests

| Branch | Scope | PR Target |
|--------|-------|-----------|
| feature/foundation-phase1-4 | Core services, SDKs, dashboard, CI | develop |
| feature/phase5-ecosystem | Causal graph, multi-lang SDKs, warehouse, VS Code, marketplace | develop |
| feature/phase6-enterprise-closure | Relay proxy, OpenFeature, SAML, Helm, ClickHouse, Tombstone rename | develop |

## Workflow

1. Create feature branch from develop:
   ```bash
   git checkout develop && git checkout -b feature/my-task
   ```

2. Make atomic conventional commits per component

3. Push and open PR to develop:
   ```bash
   git push -u origin feature/my-task
   gh pr create --base develop --title "feat: ..."
   ```

4. After review, merge to develop via squash or merge commit

5. Release: develop -> main via PR when ready

## Conventional Commit Types

| Type | Use |
|------|-----|
| feat | New feature |
| fix | Bug fix |
| chore | Tooling, config, rename |
| refactor | Refactoring without behavior change |
| test | Tests only |
| docs | Documentation only |
| ci | CI/CD changes |
| perf | Performance improvement |

## Scopes

```
flag-api · gateway · evaluator · intelligence · gitops-sync · ast-rewriter
marketplace · sdk · dashboard · mcp · cli · infra · helm · terraform · auth
```

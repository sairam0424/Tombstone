# Tombstone GitOps Configuration

Flux CD v2.3+ manages all Tombstone deployments. This directory is the
GitOps source of truth for cluster state.

## Directory Structure

```
gitops/
├── clusters/
│   ├── production/    # Production cluster bootstrap — tracks main branch
│   └── staging/       # Staging cluster bootstrap — tracks develop branch
├── infrastructure/
│   └── controllers/
│       ├── tombstone-operator/   # HelmRelease: installs operator + CRDs
│       └── image-automation/     # ImagePolicy + ImageUpdateAutomation
├── apps/
│   ├── production/    # flagmind HelmRelease (full replicas, primary region)
│   └── staging/       # Patch overlay: single replica, IS_PRIMARY_REGION=false
└── flags/
    ├── base/           # FeatureFlag + FlagPolicy CR definitions
    └── overlays/       # Per-environment namespace binding
```

## Reconciliation Order

1. `tombstone-infrastructure` Kustomization installs tombstone-operator chart
   (`spec.install.crds: CreateReplace` — upgrades CRDs on operator updates)
2. `tombstone-apps` Kustomization depends on `tombstone-infrastructure` being Ready —
   FeatureFlag/FlagEnvironment/FlagPolicy CRDs are guaranteed present before HelmRelease
3. `tombstone-flags` Kustomization depends on `tombstone-apps` being Ready —
   FeatureFlag CRs are applied after tombstone-operator is running

## Image Updates

Flux ImageUpdateAutomation scans ghcr.io/sairam0424/tombstone-* every 5 minutes and
commits updated tag values to this repo. Tags follow semver (`>=1.0.0`).

Manual override (for hotfixes):
```bash
flux reconcile imagerepository tombstone-flag-api -n flux-system
flux reconcile imageupdateautomation tombstone-image-updater -n flux-system
```

## Drift Detection

Flux will detect and remediate drift between Git state and cluster state automatically.
Exception: `rolloutPct` fields on FlagEnvironment CRs are excluded from drift detection
(`spec.ignore.paths: ["/spec/environments/*/rolloutPct"]`) because the ML intelligence
service (LinUCB) and circuit-breaker auto-rollback modify these at runtime.

## gitops-sync Service (Deprecated)

`services/gitops-sync/` is kept for reference but is no longer published as a Docker
image or run in Kubernetes. Its function (YAML -> flag-api REST sync) is now handled
by the tombstone-operator FeatureFlagReconciler watching FeatureFlag CRs in Git.

---

## Argo CD Provider (provider=argocd|both)

When using the `argocd` or `both` provider, Argo CD manages application deployments
and flag definitions while Flux continues to manage infrastructure.

### Bootstrap order (REQUIRED)
1. Run `flux-bootstrap.yml` first — installs tombstone-operator + CRDs via Flux
2. Run `argocd-bootstrap.yml` second — installs Argo CD, creates Applications

### Split ownership
| Controller | Owns |
|-----------|------|
| **Flux** | `gitops/infrastructure/` — tombstone-operator chart, ImageUpdateAutomation, ImagePolicy |
| **Argo CD** | `gitops/apps/production/` — flagmind Helm chart |
| **Argo CD** | `gitops/flags/overlays/production/` — FeatureFlag + FlagPolicy CRs |

### ML rollout protection
FeatureFlag `.spec.environments.*.rolloutPct` is excluded from Argo CD drift correction
via `ignoreDifferences` + `RespectIgnoreDifferences=true` on each Application.
Without BOTH settings, Argo CD overwrites ML-managed rollout percentages on every sync.

### Blast-radius canary analysis (provider=both)
Argo Rollouts AnalysisTemplate `tombstone-blast-radius` polls:
  GET http://evaluator.tombstone.svc.cluster.local:8082/api/v1/blast-radius?flag_key=<key>
Returns LOW/MEDIUM (promote) or HIGH/BLOCKED (abort rollout).

### Notifications -> marketplace Slack (provider=both)
Sync-failed events are routed to the marketplace Slack kill-switch endpoint rather than
a separate Slack webhook. Requires `tombstone-marketplace-token` in `argocd-notifications-secret`.

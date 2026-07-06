# Tombstone GitOps — Flux CD + Argo CD Operations Guide

This directory is the GitOps source of truth for all Tombstone cluster state.
Flux CD v2.3+ is the baseline controller. Argo CD v2.11 is an optional overlay
available when `provider=argocd` or `provider=both`.

---

## Directory Structure

```
gitops/
├── clusters/
│   ├── production/           # Flux bootstrap — tracks main branch
│   │   ├── flux-system/      # Flux system Kustomization + GitRepository
│   │   └── argocd/           # Argo CD bootstrap resources (provider=argocd|both)
│   └── staging/              # Flux bootstrap — tracks develop branch
│       ├── flux-system/
│       └── argocd/
├── infrastructure/
│   └── controllers/
│       ├── tombstone-operator/   # HelmRelease: operator chart + CRDs (CreateReplace)
│       └── image-automation/     # ImageRepository + ImagePolicy + ImageUpdateAutomation
├── apps/
│   ├── production/           # flagmind HelmRelease (full replicas, IS_PRIMARY_REGION=true)
│   └── staging/              # Kustomize patch: single replica, IS_PRIMARY_REGION=false
├── flags/
│   ├── base/                 # FeatureFlag + FlagPolicy CR definitions
│   └── overlays/             # Per-environment namespace binding
└── providers/
    ├── flux/                 # Flux-only Kustomize overlay
    ├── argocd/               # Argo CD overlay: Application CRs, health checks, notifications
    └── both/                 # Combined overlay: Flux infra + Argo CD apps + Rollouts canary
```

---

## Provider Modes

| Mode | Flux owns | Argo CD owns | Use case |
|------|-----------|--------------|----------|
| `flux` | infrastructure + apps + flags | nothing | Lightweight; no Argo CD dependency |
| `argocd` | infrastructure only | apps + flags | Full Argo CD UI + RBAC + sync waves |
| `both` | infrastructure | apps + flags | Argo CD + Rollouts canary + blast-radius analysis |

Set the mode by activating the matching Kustomize overlay in `providers/<mode>/`.
Flux always manages `gitops/infrastructure/` regardless of provider.

---

## Bootstrap Order (CRITICAL)

**Flux must run before Argo CD.** Argo CD manages FeatureFlag and FlagPolicy
custom resources — those CRDs are installed by the tombstone-operator chart,
which is delivered by Flux. If Argo CD starts first, every Application sync
fails with `no matches for kind "FeatureFlag"`.

```
Step 1  →  .github/workflows/flux-bootstrap.yml
           Installs Flux controllers + tombstone-operator + CRDs
           Input secret: KUBECONFIG_B64 (base64 kubeconfig)

Step 2  →  .github/workflows/argocd-bootstrap.yml   (provider=argocd|both only)
           Installs Argo CD in argocd namespace
           Applies provider overlay (Applications, AnalysisTemplates, notifications)
           MUST run after Step 1 completes successfully
```

Verify CRDs are present before running Step 2:
```bash
kubectl get crd featureflags.tombstone.dev flagpolicies.tombstone.dev
```

---

## Flux Reconciliation Chain

Kustomizations are applied in strict dependency order enforced by `dependsOn`
and `healthChecks`.

```
tombstone-infrastructure (clusters/*/flux-system/infrastructure.yaml)
│   spec.dependsOn: []
│   spec.interval: 10m
│   spec.install.crds: CreateReplace          # upgrades CRDs on every operator release
│   healthChecks: tombstone-operator Deployment
│
└── tombstone-apps (clusters/*/flux-system/apps.yaml)
        spec.dependsOn: [tombstone-infrastructure]
        # FeatureFlag/FlagEnvironment/FlagPolicy CRDs guaranteed present
        # before any HelmRelease in apps/ is applied
        healthChecks: flagmind Deployment
        │
        └── tombstone-flags (clusters/*/flux-system/flags.yaml)
                spec.dependsOn: [tombstone-apps]
                # tombstone-operator Reconciler is running before CRs land
                # FeatureFlag CRs immediately reconciled by operator
```

`CreateReplace` on the HelmRelease means CRDs are always in sync with the
operator version — no manual `kubectl replace` needed on schema changes.

---

## Argo CD (provider=argocd|both)

### Sync Waves

Resources are applied in wave order within each Application sync cycle:

| Wave | Resource | Reason |
|------|----------|--------|
| 0 | `tombstone-root` Application | App-of-Apps root — discovers child Applications |
| 1 | `tombstone-app` Application | flagmind HelmRelease |
| 2 | `tombstone-flags` Application | FeatureFlag + FlagPolicy CRs |

Waves are set via annotation:
```yaml
metadata:
  annotations:
    argocd.argoproj.io/sync-wave: "2"
```

### Lua Health Checks

Four custom Lua checks are registered in `argocd-cm` so the App-of-Apps
health rollup correctly reflects flag CR state. (The App-of-Apps rollup was
removed in Argo CD v1.8 and must be re-added manually.)

| Resource kind | Healthy | Degraded |
|---------------|---------|----------|
| `FeatureFlag` | `.status.phase == "Synced"` | `.status.phase == "Error"` |
| `FeatureFlag` | `.status.phase == "Progressing"` maps to Processing | |
| `FeatureFlag` | `.status.phase == "Pending"` maps to Processing | |
| `FlagPolicy` | `.status.compliance == "Compliant"` | `.status.compliance == "Violation"` or `"Error"` |

### ignoreDifferences + RespectIgnoreDifferences

`rolloutPct` on FeatureFlag `.spec.environments[*]` is managed at runtime by
the LinUCB ML service and the circuit-breaker auto-rollback. Argo CD must not
overwrite these values during a sync.

Two settings are **both required** — `ignoreDifferences` alone is insufficient:

```yaml
# providers/argocd/applications/tombstone-flags.yaml
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: tombstone-flags
  namespace: argocd
spec:
  ignoreDifferences:
    - group: tombstone.dev
      kind: FeatureFlag
      jsonPointers:
        - /spec/environments/production/rolloutPct
        - /spec/environments/staging/rolloutPct
  syncPolicy:
    syncOptions:
      - RespectIgnoreDifferences=true   # REQUIRED: prevents overwrite during sync
```

Without `RespectIgnoreDifferences=true`, Argo CD marks the app Out-of-Sync
and then **overwrites** `rolloutPct` on the next automated sync, undoing the
ML engine's adjustments. Without `ignoreDifferences`, the live value causes
the app to stay permanently Out-of-Sync.

---

## Image Automation (Flux)

```
ImageRepository (ghcr.io/sairam0424/tombstone-*)
        │   polls every 5 minutes
        ▼
ImagePolicy (semver >=1.0.0)
        │   selects latest matching tag
        ▼
ImageUpdateAutomation
        │   commits updated image tag to gitops/ in this repo
        ▼
HelmRelease (apps/production/ or apps/staging/)
        │   Flux detects the commit and reconciles the chart
        ▼
Running pods with the new image
```

Manual override for hotfixes:
```bash
flux reconcile imagerepository tombstone-flag-api -n flux-system
flux reconcile imageupdateautomation tombstone-image-updater -n flux-system
```

Image tags in HelmRelease values files are updated via marker comments:
```yaml
image:
  tag: 1.2.1 # {"$imagepolicy": "flux-system:tombstone-flag-api:tag"}
```

---

## Argo Rollouts Blast-Radius Canary (provider=both)

When `provider=both`, new flag-api image releases use an Argo Rollouts
canary strategy gated by the `tombstone-blast-radius` AnalysisTemplate.

### AnalysisTemplate

```yaml
apiVersion: argoproj.io/v1alpha1
kind: AnalysisTemplate
metadata:
  name: tombstone-blast-radius
  namespace: tombstone
spec:
  metrics:
    - name: blast-radius-gate
      interval: 30s
      successCondition: result == "LOW" || result == "MEDIUM"
      failureCondition: result == "HIGH" || result == "BLOCKED"
      provider:
        web:
          url: http://evaluator.tombstone.svc.cluster.local:8082/api/v1/blast-radius?flag_key={{args.flag_key}}
          jsonPath: "{$.level}"
```

### Blast-Radius Levels

| Level | Action |
|-------|--------|
| `LOW` | Promote canary — continue increasing traffic weight |
| `MEDIUM` | Promote canary — acceptable risk |
| `HIGH` | Abort rollout — revert to stable |
| `BLOCKED` | Abort rollout — circuit breaker or kill switch active |

### Canary Steps (example)

```yaml
spec:
  strategy:
    canary:
      steps:
        - setWeight: 10
        - analysis:
            templates:
              - templateName: tombstone-blast-radius
            args:
              - name: flag_key
                value: "{{.RolloutSpec.Template.Annotations['tombstone/flag-key']}}"
        - setWeight: 50
        - analysis:
            templates:
              - templateName: tombstone-blast-radius
            args:
              - name: flag_key
                value: "{{.RolloutSpec.Template.Annotations['tombstone/flag-key']}}"
        - setWeight: 100
```

Traffic weight advances only after each AnalysisRun returns LOW or MEDIUM.
A HIGH or BLOCKED result immediately aborts and rolls back to the previous
stable revision.

---

## Notifications (provider=both)

Sync-failed events are routed to the Tombstone marketplace service, which
fans out to the configured Slack kill-switch endpoint. This ensures sync
failures appear in the same Slack channel as circuit-breaker alerts.

### Secret

```yaml
# providers/both/notifications/secret.yaml
apiVersion: v1
kind: Secret
metadata:
  name: argocd-notifications-secret
  namespace: argocd
stringData:
  marketplace-token: {{ required "tombstone-marketplace-token is required" .Values.marketplaceToken | b64enc }}
```

The `required` guard causes helm template to fail loudly if the token is
absent, preventing silent misconfiguration.

### Notification Trigger

```yaml
# providers/both/notifications/trigger.yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-notifications-cm
  namespace: argocd
data:
  trigger.on-sync-failed: |
    - when: app.status.operationState.phase in ['Error', 'Failed']
      send: [tombstone-sync-failed]
  template.tombstone-sync-failed: |
    webhook:
      marketplace-slack:
        method: POST
        path: /api/v1/marketplace/slack/actions
        body: |
          {
            "event": "sync_failed",
            "app": "{{.app.metadata.name}}",
            "revision": "{{.app.status.sync.revision}}",
            "message": "{{.app.status.operationState.message}}"
          }
  service.webhook.marketplace-slack: |
    url: http://marketplace.tombstone.svc.cluster.local:8086
    headers:
      - name: Authorization
        value: Bearer $marketplace-token
```

### Flux Notification Controller (provider=flux|both)

Flux-side alerting uses its own Notification Controller resources.
These are independent of Argo CD notifications.

```yaml
# infrastructure/controllers/notifications/provider.yaml
apiVersion: notification.toolkit.fluxcd.io/v1beta3
kind: Provider
metadata:
  name: tombstone-slack
  namespace: flux-system
spec:
  type: slack
  channel: "#tombstone-alerts"
  secretRef:
    name: tombstone-slack-secret   # contains "address" key (webhook URL)
---
apiVersion: notification.toolkit.fluxcd.io/v1beta3
kind: Alert
metadata:
  name: tombstone-reconcile-alert
  namespace: flux-system
spec:
  providerRef:
    name: tombstone-slack
  eventSeverity: error
  eventSources:
    - kind: Kustomization
      name: tombstone-infrastructure
    - kind: Kustomization
      name: tombstone-apps
    - kind: Kustomization
      name: tombstone-flags
    - kind: HelmRelease
      name: tombstone-operator
---
# Receiver for GitHub webhook → trigger reconcile on push
apiVersion: notification.toolkit.fluxcd.io/v1beta3
kind: Receiver
metadata:
  name: github-receiver
  namespace: flux-system
spec:
  type: github
  events:
    - ping
    - push
  secretRef:
    name: webhook-token
  resources:
    - kind: GitRepository
      name: tombstone-gitops
```

---

## Drift Detection

Two independent mechanisms protect runtime-managed fields from being
overwritten by the GitOps controller.

| Concern | Flux mechanism | Argo CD mechanism |
|---------|---------------|-------------------|
| `rolloutPct` on FeatureFlag CRs | `spec.ignore.paths: ["/spec/environments/*/rolloutPct"]` in Kustomization | `ignoreDifferences` jsonPointers + `RespectIgnoreDifferences=true` |
| All other fields | Flux detects and remediates automatically | Argo CD auto-sync detects and corrects |

Flux `spec.ignore.paths` prevents the path from being written by Flux's
server-side apply patches. It does not prevent Argo CD from syncing it —
which is why the Argo CD `ignoreDifferences` setting is also required when
`provider=argocd` or `provider=both`.

---

## Operational Commands

### Flux

```bash
# Check reconciliation status
flux get kustomizations -n flux-system
flux get helmreleases -n tombstone

# Force reconciliation
flux reconcile kustomization tombstone-infrastructure -n flux-system
flux reconcile kustomization tombstone-apps -n flux-system
flux reconcile kustomization tombstone-flags -n flux-system
flux reconcile helmrelease tombstone-operator -n tombstone

# Check image automation
flux get imagerepositories -n flux-system
flux get imagepolicies -n flux-system
flux get imageupdateautomations -n flux-system

# Force image scan + update commit
flux reconcile imagerepository tombstone-flag-api -n flux-system
flux reconcile imageupdateautomation tombstone-image-updater -n flux-system

# Check alerts and receivers
flux get alerts -n flux-system
flux get receivers -n flux-system

# Suspend reconciliation (e.g. during manual hotfix)
flux suspend kustomization tombstone-flags -n flux-system
# Resume
flux resume kustomization tombstone-flags -n flux-system

# Tail logs
flux logs -n flux-system --follow
```

### Argo CD (provider=argocd|both)

```bash
# List all Applications and their health/sync status
argocd app list

# Sync an application manually
argocd app sync tombstone-flags

# Get detailed sync status and diff
argocd app get tombstone-flags --show-operation

# Force hard refresh (bypasses cache)
argocd app get tombstone-flags --hard-refresh

# Rollback to previous revision
argocd app rollback tombstone-flags

# Watch sync progress
argocd app wait tombstone-flags --sync --health --timeout 300

# Get Argo Rollouts canary status (provider=both)
kubectl argo rollouts get rollout flagmind -n tombstone --watch

# Promote canary manually
kubectl argo rollouts promote flagmind -n tombstone

# Abort canary and rollback
kubectl argo rollouts abort flagmind -n tombstone
kubectl argo rollouts undo flagmind -n tombstone
```

---

## gitops-sync Service (Deprecated)

`services/gitops-sync/` is kept for reference only. It is no longer published
as a Docker image and is not deployed to any cluster. Its function — syncing
YAML flag definitions to flag-api via REST — is now handled entirely by the
tombstone-operator `FeatureFlagReconciler`, which watches FeatureFlag CRs in
Git and reconciles them against the flag-api state.

**Do not re-enable gitops-sync.** It has no awareness of the operator's
ownership semantics and will conflict with reconciler writes.

---

## Known Caveats

### Staging patch files are duplicated

`apps/staging/` contains a full Kustomize patch overlay that duplicates some
values from `apps/production/`. There is no DRY shared base for replica counts
and region env vars. A future refactor should extract a `apps/base/` layer.

### --components-extra is required for image automation

When bootstrapping Flux, the `--components-extra=image-reflector-controller,image-automation-controller`
flag must be passed to `flux bootstrap`. Without it, the image-related
controllers are not installed and `ImageRepository`/`ImagePolicy`/`ImageUpdateAutomation`
resources will be silently ignored (no error, just no reconciliation).

```bash
flux bootstrap github \
  --owner=sairam0424 \
  --repository=tombstone \
  --branch=main \
  --path=gitops/clusters/production \
  --components-extra=image-reflector-controller,image-automation-controller
```

### Pure flux mode has no dedicated provider overlay

`providers/flux/` contains only a passthrough `kustomization.yaml`. The
default cluster bootstrap (`clusters/*/flux-system/`) IS the flux-only
configuration. No additional provider resources are needed.

### App-of-Apps health rollup requires manual re-add

Argo CD removed the App-of-Apps health rollup in v1.8. When deploying Argo
CD, the Lua health check for `Application` resources must be added back to
`argocd-cm` explicitly (included in `providers/argocd/configmap-patch.yaml`).
Without it, `argocd app get tombstone-root` always shows `Missing` health
even when all child apps are healthy.

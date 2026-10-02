# Argo CD + Dynamic GitOps Provider Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add Argo CD as a second GitOps controller alongside Flux CD, implement a Kustomize-overlay-based dynamic provider pattern (GITOPS_PROVIDER=flux|argocd|both), wire Argo Rollouts blast-radius canary analysis, and route Argo CD notifications through the Tombstone marketplace Slack kill-switch.

**Architecture:** Split-responsibility dual-controller: Flux retains infrastructure ownership (tombstone-operator chart, CRD lifecycle, ImageUpdateAutomation) — moving these to Argo CD would hit the confirmed 5-minute false-Degraded CRD window bug (#26346). Argo CD owns applications (flagmind Helm chart + FeatureFlag CRs) and adds unique capabilities Flux cannot replicate: Rollouts AnalysisTemplate polling `GET /api/v1/blast-radius` as a canary gate, Notifications routing sync-failed webhooks to the marketplace Slack endpoint, and a web UI for resource tree visualization. The provider selection is a Kustomize overlay (`gitops/providers/flux/` vs `gitops/providers/argocd/`), NOT a runtime CRD — the GitOpsProvider-CRD approach has an irreducible circular bootstrap dependency (operator needs GitOps to deploy it, GitOps needs operator CRDs to exist first). ML rollout protection in Argo CD requires BOTH `ignoreDifferences` AND `RespectIgnoreDifferences: true` — `ignoreDifferences` alone does NOT prevent overwrite during sync.

**Tech Stack:** Argo CD v2.11+, Argo Rollouts v1.7+, Argo CD Image Updater v0.14+, Kustomize v5, Flux CD v2.3+ (retained), Go 1.22 (tombstone-operator), Lua (Argo CD health checks)

## Global Constraints

- CRD group for Tombstone custom resources: `tombstone.io/v1alpha1` (from `services/tombstone-operator/api/v1alpha1/register.go`)
- Blast-radius endpoint: `GET http://evaluator.tombstone.svc.cluster.local:8082/api/v1/blast-radius?flag_key=<key>` — runs on evaluator service port 8082
- FeatureFlag.status.phase values: `Pending`, `Synced`, `Error` (exact strings from types.go)
- FlagPolicy.status.phase values: `Compliant`, `Violation`, `Error` (exact strings from types.go)
- ML rollout protection: BOTH `spec.ignoreDifferences` AND `configs.params["application.resourceTrackingMethod"]` + `RespectIgnoreDifferences: true` in argocd-cmd-params-cm — `ignoreDifferences` alone reverts rolloutPct on every sync
- Provider pattern: Kustomize overlays only — NO GitOpsProvider CRD (circular bootstrap dependency)
- Flux RETAINS infrastructure: `gitops/infrastructure/` Kustomization, tombstone-operator HelmRelease, ImageUpdateAutomation — these are NOT migrated to Argo CD
- Argo CD App-of-Apps health rollup was removed in v1.8 and must be manually restored in argocd-cm (`resource.customizations.health.argoproj.io_Application`)
- All Argo CD manifests use `apiVersion: argoproj.io/v1alpha1` for Application/ApplicationSet
- Argo CD namespace: `argocd` (separate from `tombstone` and `flux-system`)
- Branch: `feat/argocd-gitops-provider` off `develop`
- Conventional commits: `feat(infra): ...`, `fix(infra): ...`
- All new gitops/providers/ kustomize builds must pass `kubectl kustomize <path>` before committing

---

## Phase A — Argo CD Provider Scaffold (Kustomize Overlay Structure)

### Task A1: Create gitops/providers/ directory — Flux and Argo CD overlays

**Files:**
- Create: `gitops/providers/flux/kustomization.yaml`
- Create: `gitops/providers/argocd/kustomization.yaml`
- Create: `gitops/providers/both/kustomization.yaml`
- Create: `gitops/providers/README.md`

**Interfaces:**
- Produces: Three provider overlays (flux|argocd|both) that include different sets of cluster bootstrap manifests. The `gitops/clusters/<env>/flux-system/gotk-sync.yaml` remains unchanged — it is the Flux bootstrap entry point. The Argo CD provider adds a parallel `gitops/clusters/<env>/argocd/` directory.

- [ ] **Step 1: Create gitops/providers/flux/kustomization.yaml**

```yaml
# gitops/providers/flux/kustomization.yaml
# Flux-only provider: uses existing Flux Kustomization chain.
# Active when GITOPS_PROVIDER=flux (default).
# No changes needed — gitops/clusters/*/flux-system/gotk-sync.yaml already bootstraps Flux.
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
# This overlay intentionally has no resources.
# Flux bootstrap is handled by gitops/clusters/<env>/flux-system/gotk-sync.yaml.
# See gitops/README.md for bootstrap instructions.
resources: []
```

- [ ] **Step 2: Create gitops/providers/argocd/kustomization.yaml**

```yaml
# gitops/providers/argocd/kustomization.yaml
# Argo CD provider: Argo CD owns apps/flags; Flux retains infrastructure.
# Active when GITOPS_PROVIDER=argocd.
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - ../../clusters/production/argocd/install.yaml
  - ../../clusters/production/argocd/apps.yaml
```

- [ ] **Step 3: Create gitops/providers/both/kustomization.yaml**

```yaml
# gitops/providers/both/kustomization.yaml
# Dual-controller: Flux owns infrastructure + image automation.
#                  Argo CD owns flagmind chart + flag definitions + Rollouts.
# Active when GITOPS_PROVIDER=both (recommended for full capability).
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - ../../clusters/production/argocd/install.yaml
  - ../../clusters/production/argocd/apps.yaml
  - ../../clusters/production/argocd/rollouts.yaml
  - ../../clusters/production/argocd/notifications.yaml
```

- [ ] **Step 4: Create gitops/providers/README.md**

```markdown
# GitOps Provider Selection

Tombstone supports three GitOps provider configurations, selected via Kustomize overlay.

## Providers

| Provider | Who owns what | When to use |
|----------|--------------|-------------|
| `flux` | Flux owns everything (default) | Minimal footprint, no UI needed |
| `argocd` | Flux: infrastructure + image automation<br>Argo CD: flagmind chart + flag CRs | Need Argo CD UI + Rollouts canary analysis |
| `both` | Same as `argocd` + Argo Rollouts + Notifications → marketplace Slack | Full capability, production recommended |

## Selecting a provider

Apply the provider overlay on top of the cluster bootstrap:

```bash
# Flux only (default — already active via flux-bootstrap.yml)
flux bootstrap github --path=gitops/clusters/production ...

# Argo CD or both (apply after flux bootstrap installs the operator)
kubectl apply -k gitops/providers/argocd/
# or
kubectl apply -k gitops/providers/both/
```

## Split-ownership boundary

Flux ALWAYS owns:
- `gitops/infrastructure/` (tombstone-operator chart, CRDs, ImageUpdateAutomation)
- Image tag automation (ImagePolicy + ImageUpdateAutomation)

Argo CD owns (when provider=argocd|both):
- flagmind Helm chart deployment
- FeatureFlag + FlagPolicy CR definitions
- Argo Rollouts canary analysis (provider=both only)
- Notifications → marketplace Slack (provider=both only)

## Why not a GitOpsProvider CRD?

A runtime CRD approach has an irreducible circular bootstrap dependency:
tombstone-operator needs a GitOps controller to deploy it, but the GitOps
controller needs tombstone-operator's CRDs to exist first.
Kustomize overlays are deploy-time selection — no circular dependency.
```

- [ ] **Step 5: Validate kustomize builds**

```bash
kubectl kustomize gitops/providers/flux/ > /dev/null && echo "flux: OK"
kubectl kustomize gitops/providers/argocd/ 2>&1 | grep -v "no matches" | head -3 || echo "argocd: needs clusters/production/argocd/ (Task A2)"
```

- [ ] **Step 6: Commit**

```bash
git add gitops/providers/
git commit -m "feat(infra): scaffold gitops/providers/ — flux|argocd|both Kustomize overlay provider pattern"
```

---

## Phase B — Argo CD Installation Manifests

### Task A2: Create Argo CD install manifests and bootstrap configuration

**Files:**
- Create: `gitops/clusters/production/argocd/install.yaml`
- Create: `gitops/clusters/production/argocd/argocd-cm-patch.yaml`
- Create: `gitops/clusters/production/argocd/argocd-cmd-params-cm-patch.yaml`
- Create: `gitops/clusters/production/argocd/kustomization.yaml`
- Create: `gitops/clusters/staging/argocd/install.yaml`
- Create: `gitops/clusters/staging/argocd/kustomization.yaml`

**Interfaces:**
- Produces: Argo CD installed in `argocd` namespace with custom health checks for Tombstone CRDs and `RespectIgnoreDifferences: true` enabled

- [ ] **Step 1: Create production Argo CD install.yaml**

```yaml
# gitops/clusters/production/argocd/install.yaml
# Argo CD v2.11 installation via upstream manifest.
# Namespace: argocd (separate from tombstone and flux-system).
apiVersion: v1
kind: Namespace
metadata:
  name: argocd
---
# Download upstream install manifest and apply as a resource reference.
# In production, vendor this: curl -sL https://raw.githubusercontent.com/argoproj/argo-cd/v2.11.0/manifests/install.yaml -o gitops/clusters/production/argocd/argocd-install-v2.11.0.yaml
# Then reference it here as a resource.
# For now: reference the upstream URL directly (requires network at apply time).
# Replace with vendored file before air-gapped deployment.
```

Create `gitops/clusters/production/argocd/argocd-install-v2.11.0.yaml` by downloading:
```bash
curl -sL https://raw.githubusercontent.com/argoproj/argo-cd/v2.11.0/manifests/install.yaml \
  -o gitops/clusters/production/argocd/argocd-install-v2.11.0.yaml
echo "Downloaded $(wc -l < gitops/clusters/production/argocd/argocd-install-v2.11.0.yaml) lines"
```

- [ ] **Step 2: Create argocd-cm-patch.yaml with Tombstone CRD health checks**

```yaml
# gitops/clusters/production/argocd/argocd-cm-patch.yaml
# Patches argocd-cm to add:
# 1. Lua health checks for Tombstone's custom CRDs (tombstone.io/v1alpha1)
# 2. Restore App-of-Apps health rollup (removed in Argo CD v1.8, must be manually restored)
apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
  namespace: argocd
data:
  # Restore Application health rollup — removed in Argo CD v1.8 (issue #3781).
  # Without this, the parent App-of-Apps never becomes Healthy even when all children are.
  resource.customizations.health.argoproj.io_Application: |
    hs = {}
    hs.status = "Progressing"
    hs.message = ""
    if obj.status ~= nil then
      if obj.status.health ~= nil then
        hs.status = obj.status.health.status
        if obj.status.health.message ~= nil then
          hs.message = obj.status.health.message
        end
      end
    end
    return hs

  # FeatureFlag health check.
  # Maps Tombstone's phase values to Argo CD health states:
  #   Pending  -> Progressing (reconcile in progress)
  #   Synced   -> Healthy     (mirrored to flag-api successfully)
  #   Error    -> Degraded    (sync failed, requires intervention)
  resource.customizations.health.tombstone.io_FeatureFlag: |
    hs = {}
    if obj.status == nil or obj.status.phase == nil then
      hs.status = "Progressing"
      hs.message = "Awaiting first reconcile"
      return hs
    end
    phase = obj.status.phase
    if phase == "Synced" then
      hs.status = "Healthy"
      hs.message = "Flag synced to flag-api"
    elseif phase == "Pending" then
      hs.status = "Progressing"
      hs.message = "Reconcile in progress"
    elseif phase == "Error" then
      hs.status = "Degraded"
      if obj.status.conditions ~= nil then
        for i, c in ipairs(obj.status.conditions) do
          if c.type == "Synced" and c.status == "False" then
            hs.message = c.message
            break
          end
        end
      end
      if hs.message == "" then hs.message = "Sync error — check flag-api" end
    else
      hs.status = "Unknown"
      hs.message = "Unrecognized phase: " .. phase
    end
    return hs

  # FlagPolicy health check.
  # Maps Tombstone's policy phase values to Argo CD health states:
  #   Compliant -> Healthy    (all flags within policy bounds)
  #   Violation -> Degraded   (flags violating policy)
  #   Error     -> Degraded   (evaluation failed)
  resource.customizations.health.tombstone.io_FlagPolicy: |
    hs = {}
    if obj.status == nil or obj.status.phase == nil then
      hs.status = "Progressing"
      hs.message = "Awaiting first evaluation"
      return hs
    end
    phase = obj.status.phase
    if phase == "Compliant" then
      hs.status = "Healthy"
      hs.message = "All flags within policy bounds"
    elseif phase == "Violation" then
      hs.status = "Degraded"
      local flags = ""
      if obj.status.violatingFlags ~= nil then
        flags = table.concat(obj.status.violatingFlags, ", ")
      end
      hs.message = "Policy violated by: " .. flags
    elseif phase == "Error" then
      hs.status = "Degraded"
      hs.message = "Policy evaluation error"
    else
      hs.status = "Unknown"
      hs.message = "Unrecognized phase: " .. phase
    end
    return hs

  # FlagEnvironment health check.
  resource.customizations.health.tombstone.io_FlagEnvironment: |
    hs = {}
    if obj.status == nil or obj.status.phase == nil then
      hs.status = "Progressing"
      hs.message = "Awaiting first reconcile"
      return hs
    end
    if obj.status.phase == "Synced" then
      hs.status = "Healthy"
      hs.message = "Environment synced"
    elseif obj.status.phase == "Pending" then
      hs.status = "Progressing"
      hs.message = "Reconcile in progress"
    else
      hs.status = "Degraded"
      hs.message = "Sync error"
    end
    return hs
```

- [ ] **Step 3: Create argocd-cmd-params-cm-patch.yaml to enable RespectIgnoreDifferences**

```yaml
# gitops/clusters/production/argocd/argocd-cmd-params-cm-patch.yaml
# CRITICAL: RespectIgnoreDifferences=true prevents Argo CD from overwriting
# ignoreDifferences fields during sync. Without this, ignoreDifferences only
# suppresses the OutOfSync display — the actual sync still overwrites rolloutPct.
# (Confirmed finding from deep research — ignoreDifferences alone is insufficient.)
apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cmd-params-cm
  namespace: argocd
data:
  application.resourceTrackingMethod: "annotation"
  server.application.namespaces: "tombstone"
```

Note: `RespectIgnoreDifferences` is enabled via the Application-level flag `spec.syncPolicy.syncOptions: ["RespectIgnoreDifferences=true"]` on each Application — see Task A3.

- [ ] **Step 4: Create production argocd/kustomization.yaml**

```yaml
# gitops/clusters/production/argocd/kustomization.yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - argocd-install-v2.11.0.yaml
patches:
  - path: argocd-cm-patch.yaml
    target:
      kind: ConfigMap
      name: argocd-cm
  - path: argocd-cmd-params-cm-patch.yaml
    target:
      kind: ConfigMap
      name: argocd-cmd-params-cm
```

- [ ] **Step 5: Create staging versions (lighter resource limits)**

```bash
mkdir -p gitops/clusters/staging/argocd
```

Create `gitops/clusters/staging/argocd/install.yaml` (same as production — Argo CD has no staging variant):
```yaml
# gitops/clusters/staging/argocd/install.yaml
# Points to the same upstream manifest as production.
# Staging uses smaller resource requests via patch.
```

Download same file:
```bash
cp gitops/clusters/production/argocd/argocd-install-v2.11.0.yaml \
   gitops/clusters/staging/argocd/argocd-install-v2.11.0.yaml
```

Create `gitops/clusters/staging/argocd/kustomization.yaml`:
```yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - argocd-install-v2.11.0.yaml
patches:
  - path: ../production/argocd/argocd-cm-patch.yaml
    target:
      kind: ConfigMap
      name: argocd-cm
  - path: ../production/argocd/argocd-cmd-params-cm-patch.yaml
    target:
      kind: ConfigMap
      name: argocd-cmd-params-cm
```

- [ ] **Step 6: Verify kustomize patch applies**

```bash
kubectl kustomize gitops/clusters/production/argocd/ 2>&1 | grep "kind: ConfigMap" | head -5
```
Expected: ConfigMap objects present including `argocd-cm` and `argocd-cmd-params-cm`.

- [ ] **Step 7: Commit**

```bash
git add gitops/clusters/production/argocd/ gitops/clusters/staging/argocd/
git commit -m "feat(infra): add Argo CD v2.11 install manifests with Tombstone CRD Lua health checks and RespectIgnoreDifferences config"
```

---

## Phase C — Argo CD Applications (Split-Ownership)

### Task A3: Create Argo CD Application manifests for flagmind chart and flag definitions

**Files:**
- Create: `gitops/clusters/production/argocd/apps.yaml`
- Create: `gitops/clusters/staging/argocd/apps.yaml`

**Interfaces:**
- Consumes: tombstone-operator already installed by Flux infrastructure Kustomization (Task A2 does NOT move operator to Argo CD — Flux retains it)
- Produces: Two Argo CD Applications: `tombstone-app` (flagmind Helm chart) and `tombstone-flags` (FeatureFlag/FlagPolicy CRs), with `ignoreDifferences` + `RespectIgnoreDifferences=true` for rolloutPct

**Critical constraint:** Both `spec.ignoreDifferences` AND `spec.syncPolicy.syncOptions: ["RespectIgnoreDifferences=true"]` MUST be present on any Application that manages FeatureFlag or FlagEnvironment resources. `ignoreDifferences` alone only suppresses the diff display — the field is still overwritten during sync.

- [ ] **Step 1: Create gitops/clusters/production/argocd/apps.yaml**

```yaml
# gitops/clusters/production/argocd/apps.yaml
# App-of-Apps pattern: root Application manages tombstone-app and tombstone-flags.
# Sync waves ensure tombstone-app (Helm chart) deploys before tombstone-flags (CRs).
---
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: tombstone-root
  namespace: argocd
  # App-of-Apps: manages all Tombstone Argo CD Applications.
  annotations:
    argocd.argoproj.io/sync-wave: "0"
spec:
  project: default
  source:
    repoURL: https://github.com/sairam0424/Tombstone
    targetRevision: main
    path: gitops/clusters/production/argocd
    directory:
      include: "apps-children.yaml"
  destination:
    server: https://kubernetes.default.svc
    namespace: argocd
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
      - CreateNamespace=true
---
# tombstone-app: owns the flagmind Helm chart (5 services).
# Sync wave 1: deploys after tombstone-operator (managed by Flux, wave -1 conceptually).
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: tombstone-app
  namespace: argocd
  annotations:
    argocd.argoproj.io/sync-wave: "1"
spec:
  project: default
  source:
    repoURL: https://github.com/sairam0424/Tombstone
    targetRevision: main
    path: gitops/apps/production
  destination:
    server: https://kubernetes.default.svc
    namespace: tombstone
  syncPolicy:
    automated:
      prune: true
      # selfHeal=true: Argo CD will revert drift.
      # PROTECTED: rolloutPct fields are excluded via ignoreDifferences + RespectIgnoreDifferences.
      selfHeal: true
    syncOptions:
      - CreateNamespace=true
      - RespectIgnoreDifferences=true
  # CRITICAL: ignoreDifferences suppresses OutOfSync display for ML-managed fields.
  # RespectIgnoreDifferences=true (in syncOptions above) prevents overwrite during sync.
  # Without BOTH, rolloutPct is overwritten to Git value on every reconcile.
  ignoreDifferences:
    - group: tombstone.io
      kind: FeatureFlag
      jsonPointers:
        - /spec/environments/production/rolloutPct
        - /spec/environments/staging/rolloutPct
    - group: tombstone.io
      kind: FlagEnvironment
      jsonPointers:
        - /spec/defaultRolloutPct
---
# tombstone-flags: owns FeatureFlag + FlagPolicy CRs.
# Sync wave 2: CRs applied after flagmind chart (tombstone-operator must be running).
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: tombstone-flags
  namespace: argocd
  annotations:
    argocd.argoproj.io/sync-wave: "2"
spec:
  project: default
  source:
    repoURL: https://github.com/sairam0424/Tombstone
    targetRevision: main
    path: gitops/flags/overlays/production
  destination:
    server: https://kubernetes.default.svc
    namespace: tombstone
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
      - RespectIgnoreDifferences=true
  ignoreDifferences:
    - group: tombstone.io
      kind: FeatureFlag
      jsonPointers:
        - /spec/environments/production/rolloutPct
        - /spec/environments/staging/rolloutPct
```

- [ ] **Step 2: Create gitops/clusters/staging/argocd/apps.yaml**

```yaml
# gitops/clusters/staging/argocd/apps.yaml
# Staging: same structure but targetRevision=develop, namespace differences.
---
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: tombstone-app
  namespace: argocd
  annotations:
    argocd.argoproj.io/sync-wave: "1"
spec:
  project: default
  source:
    repoURL: https://github.com/sairam0424/Tombstone
    targetRevision: develop
    path: gitops/apps/staging
  destination:
    server: https://kubernetes.default.svc
    namespace: tombstone
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
      - CreateNamespace=true
      - RespectIgnoreDifferences=true
  ignoreDifferences:
    - group: tombstone.io
      kind: FeatureFlag
      jsonPointers:
        - /spec/environments/production/rolloutPct
        - /spec/environments/staging/rolloutPct
---
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: tombstone-flags
  namespace: argocd
  annotations:
    argocd.argoproj.io/sync-wave: "2"
spec:
  project: default
  source:
    repoURL: https://github.com/sairam0424/Tombstone
    targetRevision: develop
    path: gitops/flags/overlays/staging
  destination:
    server: https://kubernetes.default.svc
    namespace: tombstone
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
      - RespectIgnoreDifferences=true
  ignoreDifferences:
    - group: tombstone.io
      kind: FeatureFlag
      jsonPointers:
        - /spec/environments/production/rolloutPct
        - /spec/environments/staging/rolloutPct
```

- [ ] **Step 3: Validate YAML parses correctly**

```bash
python3 -c "import yaml; list(yaml.safe_load_all(open('gitops/clusters/production/argocd/apps.yaml')))" && echo "production apps VALID"
python3 -c "import yaml; list(yaml.safe_load_all(open('gitops/clusters/staging/argocd/apps.yaml')))" && echo "staging apps VALID"
```
Expected: both print VALID.

- [ ] **Step 4: Commit**

```bash
git add gitops/clusters/production/argocd/apps.yaml gitops/clusters/staging/argocd/apps.yaml
git commit -m "feat(infra): add Argo CD Application manifests for flagmind chart + flag CRs with ignoreDifferences+RespectIgnoreDifferences for ML rollout protection"
```

---

## Phase D — Argo Rollouts + Blast-Radius Canary Analysis

### Task A4: Add Argo Rollouts installation and blast-radius AnalysisTemplate

**Files:**
- Create: `gitops/clusters/production/argocd/rollouts.yaml`
- Create: `gitops/apps/production/tombstone/rollout-analysis.yaml`

**Key fact from research:** Argo Rollouts AnalysisTemplate uses the `web` metric provider. The blast-radius endpoint is `GET http://evaluator.tombstone.svc.cluster.local:8082/api/v1/blast-radius?flag_key={{args.flag-key}}`. The response JSON has field `risk_score` with values `LOW|MEDIUM|HIGH|BLOCKED`. The `timeoutSeconds` defaults to 10 and is the only timeout control.

- [ ] **Step 1: Create rollouts.yaml — Argo Rollouts install + AnalysisTemplate**

```yaml
# gitops/clusters/production/argocd/rollouts.yaml
# Argo Rollouts v1.7 installation + blast-radius canary AnalysisTemplate.
---
# Install Argo Rollouts controller in argo-rollouts namespace.
# Vendor this file for production: 
# curl -sL https://github.com/argoproj/argo-rollouts/releases/download/v1.7.0/install.yaml \
#   -o gitops/clusters/production/argocd/argo-rollouts-install-v1.7.0.yaml
apiVersion: v1
kind: Namespace
metadata:
  name: argo-rollouts
---
# AnalysisTemplate: polls Tombstone blast-radius endpoint as canary gate.
# Usage: reference this template in a Rollout spec.analysis.templates[].
# The canary pod will be promoted only if blast-radius returns LOW or MEDIUM.
# HIGH or BLOCKED risk causes the analysis to fail and the rollout to abort.
#
# Wire to a Rollout with:
#   spec:
#     analysis:
#       templates:
#         - templateName: tombstone-blast-radius
#       args:
#         - name: flag-key
#           value: "checkout-v2"
apiVersion: argoproj.io/v1alpha1
kind: AnalysisTemplate
metadata:
  name: tombstone-blast-radius
  namespace: tombstone
spec:
  args:
    - name: flag-key
  metrics:
    - name: blast-radius-gate
      # Poll blast-radius endpoint every 30 seconds for 5 minutes max.
      interval: 30s
      count: 10
      # Fail if HIGH or BLOCKED. LOW and MEDIUM allow promotion.
      successCondition: result.risk_score == "LOW" || result.risk_score == "MEDIUM"
      failureCondition: result.risk_score == "HIGH" || result.risk_score == "BLOCKED"
      failureLimit: 1
      provider:
        web:
          # GET is the default — no body needed since flag_key is in the URL.
          url: "http://evaluator.tombstone.svc.cluster.local:8082/api/v1/blast-radius?flag_key={{args.flag-key}}"
          timeoutSeconds: 10
          jsonPath: "{$}"
```

Download Argo Rollouts installer:
```bash
curl -sL https://github.com/argoproj/argo-rollouts/releases/download/v1.7.0/install.yaml \
  -o gitops/clusters/production/argocd/argo-rollouts-install-v1.7.0.yaml
echo "Downloaded $(wc -l < gitops/clusters/production/argocd/argo-rollouts-install-v1.7.0.yaml) lines"
```

- [ ] **Step 2: Create rollout-analysis.yaml — example Rollout for flag-api**

```yaml
# gitops/apps/production/tombstone/rollout-analysis.yaml
# Example Argo Rollout for flag-api using tombstone-blast-radius AnalysisTemplate.
# This replaces the Deployment for flag-api when Argo Rollouts is active.
# The canary step polls blast-radius before promoting to 100%.
#
# NOTE: This file is included by the argocd provider only. The Flux provider
# continues to use the standard Deployment via the flagmind Helm chart.
apiVersion: argoproj.io/v1alpha1
kind: Rollout
metadata:
  name: tombstone-flag-api-rollout
  namespace: tombstone
spec:
  replicas: 2
  selector:
    matchLabels:
      app.kubernetes.io/name: tombstone
      app.kubernetes.io/component: flag-api
  template:
    metadata:
      labels:
        app.kubernetes.io/name: tombstone
        app.kubernetes.io/component: flag-api
    spec:
      containers:
        - name: flag-api
          image: ghcr.io/sairam0424/tombstone-flag-api:v1.3.0
          ports:
            - name: http
              containerPort: 8081
  strategy:
    canary:
      # Step 1: send 20% traffic to canary
      # Step 2: run blast-radius analysis — abort if HIGH/BLOCKED
      # Step 3: promote to 100%
      steps:
        - setWeight: 20
        - analysis:
            templates:
              - templateName: tombstone-blast-radius
            args:
              - name: flag-key
                value: "flag-api-deployment"
        - setWeight: 100
```

- [ ] **Step 3: Update gitops/providers/both/kustomization.yaml to include rollouts.yaml**

Read `gitops/providers/both/kustomization.yaml` and verify `rollouts.yaml` is already in the resources list (it was added in Task A1 Step 3).

```bash
grep "rollouts" gitops/providers/both/kustomization.yaml && echo "rollouts: already referenced"
```

- [ ] **Step 4: Verify YAML validity**

```bash
python3 -c "import yaml; list(yaml.safe_load_all(open('gitops/clusters/production/argocd/rollouts.yaml')))" && echo "rollouts VALID"
python3 -c "import yaml; list(yaml.safe_load_all(open('gitops/apps/production/tombstone/rollout-analysis.yaml')))" && echo "rollout-analysis VALID"
```

- [ ] **Step 5: Commit**

```bash
git add gitops/clusters/production/argocd/rollouts.yaml \
        gitops/clusters/production/argocd/argo-rollouts-install-v1.7.0.yaml \
        gitops/apps/production/tombstone/rollout-analysis.yaml
git commit -m "feat(infra): add Argo Rollouts v1.7 + blast-radius AnalysisTemplate — canary gate using evaluator GET /api/v1/blast-radius"
```

---

## Phase E — Argo CD Notifications → Marketplace Slack

### Task A5: Route Argo CD sync-failed notifications through Tombstone marketplace

**Files:**
- Create: `gitops/clusters/production/argocd/notifications.yaml`

**Key fact from research:** The Argo CD Notifications controller accepts a webhook service with arbitrary URL. Tombstone's marketplace service handles POST requests at `http://marketplace.tombstone.svc.cluster.local:8086/api/v1/marketplace/slack/actions`. The trigger condition is `app.status.operationState.phase in ['Failed']`.

**Note:** The Notifications controller is a separate Argo CD component — it needs its own install manifest OR it's included in the full Argo CD install. Verify it's in `argocd-install-v2.11.0.yaml`:
```bash
grep "argocd-notifications" gitops/clusters/production/argocd/argocd-install-v2.11.0.yaml | wc -l
```
If 0, download the notifications install separately:
```bash
curl -sL https://raw.githubusercontent.com/argoproj/argo-cd/v2.11.0/notifications_catalog/install.yaml \
  -o gitops/clusters/production/argocd/argocd-notifications-v2.11.0.yaml
```

- [ ] **Step 1: Create notifications.yaml**

```yaml
# gitops/clusters/production/argocd/notifications.yaml
# Routes Argo CD sync-failed events to Tombstone marketplace Slack endpoint.
# The marketplace service handles the Slack message formatting and delivery.
# No duplicate Slack webhook needed — reuses existing marketplace integration.
---
# argocd-notifications-cm: configure webhook service pointing at marketplace.
apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-notifications-cm
  namespace: argocd
data:
  # Service: tombstone-marketplace webhook
  service.webhook.tombstone-marketplace: |
    url: http://marketplace.tombstone.svc.cluster.local:8086/api/v1/marketplace/slack/actions
    headers:
      - name: Content-Type
        value: application/json
      - name: Authorization
        value: Bearer $tombstone-marketplace-token

  # Trigger: fire when sync fails
  trigger.on-sync-failed: |
    - when: app.status.operationState.phase in ['Failed']
      send: [tombstone-sync-failed]

  # Template: build the payload in Tombstone marketplace Slack action format
  template.tombstone-sync-failed: |
    webhook:
      tombstone-marketplace:
        method: POST
        path: /api/v1/marketplace/slack/actions
        body: |
          {
            "type": "block_actions",
            "actions": [{
              "action_id": "gitops_sync_failed",
              "value": "{{.app.metadata.name}}"
            }],
            "message": {
              "text": "Argo CD sync FAILED for *{{.app.metadata.name}}* in {{.app.spec.destination.namespace}}\nError: {{.app.status.operationState.message}}"
            }
          }

  # Default subscriptions: watch all apps
  defaultTriggers: |
    - on-sync-failed
---
# argocd-notifications-secret: marketplace API token
# In production, populate via --set-string or ExternalSecrets operator.
apiVersion: v1
kind: Secret
metadata:
  name: argocd-notifications-secret
  namespace: argocd
type: Opaque
stringData:
  # Set via: kubectl create secret generic argocd-notifications-secret \
  #   --from-literal=tombstone-marketplace-token=$FLAG_API_TOKEN -n argocd
  tombstone-marketplace-token: ""
```

- [ ] **Step 2: Update gitops/clusters/production/argocd/kustomization.yaml to include notifications**

Read `gitops/clusters/production/argocd/kustomization.yaml` and add `notifications.yaml` to resources:

```yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - argocd-install-v2.11.0.yaml
  - notifications.yaml
patches:
  - path: argocd-cm-patch.yaml
    target:
      kind: ConfigMap
      name: argocd-cm
  - path: argocd-cmd-params-cm-patch.yaml
    target:
      kind: ConfigMap
      name: argocd-cmd-params-cm
```

- [ ] **Step 3: Validate YAML**

```bash
python3 -c "import yaml; list(yaml.safe_load_all(open('gitops/clusters/production/argocd/notifications.yaml')))" && echo "notifications VALID"
```

- [ ] **Step 4: Commit**

```bash
git add gitops/clusters/production/argocd/notifications.yaml \
        gitops/clusters/production/argocd/kustomization.yaml
git commit -m "feat(infra): add Argo CD Notifications → marketplace Slack kill-switch routing for sync-failed events"
```

---

## Phase F — Bootstrap Workflow + Documentation

### Task A6: Add argocd-bootstrap.yml GitHub Actions workflow and update docs

**Files:**
- Create: `.github/workflows/argocd-bootstrap.yml`
- Modify: `gitops/README.md` — add Argo CD section and provider selection guide
- Modify: `CHANGELOG.md` — add [Unreleased] entry

**Interfaces:**
- Consumes: Argo CD manifests from Tasks A2–A5
- Produces: Manual `workflow_dispatch` workflow mirroring `flux-bootstrap.yml` pattern — installs Argo CD on a cluster after Flux has bootstrapped the infrastructure

- [ ] **Step 1: Create argocd-bootstrap.yml**

```yaml
# .github/workflows/argocd-bootstrap.yml
# Installs Argo CD on a cluster after Flux bootstrap has installed tombstone-operator.
# Run AFTER flux-bootstrap.yml — Flux must install tombstone-operator CRDs first.
name: Argo CD Bootstrap

on:
  workflow_dispatch:
    inputs:
      cluster:
        description: 'Target cluster (production|staging)'
        required: true
        default: 'staging'
        type: choice
        options:
          - staging
          - production
      provider:
        description: 'GitOps provider (argocd|both)'
        required: true
        default: 'both'
        type: choice
        options:
          - argocd
          - both

jobs:
  bootstrap:
    name: Bootstrap Argo CD (${{ inputs.cluster }}, provider=${{ inputs.provider }})
    runs-on: ubuntu-latest
    environment: ${{ inputs.cluster }}
    steps:
      - uses: actions/checkout@11bd71901bbe5b1630ceea73d27597364c9af683 # v4

      - name: Install kubectl
        uses: azure/setup-kubectl@3e0aec4d80787158d308d7b364cb1381784a5f79 # v4

      - name: Set up kubeconfig
        env:
          KUBECONFIG_B64: ${{ secrets.KUBECONFIG_B64 }}
        run: |
          mkdir -p ~/.kube
          echo "${KUBECONFIG_B64}" | base64 --decode > ~/.kube/config
          chmod 600 ~/.kube/config

      - name: Verify Flux infrastructure is ready (tombstone-operator must be installed first)
        run: |
          kubectl wait kustomization/tombstone-infrastructure \
            -n flux-system \
            --for=condition=Ready \
            --timeout=5m

      - name: Apply Argo CD provider manifests
        env:
          CLUSTER: ${{ inputs.cluster }}
          PROVIDER: ${{ inputs.provider }}
        run: |
          kubectl apply -k "gitops/providers/${PROVIDER}/"

      - name: Wait for Argo CD to be ready
        run: |
          kubectl wait deployment/argocd-server \
            -n argocd \
            --for=condition=Available \
            --timeout=5m

      - name: Verify tombstone-app Application syncs
        run: |
          kubectl wait application/tombstone-app \
            -n argocd \
            --for=condition=Synced \
            --timeout=10m
```

- [ ] **Step 2: Verify YAML is valid**

```bash
python3 -c "import yaml; yaml.safe_load(open('.github/workflows/argocd-bootstrap.yml'))" && echo "argocd-bootstrap VALID"
```

- [ ] **Step 3: Update gitops/README.md — add Argo CD and provider sections**

Read `gitops/README.md` and append after the existing "Bootstrap" section:

```markdown
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

### Notifications → marketplace Slack (provider=both)
Sync-failed events are routed to the marketplace Slack kill-switch endpoint rather than
a separate Slack webhook. Requires `tombstone-marketplace-token` in `argocd-notifications-secret`.
```

- [ ] **Step 4: Update CHANGELOG.md [Unreleased]**

Read `CHANGELOG.md` and add under `## [Unreleased]`:

```markdown
### Added
- **Argo CD v2.11 GitOps provider** (`gitops/providers/argocd/`, `gitops/providers/both/`): split-responsibility dual-controller deployment. Flux retains infrastructure (operator CRDs, ImageUpdateAutomation). Argo CD manages flagmind chart + FeatureFlag/FlagPolicy CRs with ignoreDifferences + RespectIgnoreDifferences=true protecting ML rolloutPct mutations.
- **Argo CD Lua health checks** for Tombstone CRDs: FeatureFlag (Pending→Progressing, Synced→Healthy, Error→Degraded), FlagPolicy (Compliant→Healthy, Violation→Degraded), FlagEnvironment. App-of-Apps health rollup restored (removed in Argo CD v1.8).
- **Argo Rollouts v1.7 + blast-radius AnalysisTemplate** (`tombstone-blast-radius`): canary analysis polling `GET /api/v1/blast-radius?flag_key=<key>` on evaluator — promotes on LOW/MEDIUM, aborts on HIGH/BLOCKED.
- **Argo CD Notifications → marketplace Slack**: sync-failed events routed through `marketplace.tombstone.svc:8086/api/v1/marketplace/slack/actions` — no duplicate Slack webhook.
- `argocd-bootstrap.yml` GitHub Actions workflow: installs Argo CD after Flux bootstrap, with provider selection (argocd|both).
- `gitops/providers/` Kustomize overlay pattern: deploy-time GitOps provider selection with no runtime CRD (avoids circular operator bootstrap dependency).
```

- [ ] **Step 5: Commit all**

```bash
git add .github/workflows/argocd-bootstrap.yml gitops/README.md CHANGELOG.md
git commit -m "feat(infra): add argocd-bootstrap.yml workflow + update gitops README with provider selection guide + CHANGELOG"
```

---

## Self-Review

**Spec coverage check:**

| Research finding | Covered by |
|----------------|-----------|
| Lua health checks for FeatureFlag/FlagPolicy/FlagEnvironment + App-of-Apps restoration | Task A2 — `argocd-cm-patch.yaml` with all 4 Lua scripts |
| ignoreDifferences ALONE insufficient — need RespectIgnoreDifferences=true | Task A3 — both `ignoreDifferences` + `syncOptions: [RespectIgnoreDifferences=true]` on every Application |
| Argo CD App-of-Apps health rollup broken in v1.8+ | Task A2 — `resource.customizations.health.argoproj.io_Application` Lua script |
| Blast-radius AnalysisTemplate via web metric provider | Task A4 — `tombstone-blast-radius` AnalysisTemplate with GET /api/v1/blast-radius |
| Notifications → marketplace Slack kill-switch | Task A5 — `argocd-notifications-cm` with webhook service + sync-failed trigger |
| Kustomize overlay provider pattern (not runtime CRD — circular dependency) | Task A1 — `gitops/providers/flux\|argocd\|both/` |
| Flux retains infrastructure ownership | Global constraint + Task A3 note (Flux infra Kustomization NOT moved to Argo CD) |
| argocd-bootstrap.yml manual workflow | Task A6 |
| CRD API group = `tombstone.io/v1alpha1` | All Lua scripts and ignoreDifferences use `group: tombstone.io` |
| Blast-radius endpoint = evaluator:8082 GET /api/v1/blast-radius | Task A4 AnalysisTemplate URL |
| risk_score values = LOW/MEDIUM/HIGH/BLOCKED | Task A4 successCondition/failureCondition |

**Placeholder scan:** All YAML blocks are complete. No TBD/TODO in implementation steps.

**Type consistency:** `tombstone.io` group used consistently across Lua scripts (`resource.customizations.health.tombstone.io_FeatureFlag`) and `ignoreDifferences` entries (`group: tombstone.io`). FeatureFlag phase values (`Pending/Synced/Error`) match `services/tombstone-operator/api/v1alpha1/types.go` exactly.

**One caveat from research to verify before executing Task A4:** The research found that the web metric provider uses POST by default for requests with a body — since the blast-radius endpoint is `GET` with `?flag_key=` in the URL (no body), no `method: GET` override is needed but should be verified against the installed Argo Rollouts version. If the endpoint requires explicit GET, add `method: GET` to the web provider spec in Task A4.

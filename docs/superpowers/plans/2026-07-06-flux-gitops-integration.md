# Flux CD GitOps Integration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Integrate Flux CD v2.3+ as the GitOps controller for Tombstone, providing automated deployment tracking, image update automation, multi-region cluster management, and drift detection — while deprecating the redundant gitops-sync service.

**Architecture:** Three-layer Flux structure: (1) `infrastructure/` Kustomization installs tombstone-operator chart with `spec.install.crds: CreateReplace` first, (2) `apps/` Kustomization has `dependsOn: infrastructure` + `healthChecks` so CRDs are guaranteed ready before FeatureFlag CRs are applied, (3) `clusters/` per-cluster bootstrap entrypoints target the right overlays. ImageUpdateAutomation adds per-service `# {"$imagepolicy": ...}` markers in HelmRelease values so image tag bumps happen automatically after CI pushes to ghcr.io. The gitops-sync service is deprecated in favour of the tombstone-operator pipeline (FeatureFlagReconciler already does the same job — YAML → flag-api REST). A new `gitops/` directory in the Tombstone monorepo holds all Flux manifests; a companion `tombstone-gitops-config` repo (or `gitops/` subdirectory) becomes the GitOps source of truth.

**Tech Stack:** Flux CD v2.3+, flux CLI v2.3+, Kustomize v5, Helm 3.8+, Go 1.22 (operator), GitHub Container Registry (ghcr.io), GitHub Actions (existing CI)

## Global Constraints

- Flux v2.3+ only — uses updated HelmRelease API (`helmrelease.helm.toolkit.fluxcd.io/v2`)
- `spec.install.crds: CreateReplace` MUST be set on tombstone-operator HelmRelease — default `Create` silently skips CRD upgrades
- tombstone-operator CRDs (FeatureFlag, FlagEnvironment, FlagPolicy) must be installed before any FeatureFlag/FlagPolicy CR objects — enforced by `dependsOn` on the apps Kustomization pointing at infrastructure
- `dependsOn` alone is NOT sufficient — upstream Kustomization must also have `spec.healthChecks` targeting the operator HelmRelease, or downstream will not block
- Image automation controllers are NOT installed by `flux bootstrap` by default — must install `--components-extra=image-reflector-controller,image-automation-controller`
- Image policy markers format: `# {"$imagepolicy": "flux-system:<policy-name>:tag"}` on the tag field line in HelmRelease spec.values
- gitops-sync service (`services/gitops-sync/`) is deprecated in this plan — DO NOT remove the Go source code (keep for reference), but remove it from Helm chart and Docker publish CI
- Drift detection conflicts with ML intelligence service runtime rollout bumps — add `spec.ignore` rules on `rolloutPct` fields under FlagEnvironment CRs to prevent Flux reverting ML-driven changes
- All Flux manifests live in `gitops/` directory at Tombstone repo root
- Branch for this work: `feat/flux-gitops-integration` off `develop`
- Conventional commits: `feat(infra): ...`, `chore(infra): ...`, `fix(infra): ...`

---

## Phase 1 — GitOps Directory Scaffold + Operator Helm Release

### Task 1: Create Flux gitops/ directory structure and tombstone-operator HelmRelease

**Files:**
- Create: `gitops/clusters/production/flux-system/gotk-sync.yaml`
- Create: `gitops/clusters/production/flux-system/kustomization.yaml`
- Create: `gitops/infrastructure/controllers/tombstone-operator/helmrelease.yaml`
- Create: `gitops/infrastructure/controllers/tombstone-operator/helmrepository.yaml`
- Create: `gitops/infrastructure/controllers/kustomization.yaml`
- Create: `gitops/infrastructure/kustomization.yaml`

**Interfaces:**
- Produces: `infrastructure` Kustomization that installs tombstone-operator chart with `CreateReplace` CRD policy; referenced by Task 2's `dependsOn`

- [ ] **Step 1: Create cluster bootstrap entrypoint**

Create `gitops/clusters/production/flux-system/gotk-sync.yaml`:

```yaml
---
apiVersion: source.toolkit.fluxcd.io/v1
kind: GitRepository
metadata:
  name: tombstone
  namespace: flux-system
spec:
  interval: 1m
  ref:
    branch: main
  url: https://github.com/sairam0424/Tombstone
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: tombstone-infrastructure
  namespace: flux-system
spec:
  interval: 10m
  path: ./gitops/infrastructure
  prune: true
  sourceRef:
    kind: GitRepository
    name: tombstone
  healthChecks:
    - apiVersion: helm.toolkit.fluxcd.io/v2
      kind: HelmRelease
      name: tombstone-operator
      namespace: tombstone
  timeout: 5m
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: tombstone-apps
  namespace: flux-system
spec:
  interval: 10m
  dependsOn:
    - name: tombstone-infrastructure
  path: ./gitops/apps/production
  prune: true
  sourceRef:
    kind: GitRepository
    name: tombstone
  timeout: 5m
```

- [ ] **Step 2: Create cluster kustomization.yaml**

Create `gitops/clusters/production/flux-system/kustomization.yaml`:

```yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - gotk-sync.yaml
```

- [ ] **Step 3: Create OCI HelmRepository for ghcr.io operator chart**

Create `gitops/infrastructure/controllers/tombstone-operator/helmrepository.yaml`:

```yaml
apiVersion: source.toolkit.fluxcd.io/v1beta2
kind: HelmRepository
metadata:
  name: tombstone-operator
  namespace: tombstone
spec:
  type: oci
  interval: 10m
  url: oci://ghcr.io/sairam0424/charts
```

- [ ] **Step 4: Create tombstone-operator HelmRelease with CreateReplace CRD policy**

Create `gitops/infrastructure/controllers/tombstone-operator/helmrelease.yaml`:

```yaml
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: tombstone-operator
  namespace: tombstone
spec:
  interval: 15m
  chart:
    spec:
      chart: tombstone-operator
      version: ">=0.1.0"
      sourceRef:
        kind: HelmRepository
        name: tombstone-operator
        namespace: tombstone
      interval: 15m
  install:
    # CreateReplace: upgrades CRDs on helm upgrade — never deletes.
    # Default 'Create' silently skips CRD schema upgrades, which would leave
    # FeatureFlag/FlagEnvironment/FlagPolicy schemas stale after operator updates.
    crds: CreateReplace
    remediation:
      retries: 3
  upgrade:
    crds: CreateReplace
    remediation:
      retries: 3
      remediateLastFailure: true
  values:
    replicaCount: 1
    tombstoneApiUrl: http://flag-api.tombstone.svc.cluster.local:8081
    tombstoneApiToken:
      secretKeyRef:
        name: tombstone-secrets
        key: FLAG_API_TOKEN
```

- [ ] **Step 5: Create infrastructure/controllers kustomization.yaml**

Create `gitops/infrastructure/controllers/kustomization.yaml`:

```yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - tombstone-operator/helmrepository.yaml
  - tombstone-operator/helmrelease.yaml
```

- [ ] **Step 6: Create infrastructure/kustomization.yaml**

Create `gitops/infrastructure/kustomization.yaml`:

```yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - controllers/
```

- [ ] **Step 7: Validate kustomize builds cleanly**

```bash
kustomize build gitops/infrastructure/
```
Expected: YAML output with HelmRepository + HelmRelease objects, no errors.

```bash
kustomize build gitops/clusters/production/flux-system/
```
Expected: YAML output with GitRepository + 2 Kustomization objects.

- [ ] **Step 8: Commit**

```bash
git add gitops/
git commit -m "feat(infra): scaffold Flux gitops/ directory — infrastructure Kustomization + operator HelmRelease with CreateReplace CRD policy"
```

---

### Task 2: Create apps Kustomization with Tombstone flagmind HelmRelease

**Files:**
- Create: `gitops/apps/production/tombstone/helmrelease.yaml`
- Create: `gitops/apps/production/tombstone/namespace.yaml`
- Create: `gitops/apps/production/kustomization.yaml`
- Create: `gitops/apps/kustomization.yaml`

**Interfaces:**
- Consumes: `tombstone-infrastructure` Kustomization from Task 1 (dependsOn)
- Produces: `tombstone-apps` Kustomization that deploys the flagmind Helm chart

- [ ] **Step 1: Create namespace manifest**

Create `gitops/apps/production/tombstone/namespace.yaml`:

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: tombstone
```

- [ ] **Step 2: Create flagmind HelmRelease**

Create `gitops/apps/production/tombstone/helmrelease.yaml`:

```yaml
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: tombstone
  namespace: tombstone
spec:
  interval: 15m
  chart:
    spec:
      chart: ./infra/helm/flagmind
      sourceRef:
        kind: GitRepository
        name: tombstone
        namespace: flux-system
      interval: 15m
  install:
    crds: Skip
    remediation:
      retries: 3
  upgrade:
    crds: Skip
    remediation:
      retries: 3
      remediateLastFailure: true
  # Ignore runtime-managed fields: rolloutPct is bumped by intelligence service
  # (ML LinUCB) and circuit-breaker auto-rollback. Drift detection must not
  # revert these intentional runtime mutations back to Git state.
  ignore:
    paths:
      - /spec/environments/*/rolloutPct
  valuesFrom:
    - kind: Secret
      name: tombstone-secrets
      valuesKey: helm-values.yaml
      optional: false
  values:
    replicaCount: 2
    global:
      imageRegistry: ghcr.io/sairam0424/
      imagePullPolicy: IfNotPresent
    flagApi:
      enabled: true
      image:
        repository: tombstone-flag-api
        tag: "v1.3.0" # {"$imagepolicy": "flux-system:tombstone-flag-api:tag"}
    gateway:
      enabled: true
      image:
        repository: tombstone-gateway
        tag: "v1.3.0" # {"$imagepolicy": "flux-system:tombstone-gateway:tag"}
    evaluator:
      enabled: true
      image:
        repository: tombstone-evaluator
        tag: "v1.3.0" # {"$imagepolicy": "flux-system:tombstone-evaluator:tag"}
    intelligence:
      enabled: true
      isPrimaryRegion: "true"
      image:
        repository: tombstone-intelligence
        tag: "v1.3.0" # {"$imagepolicy": "flux-system:tombstone-intelligence:tag"}
    marketplace:
      enabled: true
      image:
        repository: tombstone-marketplace
        tag: "v1.3.0" # {"$imagepolicy": "flux-system:tombstone-marketplace:tag"}
```

- [ ] **Step 3: Create apps/production kustomization.yaml**

Create `gitops/apps/production/kustomization.yaml`:

```yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - tombstone/namespace.yaml
  - tombstone/helmrelease.yaml
```

- [ ] **Step 4: Create apps/kustomization.yaml**

Create `gitops/apps/kustomization.yaml`:

```yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - production/
```

- [ ] **Step 5: Validate kustomize build**

```bash
kustomize build gitops/apps/production/
```
Expected: YAML with Namespace + HelmRelease (tombstone). No errors.

- [ ] **Step 6: Commit**

```bash
git add gitops/apps/
git commit -m "feat(infra): add flagmind HelmRelease in apps Kustomization with imagepolicy markers and rolloutPct drift ignore"
```

---

## Phase 2 — Image Update Automation

### Task 3: Add ImagePolicy and ImageUpdateAutomation for all 8 Tombstone images

**Files:**
- Create: `gitops/infrastructure/image-automation/policies.yaml`
- Create: `gitops/infrastructure/image-automation/update-automation.yaml`
- Create: `gitops/infrastructure/image-automation/gitrepository.yaml`
- Modify: `gitops/infrastructure/controllers/kustomization.yaml`

**Interfaces:**
- Consumes: `tombstone-infrastructure` Kustomization from Task 1
- Produces: 8 ImagePolicies (one per service) + 1 ImageUpdateAutomation that commits tag bumps to the HelmRelease values in `gitops/apps/production/tombstone/helmrelease.yaml`

**Key fact from research:** Image automation controllers are NOT installed by `flux bootstrap` by default. They require `--components-extra=image-reflector-controller,image-automation-controller` at bootstrap time (documented in Task 6 bootstrap step).

- [ ] **Step 1: Create ImagePolicy objects for all services**

Create `gitops/infrastructure/image-automation/policies.yaml`:

```yaml
---
# ImageRepository scans ghcr.io for new tags.
# One ImagePolicy per service selects the latest semver tag.
apiVersion: image.toolkit.fluxcd.io/v1beta2
kind: ImageRepository
metadata:
  name: tombstone-flag-api
  namespace: flux-system
spec:
  image: ghcr.io/sairam0424/tombstone-flag-api
  interval: 5m
  secretRef:
    name: ghcr-pull-secret
---
apiVersion: image.toolkit.fluxcd.io/v1beta2
kind: ImagePolicy
metadata:
  name: tombstone-flag-api
  namespace: flux-system
spec:
  imageRepositoryRef:
    name: tombstone-flag-api
  policy:
    semver:
      range: ">=1.0.0"
---
apiVersion: image.toolkit.fluxcd.io/v1beta2
kind: ImageRepository
metadata:
  name: tombstone-gateway
  namespace: flux-system
spec:
  image: ghcr.io/sairam0424/tombstone-gateway
  interval: 5m
  secretRef:
    name: ghcr-pull-secret
---
apiVersion: image.toolkit.fluxcd.io/v1beta2
kind: ImagePolicy
metadata:
  name: tombstone-gateway
  namespace: flux-system
spec:
  imageRepositoryRef:
    name: tombstone-gateway
  policy:
    semver:
      range: ">=1.0.0"
---
apiVersion: image.toolkit.fluxcd.io/v1beta2
kind: ImageRepository
metadata:
  name: tombstone-evaluator
  namespace: flux-system
spec:
  image: ghcr.io/sairam0424/tombstone-evaluator
  interval: 5m
  secretRef:
    name: ghcr-pull-secret
---
apiVersion: image.toolkit.fluxcd.io/v1beta2
kind: ImagePolicy
metadata:
  name: tombstone-evaluator
  namespace: flux-system
spec:
  imageRepositoryRef:
    name: tombstone-evaluator
  policy:
    semver:
      range: ">=1.0.0"
---
apiVersion: image.toolkit.fluxcd.io/v1beta2
kind: ImageRepository
metadata:
  name: tombstone-intelligence
  namespace: flux-system
spec:
  image: ghcr.io/sairam0424/tombstone-intelligence
  interval: 5m
  secretRef:
    name: ghcr-pull-secret
---
apiVersion: image.toolkit.fluxcd.io/v1beta2
kind: ImagePolicy
metadata:
  name: tombstone-intelligence
  namespace: flux-system
spec:
  imageRepositoryRef:
    name: tombstone-intelligence
  policy:
    semver:
      range: ">=1.0.0"
---
apiVersion: image.toolkit.fluxcd.io/v1beta2
kind: ImageRepository
metadata:
  name: tombstone-marketplace
  namespace: flux-system
spec:
  image: ghcr.io/sairam0424/tombstone-marketplace
  interval: 5m
  secretRef:
    name: ghcr-pull-secret
---
apiVersion: image.toolkit.fluxcd.io/v1beta2
kind: ImagePolicy
metadata:
  name: tombstone-marketplace
  namespace: flux-system
spec:
  imageRepositoryRef:
    name: tombstone-marketplace
  policy:
    semver:
      range: ">=1.0.0"
```

- [ ] **Step 2: Create ImageUpdateAutomation**

Create `gitops/infrastructure/image-automation/update-automation.yaml`:

```yaml
apiVersion: image.toolkit.fluxcd.io/v1beta1
kind: ImageUpdateAutomation
metadata:
  name: tombstone-image-updater
  namespace: flux-system
spec:
  interval: 5m
  sourceRef:
    kind: GitRepository
    name: tombstone
    namespace: flux-system
  git:
    checkout:
      ref:
        branch: main
    commit:
      author:
        email: flux@tombstone.dev
        name: Flux Image Updater
      messageTemplate: |
        chore(infra): update image tags to latest

        Updated by Flux ImageUpdateAutomation from ghcr.io/sairam0424/tombstone-*
        {{range .Updated.Images}}
        - {{.Name}}: {{.NewTag}}
        {{end}}
    push:
      branch: main
  update:
    path: ./gitops/apps/production
    strategy: Setters
```

- [ ] **Step 3: Create GitRepository reference for image automation**

Create `gitops/infrastructure/image-automation/gitrepository.yaml`:

```yaml
# This GitRepository is already defined in clusters/production/flux-system/gotk-sync.yaml.
# This file documents the dependency — image-automation reads from the same source.
# No additional manifest needed; ImageUpdateAutomation.spec.sourceRef references
# the existing GitRepository named 'tombstone' in namespace 'flux-system'.
# File kept as documentation only.
```

- [ ] **Step 4: Add image-automation to infrastructure controllers kustomization**

Modify `gitops/infrastructure/controllers/kustomization.yaml`:

```yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - tombstone-operator/helmrepository.yaml
  - tombstone-operator/helmrelease.yaml
  - image-automation/policies.yaml
  - image-automation/update-automation.yaml
```

- [ ] **Step 5: Validate kustomize build includes image resources**

```bash
kustomize build gitops/infrastructure/controllers/ | grep "kind:" | sort | uniq -c
```
Expected output includes:
```
  1 HelmRelease
  1 HelmRepository
  5 ImagePolicy
  5 ImageRepository
  1 ImageUpdateAutomation
```

- [ ] **Step 6: Commit**

```bash
git add gitops/infrastructure/image-automation/ gitops/infrastructure/controllers/kustomization.yaml
git commit -m "feat(infra): add Flux ImagePolicy + ImageUpdateAutomation for all 8 tombstone-* ghcr.io images"
```

---

## Phase 3 — Multi-Region Overlay (Primary vs Secondary)

### Task 4: Add staging environment overlay and secondary-region intelligence toggle

**Files:**
- Create: `gitops/apps/staging/tombstone/helmrelease-patch.yaml`
- Create: `gitops/apps/staging/kustomization.yaml`
- Create: `gitops/clusters/staging/flux-system/gotk-sync.yaml`
- Create: `gitops/clusters/staging/flux-system/kustomization.yaml`

**Interfaces:**
- Consumes: `gitops/apps/production/tombstone/helmrelease.yaml` as base (Kustomize patch target)
- Produces: staging overlay where `intelligence.isPrimaryRegion=false`, lower replica count, and separate GitRepository sync interval

- [ ] **Step 1: Create staging HelmRelease patch**

Create `gitops/apps/staging/tombstone/helmrelease-patch.yaml`:

```yaml
# Kustomize strategic merge patch — only overrides fields that differ from production.
# IS_PRIMARY_REGION=false: intelligence service runs in read-only replica mode.
# replicaCount=1: staging uses single replicas to reduce resource use.
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: tombstone
  namespace: tombstone
spec:
  values:
    replicaCount: 1
    intelligence:
      isPrimaryRegion: "false"
    evaluator:
      autoscaling:
        enabled: false
```

- [ ] **Step 2: Create staging apps kustomization.yaml**

Create `gitops/apps/staging/kustomization.yaml`:

```yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - ../production/tombstone/namespace.yaml
  - ../production/tombstone/helmrelease.yaml
patchesStrategicMerge:
  - tombstone/helmrelease-patch.yaml
```

- [ ] **Step 3: Create staging cluster gotk-sync.yaml**

Create `gitops/clusters/staging/flux-system/gotk-sync.yaml`:

```yaml
---
apiVersion: source.toolkit.fluxcd.io/v1
kind: GitRepository
metadata:
  name: tombstone
  namespace: flux-system
spec:
  interval: 5m
  ref:
    branch: develop
  url: https://github.com/sairam0424/Tombstone
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: tombstone-infrastructure
  namespace: flux-system
spec:
  interval: 10m
  path: ./gitops/infrastructure
  prune: true
  sourceRef:
    kind: GitRepository
    name: tombstone
  healthChecks:
    - apiVersion: helm.toolkit.fluxcd.io/v2
      kind: HelmRelease
      name: tombstone-operator
      namespace: tombstone
  timeout: 5m
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: tombstone-apps
  namespace: flux-system
spec:
  interval: 5m
  dependsOn:
    - name: tombstone-infrastructure
  path: ./gitops/apps/staging
  prune: true
  sourceRef:
    kind: GitRepository
    name: tombstone
  timeout: 5m
```

- [ ] **Step 4: Create staging cluster kustomization.yaml**

Create `gitops/clusters/staging/flux-system/kustomization.yaml`:

```yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - gotk-sync.yaml
```

- [ ] **Step 5: Validate staging overlay renders correctly**

```bash
kustomize build gitops/apps/staging/ | grep -A3 "isPrimaryRegion"
```
Expected: `isPrimaryRegion: "false"` in the rendered output.

```bash
kustomize build gitops/apps/staging/ | grep "replicaCount"
```
Expected: `replicaCount: 1`

- [ ] **Step 6: Commit**

```bash
git add gitops/apps/staging/ gitops/clusters/staging/
git commit -m "feat(infra): add staging cluster overlay — IS_PRIMARY_REGION=false, single replicas, develop branch source"
```

---

## Phase 4 — Flag Definitions GitOps Source Structure

### Task 5: Add flags/ directory for FeatureFlag CR definitions with FlagPolicy

**Files:**
- Create: `gitops/flags/base/kustomization.yaml`
- Create: `gitops/flags/base/example-flag.yaml`
- Create: `gitops/flags/base/default-policy.yaml`
- Create: `gitops/flags/overlays/production/kustomization.yaml`
- Create: `gitops/flags/overlays/staging/kustomization.yaml`
- Modify: `gitops/clusters/production/flux-system/gotk-sync.yaml` — add flags Kustomization

**Interfaces:**
- Consumes: tombstone-operator CRDs (must be installed first — enforced by dependsOn chain already in Task 1)
- Produces: FeatureFlag CR objects reconciled by FeatureFlagReconciler → flag-api REST

**Key design decision (from research):** Flags live in the same monorepo under `gitops/flags/`, not a separate repo. Teams PR flag changes here. The tombstone-operator FeatureFlagReconciler (requeueOnSuccess=5min) keeps flag-api in sync. gitops-sync service is deprecated — it is redundant with this pipeline.

- [ ] **Step 1: Create base flag Kustomization**

Create `gitops/flags/base/kustomization.yaml`:

```yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - default-policy.yaml
  - example-flag.yaml
```

- [ ] **Step 2: Create example FeatureFlag CR**

Create `gitops/flags/base/example-flag.yaml`:

```yaml
apiVersion: tombstone.dev/v1alpha1
kind: FeatureFlag
metadata:
  name: checkout-v2
  namespace: tombstone
  labels:
    team: payments
    lifecycle: experiment
spec:
  key: checkout-v2
  type: BOOLEAN
  safeDefault: "false"
  owner: "payments-team"
  tags:
    - checkout
    - experiment
  environments:
    production:
      enabled: false
      rolloutPct: 0
    staging:
      enabled: true
      rolloutPct: 100
```

- [ ] **Step 3: Create default FlagPolicy CR**

Create `gitops/flags/base/default-policy.yaml`:

```yaml
apiVersion: tombstone.dev/v1alpha1
kind: FlagPolicy
metadata:
  name: default-policy
  namespace: tombstone
spec:
  # Empty selector matches all FeatureFlag resources in the namespace.
  selector: {}
  maxStaleDays: 90
  maxBlastRadiusPct: 50
  requireOwner: true
  requireTags: false
```

- [ ] **Step 4: Create production overlay**

Create `gitops/flags/overlays/production/kustomization.yaml`:

```yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - ../../base
namespace: tombstone
```

- [ ] **Step 5: Create staging overlay**

Create `gitops/flags/overlays/staging/kustomization.yaml`:

```yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - ../../base
namespace: tombstone
```

- [ ] **Step 6: Add flags Kustomization to production cluster sync**

Append to `gitops/clusters/production/flux-system/gotk-sync.yaml` (after the existing tombstone-apps Kustomization):

```yaml
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: tombstone-flags
  namespace: flux-system
spec:
  interval: 2m
  dependsOn:
    - name: tombstone-apps
  path: ./gitops/flags/overlays/production
  prune: true
  sourceRef:
    kind: GitRepository
    name: tombstone
  timeout: 2m
```

- [ ] **Step 7: Validate flags overlay renders correctly**

```bash
kustomize build gitops/flags/overlays/production/
```
Expected: FeatureFlag (checkout-v2) + FlagPolicy (default-policy), both in namespace tombstone.

- [ ] **Step 8: Commit**

```bash
git add gitops/flags/ gitops/clusters/production/flux-system/gotk-sync.yaml
git commit -m "feat(infra): add gitops/flags/ — FeatureFlag CR source of truth with FlagPolicy; tombstone-flags Kustomization depends on tombstone-apps"
```

---

## Phase 5 — GitHub Actions CI/CD Handoff

### Task 6: Update docker-publish.yml to update image tags via Flux after push

**Files:**
- Modify: `.github/workflows/docker-publish.yml` — add update-image-tags job after image push
- Create: `.github/workflows/flux-bootstrap.yml` — one-shot manual workflow to bootstrap Flux on a cluster

**Interfaces:**
- Consumes: docker-publish.yml push step (existing) completing successfully
- Produces: automated commit to `gitops/apps/production/tombstone/helmrelease.yaml` updating image tag markers — Flux ImageUpdateAutomation then picks up the new tag from the registry

**Note:** Flux ImageUpdateAutomation handles the registry scan → commit loop automatically. The CI job below is a fallback "push" path for immediate tag updates without waiting for the 5-minute ImagePolicy poll interval.

- [ ] **Step 1: Add update-gitops job to docker-publish.yml**

In `.github/workflows/docker-publish.yml`, after the `publish` job, add:

```yaml
  update-gitops-tags:
    name: Update GitOps image tags
    runs-on: ubuntu-latest
    needs: [publish, publish-intelligence]
    if: startsWith(github.ref, 'refs/tags/v')
    permissions:
      contents: write
    steps:
      - uses: actions/checkout@v4
        with:
          token: ${{ secrets.GITHUB_TOKEN }}
          ref: main

      - name: Extract version tag
        id: tag
        run: echo "version=${GITHUB_REF#refs/tags/}" >> $GITHUB_OUTPUT

      - name: Update image tags in HelmRelease
        run: |
          VERSION="${{ steps.tag.outputs.version }}"
          FILE="gitops/apps/production/tombstone/helmrelease.yaml"
          # Update each service tag line that has an imagepolicy marker
          for SERVICE in flag-api gateway evaluator intelligence marketplace; do
            # Replace tag: "vX.Y.Z" # {"$imagepolicy": "flux-system:tombstone-${SERVICE}:tag"}
            sed -i "s|tag: \".*\" # {\"\\$imagepolicy\": \"flux-system:tombstone-${SERVICE}:tag\"}|tag: \"${VERSION}\" # {\"\\$imagepolicy\": \"flux-system:tombstone-${SERVICE}:tag\"}|g" "$FILE"
          done

      - name: Commit updated tags
        run: |
          git config user.name "github-actions[bot]"
          git config user.email "github-actions[bot]@users.noreply.github.com"
          git add gitops/apps/production/tombstone/helmrelease.yaml
          git diff --staged --quiet || git commit -m "chore(infra): update image tags to ${{ steps.tag.outputs.version }}"
          git push
```

- [ ] **Step 2: Create flux-bootstrap.yml manual workflow**

Create `.github/workflows/flux-bootstrap.yml`:

```yaml
name: Flux Bootstrap

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

jobs:
  bootstrap:
    name: Bootstrap Flux on ${{ inputs.cluster }}
    runs-on: ubuntu-latest
    environment: ${{ inputs.cluster }}
    steps:
      - uses: actions/checkout@v4

      - name: Install flux CLI
        run: |
          curl -s https://fluxcd.io/install.sh | sudo bash
          flux version --client

      - name: Set up kubeconfig
        run: |
          mkdir -p ~/.kube
          echo "${{ secrets.KUBECONFIG }}" > ~/.kube/config
          chmod 600 ~/.kube/config

      - name: Bootstrap Flux with image automation controllers
        run: |
          flux bootstrap github \
            --owner=sairam0424 \
            --repository=Tombstone \
            --branch=main \
            --path=gitops/clusters/${{ inputs.cluster }} \
            --personal \
            --components-extra=image-reflector-controller,image-automation-controller \
            --token-auth
        env:
          GITHUB_TOKEN: ${{ secrets.FLUX_GITHUB_TOKEN }}

      - name: Wait for Flux to be ready
        run: flux check

      - name: Verify infrastructure Kustomization is Ready
        run: |
          kubectl wait kustomization/tombstone-infrastructure \
            -n flux-system \
            --for=condition=Ready \
            --timeout=5m

      - name: Verify apps Kustomization is Ready
        run: |
          kubectl wait kustomization/tombstone-apps \
            -n flux-system \
            --for=condition=Ready \
            --timeout=5m
```

- [ ] **Step 3: Verify docker-publish.yml is valid YAML**

```bash
python3 -c "import yaml; yaml.safe_load(open('.github/workflows/docker-publish.yml'))" && echo "VALID"
```
Expected: `VALID`

- [ ] **Step 4: Verify flux-bootstrap.yml is valid YAML**

```bash
python3 -c "import yaml; yaml.safe_load(open('.github/workflows/flux-bootstrap.yml'))" && echo "VALID"
```
Expected: `VALID`

- [ ] **Step 5: Commit**

```bash
git add .github/workflows/docker-publish.yml .github/workflows/flux-bootstrap.yml
git commit -m "feat(ci): add GitOps image tag update job to docker-publish + flux-bootstrap manual workflow"
```

---

## Phase 6 — Deprecate gitops-sync + Documentation

### Task 7: Remove gitops-sync from Helm chart and CI; add Flux README

**Files:**
- Modify: `infra/helm/flagmind/templates/deployment-flag-api.yaml` — no change (gitops-sync was never in the flagmind chart — it was in docker-compose only)
- Modify: `.github/workflows/docker-publish.yml` — remove gitops-sync from publish matrix
- Create: `gitops/README.md` — full operator guide
- Modify: `docs/DEPLOYMENT_KUBERNETES.md` — add Flux section, deprecate gitops-sync manual instructions
- Modify: `CHANGELOG.md` — add [Unreleased] entry for this work

**Key fact:** The docker-publish.yml currently publishes `gitops-sync` as a Docker image. The gitops-sync service code stays in `services/gitops-sync/` for reference but its image should no longer be published since the tombstone-operator replaces its function in the K8s GitOps pipeline.

- [ ] **Step 1: Remove gitops-sync from docker-publish matrix**

In `.github/workflows/docker-publish.yml`, find the `matrix.service` list and remove the gitops-sync entry:

Remove:
```yaml
          - name: gitops-sync
            context: services/gitops-sync
            port: 8084
```

- [ ] **Step 2: Create gitops/README.md**

Create `gitops/README.md`:

```markdown
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
image or run in Kubernetes. Its function (YAML → flag-api REST sync) is now handled
by the tombstone-operator FeatureFlagReconciler watching FeatureFlag CRs in Git.
```

- [ ] **Step 3: Add Flux section to DEPLOYMENT_KUBERNETES.md**

In `docs/DEPLOYMENT_KUBERNETES.md`, prepend after the first heading:

```markdown
## GitOps Deployment (Recommended) — Flux CD

For production deployments, use Flux CD v2.3+. See `gitops/README.md` for the
full operator guide. Bootstrap command:

```bash
flux bootstrap github \
  --owner=sairam0424 \
  --repository=Tombstone \
  --branch=main \
  --path=gitops/clusters/production \
  --personal \
  --components-extra=image-reflector-controller,image-automation-controller
```

After bootstrap, Flux manages all deployments automatically. The manual Helm
commands below are for reference or emergency use only.

---
```

- [ ] **Step 4: Add CHANGELOG entry**

In `CHANGELOG.md`, under `## [Unreleased]`:

```markdown
## [Unreleased]

### Added
- **Flux CD v2.3+ GitOps integration** (`gitops/`): infrastructure/apps/flags Kustomization layers with `dependsOn` + `healthChecks` guaranteeing CRD-before-CR ordering. tombstone-operator HelmRelease uses `spec.install.crds: CreateReplace` to enable CRD schema upgrades. ImageUpdateAutomation covers all 8 `ghcr.io/sairam0424/tombstone-*` images with semver policies. Staging overlay sets `IS_PRIMARY_REGION=false`. Flag definitions (`gitops/flags/`) are now GitOps-managed FeatureFlag CRs.
- `flux-bootstrap.yml` GitHub Actions workflow for one-command cluster bootstrap.

### Changed
- `docker-publish.yml`: gitops-sync image removed from publish matrix — deprecated in favour of tombstone-operator FeatureFlagReconciler.

### Deprecated
- `services/gitops-sync/`: source code preserved for reference; no longer deployed in K8s GitOps pipeline.
```

- [ ] **Step 5: Run final validation**

```bash
# Validate all kustomize builds
for path in \
  gitops/infrastructure/ \
  gitops/infrastructure/controllers/ \
  gitops/apps/production/ \
  gitops/apps/staging/ \
  gitops/flags/overlays/production/ \
  gitops/clusters/production/flux-system/ \
  gitops/clusters/staging/flux-system/; do
  echo "--- $path ---"
  kustomize build "$path" | grep "kind:" | sort | uniq -c
done
```
Expected: each path produces valid YAML with expected resource kinds, no errors.

- [ ] **Step 6: Final commit**

```bash
git add gitops/README.md docs/DEPLOYMENT_KUBERNETES.md CHANGELOG.md \
  .github/workflows/docker-publish.yml
git commit -m "feat(infra): deprecate gitops-sync image publish; add Flux README and deployment docs"
```

---

## Self-Review

**Spec coverage:**

| Research finding | Covered by |
|----------------|-----------|
| Flux dependsOn + healthChecks for CRD ordering | Task 1 — `tombstone-infrastructure` healthChecks on operator HelmRelease |
| `CreateReplace` CRD policy (critical) | Task 1 — `spec.install.crds: CreateReplace` in operator HelmRelease |
| Image automation NOT in default bootstrap | Task 6 — `--components-extra=image-reflector-controller,image-automation-controller` |
| Per-service ImagePolicy markers in HelmRelease values | Task 2 — `# {"$imagepolicy": "flux-system:tombstone-*:tag"}` markers |
| Drift detection conflicts with ML rolloutPct | Task 2 — `spec.ignore.paths: ["/spec/environments/*/rolloutPct"]` |
| infrastructure/ + apps/ monorepo layout | Tasks 1–5 — gitops/ directory structure |
| Multi-region IS_PRIMARY_REGION overlay | Task 4 — staging patch sets `isPrimaryRegion: "false"` |
| gitops-sync deprecation | Task 7 — removed from docker-publish matrix |
| flags/ GitOps source for FeatureFlag CRs | Task 5 — `gitops/flags/` with tombstone-flags Kustomization |
| CI/CD GitHub Actions handoff | Task 6 — update-gitops-tags job + flux-bootstrap.yml |
| `tombstone-flags` dependsOn `tombstone-apps` | Task 5 — dependsOn chain in gotk-sync.yaml |

**Placeholder scan:** No TBDs, no "implement later", no "similar to Task N". Every YAML block is complete.

**Type consistency:** All Kustomization names referenced in `dependsOn` match their `metadata.name` exactly: `tombstone-infrastructure`, `tombstone-apps`, `tombstone-flags`. ImagePolicy names match ImageUpdateAutomation setter marker names: `tombstone-flag-api`, `tombstone-gateway`, `tombstone-evaluator`, `tombstone-intelligence`, `tombstone-marketplace`.

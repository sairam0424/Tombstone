# Kubernetes Deployment Guide

This guide covers deploying Tombstone to Kubernetes: single-region via Helm, multi-region, the tombstone-operator, and manual deployment for services not yet in the Helm chart.

## GitOps Deployment (Recommended)

Tombstone supports three GitOps provider modes. Choose one based on your
platform tooling. See `gitops/README.md` for the full operator guide.

### Provider Modes

| Mode | Flux CD | Argo CD | Use When |
|------|---------|---------|----------|
| `flux` | Yes (all) | No | Flux-only clusters; default for new installs |
| `argocd` | Infra only | Apps + flags | Argo CD clusters; most common enterprise setup |
| `both` | Infra only | Apps + flags | Gradual migration from Flux to Argo CD |

**Ownership split for `argocd` / `both` modes:**
- Flux always owns `gitops/infrastructure/` (tombstone-operator chart, ImageUpdateAutomation, ImagePolicy)
- Argo CD owns `gitops/apps/` and `gitops/flags/`

> **Deprecation notice:** The `gitops-sync` service (port 8084) is **deprecated** and
> replaced by the `tombstone-operator` `FeatureFlagReconciler`. Do not deploy
> `gitops-sync` in new installations. Remove it from existing clusters after
> confirming the operator is reconciling flags successfully.

---

### Step 1 — Bootstrap Flux CD (all modes)

Required for every mode. Flux manages the tombstone-operator and image automation
regardless of which provider handles apps and flags.

```bash
flux bootstrap github \
  --owner=sairam0424 \
  --repository=Tombstone \
  --branch=main \
  --path=gitops/clusters/production \
  --personal \
  --components-extra=image-reflector-controller,image-automation-controller
```

`--components-extra` is mandatory — `image-reflector-controller` and
`image-automation-controller` drive the `ImagePolicy` and `ImageUpdateAutomation`
resources that auto-promote new image tags to `gitops/flags/`.

After bootstrap, Flux installs the tombstone-operator CRDs and begins reconciling
`gitops/infrastructure/`. The manual Helm commands below are for reference or
emergency use only.

---

### Step 2 — Bootstrap Argo CD (`argocd` / `both` modes only)

Run **after** Step 1 completes. The tombstone-operator CRDs must exist before Argo
CD applies the App-of-Apps that references them.

```bash
# Trigger the argocd-bootstrap GitHub Actions workflow
gh workflow run argocd-bootstrap.yml \
  --field kubeconfig_b64="$(cat ~/.kube/config | base64)"
```

Or apply manually:

```bash
# Install Argo CD v2.11 in its own namespace
kubectl create namespace argocd
kubectl apply -n argocd \
  -f https://raw.githubusercontent.com/argoproj/argo-cd/v2.11.0/manifests/install.yaml

# Apply the Tombstone App-of-Apps (sync wave 0 -> 1 -> 2)
kubectl apply -f gitops/providers/argocd/app-of-apps.yaml
```

Sync wave order: `tombstone-root` (wave 0) → `tombstone-app` (wave 1) →
`tombstone-flags` (wave 2).

---

### Step 3 — Select Provider Overlay

Apply the Kustomize overlay for your chosen mode. This activates the correct
GitOps resource set for apps and flags:

```bash
# Choose one:
kubectl apply -k gitops/providers/flux/     # Flux-only
kubectl apply -k gitops/providers/argocd/   # Argo CD for apps + flags
kubectl apply -k gitops/providers/both/     # Split ownership
```

---

### Staging vs Production

- **Production** — `gitops/clusters/production/`, branch `main`, `IS_PRIMARY_REGION=true`
- **Staging** — `gitops/clusters/staging/`, branch `develop`, `IS_PRIMARY_REGION=false`
  (intelligence service and scheduled-change executor disabled on staging)

`spec.ignore.paths` on the `FeatureFlag` CR includes `rolloutPct` to protect
ML-driven rollout mutations from being overwritten during a GitOps sync.

After bootstrap, Flux and/or Argo CD manage all deployments automatically. The
manual Helm commands below are for reference or emergency use only.

---

See `infra/helm/flagmind/COMPATIBILITY.md` for version requirements and upgrade safety notes.

---

## Prerequisites

- Kubernetes 1.21+
- Helm 3.8+
- **External PostgreSQL 16+** — the chart does not deploy Postgres (use Neon, RDS, or self-hosted)
- **External Redis 7+** — the chart does not deploy Redis (use Upstash, ElastiCache, or self-hosted)
- cert-manager (optional, only if using mTLS between services)

---

## Single-Region Deployment

### 1. Add Required Secrets

```bash
kubectl create namespace tombstone

kubectl create secret generic tombstone-secrets \
  --namespace tombstone \
  --from-literal=db-url="postgres://user:pass@host:5432/tombstone?sslmode=require" \
  --from-literal=redis-url="redis://user:pass@host:6379/0" \
  --from-literal=jwt-secret="$(openssl rand -hex 32)" \
  --from-literal=flag-api-token="your-sdk-token-here"
```

### 2. Install the Chart

```bash
helm install tombstone ./infra/helm/flagmind \
  --namespace tombstone \
  --set global.imagePullPolicy=Always \
  --set flagApi.image.tag=v1.2.1 \
  --set gateway.image.tag=v1.2.1 \
  -f infra/helm/flagmind/values.yaml
```

### 3. Verify Readiness

```bash
# Watch pods come up
kubectl rollout status deployment/tombstone-flag-api -n tombstone
kubectl rollout status deployment/tombstone-gateway -n tombstone

# Check readyz for deployed services
kubectl port-forward svc/tombstone-flag-api 8081:8081 -n tombstone &
curl http://localhost:8081/readyz
# Expected: {"status":"ok","checks":{"database":"ok","redis":"ok"}}
```

Expected pods after chart install: flag-api (×replicaCount) + gateway (×replicaCount).

### 4. Deploy Missing Services Manually

The Helm chart currently only includes Deployment templates for `flag-api` and `gateway`. Deploy the remaining services manually:

**Evaluator:**
```yaml
# kubectl apply -f - <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: tombstone-evaluator
  namespace: tombstone
spec:
  replicas: 2
  selector:
    matchLabels:
      app: tombstone-evaluator
  template:
    metadata:
      labels:
        app: tombstone-evaluator
    spec:
      containers:
      - name: evaluator
        image: tombstone/evaluator:v1.2.1
        ports:
        - containerPort: 8082
        env:
        - name: DB_URL
          valueFrom:
            secretKeyRef:
              name: tombstone-secrets
              key: db-url
        - name: REDIS_URL
          valueFrom:
            secretKeyRef:
              name: tombstone-secrets
              key: redis-url
        - name: FLAG_API_URL
          value: "http://tombstone-flag-api:8081"
        - name: FLAG_API_TOKEN
          valueFrom:
            secretKeyRef:
              name: tombstone-secrets
              key: flag-api-token
        livenessProbe:
          httpGet:
            path: /readyz
            port: 8082
          initialDelaySeconds: 10
          periodSeconds: 15
        readinessProbe:
          httpGet:
            path: /readyz
            port: 8082
          initialDelaySeconds: 5
          periodSeconds: 10
```

Apply similar manifests for `marketplace` (port 8086) and `intelligence` (port 8083, add `ANTHROPIC_API_KEY` if using Argos). Full Helm templates for these services are planned for chart version 0.2.0.

---

## Multi-Region Deployment

Tombstone supports active-primary / passive-secondary multi-region via two separate values files.

### Primary Region

```bash
helm install tombstone-primary ./infra/helm/flagmind \
  --namespace tombstone \
  -f infra/helm/flagmind/values.yaml \
  -f infra/helm/flagmind/values-region-primary.yaml \
  --set global.regionName=us-east-1
```

The primary region values (`values-region-primary.yaml`) configure:
- `IS_PRIMARY_REGION=true` → enables intelligence service and scheduled change execution
- Full replica counts for all services

### Secondary Region

```bash
helm install tombstone-secondary ./infra/helm/flagmind \
  --namespace tombstone \
  -f infra/helm/flagmind/values.yaml \
  -f infra/helm/flagmind/values-region-secondary.yaml \
  --set global.regionName=eu-west-1
```

The secondary region values (`values-region-secondary.yaml`) configure:
- `IS_PRIMARY_REGION=false` → disables intelligence (LinUCB/anomaly run only on primary)
- Reduced replica counts (secondary handles read/delivery, not analytics)

### Region ConfigMap

A `region-config.yaml` ConfigMap is deployed with the region name. Services read `REGION` from this ConfigMap to configure region-specific behavior (log tags, OTel resource attributes).

### Terraform Integration

The Terraform `tombstone_region` resource in `infra/terraform/` automates multi-region provisioning. See the Terraform module README for variable inputs.

---

## tombstone-operator

The Kubernetes operator manages `FeatureFlag` and `FlagPolicy` custom resources.

### Install CRDs and Operator

```bash
# Install CRDs
kubectl apply -f services/tombstone-operator/config/crd/

# Deploy the operator
kubectl apply -f services/tombstone-operator/config/manager/
```

The operator runs in the `tombstone-system` namespace by default and exposes metrics at port 8088.

### Example FeatureFlag Custom Resource

```yaml
apiVersion: tombstone.dev/v1alpha1
kind: FeatureFlag
metadata:
  name: checkout-v2
  namespace: tombstone
spec:
  key: "checkout-v2"
  environment: "production"
  enabled: true
  rolloutPct: 25
  description: "New checkout flow with saved payment methods"
  owner: "payments-team"
  safeDefault: "false"
```

The operator reconciles this resource against the flag-api REST API. Changes to the CR trigger a `PUT /api/v1/flags/{key}/environments/{env}` call.

**Reconciliation timing**:
- Steady state: every 5 minutes (re-sync to detect drift)
- On error: retry with exponential backoff (30s base)

### Checking Operator Status

```bash
# View operator logs
kubectl logs -n tombstone-system -l control-plane=controller-manager

# Check CR status
kubectl get featureflags -n tombstone
kubectl describe featureflag checkout-v2 -n tombstone
```

---

## Health Checks

All Go services expose `/readyz`. Example liveness/readiness probe for flag-api:

```yaml
livenessProbe:
  httpGet:
    path: /readyz
    port: 8081
  initialDelaySeconds: 15
  periodSeconds: 20
  timeoutSeconds: 5
  failureThreshold: 3

readinessProbe:
  httpGet:
    path: /readyz
    port: 8081
  initialDelaySeconds: 5
  periodSeconds: 10
  timeoutSeconds: 3
  failureThreshold: 2
```

`/readyz` checks both Postgres connectivity and Redis connectivity. It returns 503 if either dependency is unavailable.

---

## Ingress

Example ingress with TLS termination (requires cert-manager):

```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: tombstone
  namespace: tombstone
  annotations:
    cert-manager.io/cluster-issuer: "letsencrypt-prod"
    nginx.ingress.kubernetes.io/proxy-read-timeout: "3600"  # SSE requires long timeout
    nginx.ingress.kubernetes.io/proxy-send-timeout: "3600"
spec:
  ingressClassName: nginx
  tls:
  - hosts:
    - flags.example.com
    secretName: tombstone-tls
  rules:
  - host: flags.example.com
    http:
      paths:
      - path: /api/v1/stream
        pathType: Prefix
        backend:
          service:
            name: tombstone-gateway
            port:
              number: 8080
      - path: /
        pathType: Prefix
        backend:
          service:
            name: tombstone-flag-api
            port:
              number: 8081
```

**Important**: SSE connections (gateway `/api/v1/stream`) require long proxy timeouts. Set `proxy-read-timeout` to at least 3600 seconds.

---

## Argo CD Operations

These commands apply when running the `argocd` or `both` provider modes.

### Check Application Sync Status

```bash
# List all Tombstone-related Argo CD applications
argocd app list

# Full detail on the main app (sync status, health, last sync time)
argocd app get tombstone-app

# Show individual resource health within the app
argocd app resources tombstone-app
```

### Manual Sync

```bash
# Sync tombstone-app (useful after provider overlay change or manual flag CR edit)
argocd app sync tombstone-app

# Force sync (re-applies all resources even if no diff detected)
argocd app sync tombstone-app --force

# Sync a specific resource only
argocd app sync tombstone-app --resource apps:Deployment:tombstone-flag-api
```

### Argo Rollouts — Progressive Delivery

The `tombstone-blast-radius` AnalysisTemplate gates canary promotions via the
evaluator blast-radius API (`GET evaluator:8082/api/v1/blast-radius?flag_key=<key>`).
`LOW` / `MEDIUM` results promote; `HIGH` / `BLOCKED` abort the rollout.

```bash
# List all active rollouts in the tombstone namespace
kubectl get rollouts -n tombstone

# Watch a specific rollout progress (shows canary weight, step, status)
kubectl argo rollouts get rollout tombstone-flag-api -n tombstone --watch

# Manually promote a paused rollout (skip current step)
kubectl argo rollouts promote tombstone-flag-api -n tombstone

# Abort a rollout (triggers immediate rollback to stable)
kubectl argo rollouts abort tombstone-flag-api -n tombstone
```

### View Blast-Radius AnalysisRuns

```bash
# List all analysis runs (shows Pass / Running / Failed / Error)
kubectl get analysisrun -n tombstone

# Describe a specific run to see blast-radius metric results
kubectl describe analysisrun <run-name> -n tombstone
```

### Notifications

Sync failures trigger Slack notifications via the marketplace service
(`marketplace.tombstone.svc:8086/api/v1/marketplace/slack/actions`). Verify the
`SLACK_BOT_TOKEN` secret is present in the `argocd` namespace if notifications
are not arriving.

---

## Upgrading

```bash
# Diff first
helm diff upgrade tombstone ./infra/helm/flagmind \
  -n tombstone \
  -f infra/helm/flagmind/values.yaml

# Apply migrations before upgrading services
make migrate

# Upgrade (--atomic rolls back on failure)
helm upgrade tombstone ./infra/helm/flagmind \
  --namespace tombstone \
  --atomic \
  --timeout 5m \
  -f infra/helm/flagmind/values.yaml
```

See `infra/helm/flagmind/COMPATIBILITY.md` for pre-upgrade checklist and version compatibility notes.

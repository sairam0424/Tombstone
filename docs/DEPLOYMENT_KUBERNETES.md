# Kubernetes Deployment Guide

This guide covers deploying Tombstone to Kubernetes: single-region via Helm, multi-region, the tombstone-operator, and manual deployment for services not yet in the Helm chart.

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

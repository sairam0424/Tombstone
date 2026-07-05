# Helm Chart Compatibility Matrix

## Current Chart

| Field | Value |
|-------|-------|
| Chart version | `0.1.0` |
| appVersion | `0.1.0` |
| Kubernetes minimum | 1.21+ (uses `apps/v1` Deployments, `networking.k8s.io/v1` Ingress) |
| Helm minimum | 3.8+ |

---

## Kubernetes API Requirements

Templates in this chart use:

| Resource | apiVersion | Min K8s |
|----------|-----------|---------|
| Deployment | `apps/v1` | 1.9 |
| Service | `v1` | All |
| Ingress | `networking.k8s.io/v1` | 1.19 |
| ConfigMap | `v1` | All |
| Secret | `v1` | All |
| ServiceAccount | `v1` | All |

**Effective minimum: Kubernetes 1.21** (1.19 for Ingress v1 + GKE/EKS/AKS version lag).

---

## tombstone-operator Compatibility

The Kubernetes operator (`services/tombstone-operator`) uses `controller-runtime`. CRDs (FeatureFlag, FlagPolicy) use `apiextensions.k8s.io/v1` (CRD v1), which requires Kubernetes 1.16+.

| tombstone-operator version | controller-runtime | Supported K8s |
|---------------------------|-------------------|--------------|
| v0.1.0 | v0.19.x | 1.26–1.31 |

The operator is NOT deployed by this Helm chart — it must be deployed separately via `kubectl apply -f` or a dedicated operator chart.

---

## Known Gap: Incomplete Helm Coverage

**This chart currently only deploys `flag-api` and `gateway` via Deployment templates.**

| Service | Helm Deployment template | values.yaml entry |
|---------|--------------------------|-------------------|
| flag-api | ✅ `deployment-flag-api.yaml` | ✅ `flagApi:` |
| gateway | ✅ `deployment-gateway.yaml` | ✅ `gateway:` |
| evaluator | ❌ not yet | ✅ `evaluator:` |
| intelligence | ❌ not yet | ✅ `intelligence:` |
| marketplace | ❌ not yet | ✅ `marketplace:` |

Services without Deployment templates must be deployed manually (see `docs/DEPLOYMENT_KUBERNETES.md`).

---

## Upgrade Safety

### Version Compatibility

| From chart | To chart | Safe? | Notes |
|-----------|---------|-------|-------|
| 0.1.0 | 0.1.0 | ✅ | Same chart, image tag change only |

There is only one chart version as of v1.2.1. When upgrading chart versions in future releases:
- **Do not skip chart versions** — each version may introduce CRD changes, RBAC changes, or values renames that must be applied in sequence.
- Always run `helm diff upgrade` before `helm upgrade` to preview changes.

### Pre-Upgrade Steps

```bash
# 1. Diff the upgrade
helm diff upgrade tombstone ./infra/helm/flagmind -f values.yaml -f values-production.yaml

# 2. Apply migrations first (separate from Helm)
make migrate

# 3. Upgrade with --atomic for auto-rollback on failure
helm upgrade tombstone ./infra/helm/flagmind \
  --namespace tombstone \
  --atomic \
  --timeout 5m \
  -f values.yaml \
  -f values-production.yaml
```

### CRD Migration

CRDs (FeatureFlag, FlagPolicy) are not managed by this Helm chart. If CRD schemas change between releases:
1. Apply new CRDs first: `kubectl apply -f services/tombstone-operator/config/crd/`
2. Wait for CRD to be established: `kubectl wait --for=condition=Established crd/featureflags.tombstone.dev`
3. Then upgrade the operator and Helm chart

---

## Multi-Region Notes

- `values-region-primary.yaml` enables all services including intelligence (`IS_PRIMARY_REGION=true`)
- `values-region-secondary.yaml` disables intelligence (`IS_PRIMARY_REGION=false`) and reduces replica counts
- Both region configs are designed for use with the Terraform `tombstone_region` resource (see `infra/terraform/`)

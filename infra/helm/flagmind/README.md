# Tombstone Helm Chart — Kubernetes Deployment

> **Note:** This is a **v1.1+ production deployment guide** for Kubernetes.
> For Tombstone v1.0 local self-hosted development, use `make dev` from the root directory instead.

Production Intelligence Layer for Feature Flags — Kubernetes deployment via Helm.

## Prerequisites

- Kubernetes 1.24+
- Helm 3.10+
- External PostgreSQL 16 instance accessible from the cluster
- External Redis instance accessible from the cluster
- Secrets pre-created: `tombstone-db-secret`, `tombstone-redis-secret`

## Quick Install

```bash
# Add chart (local path)
helm install tombstone ./infra/helm/tombstone \
  --namespace tombstone \
  --create-namespace \
  --set postgresql.host=your-postgres-host \
  --set redis.host=your-redis-host

# Upgrade
helm upgrade tombstone ./infra/helm/tombstone --namespace tombstone
```

Before deploying, replace the placeholder base64 values in `templates/secret.yaml` with actual encoded secrets:

```bash
echo -n "your-db-url" | base64
```

## Key Values

| Value | Default | Description |
|-------|---------|-------------|
| `replicaCount` | `2` | Replica count for all deployments |
| `global.imagePullPolicy` | `IfNotPresent` | Image pull policy |
| `flagApi.image.tag` | `0.1.0` | flag-api container image tag |
| `gateway.image.tag` | `0.1.0` | gateway container image tag |
| `postgresql.host` | `postgres` | External PostgreSQL hostname |
| `postgresql.database` | `tombstone` | PostgreSQL database name |
| `redis.host` | `redis` | External Redis hostname |
| `ingress.enabled` | `false` | Enable ingress resource |
| `ingress.className` | `nginx` | Ingress class name |
| `serviceAccount.create` | `true` | Create a dedicated ServiceAccount |

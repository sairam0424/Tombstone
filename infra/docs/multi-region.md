# Tombstone Multi-Region Topology

Tombstone supports a primary/secondary region model where one region owns all flag writes and secondary regions serve read-replicas locally for low-latency flag evaluation.

## Concepts

### Primary region
- Runs the authoritative flag-api (3 replicas) with write access.
- Runs the intelligence service (anomaly detection, incident correlation).
- Anchors the Merkle-linked audit log.

### Secondary region
- Runs a read-replica flag-api (2 replicas) that forwards writes to `PRIMARY_API_URL`.
- Runs the full gateway + evaluator stack for local SSE fan-out and circuit-breaking.
- Skips the intelligence service (cost optimisation — run it only in primary).

## Helm deployment

### Primary region (e.g. us-east-1)

```bash
helm upgrade --install tombstone infra/helm/flagmind \
  -f infra/helm/flagmind/values.yaml \
  -f infra/helm/flagmind/values-region-primary.yaml \
  --namespace tombstone-us-east \
  --create-namespace \
  --set redis.externalUrl="rediss://global.upstash.io:6379"
```

Key overrides in `values-region-primary.yaml`:
- `global.region: us-east-1`
- `global.isPrimary: true`
- flag-api 3 replicas, gateway 5 replicas (high SSE connection count)
- External Redis (Upstash Global) — set `redis.externalUrl` at deploy time

### Secondary region (e.g. eu-west-1)

```bash
helm upgrade --install tombstone infra/helm/flagmind \
  -f infra/helm/flagmind/values.yaml \
  -f infra/helm/flagmind/values-region-secondary.yaml \
  --namespace tombstone-eu-west \
  --create-namespace \
  --set flag-api.env[2].value="https://api.tombstone-us-east.example.com"
```

Key overrides in `values-region-secondary.yaml`:
- `global.region: eu-west-1`
- `global.isPrimary: false`
- flag-api 2 replicas; `IS_PRIMARY_REGION=false`; `PRIMARY_API_URL` set at deploy time
- `intelligence.enabled: false` — only the primary region runs intelligence

### Region ConfigMap

`templates/region-config.yaml` creates a `tombstone-region-config` ConfigMap in every deployment, exposing `region` and `is-primary` as data keys. Services can mount this ConfigMap to discover their own region identity at runtime without env-var duplication.

## Terraform (IaC topology)

The `tombstone_region` resource registers each region with the primary flag-api so the full topology is tracked in Terraform state:

```hcl
resource "tombstone_region" "us_east_1" {
  region      = "us-east-1"
  api_url     = "https://tombstone-us-east.example.com"
  gateway_url = "https://tombstone-gw-us-east.example.com"
  is_primary  = true
}

resource "tombstone_region" "eu_west_1" {
  region      = "eu-west-1"
  api_url     = "https://tombstone-eu-west.example.com"
  gateway_url = "https://tombstone-gw-eu-west.example.com"
  is_primary  = false
}
```

See `infra/terraform/examples/multi-region/main.tf` for a full working example.

### API contract

The `tombstone_region` resource calls three endpoints on the primary flag-api:

| Operation | Method | Path |
|-----------|--------|------|
| Register  | POST   | `/api/v1/regions` |
| Read      | GET    | `/api/v1/regions/{region}` |
| Deregister| DELETE | `/api/v1/regions/{region}` |

All fields (`region`, `api_url`, `gateway_url`, `is_primary`) are immutable after creation — Terraform will replace the resource if any of them change.

## Operational notes

- **Redis**: Use Upstash Global or a self-managed Redis Cluster that replicates across regions. Each region's gateway connects to the same global Redis keyspace for cross-region SSE fan-out.
- **PostgreSQL**: Use Neon branching or read replicas per region. Secondary flag-apis read from the nearest replica; writes go through the primary.
- **Audit log**: Merkle-linked entries are only written in the primary region to maintain the causal ordering guarantee.
- **Intelligence**: Run only in the primary region. The anomaly detection models operate on the global flag event stream (Kafka), which is only fully available in primary.

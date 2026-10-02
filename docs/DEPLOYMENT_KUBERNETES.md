# Kubernetes Deployment Guide

This guide covers deploying Tombstone to Kubernetes: single-region via Helm (the only topology actually proven end-to-end today), a multi-region design sketch (roadmap, not yet functional — see that section's own status note), the tombstone-operator, and manual deployment for services not yet in the Helm chart.

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

## GitOps Deployment — Argo CD Provider

Tombstone supports three GitOps provider modes. Choose based on your existing
cluster tooling:

| Provider Mode | Who Owns What | When to Use |
|---------------|---------------|-------------|
| `flux` | Flux manages infrastructure + apps + flag CRs | Default. Flux-only clusters. |
| `argocd` | Flux manages infrastructure; Argo CD manages apps + flag CRs | Org already runs Argo CD. |
| `both` | Same as `argocd` + Argo Rollouts canary analysis | Production with blast-radius gating. |

### Step 1 — Bootstrap Flux (infrastructure + CRDs)

Run the Flux bootstrap first. This installs tombstone-operator and all CRDs via
the `flux-bootstrap.yml` job. Argo CD cannot create FeatureFlag resources until
the CRDs exist.

```bash
kubectl apply -f gitops/providers/argocd/flux-bootstrap.yml
```

Wait for the tombstone-operator to report Ready before proceeding:

```bash
kubectl rollout status deployment/tombstone-operator -n tombstone-system
```

### Step 2 — Bootstrap Argo CD (apps + flag CRs)

After Flux is running and CRDs are installed, apply the Argo CD bootstrap:

```bash
kubectl apply -f gitops/providers/argocd/argocd-bootstrap.yml
```

This installs Argo CD and creates the Applications for `tombstone-apps` and
`tombstone-flags`. Verify sync status:

```bash
argocd app list
```

### Applying a Provider Overlay

To switch the active provider mode (or apply it to a fresh cluster):

```bash
# Replace <mode> with: flux | argocd | both
kubectl apply -k gitops/providers/<mode>/
```

> **Warning:** Never run `argocd` and `flux` reconciling the same app resources
> simultaneously without suspending one first — they will conflict on `rolloutPct`.

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

**Status: roadmap / non-functional scaffolding, not a working feature today.** The Helm values files and manifests below exist and will deploy real pods, but the primary/secondary distinction they're meant to express is not actually enforced anywhere at runtime, verified directly against the live code (not assumed):

- `IS_PRIMARY_REGION` is passed into the intelligence deployment's container env (`deployment-intelligence.yaml`), but `services/intelligence`'s own Python code never reads it — a "secondary" region deployed today runs the exact same LinUCB/anomaly-detection analytics as primary, not the documented reduced/read-only behavior.
- `region-config.yaml`'s ConfigMap values (`region`, `is-primary`) are likewise never read by any service — no log tag, OTel resource attribute, or code path consults them.
- The Terraform `tombstone_region` resource (`infra/terraform/provider/`) calls `POST /api/v1/regions` against flag-api to register a region — that route does not exist on flag-api at all. `terraform apply` with this resource fails with a 404, not a successful provisioning.

Single-region deployment (the rest of this guide) is the only topology actually proven end-to-end. Treat everything below as a design sketch for a future release, not an instruction set to follow for a real multi-region rollout.

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

## Client IP and trusted proxies

flag-api, evaluator and marketplace decide which client IP a request belongs to in one place (`internal/clientip`, one copy per service). It feeds the per-IP rate-limit buckets, the audit log's `ip_address` (flag-api), the access logs and marketplace's failed-webhook-signature log line.

### Default behaviour

- The client is the **TCP peer**: the address that opened the connection to the service.
- `X-Forwarded-For` is believed only when that peer is inside `TRUSTED_PROXY_CIDRS` (comma-separated CIDRs; whitespace and a trailing comma are fine). The header is then walked right to left, trusted hops are skipped, and the first other entry is the client. An entry that is not an IP ends the walk and the peer is used.
- `X-Real-IP` and `True-Client-IP` are never read.
- Unset or empty trusts no proxy. An invalid entry stops the service at startup with an error naming the entry: a typo, `10.0.0.1` without a prefix length, a `/0` (it would make every caller a trusted proxy), or an IPv4-mapped IPv6 prefix such as `::ffff:10.0.0.0/104` (it never matches; write the IPv4 CIDR).
- Each service logs `client IP resolution` once at startup with `mode` (`peer_only` or `trusted_proxies`) and the `trusted_proxy_cidrs` it parsed. No request data is logged.

### If you leave it unset behind a proxy

Nothing fails, which makes it easy to miss. Every request looks like it came from the proxy:

- All callers that are rate-limited by IP share **one bucket**. In flag-api that is every request without a `Bearer` credential (200 requests/min sustained, burst 20). In evaluator it is every request (200/min, burst 20; the telemetry route has its own bucket at 5000/min, burst 200).
- **New audit rows record the proxy's IP** in `ip_address`. Earlier rows are unchanged.
- Access logs (flag-api, marketplace) and marketplace's failed-signature line name the proxy.

Check the startup line and the `from <ip>` address in the access log after any change to the proxy layer.

### Choosing the value

Trust the smallest range that contains only your proxies, meaning the address the **service sees** as the peer, not the client-facing address. Never list client ranges: trusted entries are skipped while walking the chain, so a listed client could never be identified.

To find the peer address, leave the variable empty, send one request through the proxy, and read the `from <ip>` address in flag-api's (or marketplace's) access log. With nothing trusted that is the TCP peer.

A trusted proxy must set `X-Forwarded-For` itself. One that passes a client-sent header through unchanged hands the client's value to the service.

### Kubernetes with ingress-nginx (Helm)

Set `trustedProxyCIDRs` in your Helm values, as a comma-separated string or a YAML list. Use a values file: `--set trustedProxyCIDRs=a,b` splits on the comma and fails. The GitOps `HelmRelease` (`gitops/apps/production/tombstone/helmrelease.yaml`) takes values from a Secret (`helm-values.yaml`), so the live value is not necessarily in this repo, and it also has an inline `values:` block that can carry it. Set it in one of them. The chart renders it into the `tombstone-config` ConfigMap as `TRUSTED_PROXY_CIDRS`, which the flag-api, evaluator and marketplace Deployments all mount through `envFrom`.

- Leave ingress-nginx's `use-forwarded-headers` off (its default). Per the upstream documentation the controller then ignores an incoming `X-Forwarded-For` and writes the address it sees, so a client-sent chain never reaches the services. Confirm this on your controller version.
- The services' peer is the **ingress controller pod**, so trust the pod address range of the nodes the controller runs on, or something narrower. Do not trust the whole cluster pod CIDR: the chart ships no NetworkPolicy, so any pod can reach a ClusterIP service, and a trusted range lets that pod assert any client IP.
- The ingress itself must see the real client address. A cloud load balancer that source-NATs (`externalTrafficPolicy: Cluster`) makes the ingress see a node address as the client. Use `externalTrafficPolicy: Local` or PROXY protocol, and check the result with the access-log method above.
- If an L7 proxy or CDN sits in front of the ingress, `use-forwarded-headers` and the controller's trusted-proxy setting change. The services' trusted range is still the ingress pods, because they are what connects to the services.

Rollout:

- **Set the value before the image that contains this change is promoted.** Flux image automation rolls new tags independently of the chart values (the `$imagepolicy` markers in that `HelmRelease`). Older images ignore the variable, so setting it first is harmless. The other order leaves flag-api and evaluator on one shared IP bucket, and new audit rows recording the ingress IP, until the value lands.
- **Restart after changing it.** The chart has no config checksum annotation, so a changed value updates the ConfigMap but running pods keep their old environment. Restart the `<release>-tombstone-flag-api`, `-evaluator` and `-marketplace` Deployments (`kubectl rollout restart deployment/<name>`), then read the `client IP resolution` startup line, which still shows the old mode until they restart.
- **Unset is a change from before.** flag-api used to run chi's `RealIP`, which believes `X-Real-IP` and `X-Forwarded-For` from anyone, so behind an ingress that fills those headers callers had separate buckets with no configuration. Unset now puts every caller in the ingress pod's bucket (see above).

### Oracle VM with host nginx (`infra/oracle`)

`infra/oracle/nginx.conf` overwrites `X-Forwarded-For` with `$remote_addr` in every location, so a client-sent chain cannot survive. Set `TRUSTED_PROXY_CIDRS` (`infra/oracle/docker-compose.prod.yml` passes it to flag-api, evaluator and marketplace) to the address the containers see for nginx. That depends on how Docker publishes the port (commonly the compose network's gateway address), so measure it with the method above and trust that one address (`/32`), not the whole compose subnet. Deploy the nginx change and the variable together with the new images: until the variable is set, every client is the nginx address.

Compose fills `${TRUSTED_PROXY_CIDRS}` from the same place as that file's other `${...}` values (`DB_URL`, `JWT_SECRET` and so on), so put it beside them. `setup.sh` checks `infra/.env`, but which file Compose reads depends on the project directory and `--env-file`, which this repo does not pin. Do not assume: after `docker compose up -d`, each service's `client IP resolution` startup line must report `trusted_proxies`.

Two things make the measured address stop being right, and nothing at runtime signals either (a trusted range that no longer matches quietly falls back to the peer):

- Recreating the Docker network can change the gateway address. Re-measure after any recreation; pinning the compose network's subnet is an option, and the owner's call.
- If a CDN or load balancer fronts nginx, `$remote_addr` is that proxy's address, not the client's. nginx then needs its real-IP module configured with that proxy's ranges before the value it forwards means anything. This repo does not say whether such a proxy exists.

The compose file publishes the service ports on all interfaces, and `cloud-init.yml` opens 8081, 8082, 8084, 8085 and 8086 in ufw, so a client may be able to reach a service directly and skip nginx. Whether that is safe depends on what the service sees as the peer for such a caller, and **that has not been measured on this host**. If Docker preserves the caller's source address, the caller is not a trusted peer and its forwarded headers are ignored. If Docker rewrites it to the address nginx also arrives from (its userland proxy does this; which connections take that path depends on the Docker version and configuration), the caller looks like nginx and can forge `X-Forwarded-For`, which is the original hole.

Measure it from a machine outside the VM, over IPv4 and, if the VM has an IPv6 address, over IPv6: send a request straight to a published port with `X-Forwarded-For: 198.51.100.77` and read the `from <ip>` address in the access log. It must be the outside machine's address, neither the sentinel nor the nginx address. These are recommendations for the owner to decide; none is applied by the change that added this section:

1. Bind the published ports to loopback (`127.0.0.1:8081:8081` and so on) so nginx is the only path in. This is what closes the direct path whatever Docker does to the source address. Without it, trusting the gateway address is safe only if the measurement above shows the caller's address is preserved. Re-measure the peer address afterwards, as it can change.
2. Removing the ufw allow rules for 8081, 8082, 8084, 8085 and 8086 in `cloud-init.yml` (and on existing hosts) is tidy-up, not a substitute for step 1: Docker publishes ports through its own iptables rules, which ufw's rules generally do not filter. `DOCKER-USER` rules are the Docker-aware alternative. Confirm with the outside-machine check above.
3. Check the OCI security lists for the same ports. They are not visible in this repo.

### Local development

`make dev` has no proxy in front of the services. Leave `TRUSTED_PROXY_CIDRS` empty; the client is the Docker bridge address the request arrives from.

### Reading older audit rows

`audit_log.ip_address` is now a single validated IP. Earlier rows could hold the raw `X-Forwarded-For` header, including text the caller chose, so treat an older value as unverified. The hash chain and `GET /api/v1/audit/verify` are unaffected: they hash whatever was stored.

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

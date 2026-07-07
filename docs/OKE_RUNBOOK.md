# OKE Staging Cluster Runbook

When you're ready to provision Oracle Cloud OKE, follow this guide.

## Step 1 — Oracle Cloud signup

Go to https://cloud.oracle.com/free
- Credit card required for identity verification only (not charged for Always Free)
- Oracle does a ~$1 temporary authorization that is reversed

## Step 2 — Provision the cluster (Quick Create)

Oracle Cloud Console → Kubernetes Engine (OKE) → Create Cluster → Quick Create

| Field | Value |
|-------|-------|
| Name | `tombstone-staging` |
| Kubernetes version | v1.33.x (latest) |
| Shape | VM.Standard.A1.Flex (Always Free ARM) |
| OCPUs | 2 |
| Memory | 12 GB |
| Nodes | 2 |
| Visibility | Private with public endpoint |

Provisioning takes ~15–20 min.

## Step 3 — Install OCI CLI and configure

```bash
brew install oci-cli
oci setup config   # creates ~/.oci/config with your API key
```

## Step 4 — Download kubeconfig

```bash
oci ce cluster create-kubeconfig \
  --cluster-id <your-cluster-ocid> \
  --file /tmp/oke-staging-kubeconfig.yaml \
  --region <your-region> \
  --token-version 2.0.0

# Verify it works
kubectl --kubeconfig /tmp/oke-staging-kubeconfig.yaml get nodes
```

## Step 5 — Update GitHub secret

```bash
cat /tmp/oke-staging-kubeconfig.yaml | base64 | tr -d '\n' | pbcopy
# Paste into: GitHub → Settings → Secrets → KUBECONFIG_B64 → Update
```

## Step 6 — Pre-populate ghcr-pull-secret (before Flux bootstrap)

```bash
export KUBECONFIG=/tmp/oke-staging-kubeconfig.yaml

# Create flux-system namespace first
kubectl create namespace flux-system

# Populate the ghcr-pull-secret
./scripts/bootstrap-ghcr-secret.sh
# or manually:
# kubectl create secret docker-registry ghcr-pull-secret \
#   --namespace flux-system \
#   --docker-server=ghcr.io \
#   --docker-username=sairam0424 \
#   --docker-password=$(gh auth token)
```

## Step 7 — Run Flux bootstrap

```
GitHub → Actions → Flux Bootstrap → cluster: staging → Run workflow
```

Wait for tombstone-infrastructure Kustomization Ready (~3 min after chart is pulled).

## Step 8 — Run Argo CD bootstrap

```
GitHub → Actions → Argo CD Bootstrap → cluster: staging, provider: both → Run workflow
```

## Step 9 — Add marketplace notification token

```bash
kubectl create secret generic argocd-notifications-secret \
  --from-literal=tombstone-marketplace-token=$FLAG_API_TOKEN \
  -n argocd --dry-run=client -o yaml | kubectl apply -f -
```

## Step 10 — Verify everything is live

```bash
kubectl get nodes
flux get kustomizations
kubectl get applications -n argocd
kubectl get rollouts -n tombstone
```

Expected: tombstone-infrastructure Ready, tombstone-app Synced/Healthy, tombstone-flags Synced/Healthy.

## Current blocker status

| Blocker | Status |
|---------|--------|
| B1: ghcr-pull-secret | Script ready at scripts/bootstrap-ghcr-secret.sh — run at Step 6 above |
| B2: tombstone-operator chart | ✅ Published to ghcr.io/sairam0424/charts (run #28844833994) |
| B3: helm-publish.yml | ✅ Working — auto-publishes on push to main |

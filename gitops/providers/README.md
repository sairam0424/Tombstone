# GitOps Provider Selection

Tombstone supports three GitOps provider configurations, selected via Kustomize overlay.

## Providers

| Provider | Who owns what | When to use |
|----------|--------------|-------------|
| `flux` | Flux owns everything (default) | Minimal footprint, no UI needed |
| `argocd` | Flux: infrastructure + image automation<br>Argo CD: flagmind chart + flag CRs | Need Argo CD UI + Rollouts canary analysis |
| `both` | Same as `argocd` + Argo Rollouts + Notifications -> marketplace Slack | Full capability, production recommended |

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
- Notifications -> marketplace Slack (provider=both only)

## Why not a GitOpsProvider CRD?

A runtime CRD approach has an irreducible circular bootstrap dependency:
tombstone-operator needs a GitOps controller to deploy it, but the GitOps
controller needs tombstone-operator's CRDs to exist first.
Kustomize overlays are deploy-time selection — no circular dependency.

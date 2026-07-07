#!/usr/bin/env bash
# bootstrap-ghcr-secret.sh -- Create or update the ghcr-pull-secret in flux-system.
#
# Usage:
#   ./scripts/bootstrap-ghcr-secret.sh [GHCR_PAT]
#
# Priority: CLI arg > GHCR_PAT env var > gh auth token
# Run this BEFORE flux bootstrap (or make gitops-bootstrap).
# Required scopes: read:packages (pull); write:packages (push, optional).

set -euo pipefail

NAMESPACE="flux-system"
SECRET_NAME="ghcr-pull-secret"
DOCKER_SERVER="ghcr.io"
DOCKER_USERNAME="sairam0424"

# Resolve the registry credential from the first argument,
# the GHCR_PAT environment variable, or the gh CLI.
if [[ $# -ge 1 && -n "${1:-}" ]]; then
  _reg_cred="$1"
elif [[ -n "${GHCR_PAT:-}" ]]; then
  _reg_cred="${GHCR_PAT}"
elif command -v gh &>/dev/null; then
  _reg_cred="$(gh auth token)"
else
  echo "ERROR: No credential found." \
       "Pass as first arg, set GHCR_PAT env var, or install 'gh'." >&2
  exit 1
fi

if [[ -z "${_reg_cred}" ]]; then
  echo "ERROR: Resolved credential is empty." >&2
  exit 1
fi

# Ensure the target namespace exists (Flux bootstrap may not have created it yet)
kubectl get namespace "${NAMESPACE}" &>/dev/null \
  || kubectl create namespace "${NAMESPACE}"

echo "Creating / updating ${SECRET_NAME} in namespace ${NAMESPACE} ..."

kubectl create secret docker-registry "${SECRET_NAME}" \
  --namespace "${NAMESPACE}" \
  --docker-server="${DOCKER_SERVER}" \
  --docker-username="${DOCKER_USERNAME}" \
  "--docker-password=${_reg_cred}" \
  --dry-run=client -o yaml \
  | kubectl apply -f -

echo "Done. Secret ${NAMESPACE}/${SECRET_NAME} is ready."

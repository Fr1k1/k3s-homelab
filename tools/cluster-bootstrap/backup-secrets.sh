#!/usr/bin/env bash
# Captures the two secrets that exist only in-cluster and are gone forever
# if the node dies: the sealed-secrets controller's private key(s), and
# ArgoCD's own admin secret. Encrypts them with age against the committed
# public key, writes a ciphertext file meant to be committed to git.
# Run rarely, by hand -- only when either secret is first created or
# deliberately rotated, not on a schedule.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RECIPIENT_FILE="$SCRIPT_DIR/age-recipient.txt"
OUT_FILE="$SCRIPT_DIR/bootstrap-secrets.tar.gz.age"

for bin in kubectl age jq tar; do
  if ! command -v "$bin" >/dev/null 2>&1; then
    echo "backup-secrets: $bin not found on PATH" >&2
    exit 1
  fi
done

if [ ! -f "$RECIPIENT_FILE" ]; then
  echo "backup-secrets: missing $RECIPIENT_FILE" >&2
  echo "  One-time setup: age-keygen -o bootstrap-secrets-key.txt" >&2
  echo "  Then put the 'public key:' line's value into $RECIPIENT_FILE," >&2
  echo "  and store bootstrap-secrets-key.txt's private key in Bitwarden -- never on this server." >&2
  exit 1
fi

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

# Strip server-assigned metadata so `kubectl apply` on a fresh cluster does a
# clean create, not a stale-object fight with leftover fields from the dead
# cluster.
kubectl get secret -n kube-system -l sealedsecrets.bitnami.com/sealed-secrets-key -o json \
  | jq 'del(.items[].metadata.resourceVersion, .items[].metadata.uid, .items[].metadata.creationTimestamp, .items[].metadata.selfLink, .items[].metadata.managedFields, .items[].metadata.ownerReferences)' \
  > "$TMP_DIR/sealed-secrets-keys.json"

kubectl get secret argocd-secret -n argocd -o json \
  | jq 'del(.metadata.resourceVersion, .metadata.uid, .metadata.creationTimestamp, .metadata.selfLink, .metadata.managedFields, .metadata.ownerReferences)' \
  > "$TMP_DIR/argocd-secret.json"

tar -czf - -C "$TMP_DIR" sealed-secrets-keys.json argocd-secret.json \
  | age -e -R "$RECIPIENT_FILE" > "$OUT_FILE"

echo "backup-secrets: wrote $OUT_FILE -- review, then git add/commit/push it"

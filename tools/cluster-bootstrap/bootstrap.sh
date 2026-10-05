#!/usr/bin/env bash
# One-command disaster recovery: a freshly-installed bare Linux box becomes a
# fully running platform, with the original sealed-secrets key and ArgoCD
# admin secret restored, then handed off to ArgoCD to rebuild everything else
# from git. See docs/superpowers/specs/2026-08-14-disaster-recovery-design.md.
#
# Recovery time is realistically tens of minutes (package downloads, chart
# installs), not seconds -- this collapses "hours of manual reconstruction
# from memory, with some things permanently unrecoverable" down to one
# command, not literal instant recovery.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BUNDLE="$SCRIPT_DIR/bootstrap-secrets.tar.gz.age"
APPLICATION_MANIFEST="$SCRIPT_DIR/homelab-application.yaml"

K3S_VERSION="v1.36.2+k3s1"
AGE_VERSION="v1.3.1"
SEALED_SECRETS_VERSION="0.27.1"
ARGOCD_VERSION="v3.4.5"

for f in "$BUNDLE" "$APPLICATION_MANIFEST"; do
  if [ ! -f "$f" ]; then
    echo "bootstrap: missing $f" >&2
    exit 1
  fi
done

if ! command -v curl >/dev/null 2>&1; then
  echo "bootstrap: curl not found on PATH -- install it first" >&2
  exit 1
fi

echo "== 1/6: installing k3s $K3S_VERSION =="
curl -sfL https://get.k3s.io | INSTALL_K3S_VERSION="$K3S_VERSION" sh -
export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
kubectl wait --for=condition=Ready node --all --timeout=120s

echo "== 2/6: installing age (if needed) =="
if ! command -v age >/dev/null 2>&1; then
  curl -fsSL -o /tmp/age.tar.gz "https://github.com/FiloSottile/age/releases/download/${AGE_VERSION}/age-${AGE_VERSION}-linux-amd64.tar.gz"
  tar -xzf /tmp/age.tar.gz -C /tmp
  sudo install -m 755 /tmp/age/age /usr/local/bin/age
  sudo install -m 755 /tmp/age/age-keygen /usr/local/bin/age-keygen
  rm -rf /tmp/age.tar.gz /tmp/age
fi

echo "== 2/6: installing helm (if needed) =="
if ! command -v helm >/dev/null 2>&1; then
  curl -fsSL https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | bash
fi

for bin in kubectl age helm tar; do
  if ! command -v "$bin" >/dev/null 2>&1; then
    echo "bootstrap: $bin still not found on PATH after install steps" >&2
    exit 1
  fi
done

echo "== 3/6: decrypting bootstrap secrets bundle =="
echo "Paste the age private key (from Bitwarden), then press Ctrl-D:"
AGE_KEY_FILE="$(mktemp)"
TMP_DIR="$(mktemp -d)"
cleanup() {
  rm -f "$AGE_KEY_FILE"
  rm -rf "$TMP_DIR"
}
trap cleanup EXIT
cat > "$AGE_KEY_FILE"
age -d -i "$AGE_KEY_FILE" "$BUNDLE" | tar -xzf - -C "$TMP_DIR"
rm -f "$AGE_KEY_FILE"

echo "== 4/6: restoring sealed-secrets key, then installing the controller =="
kubectl apply -f "$TMP_DIR/sealed-secrets-keys.json"
kubectl apply -f "https://github.com/bitnami-labs/sealed-secrets/releases/download/v${SEALED_SECRETS_VERSION}/controller.yaml"
kubectl -n kube-system rollout status deployment/sealed-secrets-controller --timeout=120s

echo "== 5/6: restoring argocd-secret, then installing ArgoCD =="
kubectl create namespace argocd --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -f "$TMP_DIR/argocd-secret.json"
kubectl apply -n argocd -f "https://raw.githubusercontent.com/argoproj/argo-cd/${ARGOCD_VERSION}/manifests/install.yaml"
kubectl -n argocd rollout status deployment/argocd-server --timeout=180s

echo "== 6/6: bootstrapping the homelab Application (also installs Image Updater) =="
kubectl apply -f "$APPLICATION_MANIFEST"

echo "bootstrap: done. ArgoCD will now resync everything else from git automatically."
echo "Watch progress with: kubectl get application -n argocd -w"

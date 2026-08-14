#!/usr/bin/env bash
# Exercises bootstrap.sh's control flow and ordering with stubbed
# kubectl/helm/curl -- no real cluster or node install happens. Real `age`
# is used for the decrypt step, so that half of the pipeline is genuinely
# proven, not assumed. What can't be verified this way -- whether the real
# k3s/ArgoCD/sealed-secrets install commands actually succeed against real
# hardware -- is called out explicitly in the design spec as needing a real
# fire drill before this is trusted in an actual incident.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REAL_SCRIPT="$SCRIPT_DIR/bootstrap.sh"

PASS=0
FAIL=0
fail() { echo "FAIL: $1"; FAIL=$((FAIL + 1)); }
pass() { PASS=$((PASS + 1)); }

setup_repo() {
  local base="$1"
  REPO="$base/repo"
  STUBS="$base/stubs"
  mkdir -p "$REPO/tools/cluster-bootstrap" "$STUBS"
  cp "$REAL_SCRIPT" "$REPO/tools/cluster-bootstrap/bootstrap.sh"
  chmod +x "$REPO/tools/cluster-bootstrap/bootstrap.sh"
  STUB_LOG="$base/stub.log"
  : > "$STUB_LOG"

  cat > "$STUBS/kubectl" <<'STUBEOF'
#!/usr/bin/env bash
echo "kubectl $*" >> "$STUB_LOG"
for arg in "$@"; do
  if [ -f "$arg" ]; then
    echo "  [content of $arg]:" >> "$STUB_LOG"
    cat "$arg" >> "$STUB_LOG"
  fi
done
if [[ " $* " == *" -f - "* ]]; then
  echo "  [stdin]:" >> "$STUB_LOG"
  cat >> "$STUB_LOG"
fi
if [ "${1:-}" = "create" ] && [ "${2:-}" = "namespace" ]; then
  echo "apiVersion: v1"
  echo "kind: Namespace"
fi
exit 0
STUBEOF
  chmod +x "$STUBS/kubectl"

  cat > "$STUBS/helm" <<'STUBEOF'
#!/usr/bin/env bash
echo "helm $*" >> "$STUB_LOG"
exit 0
STUBEOF
  chmod +x "$STUBS/helm"

  # Only the k3s-install line's curl|sh needs stubbing -- age/helm are
  # already real binaries on this test's PATH, so bootstrap.sh's own
  # `command -v age`/`command -v helm` checks skip their download branches
  # entirely, and no other curl call happens.
  cat > "$STUBS/curl" <<'STUBEOF'
#!/usr/bin/env bash
echo "curl $*" >> "$STUB_LOG"
case "$*" in
  *"get.k3s.io"*)
    echo '#!/usr/bin/env bash'
    echo "echo \"k3s-install-ran version=\$INSTALL_K3S_VERSION\" >> \"$STUB_LOG\""
    ;;
  *)
    echo "unexpected curl args: $*" >&2
    exit 1
    ;;
esac
STUBEOF
  chmod +x "$STUBS/curl"
}

run_script() {
  ( cd "$REPO" && PATH="$STUBS:$HOME/go/bin:$PATH" STUB_LOG="$STUB_LOG" bash tools/cluster-bootstrap/bootstrap.sh )
}

make_test_bundle() {
  local repo="$1"
  age-keygen -o "$repo/test-key.txt" 2>/dev/null
  local pubkey
  pubkey=$(grep "public key" "$repo/test-key.txt" | cut -d: -f2 | tr -d ' ')
  local tmp
  tmp=$(mktemp -d)
  echo '{"FAKE_SEALED_SECRETS_KEY_MARKER":true}' > "$tmp/sealed-secrets-keys.json"
  echo '{"FAKE_ARGOCD_SECRET_MARKER":true}' > "$tmp/argocd-secret.json"
  tar -czf - -C "$tmp" sealed-secrets-keys.json argocd-secret.json \
    | age -e -r "$pubkey" > "$repo/tools/cluster-bootstrap/bootstrap-secrets.tar.gz.age"
  rm -rf "$tmp"
}

# --- test: missing bundle file -> exit 1 ---
t=$(mktemp -d); setup_repo "$t"
echo "name: homelab" > "$REPO/tools/cluster-bootstrap/homelab-application.yaml"
set +e
out=$(run_script </dev/null 2>&1); code=$?
set -e
if [ "$code" -eq 1 ] && echo "$out" | grep -q "bootstrap-secrets.tar.gz.age"; then pass; else fail "missing-bundle: got exit=$code out=$out"; fi
rm -rf "$t"

# --- test: missing application manifest -> exit 1 ---
t=$(mktemp -d); setup_repo "$t"
make_test_bundle "$REPO"
set +e
out=$(run_script </dev/null 2>&1); code=$?
set -e
if [ "$code" -eq 1 ] && echo "$out" | grep -q "homelab-application.yaml"; then pass; else fail "missing-manifest: got exit=$code out=$out"; fi
rm -rf "$t"

# --- test: happy path -- correct ordering, real decrypt, correct pinned versions ---
t=$(mktemp -d); setup_repo "$t"
make_test_bundle "$REPO"
cat > "$REPO/tools/cluster-bootstrap/homelab-application.yaml" <<'EOF'
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: homelab
  namespace: argocd
EOF

set +e
out=$( ( cd "$REPO" && PATH="$STUBS:$HOME/go/bin:$PATH" STUB_LOG="$STUB_LOG" AGE_KEY_INPUT="$(cat "$REPO/test-key.txt")" bash -c 'echo "$AGE_KEY_INPUT" | bash tools/cluster-bootstrap/bootstrap.sh' ) 2>&1 ); code=$?
set -e
if [ "$code" -eq 0 ]; then pass; else fail "happy-path: got exit=$code out=$out"; fi

if grep -q "k3s-install-ran version=v1.36.2+k3s1" "$STUB_LOG"; then pass; else fail "happy-path: k3s install did not run with pinned version, log: $(cat "$STUB_LOG")"; fi

# sealed-secrets key must be applied BEFORE the controller manifest
key_line=$(grep -n "FAKE_SEALED_SECRETS_KEY_MARKER" "$STUB_LOG" | head -1 | cut -d: -f1)
controller_line=$(grep -n "sealed-secrets/releases/download/v0.27.1/controller.yaml" "$STUB_LOG" | head -1 | cut -d: -f1)
if [ -n "$key_line" ] && [ -n "$controller_line" ] && [ "$key_line" -lt "$controller_line" ]; then
  pass
else
  fail "happy-path: sealed-secrets key was not restored before the controller was installed (key_line=$key_line, controller_line=$controller_line)"
fi

# argocd-secret must be applied BEFORE the ArgoCD install manifest
argosecret_line=$(grep -n "FAKE_ARGOCD_SECRET_MARKER" "$STUB_LOG" | head -1 | cut -d: -f1)
argocd_install_line=$(grep -n "argo-cd/v3.4.5/manifests/install.yaml" "$STUB_LOG" | head -1 | cut -d: -f1)
if [ -n "$argosecret_line" ] && [ -n "$argocd_install_line" ] && [ "$argosecret_line" -lt "$argocd_install_line" ]; then
  pass
else
  fail "happy-path: argocd-secret was not restored before ArgoCD was installed (argosecret_line=$argosecret_line, argocd_install_line=$argocd_install_line)"
fi

if grep -q "helm install argocd-image-updater argo/argocd-image-updater --namespace argocd --version 0.14.0" "$STUB_LOG"; then
  pass
else
  fail "happy-path: Image Updater not installed with pinned chart version, log: $(cat "$STUB_LOG")"
fi

# the homelab Application must be the LAST kubectl apply, and use the real file
if grep -q "kubectl apply -f .*homelab-application.yaml" "$STUB_LOG" && grep -q "name: homelab" "$STUB_LOG"; then
  pass
else
  fail "happy-path: homelab Application was not applied, or its real file content wasn't logged"
fi

rm -rf "$t"

echo ""
echo "$PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]

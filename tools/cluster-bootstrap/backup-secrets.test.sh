#!/usr/bin/env bash
# Exercises backup-secrets.sh for real (real age/jq/tar -- only kubectl is
# stubbed, since that's the only thing needing a live cluster). Proves the
# actual encrypt round-trip works and that server-assigned metadata really
# gets stripped, not just that the script "runs".
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REAL_SCRIPT="$SCRIPT_DIR/backup-secrets.sh"

PASS=0
FAIL=0
fail() { echo "FAIL: $1"; FAIL=$((FAIL + 1)); }
pass() { PASS=$((PASS + 1)); }

setup_repo() {
  local base="$1"
  REPO="$base/repo"
  STUBS="$base/stubs"
  mkdir -p "$REPO/tools/cluster-bootstrap" "$STUBS"
  cp "$REAL_SCRIPT" "$REPO/tools/cluster-bootstrap/backup-secrets.sh"
  chmod +x "$REPO/tools/cluster-bootstrap/backup-secrets.sh"
  STUB_LOG="$base/stub.log"
  : > "$STUB_LOG"

  cat > "$STUBS/kubectl" <<'STUBEOF'
#!/usr/bin/env bash
echo "kubectl $*" >> "$STUB_LOG"
case "$*" in
  *"-l sealedsecrets.bitnami.com/sealed-secrets-key"*)
    cat <<'JSON'
{"apiVersion":"v1","kind":"List","items":[{"apiVersion":"v1","kind":"Secret","metadata":{"name":"sealed-secrets-keyabcde","namespace":"kube-system","resourceVersion":"999","uid":"fake-uid-1","creationTimestamp":"2026-01-01T00:00:00Z"},"data":{"tls.crt":"ZmFrZQ==","tls.key":"ZmFrZQ=="},"type":"kubernetes.io/tls"}]}
JSON
    ;;
  *"argocd-secret"*)
    cat <<'JSON'
{"apiVersion":"v1","kind":"Secret","metadata":{"name":"argocd-secret","namespace":"argocd","resourceVersion":"888","uid":"fake-uid-2","creationTimestamp":"2026-01-01T00:00:00Z"},"data":{"admin.password":"ZmFrZQ==","server.secretkey":"ZmFrZQ=="}}
JSON
    ;;
  *)
    echo "unexpected kubectl args: $*" >&2
    exit 1
    ;;
esac
STUBEOF
  chmod +x "$STUBS/kubectl"
}

run_script() {
  ( cd "$REPO" && PATH="$STUBS:$PATH" STUB_LOG="$STUB_LOG" bash tools/cluster-bootstrap/backup-secrets.sh "$@" )
}

# --- test: missing kubectl on PATH -> exit 1 ---
t=$(mktemp -d); setup_repo "$t"
age-keygen -o "$REPO/tools/cluster-bootstrap/test-key.txt" 2>/dev/null
grep "public key" "$REPO/tools/cluster-bootstrap/test-key.txt" | cut -d: -f2 | tr -d ' ' > "$REPO/tools/cluster-bootstrap/age-recipient.txt"
set +e
out=$( cd "$REPO" && PATH="/usr/bin:/bin" bash tools/cluster-bootstrap/backup-secrets.sh 2>&1 ); code=$?
set -e
if [ "$code" -eq 1 ] && echo "$out" | grep -qi "not found"; then pass; else fail "no-kubectl: got exit=$code out=$out"; fi
rm -rf "$t"

# --- test: missing age-recipient.txt -> exit 1, mentions age-keygen ---
t=$(mktemp -d); setup_repo "$t"
set +e
out=$(run_script 2>&1); code=$?
set -e
if [ "$code" -eq 1 ] && echo "$out" | grep -q "age-keygen"; then pass; else fail "missing-recipient: got exit=$code out=$out"; fi
rm -rf "$t"

# --- test: happy path -- real encrypt round-trip, metadata actually stripped ---
t=$(mktemp -d); setup_repo "$t"
age-keygen -o "$REPO/tools/cluster-bootstrap/test-key.txt" 2>/dev/null
grep "public key" "$REPO/tools/cluster-bootstrap/test-key.txt" | cut -d: -f2 | tr -d ' ' > "$REPO/tools/cluster-bootstrap/age-recipient.txt"
set +e
out=$(run_script 2>&1); code=$?
set -e
outfile="$REPO/tools/cluster-bootstrap/bootstrap-secrets.tar.gz.age"
if [ "$code" -eq 0 ] && [ -f "$outfile" ]; then pass; else fail "happy-path: got exit=$code out=$out"; fi

decrypt_dir=$(mktemp -d)
age -d -i "$REPO/tools/cluster-bootstrap/test-key.txt" "$outfile" | tar -xzf - -C "$decrypt_dir"

if [ -f "$decrypt_dir/sealed-secrets-keys.json" ] && [ -f "$decrypt_dir/argocd-secret.json" ]; then
  pass
else
  fail "happy-path: expected both extracted files, got: $(ls "$decrypt_dir")"
fi

if grep -q "resourceVersion" "$decrypt_dir/sealed-secrets-keys.json" 2>/dev/null; then
  fail "happy-path: resourceVersion was NOT stripped from sealed-secrets-keys.json"
else
  pass
fi
if grep -q "resourceVersion" "$decrypt_dir/argocd-secret.json" 2>/dev/null; then
  fail "happy-path: resourceVersion was NOT stripped from argocd-secret.json"
else
  pass
fi
if grep -q "tls.crt" "$decrypt_dir/sealed-secrets-keys.json"; then
  pass
else
  fail "happy-path: real secret data (tls.crt) was lost somewhere in the pipeline"
fi
if grep -q "admin.password" "$decrypt_dir/argocd-secret.json"; then
  pass
else
  fail "happy-path: real secret data (admin.password) was lost somewhere in the pipeline"
fi
rm -rf "$t" "$decrypt_dir"

echo ""
echo "$PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]

#!/usr/bin/env bash
# Exercises check-no-plaintext-secrets.sh against throwaway git repos, so the
# git ls-files check runs for real rather than being mocked.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REAL_SCRIPT="$SCRIPT_DIR/check-no-plaintext-secrets.sh"

PASS=0
FAIL=0

fail() { echo "FAIL: $1"; FAIL=$((FAIL + 1)); }
pass() { PASS=$((PASS + 1)); }

setup_repo() {
  REPO="$1/repo"
  mkdir -p "$REPO/tools/seal-secret" "$REPO/apps/myapp"
  cp "$REAL_SCRIPT" "$REPO/tools/seal-secret/check-no-plaintext-secrets.sh"
  ( cd "$REPO" && git init -q && git config user.email t@t.com && git config user.name t )
}

run_check() {
  ( cd "$REPO" && bash tools/seal-secret/check-no-plaintext-secrets.sh )
}

# --- test: clean repo (only SealedSecrets) -> passes ---
t=$(mktemp -d); setup_repo "$t"
cat > "$REPO/apps/myapp/sealed-secret.yaml" <<'EOF'
apiVersion: bitnami.com/v1alpha1
kind: SealedSecret
metadata:
  name: myapp-secret
EOF
( cd "$REPO" && git add -A && git commit -q -m init )
set +e
out=$(run_check 2>&1); code=$?
set -e
if [ "$code" -eq 0 ]; then pass; else fail "clean-repo: expected exit 0, got $code, out=$out"; fi
rm -rf "$t"

# --- test: plaintext Secret present -> fails ---
t=$(mktemp -d); setup_repo "$t"
cat > "$REPO/apps/myapp/oops.yaml" <<'EOF'
apiVersion: v1
kind: Secret
metadata:
  name: myapp-secret
EOF
( cd "$REPO" && git add -A && git commit -q -m init )
set +e
out=$(run_check 2>&1); code=$?
set -e
if [ "$code" -ne 0 ] && echo "$out" | grep -q "oops.yaml"; then pass; else fail "plaintext-secret: got exit=$code out=$out"; fi
rm -rf "$t"

# --- test: SealedSecret alone does not false-positive ---
t=$(mktemp -d); setup_repo "$t"
cat > "$REPO/apps/myapp/sealed-secret.yaml" <<'EOF'
apiVersion: bitnami.com/v1alpha1
kind: SealedSecret
EOF
( cd "$REPO" && git add -A && git commit -q -m init )
set +e
out=$(run_check 2>&1); code=$?
set -e
if [ "$code" -eq 0 ]; then pass; else fail "no-false-positive: expected exit 0, got $code out=$out"; fi
rm -rf "$t"

# --- test: tracked .env file -> fails ---
t=$(mktemp -d); setup_repo "$t"
echo "FOO=bar" > "$REPO/apps/myapp/.env"
( cd "$REPO" && git add -A -f && git commit -q -m init )
set +e
out=$(run_check 2>&1); code=$?
set -e
if [ "$code" -ne 0 ] && echo "$out" | grep -q "apps/myapp/.env"; then pass; else fail "tracked-env: got exit=$code out=$out"; fi
rm -rf "$t"

# --- test: untracked .env file (gitignored, never staged) -> passes ---
t=$(mktemp -d); setup_repo "$t"
echo "FOO=bar" > "$REPO/apps/myapp/.env"
( cd "$REPO" && git add -A tools/seal-secret && git commit -q -m init )
set +e
out=$(run_check 2>&1); code=$?
set -e
if [ "$code" -eq 0 ]; then pass; else fail "untracked-env: expected exit 0, got $code out=$out"; fi
rm -rf "$t"

echo ""
echo "$PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]

#!/usr/bin/env bash
# Exercises seal-secret.sh's control flow with stubbed kubectl/kubeseal --
# no real cluster or kubeseal binary required. Each test builds a throwaway
# repo layout (apps/<app>/.env, tools/seal-secret/{seal-secret.sh,pub-cert.pem})
# so the script's own REPO_ROOT resolution (relative to its own path) is
# exercised for real, not mocked.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REAL_SCRIPT="$SCRIPT_DIR/seal-secret.sh"

PASS=0
FAIL=0

fail() {
  echo "FAIL: $1"
  FAIL=$((FAIL + 1))
}

pass() {
  PASS=$((PASS + 1))
}

# Builds a throwaway repo at $1/repo with a working stub kubectl/kubeseal on
# PATH, and returns (via globals) the paths a test needs.
setup_repo() {
  local base="$1"
  REPO="$base/repo"
  STUBS="$base/stubs"
  mkdir -p "$REPO/tools/seal-secret" "$REPO/apps/myapp" "$STUBS"
  cp "$REAL_SCRIPT" "$REPO/tools/seal-secret/seal-secret.sh"
  chmod +x "$REPO/tools/seal-secret/seal-secret.sh"

  cat > "$STUBS/kubectl" <<'EOF'
#!/usr/bin/env bash
echo "kubectl $*" >> "$STUB_LOG"
echo "kind: Secret"
echo "fromEnvFile: yes"
EOF
  cat > "$STUBS/kubeseal" <<'EOF'
#!/usr/bin/env bash
echo "kubeseal $*" >> "$STUB_LOG"
echo "kind: SealedSecret"
cat
EOF
  chmod +x "$STUBS/kubectl" "$STUBS/kubeseal"
  STUB_LOG="$base/stub.log"
  : > "$STUB_LOG"
}

run_script() {
  ( cd "$REPO" && PATH="$STUBS:$PATH" STUB_LOG="$STUB_LOG" bash tools/seal-secret/seal-secret.sh "$@" )
}

# --- test: no args -> usage, exit 2 ---
t=$(mktemp -d); setup_repo "$t"
set +e
out=$(run_script 2>&1); code=$?
set -e
if [ "$code" -eq 2 ] && echo "$out" | grep -q "Usage:"; then pass; else fail "no-args: expected exit 2 + usage, got exit=$code out=$out"; fi
rm -rf "$t"

# --- test: unknown flag -> usage, exit 2 ---
t=$(mktemp -d); setup_repo "$t"
set +e
out=$(run_script myapp --bogus 2>&1); code=$?
set -e
if [ "$code" -eq 2 ]; then pass; else fail "unknown-flag: expected exit 2, got $code"; fi
rm -rf "$t"

# --- test: missing .env -> exit 1, mentions the expected path ---
t=$(mktemp -d); setup_repo "$t"
set +e
out=$(run_script myapp 2>&1); code=$?
set -e
if [ "$code" -eq 1 ] && echo "$out" | grep -q "apps/myapp/.env"; then pass; else fail "missing-env: got exit=$code out=$out"; fi
rm -rf "$t"

# --- test: missing cert -> exit 1, mentions fetch-cert bootstrap ---
t=$(mktemp -d); setup_repo "$t"
echo "FOO=bar" > "$REPO/apps/myapp/.env"
set +e
out=$(run_script myapp 2>&1); code=$?
set -e
if [ "$code" -eq 1 ] && echo "$out" | grep -q "fetch-cert"; then pass; else fail "missing-cert: got exit=$code out=$out"; fi
rm -rf "$t"

# --- test: missing kubectl/kubeseal on PATH -> exit 1 ---
t=$(mktemp -d); setup_repo "$t"
echo "FOO=bar" > "$REPO/apps/myapp/.env"
echo "fake-cert" > "$REPO/tools/seal-secret/pub-cert.pem"
set +e
out=$( cd "$REPO" && PATH="/usr/bin:/bin" bash tools/seal-secret/seal-secret.sh myapp 2>&1); code=$?
set -e
if [ "$code" -eq 1 ] && echo "$out" | grep -qi "not found"; then pass; else fail "no-kubectl: got exit=$code out=$out"; fi
rm -rf "$t"

# --- test: happy path -- writes output, wires kubectl | kubeseal correctly ---
t=$(mktemp -d); setup_repo "$t"
echo "FOO=bar" > "$REPO/apps/myapp/.env"
echo "fake-cert" > "$REPO/tools/seal-secret/pub-cert.pem"
set +e
out=$(run_script myapp 2>&1); code=$?
set -e
outfile="$REPO/apps/myapp/sealed-secret.yaml"
if [ "$code" -eq 0 ] && [ -f "$outfile" ] && grep -q "kind: SealedSecret" "$outfile"; then
  pass
else
  fail "happy-path: got exit=$code out=$out file-exists=$([ -f "$outfile" ] && echo yes || echo no)"
fi
if grep -q "kubectl create secret generic myapp-secret -n default --from-env-file=$REPO/apps/myapp/.env --dry-run=client -o yaml" "$STUB_LOG"; then
  pass
else
  fail "happy-path: kubectl not called with expected args, log: $(cat "$STUB_LOG")"
fi
if grep -q "kubeseal --cert $REPO/tools/seal-secret/pub-cert.pem --format yaml" "$STUB_LOG"; then
  pass
else
  fail "happy-path: kubeseal not called with expected args, log: $(cat "$STUB_LOG")"
fi
rm -rf "$t"

# --- test: -n and --name override the defaults ---
t=$(mktemp -d); setup_repo "$t"
echo "FOO=bar" > "$REPO/apps/myapp/.env"
echo "fake-cert" > "$REPO/tools/seal-secret/pub-cert.pem"
set +e
out=$(run_script myapp -n custom-ns --name custom-secret 2>&1); code=$?
set -e
if [ "$code" -eq 0 ] && grep -q "kubectl create secret generic custom-secret -n custom-ns" "$STUB_LOG"; then
  pass
else
  fail "overrides: got exit=$code, log: $(cat "$STUB_LOG")"
fi
rm -rf "$t"

# --- test: on kubeseal failure, existing output file is left untouched ---
t=$(mktemp -d); setup_repo "$t"
echo "FOO=bar" > "$REPO/apps/myapp/.env"
echo "fake-cert" > "$REPO/tools/seal-secret/pub-cert.pem"
echo "previous-good-content" > "$REPO/apps/myapp/sealed-secret.yaml"
cat > "$STUBS/kubeseal" <<'EOF'
#!/usr/bin/env bash
echo "kubeseal $*" >> "$STUB_LOG"
cat > /dev/null
echo "boom" >&2
exit 1
EOF
chmod +x "$STUBS/kubeseal"
set +e
out=$(run_script myapp 2>&1); code=$?
set -e
content="$(cat "$REPO/apps/myapp/sealed-secret.yaml")"
if [ "$code" -ne 0 ] && [ "$content" = "previous-good-content" ]; then
  pass
else
  fail "kubeseal-failure: expected non-zero exit and untouched file, got exit=$code content=$content"
fi
rm -rf "$t"

echo ""
echo "$PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]

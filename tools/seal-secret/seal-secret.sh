#!/usr/bin/env bash
# Renders apps/<app>/.env into a SealedSecret, sealed offline against the
# cached public cert in this directory. Never touches the cluster.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
CERT_FILE="$SCRIPT_DIR/pub-cert.pem"

usage() {
  echo "Usage: $(basename "$0") <app-dir> [-n namespace] [--name secret-name]" >&2
  echo "  Reads apps/<app-dir>/.env, writes apps/<app-dir>/sealed-secret.yaml" >&2
}

if [ $# -lt 1 ]; then
  usage
  exit 2
fi

APP_DIR="$1"
shift
NAMESPACE="default"
SECRET_NAME="${APP_DIR}-secret"

while [ $# -gt 0 ]; do
  case "$1" in
    -n)
      NAMESPACE="$2"
      shift 2
      ;;
    --name)
      SECRET_NAME="$2"
      shift 2
      ;;
    *)
      echo "seal-secret: unknown argument: $1" >&2
      usage
      exit 2
      ;;
  esac
done

ENV_FILE="$REPO_ROOT/apps/$APP_DIR/.env"
OUT_FILE="$REPO_ROOT/apps/$APP_DIR/sealed-secret.yaml"

if [ ! -f "$ENV_FILE" ]; then
  echo "seal-secret: no such file: $ENV_FILE" >&2
  echo "  Create it (KEY=VALUE per line) before sealing." >&2
  exit 1
fi

if [ ! -f "$CERT_FILE" ]; then
  echo "seal-secret: missing public cert: $CERT_FILE" >&2
  echo "  One-time bootstrap (needs cluster access, run once):" >&2
  echo "    kubeseal --fetch-cert > $CERT_FILE" >&2
  exit 1
fi

for bin in kubectl kubeseal; do
  if ! command -v "$bin" >/dev/null 2>&1; then
    echo "seal-secret: $bin not found on PATH" >&2
    exit 1
  fi
done

TMP_FILE="$(mktemp)"
trap 'rm -f "$TMP_FILE"' EXIT

kubectl create secret generic "$SECRET_NAME" \
  -n "$NAMESPACE" \
  --from-env-file="$ENV_FILE" \
  --dry-run=client -o yaml \
  | kubeseal --cert "$CERT_FILE" --format yaml > "$TMP_FILE"

mv "$TMP_FILE" "$OUT_FILE"
trap - EXIT

echo "seal-secret: wrote $OUT_FILE"

#!/usr/bin/env bash
# CI guardrail: fails if a plaintext Kubernetes Secret (not SealedSecret) or a
# tracked .env file ever lands under apps/. Holds no secret material itself --
# it only greps for shapes that shouldn't be committed.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"

status=0

plaintext_secrets=$(grep -rlE '^kind: Secret$' apps/ --include='*.yaml' --include='*.yml' 2>/dev/null || true)
if [ -n "$plaintext_secrets" ]; then
  echo "::error::Plaintext Kubernetes Secret(s) found under apps/ -- these must be SealedSecrets:"
  echo "$plaintext_secrets"
  status=1
fi

tracked_env_files=$(git ls-files 'apps/*/.env' 2>/dev/null || true)
if [ -n "$tracked_env_files" ]; then
  echo "::error::.env file(s) tracked in git -- these must never be committed:"
  echo "$tracked_env_files"
  status=1
fi

exit $status

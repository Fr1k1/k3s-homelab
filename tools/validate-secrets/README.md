# validate-secrets

CI guardrail, not a generator: `check-no-plaintext-secrets.sh` holds no
secret material and never seals anything. It fails the build if a plaintext
Kubernetes `Secret` (instead of a `SealedSecret`) or a tracked `.env` file
ever lands under `apps/` -- the realistic failure mode being guarded
against is a mis-run `kubectl create secret` (missing `--dry-run`) or an
accidentally staged `.env`, not a sophisticated leak.

Wired into `.github/workflows/validate-secrets.yml`, runs on every push/PR
to `master`.

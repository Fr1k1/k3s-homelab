# seal-secret

Automates the manual `kubectl create secret ... | kubeseal ...` step
documented in the root `README.md`, so changing an app's env vars doesn't
mean hand-typing `--from-literal` flags and re-deriving the naming
convention every time.

## One-time bootstrap (per cluster, needs cluster access)

```
kubeseal --fetch-cert > tools/seal-secret/pub-cert.pem
```

This is the sealed-secrets controller's **public** cert -- safe to commit,
and it's what makes every subsequent seal fully offline (no cluster access,
no live credentials, just this file). Only re-run this if the controller is
ever fully reinstalled/reset; routine key rotation keeps old private keys
around, so a cached older public cert still produces something the
controller can decrypt.

## Usage

```
echo "DATABASE_URL=postgres://..." > apps/vjencanja-backend/.env
echo "JWT_SECRET_KEY=..."          >> apps/vjencanja-backend/.env

./tools/seal-secret/seal-secret.sh vjencanja-backend
```

Writes `apps/vjencanja-backend/sealed-secret.yaml`, sealed as
`vjencanja-backend-secret` in the `default` namespace -- matching the
existing naming convention for every app already in this repo. Override
either with `-n <namespace>` / `--name <secret-name>`.

`apps/<app>/.env` is gitignored and never leaves your machine -- it's the
actual source of truth for plaintext values, deliberately kept out of git
and out of any CI secret store. Review the diff, then commit and push the
resulting `sealed-secret.yaml` yourself; ArgoCD picks it up like any other
manifest change.

## What this doesn't do

No secret rotation, expiry, or access auditing -- this only automates
building the manifest and running kubeseal correctly. The sealed-secrets
controller's private key is still a single point of failure and needs its
own backup, separate from this tool. `.github/workflows/validate-secrets.yml`
is a CI guardrail that fails the build if a plaintext `Secret` or a tracked
`.env` ever gets committed by mistake -- it holds no secret material and
never generates anything.

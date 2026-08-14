# cluster-bootstrap

Disaster recovery for the k3s node: `backup-secrets.sh` captures the only
two things that exist purely in-cluster and are gone forever if the node
dies; `bootstrap.sh` takes a freshly-installed bare Linux box back to a
fully running platform in one command. Full reasoning in
`../../docs/superpowers/specs/2026-08-14-disaster-recovery-design.md`.

## One-time setup (do this now, before you need it)

```bash
age-keygen -o bootstrap-secrets-key.txt
```

Take the `public key: age1...` line's value and put it into
`age-recipient.txt` in this directory (safe to commit -- it's public,
encryption-only). Then put the whole `bootstrap-secrets-key.txt` file's
private key into Bitwarden as a secure note, and delete the local file.
**This private key must never be committed or stored on the server** -- if
it were, it would be destroyed in the same disaster it's meant to recover
from.

## Backing up (run this now, and again only if these ever rotate)

```bash
./backup-secrets.sh
```

Writes `bootstrap-secrets.tar.gz.age`. Review it, then `git add`, commit,
and push it like any other file. This is not a scheduled job -- the two
secrets it captures (the sealed-secrets controller's private key, and
ArgoCD's own admin secret) change essentially never after initial setup.

## Recovering (on a freshly-installed bare Linux box)

```bash
git clone https://github.com/Fr1k1/k3s-homelab
cd k3s-homelab
./tools/cluster-bootstrap/bootstrap.sh
```

You'll be prompted once, for the age private key from Bitwarden -- paste
it and press Ctrl-D. That's the only manual input; everything else
installs k3s, restores the two secrets, reinstalls the platform pinned to
the exact versions that were running, and hands off to ArgoCD to rebuild
every Deployment/Service/Ingress/SealedSecret automatically. Realistic
time: tens of minutes, not seconds -- package downloads and chart installs
take real wall-clock time even fully automated.

**Before trusting this in a real incident:** do one real fire drill --
run `bootstrap.sh` against a spare machine or a throwaway VM -- since the
actual k3s/ArgoCD/sealed-secrets install sequence can only be verified
against real hardware, not in an automated test.

## What's NOT backed up, and why that's fine

Every app secret (`vjencanja-backend-secret`, `getreeba-secret`,
`ghcr-secret`, Image Updater's credentials) is already a `SealedSecret`
committed to git. The moment the sealed-secrets controller is running
again with its original private key, ArgoCD resyncing the repo recreates
all of those on its own -- nothing further to back up for them. Neither
`getreeba` nor `vjencanja-backend` has any persistent storage either; real
data lives externally in Supabase.

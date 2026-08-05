# k3s Homelab

A self-hosted Kubernetes platform, managed entirely through GitOps. This isn't a tutorial cluster — it runs two live backend services (`getreeba`, `vjencanja-backend`) behind real public domains, with every change to cluster state going through git rather than manual `kubectl` commands.

**Stack:** k3s · ArgoCD · Argo CD Image Updater · Traefik · Helm · Bitnami Sealed Secrets · kube-prometheus-stack (Prometheus + Grafana) · GHCR

---

## What this demonstrates

| Area | What's implemented |
|---|---|
| **GitOps** | ArgoCD continuously reconciles cluster state against this repo — no manual `kubectl apply` in the deploy path. Git history *is* the deployment history and rollback mechanism. |
| **Secrets management** | Credentials are never committed in plaintext. Bitnami Sealed Secrets encrypts them asymmetrically against the cluster's public key, so the ciphertext in git is only decryptable by the one controller holding the private key. |
| **Kubernetes fundamentals** | Deployments, Services, Ingress, namespacing, private registry auth (`imagePullSecrets`), all hand-written (not scaffolded by a GUI). |
| **Observability** | Prometheus + Grafana (via `kube-prometheus-stack`) running as first-class cluster citizens, exposed through the same ingress pattern as application traffic. |
| **Ingress / traffic routing** | Traefik handling host-based routing for multiple independent services and domains off a single entry point. |
| **Platform vs. application layering** | Cluster add-ons (ArgoCD, monitoring, secrets controller) installed via Helm; application workloads defined as plain manifests — a deliberate separation between "platform" and "product" concerns. |
| **App-of-apps / progressive GitOps** | Started as one flat `directory` Application; `vjencanja-backend` was deliberately carved out into its own Kustomize-aware child Application once automated image promotion needed it — expanding the pattern only where it earns its complexity, not converting the whole repo speculatively. |
| **Automated, credential-isolated CI/CD** | `vjencanja-backend` redeploys on every push to `main` with zero shared secrets between the app repo and this one — the write credential that closes the loop lives only in-cluster. See "Automated image promotion" below. |

---

## Architecture

Two ArgoCD Applications, not one — this is a deliberate hybrid, not an accident:

```
                        ┌───────────────────────────────────────────────────────┐
                        │                      k3s cluster                        │
                        │                                                          │
  git push  ──────────▶ │  homelab (Application: directory, recurse: true)        │
  (this repo)           │  watches apps/, excludes apps/vjencanja-backend/**       │
                        │       │                                                  │
                        │       ├──▶ getreeba (Deployment/Service/Ingress/Secret)  │
                        │       ├──▶ Ingress: argocd, grafana                      │
                        │       └──▶ apps/applications/vjencanja-backend.yaml      │
                        │                    │ (bootstraps a child Application)    │
                        │                    ▼                                     │
                        │       vjencanja-backend (Application: Kustomize)         │
                        │       watches apps/vjencanja-backend/ directly           │
                        │                    │                                     │
                        │                    ▼                                     │
                        │       Deployment / Service / Ingress / SealedSecret      │
                        │                    ▲                                     │
                        │                    │ digest write-back (git push)        │
                        │       Argo CD Image Updater ◀── polls ghcr.io/fosleen/…  │
                        │                                                          │
                        │  Traefik (built into k3s) ── Ingress ──▶                │
                        │  argocd.homelab.local · api.getreeba.com                 │
                        │  api.vjenchanje.com                                      │
                        └───────────────────────────────────────────────────────┘
```

**Why two Applications:** `homelab` is a plain `directory`-type source with `recurse: true` — it has no idea what a `kustomization.yaml` is and just `kubectl apply`s every file it finds. That's fine for static manifests, but Argo CD Image Updater needs a Kustomize-aware Application to rewrite an image digest declaratively, and it needs one Application per unit it independently redeploys. So `vjencanja-backend` was carved out into its own child Application — a minimal, deliberate app-of-apps pattern, not the whole repo converted to one. `getreeba` still hangs directly off `homelab` since nothing about it needs Kustomize (yet).

---

## Repo layout

```
apps/
├── argocd/                 # Ingress for the ArgoCD UI
├── applications/           # Child Application manifests (app-of-apps) — carved out of
│   └── vjencanja-backend.yaml   # homelab's direct management, e.g. so Image Updater has
│                                 # a Kustomize-aware Application to target
├── getreeba/                # Deployment, Service, Ingress, SealedSecret — managed
│                             # directly by `homelab`
├── image-updater/           # Sealed credentials Argo CD Image Updater needs: an SSH
│                             # deploy key for git write-back, a GHCR pull secret
└── vjencanja-backend/        # Deployment, Service, Ingress, SealedSecret, kustomization.yaml
                               # — managed by its own child Application (see Architecture)

tools/
└── deploy-verify/            # Go CLI, invoked from vjencanja's CI, that polls the live
                               # API until a deploy's git SHA is confirmed live — closes
                               # the loop on an otherwise fire-and-forget async GitOps chain
```

Every app's workload still follows the same pattern: `Deployment` (private GHCR image) → `Service` → `Ingress` → `SealedSecret` for config/credentials. Consistent structure across apps means onboarding a third service is a copy-and-adjust operation, not a redesign — the app-of-apps carve-out only kicks in for a service once something (like Image Updater) actually needs Kustomize-level control over it.

---

## How it works

### GitOps reconciliation
ArgoCD watches this repository and continuously diffs it against live cluster state, auto-healing any drift. Deploying a manifest change is `git push` — there's no separate deploy step to forget or get wrong.

### Secrets, done properly
Real credentials (DB URLs, JWT secrets, API keys for Resend/Supabase) never touch git in plaintext:

```
kubectl create secret generic getreeba-secret --dry-run=client -o yaml \
  | kubeseal --format yaml > apps/getreeba/sealed-secret.yaml
```

The output is a `SealedSecret` CRD — safe to commit publicly, since only the in-cluster controller's private key can turn it back into a usable `Secret`.

### Automated image promotion
`vjencanja-backend` redeploys itself on every push to `vjencanja`'s `main` branch, with no manual step and no shared credentials between the two repos:

1. CI in `vjencanja` builds and pushes `ghcr.io/fosleen/vjencanja-backend`, tagged `:latest` and `:<git-sha>`, using only its own repo's `GITHUB_TOKEN` — it never touches this repo or the cluster.
2. Argo CD Image Updater (running in-cluster, polling every ~2 min) notices `:latest`'s digest changed, and writes an `images:` override into `apps/vjencanja-backend/kustomization.yaml`, committing and pushing to this repo with its own SSH deploy key (sealed in `apps/image-updater/git-creds-sealed.yaml`) — a credential that lives only in-cluster, never in `vjencanja`'s CI.
3. The `vjencanja-backend` child Application (auto-sync, `selfHeal: true`) picks up that commit and rolls the Deployment. `maxUnavailable: 0` plus a readiness probe on `deployment.yaml` means the old pod keeps serving until the new one is confirmed healthy — a bad image never causes downtime, it just fails to roll out.
4. `vjencanja`'s CI has a final `verify-deploy` job that polls the live public endpoint for up to 10 minutes, confirming the whole async chain actually completed — without it, a break anywhere in steps 2–3 would fail silently.

Digest tracking (not a version tag) is what makes this work without a release process: the app always deploys whatever is actually behind `:latest`, no manual tag bump required. See `apps/vjencanja-backend/SETUP.md` for the one-time bootstrap this depends on.

### Platform add-ons via Helm
ArgoCD, the Sealed Secrets controller, `kube-prometheus-stack`, and Argo CD Image Updater are installed as Helm releases — standard practice for cluster infrastructure that benefits from upstream-maintained charts, versioned upgrades, and configurable values, as opposed to hand-rolled manifests. (Image Updater specifically is pinned to a pre-1.0 chart version — its 1.0 release moved primary configuration to a separate CRD and only optionally honors the Application-annotation style this repo uses, so pinning avoids silently losing config on an upgrade.)

### Bootstrapping from zero
1. Install k3s: `curl -sfL https://get.k3s.io | sh -`
2. Install Sealed Secrets controller (Helm), capture its public key.
3. Install ArgoCD (Helm), point it at this repo.
4. Install `kube-prometheus-stack` (Helm) for monitoring.
5. Let ArgoCD sync `apps/`.

---

## Roadmap

Actively planned next steps, in priority order:

1. ~~Automated image promotion~~ — done via Argo CD Image Updater (digest strategy, git write-back, app-of-apps split for `vjencanja-backend`). See "Automated image promotion" above, `apps/vjencanja-backend/SETUP.md` for the one-time bootstrap, and `vjencanja`'s `.github/workflows/api-deploy.yml` for the CI side.
2. **Clean up the stuck `monitoring` namespace** — a prior teardown left it wedged in `Terminating` (likely a `kube-prometheus-stack` CRD finalizer with no operator left to clear it); `apps/grafana/ingress.yaml` was removed so nothing keeps trying to write into it, but the namespace itself still needs a manual finalizer-clear before monitoring can come back.
3. **TLS via cert-manager** — issue certs for all ingress hosts and drop the current homelab-only HTTP setup.
4. **Shared Helm chart for app workloads** — `getreeba` and `vjencanja-backend` are structurally identical; templatizing them removes duplication as more services are added.
5. **Extend the app-of-apps split to `getreeba`** — if it ever needs its own Kustomize-aware Application (e.g. for the same kind of image automation `vjencanja-backend` now has), the pattern in `apps/applications/` is already there to copy.
6. **High availability** — multi-replica deployments with `PodDisruptionBudget`s once the cluster has more than one node.
7. **Infrastructure-as-code for cluster bootstrap** — script the steps above (Ansible or a shell bootstrap) so the whole environment is reproducible from a single command.

---

## Apps at a glance

| App | Image | Domain | Backing services | Deploys |
|---|---|---|---|---|
| `getreeba` | `ghcr.io/fosleen/getreeba` | `api.getreeba.com` | Supabase (Postgres + S3 storage), JWT auth, Resend email | Manual (`git push` to this repo) |
| `vjencanja-backend` | `ghcr.io/fosleen/vjencanja-backend` | `api.vjenchanje.com` | Supabase (Postgres + S3 storage), JWT auth, Resend email | Automatic — push to `vjencanja`'s `main` |

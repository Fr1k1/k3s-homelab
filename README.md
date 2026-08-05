# k3s Homelab

A self-hosted Kubernetes platform, managed entirely through GitOps. This isn't a tutorial cluster — it runs two live backend services (`getreeba`, `vjencanja-backend`) behind real public domains, with every change to cluster state going through git rather than manual `kubectl` commands.

**Stack:** k3s · ArgoCD · Traefik · Helm · Bitnami Sealed Secrets · kube-prometheus-stack (Prometheus + Grafana) · GHCR

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

---

## Architecture

```
                        ┌─────────────────────────────────────────┐
                        │                k3s cluster               │
                        │                                           │
  git push  ──────────▶ │  ArgoCD  ──watches this repo──▶ kubectl   │
  (this repo)           │  (GitOps controller)             apply    │
                        │       │                                    │
                        │       ▼                                    │
                        │  ┌─────────────┐  ┌─────────────┐         │
                        │  │  namespace  │  │  namespace  │         │
                        │  │  default    │  │  monitoring │         │
                        │  │             │  │             │         │
                        │  │ getreeba    │  │ Prometheus  │         │
                        │  │ vjencanja-  │  │ Grafana     │         │
                        │  │  backend    │  │             │         │
                        │  └─────────────┘  └─────────────┘         │
                        │                                           │
                        │  Traefik (built into k3s) ── Ingress ──▶  │
                        │  argocd.homelab.local                     │
                        │  grafana.homelab.local                    │
                        │  api.getreeba.com                          │
                        │  api.vjenchanje.com                        │
                        └─────────────────────────────────────────┘
```

---

## Repo layout

```
apps/
├── argocd/            # Ingress for the ArgoCD UI
├── grafana/           # Ingress for the monitoring stack's Grafana UI
├── getreeba/          # Deployment, Service, Ingress, SealedSecret
└── vjencanja-backend/ # Deployment, Service, Ingress, SealedSecret
```

Every app follows the same pattern: `Deployment` (private GHCR image) → `Service` → `Ingress` → `SealedSecret` for config/credentials. Consistent structure across apps means onboarding a third service is a copy-and-adjust operation, not a redesign.

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

### Platform add-ons via Helm
ArgoCD, the Sealed Secrets controller, and `kube-prometheus-stack` are installed as Helm releases — standard practice for cluster infrastructure that benefits from upstream-maintained charts, versioned upgrades, and configurable values, as opposed to hand-rolled manifests.

### Bootstrapping from zero
1. Install k3s: `curl -sfL https://get.k3s.io | sh -`
2. Install Sealed Secrets controller (Helm), capture its public key.
3. Install ArgoCD (Helm), point it at this repo.
4. Install `kube-prometheus-stack` (Helm) for monitoring.
5. Let ArgoCD sync `apps/`.

---

## Roadmap

Actively planned next steps, in priority order:

1. ~~Automated image promotion~~ — done via Argo CD Image Updater (digest strategy, git write-back). See `apps/vjencanja-backend/SETUP.md` for the one-time bootstrap and `vjencanja`'s `.github/workflows/api-deploy.yml` for the CI side.
2. **TLS via cert-manager** — issue certs for all ingress hosts and drop the current homelab-only HTTP setup.
3. **Shared Helm chart for app workloads** — `getreeba` and `vjencanja-backend` are structurally identical; templatizing them removes duplication as more services are added.
4. **High availability** — multi-replica deployments with `PodDisruptionBudget`s once the cluster has more than one node.
5. **Infrastructure-as-code for cluster bootstrap** — script the steps above (Ansible or a shell bootstrap) so the whole environment is reproducible from a single command.

---

## Apps at a glance

| App | Image | Domain | Backing services |
|---|---|---|---|
| `getreeba` | `ghcr.io/fosleen/getreeba` | `api.getreeba.com` | Supabase (Postgres + S3 storage), JWT auth, Resend email |
| `vjencanja-backend` | `ghcr.io/fosleen/vjencanja-backend` | `api.vjenchanje.com` | Supabase (Postgres + S3 storage), JWT auth, Resend email |

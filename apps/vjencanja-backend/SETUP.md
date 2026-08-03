# One-time setup: Argo CD Image Updater for vjencanja-backend

These steps let the cluster automatically redeploy `vjencanja-backend`
whenever a new image lands on `ghcr.io/fosleen/vjencanja-backend:latest`.
Run them once, by hand, against the live cluster - they are not GitOps
managed (same bootstrap category as the Sealed Secrets controller's own
key: something has to exist before GitOps can manage anything else).

## 1. Apply the ArgoCD Application manifest

The `vjencanja-backend` Application currently exists in the cluster only
imperatively (created by hand, no annotations). Reconcile it onto the
git-defined version, which carries the Image Updater annotations:

    kubectl apply -f apps/vjencanja-backend/application.yaml

(`kubectl apply` on a resource with no prior `last-applied-configuration`
annotation will print a one-time warning — that's expected and harmless.)

This file is intentionally NOT included in `kustomization.yaml` — it's the
Application resource itself, not something the app it points at should
manage.

## 2. Install Argo CD Image Updater

This project's Application annotations (see apps/vjencanja-backend/application.yaml)
use the pre-1.0 annotation-based configuration format. Image Updater 1.0+ moved
config to a separate ImageUpdater CRD and only honors annotations via a deprecated
compatibility flag - so this MUST be pinned to the latest pre-1.0 release, not
whatever "latest" resolves to.

    helm repo add argo https://argoproj.github.io/argo-helm
    helm repo update
    helm search repo argo/argocd-image-updater --versions
    # ^ find the highest version number below 1.0.0 in the output, then:
    helm install argocd-image-updater argo/argocd-image-updater \
      --namespace argocd \
      --version <the pre-1.0 version you found above>

## 3. Create a git deploy key for k3s-homelab

    ssh-keygen -t ed25519 -f ./image-updater-deploy-key -N "" -C "argocd-image-updater"

Add `./image-updater-deploy-key.pub` as a deploy key on the `k3s-homelab`
GitHub repo (Settings -> Deploy keys -> Add deploy key) with **write**
access checked.

Store the private key in the cluster as a plain Secret - not a
SealedSecret. This key is what makes git-write trust possible in the first
place; it never leaves the argocd namespace and is never committed:

    kubectl create secret generic git-creds \
      --namespace argocd \
      --from-file=sshPrivateKey=./image-updater-deploy-key \
      --from-literal=type=git \
      --from-literal=url=git@github.com:Fr1k1/k3s-homelab.git \
      --dry-run=client -o yaml \
      | kubectl label -f - --local -o yaml argocd.argoproj.io/secret-type=repository \
      | kubectl apply -f -

    rm ./image-updater-deploy-key ./image-updater-deploy-key.pub

## 4. Give Image Updater read access to the private GHCR image

    kubectl create secret docker-registry image-updater-ghcr \
      --namespace argocd \
      --docker-server=ghcr.io \
      --docker-username=<your-github-username> \
      --docker-password=<a GHCR PAT with read:packages>

Wire both secrets into the Image Updater Helm release per the installed
chart version's `values.yaml` (`git.credentials` / `registries.conf` keys
have moved between chart versions - check `helm show values argo/argocd-image-updater`
against what's actually deployed before assuming a specific key name).

## 5. Verify

    kubectl logs -n argocd deploy/argocd-image-updater -f

Push a change to `vjencanja`'s `main` branch touching `api/**`, and watch
this log for a line noting a new digest was found and written back to
`k3s-homelab`.

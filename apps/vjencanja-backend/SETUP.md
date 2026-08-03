# One-time setup: Argo CD Image Updater for vjencanja-backend

These steps let the cluster automatically redeploy `vjencanja-backend`
whenever a new image lands on `ghcr.io/fosleen/vjencanja-backend:latest`.
Run them once, by hand, against the live cluster - they are not GitOps
managed (same bootstrap category as the Sealed Secrets controller's own
key: something has to exist before GitOps can manage anything else).

## 1. Hand `apps/vjencanja-backend/` off to its own Application

There is no per-service ArgoCD Application in this cluster — only one
umbrella `homelab` Application (`source.directory.recurse: true` over the
whole `apps/` path), which applies every file it finds as a raw manifest.
It has no idea what a `kustomization.yaml` is, so left alone it tries to
`kubectl apply` that file directly and fails outright (`Kubernetes API
could not find kustomize.config.k8s.io/Kustomization`) — which blocks its
*entire* sync, not just this one resource.

So `apps/vjencanja-backend/` needs to be carved out from `homelab`'s direct
management and handed to its own child Application instead. That's why the
Application manifest itself lives at `apps/applications/vjencanja-backend.yaml`
— a sibling location `homelab` still applies directly — rather than inside
`apps/vjencanja-backend/` alongside the files it's taking over.

Exclude the directory from `homelab`'s recursive scan:

    kubectl patch application homelab -n argocd --type merge \
      -p '{"spec":{"source":{"directory":{"recurse":true,"exclude":"vjencanja-backend/**"}}}}'

Confirm it took (look for an `exclude:` line under `spec.source.directory`):

    kubectl get application homelab -n argocd -o yaml | grep -A3 "directory:"

Commit and push the relocated `apps/applications/vjencanja-backend.yaml` (removed
from `apps/vjencanja-backend/`) to `k3s-homelab`'s `master` branch, if you
haven't already. Once `homelab` re-syncs (automated, or force it with
`kubectl patch application homelab -n argocd --type merge -p '{"operation":{"sync":{}}}'`),
it picks up the relocated file and creates the `vjencanja-backend`
Application — which *does* correctly auto-detect `kustomization.yaml`,
since it sits directly at that child Application's own `source.path`.

The previously-existing `vjencanja-backend` Deployment/Service/Ingress/SealedSecret
(currently managed directly by `homelab`) get silently re-labeled to the new
child Application on its first sync — expected, not destructive; they're
the same live objects, just changing which Application tracks them.

## 2. Install Argo CD Image Updater

This project's Application annotations (see apps/applications/vjencanja-backend.yaml)
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

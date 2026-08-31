# `config-reloader`: a Kubebuilder operator that auto-restarts Deployments on ConfigMap/Secret change

## Problem

Kubernetes doesn't roll a Deployment when a ConfigMap or Secret it consumes
changes — the pod template text is unchanged, so neither the Deployment
controller nor ArgoCD has any reason to restart running pods. Today this is
handled by hand (`kubectl rollout restart deployment/...`) whenever a config
or secret is updated in this homelab. This project builds a small operator
that does it automatically, opt-in per Deployment, functioning like a
minimal version of the OSS tool [Reloader](https://github.com/stakater/Reloader).

**Why build it instead of installing Reloader:** the goal isn't the
automation itself (Reloader already solves that well) — it's hands-on
experience with the actual Kubebuilder/controller-runtime patterns
(reconcile loops, secondary watches via mapping functions, CRDs with status
subresources, RBAC generation) for an interview where "hands-on familiarity
with Kubernetes operators, controllers, or custom resources" is the
first-listed core requirement.

**Constraint:** roughly one day of work. Every scope decision below is
deliberately biased toward "fully understood and explainable" over
"maximally featureful."

## Decision: CRD, not annotation-only

The design went through one real pivot worth recording. The first pass used
a Deployment annotation as the sole opt-in mechanism (`reloader.homelab/watch:
configmap:foo,secret:bar`), with no new API type at all — simpler, less
scaffolding, closer to how Reloader itself actually works.

That was rejected in favor of a CRD (`ReloadTrigger`) because:

- The job requirement explicitly names "custom resources" as one of three
  things to have hands-on familiarity with — an annotation demonstrates
  none of that.
- A CRD gives a demoable artifact (`kubectl get reloadtriggers`) instead of
  something only visible via `kubectl describe deployment` — a meaningfully
  better live-interview demo.
- It's a real, load-bearing use of spec/status — not decorative. `spec`
  declares intent (which Deployment, which ConfigMaps/Secrets to watch);
  `status` reports genuinely observed state (hash, last reload time, a
  `Ready` condition) that only the controller can know. That's the litmus
  test for "is this CRD actually earning its complexity," and it passes.

The trade-off accepted: opting a Deployment in now means adding a new CR
manifest rather than one annotation line — marginally more GitOps surface,
but the more idiomatic, more explainable answer.

## Architecture

**Custom resource: `ReloadTrigger`** (group `reloader.homelab.dev`, version
`v1alpha1`, namespaced). One instance per Deployment that wants auto-restart
behavior.

```yaml
apiVersion: reloader.homelab.dev/v1alpha1
kind: ReloadTrigger
metadata:
  name: vjencanja-backend-config
  namespace: default
spec:
  targetDeployment: vjencanja-backend
  watch:
    - kind: Secret
      name: vjencanja-backend-secret
status:
  observedHash: sha256:...
  lastReloadTime: "2026-08-05T10:00:00Z"
  conditions:
    - type: Ready
      status: "True"
      reason: HashUnchanged
      message: "No drift detected"
```

(Ground truth checked against the live manifests: `apps/vjencanja-backend/deployment.yaml` and its
`SealedSecret` both run in namespace `default` — this repo doesn't give either app its own
namespace. There's also no ConfigMap anywhere in this repo today, only the one Secret consumed via
`envFrom.secretRef` — so the real instance watches a Secret only. The `Kind` field in `spec.watch`
still supports `ConfigMap` generically; that path is exercised by the fake-client unit tests even
though nothing live uses it yet.)

**Primary reconciled type:** `ReloadTrigger`.

**Secondary watches:** `ConfigMap`, `Secret` — via
`Watches(&corev1.ConfigMap{}, handler.EnqueueRequestsFromMapFunc(mapFn))`.
`mapFn` lists `ReloadTrigger` objects in the changed object's namespace,
filters to ones whose `spec.watch` references it by kind+name, and enqueues
a reconcile request for each match. This is the core pattern worth being
able to explain: a change to resource A (ConfigMap) triggers reconciliation
of resource B (ReloadTrigger), because B is what owns the response.

### Reconcile logic, per `ReloadTrigger`

1. Fetch the `ReloadTrigger`. Not found → was deleted, nothing to clean up
   (see "No finalizer" below), return.
2. Resolve `spec.watch` — fetch each referenced ConfigMap/Secret from the
   `ReloadTrigger`'s own namespace. Any missing → set condition
   `Ready=False, reason=ResourceNotFound`, return an error (gets requeued
   with backoff).
3. Compute a single sha256 hash over all referenced resources' `.data` /
   `.binaryData`, combined (sorted by kind+name first, for determinism).
4. Compare to `status.observedHash`:
   - **Empty** (first reconcile since creation) → write the hash to
     `status.observedHash`, condition `Ready=True, reason=Initialized`, **no
     restart**. Opting a Deployment in must not restart an already-healthy
     Deployment just because a `ReloadTrigger` was created for it.
   - **Present, differs** → fetch `spec.targetDeployment` (missing →
     `Ready=False, reason=DeploymentNotFound`, error, requeue). Patch its
     `spec.template.metadata.annotations` with a new
     `reloader.homelab.dev/configHash` and
     `reloader.homelab.dev/restartedAt` (RFC3339 timestamp) — the same
     mechanism `kubectl rollout restart` uses, so the existing
     `maxUnavailable: 0` / readiness-probe safety on vjencanja-backend's
     rollout applies unchanged. Update `status.observedHash`,
     `status.lastReloadTime`, condition `Ready=True, reason=Reloaded`.
     Record a Kubernetes `Event` (`involvedObject` = the target Deployment)
     describing what triggered it.
   - **Present, matches** → no-op. Condition stays `Ready=True,
     reason=HashUnchanged`. Reconcile must be safely re-runnable any number
     of times with no side effect when nothing changed.

### No finalizer

Deleting a `ReloadTrigger` has no external side effect to reverse — it
doesn't create anything outside the cluster, it only reads ConfigMaps/Secrets
and patches a Deployment it doesn't own. Garbage collection alone is
correct; adding a finalizer here would be applying a pattern to a problem it
doesn't solve.

## RBAC

Cluster-wide (`ClusterRole`/`ClusterRoleBinding`), generated from kubebuilder
markers, since `ReloadTrigger` instances can live in any namespace:

- `reloader.homelab.dev/reloadtriggers` — get, list, watch, update
- `reloader.homelab.dev/reloadtriggers/status` — get, update, patch
  (separate subresource permission — standard split between editing spec
  and the controller-only status write path)
- core `configmaps`, `secrets` — get, list, watch (read-only; the
  controller never writes these)
- `apps/deployments` — get, list, watch, patch
- core `events` — create, patch

## Repo layout

```
operators/config-reloader/
├── go.mod
├── Dockerfile
├── Makefile                              # kubebuilder-generated
├── api/v1alpha1/
│   └── reloadtrigger_types.go            # Spec/Status structs + generated deepcopy
├── controllers/
│   ├── reloadtrigger_controller.go       # the reconciler
│   └── reloadtrigger_controller_test.go  # fake-client unit tests
└── config/
    ├── crd/                              # generated CRD YAML
    ├── rbac/                             # generated Role/ClusterRole YAML
    ├── manager/                          # controller Deployment
    └── default/                          # kustomize base combining the above —
                                           # this is what ArgoCD points at directly

apps/applications/config-reloader.yaml    # new child Application, same shape as
                                           # vjencanja-backend.yaml; source:
                                           # operators/config-reloader/config/default

apps/vjencanja-backend/reload-trigger.yaml  # ReloadTrigger CR instance, opts
                                             # vjencanja-backend in
```

This mirrors the existing vjencanja-backend app-of-apps pattern exactly:
`operators/config-reloader/config/default` is Kubebuilder's own Kustomize
output, so ArgoCD points straight at generated manifests rather than
hand-written ones — same "point ArgoCD at a Kustomize dir" move already used
for vjencanja-backend, aimed at generated output instead of hand-written.

**Bootstrap ordering note:** on first sync, if `apps/vjencanja-backend`'s
`ReloadTrigger` manifest reaches ArgoCD before `config-reloader`'s CRD is
installed, that one resource will show a transient sync error until the CRD
exists, then self-heal on the next auto-sync pass. Same category of
"assumed vs. actual" gap the `homelab`/Kustomize mismatch documented in
`CICD.md` — worth naming up front rather than being surprised by it live.

## Image delivery

`ghcr.io/fr1k1/config-reloader` (this repo's own GHCR namespace, not
`fosleen`'s — the controller's source lives in this repo, so there's no
cross-repo credential problem to solve the way vjencanja-backend's pipeline
does). A GitHub Actions workflow builds and pushes on changes to
`operators/config-reloader/**`, tagged `:latest` and `:<short-sha>`.

**No Argo CD Image Updater wiring** for this controller's own image — image
tag in `config/manager/kustomization.yaml` gets bumped by hand when the
controller actually changes, which will be rare after today. Zero-touch
redeploy automation is a solved, demonstrated problem already
(vjencanja-backend); re-proving it here would spend the one-day budget on
something that doesn't teach anything new.

## Testing

Fake-client (`sigs.k8s.io/controller-runtime/pkg/client/fake`) unit tests
against the reconciler — no envtest, to protect the time budget for the live
cluster deploy. Cases:

1. First reconcile, no prior `status.observedHash` → hash gets written,
   condition `reason=Initialized`, **no** Deployment patch.
2. Watched ConfigMap content changes (hash differs from
   `status.observedHash`) → Deployment pod template gets `configHash` +
   `restartedAt` patched, status updates, condition `reason=Reloaded`, Event
   recorded.
3. No change (hash matches) → no patch, no-op, condition stays
   `reason=HashUnchanged`. Proves idempotency.
4. Referenced ConfigMap/Secret missing → no panic, error returned (get
   requeued), condition `Ready=False, reason=ResourceNotFound`.
5. `spec.targetDeployment` missing → same shape, condition
   `Ready=False, reason=DeploymentNotFound`.
6. Two resources referenced, only one changes → combined hash still
   changes, restart still triggers — proves the hash covers *all* watched
   resources combined, not tracked per-resource.

**Explicitly not tested:** RBAC enforcement (fake client doesn't enforce
it; real coverage needs envtest, out of scope) and the `Watches`/mapping-
function wiring itself (controller-runtime's own machinery — verified live
against the real cluster instead, where it actually matters).

## Out of scope

- **StatefulSets/DaemonSets** — Deployments only. Same pattern would
  extend; not worth today's time.
- **Cross-namespace watch targets** — a `ReloadTrigger` can only reference
  resources in its own namespace. Simpler RBAC, and nothing in the current
  setup needs more.
- **Admission webhook for spec validation** — CRD-level
  `+kubebuilder:validation` markers (required fields, enum on `kind`) cover
  basic validation without a running webhook server.
- **Leader election tuning** — left at Kubebuilder's default. Inert with a
  single replica, but correct boilerplate; not worth spending time on.
- **`getreeba`** — no `ReloadTrigger` instance today (the user may delete
  this service). Trivial to add later — just a new CR manifest once the CRD
  already exists.
- **CRD versioning/conversion, multiple API versions** — `v1alpha1` only.

## Open questions for the implementation plan

None outstanding — all scope decisions above were made collaboratively
during brainstorming. The next step is a detailed implementation plan
(file-by-file, in dependency order) via the `writing-plans` skill.

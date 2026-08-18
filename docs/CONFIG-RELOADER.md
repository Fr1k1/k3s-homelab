# config-reloader: how it works, and how to explain it in an interview

This is a reference doc for explaining `operators/config-reloader` out loud
— in an interview, a design review, or to yourself six months from now
having forgotten the details. It follows the same shape as `CICD.md`: what
was built, why each piece exists, and the real judgment calls made getting
here. Read top-to-bottom for the full story, or jump to "Anticipated
questions" at the end for talking points.

Related: `superpowers/specs/2026-08-05-config-reloader-design.md` (the
original design decisions and their rationale), `superpowers/plans/2026-08-05-config-reloader.md`
(the file-by-file build plan), `../README.md` (repo overview).

---

## The problem, in one sentence

Kubernetes doesn't roll a Deployment when a ConfigMap or Secret it consumes
changes — the pod template text is unchanged, so nothing tells the
Deployment controller to restart anything. This project builds a small,
from-scratch operator that does it automatically: opt a Deployment in by
creating a `ReloadTrigger` custom resource, and any change to the
ConfigMaps/Secrets it lists triggers a real, safe rolling restart.

**Why build it instead of installing the existing tool that already does
this** (Reloader, by Stakater): the goal isn't the automation — it's
hands-on experience with the actual Kubebuilder/controller-runtime patterns
(reconcile loops, secondary watches, CRDs with status subresources, RBAC
generation) that the job requirement "hands-on familiarity with Kubernetes
operators, controllers, or custom resources" is actually asking about.

---

## The one-sentence architecture

A single controller reconciles a namespaced CRD, `ReloadTrigger`
(`reloader.homelab.dev/v1alpha1`). Its `spec` declares intent (which
Deployment to restart, which ConfigMaps/Secrets to watch); its `status`
reports what the controller actually observed (a content hash, when it last
restarted something, a `Ready` condition). On every reconcile, the
controller re-hashes the watched resources' current content and compares it
to the hash it saw last time — a mismatch means "something changed," which
triggers a Deployment pod-template patch (the same mechanism
`kubectl rollout restart` uses).

---

## Why a CRD, not just an annotation

The first design pass used a plain annotation on the Deployment itself
(`reloader.homelab.dev/watch: configmap:foo,secret:bar`) — simpler, no new
API type, and closer to how the real Reloader tool works. That got replaced
with a CRD deliberately, for reasons worth being able to defend out loud:

- The job posting explicitly names **custom resources** as one of three
  things to have hands-on familiarity with. An annotation demonstrates none
  of that.
- A CRD gives a real, demoable artifact — `kubectl get reloadtriggers`
  shows `TARGET`/`READY`/`REASON`/`LAST RELOAD`/`AGE` columns at a glance,
  versus needing `kubectl describe deployment` and reading annotations to
  understand an annotation-only design.
- It's a **load-bearing** use of a CRD, not a decorative one — the litmus
  test for whether a CRD is "real": does `spec` declare genuine intent, and
  does `status` report genuine observed state only the controller could
  know? Here, yes to both. A CRD that's just a fancy ConfigMap (nothing in
  `status` that the controller actually computed) would have been the wrong
  call even with the CV pressure to "use a CRD somewhere."

The trade-off accepted: opting a Deployment in now means writing a new
`ReloadTrigger` manifest rather than adding one annotation line to an
existing Deployment. Marginally more GitOps surface, for a meaningfully
better interview answer.

---

## File-by-file walkthrough

### `api/v1alpha1/` — the CRD's Go type definitions

- **`groupversion_info.go`** — registers the API group/version
  (`reloader.homelab.dev/v1alpha1`) with Kubernetes' type system, so a
  generic client can turn decoded JSON/YAML bytes into a Go
  `*ReloadTrigger` struct. Every custom resource needs this file; it's
  almost entirely boilerplate.
- **`reloadtrigger_types.go`** — the actual API definition. Three things
  worth being able to explain cold:
  1. **The spec/status split.** `ReloadTriggerSpec` is what a human/GitOps
     pipeline writes down (desired state); `ReloadTriggerStatus` is what
     only the controller can write (observed state — a hash, a timestamp,
     a condition). This split, backed by `+kubebuilder:subresource:status`,
     is what makes something a genuine control loop instead of a config
     file with extra steps.
  2. **Validation markers become real API-server validation.**
     `+kubebuilder:validation:Enum=ConfigMap;Secret` and `MinItems=1`
     aren't comments — `controller-gen` compiles them into the CRD's
     OpenAPI schema, so a malformed `ReloadTrigger` gets rejected by the
     Kubernetes API server itself, before this controller's code ever
     runs.
  3. **Printer columns are what make `kubectl get` useful.** The
     `+kubebuilder:printcolumn` markers are the entire reason
     `kubectl get reloadtriggers` shows meaningful columns instead of just
     `NAME`/`AGE`.
- **`zz_generated.deepcopy.go`** — 100% generated by `controller-gen`,
  never hand-edited (the `zz_` prefix is a convention signaling "sorts to
  the bottom, don't touch"). Implements `DeepCopyObject()`, which
  Kubernetes' machinery requires so objects can be safely cloned across
  goroutine boundaries (the informer cache) without one goroutine's mutation
  corrupting another's view. Regenerate via:
  ```
  controller-gen object paths="./api/v1alpha1"
  ```
- **`reloadtrigger_types_test.go`** — one real test: deep-copy a
  `ReloadTrigger`, mutate the copy's `Watch` slice, assert the original is
  untouched. This is the test that would catch a *shallow* copy bug (one
  that copies the slice header but shares the backing array) — a subtle,
  real bug class in generated code, not a box-ticking test.

### `controllers/` — the reconcile loop

- **`hash.go`** — pure functions, no Kubernetes client involved. Takes the
  fetched content of every watched ConfigMap/Secret and reduces it to one
  deterministic sha256 hex digest. "Deterministic" requires two explicit
  sorting passes (resources by kind+name, each resource's own keys) because
  Go's map iteration order is randomized per-process — skip either sort and
  the hash changes on every call even when content hasn't, which would make
  the controller think something changed on every single reconcile.
- **`reloadtrigger_controller.go`** — the reconciler itself. The whole
  `Reconcile` method is a three-way branch on comparing a freshly computed
  hash to `status.observedHash`:
  1. **No previous hash** (`ObservedHash == ""`) → record a baseline, no
     restart. Opting an already-healthy Deployment in must not itself
     cause a restart.
  2. **Hash unchanged** → true no-op. This is what makes the whole loop
     safe to run redundantly — reconciling with nothing changed produces
     zero side effects.
  3. **Hash changed** → patch the target Deployment's **pod template**
     annotations (`spec.template.metadata.annotations`, not the
     Deployment's own top-level annotations) with the new hash and a
     timestamp. This is the same trick `kubectl rollout restart` uses:
     changing anything in the pod template gives the Deployment a new
     pod-template hash, which the built-in Deployment controller treats as
     "roll a new ReplicaSet" — the existing `maxUnavailable`/readiness-probe
     safety on the target Deployment governs the rollout, entirely for
     free, without reimplementing any rollout logic.

  The second pattern worth being able to whiteboard: **`SetupWithManager`
  declares one primary watch (`ReloadTrigger`) and two secondary watches
  (`ConfigMap`, `Secret`)**. The secondary watches don't reconcile
  ConfigMaps/Secrets directly — `handler.EnqueueRequestsFromMapFunc(r.mapToTriggers)`
  runs on every ConfigMap/Secret event cluster-wide, and `mapToTriggers`
  translates "this ConfigMap changed" into "here are the `ReloadTrigger`
  object(s) that reference it" by namespace-scoped listing and filtering.
  This is the actual mechanism behind "watch a resource for changes" in
  controller-runtime, and it's the single most reusable pattern in this
  codebase — the same shape backs things like "restart a Deployment when
  its ServiceAccount's token Secret rotates" or "reconcile an Ingress when
  the Certificate it references changes."

  RBAC markers (`+kubebuilder:rbac:...` comments directly above
  `Reconcile`) are the actual source of truth for `config/rbac/role.yaml` —
  `controller-gen rbac` compiles them into the generated `ClusterRole`.
  There's no second place permissions are declared; get a verb wrong in the
  comment and the generated role is wrong.

- **`reloadtrigger_controller_test.go`** — fake-client (in-memory, no real
  API server) tests covering all three `Reconcile` branches plus two
  not-found failure modes plus the `mapToTriggers` mapping logic directly.
  Deliberately **not** using `envtest` (a real `kube-apiserver`+`etcd`) —
  a scope decision made explicit in the design spec to protect the
  one-day time budget; the trade-off is not exercising real admission/RBAC
  enforcement.

### `main.go` / `logging.go` — the manager entrypoint

Wires everything above into a running process: builds a `runtime.Scheme`
(registers both the built-in Kinds this controller touches and the one
custom `ReloadTrigger` Kind), constructs a `ctrl.Manager` (the object
owning the shared informer cache and controller lifecycle), registers
`/healthz`/`/readyz` HTTP checks (which the Deployment's own liveness/
readiness probes call), and starts the reconciler. `LeaderElection: false`
is deliberate, not an oversight — the manager runs a single replica, so
there's no second instance to coordinate leadership with.

### `Dockerfile`

Multi-stage: a full Go toolchain image builds a static binary
(`CGO_ENABLED=0`), then a `distroless/static:nonroot` image runs it —
no shell, no package manager, smallest reasonable attack surface. The
`go.mod`/`go.sum`-before-source-copy ordering is a deliberate Docker
layer-cache trick: dependency downloads are cached across rebuilds as long
as `go.mod`/`go.sum` themselves don't change.

### `config/` — the Kustomize deployment manifests

Four bases combined by `config/default/kustomization.yaml`:
- `config/crd/` — the generated `ReloadTrigger` CRD (teaches the API
  server the Kind exists at all).
- `config/rbac/` — the generated `ClusterRole`, its `ClusterRoleBinding`,
  and the controller's `ServiceAccount`.
- `config/manager/` — the controller's own `Deployment`.
- `config/default/` — the root: sets a shared `namespace:` and
  `namePrefix:` that Kustomize applies to everything pulled in above,
  including fixing up cross-references automatically (a `ClusterRoleBinding`'s
  `subjects[0].name` gets renamed in lockstep with the `ServiceAccount` it
  points at).

`apps/applications/config-reloader.yaml` points ArgoCD directly at
`config/default` — the same "point ArgoCD at a Kustomize dir" move already
used for `vjencanja-backend`, just aimed at Kubebuilder's own generated
output instead of hand-written manifests.

### `apps/vjencanja-backend/reload-trigger.yaml`

The one `ReloadTrigger` instance actually in use: watches
`vjencanja-backend-secret` (the only credential-bearing resource that
Deployment consumes today — there's no ConfigMap in this repo) and targets
the `vjencanja-backend` Deployment, both in namespace `default` (this repo
doesn't give either app its own namespace).

### `.github/workflows/config-reloader.yml`

Tests, then builds and pushes `ghcr.io/fr1k1/config-reloader` on push to
`master`. Deliberately simpler than `vjencanja`'s CI (no `verify-deploy`
job) — that job exists there to close an *asynchronous* gap across two
separate GitHub repos with no shared credentials; this pipeline has neither
problem, and rolling out a new image here is a manual tag bump, not a
zero-touch redeploy, so there's no async chain to verify.

---

## How to test, end to end

**Locally, right now — no cluster needed:**
```
cd operators/config-reloader
go build ./...
go vet ./...
go test ./... -v
```
All 12 tests should pass (1 deepcopy test, 5 hash tests, 6 reconciler/
mapping tests). This is the fake-client layer — fast, deterministic, no
external dependencies.

**Rendering the manifests without a cluster** (useful to sanity-check
Kustomize composition before anything touches git):
```
kubectl kustomize operators/config-reloader/config/default
kubectl kustomize apps/vjencanja-backend
```

**After you push** (nothing reaches the cluster before this — ArgoCD is
this repo's only deploy path):
1. Watch `homelab` and the new `config-reloader` Application sync in the
   ArgoCD UI. A transient error on `apps/vjencanja-backend`'s
   `ReloadTrigger` — if it syncs before the CRD lands — is expected and
   should self-heal on the next auto-sync pass.
2. `kubectl get reloadtriggers -A` — `vjencanja-backend-config` should show
   `READY=True`, `REASON=Initialized`, no `LAST RELOAD` yet (first
   reconcile is baseline-only, by design).
3. `kubectl logs -n config-reloader-system deploy/config-reloader-controller-manager`
   — confirm the manager started and `/healthz`/`/readyz` are responding
   (pod should show `Ready`).
4. **The real end-to-end proof:** rotate one key in
   `vjencanja-backend-secret` (reseal and push, following this repo's
   existing sealed-secrets workflow in the README). Then confirm:
   - `kubectl get reloadtriggers` shows `REASON=Reloaded` and a real
     `LAST RELOAD` timestamp.
   - `kubectl get pods -l app=vjencanja-backend` shows a new pod (check its
     `AGE`).
   - `kubectl get events --field-selector reason=ConfigReloaded` shows the
     recorded Event.
   - The vjencanja-backend API stayed up throughout — same
     `maxUnavailable: 0` zero-downtime guarantee `CICD.md` documents for
     image rollouts applies here too, since this triggers the exact same
     rolling-update mechanism, just from a different trigger.

---

## Anticipated questions

**"Walk me through what happens when a Secret changes."**
The Secret update event fires on the controller's secondary watch. That
event goes through `mapToTriggers`, which lists every `ReloadTrigger` in
the Secret's namespace and finds the ones whose `spec.watch` names that
Secret — here, `vjencanja-backend-config`. That produces a
`Reconcile` request for that one object. `Reconcile` re-fetches the
Secret's *current* content (not the content from the event — the event is
only a trigger, never a payload), hashes it, sees it differs from
`status.observedHash`, and patches `vjencanja-backend`'s pod template with
a new hash + timestamp annotation. The built-in Deployment controller sees
the pod template changed and rolls a new ReplicaSet, governed by the
Deployment's own existing rollout strategy.

**"Why not just restart the Deployment directly (e.g. delete its pods)?"**
Patching the pod template and letting the Deployment controller do the
rollout means this operator inherits the target's own
`maxUnavailable`/`maxSurge`/readiness-probe configuration for free. Deleting
pods directly would bypass all of that and risk a moment with zero healthy
replicas — the same zero-downtime guarantee the rest of this repo already
depends on for image rollouts.

**"Why compute a hash instead of just comparing resourceVersion?"**
`resourceVersion` changes on *any* write, including ones that don't change
`data` (e.g. metadata/label edits, or the API server's own internal
bookkeeping) — using it would cause spurious restarts unrelated to actual
content drift. Hashing `.data`/`.binaryData` directly means only real
content changes matter.

**"What's the actual difference between this and just installing
Reloader?"** None, functionally, for this narrow use case — Reloader is
more mature, supports more workload types (StatefulSet, DaemonSet), and has
an "auto-detect from pod spec" mode this project doesn't. The point of
building it wasn't to out-engineer Reloader; it was to have a codebase
small enough to fully understand and defend line-by-line, which is a
different (and for an interview, more valuable) goal than "solve the
problem the fastest way."

**"What would you do differently at scale, or with more time?"**
- `mapToTriggers` does a full namespace-scoped `List` + in-memory filter on
  every ConfigMap/Secret event. Fine at homelab scale; at real scale, a
  `client.IndexField` on the watched-resource name would make this an
  indexed lookup instead.
- No `envtest` coverage — fake-client tests don't exercise real RBAC
  enforcement or admission validation. Worth adding if this graduated
  beyond a portfolio project.
- No support for StatefulSet/DaemonSet targets, and no cross-namespace
  watch targets — both narrow scope decisions made explicitly to fit a
  one-day build, not fundamental limitations of the design.
- `getreeba` has no `ReloadTrigger` yet — the pattern is proven on one
  service and ready to copy, not applied repo-wide speculatively (mirrors
  the same "prove it on one service first" judgment call already made for
  Image Updater, per `CICD.md`).

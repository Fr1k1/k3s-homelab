# config-reloader — study guide

Written for you specifically: strong on Kubernetes/GitOps/Docker/Helm
already, **this is your first Go code and your first operator**. So this
skips explaining what a Deployment or a Secret is, and spends its time on
what's actually new — Go syntax, and how a controller/CRD actually works
under the hood. Goal: be able to talk through this confidently in a
Platform Software Engineer interview, not recite it from memory.

Related: `../operators/config-reloader/` (the code, now heavily commented
— read it alongside this), `superpowers/specs/2026-08-05-config-reloader-design.md`
(why a CRD, not an annotation), `../README.md` (how this fits the rest of
the repo).

---

## The 60-second pitch (say this if asked "tell me about a project")

"Kubernetes doesn't restart a Deployment when a ConfigMap or Secret it
uses changes — I'd been doing `kubectl rollout restart` by hand after every
credential rotation. I built a small Kubernetes operator from scratch in Go
— not installed an existing one — that watches a custom resource I
defined, `ReloadTrigger`, which declares 'watch this Secret, restart that
Deployment.' When the Secret's content changes, the controller hashes it,
notices the hash differs from last time, and patches the target
Deployment's pod template to trigger a normal rolling restart — same
mechanism `kubectl rollout restart` uses, so it inherits the Deployment's
existing zero-downtime rollout config for free. It's live in my homelab
cluster today, watching a real production secret."

---

## Go, for someone who's never written it

Scoped tightly to what this codebase actually uses — not a Go course.

- **No classes.** `type ReloadTriggerReconciler struct { ... }` is just a
  data container. Behavior gets attached separately via **methods with a
  receiver**: `func (r *ReloadTriggerReconciler) Reconcile(...)` reads as
  "Reconcile is a method on `*ReloadTriggerReconciler`; call it `r.Reconcile(...)`."
- **Pointers are explicit and common.** `*T` is "pointer to T," `&x` takes
  the address of `x`. A pointer receiver (`func (r *Foo) Method()`) means
  the method can mutate the actual struct, not a copy — used everywhere
  here because reconcilers and their embedded client need to stay the same
  instance across calls. `*metav1.Time` (a pointer, not a plain value) is
  also how this codebase represents "optional" — `nil` means "never set,"
  a normal `Time` value can't represent "absent."
- **No exceptions.** Every fallible call returns two things:
  `value, err := someCall()`. The universal idiom is
  `if err != nil { return err }` (or wrapped with context:
  `fmt.Errorf("fetching secret %s: %w", name, err)`). This is the single
  biggest "feels different" thing coming from Python/JS try/catch — errors
  are just regular return values you're expected to check every time, not
  a control-flow jump.
- **Interfaces are satisfied implicitly** — there's no `implements`
  keyword. Any struct with the right methods automatically satisfies an
  interface. `ReloadTriggerReconciler` embeds `client.Client` (a field with
  no name, just the type) — that's Go's version of "has-a, but call its
  methods like they're your own": `r.Get(...)`, `r.Update(...)`,
  `r.Status()` all come from the embedded client, not code written in this
  project.
- **Zero values are meaningful.** An unset `string` is `""`, not `null`/
  `undefined` — that's exactly why `trigger.Status.ObservedHash == ""` is
  the real check for "has this ReloadTrigger ever reconciled before."
- **`:=`** declares and assigns in one step (type inferred); `var x Type`
  declares explicitly. Both appear throughout.
- **Slices** (`[]T`, resizable) and **maps** (`map[K]V`) are the two
  collection types you'll see — `[]WatchedResource`, `map[string][]byte`.
- **Tests are just functions**: `func TestSomething(t *testing.T)`,
  `t.Fatalf(...)` fails and stops that test. Run with `go test ./...`. No
  separate test framework/assertion library — this codebase deliberately
  uses none.

---

## The big idea: an operator is a loop you write yourself

You already trust a control loop you never had to write: a Deployment's
built-in controller constantly compares its `spec` to reality and
reconciles ReplicaSets/Pods to match. This project is you writing that
exact same kind of loop, for a `Kind` you invented.

| Something you already know | Its operator equivalent | Where |
|---|---|---|
| Deployment's controller reconciles Pods to match `spec` | `ReloadTriggerReconciler.Reconcile` does the same for `ReloadTrigger` | `controllers/reloadtrigger_controller.go` |
| ArgoCD diffs desired (git) vs. actual (cluster) and corrects drift | `Reconcile` diffs a freshly computed hash (actual) against `status.observedHash` (last known) and corrects drift | same file |
| Helm charts (e.g. `kube-prometheus-stack`) ship CRDs you never hand-wrote | `controller-gen` generates *this* CRD's YAML from Go struct + comment markers — same idea, different generator | `api/v1alpha1/reloadtrigger_types.go` → `config/crd/bases/*.yaml` |
| RBAC `Role`/`RoleBinding` objects you'd otherwise hand-write | Generated from `+kubebuilder:rbac` comments directly above `Reconcile` — one source of truth, not two | `config/rbac/role.yaml` |
| A Deployment only reacts to changes on itself | This controller *also* reacts to changes on other objects (ConfigMap/Secret) — a "secondary watch," mapped back to the right `ReloadTrigger` | `mapToTriggers`, same file |

**Custom Resource Definition (CRD), plainly:** Kubernetes ships with
built-in Kinds (`Deployment`, `Secret`, ...). A CRD teaches the API server
one more Kind that *you* define — here, `ReloadTrigger`, with whatever
fields you want (`spec.targetDeployment`, `spec.watch`). Once the CRD is
applied, `kind: ReloadTrigger` YAML works exactly like any built-in
object: `kubectl get/apply/describe` all just work.

**Reconcile loop, plainly:** a function that gets called whenever the
object it manages — or something it's watching — changes. Every call, it
re-derives everything from *current real state*, never from "what just
changed." That's what makes it safe to call redundantly, and it's the
actual pattern interviewers mean by "write a controller."

---

## The one flow to have memorized

Every `Reconcile` call is exactly one of three branches:

1. **No hash recorded yet** → this is the first reconcile since the
   `ReloadTrigger` was created. Record a baseline hash. **Don't restart** —
   opting a healthy Deployment in must not itself cause a restart.
2. **Hash unchanged** → true no-op. Nothing to do. This is what makes the
   loop safe to run any number of times.
3. **Hash changed** → patch the target Deployment's *pod template*
   annotations (not the Deployment's own top-level ones) with a new hash +
   timestamp. That's the exact trick `kubectl rollout restart` uses —
   Kubernetes sees the pod template changed and rolls a new ReplicaSet,
   governed by whatever rollout strategy that Deployment already has.

And separately: **how a Secret/ConfigMap change even reaches this loop** —
the controller also watches `ConfigMap`/`Secret` directly (a "secondary
watch"). On any such change, `mapToTriggers` lists every `ReloadTrigger` in
that namespace and finds the ones referencing the changed object by
kind+name, then enqueues a `Reconcile` call for each match.

---

## Where to look in the code

Every file already has thorough inline comments explaining the *why* — use
this table to navigate, not to avoid opening the files.

| File | What it is | Read if... |
|---|---|---|
| `controllers/reloadtrigger_controller.go` | The reconcile loop itself | You only read one file, read this |
| `controllers/hash.go` | Turns ConfigMap/Secret content into one deterministic hash | You want the "how do we detect drift" answer |
| `api/v1alpha1/reloadtrigger_types.go` | The CRD's Go types — `spec`/`status`, validation markers | You want to explain the CRD design |
| `main.go` | Wires the manager, scheme, and reconciler together, starts the process | You want the "how does this actually run" answer |
| `config/crd/`, `config/rbac/`, `config/manager/` | Generated + hand-written Kubernetes manifests that deploy the above | You want to explain how it gets into the cluster |
| `*_test.go` files | Fake-client tests, no real cluster needed | You want to explain how you tested it |

---

## How to prove it works

```
cd operators/config-reloader
go test ./...                                    # 12 tests, no cluster needed
kubectl kustomize config/default                  # renders the full manifest set
```

Live, once deployed: `kubectl get reloadtriggers -A` (status at a glance),
`kubectl describe deployment vjencanja-backend` (see the restart
annotations after a real change), `kubectl get events --field-selector reason=ConfigReloaded`.

---

## Interview cheat sheet

**"Walk me through what happens when a Secret changes."**
Secondary watch fires → `mapToTriggers` finds the `ReloadTrigger`(s)
referencing it → `Reconcile` runs, re-fetches the Secret's *current*
content (never trusts the event payload), hashes it, sees it differs from
`status.observedHash`, patches the target Deployment's pod template →
built-in Deployment controller rolls a new ReplicaSet under its own
existing strategy.

**"Why hash the content instead of comparing `resourceVersion`?"**
`resourceVersion` changes on *any* write (labels, internal bookkeeping),
not just content — using it would cause restarts unrelated to real drift.

**"Why patch the pod template instead of just deleting pods?"**
Patching inherits the Deployment's own `maxUnavailable`/readiness-probe
safety for free. Deleting pods directly bypasses all of that.

**"Why a CRD instead of an annotation on the Deployment?"** (the real
design pivot on this project) — an annotation demonstrates nothing about
custom resources, which the job spec explicitly names. A CRD gives a real,
demoable artifact (`kubectl get reloadtriggers`) and is a genuine
spec/status split, not decoration — full reasoning in the design spec.

**"What would you do differently at scale?"** `mapToTriggers` does a full
namespace List + filter on every event — fine at homelab scale, would
become a field index (`client.IndexField`) at real scale. No `envtest`
coverage (fake-client only), no StatefulSet/DaemonSet support, no
cross-namespace watch targets — all explicit one-day scope cuts, not
oversights.

**"What's the actual difference between this and just installing
Reloader (the existing OSS tool)?"** Functionally, for this narrow case:
nothing — Reloader is more mature and does more. The point wasn't to
out-engineer it; it was to build something small enough to defend
line-by-line, which is the more valuable thing to have in an interview.

---

## Questions I asked while learning this (running log)

Every question asked about this project during the build gets logged here.

### Why do we need `go.mod` and `go.sum`, and how are they created?

`go.mod` declares the module's identity, Go version, and every
dependency pinned to one exact version — Go's dependency resolution reads
it to decide precisely what to compile against. `go.sum` is a
cryptographic hash of every dependency's actual downloaded content,
re-verified on every build, so a compromised proxy can't silently swap in
different code.

Created via `go mod init config-reloader`, then populated by `go get
<pkg>@<version>`. **A real mistake worth retelling:** I ran `go mod tidy`
once before any `.go` files existed — with zero imports anywhere yet, it
decided every pinned dependency was "unused" and stripped them all out.
The next build failed; a second `tidy` (now with real imports) re-resolved
onto the *latest* versions instead of my original pins, since the pin
info was already gone. Fixed by explicitly re-running `go get` with the
intended versions once source files existed. Lesson: `tidy` only reflects
what your code *currently* imports — run it too early and it can't tell
"not yet used" from "unused."

### Does the controller's GHCR image need an `imagePullSecret`? Can it stay public?

No, and public is the right call. A pull secret only matters for a
*private* image; this one is just the compiled controller binary, nothing
sensitive. That's a deliberate, explainable difference from
`vjencanja-backend`/`getreeba`, which legitimately are private (real
application code) and do need `ghcr-secret`.

I initially flagged this as a bug — GHCR packages default to private on
first push, and the existing `ghcr-secret` is sealed into namespace
`default` only, so it wouldn't even be visible from `config-reloader-system`
if reused. Both facts are true in general; the package just turned out to
already be public, so there was nothing to fix. Checked the real GitHub
package settings to confirm rather than guessing either way.

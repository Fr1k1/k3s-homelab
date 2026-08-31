package controllers

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"

	reloaderv1alpha1 "config-reloader/api/v1alpha1"
)

// Annotations the controller patches onto the *target Deployment's pod
// template* (spec.template.metadata.annotations, not the Deployment's own
// top-level metadata). This is the same trick `kubectl rollout restart`
// uses: changing anything in the pod template — even just an annotation —
// gives the Deployment a new pod-template hash, which the Deployment
// controller treats as "roll a new ReplicaSet." We get a real, safe
// rolling update (governed by the target's own maxUnavailable/readiness
// probes) entirely for free, without reimplementing any of that logic
// ourselves.
const (
	configHashAnnotation  = "reloader.homelab.dev/configHash"
	restartedAtAnnotation = "reloader.homelab.dev/restartedAt"

	conditionTypeReady = "Ready"
)

// ReloadTriggerReconciler reconciles a ReloadTrigger object.
//
// Embedding client.Client (rather than naming the field, e.g. `Client
// client.Client`) means every method call on the reconciler — r.Get,
// r.Update, r.Status(), r.List — is actually a call straight through to the
// embedded client. It's a common controller-runtime idiom: the reconciler
// *is* a thin wrapper around "a client, plus an event recorder," not its
// own abstraction layer on top of them.
type ReloadTriggerReconciler struct {
	client.Client
	Recorder record.EventRecorder
}

// The five +kubebuilder:rbac markers below are the actual source of truth
// for this operator's permissions — controller-gen reads them and
// generates config/rbac/role.yaml from them (Task 3 of the build
// regenerated that file straight from these comments). Get the verbs wrong
// here and the generated ClusterRole is wrong; there's no second place
// permissions are declared.
//
//   - reloadtriggers (get/list/watch/update/patch): standard read+reconcile
//     access to the primary resource.
//   - reloadtriggers/status (get/update/patch), listed *separately* from
//     the line above: because ReloadTrigger has `+kubebuilder:subresource:
//     status` (see reloadtrigger_types.go), status is a distinct HTTP
//     resource with its own RBAC — this line is what actually authorizes
//     r.Status().Update(...) below.
//   - configmaps;secrets (get/list/watch only, no write verbs): the
//     controller only ever *reads* the resources it watches. It should be
//     structurally impossible for a bug in this code to modify someone's
//     Secret.
//   - deployments (get/list/watch/patch): patch is what makes the restart
//     mechanism work; list/watch back the secondary-watch/cache machinery.
//   - events (create/patch): needed for r.Recorder.Eventf below to work at
//     all — recording an Event is itself a write to the API server.
//
// +kubebuilder:rbac:groups=reloader.homelab.dev,resources=reloadtriggers,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=reloader.homelab.dev,resources=reloadtriggers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=configmaps;secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile is the whole control loop. controller-runtime calls it once per
// ReloadTrigger whenever: (a) that ReloadTrigger itself changes, or (b) a
// ConfigMap/Secret it references changes (via mapToTriggers, below) — the
// caller doesn't distinguish between the two; req just tells us which
// ReloadTrigger to reconcile, and Reconcile re-derives everything else from
// current cluster state. This "level-based," not "edge-based," design is
// deliberate and idiomatic: Reconcile never trusts *why* it was called, only
// *what's true right now* — which is also what makes it safe to call
// redundantly (a missed or duplicate event is never a correctness bug here,
// only a slightly wasted reconcile).
func (r *ReloadTriggerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var trigger reloaderv1alpha1.ReloadTrigger
	if err := r.Get(ctx, req.NamespacedName, &trigger); err != nil {
		// client.IgnoreNotFound turns a real "does not exist" error into a
		// nil error. That's correct here: if the ReloadTrigger was
		// deleted, there's nothing to reconcile and nothing to clean up
		// (no finalizer — deleting a ReloadTrigger has no external side
		// effect to reverse), so "object's gone" isn't a failure.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Step 1: fetch the actual current content of everything spec.watch
	// references, and reduce it to one hash. See resolveWatched and
	// hash.go's computeHash.
	contents, err := r.resolveWatched(ctx, req.Namespace, trigger.Spec.Watch)
	if err != nil {
		// A missing ConfigMap/Secret is reported on the ReloadTrigger's own
		// status (Ready=False) and returned as an error, which makes
		// controller-runtime requeue this reconcile automatically with
		// exponential backoff — no manual retry/backoff logic needed here.
		return r.setNotReady(ctx, &trigger, "ResourceNotFound", err.Error())
	}
	newHash := computeHash(contents)

	// Step 2: the three-way branch that is the entire point of this
	// controller.
	switch {
	case trigger.Status.ObservedHash == "":
		// Case A — first reconcile since this ReloadTrigger was created.
		// Deliberately does NOT restart the Deployment: opting an
		// already-healthy Deployment into auto-reload must not itself
		// cause a restart. We only ever have something to compare
		// *against* starting from the second reconcile onward, so this
		// first pass just records a baseline.
		trigger.Status.ObservedHash = newHash
		meta.SetStatusCondition(&trigger.Status.Conditions, metav1.Condition{
			Type: conditionTypeReady, Status: metav1.ConditionTrue,
			Reason: "Initialized", Message: "Baseline hash recorded, no restart triggered",
		})
		if err := r.Status().Update(ctx, &trigger); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil

	case newHash == trigger.Status.ObservedHash:
		// Case B — nothing changed since last time. This branch is what
		// makes Reconcile idempotent: it can run any number of times with
		// no watched content change and produce zero side effects (no
		// Deployment patch), only refreshing the condition's timestamp.
		// TestReconcile_NoChangeIsNoOp is the test that pins this down.
		meta.SetStatusCondition(&trigger.Status.Conditions, metav1.Condition{
			Type: conditionTypeReady, Status: metav1.ConditionTrue,
			Reason: "HashUnchanged", Message: "No drift detected",
		})
		if err := r.Status().Update(ctx, &trigger); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Case C — the hash changed. Everything below only runs on real drift.
	var deploy appsv1.Deployment
	deployKey := types.NamespacedName{Namespace: req.Namespace, Name: trigger.Spec.TargetDeployment}
	if err := r.Get(ctx, deployKey, &deploy); err != nil {
		if apierrors.IsNotFound(err) {
			return r.setNotReady(ctx, &trigger, "DeploymentNotFound", err.Error())
		}
		// Any other error (e.g. a transient API server hiccup) isn't a
		// "this ReloadTrigger is misconfigured" problem, so it doesn't get
		// written to status — it's just returned, and controller-runtime
		// requeues.
		return ctrl.Result{}, err
	}

	// This is the actual restart mechanism: patch the pod template's own
	// annotations (not the Deployment's top-level annotations — see the
	// const block's comment above for why that distinction matters).
	//
	// A real client.Patch (strategic merge, via MergeFrom against the
	// object as fetched above) rather than a full r.Update - matches the
	// +kubebuilder:rbac marker above, which only ever granted "patch" on
	// deployments (not "update"). This used to call r.Update, which
	// silently required a verb the ClusterRole never had, so every
	// reconcile past this point failed with a Forbidden error and no
	// Deployment ever actually got restarted.
	original := deploy.DeepCopy()
	if deploy.Spec.Template.Annotations == nil {
		deploy.Spec.Template.Annotations = map[string]string{}
	}
	deploy.Spec.Template.Annotations[configHashAnnotation] = newHash
	deploy.Spec.Template.Annotations[restartedAtAnnotation] = time.Now().UTC().Format(time.RFC3339)
	if err := r.Patch(ctx, &deploy, client.MergeFrom(original)); err != nil {
		return ctrl.Result{}, err
	}

	// Only after the Deployment patch has actually succeeded do we record
	// the new hash and "Reloaded" on the ReloadTrigger's status. If the
	// process crashed between the two r.Update/r.Status().Update calls,
	// the next reconcile would see ObservedHash still stale, recompute the
	// same newHash, and safely re-attempt the same patch — Case C is
	// naturally idempotent too, since re-patching the same annotation
	// values onto an already-restarted Deployment is a no-op write.
	trigger.Status.ObservedHash = newHash
	now := metav1.Now()
	trigger.Status.LastReloadTime = &now
	meta.SetStatusCondition(&trigger.Status.Conditions, metav1.Condition{
		Type: conditionTypeReady, Status: metav1.ConditionTrue,
		Reason: "Reloaded", Message: fmt.Sprintf("Restarted %s after watched content changed", deploy.Name),
	})
	if err := r.Status().Update(ctx, &trigger); err != nil {
		return ctrl.Result{}, err
	}

	// A Kubernetes Event, visible via `kubectl describe deployment
	// vjencanja-backend` / `kubectl get events`. This is the closest
	// analog this project has to the classic "owner reference" pattern:
	// we don't *create* any object that needs an owner reference for
	// garbage collection (we only mutate a pre-existing Deployment), but
	// we do want an audit trail tying "why did this pod restart" back to
	// the ReloadTrigger that caused it — an Event's involvedObject does
	// that without implying any ownership/lifecycle relationship.
	r.Recorder.Eventf(&deploy, corev1.EventTypeNormal, "ConfigReloaded",
		"Restarted by ReloadTrigger %s after watched resource content changed", trigger.Name)
	logger.Info("triggered rolling restart", "deployment", deploy.Name, "trigger", trigger.Name)

	return ctrl.Result{}, nil
}

// resolveWatched turns spec.watch (a list of kind+name references) into
// actual fetched content. Any single missing resource fails the whole
// reconcile — a ReloadTrigger watching 3 resources where 1 is missing
// reports NotReady rather than silently hashing the other 2, since that
// could mask a real misconfiguration (e.g. a typo'd Secret name) as
// "everything's fine."
func (r *ReloadTriggerReconciler) resolveWatched(ctx context.Context, namespace string, watch []reloaderv1alpha1.WatchedResource) ([]watchedContent, error) {
	contents := make([]watchedContent, 0, len(watch))
	for _, w := range watch {
		key := types.NamespacedName{Namespace: namespace, Name: w.Name}
		switch w.Kind {
		case reloaderv1alpha1.WatchedResourceKindConfigMap:
			var cm corev1.ConfigMap
			if err := r.Get(ctx, key, &cm); err != nil {
				return nil, fmt.Errorf("fetching configmap %s: %w", w.Name, err)
			}
			contents = append(contents, configMapContent(w.Name, &cm))
		case reloaderv1alpha1.WatchedResourceKindSecret:
			var s corev1.Secret
			if err := r.Get(ctx, key, &s); err != nil {
				return nil, fmt.Errorf("fetching secret %s: %w", w.Name, err)
			}
			contents = append(contents, secretContent(w.Name, &s))
		default:
			// Unreachable in practice — the CRD's OpenAPI schema (the
			// Enum marker on WatchedResource.Kind) already rejects any
			// other value at admission time. Kept as a defensive default
			// rather than a panic, since "the API server's validation
			// might not be enforced" (e.g. --validate=false in a weird
			// setup) is cheap insurance against a very confusing crash.
			return nil, fmt.Errorf("unknown watched resource kind %q", w.Kind)
		}
	}
	return contents, nil
}

// setNotReady is the one place that writes Ready=False. Centralizing it
// means every "this ReloadTrigger can't currently do its job" path — a
// missing watched resource, a missing target Deployment — reports through
// the exact same condition shape, which is what makes `kubectl get
// reloadtriggers` a reliable at-a-glance health check across every failure
// mode, not just the ones someone remembered to wire up.
//
// It also always returns a non-nil error, deliberately: returning an error
// from Reconcile is what tells controller-runtime "retry this with
// backoff," which is exactly the right behavior for "the Secret doesn't
// exist yet" (it might show up any second, e.g. mid-GitOps-sync) or "the
// API server hiccuped."
func (r *ReloadTriggerReconciler) setNotReady(ctx context.Context, trigger *reloaderv1alpha1.ReloadTrigger, reason, message string) (ctrl.Result, error) {
	meta.SetStatusCondition(&trigger.Status.Conditions, metav1.Condition{
		Type: conditionTypeReady, Status: metav1.ConditionFalse,
		Reason: reason, Message: message,
	})
	if err := r.Status().Update(ctx, trigger); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, fmt.Errorf("%s: %s", reason, message)
}

// SetupWithManager wires the controller into the manager. This is where the
// "primary vs. secondary watch" pattern actually gets declared:
//
//   - For(&ReloadTrigger{}) — the primary watch. Every create/update/delete
//     of a ReloadTrigger enqueues a Reconcile call for that exact object.
//     This part is unremarkable; almost every controller has exactly one
//     of these.
//   - Watches(&ConfigMap{}, ...) / Watches(&Secret{}, ...) — secondary
//     watches. This is the genuinely interesting controller-runtime
//     pattern this project exists to demonstrate: ConfigMaps and Secrets
//     are resources this controller cares about but does NOT reconcile
//     directly (there's no "ConfigMap controller" here). Instead,
//     EnqueueRequestsFromMapFunc(r.mapToTriggers) runs on every
//     ConfigMap/Secret event cluster-wide, and mapToTriggers below
//     translates "this ConfigMap changed" into "here are the
//     ReloadTrigger(s) that care" — the actual mechanism behind "watch a
//     ConfigMap for changes" despite ConfigMap never being the reconciled
//     type.
func (r *ReloadTriggerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&reloaderv1alpha1.ReloadTrigger{}).
		Watches(&corev1.ConfigMap{}, handler.EnqueueRequestsFromMapFunc(r.mapToTriggers)).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(r.mapToTriggers)).
		Complete(r)
}

// mapToTriggers is the map function referenced above. Given one changed
// ConfigMap or Secret, it returns the list of Reconcile requests that
// change should trigger — zero, one, or (in principle) many, if multiple
// ReloadTriggers in the same namespace happened to reference the same
// resource.
//
// It works by brute-force listing every ReloadTrigger in the changed
// object's namespace and checking each one's spec.watch for a match. For a
// homelab-scale cluster (a handful of ReloadTriggers per namespace) that's
// perfectly fine; at real scale you'd add a client-side field index
// (mgr.GetFieldIndexer().IndexField) keyed on watched-resource-name so this
// becomes an indexed lookup instead of a full List+filter — a good "what
// would you do differently at scale" answer if asked.
func (r *ReloadTriggerReconciler) mapToTriggers(ctx context.Context, obj client.Object) []ctrl.Request {
	var kind reloaderv1alpha1.WatchedResourceKind
	switch obj.(type) {
	case *corev1.ConfigMap:
		kind = reloaderv1alpha1.WatchedResourceKindConfigMap
	case *corev1.Secret:
		kind = reloaderv1alpha1.WatchedResourceKindSecret
	default:
		// Can't happen given how Watches() is wired above, but a map
		// function returning nil is always safe — controller-runtime
		// just enqueues nothing.
		return nil
	}

	var triggers reloaderv1alpha1.ReloadTriggerList
	if err := r.List(ctx, &triggers, client.InNamespace(obj.GetNamespace())); err != nil {
		// A map function has no error return — if the List call itself
		// fails (e.g. cache not synced yet), the safest behavior is
		// "trigger nothing this time" rather than blocking or panicking;
		// the next event for this same object will simply try again.
		return nil
	}

	var requests []ctrl.Request
	for _, t := range triggers.Items {
		for _, w := range t.Spec.Watch {
			if w.Kind == kind && w.Name == obj.GetName() {
				requests = append(requests, ctrl.Request{
					NamespacedName: types.NamespacedName{Namespace: t.Namespace, Name: t.Name},
				})
				break
			}
		}
	}
	return requests
}

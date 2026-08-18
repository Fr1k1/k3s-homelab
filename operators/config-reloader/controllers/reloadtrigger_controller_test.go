package controllers

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	reloaderv1alpha1 "config-reloader/api/v1alpha1"
)

// These tests use controller-runtime's fake client — an in-memory
// implementation of client.Client backed by a plain object tracker, not a
// real API server. That's a deliberate scope decision (see the design
// spec): it's fast and needs no external binaries, at the cost of not
// exercising real API-server behavior like admission webhooks, defaulting,
// or RBAC enforcement. The alternative, envtest, spins up a real
// kube-apiserver+etcd for genuinely realistic tests — overkill for a
// one-day project, and explicitly out of scope here.

// newTestScheme builds a runtime.Scheme with every Kind these tests touch
// registered — corev1 (ConfigMap/Secret), appsv1 (Deployment), and our own
// reloaderv1alpha1 (ReloadTrigger). The fake client needs this to know how
// to encode/decode/deep-copy each type; forgetting one here produces a
// runtime "no kind is registered" panic, not a compile error.
func newTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("adding corev1 to scheme: %v", err)
	}
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatalf("adding appsv1 to scheme: %v", err)
	}
	if err := reloaderv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("adding reloaderv1alpha1 to scheme: %v", err)
	}
	return scheme
}

// newReconciler builds a fresh fake-client-backed reconciler pre-seeded
// with objs. WithStatusSubresource(&ReloadTrigger{}) matters: it tells the
// fake client to treat status as a distinct sub-resource (mirroring the
// real API server's behavior for any type with
// +kubebuilder:subresource:status) — without it, r.Status().Update would
// silently behave just like r.Update, and a bug that wrote to the wrong
// one wouldn't be caught by these tests.
func newReconciler(t *testing.T, objs ...client.Object) (*ReloadTriggerReconciler, client.Client) {
	t.Helper()
	c := fake.NewClientBuilder().
		WithScheme(newTestScheme(t)).
		WithStatusSubresource(&reloaderv1alpha1.ReloadTrigger{}).
		WithObjects(objs...).
		Build()
	return &ReloadTriggerReconciler{Client: c, Recorder: record.NewFakeRecorder(10)}, c
}

// baseDeployment/baseSecret/baseTrigger are shared fixtures modeling the
// real objects this operator targets in this repo — same names/namespace
// as the actual apps/vjencanja-backend manifests, so the tests double as
// documentation of what's really deployed.
func baseDeployment() *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "vjencanja-backend", Namespace: "default"},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "vjencanja-backend"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "vjencanja-backend"}},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "vjencanja-backend", Image: "ghcr.io/fosleen/vjencanja-backend:latest"}},
				},
			},
		},
	}
}

func baseSecret(data map[string][]byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "vjencanja-backend-secret", Namespace: "default"},
		Data:       data,
	}
}

func baseTrigger() *reloaderv1alpha1.ReloadTrigger {
	return &reloaderv1alpha1.ReloadTrigger{
		ObjectMeta: metav1.ObjectMeta{Name: "vjencanja-backend-config", Namespace: "default"},
		Spec: reloaderv1alpha1.ReloadTriggerSpec{
			TargetDeployment: "vjencanja-backend",
			Watch: []reloaderv1alpha1.WatchedResource{
				{Kind: reloaderv1alpha1.WatchedResourceKindSecret, Name: "vjencanja-backend-secret"},
			},
		},
	}
}

// reconcileTrigger runs one Reconcile call for the fixture ReloadTrigger
// and re-fetches it afterward, so tests can assert on the resulting
// status without repeating this boilerplate five times.
func reconcileTrigger(t *testing.T, r *ReloadTriggerReconciler) reloaderv1alpha1.ReloadTrigger {
	t.Helper()
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "default", Name: "vjencanja-backend-config"},
	})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	var got reloaderv1alpha1.ReloadTrigger
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "vjencanja-backend-config"}, &got); err != nil {
		t.Fatalf("fetching ReloadTrigger after reconcile: %v", err)
	}
	return got
}

func readyCondition(t *testing.T, trigger reloaderv1alpha1.ReloadTrigger) metav1.Condition {
	t.Helper()
	for _, c := range trigger.Status.Conditions {
		if c.Type == conditionTypeReady {
			return c
		}
	}
	t.Fatal("no Ready condition found on ReloadTrigger status")
	return metav1.Condition{}
}

// Covers Reconcile's Case A: opting a Deployment in must not itself
// restart it. Asserts both halves — the status side (hash recorded,
// reason=Initialized) and the Deployment side (no restartedAt annotation
// appeared).
func TestReconcile_FirstRunRecordsBaselineWithoutRestart(t *testing.T) {
	deploy := baseDeployment()
	secret := baseSecret(map[string][]byte{"DATABASE_URL": []byte("postgres://original")})
	trigger := baseTrigger()

	r, c := newReconciler(t, deploy, secret, trigger)
	got := reconcileTrigger(t, r)

	if got.Status.ObservedHash == "" {
		t.Fatal("expected ObservedHash to be set on first reconcile")
	}
	if cond := readyCondition(t, got); cond.Reason != "Initialized" {
		t.Fatalf("expected condition reason Initialized, got %q", cond.Reason)
	}

	var gotDeploy appsv1.Deployment
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "vjencanja-backend"}, &gotDeploy); err != nil {
		t.Fatalf("fetching deployment: %v", err)
	}
	if _, ok := gotDeploy.Spec.Template.Annotations[restartedAtAnnotation]; ok {
		t.Fatal("expected no restart on first reconcile (opt-in must not restart an already-healthy Deployment)")
	}
}

// Covers Reconcile's Case C: the actual "the operator's job" test. Two
// reconciles — a baseline pass, then a real secret rotation in between —
// and asserts the restart mechanism (pod template annotations) and the
// status update both actually happened, with matching hash values.
func TestReconcile_ContentChangeTriggersRestart(t *testing.T) {
	deploy := baseDeployment()
	secret := baseSecret(map[string][]byte{"DATABASE_URL": []byte("postgres://original")})
	trigger := baseTrigger()

	r, c := newReconciler(t, deploy, secret, trigger)
	reconcileTrigger(t, r) // baseline

	var liveSecret corev1.Secret
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "vjencanja-backend-secret"}, &liveSecret); err != nil {
		t.Fatalf("fetching secret: %v", err)
	}
	liveSecret.Data["DATABASE_URL"] = []byte("postgres://rotated")
	if err := c.Update(context.Background(), &liveSecret); err != nil {
		t.Fatalf("updating secret: %v", err)
	}

	got := reconcileTrigger(t, r)

	if cond := readyCondition(t, got); cond.Reason != "Reloaded" {
		t.Fatalf("expected condition reason Reloaded, got %q", cond.Reason)
	}
	if got.Status.LastReloadTime == nil {
		t.Fatal("expected LastReloadTime to be set after a restart")
	}

	var gotDeploy appsv1.Deployment
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "vjencanja-backend"}, &gotDeploy); err != nil {
		t.Fatalf("fetching deployment: %v", err)
	}
	if _, ok := gotDeploy.Spec.Template.Annotations[restartedAtAnnotation]; !ok {
		t.Fatal("expected restartedAt annotation to be patched onto pod template after content change")
	}
	if gotDeploy.Spec.Template.Annotations[configHashAnnotation] != got.Status.ObservedHash {
		t.Fatal("expected pod template configHash annotation to match the new ObservedHash")
	}
}

// Covers Reconcile's Case B: reconciling twice with nothing changed in
// between must be a true no-op — no Deployment patch. This is the test
// that would fail if the hash comparison were ever accidentally inverted
// or dropped.
func TestReconcile_NoChangeIsNoOp(t *testing.T) {
	deploy := baseDeployment()
	secret := baseSecret(map[string][]byte{"DATABASE_URL": []byte("postgres://original")})
	trigger := baseTrigger()

	r, c := newReconciler(t, deploy, secret, trigger)
	reconcileTrigger(t, r) // baseline

	got := reconcileTrigger(t, r) // no change in between

	if cond := readyCondition(t, got); cond.Reason != "HashUnchanged" {
		t.Fatalf("expected condition reason HashUnchanged, got %q", cond.Reason)
	}

	var gotDeploy appsv1.Deployment
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "vjencanja-backend"}, &gotDeploy); err != nil {
		t.Fatalf("fetching deployment: %v", err)
	}
	if _, ok := gotDeploy.Spec.Template.Annotations[restartedAtAnnotation]; ok {
		t.Fatal("expected no restart when content hasn't changed")
	}
}

// A watched Secret that doesn't exist must surface as a visible,
// requeue-triggering failure (Ready=False/ResourceNotFound) rather than a
// silent no-op that could hide a typo'd resource name indefinitely.
func TestReconcile_MissingWatchedSecretSetsNotReady(t *testing.T) {
	deploy := baseDeployment()
	trigger := baseTrigger() // references a secret that doesn't exist

	r, _ := newReconciler(t, deploy, trigger)
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "default", Name: "vjencanja-backend-config"},
	})
	if err == nil {
		t.Fatal("expected an error (for requeue) when a watched Secret is missing")
	}

	var got reloaderv1alpha1.ReloadTrigger
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "vjencanja-backend-config"}, &got); err != nil {
		t.Fatalf("fetching ReloadTrigger: %v", err)
	}
	cond := readyCondition(t, got)
	if cond.Status != metav1.ConditionFalse || cond.Reason != "ResourceNotFound" {
		t.Fatalf("expected Ready=False/ResourceNotFound, got status=%s reason=%s", cond.Status, cond.Reason)
	}
}

// Same idea, but for the target Deployment rather than a watched resource
// — a distinct failure reason (DeploymentNotFound) so `kubectl get
// reloadtriggers` tells these two misconfigurations apart. Pre-seeds a
// non-empty ObservedHash so the reconcile actually reaches Case C (the
// Deployment lookup) instead of stopping at Case A.
func TestReconcile_MissingTargetDeploymentSetsNotReady(t *testing.T) {
	secret := baseSecret(map[string][]byte{"DATABASE_URL": []byte("postgres://original")})
	trigger := baseTrigger() // targets a deployment that doesn't exist
	trigger.Status.ObservedHash = "stale-hash-forcing-a-diff-path"

	r, _ := newReconciler(t, secret, trigger)
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "default", Name: "vjencanja-backend-config"},
	})
	if err == nil {
		t.Fatal("expected an error (for requeue) when the target Deployment is missing")
	}

	var got reloaderv1alpha1.ReloadTrigger
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "vjencanja-backend-config"}, &got); err != nil {
		t.Fatalf("fetching ReloadTrigger: %v", err)
	}
	cond := readyCondition(t, got)
	if cond.Status != metav1.ConditionFalse || cond.Reason != "DeploymentNotFound" {
		t.Fatalf("expected Ready=False/DeploymentNotFound, got status=%s reason=%s", cond.Status, cond.Reason)
	}
}

// The one test that exercises mapToTriggers directly rather than through a
// full Reconcile — proves the secondary-watch machinery itself: given a
// changed Secret, it must find only the ReloadTrigger(s) that actually
// reference it by kind+name, not every ReloadTrigger in the namespace.
func TestMapToTriggers_FindsTriggersReferencingChangedSecret(t *testing.T) {
	trigger := baseTrigger()
	other := baseTrigger()
	other.Name = "unrelated-trigger"
	other.Spec.Watch = []reloaderv1alpha1.WatchedResource{
		{Kind: reloaderv1alpha1.WatchedResourceKindSecret, Name: "some-other-secret"},
	}

	r, _ := newReconciler(t, trigger, other)

	requests := r.mapToTriggers(context.Background(), baseSecret(nil))

	if len(requests) != 1 {
		t.Fatalf("expected exactly 1 request, got %d: %+v", len(requests), requests)
	}
	if requests[0].Name != "vjencanja-backend-config" {
		t.Fatalf("expected request for vjencanja-backend-config, got %q", requests[0].Name)
	}
}

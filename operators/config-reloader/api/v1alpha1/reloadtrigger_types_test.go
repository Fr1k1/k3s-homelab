package v1alpha1

import "testing"

// TestReloadTrigger_DeepCopy exercises the generated zz_generated.deepcopy.go
// (see that file — it's controller-gen output, never hand-edited). The
// specific bug this guards against: a *shallow* copy would copy the Watch
// slice header (pointer + length) but not its backing array, so mutating
// copied.Spec.Watch[0] would silently corrupt the original object too.
// controller-runtime's caches and informers rely on DeepCopy being
// genuinely deep — this is what makes it safe for multiple goroutines to
// read/mutate their "own" copy of a cached object concurrently.
func TestReloadTrigger_DeepCopy(t *testing.T) {
	original := &ReloadTrigger{
		Spec: ReloadTriggerSpec{
			TargetDeployment: "vjencanja-backend",
			Watch: []WatchedResource{
				{Kind: WatchedResourceKindSecret, Name: "vjencanja-backend-secret"},
			},
		},
	}

	copied := original.DeepCopy()
	copied.Spec.Watch[0].Name = "mutated"

	if original.Spec.Watch[0].Name != "vjencanja-backend-secret" {
		t.Fatalf("DeepCopy shared underlying slice: mutating copy changed original to %q", original.Spec.Watch[0].Name)
	}
}

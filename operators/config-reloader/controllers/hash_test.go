package controllers

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// The hash tests below deliberately test computeHash as a pure function —
// no Kubernetes client, no fake, nothing async. This is the fastest,
// least-flaky layer of the test pyramid for this project: if the hashing
// logic itself is wrong, we want that to fail in milliseconds here, not
// buried inside a slower reconciler test in reloadtrigger_controller_test.go.

// Guards against a real bug class: a hash function that (accidentally)
// depends on Go's randomized map iteration order, or on the order the
// caller happened to build the input slice in.
func TestComputeHash_DeterministicRegardlessOfInputOrder(t *testing.T) {
	a := watchedContent{Kind: "ConfigMap", Name: "cfg", Data: map[string][]byte{"key": []byte("value")}}
	b := watchedContent{Kind: "Secret", Name: "sec", Data: map[string][]byte{"token": []byte("secret")}}

	h1 := computeHash([]watchedContent{a, b})
	h2 := computeHash([]watchedContent{b, a})

	if h1 != h2 {
		t.Fatalf("hash depends on input order: %q vs %q", h1, h2)
	}
}

// The whole reason this function exists: prove it actually detects drift.
func TestComputeHash_ChangesWhenDataChanges(t *testing.T) {
	before := []watchedContent{{Kind: "ConfigMap", Name: "cfg", Data: map[string][]byte{"key": []byte("value")}}}
	after := []watchedContent{{Kind: "ConfigMap", Name: "cfg", Data: map[string][]byte{"key": []byte("changed")}}}

	if computeHash(before) == computeHash(after) {
		t.Fatal("hash did not change when data content changed")
	}
}

// The Reconcile loop calls computeHash on every single reconcile (even
// no-op ones) and compares against the previously stored hash — if the
// function weren't stable across repeated calls on identical input, every
// reconcile would look like a change and the controller would restart the
// Deployment in an infinite loop.
func TestComputeHash_SameForIdenticalContent(t *testing.T) {
	resources := []watchedContent{{Kind: "Secret", Name: "sec", Data: map[string][]byte{"a": []byte("1"), "b": []byte("2")}}}

	if computeHash(resources) != computeHash(resources) {
		t.Fatal("hash is not stable across repeated calls on identical input")
	}
}

// A ReloadTrigger can watch more than one resource (spec.watch is a list) —
// this proves the combined hash reacts to a change in *any* one of them,
// not just the first/last.
func TestComputeHash_TwoResourcesOnlyOneChanges(t *testing.T) {
	base := []watchedContent{
		{Kind: "ConfigMap", Name: "cfg", Data: map[string][]byte{"key": []byte("value")}},
		{Kind: "Secret", Name: "sec", Data: map[string][]byte{"token": []byte("secret")}},
	}
	changed := []watchedContent{
		{Kind: "ConfigMap", Name: "cfg", Data: map[string][]byte{"key": []byte("value")}},
		{Kind: "Secret", Name: "sec", Data: map[string][]byte{"token": []byte("rotated")}},
	}

	if computeHash(base) == computeHash(changed) {
		t.Fatal("combined hash did not change when only one of two watched resources changed")
	}
}

// Proves configMapContent actually merges both of a ConfigMap's data
// fields (Data and BinaryData) rather than silently dropping one — a
// ReloadTrigger watching a ConfigMap that only uses BinaryData should
// still detect drift.
func TestConfigMapContent_CombinesDataAndBinaryData(t *testing.T) {
	cm := &corev1ConfigMapFixture
	content := configMapContent("cfg", cm)

	if string(content.Data["text-key"]) != "text-value" {
		t.Fatalf("expected Data key carried over, got %q", content.Data["text-key"])
	}
	if string(content.Data["binary-key"]) != "binary-value" {
		t.Fatalf("expected BinaryData key carried over, got %q", content.Data["binary-key"])
	}
}

var corev1ConfigMapFixture = corev1.ConfigMap{
	Data:       map[string]string{"text-key": "text-value"},
	BinaryData: map[string][]byte{"binary-key": []byte("binary-value")},
}

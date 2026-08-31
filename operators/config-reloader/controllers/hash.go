package controllers

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"sort"

	corev1 "k8s.io/api/core/v1"
)

// watchedContent is the minimal data needed to hash one watched resource.
// This is a small, unexported, k8s-client-free type deliberately: it lets
// hash.go be tested with plain Go structs (see hash_test.go) instead of
// needing a fake Kubernetes client just to test a hashing function.
type watchedContent struct {
	Kind string
	Name string
	Data map[string][]byte
}

// configMapContent extracts the hashable content out of a ConfigMap.
// ConfigMaps have two data fields — Data (string values, for text config)
// and BinaryData (byte values, for anything non-UTF8) — and either one
// changing should count as a content change, so both get folded into one
// map keyed by their original keys.
func configMapContent(name string, cm *corev1.ConfigMap) watchedContent {
	data := make(map[string][]byte, len(cm.Data)+len(cm.BinaryData))
	for k, v := range cm.Data {
		data[k] = []byte(v)
	}
	maps.Copy(data, cm.BinaryData)
	return watchedContent{Kind: "ConfigMap", Name: name, Data: data}
}

// secretContent extracts the hashable content out of a Secret. Secret.Data
// is already map[string][]byte (the API server stores/serves it
// base64-encoded on the wire, but client-go hands it back to us decoded),
// so unlike configMapContent there's no merging to do.
func secretContent(name string, s *corev1.Secret) watchedContent {
	return watchedContent{Kind: "Secret", Name: name, Data: s.Data}
}

// computeHash returns a deterministic sha256 hex digest over the combined
// content of all watched resources.
//
// "Deterministic" is the whole point of this function, and it requires two
// separate sorting steps most naive hash-a-map implementations skip:
//  1. Go map iteration order is randomized per-process, so iterating
//     resources[i].Data directly would produce a different hash on every
//     single call even when the content hasn't changed. Both this
//     function's own resource ordering (sorted by kind+name) and each
//     resource's key ordering (sorted per resource) are made explicit for
//     exactly this reason.
//  2. The caller (Reconcile, in reloadtrigger_controller.go) builds this
//     slice by iterating spec.Watch in whatever order the user wrote it in
//     YAML — that order shouldn't matter to whether two hashes are "the
//     same," so the function re-sorts rather than trusting caller order.
//
// Without both of these, TestComputeHash_DeterministicRegardlessOfInputOrder
// would fail intermittently — a classic "works on my machine, flakes in CI"
// bug class that's worth being able to name in an interview.
func computeHash(resources []watchedContent) string {
	// Copy before sorting so we never mutate the caller's slice in place —
	// computeHash has no business reordering data the caller still holds a
	// reference to.
	sorted := make([]watchedContent, len(resources))
	copy(sorted, resources)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Kind != sorted[j].Kind {
			return sorted[i].Kind < sorted[j].Kind
		}
		return sorted[i].Name < sorted[j].Name
	})

	h := sha256.New()
	for _, r := range sorted {
		// kind=/name= markers stop two differently-shaped inputs from
		// hashing to the same digest by coincidence (e.g. a ConfigMap
		// named "sec" vs a Secret named "sec" with identical data).
		fmt.Fprintf(h, "kind=%s,name=%s\n", r.Kind, r.Name)
		keys := make([]string, 0, len(r.Data))
		for k := range r.Data {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(h, "%s=", k)
			h.Write(r.Data[k])
			h.Write([]byte{'\n'})
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

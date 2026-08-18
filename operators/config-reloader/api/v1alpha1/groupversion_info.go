// Package v1alpha1 contains the reloader.homelab.dev/v1alpha1 API group.
//
// Every Kubernetes API type — built-in or custom — belongs to a
// "GroupVersionKind" (GVK): a Group (a namespace for related APIs, e.g.
// "apps"), a Version (e.g. "v1alpha1" — the "alpha" signals this schema can
// still change shape between releases), and a Kind (the type name, e.g.
// "ReloadTrigger"). This file registers our Group+Version and gives the
// rest of the package a shared place to register Kinds against it.
//
// +kubebuilder:object:generate=true
// +groupName=reloader.homelab.dev
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	// GroupVersion is group version used to register these objects.
	// This is the literal "apiVersion: reloader.homelab.dev/v1alpha1"
	// string every ReloadTrigger YAML manifest must carry.
	GroupVersion = schema.GroupVersion{Group: "reloader.homelab.dev", Version: "v1alpha1"}

	// SchemeBuilder is used to add go types to the GroupVersionKind scheme.
	// A "scheme" is Kubernetes' runtime type registry — it's how a generic
	// client knows that the bytes it just decoded as JSON/YAML should
	// become a Go *ReloadTrigger struct instead of some other type.
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

	// AddToScheme adds the types in this group-version to the given scheme.
	// main.go calls this once at startup so the manager's client knows how
	// to read/write ReloadTrigger objects, exactly like it already knows
	// how to read/write built-in types such as Deployment.
	AddToScheme = SchemeBuilder.AddToScheme
)

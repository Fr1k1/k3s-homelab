package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// WatchedResourceKind identifies the kind of resource a ReloadTrigger watches.
// A plain string type (not an int enum) because it needs to round-trip to
// YAML/JSON as human-readable text — "ConfigMap", not "0".
type WatchedResourceKind string

const (
	WatchedResourceKindConfigMap WatchedResourceKind = "ConfigMap"
	WatchedResourceKindSecret    WatchedResourceKind = "Secret"
)

// WatchedResource references one ConfigMap or Secret, in the ReloadTrigger's
// own namespace, whose content should be watched for changes.
//
// The +kubebuilder:validation markers below aren't decorative comments —
// controller-gen parses them and bakes the constraints into the CRD's
// OpenAPI schema (see config/crd/bases/*.yaml). That means the Kubernetes
// API server itself rejects an invalid ReloadTrigger (e.g. kind: "Pod",
// which isn't in the enum) before it ever reaches this controller's
// Reconcile loop — validation-at-the-boundary, not validation-in-code.
type WatchedResource struct {
	// +kubebuilder:validation:Enum=ConfigMap;Secret
	Kind WatchedResourceKind `json:"kind"`
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// ReloadTriggerSpec defines which Deployment to restart and which resources
// to watch for content changes that should trigger that restart.
//
// Spec is the *desired state* half of the Kubernetes API convention: it's
// what a human (or GitOps pipeline) writes down and Kubernetes stores
// verbatim. The controller never mutates Spec — only Status, below.
type ReloadTriggerSpec struct {
	// TargetDeployment is the name of the Deployment, in this object's own
	// namespace, to restart when a watched resource's content changes.
	// +kubebuilder:validation:MinLength=1
	TargetDeployment string `json:"targetDeployment"`

	// Watch lists the ConfigMaps/Secrets, in this object's own namespace,
	// whose content is combined into a single hash. Any change to any of
	// them triggers a restart.
	// +kubebuilder:validation:MinItems=1
	Watch []WatchedResource `json:"watch"`
}

// ReloadTriggerStatus reports the last observed content hash and the result
// of reconciling it.
//
// Status is the *observed state* half of the convention: only the
// controller writes it (via a separate "status" subresource — see the
// +kubebuilder:subresource:status marker below), reporting back facts a
// human/GitOps pipeline can't know in advance, like "what hash did I last
// see" or "did the restart actually happen." This Spec/Status split is
// what makes a CRD a genuine control loop instead of a glorified config
// file: something declares intent, something else reports reality, and the
// controller's whole job is closing the gap between the two.
type ReloadTriggerStatus struct {
	// ObservedHash is the sha256 hex digest of the combined content of all
	// watched resources, as of the last successful reconcile. This is the
	// piece of state the controller diffs against on every reconcile to
	// decide "did anything actually change" — see hash.go and the
	// Reconcile method in reloadtrigger_controller.go.
	ObservedHash string `json:"observedHash,omitempty"`

	// LastReloadTime is when the target Deployment was last restarted by
	// this ReloadTrigger. Unset if it has never restarted anything (i.e.
	// only the baseline hash has ever been recorded).
	LastReloadTime *metav1.Time `json:"lastReloadTime,omitempty"`

	// Conditions represents the latest observations, keyed by Type. The
	// controller maintains a single condition of type "Ready" whose
	// Reason (Initialized / HashUnchanged / Reloaded / ResourceNotFound /
	// DeploymentNotFound) is the human-readable summary of what the last
	// reconcile actually did — this is the standard Kubernetes
	// "conditions" pattern (the same shape Deployment/Pod/Node status use),
	// not something invented for this project.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Target",type=string,JSONPath=`.spec.targetDeployment`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].reason`
// +kubebuilder:printcolumn:name="Last Reload",type=date,JSONPath=`.status.lastReloadTime`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
//
// The printcolumn markers are what make `kubectl get reloadtriggers` show
// useful columns (Target/Ready/Reason/Last Reload/Age) instead of just
// NAME/AGE — a small thing, but it's the difference between a CRD that's
// demoable at a glance and one you have to `-o yaml` to understand.
//
// +kubebuilder:subresource:status splits this object's HTTP API in two:
// PUT/PATCH on the object itself can only touch `spec` and `metadata`;
// only PUT/PATCH on the dedicated `/status` sub-resource can touch
// `status`. That's what makes the RBAC split in the controller
// (`reloadtriggers` vs `reloadtriggers/status`, see the +kubebuilder:rbac
// markers in reloadtrigger_controller.go) a real permission boundary and
// not just documentation.

// ReloadTrigger opts a Deployment into automatic rolling restarts whenever
// a ConfigMap/Secret it references changes content.
type ReloadTrigger struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ReloadTriggerSpec   `json:"spec,omitempty"`
	Status ReloadTriggerStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ReloadTriggerList contains a list of ReloadTrigger.
// Every custom Kind needs a matching List type — it's what backs
// `kubectl get reloadtriggers` (plural) and List/Watch API calls, as
// opposed to Get calls against one named object.
type ReloadTriggerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ReloadTrigger `json:"items"`
}

func init() {
	// Registers both types against the scheme built in
	// groupversion_info.go. Without this, AddToScheme would be a no-op and
	// the manager's client would have no idea a "ReloadTrigger" kind
	// exists at all.
	SchemeBuilder.Register(&ReloadTrigger{}, &ReloadTriggerList{})
}

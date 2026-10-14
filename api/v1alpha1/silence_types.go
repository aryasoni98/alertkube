// Package v1alpha1 contains the Go types for the alertkube.io API group.
//
// These exist even though the controller watches the CRD with a *dynamic*
// informer (ADR-0004, which keeps us off controller-runtime per ADR-0001). A
// dynamic informer means no generated clientset is required; it does not mean
// the API should have no types. Without them the schema lives only in the Helm
// template, there is nothing for an external integrator to import, and the
// controller's own decoding is a pile of unstructured map lookups whose
// contract is enforced nowhere.
//
// This package is intentionally dependency-light: apimachinery only, no
// controller-runtime, no generated client. It is the published shape of the
// API, not a client for it.
package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// Silence suppresses matching alerts until an expiry timestamp. It is the
// declarative analog of a config-file silence, manageable with kubectl/GitOps
// instead of a controller rollout.
//
// The controller consults silences from three sources - config file, this CRD,
// and the runtime API - and matches all of them identically.
type Silence struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec SilenceSpec `json:"spec"`
}

// SilenceSpec is the desired state. There is deliberately no status subresource:
// a silence is a pure input to routing, and the controller holds no
// reconciliation state about it that an operator would need to read back.
type SilenceSpec struct {
	// Matchers selects which alerts to silence, as alert field/label key to
	// expected value. At least one is required - an empty matcher set would
	// silence every alert in the cluster, which is never what someone means.
	//
	// Matching semantics are shared with config-file silences: `namespace` and
	// `reason` are treated as anchored regexes, every other key is exact-match.
	// A `namespace` or `reason` that matches every alert, such as `.*`, makes
	// the controller ignore the Silence.
	//
	// A namespaced Silence only mutes its own namespace. The controller sets an
	// omitted `namespace` matcher to the object's namespace, and ignores the
	// Silence when the matcher has any other value, a pattern included. Only a
	// cluster-scoped Silence can match other namespaces. So a namespaced
	// Silence never mutes alerts with no namespace or one that is not a
	// Kubernetes namespace: Node alerts, cloud-source alerts (namespace is the
	// region or account scope), and receiver alerts without a namespace label.
	// Silence those with a cluster-scoped Silence or a config-file or
	// runtime-API silence.
	Matchers map[string]string `json:"matchers"`

	// Until is the RFC3339 timestamp after which this silence stops applying.
	// Required, and deliberately not optional: an unbounded silence is how
	// alerting quietly dies, so expiry is part of the type rather than a
	// convention.
	//
	// The controller applies a silence for at most 30 days after the object's
	// creationTimestamp; a later until is capped there. Editing until cannot
	// extend a silence past that point: recreate the object instead.
	Until string `json:"until"`

	// Comment is an optional human note describing why the silence exists.
	// Recommended - a silence with no rationale outlives the reason for it.
	Comment string `json:"comment,omitempty"`
}

// SilenceList is a list of Silence objects.
type SilenceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []Silence `json:"items"`
}

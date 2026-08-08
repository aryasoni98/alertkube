package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Group/Version for the alertkube.io API. The plural resource name must match
// the CRD's spec.names.plural (see helm/templates/crd-silence.yaml) or the
// dynamic informer watches a resource that does not exist and silently syncs
// nothing.
const (
	Group   = "alertkube.io"
	Version = "v1alpha1"

	// SilenceResource is the plural, lowercase resource name.
	SilenceResource = "silences"
	// SilenceKind is the object kind.
	SilenceKind = "Silence"
)

// GroupVersion is the group-version for this API.
var GroupVersion = schema.GroupVersion{Group: Group, Version: Version}

// SilenceGVR is the GroupVersionResource the dynamic informer watches.
var SilenceGVR = GroupVersion.WithResource(SilenceResource)

// SilenceGVK is the GroupVersionKind of a Silence object.
var SilenceGVK = GroupVersion.WithKind(SilenceKind)

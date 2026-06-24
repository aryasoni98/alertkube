package authz

import (
	"context"
	"fmt"

	authnv1 "k8s.io/api/authentication/v1"
	authzv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// ResourceAttributes names the synthetic RBAC object a console write maps to.
// These are not real Kubernetes resources - the apiserver evaluates RBAC purely
// by string match - so a cluster admin grants console write access with an
// ordinary Role, e.g.
//
//	rules:
//	- apiGroups: ["alertkube.io"]
//	  resources: ["silences"]
//	  verbs: ["create", "delete"]
//	- apiGroups: ["alertkube.io"]
//	  resources: ["channels"]
//	  verbs: ["create"]
type ResourceAttributes struct {
	Group       string
	Resource    string
	Subresource string
	Verb        string
}

// RBACAuthorizer authenticates a bearer token against the apiserver
// (TokenReview) and authorizes the resulting identity against RBAC
// (SubjectAccessReview). It lets the console reuse Kubernetes identity and RBAC
// for write actions instead of a single shared token, so the audit trail records
// a real username and access is managed with standard RBAC.
//
// It requires the controller's ServiceAccount to be allowed to create
// tokenreviews and subjectaccessreviews (the system:auth-delegator ClusterRole
// bundles exactly those two).
type RBACAuthorizer struct {
	client kubernetes.Interface
}

// NewRBACAuthorizer builds an authorizer backed by the given Kubernetes client.
func NewRBACAuthorizer(c kubernetes.Interface) *RBACAuthorizer {
	return &RBACAuthorizer{client: c}
}

// Authorize returns the authenticated username and whether that identity may
// perform the attributes' verb. A non-nil error means the check itself failed
// (e.g. the apiserver was unreachable) and the caller must treat it as a deny.
// An empty or invalid token yields ("", false, nil).
func (a *RBACAuthorizer) Authorize(ctx context.Context, token string, attr ResourceAttributes) (string, bool, error) {
	if token == "" {
		return "", false, nil
	}
	tr, err := a.client.AuthenticationV1().TokenReviews().Create(ctx, &authnv1.TokenReview{
		Spec: authnv1.TokenReviewSpec{Token: token},
	}, metav1.CreateOptions{})
	if err != nil {
		return "", false, fmt.Errorf("tokenreview: %w", err)
	}
	if !tr.Status.Authenticated {
		return "", false, nil
	}
	u := tr.Status.User

	extra := make(map[string]authzv1.ExtraValue, len(u.Extra))
	for k, v := range u.Extra {
		extra[k] = authzv1.ExtraValue(v)
	}
	sar, err := a.client.AuthorizationV1().SubjectAccessReviews().Create(ctx, &authzv1.SubjectAccessReview{
		Spec: authzv1.SubjectAccessReviewSpec{
			User:   u.Username,
			Groups: u.Groups,
			UID:    u.UID,
			Extra:  extra,
			ResourceAttributes: &authzv1.ResourceAttributes{
				Group:       attr.Group,
				Resource:    attr.Resource,
				Subresource: attr.Subresource,
				Verb:        attr.Verb,
			},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		return u.Username, false, fmt.Errorf("subjectaccessreview: %w", err)
	}
	return u.Username, sar.Status.Allowed, nil
}

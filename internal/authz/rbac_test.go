package authz

import (
	"context"
	"errors"
	"testing"

	authnv1 "k8s.io/api/authentication/v1"
	authzv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func tokenReviewReactor(authenticated bool, user string, groups []string) ktesting.ReactionFunc {
	return func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &authnv1.TokenReview{
			Status: authnv1.TokenReviewStatus{
				Authenticated: authenticated,
				User:          authnv1.UserInfo{Username: user, Groups: groups},
			},
		}, nil
	}
}

func sarReactor(allowed bool, capture *authzv1.SubjectAccessReview) ktesting.ReactionFunc {
	return func(action ktesting.Action) (bool, runtime.Object, error) {
		if capture != nil {
			if ca, ok := action.(ktesting.CreateAction); ok {
				if sar, ok := ca.GetObject().(*authzv1.SubjectAccessReview); ok {
					*capture = *sar
				}
			}
		}
		return true, &authzv1.SubjectAccessReview{Status: authzv1.SubjectAccessReviewStatus{Allowed: allowed}}, nil
	}
}

func TestAuthorizeAllowed(t *testing.T) {
	cs := fake.NewSimpleClientset()
	cs.PrependReactor("create", "tokenreviews", tokenReviewReactor(true, "alice", []string{"devs"}))
	var gotSAR authzv1.SubjectAccessReview
	cs.PrependReactor("create", "subjectaccessreviews", sarReactor(true, &gotSAR))

	a := NewRBACAuthorizer(cs)
	user, allowed, err := a.Authorize(context.Background(), "tok", ResourceAttributes{Group: "alertkube.io", Resource: "silences", Verb: "create"})
	if err != nil || !allowed || user != "alice" {
		t.Fatalf("Authorize = (%q, %v, %v), want (alice, true, nil)", user, allowed, err)
	}
	// The SAR must carry the identity and the requested attributes.
	if gotSAR.Spec.User != "alice" {
		t.Errorf("SAR user = %q, want alice", gotSAR.Spec.User)
	}
	ra := gotSAR.Spec.ResourceAttributes
	if ra == nil || ra.Group != "alertkube.io" || ra.Resource != "silences" || ra.Verb != "create" {
		t.Errorf("SAR ResourceAttributes = %+v, want alertkube.io/silences create", ra)
	}
}

func TestAuthorizeNotAuthenticated(t *testing.T) {
	cs := fake.NewSimpleClientset()
	cs.PrependReactor("create", "tokenreviews", tokenReviewReactor(false, "", nil))
	a := NewRBACAuthorizer(cs)
	user, allowed, err := a.Authorize(context.Background(), "bad", ResourceAttributes{Resource: "silences", Verb: "create"})
	if err != nil || allowed || user != "" {
		t.Fatalf("Authorize unauthenticated = (%q, %v, %v), want (\"\", false, nil)", user, allowed, err)
	}
}

func TestAuthorizeAuthenticatedButDenied(t *testing.T) {
	cs := fake.NewSimpleClientset()
	cs.PrependReactor("create", "tokenreviews", tokenReviewReactor(true, "bob", nil))
	cs.PrependReactor("create", "subjectaccessreviews", sarReactor(false, nil))
	a := NewRBACAuthorizer(cs)
	user, allowed, err := a.Authorize(context.Background(), "tok", ResourceAttributes{Resource: "silences", Verb: "delete"})
	if err != nil || allowed || user != "bob" {
		t.Fatalf("Authorize denied = (%q, %v, %v), want (bob, false, nil)", user, allowed, err)
	}
}

func TestAuthorizeEmptyTokenShortCircuits(t *testing.T) {
	cs := fake.NewSimpleClientset()
	cs.PrependReactor("create", "tokenreviews", func(ktesting.Action) (bool, runtime.Object, error) {
		t.Fatal("empty token must not hit the apiserver")
		return false, nil, nil
	})
	a := NewRBACAuthorizer(cs)
	if _, allowed, err := a.Authorize(context.Background(), "", ResourceAttributes{}); allowed || err != nil {
		t.Fatalf("empty token = (allowed=%v, err=%v), want (false, nil)", allowed, err)
	}
}

func TestAuthorizeTokenReviewError(t *testing.T) {
	cs := fake.NewSimpleClientset()
	cs.PrependReactor("create", "tokenreviews", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("apiserver down")
	})
	a := NewRBACAuthorizer(cs)
	if _, allowed, err := a.Authorize(context.Background(), "tok", ResourceAttributes{}); allowed || err == nil {
		t.Fatal("tokenreview error must surface as (false, err) so the caller denies")
	}
}

package authz

import "testing"

func TestBearerEqual(t *testing.T) {
	const token = "s3cret"
	cases := []struct {
		name   string
		header string
		token  string
		want   bool
	}{
		{"exact match", "Bearer s3cret", token, true},
		{"wrong token", "Bearer nope", token, false},
		// TrimPrefix is lenient: a bare token with no "Bearer " scheme also
		// authenticates. Established receiver behavior, centralized here.
		{"bare token without scheme still matches", "s3cret", token, true},
		{"empty header", "", token, false},
		{"lowercase scheme not accepted", "bearer s3cret", token, false},
		// An empty token compares equal to a bare "Bearer " header, which is
		// why both call sites gate on `token != ""` before calling this.
		{"empty token matches bare prefix (callers must gate on token != \"\")", "Bearer ", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := BearerEqual(tc.header, tc.token); got != tc.want {
				t.Errorf("BearerEqual(%q, %q) = %v, want %v", tc.header, tc.token, got, tc.want)
			}
		})
	}
}

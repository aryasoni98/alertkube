package env

import "testing"

func TestOr(t *testing.T) {
	const key = "ALERTKUBE_TEST_OR"
	tests := []struct {
		name, set, def, want string
		unset                bool
	}{
		{name: "set non-empty wins", set: "value", def: "fallback", want: "value"},
		{name: "empty falls back", set: "", def: "fallback", want: "fallback"},
		{name: "unset falls back", unset: true, def: "fallback", want: "fallback"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !tt.unset {
				t.Setenv(key, tt.set)
			}
			if got := Or(key, tt.def); got != tt.want {
				t.Fatalf("Or() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIntOr(t *testing.T) {
	const key = "ALERTKUBE_TEST_INT"
	tests := []struct {
		name, set string
		def, want int
		unset     bool
	}{
		{name: "parses int", set: "42", def: 7, want: 42},
		{name: "negative", set: "-3", def: 7, want: -3},
		{name: "unparsable falls back", set: "notanint", def: 7, want: 7},
		{name: "empty falls back", set: "", def: 7, want: 7},
		{name: "unset falls back", unset: true, def: 7, want: 7},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !tt.unset {
				t.Setenv(key, tt.set)
			}
			if got := IntOr(key, tt.def); got != tt.want {
				t.Fatalf("IntOr() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestBool(t *testing.T) {
	const key = "ALERTKUBE_TEST_BOOL"
	tests := []struct {
		name, set string
		def, want bool
		unset     bool
	}{
		{name: "1 is true", set: "1", def: false, want: true},
		{name: "true is true", set: "true", def: false, want: true},
		{name: "TRUE is true", set: "TRUE", def: false, want: true},
		{name: "yes is true", set: "yes", def: false, want: true},
		{name: "0 is false", set: "0", def: true, want: false},
		{name: "false is false", set: "false", def: true, want: false},
		{name: "FALSE is false", set: "FALSE", def: true, want: false},
		{name: "no is false", set: "no", def: true, want: false},
		{name: "garbage uses default true", set: "maybe", def: true, want: true},
		{name: "garbage uses default false", set: "maybe", def: false, want: false},
		{name: "unset uses default", unset: true, def: true, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !tt.unset {
				t.Setenv(key, tt.set)
			}
			if got := Bool(key, tt.def); got != tt.want {
				t.Fatalf("Bool() = %v, want %v", got, tt.want)
			}
		})
	}
}

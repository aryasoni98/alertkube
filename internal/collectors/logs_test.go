package collectors

import (
	"strings"
	"testing"
)

func TestRedactSecrets(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"aws access key", "creds=AKIAABCDEFGHIJKLMNOP go", "creds=[REDACTED] go"},
		{"github personal token", "auth ghp_1234567890abcdefghijklmnopqrstuv done", "auth [REDACTED] done"},
		{"slack bot token", "tok=xoxb-1234567890-abcdef-XYZ next", "tok=[REDACTED] next"},
		{"openai key", "OPENAI=sk-abcdefghijklmnopqrstuvwxyz1234567890", "OPENAI=[REDACTED]"},
		{"bearer", "Authorization: Bearer abc.def.ghi", "Authorization: Bearer [REDACTED]"},
		{"password kv", "password=hunter2 trailing", "password=[REDACTED] trailing"},
		{"url token", "https://x/?token=secret123&keep=1", "https://x/?token=[REDACTED]&keep=1"},
		{"jwt", "got token eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dBjftJeZ4CVPmB92K27uhbUJU1p1r_wW1gFWFOEjXk ok", "got token [REDACTED] ok"},
		{"db url creds", "dial postgres://app:s3cr3tpw@db.prod:5432/main failed", "dial postgres://app:[REDACTED]@db.prod:5432/main failed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := RedactSecrets(c.in)
			if got != c.want {
				t.Fatalf("RedactSecrets(%q) = %q, want %q", c.in, got, c.want)
			}
			if strings.Contains(got, "hunter2") || strings.Contains(got, "secret123") {
				t.Fatalf("secret leaked through: %q", got)
			}
		})
	}
}

func TestRedactSecretsLeavesPlainText(t *testing.T) {
	in := "container exited code 137 due to OOM"
	if got := RedactSecrets(in); got != in {
		t.Fatalf("redactor mutated plain text: %q -> %q", in, got)
	}
}

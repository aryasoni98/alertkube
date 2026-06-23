package config

import "testing"

func TestValidateRules(t *testing.T) {
	withRule := func(r Rule) *Config {
		c := awsBaseConfig() // sets the mute/resolve/pvc fields Validate requires
		c.Rules = []Rule{r}
		return c
	}
	cases := []struct {
		name    string
		rule    Rule
		wantErr bool
	}{
		{"valid count", Rule{Name: "a", Severity: "critical", WindowSeconds: 300, Count: &RuleCount{Match: map[string]string{"reason": "X"}, Threshold: 3}}, false},
		{"valid all", Rule{Name: "a", Severity: "critical", WindowSeconds: 300, All: []map[string]string{{"kind": "Node"}}}, false},
		{"valid absent", Rule{Name: "a", Severity: "warning", Absent: &RuleAbsent{Match: map[string]string{"name": "hb"}, ForSeconds: 600}}, false},
		{"missing name", Rule{Severity: "critical", WindowSeconds: 300, Count: &RuleCount{Threshold: 1}}, true},
		{"bad severity", Rule{Name: "a", Severity: "bogus", WindowSeconds: 300, Count: &RuleCount{Threshold: 1}}, true},
		{"no condition", Rule{Name: "a", Severity: "info"}, true},
		{"two conditions", Rule{Name: "a", Severity: "info", WindowSeconds: 300, Count: &RuleCount{Threshold: 1}, Absent: &RuleAbsent{ForSeconds: 60}}, true},
		{"count needs window", Rule{Name: "a", Severity: "info", Count: &RuleCount{Threshold: 1}}, true},
		{"count needs threshold", Rule{Name: "a", Severity: "info", WindowSeconds: 60, Count: &RuleCount{Threshold: 0}}, true},
		{"all needs window", Rule{Name: "a", Severity: "critical", All: []map[string]string{{"kind": "Node"}}}, true},
		{"absent needs forSeconds", Rule{Name: "a", Severity: "warning", Absent: &RuleAbsent{ForSeconds: 0}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := withRule(tc.rule).Validate()
			if tc.wantErr && err == nil {
				t.Fatal("expected validation error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected validation error: %v", err)
			}
		})
	}
}

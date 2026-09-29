package config

import (
	"strings"
	"testing"
)

// validBaseConfig returns a minimal Config that passes Validate. It sets only
// the behavior fields Validate always requires, so each case's mutation is the
// only thing under test.
func validBaseConfig() *Config {
	c := &Config{}
	c.Behavior.MuteSeconds = 600
	c.Behavior.ResolveTTLSeconds = 600
	c.Behavior.PVCPendingSeconds = 300
	return c
}

func TestValidateAWS(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Config)
		wantErr bool
	}{
		{"disabled ignores aws fields", func(c *Config) { c.AWS.Enabled = false }, false},
		{"enabled needs regions", func(c *Config) {
			c.AWS.Enabled = true
			c.AWS.EKS = true
			c.AWS.PollSeconds = 60
		}, true},
		{"enabled needs a source", func(c *Config) {
			c.AWS.Enabled = true
			c.AWS.Regions = []string{"us-east-1"}
			c.AWS.PollSeconds = 60
		}, true},
		{"poll must be positive", func(c *Config) {
			c.AWS.Enabled = true
			c.AWS.Regions = []string{"us-east-1"}
			c.AWS.EKS = true
			c.AWS.PollSeconds = 0
		}, true},
		{"poll must be below resolveTTL", func(c *Config) {
			c.AWS.Enabled = true
			c.AWS.Regions = []string{"us-east-1"}
			c.AWS.CloudWatch = true
			c.AWS.PollSeconds = 600 // == ResolveTTLSeconds
		}, true},
		{"poll deadline at resolveTTL stays valid (warning only)", func(c *Config) {
			c.AWS.Enabled = true
			c.AWS.Regions = []string{"us-east-1"}
			c.AWS.CloudWatch = true
			c.AWS.PollSeconds = 300 // deadline 600s == ResolveTTLSeconds
		}, false},
		{"longest valid poll", func(c *Config) {
			c.AWS.Enabled = true
			c.AWS.Regions = []string{"us-east-1"}
			c.AWS.CloudWatch = true
			c.AWS.PollSeconds = 599
		}, false},
		{"valid aws config", func(c *Config) {
			c.AWS.Enabled = true
			c.AWS.Regions = []string{"us-east-1", "eu-west-1"}
			c.AWS.EKS = true
			c.AWS.CloudWatch = true
			c.AWS.EC2 = true
			c.AWS.PollSeconds = 60
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := validBaseConfig()
			tc.mutate(c)
			err := c.Validate()
			if tc.wantErr && err == nil {
				t.Fatal("expected validation error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected validation error: %v", err)
			}
		})
	}
}

func TestPollDeadlineWarnings(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
		want   []string // providers expected to warn, in order
	}{
		{"disabled providers never warn", func(c *Config) {
			c.AWS.PollSeconds = 500
		}, nil},
		{"deadline below TTL", func(c *Config) {
			c.AWS.Enabled = true
			c.AWS.PollSeconds = 299
		}, nil},
		{"deadline at TTL", func(c *Config) {
			c.AWS.Enabled = true
			c.AWS.PollSeconds = 300
		}, []string{"aws"}},
		{"each provider checked", func(c *Config) {
			c.AWS.Enabled, c.AWS.PollSeconds = true, 60
			c.Azure.Enabled, c.Azure.PollSeconds = true, 400
			c.GCP.Enabled, c.GCP.PollSeconds = true, 500
		}, []string{"azure", "gcp"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := validBaseConfig()
			tc.mutate(c)
			got := c.PollDeadlineWarnings()
			if len(got) != len(tc.want) {
				t.Fatalf("warnings = %q, want one each for %v", got, tc.want)
			}
			for i, p := range tc.want {
				if !strings.HasPrefix(got[i], p+".pollSeconds") {
					t.Errorf("warning %d = %q, want it to name %s.pollSeconds", i, got[i], p)
				}
			}
		})
	}
}

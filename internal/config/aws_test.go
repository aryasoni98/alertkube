package config

import "testing"

// awsBaseConfig returns a Config that passes every non-AWS Validate rule, so
// AWS-specific cases test only the AWS branch.
func awsBaseConfig() *Config {
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
			c := awsBaseConfig()
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

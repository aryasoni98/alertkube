package config

// AWS configures region-scoped cloud sources and their service toggles.
type AWS struct {
	Enabled bool `yaml:"enabled"`
	// Regions to poll. Each AWS API call is per-region, so every region
	// here multiplies the per-poll API call count.
	Regions []string `yaml:"regions"`
	// PollSeconds is the interval between polls. It must be below
	// resolveTTLSeconds or a still-firing alarm false-resolves between
	// polls (Validate enforces this, mirroring the informer-resync rule).
	// Each poll is cancelled after PollDeadlineFactor intervals; a deadline
	// at or above the TTL is logged as a startup warning.
	PollSeconds int `yaml:"pollSeconds"`
	// Source toggles. At least one must be true when Enabled.
	EKS         bool `yaml:"eks"`
	CloudWatch  bool `yaml:"cloudwatch"`
	EC2         bool `yaml:"ec2"`
	ELBV2       bool `yaml:"elbv2"`
	RDS         bool `yaml:"rds"`
	DynamoDB    bool `yaml:"dynamodb"`
	ElastiCache bool `yaml:"elasticache"`
	S3          bool `yaml:"s3"`
	CloudTrail  bool `yaml:"cloudtrail"`
	// CloudTrailEvents overrides the management event names the CloudTrail
	// source looks up. Empty uses a curated security set (security-group,
	// S3 policy/ACL, and IAM mutating events).
	CloudTrailEvents []string `yaml:"cloudtrailEvents"`
	ASG              bool     `yaml:"asg"`
	KMS              bool     `yaml:"kms"`
	EBS              bool     `yaml:"ebs"`
	Aurora           bool     `yaml:"aurora"`
	NAT              bool     `yaml:"nat"`
	EFS              bool     `yaml:"efs"`
	Route53          bool     `yaml:"route53"`
	ACM              bool     `yaml:"acm"`
	VPN              bool     `yaml:"vpn"`
}

// Azure configures subscription-scoped cloud sources and their service toggles.
type Azure struct {
	Enabled       bool     `yaml:"enabled"`
	Subscriptions []string `yaml:"subscriptions"`
	PollSeconds   int      `yaml:"pollSeconds"`
	// AKS enables AKS managed-cluster and node-pool health alerts.
	AKS bool `yaml:"aks"`
	// Monitor enables ingesting fired Azure Monitor alerts (Alerts
	// Management): an alert with monitorCondition Fired pages, Resolved
	// resolves.
	Monitor bool `yaml:"monitor"`
	// VMs enables Azure Virtual Machine provisioning-health alerts.
	VMs bool `yaml:"vms"`
	// Storage enables Azure Storage account availability alerts.
	Storage bool `yaml:"storage"`
	// SQL enables Azure SQL Database health alerts (Suspect/Offline/
	// Inaccessible/EmergencyMode/Shutdown).
	SQL bool `yaml:"sql"`
	// Redis enables Azure Cache for Redis provisioning-health alerts (Failed
	// is critical; recovering from a scale failure is a warning).
	Redis bool `yaml:"redis"`
}

// GCP configures project-scoped cloud sources and their service toggles.
type GCP struct {
	Enabled     bool     `yaml:"enabled"`
	Projects    []string `yaml:"projects"`
	PollSeconds int      `yaml:"pollSeconds"`
	// GKE enables GKE cluster and node-pool health alerts.
	GKE bool `yaml:"gke"`
	// Monitoring enables a Cloud Monitoring posture source: it alerts when
	// an alert policy is disabled. GCP's Go SDK exposes no fired-incident
	// listing, so this surfaces monitoring-coverage posture, not fired
	// incidents.
	Monitoring bool `yaml:"monitoring"`
	// Compute enables Compute Engine instance health (REPAIRING) alerts.
	Compute bool `yaml:"compute"`
	// CloudSQL enables Cloud SQL instance state alerts.
	CloudSQL bool `yaml:"cloudsql"`
}

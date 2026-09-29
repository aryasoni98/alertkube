// Package aws polls AWS APIs and emits cloud-resource alerts into the same
// pipeline as the in-cluster Kubernetes watchers. It implements one
// sources.Source per AWS service, each gated by its own config toggle and
// scoped to the configured regions (S3 and Route53 are global, built once):
//
//   - EKS         - control-plane / nodegroup health
//   - CloudWatch  - alarms in ALARM state
//   - EC2         - instance status-check failures
//   - ELBv2       - load-balancer / target-group health
//   - RDS         - DB-instance health
//   - DynamoDB    - table status
//   - ElastiCache - cluster status
//   - S3          - public-access exposure (global)
//   - CloudTrail  - curated security/change events
//   - ASG         - Auto Scaling group health
//   - KMS         - key state
//   - EBS         - volume status
//   - Aurora      - cluster health
//   - NAT         - NAT gateway state
//   - EFS         - file-system state
//   - ACM         - certificate expiry/status
//   - VPN         - VPN connection state
//   - Route53     - health-check status (global)
//
// Credentials resolve through the standard AWS chain (config.LoadDefaultConfig):
// IAM Roles for Service Accounts (IRSA) in-cluster, or env/shared-config when
// run locally. Each source declares a narrow per-service interface (eksAPI,
// cloudwatchAPI, ec2API, ...) naming exactly the API calls it makes, so the
// sources unit-test against canned responses without the SDK touching the
// network or real credentials.
package aws

import (
	"context"
	"fmt"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/acm"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/cloudtrail"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/efs"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/elasticache"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/aryasoni98/alertkube/internal/alert"
	"github.com/aryasoni98/alertkube/internal/config"
	"github.com/aryasoni98/alertkube/internal/sources"
)

// provider labels every AWS alert so routing/severityOverrides can target the
// cloud source as a class via the "provider": "aws" label.
const provider = "aws"

// init self-registers the AWS provider so the controller wires it from the
// registry rather than hardcoding it (mirrors sink self-registration).
func init() {
	sources.RegisterProvider(sources.Provider{
		Name: provider,
		Bind: func(c *config.Config) sources.Bound {
			section := c.AWS
			return sources.Bound{
				Enabled:     section.Enabled,
				PollSeconds: section.PollSeconds,
				Build: func(ctx context.Context) ([]sources.Source, error) {
					return buildSources(ctx, section)
				},
			}
		},
	})
}

// buildSources builds the enabled AWS sources, one client set per configured
// region. It returns an error only if the AWS config cannot be loaded for a
// region (e.g. malformed shared config); the caller logs it and continues
// without AWS so a cloud-auth problem never takes down the Kubernetes
// watchers. Credentials are fetched lazily on the first API call, so missing
// or invalid credentials surface as alertkube_cloud_poll_errors_total on every
// poll rather than as an error here.
func buildSources(ctx context.Context, cfg config.AWS) ([]sources.Source, error) {
	regions := make([]regionConfig, 0, len(cfg.Regions))
	for _, region := range cfg.Regions {
		awscfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
		if err != nil {
			return nil, fmt.Errorf("aws: load config for region %s: %w", region, err)
		}
		regions = append(regions, regionConfig{region: region, cfg: awscfg})
	}
	// One line per service: its config toggle, the client constructor, and the
	// Source that owns the resulting per-region clients. A disabled service
	// yields nil and Compact drops it. Adding a service means its own file
	// (client interface, Source and evaluator) plus: an entry here, a toggle on
	// config.AWS (config/cloud.go), a matching entry in validateAWS's toggle
	// list (config/validate.go), the key in helm/values.yaml, and the toggle
	// row in docs/docs/reference/config-schema.md.
	return sources.Compact([]sources.Source{
		regionalSource(cfg.EKS, regions,
			func(c awssdk.Config) eksAPI { return eks.NewFromConfig(c) },
			func(rs []eksRegion) sources.Source { return &eksSource{regions: rs} }),
		regionalSource(cfg.CloudWatch, regions,
			func(c awssdk.Config) cloudwatchAPI { return cloudwatch.NewFromConfig(c) },
			func(rs []cwRegion) sources.Source { return &cloudWatchSource{regions: rs} }),
		regionalSource(cfg.EC2, regions,
			func(c awssdk.Config) ec2API { return ec2.NewFromConfig(c) },
			func(rs []ec2Region) sources.Source { return &ec2Source{regions: rs} }),
		regionalSource(cfg.ELBV2, regions,
			func(c awssdk.Config) elbv2API { return elbv2.NewFromConfig(c) },
			func(rs []elbv2Region) sources.Source { return &elbv2Source{regions: rs} }),
		regionalSource(cfg.RDS, regions,
			func(c awssdk.Config) rdsAPI { return rds.NewFromConfig(c) },
			func(rs []rdsRegion) sources.Source { return &rdsSource{regions: rs} }),
		regionalSource(cfg.DynamoDB, regions,
			func(c awssdk.Config) dynamoDBAPI { return dynamodb.NewFromConfig(c) },
			func(rs []dynRegion) sources.Source { return &dynamoDBSource{regions: rs} }),
		regionalSource(cfg.ElastiCache, regions,
			func(c awssdk.Config) elastiCacheAPI { return elasticache.NewFromConfig(c) },
			func(rs []ecRegion) sources.Source { return &elastiCacheSource{regions: rs} }),
		globalSource(cfg.S3, regions,
			func(c awssdk.Config) sources.Source { return &s3Source{client: s3.NewFromConfig(c)} }),
		regionalSource(cfg.CloudTrail, regions,
			func(c awssdk.Config) cloudTrailAPI { return cloudtrail.NewFromConfig(c) },
			func(rs []cloudTrailRegion) sources.Source { return newCloudTrailSource(rs, cfg) }),
		regionalSource(cfg.ASG, regions,
			func(c awssdk.Config) autoscalingAPI { return autoscaling.NewFromConfig(c) },
			func(rs []asgRegion) sources.Source { return &asgSource{regions: rs} }),
		regionalSource(cfg.KMS, regions,
			func(c awssdk.Config) kmsAPI { return kms.NewFromConfig(c) },
			func(rs []kmsRegion) sources.Source { return &kmsSource{regions: rs} }),
		regionalSource(cfg.EBS, regions,
			func(c awssdk.Config) ebsAPI { return ec2.NewFromConfig(c) },
			func(rs []ebsRegion) sources.Source { return &ebsSource{regions: rs} }),
		regionalSource(cfg.Aurora, regions,
			func(c awssdk.Config) auroraAPI { return rds.NewFromConfig(c) },
			func(rs []auroraRegion) sources.Source { return &auroraSource{regions: rs} }),
		regionalSource(cfg.NAT, regions,
			func(c awssdk.Config) natAPI { return ec2.NewFromConfig(c) },
			func(rs []natRegion) sources.Source { return &natSource{regions: rs} }),
		regionalSource(cfg.EFS, regions,
			func(c awssdk.Config) efsAPI { return efs.NewFromConfig(c) },
			func(rs []efsRegion) sources.Source { return &efsSource{regions: rs} }),
		regionalSource(cfg.ACM, regions,
			func(c awssdk.Config) acmAPI { return acm.NewFromConfig(c) },
			func(rs []acmRegion) sources.Source { return &acmSource{regions: rs} }),
		regionalSource(cfg.VPN, regions,
			func(c awssdk.Config) vpnAPI { return ec2.NewFromConfig(c) },
			func(rs []vpnRegion) sources.Source { return &vpnSource{regions: rs} }),
		globalSource(cfg.Route53, regions,
			func(c awssdk.Config) sources.Source { return &route53Source{client: route53.NewFromConfig(c)} }),
	}), nil
}

// emitFiring publishes a firing cloud alert. Identity is (kind, region, name);
// reason completes the dedupe fingerprint. The region rides in Namespace so a
// resolve (which the store matches on kind+namespace+name) targets exactly one
// cloud resource and never clears an unrelated one. Delegates to the shared
// sources.EmitFiring with AWS's provider+region labels.
func emitFiring(emit sources.Emit, k alert.Kind, region, name, reason, summary string, sev alert.Severity, details map[string]string) {
	sources.EmitFiring(emit, k, region, name, reason, summary, sev,
		map[string]string{"provider": provider, "region": region}, details)
}

// emitResolve clears any active alert for one cloud resource (see
// sources.EmitResolve). A resolve for a resource with no active alert is a
// no-op, so callers may emit it for every healthy resource each poll.
func emitResolve(emit sources.Emit, k alert.Kind, region, name string) {
	sources.EmitResolve(emit, k, region, name)
}

// pollErr records a per-source poll failure on the shared metric and logs it,
// so a blinded cloud source is observable without crashing the controller.
func pollErr(source, region string, err error) {
	sources.PollErr(source, region, err)
}

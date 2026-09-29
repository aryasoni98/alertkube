package aws

import (
	"context"
	"strings"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"golang.org/x/time/rate"

	"github.com/aryasoni98/alertkube/internal/alert"
	"github.com/aryasoni98/alertkube/internal/sources"
)

const sourceEKS = "aws-eks"

// eksAPI is the subset of the EKS client the EKS source uses.
type eksAPI interface {
	ListClusters(context.Context, *eks.ListClustersInput, ...func(*eks.Options)) (*eks.ListClustersOutput, error)
	DescribeCluster(context.Context, *eks.DescribeClusterInput, ...func(*eks.Options)) (*eks.DescribeClusterOutput, error)
	ListNodegroups(context.Context, *eks.ListNodegroupsInput, ...func(*eks.Options)) (*eks.ListNodegroupsOutput, error)
	DescribeNodegroup(context.Context, *eks.DescribeNodegroupInput, ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error)
}

type eksRegion = regionClient[eksAPI]

// eksSource discovers EKS clusters per region and alerts on their control-plane
// health. A cluster that is not ACTIVE (FAILED/DELETING/CREATING/UPDATING/
// PENDING) or that reports control-plane health issues fires; an ACTIVE
// cluster with no health issues resolves. This is the brief's EKS "cluster
// discovery + cluster health monitoring" for the control plane.
type eksSource struct {
	regions []eksRegion
}

func (s *eksSource) Name() string { return sourceEKS }

func (s *eksSource) Poll(ctx context.Context, emit sources.Emit) {
	pollByRegion(ctx, sourceEKS, s.regions, emit, s.pollRegion)
}

// pollRegion describes each listed cluster as its page arrives, so a
// ListClusters failure on a later page still leaves the earlier clusters
// evaluated.
func (s *eksSource) pollRegion(ctx context.Context, rc eksRegion, emit sources.Emit) {
	lim := newDescribeLimiter()
	forEachPage(ctx, sourceEKS, rc.region, func(ctx context.Context, token *string) (*string, error) {
		list, err := rc.client.ListClusters(ctx, &eks.ListClustersInput{NextToken: token})
		if err != nil {
			return nil, err
		}
		for _, name := range list.Clusters {
			if err := waitDescribe(ctx, lim); err != nil {
				return nil, err
			}
			out, err := rc.client.DescribeCluster(ctx, &eks.DescribeClusterInput{Name: awssdk.String(name)})
			if err != nil {
				pollErr(sourceEKS, rc.region, err)
				continue
			}
			if out == nil || out.Cluster == nil {
				continue
			}
			evaluateEKSCluster(rc.region, out.Cluster, emit)
			if err := s.pollNodegroups(ctx, rc, lim, name, emit); err != nil {
				return nil, err
			}
		}
		return list.NextToken, nil
	})
}

// pollNodegroups evaluates the node groups of one cluster. EKS does not embed
// node groups in DescribeCluster (unlike AKS/GKE), so this issues
// ListNodegroups + DescribeNodegroup per cluster. A describe API error is
// recorded and skips only that node group. The describe-limiter error, and a
// node-group list that ran out of time, are returned instead, so pollRegion
// stops the region and records it once.
func (s *eksSource) pollNodegroups(ctx context.Context, rc eksRegion, lim *rate.Limiter, cluster string, emit sources.Emit) error {
	names, err := s.listNodegroups(ctx, rc, cluster)
	if err != nil {
		return err
	}
	for _, ng := range names {
		if err := waitDescribe(ctx, lim); err != nil {
			return err
		}
		out, err := rc.client.DescribeNodegroup(ctx, &eks.DescribeNodegroupInput{
			ClusterName:   awssdk.String(cluster),
			NodegroupName: awssdk.String(ng),
		})
		if err != nil {
			pollErr(sourceEKS, rc.region, err)
			continue
		}
		if out == nil || out.Nodegroup == nil {
			continue
		}
		evaluateNodegroup(rc.region, cluster, out.Nodegroup, emit)
	}
	return nil
}

// listNodegroups returns the node group names of one cluster. A list API
// error is recorded and ends the list early; names from the pages already
// fetched are kept, so they are still evaluated. Once ctx is done the error
// is returned unrecorded instead, for pollRegion to record once for the
// region.
func (s *eksSource) listNodegroups(ctx context.Context, rc eksRegion, cluster string) ([]string, error) {
	var names []string
	err := walkPages(ctx, sourceEKS, rc.region, func(ctx context.Context, token *string) (*string, error) {
		list, err := rc.client.ListNodegroups(ctx, &eks.ListNodegroupsInput{ClusterName: awssdk.String(cluster), NextToken: token})
		if err != nil {
			return nil, err
		}
		names = append(names, list.Nodegroups...)
		return list.NextToken, nil
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, err
		}
		pollErr(sourceEKS, rc.region, err)
	}
	return names, nil
}

// evaluateNodegroup maps a node group's status + control-plane health onto a
// single firing-or-resolve decision (identity is cluster/nodegroup).
func evaluateNodegroup(region, cluster string, ng *ekstypes.Nodegroup, emit sources.Emit) {
	name := awssdk.ToString(ng.NodegroupName)
	if name == "" {
		return
	}
	id := cluster + "/" + name
	details := map[string]string{"cluster": cluster, "status": string(ng.Status)}
	switch ng.Status {
	case ekstypes.NodegroupStatusActive:
		// Healthy; fall through to the health-issue check below.
	case ekstypes.NodegroupStatusCreateFailed, ekstypes.NodegroupStatusDeleteFailed, ekstypes.NodegroupStatusDegraded:
		emitFiring(emit, alert.KindEKSNodegroup, region, id, "EKSNodegroupUnhealthy",
			"EKS node group "+id+" status is "+string(ng.Status), alert.SeverityCritical, details)
		return
	default:
		emitFiring(emit, alert.KindEKSNodegroup, region, id, "EKSNodegroupNotActive",
			"EKS node group "+id+" is not active (status "+string(ng.Status)+")", alert.SeverityWarning, details)
		return
	}
	if ng.Health != nil && len(ng.Health.Issues) > 0 {
		summary := nodegroupIssueSummary(ng.Health.Issues)
		emitFiring(emit, alert.KindEKSNodegroup, region, id, "EKSNodegroupHealthIssue",
			"EKS node group "+id+" reports health issues: "+summary, alert.SeverityWarning,
			map[string]string{"cluster": cluster, "status": string(ng.Status), "issues": summary})
		return
	}
	emitResolve(emit, alert.KindEKSNodegroup, region, id)
}

func nodegroupIssueSummary(issues []ekstypes.Issue) string {
	return issueSummary(issues, func(is ekstypes.Issue) (string, string) {
		return string(is.Code), awssdk.ToString(is.Message)
	})
}

// issueSummary renders EKS health issues as a compact "CODE: message; CODE:
// message" string for the alert body. Cluster and node-group issues are
// distinct SDK types with the same shape, so the rendering is generic over an
// extractor.
func issueSummary[T any](issues []T, extract func(T) (code, msg string)) string {
	parts := make([]string, 0, len(issues))
	for _, is := range issues {
		code, msg := extract(is)
		switch {
		case code != "" && msg != "":
			parts = append(parts, code+": "+msg)
		case code != "":
			parts = append(parts, code)
		case msg != "":
			parts = append(parts, msg)
		}
	}
	return strings.Join(parts, "; ")
}

// evaluateEKSCluster maps one cluster's state onto a single firing-or-resolve
// decision. At most one alert is emitted per cluster per poll, so the resolve
// (which clears every active alert for the cluster, across reasons) stays
// surgical and a transient status change cannot leave two alerts open.
func evaluateEKSCluster(region string, c *ekstypes.Cluster, emit sources.Emit) {
	name := awssdk.ToString(c.Name)
	if name == "" {
		return
	}
	switch c.Status {
	case ekstypes.ClusterStatusActive:
		// Fall through to the health-issue check below.
	case ekstypes.ClusterStatusFailed:
		emitFiring(emit, alert.KindEKSCluster, region, name, "EKSClusterFailed",
			"EKS cluster "+name+" is in FAILED state", alert.SeverityCritical,
			map[string]string{"status": string(c.Status)})
		return
	case ekstypes.ClusterStatusDeleting:
		emitFiring(emit, alert.KindEKSCluster, region, name, "EKSClusterDeleting",
			"EKS cluster "+name+" is being deleted", alert.SeverityCritical,
			map[string]string{"status": string(c.Status)})
		return
	default:
		// CREATING / UPDATING / PENDING: transient, not (fully) serving.
		emitFiring(emit, alert.KindEKSCluster, region, name, "EKSClusterNotActive",
			"EKS cluster "+name+" is not ACTIVE (status "+string(c.Status)+")", alert.SeverityWarning,
			map[string]string{"status": string(c.Status)})
		return
	}
	if c.Health != nil && len(c.Health.Issues) > 0 {
		summary := eksIssueSummary(c.Health.Issues)
		emitFiring(emit, alert.KindEKSCluster, region, name, "EKSClusterHealthIssue",
			"EKS cluster "+name+" reports health issues: "+summary, alert.SeverityWarning,
			map[string]string{"status": string(c.Status), "issues": summary})
		return
	}
	emitResolve(emit, alert.KindEKSCluster, region, name)
}

func eksIssueSummary(issues []ekstypes.ClusterIssue) string {
	return issueSummary(issues, func(is ekstypes.ClusterIssue) (string, string) {
		return string(is.Code), awssdk.ToString(is.Message)
	})
}

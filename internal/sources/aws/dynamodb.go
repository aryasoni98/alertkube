package aws

import (
	"context"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dynamodbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"alertkube/internal/alert"
	"alertkube/internal/sources"
)

const sourceDynamoDB = "aws-dynamodb"

type dynRegion = regionClient[dynamoDBAPI]

// dynamoDBSource discovers DynamoDB tables per region and alerts on tables
// whose status is not healthy. INACCESSIBLE_ENCRYPTION_CREDENTIALS is critical
// (the table is unreadable); ARCHIVED is a warning (the table is no longer
// usable); ACTIVE plus transient states (CREATING/UPDATING/DELETING/ARCHIVING)
// resolve.
type dynamoDBSource struct {
	regions []dynRegion
}

func (s *dynamoDBSource) Name() string { return sourceDynamoDB }

func (s *dynamoDBSource) Poll(ctx context.Context, emit sources.Emit) {
	pollByRegion(ctx, s.regions, emit, s.pollRegion)
}

func (s *dynamoDBSource) pollRegion(ctx context.Context, rc dynRegion, emit sources.Emit) {
	forEachPage(ctx, sourceDynamoDB, rc.region, func(ctx context.Context, start *string) (*string, error) {
		list, err := rc.client.ListTables(ctx, &dynamodb.ListTablesInput{ExclusiveStartTableName: start})
		if err != nil {
			return nil, err
		}
		for _, name := range list.TableNames {
			out, err := rc.client.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: awssdk.String(name)})
			if err != nil {
				pollErr(sourceDynamoDB, rc.region, err)
				continue
			}
			if out == nil || out.Table == nil {
				continue
			}
			evaluateDynamoTable(rc.region, name, out.Table.TableStatus, emit)
		}
		return list.LastEvaluatedTableName, nil
	})
}

func evaluateDynamoTable(region, name string, status dynamodbtypes.TableStatus, emit sources.Emit) {
	if name == "" {
		return
	}
	switch status {
	case dynamodbtypes.TableStatusInaccessibleEncryptionCredentials:
		emitFiring(emit, alert.KindDynamoDBTable, region, name, "DynamoDBTableInaccessible",
			"DynamoDB table "+name+" cannot access its encryption credentials", alert.SeverityCritical,
			map[string]string{"status": string(status)})
	case dynamodbtypes.TableStatusArchived:
		emitFiring(emit, alert.KindDynamoDBTable, region, name, "DynamoDBTableArchived",
			"DynamoDB table "+name+" is archived", alert.SeverityWarning,
			map[string]string{"status": string(status)})
	default:
		emitResolve(emit, alert.KindDynamoDBTable, region, name)
	}
}

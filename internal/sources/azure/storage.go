package azure

import (
	"context"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storage/armstorage"

	"alertkube/internal/alert"
	"alertkube/internal/sources"
)

const sourceAzureStorage = "azure-storage"

// storageLister lists storage accounts in one subscription. The real adapter
// drains the List pager; tests provide a fake.
type storageLister interface {
	List(ctx context.Context) ([]*armstorage.Account, error)
}

type armStorageLister struct {
	client *armstorage.AccountsClient
}

func (l *armStorageLister) List(ctx context.Context) ([]*armstorage.Account, error) {
	return drainPager(ctx, l.client.NewListPager(nil),
		func(r armstorage.AccountsClientListResponse) []*armstorage.Account { return r.Value })
}

type azureStorageSubscription = subLister[storageLister]

// azureStorageSource alerts on Storage accounts whose primary endpoint is
// unavailable (critical); available resolves. This is Azure's analog of the
// AWS S3 source.
type azureStorageSource struct {
	subs []azureStorageSubscription
}

func (s *azureStorageSource) Name() string { return sourceAzureStorage }

func (s *azureStorageSource) Poll(ctx context.Context, emit sources.Emit) {
	pollBySubscription(ctx, sourceAzureStorage, s.subs, emit, evaluateStorageAccount)
}

func evaluateStorageAccount(subscription string, acct *armstorage.Account, emit sources.Emit) {
	if acct == nil {
		return
	}
	name := strVal(acct.Name)
	if name == "" {
		return
	}
	region := strVal(acct.Location)
	scope := sources.Scope(subscription, region)
	var status string
	if acct.Properties != nil && acct.Properties.StatusOfPrimary != nil {
		status = string(*acct.Properties.StatusOfPrimary)
	}
	if status == string(armstorage.AccountStatusUnavailable) {
		emitFiring(emit, alert.KindAzureStorage, scope, name, "AzureStorageUnavailable",
			"Azure Storage account "+name+" primary endpoint is unavailable", alert.SeverityCritical,
			map[string]string{"statusOfPrimary": status, "location": region})
		return
	}
	emitResolve(emit, alert.KindAzureStorage, scope, name)
}

package azure

import (
	"context"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storage/armstorage"

	"github.com/aryasoni98/alertkube/v2/internal/alert"
	"github.com/aryasoni98/alertkube/v2/internal/sources"
)

const sourceAzureStorage = "azure-storage"

// armStorageLister lists storage accounts in one subscription by draining the
// List pager; tests provide a fake.
type armStorageLister struct {
	client *armstorage.AccountsClient
}

func (l *armStorageLister) List(ctx context.Context) ([]*armstorage.Account, error) {
	return drainPager(ctx, sourceAzureStorage, l.client.NewListPager(nil),
		func(r armstorage.AccountsClientListResponse) []*armstorage.Account { return r.Value })
}

// newAzureStorageSource alerts on Storage accounts whose primary endpoint is
// unavailable (critical); available resolves. This is Azure's analog of the
// AWS S3 source.
func newAzureStorageSource(subs []sources.Scoped[*armstorage.Account]) sources.Source {
	return sources.NewListSource(sourceAzureStorage, subs, evaluateStorageAccount)
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

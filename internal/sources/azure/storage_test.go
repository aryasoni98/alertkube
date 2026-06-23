package azure

import (
	"context"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storage/armstorage"

	"alertkube/internal/alert"
)

type fakeStorageLister struct {
	accounts []*armstorage.Account
	err      error
}

func (f *fakeStorageLister) List(context.Context) ([]*armstorage.Account, error) {
	return f.accounts, f.err
}

func storageAccount(name, location, status string) *armstorage.Account {
	a := &armstorage.Account{Name: sp(name), Location: sp(location)}
	if status != "" {
		st := armstorage.AccountStatus(status)
		a.Properties = &armstorage.AccountProperties{StatusOfPrimary: &st}
	}
	return a
}

func TestEvaluateStorageAccount(t *testing.T) {
	cases := []struct {
		name         string
		account      *armstorage.Account
		wantEmit     bool
		wantResolved bool
	}{
		{"unavailable critical", storageAccount("s", "eastus", string(armstorage.AccountStatusUnavailable)), true, false},
		{"available resolves", storageAccount("s", "eastus", string(armstorage.AccountStatusAvailable)), true, true},
		{"no status resolves", storageAccount("s", "eastus", ""), true, true},
		{"empty name skipped", storageAccount("", "eastus", string(armstorage.AccountStatusUnavailable)), false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			emit, got := collect()
			evaluateStorageAccount("sub-1", tc.account, emit)
			if !tc.wantEmit {
				if len(*got) != 0 {
					t.Fatalf("expected no emit, got %d", len(*got))
				}
				return
			}
			if len(*got) != 1 {
				t.Fatalf("expected 1 alert, got %d", len(*got))
			}
			a := (*got)[0]
			if a.Kind != alert.KindAzureStorage || a.Namespace != "sub-1/eastus" {
				t.Errorf("identity: kind=%s ns=%s", a.Kind, a.Namespace)
			}
			if a.Resolved != tc.wantResolved {
				t.Fatalf("resolved = %v, want %v", a.Resolved, tc.wantResolved)
			}
			if !tc.wantResolved && a.Severity != alert.SeverityCritical {
				t.Errorf("severity = %q, want critical", a.Severity)
			}
		})
	}
}

func TestAzureStorageSourcePoll(t *testing.T) {
	fake := &fakeStorageLister{accounts: []*armstorage.Account{
		storageAccount("good", "eastus", string(armstorage.AccountStatusAvailable)),
		storageAccount("bad", "westus", string(armstorage.AccountStatusUnavailable)),
	}}
	src := &azureStorageSource{subs: []azureStorageSubscription{{subscription: "sub-1", lister: fake}}}
	emit, got := collect()
	src.Poll(context.Background(), emit)
	if len(*got) != 2 {
		t.Fatalf("expected 2 alerts, got %d", len(*got))
	}
}

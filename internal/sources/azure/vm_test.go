package azure

import (
	"context"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v6"

	"alertkube/internal/alert"
)

type fakeVMLister struct {
	vms []*armcompute.VirtualMachine
	err error
}

func (f *fakeVMLister) List(context.Context) ([]*armcompute.VirtualMachine, error) {
	return f.vms, f.err
}

func vm(name, location, provState string) *armcompute.VirtualMachine {
	return &armcompute.VirtualMachine{
		Name:       sp(name),
		Location:   sp(location),
		Properties: &armcompute.VirtualMachineProperties{ProvisioningState: sp(provState)},
	}
}

func TestEvaluateVM(t *testing.T) {
	cases := []struct {
		name         string
		vm           *armcompute.VirtualMachine
		wantEmit     bool
		wantResolved bool
	}{
		{"failed critical", vm("v", "eastus", "Failed"), true, false},
		{"succeeded resolves", vm("v", "eastus", "Succeeded"), true, true},
		{"updating resolves", vm("v", "eastus", "Updating"), true, true},
		{"empty name skipped", vm("", "eastus", "Failed"), false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			emit, got := collect()
			evaluateVM("sub-1", tc.vm, emit)
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
			if a.Kind != alert.KindAzureVM || a.Namespace != "sub-1/eastus" {
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

func TestAzureVMSourcePoll(t *testing.T) {
	fake := &fakeVMLister{vms: []*armcompute.VirtualMachine{
		vm("good", "eastus", "Succeeded"),
		vm("bad", "westus", "Failed"),
	}}
	src := &azureVMSource{subs: []azureVMSubscription{{subscription: "sub-1", lister: fake}}}
	emit, got := collect()
	src.Poll(context.Background(), emit)
	if len(*got) != 2 {
		t.Fatalf("expected 2 alerts, got %d", len(*got))
	}
	for _, a := range *got {
		switch a.Name {
		case "good":
			if !a.Resolved {
				t.Errorf("good should resolve: %+v", a)
			}
		case "bad":
			if a.Resolved || a.Severity != alert.SeverityCritical {
				t.Errorf("bad should be critical: %+v", a)
			}
		default:
			t.Errorf("unexpected vm %q", a.Name)
		}
	}
}

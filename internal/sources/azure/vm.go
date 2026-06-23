package azure

import (
	"context"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v6"

	"alertkube/internal/alert"
	"alertkube/internal/sources"
)

const sourceAzureVM = "azure-vm"

// vmLister lists virtual machines in one subscription. The real adapter drains
// the ListAll pager; tests provide a fake.
type vmLister interface {
	List(ctx context.Context) ([]*armcompute.VirtualMachine, error)
}

type armVMLister struct {
	client *armcompute.VirtualMachinesClient
}

func (l *armVMLister) List(ctx context.Context) ([]*armcompute.VirtualMachine, error) {
	var out []*armcompute.VirtualMachine
	pager := l.client.NewListAllPager(nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		out = append(out, page.Value...)
	}
	return out, nil
}

type azureVMSubscription struct {
	subscription string
	lister       vmLister
}

// azureVMSource alerts on Azure VMs whose provisioning state is Failed
// (critical). Power state (an intentional stop/deallocate) is deliberately not
// alerted, mirroring the AWS EC2 source's "alert on genuine failure, not on an
// intentional stop" philosophy. Everything else resolves.
type azureVMSource struct {
	subs []azureVMSubscription
}

func (s *azureVMSource) Name() string { return sourceAzureVM }

func (s *azureVMSource) Poll(ctx context.Context, emit sources.Emit) {
	for _, sub := range s.subs {
		vms, err := sub.lister.List(ctx)
		if err != nil {
			pollErr(sourceAzureVM, sub.subscription, err)
			continue
		}
		for _, vm := range vms {
			evaluateVM(sub.subscription, vm, emit)
		}
	}
}

func evaluateVM(subscription string, vm *armcompute.VirtualMachine, emit sources.Emit) {
	if vm == nil {
		return
	}
	name := strVal(vm.Name)
	if name == "" {
		return
	}
	region := strVal(vm.Location)
	scope := subscription
	if region != "" {
		scope = subscription + "/" + region
	}
	var state string
	if vm.Properties != nil {
		state = strVal(vm.Properties.ProvisioningState)
	}
	if state == "Failed" {
		emitFiring(emit, alert.KindAzureVM, scope, name, "AzureVMProvisioningFailed",
			"Azure VM "+name+" provisioning state is Failed", alert.SeverityCritical,
			map[string]string{"provisioningState": state, "location": region})
		return
	}
	emitResolve(emit, alert.KindAzureVM, scope, name)
}

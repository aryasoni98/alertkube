package azure

import (
	"context"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v6"

	"github.com/aryasoni98/alertkube/v2/internal/alert"
	"github.com/aryasoni98/alertkube/v2/internal/sources"
)

const sourceAzureVM = "azure-vm"

// armVMLister lists virtual machines in one subscription by draining the
// ListAll pager; tests provide a fake.
type armVMLister struct {
	client *armcompute.VirtualMachinesClient
}

func (l *armVMLister) List(ctx context.Context) ([]*armcompute.VirtualMachine, error) {
	return drainPager(ctx, sourceAzureVM, l.client.NewListAllPager(nil),
		func(r armcompute.VirtualMachinesClientListAllResponse) []*armcompute.VirtualMachine { return r.Value })
}

// newAzureVMSource alerts on Azure VMs whose provisioning state is Failed
// (critical). Power state (an intentional stop/deallocate) is deliberately not
// alerted, mirroring the AWS EC2 source's "alert on genuine failure, not on an
// intentional stop" philosophy. Everything else resolves.
func newAzureVMSource(subs []sources.Scoped[*armcompute.VirtualMachine]) sources.Source {
	return sources.NewListSource(sourceAzureVM, subs, evaluateVM)
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
	scope := sources.Scope(subscription, region)
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

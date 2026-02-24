package commitments

import (
	"context"
	"fmt"

	"github.com/koptimizer/koptimizer/pkg/cloudprovider"
)

// Importer imports commitments from cloud provider APIs.
type Importer struct {
	provider cloudprovider.CloudProvider
}

func NewImporter(provider cloudprovider.CloudProvider) *Importer {
	return &Importer{provider: provider}
}

// ImportAll imports all types of commitments from the cloud provider.
func (i *Importer) ImportAll(ctx context.Context) ([]*cloudprovider.Commitment, error) {
	var all []*cloudprovider.Commitment

	ris, err := i.provider.GetReservedInstances(ctx)
	if err != nil {
		return nil, fmt.Errorf("importing reserved instances: %w", err)
	}
	all = append(all, ris...)

	sps, err := i.provider.GetSavingsPlans(ctx)
	if err != nil {
		return nil, fmt.Errorf("importing savings plans: %w", err)
	}
	all = append(all, sps...)

	cuds, err := i.provider.GetCommittedUseDiscounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("importing CUDs: %w", err)
	}
	all = append(all, cuds...)

	res, err := i.provider.GetReservations(ctx)
	if err != nil {
		return nil, fmt.Errorf("importing reservations: %w", err)
	}
	all = append(all, res...)

	return all, nil
}

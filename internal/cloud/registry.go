package cloud

import (
	"database/sql"
	"fmt"

	"github.com/koptimizer/koptimizer/internal/cloud/aws"
	"github.com/koptimizer/koptimizer/internal/cloud/azure"
	"github.com/koptimizer/koptimizer/internal/cloud/gcp"
	"github.com/koptimizer/koptimizer/internal/store"
	"github.com/koptimizer/koptimizer/pkg/cloudprovider"
)

// NewProvider creates a CloudProvider for the given cloud.
// The db parameter is optional; if non-nil it enables SQLite-backed pricing caching.
func NewProvider(cloud, region string, db *sql.DB) (cloudprovider.CloudProvider, error) {
	var pCache *store.PricingCache
	if db != nil {
		pCache = store.NewPricingCache(db)
	}

	switch cloud {
	case "aws":
		return aws.NewProvider(region, pCache)
	case "gcp":
		return gcp.NewProvider(region, pCache)
	case "azure":
		return azure.NewProvider(region, pCache)
	case "":
		return nil, fmt.Errorf("cloudProvider is required: set to 'aws', 'gcp', or 'azure' in config")
	default:
		return nil, fmt.Errorf("unsupported cloud provider: %s", cloud)
	}
}

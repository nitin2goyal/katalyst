package apiserver

import (
	"fmt"
	"net/http"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/koptimizer/koptimizer/internal/config"
	intmetrics "github.com/koptimizer/koptimizer/internal/metrics"
	"github.com/koptimizer/koptimizer/internal/state"
	"github.com/koptimizer/koptimizer/internal/store"
	"github.com/koptimizer/koptimizer/pkg/cloudprovider"
	"github.com/koptimizer/koptimizer/pkg/familylock"
)

// NewServer creates a new HTTP server for the REST API.
func NewServer(cfg *config.Config, clusterState *state.ClusterState, provider cloudprovider.CloudProvider, guard *familylock.FamilyLockGuard, k8sClient client.Client, costStore *store.CostStore, metricsStore *intmetrics.Store) *http.Server {
	router := NewRouter(cfg, clusterState, provider, guard, k8sClient, costStore, metricsStore)

	return &http.Server{
		Addr:         fmt.Sprintf("%s:%d", cfg.APIServer.Address, cfg.APIServer.Port),
		Handler:      router,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}
}

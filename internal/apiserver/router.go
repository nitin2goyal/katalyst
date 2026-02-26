package apiserver

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/koptimizer/koptimizer/internal/apiserver/handler"
	"github.com/koptimizer/koptimizer/internal/config"
	"github.com/koptimizer/koptimizer/internal/state"
	"github.com/koptimizer/koptimizer/internal/store"
	"github.com/koptimizer/koptimizer/pkg/cloudprovider"
	"github.com/koptimizer/koptimizer/pkg/familylock"
)

// NewRouter creates the API router with all endpoints.
func NewRouter(cfg *config.Config, clusterState *state.ClusterState, provider cloudprovider.CloudProvider, guard *familylock.FamilyLockGuard, k8sClient client.Client, costStore *store.CostStore) http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.RequestID)

	clusterHandler := handler.NewClusterHandler(clusterState, provider, cfg, k8sClient)
	nodeHandler := handler.NewNodeHandler(clusterState)
	nodeGroupHandler := handler.NewNodeGroupHandler(clusterState, guard)
	costHandler := handler.NewCostHandler(clusterState, provider, k8sClient, costStore)
	recHandler := handler.NewRecommendationHandler(clusterState, k8sClient)
	workloadHandler := handler.NewWorkloadHandler(clusterState)
	commitmentHandler := handler.NewCommitmentHandler(provider)
	gpuHandler := handler.NewGPUHandler(clusterState)
	spotHandler := handler.NewSpotHandler(clusterState, provider)
	storageHandler := handler.NewStorageHandler(k8sClient, cfg)
	networkHandler := handler.NewNetworkHandler(clusterState, cfg)
	configHandler := handler.NewConfigHandler(cfg)
	auditHandler := handler.NewAuditHandler(clusterState.AuditLog)
	idleHandler := handler.NewIdleResourceHandler(clusterState, k8sClient, cfg)
	notifHandler := handler.NewNotificationHandler(clusterState.AuditLog, cfg)
	metricsHandler := handler.NewMetricsHandler(clusterState, provider, k8sClient, cfg)
	policyHandler := handler.NewPolicyHandler(clusterState, cfg)

	r.Route("/api/v1", func(r chi.Router) {
		// Cluster
		r.Get("/cluster/summary", clusterHandler.GetSummary)
		r.Get("/cluster/health", clusterHandler.GetHealth)
		r.Get("/cluster/efficiency", clusterHandler.GetEfficiency)
		r.Get("/cluster/score", clusterHandler.GetScore)

		// Node Groups (literal routes before parameterized to avoid conflicts)
		r.Get("/nodegroups", nodeGroupHandler.List)
		r.Get("/nodegroups/empty", nodeGroupHandler.GetEmpty)
		r.Get("/nodegroups/{id}", nodeGroupHandler.Get)
		r.Get("/nodegroups/{id}/nodes", nodeGroupHandler.GetNodes)

		// Nodes
		r.Get("/nodes", nodeHandler.List)
		r.Get("/nodes/{name}", nodeHandler.Get)

		// Cost (literal routes before parameterized)
		r.Get("/cost/summary", costHandler.GetSummary)
		r.Get("/cost/by-namespace", costHandler.GetByNamespace)
		r.Get("/cost/by-workload", costHandler.GetByWorkload)
		r.Get("/cost/by-label", costHandler.GetByLabel)
		r.Get("/cost/trend", costHandler.GetTrend)
		r.Get("/cost/savings", costHandler.GetSavings)
		r.Get("/cost/network", networkHandler.GetCost)
		r.Get("/cost/comparison", costHandler.GetComparison)
		r.Get("/cost/impact", costHandler.GetImpact)

		// Commitments
		r.Get("/commitments", commitmentHandler.List)
		r.Get("/commitments/underutilized", commitmentHandler.GetUnderutilized)
		r.Get("/commitments/expiring", commitmentHandler.GetExpiring)

		// Recommendations (literal routes before parameterized)
		r.Get("/recommendations", recHandler.List)
		r.Get("/recommendations/summary", recHandler.GetSummary)
		r.Get("/recommendations/{id}", recHandler.Get)
		r.Post("/recommendations/{id}/approve", recHandler.Approve)
		r.Post("/recommendations/{id}/dismiss", recHandler.Dismiss)

		// Workloads (literal routes BEFORE parameterized to avoid chi conflict)
		r.Get("/workloads", workloadHandler.List)
		r.Get("/workloads/efficiency", workloadHandler.GetEfficiency)
		r.Get("/workloads/{ns}/{kind}/{name}", workloadHandler.Get)
		r.Get("/workloads/{ns}/{kind}/{name}/rightsizing", workloadHandler.GetRightsizing)
		r.Get("/workloads/{ns}/{kind}/{name}/scaling", workloadHandler.GetScaling)

		// GPU
		r.Get("/gpu/nodes", gpuHandler.GetNodes)
		r.Get("/gpu/utilization", gpuHandler.GetUtilization)
		r.Get("/gpu/recommendations", gpuHandler.GetRecommendations)

		// Spot instances
		r.Get("/spot/summary", spotHandler.GetSummary)
		r.Get("/spot/nodes", spotHandler.GetNodes)

		// Storage
		r.Get("/storage/summary", storageHandler.GetSummary)
		r.Get("/storage/pvcs", storageHandler.GetPVCs)

		// Network
		r.Get("/network/summary", networkHandler.GetSummary)

		// Config
		r.Get("/config", configHandler.Get)
		r.Put("/config/mode", configHandler.SetMode)

		// Audit
		r.Get("/audit", auditHandler.List)

		// Diagnostics
		r.Get("/diagnostics", clusterHandler.GetDiagnostics)

		// New endpoints
		r.Get("/events", auditHandler.ListEvents)
		r.Get("/clusters", clusterHandler.GetClusters)
		r.Get("/idle-resources", idleHandler.Get)
		r.Get("/notifications", notifHandler.Get)
		r.Get("/policies", policyHandler.Get)
		r.Get("/metrics", metricsHandler.Get)
	})

	return r
}

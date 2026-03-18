package apiserver

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/koptimizer/koptimizer/internal/apiserver/handler"
	"github.com/koptimizer/koptimizer/internal/config"
	"github.com/koptimizer/koptimizer/internal/helmdrift"
	intmetrics "github.com/koptimizer/koptimizer/internal/metrics"
	"github.com/koptimizer/koptimizer/internal/state"
	"github.com/koptimizer/koptimizer/internal/store"
	"github.com/koptimizer/koptimizer/pkg/cloudprovider"
	"github.com/koptimizer/koptimizer/pkg/familylock"
)

// NewRouter creates the API router with all endpoints.
func NewRouter(cfg *config.Config, clusterState *state.ClusterState, provider cloudprovider.CloudProvider, guard *familylock.FamilyLockGuard, k8sClient client.Client, costStore *store.CostStore, metricsStore *intmetrics.Store, settingsStore *store.SettingsStore) http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.RequestID)
	r.Use(middleware.Throttle(100))

	// Limit request body size to 1MB to prevent memory exhaustion.
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1MB
			next.ServeHTTP(w, r)
		})
	})

	clusterHandler := handler.NewClusterHandler(clusterState, provider, cfg, k8sClient, metricsStore)
	nodeHandler := handler.NewNodeHandler(clusterState)
	nodeGroupHandler := handler.NewNodeGroupHandler(clusterState, guard)
	costHandler := handler.NewCostHandler(clusterState, provider, k8sClient, costStore, metricsStore)
	recHandler := handler.NewRecommendationHandler(clusterState, k8sClient, metricsStore)
	workloadHandler := handler.NewWorkloadHandler(clusterState, k8sClient)
	commitmentHandler := handler.NewCommitmentHandler(provider)
	gpuHandler := handler.NewGPUHandler(clusterState, cfg)
	storageHandler := handler.NewStorageHandler(k8sClient, cfg)
	networkHandler := handler.NewNetworkHandler(clusterState, cfg)
	configHandler := handler.NewConfigHandler(cfg, settingsStore)
	auditHandler := handler.NewAuditHandler(clusterState.AuditLog)
	idleHandler := handler.NewIdleResourceHandler(clusterState, k8sClient, cfg)
	notifHandler := handler.NewNotificationHandler(clusterState.AuditLog, cfg, settingsStore)
	metricsHandler := handler.NewMetricsHandler(clusterState, provider, k8sClient, cfg)
	policyHandler := handler.NewPolicyHandler(clusterState, cfg)
	actionsHandler := handler.NewActionsHandler(clusterState, k8sClient)
	autoscalerHandler := handler.NewAutoscalerHandler(clusterState, cfg, k8sClient)
	scaledownHandler := handler.NewScaleDownBlockersHandler(clusterState, k8sClient)
	overscaledHandler := handler.NewOverscaledHandler(clusterState, k8sClient)
	inefficiencyHandler := handler.NewInefficiencyHandler(clusterState, k8sClient)
	helmDriftSvc := helmdrift.NewService(cfg, clusterState)
	helmDriftHandler := handler.NewHelmDriftHandler(helmDriftSvc)

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
		r.Get("/recommendations/debug", recHandler.Debug)
		r.Post("/recommendations/bulk-approve", recHandler.BulkApprove)
		r.Post("/recommendations/bulk-dismiss", recHandler.BulkDismiss)
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
		r.Get("/gpu/activity", gpuHandler.GetActivity)
		r.Get("/gpu/scavenging", gpuHandler.GetScavenging)

		// Storage
		r.Get("/storage/summary", storageHandler.GetSummary)
		r.Get("/storage/pvcs", storageHandler.GetPVCs)

		// Network
		r.Get("/network/summary", networkHandler.GetSummary)

		// Config
		r.Get("/config", configHandler.Get)
		r.Put("/config/mode", configHandler.SetMode)
		r.Put("/config/pod-purger", configHandler.SetPodPurger)
		r.Put("/config/controllers/{name}", configHandler.SetController)
		r.Put("/config/controllers/{name}/auto-approve", configHandler.SetAutoApprove)

		// Audit
		r.Get("/audit", auditHandler.List)
		r.Post("/audit", auditHandler.Record)

		// Diagnostics
		r.Get("/diagnostics", clusterHandler.GetDiagnostics)

		// New endpoints
		r.Get("/events", auditHandler.ListEvents)
		r.Get("/clusters", clusterHandler.GetClusters)
		r.Get("/idle-resources", idleHandler.Get)
		r.Get("/notifications", notifHandler.Get)
		r.Post("/notifications/channels", notifHandler.AddChannel)
		r.Put("/notifications/channels/{idx}", notifHandler.ToggleChannel)
		r.Delete("/notifications/channels/{idx}", notifHandler.DeleteChannel)
		r.Get("/policies", policyHandler.Get)
		r.Get("/metrics", metricsHandler.Get)

		// Actions
		r.Get("/actions/bad-pods", actionsHandler.ListBadPods)
		r.Post("/actions/delete-pods", actionsHandler.DeletePods)
		r.Get("/actions/bad-replicasets", actionsHandler.ListBadReplicaSets)
		r.Post("/actions/delete-replicasets", actionsHandler.DeleteReplicaSets)

		// Autoscaler
		r.Get("/autoscaler/status", autoscalerHandler.GetStatus)
		r.Get("/autoscaler/events", autoscalerHandler.GetEvents)
		r.Get("/autoscaler/overscaled", overscaledHandler.Get)

		// Scale-Down Blockers
		r.Get("/scaledown/blockers", scaledownHandler.GetBlockers)
		r.Post("/scaledown/delete-pdbs", scaledownHandler.DeletePDBs)

		// Cluster Inefficiencies
		r.Get("/inefficiencies", inefficiencyHandler.Get)

		// Helm Drift
		r.Get("/helm-drift", helmDriftHandler.Get)
	})

	return r
}

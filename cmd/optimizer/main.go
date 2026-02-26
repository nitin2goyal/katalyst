package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	koptv1alpha1 "github.com/koptimizer/koptimizer/api/v1alpha1"
	"github.com/koptimizer/koptimizer/internal/apiserver"
	"github.com/koptimizer/koptimizer/internal/cloud"
	"github.com/koptimizer/koptimizer/internal/config"
	"github.com/koptimizer/koptimizer/internal/controller/alerts"
	"github.com/koptimizer/koptimizer/internal/controller/commitments"
	"github.com/koptimizer/koptimizer/internal/controller/costmonitor"
	"github.com/koptimizer/koptimizer/internal/controller/evictor"
	"github.com/koptimizer/koptimizer/internal/controller/gpu"
	"github.com/koptimizer/koptimizer/internal/controller/hibernation"
	"github.com/koptimizer/koptimizer/internal/controller/network"
	"github.com/koptimizer/koptimizer/internal/controller/nodeautoscaler"
	"github.com/koptimizer/koptimizer/internal/controller/nodetemplates"
	"github.com/koptimizer/koptimizer/internal/controller/nodegroupmgr"
	"github.com/koptimizer/koptimizer/internal/controller/rebalancer"
	"github.com/koptimizer/koptimizer/internal/controller/rightsizer"
	"github.com/koptimizer/koptimizer/internal/controller/spot"
	"github.com/koptimizer/koptimizer/internal/controller/storage"
	"github.com/koptimizer/koptimizer/internal/controller/workloadscaler"
	intmetrics "github.com/koptimizer/koptimizer/internal/metrics"
	"github.com/koptimizer/koptimizer/internal/state"
	"github.com/koptimizer/koptimizer/internal/store"
	"github.com/koptimizer/koptimizer/pkg/aigate"
	"github.com/koptimizer/koptimizer/pkg/cloudprovider"
	"github.com/koptimizer/koptimizer/pkg/familylock"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(metricsv1beta1.AddToScheme(scheme))
	utilruntime.Must(koptv1alpha1.AddToScheme(scheme))
}

func main() {
	var configFile string
	var metricsAddr string
	var probeAddr string

	flag.StringVar(&configFile, "config", "/etc/koptimizer/config.yaml", "Path to config file")
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":9090", "The address the metric endpoint binds to")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to")

	opts := zap.Options{Development: false}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	// Load configuration
	cfg, err := config.LoadFromFile(configFile)
	if err != nil {
		setupLog.Error(err, "Failed to load config file, falling back to defaults/env", "path", configFile)
		cfg = config.DefaultConfig()
	}

	if err := cfg.ValidateDetailed(); err != nil {
		setupLog.Error(err, "Invalid configuration",
			"cloudProvider", cfg.CloudProvider,
			"region", cfg.Region,
			"configFile", configFile,
		)
		os.Exit(1)
	}

	setupLog.Info("Starting KOptimizer",
		"mode", cfg.Mode,
		"cloudProvider", cfg.CloudProvider,
		"region", cfg.Region,
	)

	// Open SQLite database (nil-safe: if it fails, everything works in-memory)
	var appDB *store.DB
	if cfg.Database.Path != "" {
		var dbErr error
		appDB, dbErr = store.Open(store.Config{
			Path:          cfg.Database.Path,
			RetentionDays: cfg.Database.RetentionDays,
		})
		if dbErr != nil {
			setupLog.Info("Database open failed, continuing with in-memory mode", "error", dbErr)
		} else {
			setupLog.Info("Database opened", "path", cfg.Database.Path)
		}
	}

	// Extract raw *sql.DB and create async writer (both nil-safe)
	var sqlDBRef *sql.DB
	var dbWriter *store.Writer
	if appDB != nil {
		sqlDBRef = appDB.RawDB()
		dbWriter = store.NewWriter(sqlDBRef, 4096)
	}

	// Start the async writer background goroutine
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if dbWriter != nil {
		dbWriter.Run(ctx)
	}

	// Create controller manager
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: metricsAddr,
		},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         true,
		LeaderElectionID:       "koptimizer-leader",
	})
	if err != nil {
		setupLog.Error(err, "Unable to create manager")
		os.Exit(1)
	}

	// Initialize cloud provider (pass sqlDBRef for SQLite-backed pricing cache)
	provider, err := cloud.NewProvider(cfg.CloudProvider, cfg.Region, sqlDBRef)
	if err != nil {
		setupLog.Error(err, "Unable to create cloud provider")
		os.Exit(1)
	}
	// Start background pricing cache refresh if the provider supports it.
	// Uses a cancellable context that will be cancelled on process exit.
	bgCtx, bgCancel := context.WithCancel(context.Background())
	defer bgCancel()
	if br, ok := provider.(cloudprovider.BackgroundRefresher); ok {
		br.StartBackgroundRefresh(bgCtx)
	}

	// Initialize metrics collector
	metricsCollector := intmetrics.NewCollector(mgr.GetClient())

	// Initialize metrics store for time-series data (percentiles)
	var metricsStore *intmetrics.Store
	if sqlDBRef != nil {
		metricsStore = intmetrics.NewStoreWithDB(7*24*time.Hour, sqlDBRef, dbWriter)
	} else {
		metricsStore = intmetrics.NewStore(7 * 24 * time.Hour)
	}

	// Initialize cluster state (audit log backed by SQLite when available)
	clusterState := state.NewClusterState(mgr.GetClient(), provider, metricsCollector, sqlDBRef, dbWriter, metricsStore)

	// Initialize cost store (nil-safe)
	costStore := store.NewCostStore(sqlDBRef)

	// Initialize family lock guard
	guard := familylock.NewFamilyLockGuard(provider)

	// Initialize AI Safety Gate
	aiGateCfg := aigate.Config{
		Enabled:           cfg.AIGate.Enabled,
		Model:             cfg.AIGate.Model,
		Timeout:           cfg.AIGate.Timeout,
		CostThresholdUSD:  cfg.AIGate.CostThresholdUSD,
		ScaleThresholdPct: cfg.AIGate.ScaleThresholdPct,
		MaxEvictNodes:     cfg.AIGate.MaxEvictNodes,
	}
	// Configure business hours timezone for the AI Gate prompt.
	if cfg.AIGate.Timezone != "" {
		if loc, tzErr := time.LoadLocation(cfg.AIGate.Timezone); tzErr == nil {
			aigate.Timezone = loc
		} else {
			setupLog.Error(tzErr, "Invalid AI Gate timezone, falling back to UTC", "timezone", cfg.AIGate.Timezone)
		}
	}
	gate, err := aigate.NewAIGate(aiGateCfg)
	if err != nil {
		setupLog.Error(err, "Unable to create AI Safety Gate")
		os.Exit(1)
	}

	// Register controllers based on config
	if cfg.CostMonitor.Enabled {
		if err := costmonitor.NewController(mgr, provider, clusterState, cfg, costStore).SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "Unable to create controller", "controller", "CostMonitor")
			os.Exit(1)
		}
	}

	if cfg.Commitments.Enabled {
		if err := commitments.NewController(mgr, provider, cfg).SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "Unable to create controller", "controller", "Commitments")
			os.Exit(1)
		}
	}

	if cfg.NodeAutoscaler.Enabled {
		if err := nodeautoscaler.NewController(mgr, provider, clusterState, guard, gate, cfg).SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "Unable to create controller", "controller", "NodeAutoscaler")
			os.Exit(1)
		}
	}

	if cfg.NodeGroupMgr.Enabled {
		if err := nodegroupmgr.NewController(mgr, provider, clusterState, guard, gate, cfg).SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "Unable to create controller", "controller", "NodeGroupMgr")
			os.Exit(1)
		}
	}

	if cfg.Rightsizer.Enabled {
		if err := rightsizer.NewController(mgr, clusterState, gate, cfg, metricsStore).SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "Unable to create controller", "controller", "Rightsizer")
			os.Exit(1)
		}
	}

	if cfg.WorkloadScaler.Enabled {
		if err := workloadscaler.NewController(mgr, clusterState, guard, gate, cfg).SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "Unable to create controller", "controller", "WorkloadScaler")
			os.Exit(1)
		}
	}

	if cfg.Evictor.Enabled {
		if err := evictor.NewController(mgr, provider, clusterState, guard, gate, cfg).SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "Unable to create controller", "controller", "Evictor")
			os.Exit(1)
		}
	}

	if cfg.Rebalancer.Enabled {
		if err := rebalancer.NewController(mgr, provider, clusterState, guard, gate, cfg).SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "Unable to create controller", "controller", "Rebalancer")
			os.Exit(1)
		}
	}

	if cfg.GPU.Enabled {
		if err := gpu.NewController(mgr, clusterState, guard, gate, cfg).SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "Unable to create controller", "controller", "GPU")
			os.Exit(1)
		}
	}

	if cfg.Spot.Enabled {
		if err := spot.NewController(mgr, provider, clusterState, guard, gate, cfg).SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "Unable to create controller", "controller", "Spot")
			os.Exit(1)
		}
	}

	if cfg.Hibernation.Enabled {
		if err := hibernation.NewController(mgr, provider, clusterState, guard, gate, cfg).SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "Unable to create controller", "controller", "Hibernation")
			os.Exit(1)
		}
	}

	if cfg.StorageMonitor.Enabled {
		if err := storage.NewController(mgr, cfg).SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "Unable to create controller", "controller", "StorageMonitor")
			os.Exit(1)
		}
	}

	if cfg.NetworkMonitor.Enabled {
		if err := network.NewController(mgr, provider, clusterState, cfg).SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "Unable to create controller", "controller", "NetworkMonitor")
			os.Exit(1)
		}
	}

	// Node templates — always enabled when node autoscaler is enabled
	if cfg.NodeAutoscaler.Enabled {
		if err := nodetemplates.NewController(mgr, provider, clusterState, cfg).SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "Unable to create controller", "controller", "NodeTemplates")
			os.Exit(1)
		}
	}

	if cfg.Alerts.Enabled {
		if err := alerts.NewController(mgr, clusterState, cfg, sqlDBRef, dbWriter).SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "Unable to create controller", "controller", "Alerts")
			os.Exit(1)
		}
	}

	// Health checks
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "Unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "Unable to set up ready check")
		os.Exit(1)
	}

	// Start REST API server
	var apiSrv *http.Server
	if cfg.APIServer.Enabled {
		apiSrv = apiserver.NewServer(cfg, clusterState, provider, guard, mgr.GetClient(), costStore, metricsStore)
		go func() {
			addr := fmt.Sprintf("%s:%d", cfg.APIServer.Address, cfg.APIServer.Port)
			setupLog.Info("Starting API server", "address", addr)
			if err := apiSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				setupLog.Error(err, "API server error")
			}
		}()
	}

	// Start periodic state refresh
	go func() {
		ticker := time.NewTicker(cfg.ReconcileInterval)
		defer ticker.Stop()

		// Hourly cleanup ticker for database
		cleanupTicker := time.NewTicker(1 * time.Hour)
		defer cleanupTicker.Stop()

		for {
			select {
			case <-ticker.C:
				refreshCtx, refreshCancel := context.WithTimeout(ctx, 2*time.Minute)
				if err := clusterState.Refresh(refreshCtx); err != nil {
					setupLog.Error(err, "Failed to refresh cluster state")
				}
				if err := guard.Refresh(refreshCtx); err != nil {
					setupLog.Error(err, "Failed to refresh family lock guard")
				}
				refreshCancel()
			case <-cleanupTicker.C:
				if appDB != nil {
					if err := appDB.Cleanup(); err != nil {
						setupLog.Error(err, "Database cleanup failed")
					}
				}
				if dbWriter != nil {
					if n := dbWriter.DroppedCount(); n > 0 {
						setupLog.Info("Database writer drops detected", "totalDropped", n)
					}
				}
				// Purge stale metrics series keys to prevent unbounded memory growth.
				metricsStore.Cleanup()
				// Expire stale node locks from controllers that crashed or hung.
				clusterState.NodeLock.ExpireStale(10 * time.Minute)
			case <-ctx.Done():
				return
			}
		}
	}()

	// Register a shutdown runnable so that the manager drives the cleanup
	// lifecycle, eliminating the race between a custom signal goroutine and
	// ctrl.SetupSignalHandler().
	mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		// Block until the manager signals shutdown.
		<-ctx.Done()
		setupLog.Info("Manager context cancelled, running shutdown tasks")
		cancel() // cancel the local context used by state refresh / db writer
		if apiSrv != nil {
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer shutdownCancel()
			apiSrv.Shutdown(shutdownCtx)
		}
		// Expire any stale node locks from controllers that didn't clean up.
		clusterState.NodeLock.ExpireStale(10 * time.Minute)
		// Drain async writer before closing DB to flush pending writes.
		if dbWriter != nil {
			dbWriter.Drain()
		}
		if appDB != nil {
			appDB.Close()
		}
		return nil
	}))

	// Start controller manager — SetupSignalHandler is the single signal
	// handler, avoiding the previous race with a separate goroutine.
	setupLog.Info("Starting controller manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "Problem running manager")
		os.Exit(1)
	}
}

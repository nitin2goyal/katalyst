package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	intmetrics "github.com/koptimizer/koptimizer/internal/metrics"
	"github.com/koptimizer/koptimizer/internal/store"
	"github.com/koptimizer/koptimizer/pkg/cloudprovider"
	"github.com/koptimizer/koptimizer/pkg/familylock"
	pkgmetrics "github.com/koptimizer/koptimizer/pkg/metrics"
)

// AutoscalerInfo holds the autoscaler configuration for a workload.
type AutoscalerInfo struct {
	Kind        string // "HPA" or "ScaledObject"
	Name        string
	MinReplicas int32
	MaxReplicas int32
}

// VPAInfo holds information about a VerticalPodAutoscaler targeting a workload.
type VPAInfo struct {
	Name       string // VPA object name
	UpdateMode string // "Auto", "Recreate", "Initial", or "Off"
}

// ClusterState maintains an in-memory cache of the cluster state.
type ClusterState struct {
	mu               sync.RWMutex
	client           client.Client
	reader           client.Reader // direct (non-cached) reader for listing pods/nodes
	provider         cloudprovider.CloudProvider
	metrics          pkgmetrics.MetricsCollector
	nodes            map[string]*NodeState
	pods             map[string]*PodState // key: namespace/name
	autoscalers      map[string]*AutoscalerInfo // key: namespace/Kind/targetName
	vpas             map[string]*VPAInfo        // key: namespace/Kind/targetName
	nodeGroups       *NodeGroupState
	AuditLog         *AuditLog
	NodeLock         *NodeLock
	Breaker          *CircuitBreaker
	MetricsAvailable bool // true if Metrics Server returned data on last refresh
	NodesWithMetrics int  // count of nodes with actual metrics data
	PodsWithMetrics  int  // count of pods with actual metrics data
	metricsStore *intmetrics.Store // historical metrics for percentile queries
	// Metrics cache: persist last successful metrics data for 5 min
	lastNodeMetrics   map[string]*pkgmetrics.NodeMetrics
	lastPodMetrics    map[string]*pkgmetrics.PodMetrics
	lastMetricsUpdate time.Time
	// Suppress repeated warnings
	pricingWarned    map[string]bool
	metricsWarned    bool
	diskStatsWarned  bool
	// Kubernetes clientset for kubelet proxy calls (disk stats)
	kubeClientset *kubernetes.Clientset
}

// NewClusterState creates a new ClusterState. If db and writer are non-nil,
// the audit log is backed by SQLite for persistence across restarts.
// An optional direct reader can be passed to bypass the controller-runtime
// informer cache for pod/node listing (which can return incomplete results).
func NewClusterState(c client.Client, provider cloudprovider.CloudProvider, mc pkgmetrics.MetricsCollector, db *sql.DB, writer *store.Writer, metricsStore *intmetrics.Store, reader ...client.Reader) *ClusterState {
	var auditLog *AuditLog
	if db != nil && writer != nil {
		auditLog = NewAuditLogWithDB(1000, db, writer)
	} else {
		auditLog = NewAuditLog(1000)
	}
	var r client.Reader = c
	if len(reader) > 0 && reader[0] != nil {
		r = reader[0]
	}
	return &ClusterState{
		client:       c,
		reader:       r,
		provider:     provider,
		metrics:      mc,
		metricsStore: metricsStore,
		nodes:        make(map[string]*NodeState),
		pods:         make(map[string]*PodState),
		autoscalers:  make(map[string]*AutoscalerInfo),
		vpas:         make(map[string]*VPAInfo),
		nodeGroups:   NewNodeGroupState(),
		AuditLog:     auditLog,
		NodeLock:     NewNodeLock(),
		Breaker:      NewCircuitBreaker(0.5, 5*time.Minute),
	}
}

// SetRESTConfig sets the Kubernetes REST config for kubelet proxy calls (disk stats).
func (s *ClusterState) SetRESTConfig(cfg *rest.Config) {
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		slog.Warn("failed to create kubernetes clientset for disk stats", "error", err)
		return
	}
	s.kubeClientset = cs
}

// kubeletStatsSummary is a minimal struct for parsing kubelet /stats/summary.
type kubeletStatsSummary struct {
	Node struct {
		Fs *struct {
			CapacityBytes  int64 `json:"capacityBytes"`
			UsedBytes      int64 `json:"usedBytes"`
			AvailableBytes int64 `json:"availableBytes"`
		} `json:"fs"`
	} `json:"node"`
	Pods []kubeletPodStats `json:"pods"`
}

type kubeletPodStats struct {
	PodRef struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"podRef"`
	EphemeralStorage *struct {
		UsedBytes int64 `json:"usedBytes"`
	} `json:"ephemeral-storage"`
	Network *kubeletNetworkStats `json:"network"`
}

type kubeletNetworkStats struct {
	Interfaces []kubeletInterfaceStats `json:"interfaces"`
}

type kubeletInterfaceStats struct {
	Name    string `json:"name"`
	RxBytes int64  `json:"rxBytes"`
	TxBytes int64  `json:"txBytes"`
}

// PodNetworkStats holds cumulative network I/O bytes for a pod.
type PodNetworkStats struct {
	RxBytes int64
	TxBytes int64
}

// fetchDiskStats fetches disk utilization and network I/O for all nodes via kubelet proxy.
// Returns node disk stats (nodeName -> [capacityBytes, usedBytes]),
// per-pod ephemeral storage usage (namespace/podName -> usedBytes),
// and per-pod network I/O (namespace/podName -> PodNetworkStats).
func (s *ClusterState) fetchDiskStats(ctx context.Context, nodeNames []string) (map[string][2]int64, map[string]int64, map[string]*PodNetworkStats) {
	if s.kubeClientset == nil {
		return nil, nil, nil
	}

	var mu sync.Mutex
	diskMap := make(map[string][2]int64, len(nodeNames))
	podDiskMap := make(map[string]int64)
	podNetMap := make(map[string]*PodNetworkStats)
	var firstErr error // capture first error for logging

	var wg sync.WaitGroup
	sem := make(chan struct{}, 10) // limit concurrency

	for _, name := range nodeNames {
		wg.Add(1)
		sem <- struct{}{} // acquire slot
		go func(nodeName string) {
			defer wg.Done()
			defer func() { <-sem }() // release slot

			data, err := s.kubeClientset.CoreV1().RESTClient().
				Get().
				Resource("nodes").
				Name(nodeName).
				SubResource("proxy", "stats", "summary").
				DoRaw(ctx)
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("node %s: %w", nodeName, err)
				}
				mu.Unlock()
				return
			}

			var summary kubeletStatsSummary
			if err := json.Unmarshal(data, &summary); err != nil {
				slog.Warn("failed to unmarshal disk stats", "node", nodeName, "error", err)
				return
			}
			mu.Lock()
			if summary.Node.Fs != nil {
				diskMap[nodeName] = [2]int64{summary.Node.Fs.CapacityBytes, summary.Node.Fs.UsedBytes}
			}
			for _, ps := range summary.Pods {
				key := ps.PodRef.Namespace + "/" + ps.PodRef.Name
				if ps.EphemeralStorage != nil {
					podDiskMap[key] = ps.EphemeralStorage.UsedBytes
				}
				if ps.Network != nil {
					var rxTotal, txTotal int64
					for _, iface := range ps.Network.Interfaces {
						rxTotal += iface.RxBytes
						txTotal += iface.TxBytes
					}
					if rxTotal > 0 || txTotal > 0 {
						podNetMap[key] = &PodNetworkStats{RxBytes: rxTotal, TxBytes: txTotal}
					}
				}
			}
			mu.Unlock()
		}(name)
	}

	wg.Wait()
	if firstErr != nil && len(diskMap) == 0 {
		slog.Warn("kubelet disk stats fetch failed", "error", firstErr, "nodes", len(nodeNames))
	}
	return diskMap, podDiskMap, podNetMap
}

// listAllNodes fetches all nodes using pagination to avoid OOM on large clusters.
func (s *ClusterState) listAllNodes(ctx context.Context) (*corev1.NodeList, error) {
	result := &corev1.NodeList{}
	opts := &client.ListOptions{Limit: 500}
	for {
		page := &corev1.NodeList{}
		if err := s.reader.List(ctx, page, opts); err != nil {
			return nil, err
		}
		result.Items = append(result.Items, page.Items...)
		if page.Continue == "" {
			break
		}
		opts.Continue = page.Continue
	}
	return result, nil
}

// listAllPods fetches all pods using pagination to avoid OOM on large clusters.
func (s *ClusterState) listAllPods(ctx context.Context) (*corev1.PodList, error) {
	result := &corev1.PodList{}
	opts := &client.ListOptions{Limit: 500}
	for {
		page := &corev1.PodList{}
		if err := s.reader.List(ctx, page, opts); err != nil {
			return nil, err
		}
		result.Items = append(result.Items, page.Items...)
		if page.Continue == "" {
			break
		}
		opts.Continue = page.Continue
	}
	return result, nil
}

// Refresh updates the cluster state cache from the Kubernetes API and cloud provider.
func (s *ClusterState) Refresh(ctx context.Context) error {
	// Fetch nodes (paginated)
	nodeList, err := s.listAllNodes(ctx)
	if err != nil {
		return fmt.Errorf("listing nodes: %w", err)
	}

	// Fetch pods (paginated)
	podList, err := s.listAllPods(ctx)
	if err != nil {
		return fmt.Errorf("listing pods: %w", err)
	}

	// Discover node groups
	groups, err := s.provider.DiscoverNodeGroups(ctx)
	if err != nil {
		return fmt.Errorf("discovering node groups: %w", err)
	}

	// Get node metrics (single attempt — retrying is pointless when metrics-server is absent).
	var nodeMetrics []pkgmetrics.NodeMetrics
	if nm, err := s.metrics.GetNodeMetrics(ctx); err == nil && len(nm) > 0 {
		nodeMetrics = nm
		s.metricsWarned = false // reset so we warn again if it breaks later
	} else if err != nil && !s.metricsWarned {
		slog.Warn("node metrics unavailable, continuing without live metrics", "error", err)
		s.metricsWarned = true
	}
	metricsMap := make(map[string]*pkgmetrics.NodeMetrics, len(nodeMetrics))
	if len(nodeMetrics) > 0 {
		for i := range nodeMetrics {
			metricsMap[nodeMetrics[i].Name] = &nodeMetrics[i]
		}
	} else if s.lastNodeMetrics != nil && time.Since(s.lastMetricsUpdate) < 5*time.Minute {
		// Use cached metrics from last successful fetch
		slog.Info("using cached node metrics", "age", time.Since(s.lastMetricsUpdate).Round(time.Second))
		metricsMap = s.lastNodeMetrics
	}

	// Build pod-to-node mapping — include ALL scheduled pods regardless of phase
	// so that unhealthy pods (ImagePullBackOff, CrashLoopBackOff, etc.) appear
	// on their node's detail page.
	podsByNode := make(map[string][]*corev1.Pod)
	for i := range podList.Items {
		pod := &podList.Items[i]
		if pod.Spec.NodeName != "" {
			podsByNode[pod.Spec.NodeName] = append(podsByNode[pod.Spec.NodeName], pod)
		}
	}

	// Build node group lookup
	nodeGroupByNode := buildNodeGroupMapping(nodeList, groups)

	// Pre-fetch pricing for all unique node regions to support multi-region clusters.
	// Collect unique regions first, then fetch pricing for each.
	pricingByRegion := make(map[string]map[string]float64)
	var defaultRegion string
	regionSet := make(map[string]bool)
	for i := range nodeList.Items {
		if region, err := s.provider.GetNodeRegion(ctx, &nodeList.Items[i]); err == nil {
			if defaultRegion == "" {
				defaultRegion = region
			}
			regionSet[region] = true
		}
	}
	for region := range regionSet {
		if pi, err := s.provider.GetCurrentPricing(ctx, region); err == nil {
			pricingByRegion[region] = pi.Prices
		} else if !s.pricingWarned[region] {
			slog.Warn("pricing API unavailable for region, will use capacity-based fallback",
				"region", region, "error", err)
			if s.pricingWarned == nil {
				s.pricingWarned = make(map[string]bool)
			}
			s.pricingWarned[region] = true
		}
	}
	// Default pricingMap for backward compatibility.
	// Guard against nil: defaultRegion may be empty or missing from the map
	// (e.g. no nodes have region labels, or pricing fetch failed for all regions).
	pricingMap := pricingByRegion[defaultRegion]
	if pricingMap == nil {
		pricingMap = make(map[string]float64)
	}

	// Get pod metrics BEFORE acquiring the lock to avoid blocking readers.
	var podMetrics []pkgmetrics.PodMetrics
	if pm, err := s.metrics.GetPodMetrics(ctx, ""); err == nil && len(pm) > 0 {
		podMetrics = pm
	} else if err != nil && !s.metricsWarned {
		slog.Warn("pod metrics unavailable, continuing without live metrics", "error", err)
	}
	podMetricsMap := make(map[string]*pkgmetrics.PodMetrics, len(podMetrics))
	if len(podMetrics) > 0 {
		for i := range podMetrics {
			key := podMetrics[i].Namespace + "/" + podMetrics[i].Name
			podMetricsMap[key] = &podMetrics[i]
		}
	} else if s.lastPodMetrics != nil && time.Since(s.lastMetricsUpdate) < 5*time.Minute {
		slog.Info("using cached pod metrics", "age", time.Since(s.lastMetricsUpdate).Round(time.Second))
		podMetricsMap = s.lastPodMetrics
	}

	// Record raw metrics to historical store for percentile analysis.
	// Runs outside mu.Lock — metricsStore has its own mutex + async writer.
	if s.metricsStore != nil {
		for i := range nodeMetrics {
			s.metricsStore.RecordNodeMetrics(nodeMetrics[i])
		}
		for i := range podMetrics {
			s.metricsStore.RecordPodMetrics(podMetrics[i])
		}
	}

	// Fetch disk utilization and network I/O from kubelet stats summary (parallel, best-effort).
	var diskStatsMap map[string][2]int64
	var podDiskMap map[string]int64
	var podNetMap map[string]*PodNetworkStats
	if s.kubeClientset != nil {
		nodeNames := make([]string, len(nodeList.Items))
		for i := range nodeList.Items {
			nodeNames[i] = nodeList.Items[i].Name
		}
		diskCtx, diskCancel := context.WithTimeout(ctx, 15*time.Second)
		diskStatsMap, podDiskMap, podNetMap = s.fetchDiskStats(diskCtx, nodeNames)
		diskCancel()
		if len(diskStatsMap) == 0 && !s.diskStatsWarned {
			slog.Warn("kubelet disk stats unavailable, disk utilization will use capacity only")
			s.diskStatsWarned = true
		} else if len(diskStatsMap) > 0 {
			s.diskStatsWarned = false
		}
	}

	// Discover autoscalers (HPAs + KEDA ScaledObjects) for workload metadata.
	autoscalerMap := s.fetchAutoscalers(ctx)

	// Discover VPAs for workload metadata.
	vpaMap := s.fetchVPAs(ctx)

	// Detect if metrics server is available.
	metricsAvailable := len(metricsMap) > 0
	if !metricsAvailable && len(nodeList.Items) > 0 {
		slog.Warn("metrics server unavailable, node utilization data will be zero")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.MetricsAvailable = metricsAvailable
	s.NodesWithMetrics = len(metricsMap)
	s.autoscalers = autoscalerMap
	s.vpas = vpaMap
	// Cache successful metrics data for reuse across sync cycles.
	// Replace (not merge) to prevent unbounded growth from deleted nodes.
	if len(metricsMap) > 0 {
		s.lastNodeMetrics = metricsMap
		s.lastMetricsUpdate = time.Now()
	}

	// Update nodes
	newNodes := make(map[string]*NodeState, len(nodeList.Items))
	for i := range nodeList.Items {
		node := &nodeList.Items[i]
		cpuCap, memCap, gpuCap, diskCap := ExtractNodeCapacity(node)

		instanceType, _ := s.provider.GetNodeInstanceType(ctx, node)
		family, _ := familylock.ExtractFamily(instanceType)

		// Fallback GPU detection: if the NVIDIA device plugin isn't reporting
		// nvidia.com/gpu on nodes that are known GPU hardware (e.g., GCP g2-standard-8),
		// use the cloud provider's instance type knowledge.
		if gpuCap == 0 && instanceType != "" {
			if detector, ok := s.provider.(cloudprovider.GPUInstanceDetector); ok {
				if gpuCount, _ := detector.DetectGPUByInstanceType(instanceType); gpuCount > 0 {
					gpuCap = gpuCount
				}
			}
		}

		ns := &NodeState{
			Node:           node,
			Pods:           podsByNode[node.Name],
			InstanceType:   instanceType,
			InstanceFamily: family,
			CPUCapacity:    cpuCap,
			MemoryCapacity: memCap,
			GPUCapacity:    gpuCap,
			DiskCapacity:   diskCap,
			IsGPUNode:      gpuCap > 0,
		}

		// Set node group info
		if ngID, ok := nodeGroupByNode[node.Name]; ok {
			ns.NodeGroupID = ngID
		}

		// Calculate requests and GPU allocation from pods.
		// Only count Running pods and viable Pending pods — skip completed/failed
		// Job pods and Pending pods stuck in terminal states (ImagePullBackOff,
		// ErrImagePull, etc.) that will never actually run. Counting stuck pods
		// inflates CPURequested to ~100% on nodes that are actually nearly idle,
		// preventing the downscaler from ever selecting them.
		gpuResource := corev1.ResourceName("nvidia.com/gpu")
		for _, pod := range ns.Pods {
			if pod.Status.Phase == corev1.PodRunning || (pod.Status.Phase == corev1.PodPending && !IsPodStuckPending(pod)) {
				cpuReq, memReq := ExtractPodRequests(pod)
				ns.CPURequested += cpuReq
				ns.MemoryRequested += memReq
			}
			for _, c := range pod.Spec.Containers {
				if gpu, ok := c.Resources.Requests[gpuResource]; ok {
					ns.GPUsUsed += int(gpu.Value())
				}
			}
		}

		// Apply metrics (actual usage from Metrics Server).
		// Never fall back to requests — that conflates utilization with allocation.
		// If metrics are unavailable, usage stays at 0.
		if m, ok := metricsMap[node.Name]; ok {
			ns.CPUUsed = m.CPUUsage
			ns.MemoryUsed = m.MemoryUsage
		}

		// Apply disk stats from kubelet (best-effort).
		if ds, ok := diskStatsMap[node.Name]; ok {
			if ds[0] > 0 {
				ns.DiskCapacity = ds[0]
			}
			ns.DiskUsed = ds[1]
		}

		// Compute cost from pre-fetched pricing (avoids per-node API call).
		// Use per-region pricing when available for multi-region cluster support.
		nodePricingMap := pricingMap
		if nodeRegion, err := s.provider.GetNodeRegion(ctx, node); err == nil {
			if rp, ok := pricingByRegion[nodeRegion]; ok {
				nodePricingMap = rp
			}
		}
		if nodePricingMap != nil && instanceType != "" {
			if price, ok := nodePricingMap[instanceType]; ok {
				ns.IsSpot = cloudprovider.IsSpotNode(node)
				if ns.IsSpot {
					// Use per-provider, per-family spot discount estimates instead
					// of a flat multiplier. Each provider implements
					// SpotDiscountEstimator with instance-family-specific rates.
					if sde, ok := s.provider.(cloudprovider.SpotDiscountEstimator); ok {
						discount := sde.EstimateSpotDiscount(instanceType)
						ns.HourlyCostUSD = price * (1 - discount)
					} else {
						ns.HourlyCostUSD = price * 0.35 // fallback: ~65% spot discount
					}
				} else {
					ns.HourlyCostUSD = price
				}
			}
		}

		// Fallback: estimate price from node capacity when pricing API is unavailable
		// or the specific instance type is missing from the pricing map.
		if ns.HourlyCostUSD == 0 && instanceType != "" && cpuCap > 0 {
			if fp, ok := s.provider.(cloudprovider.FallbackPricer); ok {
				region := defaultRegion
				if r, err := s.provider.GetNodeRegion(ctx, node); err == nil {
					region = r
				}
				price := fp.EstimatePriceFromCapacity(instanceType, region, cpuCap, memCap)
				if price > 0 {
					ns.IsSpot = cloudprovider.IsSpotNode(node)
					if ns.IsSpot {
						if sde, ok := s.provider.(cloudprovider.SpotDiscountEstimator); ok {
							discount := sde.EstimateSpotDiscount(instanceType)
							ns.HourlyCostUSD = price * (1 - discount)
						} else {
							ns.HourlyCostUSD = price * 0.35
						}
					} else {
						ns.HourlyCostUSD = price
					}
				}
			}
		}

		newNodes[node.Name] = ns
	}
	s.nodes = newNodes

	// Update pods
	newPods := make(map[string]*PodState, len(podList.Items))
	for i := range podList.Items {
		pod := &podList.Items[i]
		key := pod.Namespace + "/" + pod.Name
		newPods[key] = NewPodState(pod)
	}

	// Apply pod metrics (actual usage from Metrics Server, fetched above without lock).
	// Replace (not merge) to prevent unbounded growth from deleted pods.
	if len(podMetricsMap) > 0 {
		s.lastPodMetrics = podMetricsMap
	}
	podsWithMetrics := 0
	for key, ps := range newPods {
		if pm, ok := podMetricsMap[key]; ok {
			for _, cm := range pm.Containers {
				ps.CPUUsage += cm.CPUUsage
				ps.MemoryUsage += cm.MemoryUsage
			}
			podsWithMetrics++
		}
		// No fallback: if metrics are unavailable for this pod, leave
		// CPUUsage/MemoryUsage at 0 so efficiency calculations show
		// accurate data instead of a misleading 100%.
	}
	// Apply per-pod disk usage from kubelet ephemeral-storage stats.
	for key, ps := range newPods {
		if usage, ok := podDiskMap[key]; ok {
			ps.DiskUsage = usage
		}
	}
	// Apply per-pod network I/O from kubelet stats summary.
	for key, ps := range newPods {
		if ns, ok := podNetMap[key]; ok {
			ps.NetworkRxBytes = ns.RxBytes
			ps.NetworkTxBytes = ns.TxBytes
		}
	}

	s.pods = newPods
	s.PodsWithMetrics = podsWithMetrics

	// Update node groups
	nodeStates := make([]*NodeState, 0, len(s.nodes))
	for _, n := range s.nodes {
		nodeStates = append(nodeStates, n)
	}
	s.nodeGroups.Update(groups, nodeStates)

	// Prune node locks for nodes that no longer exist in the cluster.
	if s.NodeLock != nil {
		currentNodeNames := make(map[string]bool, len(s.nodes))
		for name := range s.nodes {
			currentNodeNames[name] = true
		}
		s.NodeLock.PruneDeleted(currentNodeNames)
	}

	return nil
}

// GetNode returns a node by name.
func (s *ClusterState) GetNode(name string) (*NodeState, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n, ok := s.nodes[name]
	return n, ok
}

// GetAllNodes returns all nodes.
func (s *ClusterState) GetAllNodes() []*NodeState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]*NodeState, 0, len(s.nodes))
	for _, n := range s.nodes {
		result = append(result, n)
	}
	return result
}

// GetPod returns a pod by namespace/name.
func (s *ClusterState) GetPod(namespace, name string) (*PodState, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.pods[namespace+"/"+name]
	return p, ok
}

// RemovePod removes a pod from the in-memory state (e.g. after deletion).
func (s *ClusterState) RemovePod(namespace, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.pods, namespace+"/"+name)
}

// GetAllPods returns all pods.
func (s *ClusterState) GetAllPods() []*PodState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]*PodState, 0, len(s.pods))
	for _, p := range s.pods {
		result = append(result, p)
	}
	return result
}

// GetNodeGroups returns the node group state.
func (s *ClusterState) GetNodeGroups() *NodeGroupState {
	return s.nodeGroups
}

// GetAutoscaler returns autoscaler info for a workload identified by namespace/kind/name.
func (s *ClusterState) GetAutoscaler(namespace, kind, name string) (*AutoscalerInfo, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.autoscalers[namespace+"/"+kind+"/"+name]
	return a, ok
}

// fetchAutoscalers discovers HPAs and KEDA ScaledObjects, returning a map
// keyed by namespace/Kind/targetName (matching the workload key format).
func (s *ClusterState) fetchAutoscalers(ctx context.Context) map[string]*AutoscalerInfo {
	result := make(map[string]*AutoscalerInfo)

	// Fetch HPAs
	hpaList := &autoscalingv2.HorizontalPodAutoscalerList{}
	if err := s.reader.List(ctx, hpaList); err != nil {
		slog.Warn("failed to list HPAs for autoscaler discovery", "error", err)
	} else {
		for i := range hpaList.Items {
			hpa := &hpaList.Items[i]
			ref := hpa.Spec.ScaleTargetRef
			minReplicas := int32(1)
			if hpa.Spec.MinReplicas != nil {
				minReplicas = *hpa.Spec.MinReplicas
			}
			key := hpa.Namespace + "/" + ref.Kind + "/" + ref.Name
			result[key] = &AutoscalerInfo{
				Kind:        "HPA",
				Name:        hpa.Name,
				MinReplicas: minReplicas,
				MaxReplicas: hpa.Spec.MaxReplicas,
			}
		}
	}

	// Fetch KEDA ScaledObjects (best-effort — fails silently if CRDs not installed)
	soList := &unstructured.UnstructuredList{}
	soList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "keda.sh",
		Version: "v1alpha1",
		Kind:    "ScaledObjectList",
	})
	if err := s.reader.List(ctx, soList); err == nil {
		for _, item := range soList.Items {
			ns := item.GetNamespace()
			spec, ok := item.Object["spec"].(map[string]interface{})
			if !ok {
				continue
			}
			ref, ok := spec["scaleTargetRef"].(map[string]interface{})
			if !ok {
				continue
			}
			targetKind, _ := ref["kind"].(string)
			targetName, _ := ref["name"].(string)
			if targetKind == "" || targetName == "" {
				continue
			}
			minReplicas := int32(0)
			if v, ok := spec["minReplicaCount"]; ok {
				minReplicas = toInt32(v)
			}
			maxReplicas := int32(0)
			if v, ok := spec["maxReplicaCount"]; ok {
				maxReplicas = toInt32(v)
			}
			key := ns + "/" + targetKind + "/" + targetName
			result[key] = &AutoscalerInfo{
				Kind:        "ScaledObject",
				Name:        item.GetName(),
				MinReplicas: minReplicas,
				MaxReplicas: maxReplicas,
			}
		}
	}

	return result
}

// GetVPA returns VPA info for a workload identified by namespace/kind/name.
func (s *ClusterState) GetVPA(namespace, kind, name string) (*VPAInfo, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.vpas[namespace+"/"+kind+"/"+name]
	return v, ok
}

// fetchVPAs discovers VerticalPodAutoscaler objects, returning a map
// keyed by namespace/Kind/targetName (matching the workload key format).
// Fails silently if VPA CRDs are not installed.
func (s *ClusterState) fetchVPAs(ctx context.Context) map[string]*VPAInfo {
	result := make(map[string]*VPAInfo)

	vpaList := &unstructured.UnstructuredList{}
	vpaList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "autoscaling.k8s.io",
		Version: "v1",
		Kind:    "VerticalPodAutoscalerList",
	})
	if err := s.reader.List(ctx, vpaList); err != nil {
		return result
	}

	for _, item := range vpaList.Items {
		ns := item.GetNamespace()
		spec, ok := item.Object["spec"].(map[string]interface{})
		if !ok {
			continue
		}
		ref, ok := spec["targetRef"].(map[string]interface{})
		if !ok {
			continue
		}
		targetKind, _ := ref["kind"].(string)
		targetName, _ := ref["name"].(string)
		if targetKind == "" || targetName == "" {
			continue
		}

		updateMode := "Auto" // default per VPA spec
		if up, ok := spec["updatePolicy"].(map[string]interface{}); ok {
			if mode, ok := up["updateMode"].(string); ok {
				updateMode = mode
			}
		}

		key := ns + "/" + targetKind + "/" + targetName
		result[key] = &VPAInfo{
			Name:       item.GetName(),
			UpdateMode: updateMode,
		}
	}

	return result
}

// toInt32 converts an unstructured value (int64 or float64) to int32.
func toInt32(v interface{}) int32 {
	switch n := v.(type) {
	case int64:
		return int32(n)
	case float64:
		return int32(n)
	}
	return 0
}

// buildNodeGroupMapping maps node names to their node group IDs.
func buildNodeGroupMapping(nodes *corev1.NodeList, groups []*cloudprovider.NodeGroup) map[string]string {
	result := make(map[string]string)

	// Pass 1: Match using authoritative cloud-specific labels (never overwrite).
	for i := range nodes.Items {
		node := &nodes.Items[i]
		if node.Labels == nil {
			continue
		}
		poolLabel := ""
		if v, ok := node.Labels["cloud.google.com/gke-nodepool"]; ok {
			poolLabel = v
		} else if v, ok := node.Labels["eks.amazonaws.com/nodegroup"]; ok {
			poolLabel = v
		} else if v, ok := node.Labels["alpha.eksctl.io/nodegroup-name"]; ok {
			poolLabel = v
		} else if v, ok := node.Labels["karpenter.sh/nodepool"]; ok {
			poolLabel = v
		} else if v, ok := node.Labels["kubernetes.azure.com/agentpool"]; ok {
			poolLabel = v
		}
		if poolLabel != "" {
			for _, ng := range groups {
				if ng.Name == poolLabel {
					result[node.Name] = ng.ID
					break
				}
			}
		}
	}

	// Pass 2: Fallback — match unmatched nodes by instance ID from providerID.
	// providerID format: aws:///az/i-xxx, gce:///project/zone/name, azure:///subscriptions/...
	instanceIDToGroup := make(map[string]string)
	for _, ng := range groups {
		for _, id := range ng.InstanceIDs {
			instanceIDToGroup[id] = ng.ID
		}
	}
	if len(instanceIDToGroup) > 0 {
		for i := range nodes.Items {
			node := &nodes.Items[i]
			if _, matched := result[node.Name]; matched {
				continue
			}
			instanceID := extractInstanceID(node.Spec.ProviderID)
			if instanceID != "" {
				if ngID, ok := instanceIDToGroup[instanceID]; ok {
					result[node.Name] = ngID
				}
			}
		}
	}

	return result
}

// extractInstanceID extracts the cloud instance ID from a node's providerID.
// AWS format: aws:///us-east-1a/i-0123456789abcdef0
func extractInstanceID(providerID string) string {
	if providerID == "" {
		return ""
	}
	// Find the last "/" and take everything after it
	lastSlash := len(providerID) - 1
	for lastSlash >= 0 && providerID[lastSlash] != '/' {
		lastSlash--
	}
	if lastSlash >= 0 && lastSlash < len(providerID)-1 {
		return providerID[lastSlash+1:]
	}
	return ""
}

package state

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sync"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/koptimizer/koptimizer/internal/store"
	"github.com/koptimizer/koptimizer/pkg/cloudprovider"
	"github.com/koptimizer/koptimizer/pkg/familylock"
	pkgmetrics "github.com/koptimizer/koptimizer/pkg/metrics"
)

// ClusterState maintains an in-memory cache of the cluster state.
type ClusterState struct {
	mu               sync.RWMutex
	client           client.Client
	provider         cloudprovider.CloudProvider
	metrics          pkgmetrics.MetricsCollector
	nodes            map[string]*NodeState
	pods             map[string]*PodState // key: namespace/name
	nodeGroups       *NodeGroupState
	AuditLog         *AuditLog
	NodeLock         *NodeLock
	MetricsAvailable bool // true if Metrics Server returned data on last refresh
	NodesWithMetrics int  // count of nodes with actual metrics data
	PodsWithMetrics  int  // count of pods with actual metrics data
}

// NewClusterState creates a new ClusterState. If db and writer are non-nil,
// the audit log is backed by SQLite for persistence across restarts.
func NewClusterState(c client.Client, provider cloudprovider.CloudProvider, mc pkgmetrics.MetricsCollector, db *sql.DB, writer *store.Writer) *ClusterState {
	var auditLog *AuditLog
	if db != nil && writer != nil {
		auditLog = NewAuditLogWithDB(1000, db, writer)
	} else {
		auditLog = NewAuditLog(1000)
	}
	return &ClusterState{
		client:     c,
		provider:   provider,
		metrics:    mc,
		nodes:      make(map[string]*NodeState),
		pods:       make(map[string]*PodState),
		nodeGroups: NewNodeGroupState(),
		AuditLog:   auditLog,
		NodeLock:   NewNodeLock(),
	}
}

// listAllNodes fetches all nodes using pagination to avoid OOM on large clusters.
func (s *ClusterState) listAllNodes(ctx context.Context) (*corev1.NodeList, error) {
	result := &corev1.NodeList{}
	opts := &client.ListOptions{Limit: 500}
	for {
		page := &corev1.NodeList{}
		if err := s.client.List(ctx, page, opts); err != nil {
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
		if err := s.client.List(ctx, page, opts); err != nil {
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

	// Get node metrics
	nodeMetrics, _ := s.metrics.GetNodeMetrics(ctx) // Best effort
	metricsMap := make(map[string]*pkgmetrics.NodeMetrics, len(nodeMetrics))
	for i := range nodeMetrics {
		metricsMap[nodeMetrics[i].Name] = &nodeMetrics[i]
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

	// Pre-fetch pricing once to avoid N+1 GetNodeCost calls per node.
	// GetNodeRegion returns the provider's default region for nodes without
	// a region label, so a single call covers the common case.
	var pricingMap map[string]float64
	var defaultRegion string
	if len(nodeList.Items) > 0 {
		if region, err := s.provider.GetNodeRegion(ctx, &nodeList.Items[0]); err == nil {
			defaultRegion = region
			if pi, err := s.provider.GetCurrentPricing(ctx, region); err == nil {
				pricingMap = pi.Prices
			} else {
				slog.Warn("pricing API unavailable, will use capacity-based fallback",
					"region", region, "error", err)
			}
		}
	}

	// Get pod metrics BEFORE acquiring the lock to avoid blocking readers
	// during this network call.
	podMetrics, _ := s.metrics.GetPodMetrics(ctx, "") // best effort, all namespaces
	podMetricsMap := make(map[string]*pkgmetrics.PodMetrics, len(podMetrics))
	for i := range podMetrics {
		key := podMetrics[i].Namespace + "/" + podMetrics[i].Name
		podMetricsMap[key] = &podMetrics[i]
	}

	// Detect if metrics server is available.
	metricsAvailable := len(metricsMap) > 0
	if !metricsAvailable && len(nodeList.Items) > 0 {
		slog.Warn("metrics server unavailable, using resource requests as approximate utilization")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.MetricsAvailable = metricsAvailable
	s.NodesWithMetrics = len(metricsMap)

	// Update nodes
	newNodes := make(map[string]*NodeState, len(nodeList.Items))
	for i := range nodeList.Items {
		node := &nodeList.Items[i]
		cpuCap, memCap, gpuCap := ExtractNodeCapacity(node)

		instanceType, _ := s.provider.GetNodeInstanceType(ctx, node)
		family, _ := familylock.ExtractFamily(instanceType)

		ns := &NodeState{
			Node:           node,
			Pods:           podsByNode[node.Name],
			InstanceType:   instanceType,
			InstanceFamily: family,
			CPUCapacity:    cpuCap,
			MemoryCapacity: memCap,
			GPUCapacity:    gpuCap,
			IsGPUNode:      gpuCap > 0,
		}

		// Set node group info
		if ngID, ok := nodeGroupByNode[node.Name]; ok {
			ns.NodeGroupID = ngID
		}

		// Calculate requests and GPU allocation from pods.
		// Only count Running pods for resource accounting — completed/failed
		// Job pods still have spec.nodeName set and would inflate CPURequested.
		gpuResource := corev1.ResourceName("nvidia.com/gpu")
		for _, pod := range ns.Pods {
			if pod.Status.Phase == corev1.PodRunning || pod.Status.Phase == corev1.PodPending {
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

		// Compute cost from pre-fetched pricing (avoids per-node API call).
		if pricingMap != nil && instanceType != "" {
			if price, ok := pricingMap[instanceType]; ok {
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
	s.pods = newPods
	s.PodsWithMetrics = podsWithMetrics

	// Update node groups
	nodeStates := make([]*NodeState, 0, len(s.nodes))
	for _, n := range s.nodes {
		nodeStates = append(nodeStates, n)
	}
	s.nodeGroups.Update(groups, nodeStates)

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

// buildNodeGroupMapping maps node names to their node group IDs.
func buildNodeGroupMapping(nodes *corev1.NodeList, groups []*cloudprovider.NodeGroup) map[string]string {
	result := make(map[string]string)
	// Node groups are matched by labels
	for _, ng := range groups {
		for i := range nodes.Items {
			node := &nodes.Items[i]
			if matchesNodeGroup(node, ng) {
				result[node.Name] = ng.ID
			}
		}
	}
	return result
}

// matchesNodeGroup checks if a node belongs to a node group using multiple strategies.
func matchesNodeGroup(node *corev1.Node, ng *cloudprovider.NodeGroup) bool {
	if node.Labels == nil {
		return false
	}

	// Strategy 1: Cloud-specific node group labels (most reliable).
	// AWS EKS
	if v, ok := node.Labels["eks.amazonaws.com/nodegroup"]; ok && v == ng.Name {
		return true
	}
	// GCP GKE
	if v, ok := node.Labels["cloud.google.com/gke-nodepool"]; ok && v == ng.Name {
		return true
	}
	// Azure AKS
	if v, ok := node.Labels["kubernetes.azure.com/agentpool"]; ok && v == ng.Name {
		return true
	}

	// Strategy 2: Match by node group labels if present.
	if len(ng.Labels) > 0 {
		allMatch := true
		for k, v := range ng.Labels {
			if nodeVal, ok := node.Labels[k]; !ok || nodeVal != v {
				allMatch = false
				break
			}
		}
		if allMatch {
			return true
		}
	}

	return false
}

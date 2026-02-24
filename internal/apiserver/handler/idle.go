package handler

import (
	"context"
	"fmt"
	"net/http"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/koptimizer/koptimizer/internal/config"
	"github.com/koptimizer/koptimizer/internal/state"
	"github.com/koptimizer/koptimizer/pkg/cost"
)

// IdleResourceHandler handles idle resource detection.
type IdleResourceHandler struct {
	state  *state.ClusterState
	client client.Client
	cfg    *config.Config
}

// NewIdleResourceHandler creates a new IdleResourceHandler.
func NewIdleResourceHandler(st *state.ClusterState, c client.Client, cfg *config.Config) *IdleResourceHandler {
	return &IdleResourceHandler{state: st, client: c, cfg: cfg}
}

// Get returns idle nodes, idle workloads, and orphaned PVCs.
func (h *IdleResourceHandler) Get(w http.ResponseWriter, r *http.Request) {
	nodes := h.state.GetAllNodes()
	pods := h.state.GetAllPods()

	// Use evictor threshold as idle threshold, falling back to 30%
	idleThreshold := h.cfg.Evictor.UtilizationThreshold
	if idleThreshold == 0 {
		idleThreshold = 30.0
	}

	// Idle nodes: both CPU and memory below threshold
	type idleNode struct {
		Name           string  `json:"name"`
		InstanceType   string  `json:"instanceType"`
		CPUUtilPct     float64 `json:"cpuUtilPct"`
		MemUtilPct     float64 `json:"memUtilPct"`
		IdleSinceHrs   int     `json:"idleSinceHrs"`
		HourlyCostUSD  float64 `json:"hourlyCostUSD"`
		WastedCostUSD  float64 `json:"wastedCostUSD"`
		Reason         string  `json:"reason"`
	}
	var idleNodes []idleNode
	totalIdleNodeCost := 0.0

	for _, n := range nodes {
		if n.IsUnderutilized(idleThreshold) {
			monthlyCost := n.HourlyCostUSD * cost.HoursPerMonth
			idleNodes = append(idleNodes, idleNode{
				Name:          n.Node.Name,
				InstanceType:  n.InstanceType,
				CPUUtilPct:    n.CPUUtilization(),
				MemUtilPct:    n.MemoryUtilization(),
				IdleSinceHrs:  0, // no historical tracking yet
				HourlyCostUSD: n.HourlyCostUSD,
				WastedCostUSD: monthlyCost,
				Reason:        fmt.Sprintf("CPU and memory below %.0f%%", idleThreshold),
			})
			totalIdleNodeCost += monthlyCost
		}
	}

	// Build node cost map for cost allocation
	nodeCostMap := make(map[string]float64)
	nodeCPUReqMap := make(map[string]int64)
	for _, n := range nodes {
		nodeCostMap[n.Node.Name] = n.HourlyCostUSD * cost.HoursPerMonth
		nodeCPUReqMap[n.Node.Name] = n.CPURequested
	}

	// Idle workloads: group pods by owner, flag where both CPU and mem efficiency < 0.3
	type workloadAgg struct {
		Namespace   string
		Kind        string
		Name        string
		Replicas    int
		TotalCPUReq int64
		TotalCPUUse int64
		TotalMemReq int64
		TotalMemUse int64
		MonthlyCost float64
	}
	workloads := make(map[string]*workloadAgg)

	for _, p := range pods {
		ownerKind, ownerName := p.OwnerKind, p.OwnerName
		if ownerName == "" {
			ownerName = p.Name
			ownerKind = "Pod"
		}
		key := p.Namespace + "/" + ownerKind + "/" + ownerName
		wl, ok := workloads[key]
		if !ok {
			wl = &workloadAgg{
				Namespace: p.Namespace,
				Kind:      ownerKind,
				Name:      ownerName,
			}
			workloads[key] = wl
		}
		wl.Replicas++
		wl.TotalCPUReq += p.CPURequest
		wl.TotalCPUUse += p.CPUUsage
		wl.TotalMemReq += p.MemoryRequest
		wl.TotalMemUse += p.MemoryUsage

		nodeTotalReq := nodeCPUReqMap[p.NodeName]
		if nodeTotalReq > 0 && p.CPURequest > 0 {
			fraction := float64(p.CPURequest) / float64(nodeTotalReq)
			wl.MonthlyCost += nodeCostMap[p.NodeName] * fraction
		}
	}

	type idleWorkload struct {
		Namespace    string  `json:"namespace"`
		Kind         string  `json:"kind"`
		Name         string  `json:"name"`
		Replicas     int     `json:"replicas"`
		CPUUsedPct   float64 `json:"cpuUsedPct"`
		MemUsedPct   float64 `json:"memUsedPct"`
		IdleSinceHrs int     `json:"idleSinceHrs"`
		WastedCostUSD float64 `json:"wastedCostUSD"`
		Reason       string  `json:"reason"`
	}
	var idleWorkloads []idleWorkload
	totalIdleWorkloadCost := 0.0

	idleRatio := idleThreshold / 100.0
	for _, wl := range workloads {
		cpuEff := 0.0
		if wl.TotalCPUReq > 0 {
			cpuEff = float64(wl.TotalCPUUse) / float64(wl.TotalCPUReq)
		}
		memEff := 0.0
		if wl.TotalMemReq > 0 {
			memEff = float64(wl.TotalMemUse) / float64(wl.TotalMemReq)
		}
		if cpuEff < idleRatio && memEff < idleRatio && (wl.TotalCPUReq > 0 || wl.TotalMemReq > 0) {
			idleWorkloads = append(idleWorkloads, idleWorkload{
				Namespace:     wl.Namespace,
				Kind:          wl.Kind,
				Name:          wl.Name,
				Replicas:      wl.Replicas,
				CPUUsedPct:    cpuEff * 100,
				MemUsedPct:    memEff * 100,
				IdleSinceHrs:  0,
				WastedCostUSD: wl.MonthlyCost,
				Reason:        fmt.Sprintf("CPU and memory usage below %.0f%%", idleThreshold),
			})
			totalIdleWorkloadCost += wl.MonthlyCost
		}
	}

	// Orphaned PVCs: bound PVCs not mounted by any pod
	type orphanedPVC struct {
		Name           string  `json:"name"`
		Namespace      string  `json:"namespace"`
		SizeGB         float64 `json:"sizeGB"`
		MountedBy      string  `json:"mountedBy"`
		MonthlyCostUSD float64 `json:"monthlyCostUSD"`
	}
	var orphanedPVCs []orphanedPVC

	storageCostPerGB := h.cfg.StorageMonitor.StorageCostPerGBUSD
	if storageCostPerGB == 0 {
		storageCostPerGB = 0.10
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	pvcList := &corev1.PersistentVolumeClaimList{}
	if err := h.client.List(ctx, pvcList); err == nil {
		podList := &corev1.PodList{}
		if err := h.client.List(ctx, podList); err == nil {
			mountedPVCs := make(map[string]bool)
			for i := range podList.Items {
				pod := &podList.Items[i]
				if pod.Status.Phase != corev1.PodRunning && pod.Status.Phase != corev1.PodPending {
					continue
				}
				for _, vol := range pod.Spec.Volumes {
					if vol.PersistentVolumeClaim != nil {
						key := fmt.Sprintf("%s/%s", pod.Namespace, vol.PersistentVolumeClaim.ClaimName)
						mountedPVCs[key] = true
					}
				}
			}

			for i := range pvcList.Items {
				pvc := &pvcList.Items[i]
				key := fmt.Sprintf("%s/%s", pvc.Namespace, pvc.Name)
				if pvc.Status.Phase == corev1.ClaimBound && !mountedPVCs[key] {
					var gb float64
					if req, ok := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; ok {
						gb = float64(req.Value()) / (1024 * 1024 * 1024)
					}
					orphanedPVCs = append(orphanedPVCs, orphanedPVC{
						Name:           pvc.Name,
						Namespace:      pvc.Namespace,
						SizeGB:         gb,
						MountedBy:      "",
						MonthlyCostUSD: gb * storageCostPerGB,
					})
				}
			}
		}
	}

	totalWastedCost := totalIdleNodeCost + totalIdleWorkloadCost

	// Compute average idle duration (0 when no historical tracking)
	totalIdleCount := len(idleNodes) + len(idleWorkloads)
	avgIdleDurationHrs := 0.0
	if totalIdleCount > 0 {
		totalHrs := 0
		for _, n := range idleNodes {
			totalHrs += n.IdleSinceHrs
		}
		for _, w := range idleWorkloads {
			totalHrs += w.IdleSinceHrs
		}
		avgIdleDurationHrs = float64(totalHrs) / float64(totalIdleCount)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"summary": map[string]interface{}{
			"totalIdleNodes":     len(idleNodes),
			"totalIdleWorkloads": len(idleWorkloads),
			"totalWastedCostUSD": totalWastedCost,
			"avgIdleDurationHrs": avgIdleDurationHrs,
		},
		"nodes":        idleNodes,
		"workloads":    idleWorkloads,
		"orphanedPVCs": orphanedPVCs,
	})
}

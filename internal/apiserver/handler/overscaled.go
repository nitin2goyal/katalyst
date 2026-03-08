package handler

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/koptimizer/koptimizer/internal/state"
	"github.com/koptimizer/koptimizer/pkg/cost"
)

// OverscaledHandler detects workloads that have been scaled up by HPAs
// but have very low actual CPU/memory utilization, wasting resources.
type OverscaledHandler struct {
	state  *state.ClusterState
	client client.Client
}

func NewOverscaledHandler(st *state.ClusterState, c client.Client) *OverscaledHandler {
	return &OverscaledHandler{state: st, client: c}
}

// hpaDiag holds diagnostic info from the HPA object.
type hpaDiag struct {
	scalingActive    string // "True", "False", "Unknown"
	scalingActiveMsg string
	ableToScale      string
	ableToScaleMsg   string
	scalingLimited   string
	scalingLimitedMsg string
	atMax            bool
	atMin            bool
	desiredReplicas  int32
	currentReplicas  int32
	currentMetrics   []string // human-readable metric status
	lastScaleTime    *time.Time
}

// kedaDiag holds diagnostic info from the KEDA ScaledObject.
type kedaDiag struct {
	ready      string
	readyMsg   string
	active     string
	activeMsg  string
	fallback   string
	fallbackMsg string
	paused     bool
}

// Get returns over-scaled workloads with root cause analysis.
func (h *OverscaledHandler) Get(w http.ResponseWriter, r *http.Request) {
	pods := h.state.GetAllPods()
	nodes := h.state.GetAllNodes()

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	// Build node cost map
	nodeCostMap := make(map[string]float64)
	nodeCPUCapMap := make(map[string]int64)
	nodeMemCapMap := make(map[string]int64)
	for _, n := range nodes {
		nodeCostMap[n.Node.Name] = n.HourlyCostUSD * cost.HoursPerMonth
		nodeCPUCapMap[n.Node.Name] = n.CPUCapacity
		nodeMemCapMap[n.Node.Name] = n.MemoryCapacity
	}

	// Fetch HPA diagnostics
	hpaDiags := h.fetchHPADiagnostics(ctx)

	// Fetch KEDA ScaledObject diagnostics
	kedaDiags := h.fetchKEDADiagnostics(ctx)

	// Fetch blocking PDBs for PDB-based root cause analysis
	blockingPDBs := h.fetchBlockingPDBsByWorkload(ctx)

	// Group pods by owner
	type wlInfo struct {
		Namespace   string
		Kind        string
		Name        string
		Replicas    int
		CPUReqTotal int64
		CPUUsed     int64
		MemReqTotal int64
		MemUsed     int64
		CPUReqPod   int64
		MemReqPod   int64
		MonthlyCost float64
		HasMetrics  bool
	}
	workloads := make(map[string]*wlInfo)

	for _, p := range pods {
		if p.Pod.Status.Phase != "Running" {
			continue
		}
		ownerKind, ownerName := resolveOwner(p)
		key := p.Namespace + "/" + ownerKind + "/" + ownerName
		wl, ok := workloads[key]
		if !ok {
			wl = &wlInfo{
				Namespace: p.Namespace,
				Kind:      ownerKind,
				Name:      ownerName,
			}
			workloads[key] = wl
		}
		wl.Replicas++
		wl.CPUReqTotal += p.CPURequest
		wl.CPUUsed += p.CPUUsage
		wl.MemReqTotal += p.MemoryRequest
		wl.MemUsed += p.MemoryUsage
		if p.CPUUsage > 0 || p.MemoryUsage > 0 {
			wl.HasMetrics = true
		}

		fraction := 0.0
		if cpuCap := nodeCPUCapMap[p.NodeName]; cpuCap > 0 && p.CPURequest > 0 {
			fraction += 0.5 * float64(p.CPURequest) / float64(cpuCap)
		}
		if memCap := nodeMemCapMap[p.NodeName]; memCap > 0 && p.MemoryRequest > 0 {
			fraction += 0.5 * float64(p.MemoryRequest) / float64(memCap)
		}
		if fraction > 0 {
			wl.MonthlyCost += nodeCostMap[p.NodeName] * fraction
		}
	}

	// Compute per-pod requests
	for _, wl := range workloads {
		if wl.Replicas > 0 {
			wl.CPUReqPod = wl.CPUReqTotal / int64(wl.Replicas)
			wl.MemReqPod = wl.MemReqTotal / int64(wl.Replicas)
		}
	}

	type overscaledEntry struct {
		Namespace       string   `json:"namespace"`
		Kind            string   `json:"kind"`
		Name            string   `json:"name"`
		CurrentReplicas int      `json:"currentReplicas"`
		MinReplicas     int32    `json:"minReplicas"`
		MaxReplicas     int32    `json:"maxReplicas"`
		OptimalReplicas int      `json:"optimalReplicas"`
		ExcessReplicas  int      `json:"excessReplicas"`
		Autoscaler      string   `json:"autoscaler"`
		AutoscalerName  string   `json:"autoscalerName"`
		CPUReqPerPod    string   `json:"cpuRequestPerPod"`
		MemReqPerPod    string   `json:"memRequestPerPod"`
		TotalCPUReq     string   `json:"totalCPURequest"`
		TotalCPUUsage   string   `json:"totalCPUUsage"`
		TotalMemReq     string   `json:"totalMemRequest"`
		TotalMemUsage   string   `json:"totalMemUsage"`
		CPUEffPct       float64  `json:"cpuEfficiencyPct"`
		MemEffPct       float64  `json:"memEfficiencyPct"`
		MonthlyCostUSD  float64  `json:"monthlyCostUSD"`
		WastedCostUSD   float64  `json:"wastedCostUSD"`
		Reason          string   `json:"reason"`
		RootCauses      []string `json:"rootCauses"`
		Severity        string   `json:"severity"`
	}

	var result []overscaledEntry
	totalExcess := 0
	totalWastedCost := 0.0

	for _, wl := range workloads {
		as, hasAS := h.state.GetAutoscaler(wl.Namespace, wl.Kind, wl.Name)
		if !hasAS || !wl.HasMetrics {
			continue
		}
		if wl.Replicas < 2 {
			continue
		}

		cpuEff := 0.0
		if wl.CPUReqTotal > 0 {
			cpuEff = float64(wl.CPUUsed) / float64(wl.CPUReqTotal) * 100
		}
		memEff := 0.0
		if wl.MemReqTotal > 0 {
			memEff = float64(wl.MemUsed) / float64(wl.MemReqTotal) * 100
		}

		targetUtil := 0.7
		optimalByCPU := 1
		if wl.CPUReqPod > 0 && wl.CPUUsed > 0 {
			optimalByCPU = int(math.Ceil(float64(wl.CPUUsed) / float64(wl.CPUReqPod) / targetUtil))
		}
		optimalByMem := 1
		if wl.MemReqPod > 0 && wl.MemUsed > 0 {
			optimalByMem = int(math.Ceil(float64(wl.MemUsed) / float64(wl.MemReqPod) / targetUtil))
		}
		optimal := optimalByCPU
		if optimalByMem > optimal {
			optimal = optimalByMem
		}
		if optimal < int(as.MinReplicas) {
			optimal = int(as.MinReplicas)
		}
		if optimal < 1 {
			optimal = 1
		}

		excess := wl.Replicas - optimal
		if excess <= 0 {
			continue
		}
		if float64(excess) < float64(wl.Replicas)*0.3 {
			continue
		}

		wastedCost := 0.0
		if wl.Replicas > 0 {
			wastedCost = wl.MonthlyCost * float64(excess) / float64(wl.Replicas)
		}

		severity := "info"
		if cpuEff < 5 {
			severity = "critical"
		} else if cpuEff < 20 {
			severity = "warning"
		}

		// --- Root cause analysis ---
		wlKey := wl.Namespace + "/" + wl.Kind + "/" + wl.Name
		rootCauses := deduplicateCauses(h.analyzeRootCauses(wl.Namespace, as, wl.Replicas, cpuEff,
			hpaDiags, kedaDiags, blockingPDBs[wlKey]))

		reason := fmt.Sprintf("%d replicas but %.1f%% CPU utilization — could run on %d replicas",
			wl.Replicas, cpuEff, optimal)

		result = append(result, overscaledEntry{
			Namespace:       wl.Namespace,
			Kind:            wl.Kind,
			Name:            wl.Name,
			CurrentReplicas: wl.Replicas,
			MinReplicas:     as.MinReplicas,
			MaxReplicas:     as.MaxReplicas,
			OptimalReplicas: optimal,
			ExcessReplicas:  excess,
			Autoscaler:      as.Kind,
			AutoscalerName:  as.Name,
			CPUReqPerPod:    formatCPU(wl.CPUReqPod),
			MemReqPerPod:    formatMem(wl.MemReqPod),
			TotalCPUReq:     formatCPU(wl.CPUReqTotal),
			TotalCPUUsage:   formatCPU(wl.CPUUsed),
			TotalMemReq:     formatMem(wl.MemReqTotal),
			TotalMemUsage:   formatMem(wl.MemUsed),
			CPUEffPct:       cpuEff,
			MemEffPct:       memEff,
			MonthlyCostUSD:  wl.MonthlyCost,
			WastedCostUSD:   wastedCost,
			Reason:          reason,
			RootCauses:      rootCauses,
			Severity:        severity,
		})

		totalExcess += excess
		totalWastedCost += wastedCost
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].WastedCostUSD > result[j].WastedCostUSD
	})

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"summary": map[string]interface{}{
			"overscaledCount":     len(result),
			"totalExcessReplicas": totalExcess,
			"totalWastedCostUSD":  totalWastedCost,
		},
		"workloads": result,
	})
}

// analyzeRootCauses determines why a workload is over-scaled.
func (h *OverscaledHandler) analyzeRootCauses(
	namespace string,
	as *state.AutoscalerInfo,
	currentReplicas int,
	cpuEff float64,
	hpaDiags map[string]*hpaDiag,
	kedaDiags map[string]*kedaDiag,
	hasPDBBlock bool,
) []string {
	var causes []string
	hpaKey := namespace + "/" + as.Name

	if as.Kind == "ScaledObject" || as.Kind == "KEDA" {
		// Check KEDA diagnostics
		if diag, ok := kedaDiags[hpaKey]; ok {
			if diag.ready == "False" {
				causes = append(causes, "KEDA ScaledObject not ready: "+diag.readyMsg)
			}
			if diag.active == "False" {
				causes = append(causes, "KEDA trigger inactive (metrics unavailable): "+diag.activeMsg)
			}
			if diag.fallback == "True" {
				causes = append(causes, "KEDA in fallback mode — trigger metrics failing, using fallback replicas: "+diag.fallbackMsg)
			}
			if diag.paused {
				causes = append(causes, "KEDA ScaledObject is paused — will not scale down")
			}
		}
		// KEDA creates HPAs under the hood — also check the generated HPA
		kedaHPAKey := namespace + "/keda-hpa-" + as.Name
		if diag, ok := hpaDiags[kedaHPAKey]; ok {
			causes = append(causes, analyzeHPADiag(diag, currentReplicas, as)...)
		}
	} else {
		// Direct HPA
		if diag, ok := hpaDiags[hpaKey]; ok {
			causes = append(causes, analyzeHPADiag(diag, currentReplicas, as)...)
		}
	}

	// Check if PDB is blocking scale-down
	if hasPDBBlock {
		causes = append(causes, "PDB with disruptionsAllowed=0 may be preventing autoscaler from scaling down")
	}

	// Check if stuck at max
	if int32(currentReplicas) == as.MaxReplicas && cpuEff < 10 {
		causes = append(causes, fmt.Sprintf("At max replicas (%d) with <10%% CPU usage — likely a metrics/trigger misconfiguration", as.MaxReplicas))
	}

	// Check if min replicas is too high
	if as.MinReplicas > 0 && cpuEff < 20 {
		optimalForUsage := 1 // at <20% efficiency, even min is likely too many
		if int(as.MinReplicas) > optimalForUsage*3 {
			causes = append(causes, fmt.Sprintf("HPA minReplicas=%d may be set too high for actual load", as.MinReplicas))
		}
	}

	if len(causes) == 0 {
		causes = append(causes, "Autoscaler scaled up but load has since dropped — may need cooldown tuning or manual intervention")
	}

	return causes
}

// analyzeHPADiag extracts root causes from HPA condition diagnostics.
func analyzeHPADiag(diag *hpaDiag, currentReplicas int, as *state.AutoscalerInfo) []string {
	var causes []string

	if diag.scalingActive == "False" {
		msg := "HPA ScalingActive=False — cannot read metrics"
		if diag.scalingActiveMsg != "" {
			msg += ": " + diag.scalingActiveMsg
		}
		causes = append(causes, msg)
	}

	if diag.ableToScale == "False" {
		msg := "HPA AbleToScale=False"
		if diag.ableToScaleMsg != "" {
			msg += ": " + diag.ableToScaleMsg
		}
		causes = append(causes, msg)
	}

	if diag.scalingLimited == "True" {
		msg := "HPA ScalingLimited=True"
		if diag.scalingLimitedMsg != "" {
			msg += ": " + diag.scalingLimitedMsg
		}
		causes = append(causes, msg)
	}

	if len(diag.currentMetrics) == 0 {
		causes = append(causes, "HPA reports no current metrics — metric source may be misconfigured or unavailable")
	}

	if diag.desiredReplicas > 0 && diag.desiredReplicas < int32(currentReplicas) {
		causes = append(causes, fmt.Sprintf("HPA wants %d replicas but currently has %d — scale-down may be blocked",
			diag.desiredReplicas, currentReplicas))
	}

	if diag.lastScaleTime != nil {
		sinceLastScale := time.Since(*diag.lastScaleTime)
		if sinceLastScale > 24*time.Hour {
			causes = append(causes, fmt.Sprintf("Last HPA scale event was %s ago — autoscaler may be stuck",
				formatDuration(sinceLastScale)))
		}
	}

	return causes
}

// fetchHPADiagnostics fetches all HPAs and extracts diagnostic info.
// Key: namespace/hpaName
func (h *OverscaledHandler) fetchHPADiagnostics(ctx context.Context) map[string]*hpaDiag {
	result := make(map[string]*hpaDiag)
	if h.client == nil {
		return result
	}

	var hpaList autoscalingv2.HorizontalPodAutoscalerList
	if err := h.client.List(ctx, &hpaList); err != nil {
		return result
	}

	for i := range hpaList.Items {
		hpa := &hpaList.Items[i]
		diag := &hpaDiag{
			desiredReplicas: hpa.Status.DesiredReplicas,
			currentReplicas: hpa.Status.CurrentReplicas,
			atMax:           hpa.Status.CurrentReplicas == hpa.Spec.MaxReplicas,
		}
		if hpa.Spec.MinReplicas != nil {
			diag.atMin = hpa.Status.CurrentReplicas == *hpa.Spec.MinReplicas
		}
		if hpa.Status.LastScaleTime != nil {
			t := hpa.Status.LastScaleTime.Time
			diag.lastScaleTime = &t
		}

		// Parse conditions
		for _, cond := range hpa.Status.Conditions {
			switch cond.Type {
			case autoscalingv2.ScalingActive:
				diag.scalingActive = string(cond.Status)
				diag.scalingActiveMsg = cond.Message
			case autoscalingv2.AbleToScale:
				diag.ableToScale = string(cond.Status)
				diag.ableToScaleMsg = cond.Message
			case autoscalingv2.ScalingLimited:
				diag.scalingLimited = string(cond.Status)
				diag.scalingLimitedMsg = cond.Message
			}
		}

		// Summarize current metrics
		for _, m := range hpa.Status.CurrentMetrics {
			var metricStr string
			switch m.Type {
			case autoscalingv2.ResourceMetricSourceType:
				if m.Resource != nil {
					if m.Resource.Current.AverageUtilization != nil {
						metricStr = fmt.Sprintf("resource/%s: %d%%", m.Resource.Name, *m.Resource.Current.AverageUtilization)
					} else {
						metricStr = fmt.Sprintf("resource/%s: %s", m.Resource.Name, m.Resource.Current.AverageValue.String())
					}
				}
			case autoscalingv2.ExternalMetricSourceType:
				if m.External != nil {
					metricStr = fmt.Sprintf("external/%s: %s", m.External.Metric.Name, m.External.Current.Value.String())
				}
			case autoscalingv2.ObjectMetricSourceType:
				if m.Object != nil {
					metricStr = fmt.Sprintf("object/%s: %s", m.Object.Metric.Name, m.Object.Current.Value.String())
				}
			case autoscalingv2.PodsMetricSourceType:
				if m.Pods != nil {
					metricStr = fmt.Sprintf("pods/%s: %s", m.Pods.Metric.Name, m.Pods.Current.AverageValue.String())
				}
			}
			if metricStr != "" {
				diag.currentMetrics = append(diag.currentMetrics, metricStr)
			}
		}

		key := hpa.Namespace + "/" + hpa.Name
		result[key] = diag
	}

	return result
}

// fetchKEDADiagnostics fetches KEDA ScaledObjects and extracts diagnostic info.
// Key: namespace/scaledObjectName
func (h *OverscaledHandler) fetchKEDADiagnostics(ctx context.Context) map[string]*kedaDiag {
	result := make(map[string]*kedaDiag)
	if h.client == nil {
		return result
	}

	soList := &unstructured.UnstructuredList{}
	soList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "keda.sh",
		Version: "v1alpha1",
		Kind:    "ScaledObjectList",
	})
	if err := h.client.List(ctx, soList); err != nil {
		return result
	}

	for _, item := range soList.Items {
		diag := &kedaDiag{}

		// Check conditions
		status, ok := item.Object["status"].(map[string]interface{})
		if ok {
			conditions, _ := status["conditions"].([]interface{})
			for _, c := range conditions {
				cond, _ := c.(map[string]interface{})
				if cond == nil {
					continue
				}
				condType, _ := cond["type"].(string)
				condStatus, _ := cond["status"].(string)
				condMsg, _ := cond["message"].(string)

				switch condType {
				case "Ready":
					diag.ready = condStatus
					diag.readyMsg = condMsg
				case "Active":
					diag.active = condStatus
					diag.activeMsg = condMsg
				case "Fallback":
					diag.fallback = condStatus
					diag.fallbackMsg = condMsg
				}
			}

			// Check paused annotation
			annotations := item.GetAnnotations()
			if annotations != nil {
				if v, ok := annotations["autoscaling.keda.sh/paused"]; ok && v == "true" {
					diag.paused = true
				}
				if v, ok := annotations["autoscaling.keda.sh/paused-replicas"]; ok && v != "" {
					diag.paused = true
				}
			}
		}

		key := item.GetNamespace() + "/" + item.GetName()
		result[key] = diag
	}

	return result
}

// fetchBlockingPDBsByWorkload returns a set of workload keys that have
// blocking PDBs (disruptionsAllowed=0) matching their pods.
func (h *OverscaledHandler) fetchBlockingPDBsByWorkload(ctx context.Context) map[string]bool {
	result := make(map[string]bool)
	if h.client == nil {
		return result
	}

	var pdbList policyv1.PodDisruptionBudgetList
	if err := h.client.List(ctx, &pdbList); err != nil {
		return result
	}

	// Collect blocking PDB selectors
	type pdbSel struct {
		namespace string
		selector  labels.Selector
	}
	var blockingSelectors []pdbSel
	for i := range pdbList.Items {
		pdb := &pdbList.Items[i]
		if pdb.Status.DisruptionsAllowed != 0 {
			continue
		}
		if pdb.Spec.Selector == nil {
			continue
		}
		sel, err := metav1.LabelSelectorAsSelector(pdb.Spec.Selector)
		if err != nil {
			continue
		}
		blockingSelectors = append(blockingSelectors, pdbSel{namespace: pdb.Namespace, selector: sel})
	}

	if len(blockingSelectors) == 0 {
		return result
	}

	// Check each pod against blocking PDBs
	pods := h.state.GetAllPods()
	for _, p := range pods {
		if p.Pod == nil {
			continue
		}
		for _, bs := range blockingSelectors {
			if p.Namespace == bs.namespace && bs.selector.Matches(labels.Set(p.Pod.Labels)) {
				ownerKind, ownerName := resolveOwner(p)
				key := p.Namespace + "/" + ownerKind + "/" + ownerName
				result[key] = true
				break
			}
		}
	}

	return result
}

func formatDuration(d time.Duration) string {
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// truncateMsg returns at most maxLen characters of s, adding "..." if truncated.
func truncateMsg(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// deduplicateCauses removes near-duplicate causes (e.g., when both KEDA and HPA report the same underlying issue).
func deduplicateCauses(causes []string) []string {
	seen := make(map[string]bool)
	var unique []string
	for _, c := range causes {
		// Normalize by taking first 60 chars for dedup key
		key := c
		if len(key) > 60 {
			key = key[:60]
		}
		key = strings.ToLower(key)
		if !seen[key] {
			seen[key] = true
			unique = append(unique, c)
		}
	}
	return unique
}

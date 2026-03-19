package handler

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/koptimizer/koptimizer/internal/state"
	"github.com/koptimizer/koptimizer/pkg/cost"
)

var xmxRegex = regexp.MustCompile(`-Xmx(\d+[gGmMkK]?)`)

// resolveOwner resolves the workload owner chain. For pods owned by
// ReplicaSets created by Deployments, returns "Deployment" + deployment name
// (strips the pod-template-hash suffix). Orphan pods get kind="Pod".
func resolveOwner(p *state.PodState) (kind, name string) {
	kind, name = p.OwnerKind, p.OwnerName
	if kind == "ReplicaSet" && p.Pod != nil {
		if hash, ok := p.Pod.Labels["pod-template-hash"]; ok && strings.HasSuffix(name, "-"+hash) {
			kind = "Deployment"
			name = strings.TrimSuffix(name, "-"+hash)
		}
	}
	if name == "" {
		name = p.Name
		kind = "Pod"
	}
	return
}

type WorkloadHandler struct {
	state  *state.ClusterState
	client client.Client
}

func NewWorkloadHandler(st *state.ClusterState, c client.Client) *WorkloadHandler {
	return &WorkloadHandler{state: st, client: c}
}

func (h *WorkloadHandler) List(w http.ResponseWriter, r *http.Request) {
	pods := h.state.GetAllPods()

	// Fetch all PDBs for PDB info in the workloads table
	pdbMap := h.fetchPDBsByNamespace(r.Context())

	// Fetch deployment/statefulset annotations for rightsizer original values
	deployAnnotations := h.fetchWorkloadAnnotations(r.Context())

	// Group by owner
	type wlInfo struct {
		Namespace   string
		Kind        string
		Name        string
		Replicas    int
		TotalCPU    int64
		TotalMem    int64
		TotalCPULim int64
		TotalMemLim int64
		Image       string
		Pod         *corev1.Pod // first pod (for Xmx extraction and PDB matching)
	}
	workloads := make(map[string]*wlInfo)
	for _, p := range pods {
		// Only count Running and Pending pods for replica counts and
		// resource totals. Evicted, Failed, and Succeeded pods still
		// have spec.nodeName and requests set, which inflates totals.
		if p.Pod.Status.Phase != corev1.PodRunning && p.Pod.Status.Phase != corev1.PodPending {
			continue
		}
		ownerKind, ownerName := resolveOwner(p)
		key := p.Namespace + "/" + ownerKind + "/" + ownerName
		wl, ok := workloads[key]
		if !ok {
			// Use image from first pod's first container
			img := ""
			if p.Pod != nil && len(p.Pod.Spec.Containers) > 0 {
				img = p.Pod.Spec.Containers[0].Image
			}
			wl = &wlInfo{
				Namespace: p.Namespace,
				Kind:      ownerKind,
				Name:      ownerName,
				Image:     img,
				Pod:       p.Pod,
			}
			workloads[key] = wl
		}
		wl.Replicas++
		wl.TotalCPU += p.CPURequest
		wl.TotalMem += p.MemoryRequest
		wl.TotalCPULim += p.CPULimit
		wl.TotalMemLim += p.MemoryLimit
	}

	var result []map[string]interface{}
	for _, wl := range workloads {
		cpuReq, memReq, cpuLim, memLim := int64(0), int64(0), int64(0), int64(0)
		if wl.Replicas > 0 {
			cpuReq = wl.TotalCPU / int64(wl.Replicas)
			memReq = wl.TotalMem / int64(wl.Replicas)
			cpuLim = wl.TotalCPULim / int64(wl.Replicas)
			memLim = wl.TotalMemLim / int64(wl.Replicas)
		}
		entry := map[string]interface{}{
			"namespace":   wl.Namespace,
			"kind":        wl.Kind,
			"name":        wl.Name,
			"replicas":    wl.Replicas,
			"cpuRequest":  cpuReq,
			"cpuLimit":    cpuLim,
			"memRequest":  memReq,
			"memLimit":    memLim,
			"totalCPU":    wl.TotalCPU,
			"totalMem":    wl.TotalMem,
			"totalCPULim": wl.TotalCPULim,
			"totalMemLim": wl.TotalMemLim,
			"image":       wl.Image,
			"xmx":         extractXmx(wl.Pod),
		}
		if as, ok := h.state.GetAutoscaler(wl.Namespace, wl.Kind, wl.Name); ok {
			entry["minReplicas"] = as.MinReplicas
			entry["maxReplicas"] = as.MaxReplicas
			entry["autoscaler"] = as.Kind
			entry["autoscalerName"] = as.Name
		}
		if vpa, ok := h.state.GetVPA(wl.Namespace, wl.Kind, wl.Name); ok {
			entry["vpa"] = true
			entry["vpaName"] = vpa.Name
			entry["vpaMode"] = vpa.UpdateMode
		}
		// Match PDBs to this workload via label selector
		if wl.Pod != nil {
			if pdb := matchPDB(wl.Pod, pdbMap[wl.Namespace]); pdb != nil {
				entry["pdbName"] = pdb.Name
				if pdb.Spec.MinAvailable != nil {
					entry["pdbMinAvailable"] = pdb.Spec.MinAvailable.String()
				}
				if pdb.Spec.MaxUnavailable != nil {
					entry["pdbMaxUnavailable"] = pdb.Spec.MaxUnavailable.String()
				}
				entry["pdbDisruptionsAllowed"] = pdb.Status.DisruptionsAllowed
			}
		}
		// Check for rightsizer original values (from deployment/statefulset annotations)
		annKey := wl.Namespace + "/" + wl.Kind + "/" + wl.Name
		if ann, ok := deployAnnotations[annKey]; ok {
			if v, exists := ann["koptimizer.io/original-cpu-request"]; exists {
				entry["originalCPURequest"] = v
				entry["rightsized"] = true
			}
			if v, exists := ann["koptimizer.io/original-mem-request"]; exists {
				entry["originalMemRequest"] = v
				entry["rightsized"] = true
			}
		}
		result = append(result, entry)
	}
	writePaginatedJSON(w, r, result)
}

// fetchWorkloadAnnotations fetches Deployment and StatefulSet annotations
// keyed by "namespace/Kind/name" for rightsizer original-value lookup.
func (h *WorkloadHandler) fetchWorkloadAnnotations(ctx context.Context) map[string]map[string]string {
	result := make(map[string]map[string]string)
	if h.client == nil {
		return result
	}
	// Deployments
	var deploys appsv1.DeploymentList
	if err := h.client.List(ctx, &deploys); err == nil {
		for _, d := range deploys.Items {
			if d.Annotations == nil {
				continue
			}
			_, hasCPU := d.Annotations["koptimizer.io/original-cpu-request"]
			_, hasMem := d.Annotations["koptimizer.io/original-mem-request"]
			if hasCPU || hasMem {
				key := d.Namespace + "/Deployment/" + d.Name
				result[key] = d.Annotations
			}
		}
	}
	// StatefulSets
	var stsList appsv1.StatefulSetList
	if err := h.client.List(ctx, &stsList); err == nil {
		for _, s := range stsList.Items {
			if s.Annotations == nil {
				continue
			}
			_, hasCPU := s.Annotations["koptimizer.io/original-cpu-request"]
			_, hasMem := s.Annotations["koptimizer.io/original-mem-request"]
			if hasCPU || hasMem {
				key := s.Namespace + "/StatefulSet/" + s.Name
				result[key] = s.Annotations
			}
		}
	}
	return result
}

// fetchPDBsByNamespace fetches all PDBs and groups them by namespace.
func (h *WorkloadHandler) fetchPDBsByNamespace(ctx context.Context) map[string][]policyv1.PodDisruptionBudget {
	result := make(map[string][]policyv1.PodDisruptionBudget)
	if h.client == nil {
		return result
	}
	pdbList := &policyv1.PodDisruptionBudgetList{}
	if err := h.client.List(ctx, pdbList); err != nil {
		return result
	}
	for _, pdb := range pdbList.Items {
		result[pdb.Namespace] = append(result[pdb.Namespace], pdb)
	}
	return result
}

// matchPDB finds the first PDB whose selector matches the pod's labels.
func matchPDB(pod *corev1.Pod, pdbs []policyv1.PodDisruptionBudget) *policyv1.PodDisruptionBudget {
	for i := range pdbs {
		pdb := &pdbs[i]
		if pdb.Spec.Selector == nil {
			continue
		}
		sel, err := metav1.LabelSelectorAsSelector(pdb.Spec.Selector)
		if err != nil {
			continue
		}
		if sel.Matches(labels.Set(pod.Labels)) {
			return pdb
		}
	}
	return nil
}

func (h *WorkloadHandler) Get(w http.ResponseWriter, r *http.Request) {
	ns := chi.URLParam(r, "ns")
	kind := chi.URLParam(r, "kind")
	name := chi.URLParam(r, "name")

	pods := h.state.GetAllPods()
	type podDetail struct {
		Name           string `json:"name"`
		Namespace      string `json:"namespace"`
		NodeName       string `json:"nodeName"`
		CPURequest     int64  `json:"cpuRequestMilli"`
		MemoryRequest  int64  `json:"memoryRequestBytes"`
		CPUUsage       int64  `json:"cpuUsageMilli"`
		MemoryUsage    int64  `json:"memoryUsageBytes"`
		NetworkRxBytes int64  `json:"networkRxBytes"`
		NetworkTxBytes int64  `json:"networkTxBytes"`
		Status         string `json:"status"`
	}
	var matchedPods []podDetail
	totalCPUReq := int64(0)
	totalMemReq := int64(0)

	activeReplicas := 0
	for _, p := range pods {
		ownerKind, ownerName := resolveOwner(p)
		if p.Namespace == ns && ownerKind == kind && ownerName == name {
			status := computePodStatus(p.Pod)
			matchedPods = append(matchedPods, podDetail{
				Name:           p.Name,
				Namespace:      p.Namespace,
				NodeName:       p.NodeName,
				CPURequest:     p.CPURequest,
				MemoryRequest:  p.MemoryRequest,
				CPUUsage:       p.CPUUsage,
				MemoryUsage:    p.MemoryUsage,
				NetworkRxBytes: p.NetworkRxBytes,
				NetworkTxBytes: p.NetworkTxBytes,
				Status:         status,
			})
			// Only count Running+Pending for replica count (consistent with List handler)
			phase := p.Pod.Status.Phase
			if phase == corev1.PodRunning || phase == corev1.PodPending {
				activeReplicas++
				totalCPUReq += p.CPURequest
				totalMemReq += p.MemoryRequest
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"namespace":            ns,
		"kind":                 kind,
		"name":                 name,
		"replicas":             activeReplicas,
		"totalPods":            len(matchedPods),
		"totalCPURequestMilli": totalCPUReq,
		"totalMemRequestBytes": totalMemReq,
		"pods":                 matchedPods,
	})
}

func (h *WorkloadHandler) GetRightsizing(w http.ResponseWriter, r *http.Request) {
	ns := chi.URLParam(r, "ns")
	kind := chi.URLParam(r, "kind")
	name := chi.URLParam(r, "name")

	pods := h.state.GetAllPods()
	type podUtilization struct {
		Name          string  `json:"name"`
		CPURequest    int64   `json:"cpuRequestMilli"`
		MemoryRequest int64   `json:"memoryRequestBytes"`
		CPUUsage      int64   `json:"cpuUsageMilli"`
		MemoryUsage   int64   `json:"memoryUsageBytes"`
		CPUUtilPct    float64 `json:"cpuUtilizationPct"`
		MemoryUtilPct float64 `json:"memoryUtilizationPct"`
	}
	var result []podUtilization

	for _, p := range pods {
		ownerKind, ownerName := resolveOwner(p)
		if p.Namespace == ns && ownerKind == kind && ownerName == name {
			cpuUtil := float64(0)
			if p.CPURequest > 0 {
				cpuUtil = float64(p.CPUUsage) / float64(p.CPURequest) * 100
			}
			memUtil := float64(0)
			if p.MemoryRequest > 0 {
				memUtil = float64(p.MemoryUsage) / float64(p.MemoryRequest) * 100
			}
			result = append(result, podUtilization{
				Name:          p.Name,
				CPURequest:    p.CPURequest,
				MemoryRequest: p.MemoryRequest,
				CPUUsage:      p.CPUUsage,
				MemoryUsage:   p.MemoryUsage,
				CPUUtilPct:    cpuUtil,
				MemoryUtilPct: memUtil,
			})
		}
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *WorkloadHandler) GetScaling(w http.ResponseWriter, r *http.Request) {
	ns := chi.URLParam(r, "ns")
	kind := chi.URLParam(r, "kind")
	name := chi.URLParam(r, "name")

	pods := h.state.GetAllPods()
	replicaCount := 0
	totalCPUUsage := int64(0)
	totalCPUReq := int64(0)

	for _, p := range pods {
		ownerKind, ownerName := resolveOwner(p)
		if p.Namespace == ns && ownerKind == kind && ownerName == name {
			// Only count Running+Pending pods (consistent with List handler)
			phase := p.Pod.Status.Phase
			if phase == corev1.PodRunning || phase == corev1.PodPending {
				replicaCount++
				totalCPUUsage += p.CPUUsage
				totalCPUReq += p.CPURequest
			}
		}
	}

	avgCPUUtil := float64(0)
	if totalCPUReq > 0 {
		avgCPUUtil = float64(totalCPUUsage) / float64(totalCPUReq) * 100
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"namespace":            ns,
		"kind":                 kind,
		"name":                 name,
		"currentReplicas":      replicaCount,
		"avgCPUUtilizationPct": avgCPUUtil,
	})
}

// GetEfficiency returns per-workload efficiency analysis with waste identification.
func (h *WorkloadHandler) GetEfficiency(w http.ResponseWriter, r *http.Request) {
	pods := h.state.GetAllPods()
	nodes := h.state.GetAllNodes()

	// Build node cost and capacity maps for capacity-based cost allocation
	nodeCostMap := make(map[string]float64)
	nodeCPUCapMap := make(map[string]int64)
	nodeMemCapMap := make(map[string]int64)
	for _, n := range nodes {
		nodeCostMap[n.Node.Name] = n.HourlyCostUSD * cost.HoursPerMonth
		nodeCPUCapMap[n.Node.Name] = n.CPUCapacity
		nodeMemCapMap[n.Node.Name] = n.MemoryCapacity
	}

	// Group pods by owner
	type workloadInfo struct {
		Namespace   string
		Kind        string
		Name        string
		Replicas    int
		CPUReq      int64
		CPUUsed     int64
		MemReq      int64
		MemUsed     int64
		MonthlyCost float64
		HasMetrics  bool
	}
	workloads := make(map[string]*workloadInfo)

	for _, p := range pods {
		// Only include running pods for cost allocation and efficiency.
		// Non-running pods (Succeeded Jobs, Failed pods) still have NodeName
		// set but aren't counted in the node's CPURequested, which would
		// cause their cost fraction to exceed 1.0 and inflate totals.
		if p.Pod.Status.Phase != corev1.PodRunning {
			continue
		}
		ownerKind, ownerName := resolveOwner(p)
		key := p.Namespace + "/" + ownerKind + "/" + ownerName
		wl, ok := workloads[key]
		if !ok {
			wl = &workloadInfo{
				Namespace: p.Namespace,
				Kind:      ownerKind,
				Name:      ownerName,
			}
			workloads[key] = wl
		}
		wl.Replicas++
		wl.CPUReq += p.CPURequest
		wl.CPUUsed += p.CPUUsage
		wl.MemReq += p.MemoryRequest
		wl.MemUsed += p.MemoryUsage
		if p.CPUUsage > 0 || p.MemoryUsage > 0 {
			wl.HasMetrics = true
		}

		// Capacity-based cost allocation: blended 50/50 CPU+memory fraction of node capacity.
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

	type workloadEfficiency struct {
		Namespace        string  `json:"namespace"`
		Kind             string  `json:"kind"`
		Name             string  `json:"name"`
		Replicas         int     `json:"replicas"`
		CPURequest       string  `json:"cpuRequest"`
		CPUUsed          string  `json:"cpuUsed"`
		MemRequest       string  `json:"memRequest"`
		MemUsed          string  `json:"memUsed"`
		CPUEfficiencyPct float64 `json:"cpuEfficiencyPct"`
		MemEfficiencyPct float64 `json:"memEfficiencyPct"`
		WastedCPU        string  `json:"wastedCPU"`
		WastedMem        string  `json:"wastedMem"`
		MonthlyCostUSD   float64 `json:"monthlyCostUSD"`
		WastedCostUSD    float64 `json:"wastedCostUSD"`
		HasMetrics       bool    `json:"hasMetrics"`
	}

	var result []workloadEfficiency
	totalWastedCost := 0.0
	sumCPUEff := 0.0
	sumMemEff := 0.0
	count := 0

	for _, wl := range workloads {
		cpuEff := 0.0
		if wl.CPUReq > 0 {
			cpuEff = float64(wl.CPUUsed) / float64(wl.CPUReq) * 100
		}
		memEff := 0.0
		if wl.MemReq > 0 {
			memEff = float64(wl.MemUsed) / float64(wl.MemReq) * 100
		}

		wastedCPU := wl.CPUReq - wl.CPUUsed
		if wastedCPU < 0 {
			wastedCPU = 0
		}
		wastedMem := wl.MemReq - wl.MemUsed
		if wastedMem < 0 {
			wastedMem = 0
		}

		// Wasted cost: based on max(cpuEff, memEff) so we account for
		// whichever resource is the binding constraint. Without metrics
		// (cpuEff=0, memEff=0) we cannot determine waste — skip.
		wastedCost := 0.0
		if wl.HasMetrics && wl.MonthlyCost > 0 {
			maxEff := cpuEff / 100
			if memEff/100 > maxEff {
				maxEff = memEff / 100
			}
			wastedCost = wl.MonthlyCost * (1.0 - maxEff)
		}
		totalWastedCost += wastedCost

		result = append(result, workloadEfficiency{
			Namespace:        wl.Namespace,
			Kind:             wl.Kind,
			Name:             wl.Name,
			Replicas:         wl.Replicas,
			CPURequest:       formatCPU(wl.CPUReq),
			CPUUsed:          formatCPU(wl.CPUUsed),
			MemRequest:       formatMem(wl.MemReq),
			MemUsed:          formatMem(wl.MemUsed),
			CPUEfficiencyPct: cpuEff,
			MemEfficiencyPct: memEff,
			WastedCPU:        formatCPU(wastedCPU),
			WastedMem:        formatMem(wastedMem),
			MonthlyCostUSD:   wl.MonthlyCost,
			WastedCostUSD:    wastedCost,
			HasMetrics:       wl.HasMetrics,
		})
		sumCPUEff += cpuEff
		sumMemEff += memEff
		count++
	}

	// Sort by wasted cost descending
	sort.Slice(result, func(i, j int) bool {
		return result[i].WastedCostUSD > result[j].WastedCostUSD
	})

	avgCPUEff := 0.0
	avgMemEff := 0.0
	if count > 0 {
		avgCPUEff = sumCPUEff / float64(count)
		avgMemEff = sumMemEff / float64(count)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"summary": map[string]interface{}{
			"avgCPUEfficiency":   avgCPUEff,
			"avgMemEfficiency":   avgMemEff,
			"totalWastedCostUSD": totalWastedCost,
			"metricsAvailable":   h.state.MetricsAvailable,
			"podsWithMetrics":    h.state.PodsWithMetrics,
			"totalPods":          len(pods),
		},
		"workloads": result,
	})
}

// extractXmx extracts the -Xmx JVM heap size from a pod's containers.
// It checks common JVM environment variables and command/args.
func extractXmx(pod *corev1.Pod) string {
	if pod == nil {
		return ""
	}
	for _, c := range pod.Spec.Containers {
		// Scan ALL env var values — apps use many different var names
		// (JAVA_OPTS, JVM_OPTS, JAVA_TOOL_OPTIONS, _JAVA_OPTIONS,
		// CATALINA_OPTS, JDK_JAVA_OPTIONS, app-specific names, etc.)
		for _, env := range c.Env {
			if m := xmxRegex.FindStringSubmatch(env.Value); len(m) > 1 {
				return m[1]
			}
		}
		// Check command + args
		for _, arg := range append(c.Command, c.Args...) {
			if m := xmxRegex.FindStringSubmatch(arg); len(m) > 1 {
				return m[1]
			}
		}
	}
	return ""
}

// formatCPU formats millicores to K8s-style string (e.g., 100 -> "100m").
func formatCPU(millis int64) string {
	return fmt.Sprintf("%dm", millis)
}

// formatMem formats bytes to human-readable K8s-style string.
func formatMem(bytes int64) string {
	const (
		gi = 1024 * 1024 * 1024
		mi = 1024 * 1024
	)
	if bytes >= gi && bytes%gi == 0 {
		return fmt.Sprintf("%dGi", bytes/gi)
	}
	return fmt.Sprintf("%dMi", bytes/mi)
}

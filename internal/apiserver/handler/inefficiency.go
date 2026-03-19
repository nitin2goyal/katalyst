package handler

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/koptimizer/koptimizer/internal/state"
	"github.com/koptimizer/koptimizer/pkg/cost"
)

// InefficiencyHandler provides a consolidated view of all cluster inefficiencies.
type InefficiencyHandler struct {
	state  *state.ClusterState
	client client.Client
}

func NewInefficiencyHandler(st *state.ClusterState, c client.Client) *InefficiencyHandler {
	return &InefficiencyHandler{state: st, client: c}
}

// --- Response types ---

type inefficiencySummary struct {
	TotalIssues     int     `json:"totalIssues"`
	CriticalCount   int     `json:"criticalCount"`
	WarningCount    int     `json:"warningCount"`
	InfoCount       int     `json:"infoCount"`
	EstWastedCost   float64 `json:"estimatedWastedMonthlyCost"`
	Categories      []categoryCount `json:"categories"`
}

type categoryCount struct {
	Category string `json:"category"`
	Count    int    `json:"count"`
	Severity string `json:"severity"`
}

type maxPodsIssue struct {
	NodeName       string  `json:"nodeName"`
	NodeGroup      string  `json:"nodeGroup"`
	InstanceType   string  `json:"instanceType"`
	CurrentPods    int     `json:"currentPods"`
	MaxPods        int     `json:"maxPods"`
	PodUtilPct     float64 `json:"podUtilizationPct"`
	CPUAllocPct    float64 `json:"cpuAllocationPct"`
	MemAllocPct    float64 `json:"memAllocationPct"`
	CPUUsagePct    float64 `json:"cpuUsagePct"`
	MemUsagePct    float64 `json:"memUsagePct"`
	WastedCPUCores float64 `json:"wastedCPUCores"`
	WastedMemGB    float64 `json:"wastedMemGB"`
	MonthlyCost    float64 `json:"monthlyCostUSD"`
	Severity       string  `json:"severity"`
	Impact         string  `json:"impact"`
}

type antiAffinityIssue struct {
	Namespace       string  `json:"namespace"`
	Name            string  `json:"name"`
	Kind            string  `json:"kind"`
	Replicas        int     `json:"replicas"`
	NodeCount       int     `json:"nodeCount"`
	AffinityType    string  `json:"affinityType"`
	AvgCPUAllocPct  float64 `json:"avgNodeCPUAllocationPct"`
	AvgMemAllocPct  float64 `json:"avgNodeMemAllocationPct"`
	NodesWasted     int     `json:"nodesEffectivelyWasted"`
	WastedCostUSD   float64 `json:"wastedMonthlyCostUSD"`
	Severity        string  `json:"severity"`
	Impact          string  `json:"impact"`
}

type kedaIssue struct {
	Namespace      string   `json:"namespace"`
	Name           string   `json:"name"`
	ScaledObject   string   `json:"scaledObject"`
	CurrentReplicas int     `json:"currentReplicas"`
	MinReplicas    int32    `json:"minReplicas"`
	MaxReplicas    int32    `json:"maxReplicas"`
	IssueType      string   `json:"issueType"`
	Problems       []string `json:"problems"`
	CPURequestM    string   `json:"cpuRequest"`
	CPULimitM      string   `json:"cpuLimit"`
	RequestLimitRatio float64 `json:"requestLimitRatio"`
	Severity       string   `json:"severity"`
	Impact         string   `json:"impact"`
}

type badPDBIssue struct {
	Name              string `json:"name"`
	Namespace         string `json:"namespace"`
	Reason            string `json:"reason"`
	ExpectedPods      int32  `json:"expectedPods"`
	CurrentHealthy    int32  `json:"currentHealthy"`
	DisruptionsAllowed int32 `json:"disruptionsAllowed"`
	AgeDays           int    `json:"ageDays"`
	AffectedNodes     int    `json:"affectedNodes"`
	Severity          string `json:"severity"`
	Impact            string `json:"impact"`
}

type badStatePodIssue struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	NodeName  string `json:"nodeName"`
	Status    string `json:"status"`
	Readiness string `json:"readiness"`
	Restarts  int32  `json:"restarts"`
	Age       string `json:"age"`
	BlocksScaleDown bool   `json:"blocksScaleDown"`
	Severity  string `json:"severity"`
	Impact    string `json:"impact"`
}

type nodeFragIssue struct {
	NodeName     string  `json:"nodeName"`
	NodeGroup    string  `json:"nodeGroup"`
	InstanceType string  `json:"instanceType"`
	CPUAllocPct  float64 `json:"cpuAllocationPct"`
	MemAllocPct  float64 `json:"memAllocationPct"`
	CPUUsagePct  float64 `json:"cpuUsagePct"`
	MemUsagePct  float64 `json:"memUsagePct"`
	PodCount     int     `json:"podCount"`
	MonthlyCost  float64 `json:"monthlyCostUSD"`
	Severity     string  `json:"severity"`
	Impact       string  `json:"impact"`
}

type badRatioIssue struct {
	Namespace      string  `json:"namespace"`
	Name           string  `json:"name"`
	Kind           string  `json:"kind"`
	Replicas       int     `json:"replicas"`
	CPURequestPod  string  `json:"cpuRequestPerPod"`
	MemRequestPod  string  `json:"memRequestPerPod"`
	WorkloadRatio  float64 `json:"workloadRatioGBPerCPU"`
	NodeRatio      float64 `json:"nodeRatioGBPerCPU"`
	RatioDeviation float64 `json:"ratioDeviationPct"`
	TotalCPUReq    int64   `json:"totalCPURequestMilli"`
	TotalMemReq    int64   `json:"totalMemRequestBytes"`
	WastedMemGB    float64 `json:"wastedMemGB"`
	WastedCPUCores float64 `json:"wastedCPUCores"`
	WastedCostUSD  float64 `json:"wastedMonthlyCostUSD"`
	NodeGroup      string  `json:"nodeGroup"`
	InstanceType   string  `json:"instanceType"`
	Severity       string  `json:"severity"`
	Impact         string  `json:"impact"`
}

type networkHogIssue struct {
	Namespace  string `json:"namespace"`
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	Replicas   int    `json:"replicas"`
	TotalRxGB  float64 `json:"totalRxGB"`
	TotalTxGB  float64 `json:"totalTxGB"`
	TotalBytes int64   `json:"totalBytes"`
	PerPodRxGB float64 `json:"perPodRxGB"`
	PerPodTxGB float64 `json:"perPodTxGB"`
	Severity   string `json:"severity"`
	Impact     string `json:"impact"`
}

// Get returns a consolidated view of all cluster inefficiencies.
func (h *InefficiencyHandler) Get(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	maxPods := h.detectMaxPodsIssues()
	antiAffinity := h.detectAntiAffinityIssues(ctx)
	kedaIssues := h.detectKEDAIssues(ctx)
	badPDBs := h.detectBadPDBs(ctx)
	badPods := h.detectBadStatePods()
	fragmentation := h.detectNodeFragmentation()
	badRatios := h.detectBadRatioWorkloads()
	networkHogs := h.detectNetworkHogs()

	// Build summary
	totalIssues := len(maxPods) + len(antiAffinity) + len(kedaIssues) + len(badPDBs) + len(badPods) + len(fragmentation) + len(badRatios) + len(networkHogs)
	critical, warning, info := 0, 0, 0
	totalWasted := 0.0

	countSeverity := func(sev string) {
		switch sev {
		case "critical":
			critical++
		case "warning":
			warning++
		default:
			info++
		}
	}
	for _, i := range maxPods {
		countSeverity(i.Severity)
		totalWasted += i.MonthlyCost * (1 - i.CPUUsagePct/100) * 0.5 // rough estimate
	}
	for _, i := range antiAffinity {
		countSeverity(i.Severity)
		totalWasted += i.WastedCostUSD
	}
	for _, i := range kedaIssues {
		countSeverity(i.Severity)
	}
	for _, i := range badPDBs {
		countSeverity(i.Severity)
	}
	for _, i := range badPods {
		countSeverity(i.Severity)
	}
	for _, i := range fragmentation {
		countSeverity(i.Severity)
	}
	for _, i := range badRatios {
		countSeverity(i.Severity)
		totalWasted += i.WastedCostUSD
	}
	for _, i := range networkHogs {
		countSeverity(i.Severity)
	}

	categories := []categoryCount{
		{Category: "maxPods", Count: len(maxPods), Severity: maxSeverity(maxPods, func(i maxPodsIssue) string { return i.Severity })},
		{Category: "antiAffinity", Count: len(antiAffinity), Severity: maxSeverity(antiAffinity, func(i antiAffinityIssue) string { return i.Severity })},
		{Category: "keda", Count: len(kedaIssues), Severity: maxSeverity(kedaIssues, func(i kedaIssue) string { return i.Severity })},
		{Category: "badPDBs", Count: len(badPDBs), Severity: maxSeverity(badPDBs, func(i badPDBIssue) string { return i.Severity })},
		{Category: "badPods", Count: len(badPods), Severity: maxSeverity(badPods, func(i badStatePodIssue) string { return i.Severity })},
		{Category: "fragmentation", Count: len(fragmentation), Severity: maxSeverity(fragmentation, func(i nodeFragIssue) string { return i.Severity })},
		{Category: "badRatio", Count: len(badRatios), Severity: maxSeverity(badRatios, func(i badRatioIssue) string { return i.Severity })},
		{Category: "networkHogs", Count: len(networkHogs), Severity: maxSeverity(networkHogs, func(i networkHogIssue) string { return i.Severity })},
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"summary": inefficiencySummary{
			TotalIssues:   totalIssues,
			CriticalCount: critical,
			WarningCount:  warning,
			InfoCount:     info,
			EstWastedCost: totalWasted,
			Categories:    categories,
		},
		"maxPods":       maxPods,
		"antiAffinity":  antiAffinity,
		"keda":          kedaIssues,
		"badPDBs":       badPDBs,
		"badPods":       badPods,
		"fragmentation": fragmentation,
		"badRatio":      badRatios,
		"networkHogs":   networkHogs,
	})
}

// GetNetworkPods returns per-pod and per-node network I/O for debugging.
func (h *InefficiencyHandler) GetNetworkPods(w http.ResponseWriter, r *http.Request) {
	pods := h.state.GetAllPods()
	nodes := h.state.GetAllNodes()

	const gb = float64(1024 * 1024 * 1024)

	// Per-pod data
	type podNet struct {
		Name      string  `json:"name"`
		Namespace string  `json:"namespace"`
		NodeName  string  `json:"nodeName"`
		Owner     string  `json:"owner"`
		OwnerKind string  `json:"ownerKind"`
		RxBytes   int64   `json:"rxBytes"`
		TxBytes   int64   `json:"txBytes"`
		TotalBytes int64  `json:"totalBytes"`
		RxGB      float64 `json:"rxGB"`
		TxGB      float64 `json:"txGB"`
		Status    string  `json:"status"`
	}
	var podResults []podNet
	for _, p := range pods {
		if p.Pod == nil {
			continue
		}
		if p.NetworkRxBytes == 0 && p.NetworkTxBytes == 0 {
			continue
		}
		ownerKind, ownerName := resolveOwner(p)
		total := p.NetworkRxBytes + p.NetworkTxBytes
		podResults = append(podResults, podNet{
			Name:       p.Name,
			Namespace:  p.Namespace,
			NodeName:   p.NodeName,
			Owner:      ownerName,
			OwnerKind:  ownerKind,
			RxBytes:    p.NetworkRxBytes,
			TxBytes:    p.NetworkTxBytes,
			TotalBytes: total,
			RxGB:       math.Round(float64(p.NetworkRxBytes)/gb*100) / 100,
			TxGB:       math.Round(float64(p.NetworkTxBytes)/gb*100) / 100,
			Status:     computePodStatus(p.Pod),
		})
	}
	sort.Slice(podResults, func(i, j int) bool {
		return podResults[i].TotalBytes > podResults[j].TotalBytes
	})

	// Per-node aggregated data
	type nodeNet struct {
		NodeName     string  `json:"nodeName"`
		InstanceType string  `json:"instanceType"`
		NodeGroup    string  `json:"nodeGroup"`
		PodCount     int     `json:"podCount"`
		TotalRxBytes int64   `json:"totalRxBytes"`
		TotalTxBytes int64   `json:"totalTxBytes"`
		TotalBytes   int64   `json:"totalBytes"`
		RxGB         float64 `json:"rxGB"`
		TxGB         float64 `json:"txGB"`
	}
	nodeAgg := make(map[string]*nodeNet)
	for _, p := range podResults {
		nn, ok := nodeAgg[p.NodeName]
		if !ok {
			nn = &nodeNet{NodeName: p.NodeName}
			nodeAgg[p.NodeName] = nn
		}
		nn.PodCount++
		nn.TotalRxBytes += p.RxBytes
		nn.TotalTxBytes += p.TxBytes
	}
	var nodeResults []nodeNet
	for _, n := range nodes {
		nn, ok := nodeAgg[n.Node.Name]
		if !ok {
			continue
		}
		nn.InstanceType = n.InstanceType
		nn.NodeGroup = n.NodeGroupID
		nn.TotalBytes = nn.TotalRxBytes + nn.TotalTxBytes
		nn.RxGB = math.Round(float64(nn.TotalRxBytes)/gb*100) / 100
		nn.TxGB = math.Round(float64(nn.TotalTxBytes)/gb*100) / 100
		nodeResults = append(nodeResults, *nn)
	}
	sort.Slice(nodeResults, func(i, j int) bool {
		return nodeResults[i].TotalBytes > nodeResults[j].TotalBytes
	})

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"pods":  podResults,
		"nodes": nodeResults,
	})
}

// --- Detection: Max Pods per Node ---

func (h *InefficiencyHandler) detectMaxPodsIssues() []maxPodsIssue {
	nodes := h.state.GetAllNodes()
	var result []maxPodsIssue

	for _, n := range nodes {
		if n.Node == nil {
			continue
		}
		// Get max pods from allocatable
		maxPodsQty, ok := n.Node.Status.Allocatable[corev1.ResourcePods]
		if !ok {
			maxPodsQty, ok = n.Node.Status.Capacity[corev1.ResourcePods]
			if !ok {
				continue
			}
		}
		maxPodsVal := int(maxPodsQty.Value())
		if maxPodsVal == 0 {
			continue
		}

		currentPods := len(n.Pods)
		podUtilPct := float64(currentPods) / float64(maxPodsVal) * 100

		// Flag nodes at >= 85% pod capacity with low resource utilization
		if podUtilPct < 85 {
			continue
		}

		cpuAllocPct := n.CPURequestUtilization()
		memAllocPct := n.MemoryRequestUtilization()
		cpuUsagePct := n.CPUUtilization()
		memUsagePct := n.MemoryUtilization()

		// Only flag if there's actually wasted capacity (low CPU/mem utilization
		// despite being full on pods — the sign that maxPods is the bottleneck)
		if cpuAllocPct > 80 && memAllocPct > 80 {
			continue // node is well-packed, maxPods isn't the constraint
		}

		wastedCPU := float64(n.CPUCapacity-n.CPURequested) / 1000
		wastedMem := float64(n.MemoryCapacity-n.MemoryRequested) / (1024 * 1024 * 1024)
		if wastedCPU < 0 {
			wastedCPU = 0
		}
		if wastedMem < 0 {
			wastedMem = 0
		}

		severity := "warning"
		if podUtilPct >= 95 && (cpuAllocPct < 50 || memAllocPct < 50) {
			severity = "critical"
		}

		impact := fmt.Sprintf("Node is %d%% full by pod count but only %.0f%% CPU / %.0f%% memory allocated — %.1f CPU cores and %.1f GB memory wasted",
			int(podUtilPct), cpuAllocPct, memAllocPct, wastedCPU, wastedMem)

		result = append(result, maxPodsIssue{
			NodeName:       n.Node.Name,
			NodeGroup:      n.NodeGroupID,
			InstanceType:   n.InstanceType,
			CurrentPods:    currentPods,
			MaxPods:        maxPodsVal,
			PodUtilPct:     math.Round(podUtilPct*10) / 10,
			CPUAllocPct:    math.Round(cpuAllocPct*10) / 10,
			MemAllocPct:    math.Round(memAllocPct*10) / 10,
			CPUUsagePct:    math.Round(cpuUsagePct*10) / 10,
			MemUsagePct:    math.Round(memUsagePct*10) / 10,
			WastedCPUCores: math.Round(wastedCPU*10) / 10,
			WastedMemGB:    math.Round(wastedMem*10) / 10,
			MonthlyCost:    n.HourlyCostUSD * cost.HoursPerMonth,
			Severity:       severity,
			Impact:         impact,
		})
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].PodUtilPct > result[j].PodUtilPct
	})

	if result == nil {
		return []maxPodsIssue{}
	}
	return result
}

// --- Detection: Anti-Affinity Spread ---

func (h *InefficiencyHandler) detectAntiAffinityIssues(ctx context.Context) []antiAffinityIssue {
	var deployList appsv1.DeploymentList
	if err := h.client.List(ctx, &deployList); err != nil {
		return []antiAffinityIssue{}
	}

	nodes := h.state.GetAllNodes()
	nodeMap := make(map[string]*state.NodeState, len(nodes))
	for _, n := range nodes {
		nodeMap[n.Node.Name] = n
	}

	pods := h.state.GetAllPods()

	var result []antiAffinityIssue

	for i := range deployList.Items {
		deploy := &deployList.Items[i]
		if deploy.Spec.Template.Spec.Affinity == nil {
			continue
		}
		podAntiAffinity := deploy.Spec.Template.Spec.Affinity.PodAntiAffinity
		if podAntiAffinity == nil {
			continue
		}

		// Check if there are required anti-affinity rules (hard spread)
		affinityType := ""
		if len(podAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution) > 0 {
			affinityType = "required"
		} else if len(podAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution) > 0 {
			affinityType = "preferred"
		}
		if affinityType == "" {
			continue
		}

		replicas := int32(1)
		if deploy.Spec.Replicas != nil {
			replicas = *deploy.Spec.Replicas
		}
		if replicas < 2 {
			continue
		}

		// Find pods belonging to this deployment
		deployNodes := map[string]bool{}
		for _, p := range pods {
			if p.Namespace != deploy.Namespace {
				continue
			}
			if p.OwnerKind != "ReplicaSet" {
				continue
			}
			// Check if the ReplicaSet is owned by this deployment
			if !strings.HasPrefix(p.OwnerName, deploy.Name+"-") {
				continue
			}
			if p.NodeName != "" {
				deployNodes[p.NodeName] = true
			}
		}

		nodeCount := len(deployNodes)
		if nodeCount < 2 {
			continue
		}

		// Calculate average utilization of the nodes this deployment uses
		var totalCPUAlloc, totalMemAlloc float64
		var totalMonthlyCost float64
		counted := 0
		for nodeName := range deployNodes {
			if ns, ok := nodeMap[nodeName]; ok {
				totalCPUAlloc += ns.CPURequestUtilization()
				totalMemAlloc += ns.MemoryRequestUtilization()
				totalMonthlyCost += ns.HourlyCostUSD * cost.HoursPerMonth
				counted++
			}
		}

		if counted == 0 {
			continue
		}

		avgCPU := totalCPUAlloc / float64(counted)
		avgMem := totalMemAlloc / float64(counted)

		// Only flag if the spread is causing poor allocation
		// (many nodes with low utilization forced by anti-affinity)
		if avgCPU > 60 && avgMem > 60 {
			continue // nodes are reasonably well-utilized
		}

		// Estimate how many nodes could be saved if pods were colocated
		nodesWasted := 0
		if int(replicas) < nodeCount {
			// More nodes than replicas shouldn't happen with anti-affinity, but check
			nodesWasted = nodeCount - int(replicas)
		}
		// If average utilization is <50%, at least some nodes are underutilized because of this spread
		if avgCPU < 50 || avgMem < 50 {
			// Rough estimate: if you could pack better, you'd save proportionally
			consolidationRatio := math.Max(avgCPU, avgMem) / 100
			if consolidationRatio > 0 {
				idealNodes := int(math.Ceil(float64(nodeCount) * consolidationRatio))
				if idealNodes < nodeCount {
					nodesWasted = nodeCount - idealNodes
				}
			}
		}

		if nodesWasted == 0 && avgCPU > 40 && avgMem > 40 {
			continue // not significant enough
		}

		wastedCost := 0.0
		if counted > 0 && nodesWasted > 0 {
			wastedCost = totalMonthlyCost * float64(nodesWasted) / float64(counted)
		}

		severity := "info"
		if affinityType == "required" && nodesWasted >= 2 {
			severity = "warning"
		}
		if affinityType == "required" && avgCPU < 30 && nodeCount >= 4 {
			severity = "critical"
		}

		impact := fmt.Sprintf("%d pods spread across %d nodes (avg %.0f%% CPU, %.0f%% mem allocation) — %s anti-affinity forces 1-pod-per-node",
			replicas, nodeCount, avgCPU, avgMem, affinityType)

		result = append(result, antiAffinityIssue{
			Namespace:      deploy.Namespace,
			Name:           deploy.Name,
			Kind:           "Deployment",
			Replicas:       int(replicas),
			NodeCount:      nodeCount,
			AffinityType:   affinityType,
			AvgCPUAllocPct: math.Round(avgCPU*10) / 10,
			AvgMemAllocPct: math.Round(avgMem*10) / 10,
			NodesWasted:    nodesWasted,
			WastedCostUSD:  math.Round(wastedCost*100) / 100,
			Severity:       severity,
			Impact:         impact,
		})
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].WastedCostUSD > result[j].WastedCostUSD
	})

	if result == nil {
		return []antiAffinityIssue{}
	}
	return result
}

// --- Detection: KEDA/Autoscaler Issues ---

func (h *InefficiencyHandler) detectKEDAIssues(ctx context.Context) []kedaIssue {
	pods := h.state.GetAllPods()
	var result []kedaIssue

	// Fetch KEDA ScaledObjects
	soList := &unstructured.UnstructuredList{}
	soList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "keda.sh",
		Version: "v1alpha1",
		Kind:    "ScaledObjectList",
	})
	soErr := h.client.List(ctx, soList)

	// Fetch HPAs for additional diagnostics
	var hpaList autoscalingv2.HorizontalPodAutoscalerList
	_ = h.client.List(ctx, &hpaList)

	hpaDiags := make(map[string]*autoscalingv2.HorizontalPodAutoscaler)
	for i := range hpaList.Items {
		hpa := &hpaList.Items[i]
		key := hpa.Namespace + "/" + hpa.Name
		hpaDiags[key] = hpa
	}

	// Group pods by owner to get per-workload metrics
	wlPods := make(map[string]*kedaWlPodInfo)
	for _, p := range pods {
		if p.Pod.Status.Phase != corev1.PodRunning {
			continue
		}
		ownerKind, ownerName := resolveOwner(p)
		key := p.Namespace + "/" + ownerKind + "/" + ownerName
		wl, ok := wlPods[key]
		if !ok {
			wl = &kedaWlPodInfo{}
			wlPods[key] = wl
		}
		wl.replicas++
		// Use the first pod's request/limit as representative
		if wl.cpuReqPod == 0 {
			wl.cpuReqPod = p.CPURequest
			wl.cpuLimPod = p.CPULimit
		}
	}

	if soErr != nil {
		// No KEDA CRDs installed — still check HPAs for issues
		return h.detectHPAOnlyIssues(ctx, hpaDiags, wlPods)
	}

	for _, item := range soList.Items {
		ns := item.GetNamespace()
		soName := item.GetName()

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
			minReplicas = ineffToInt32(v)
		}
		maxReplicas := int32(0)
		if v, ok := spec["maxReplicaCount"]; ok {
			maxReplicas = ineffToInt32(v)
		}

		// Check KEDA status conditions
		var problems []string
		issueType := ""

		status, _ := item.Object["status"].(map[string]interface{})
		if status != nil {
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
					if condStatus == "False" {
						problems = append(problems, "ScaledObject not ready: "+condMsg)
						issueType = "not-ready"
					}
				case "Active":
					if condStatus == "False" {
						problems = append(problems, "Trigger inactive (no events/metrics): "+condMsg)
						if issueType == "" {
							issueType = "trigger-inactive"
						}
					}
				case "Fallback":
					if condStatus == "True" {
						problems = append(problems, "In fallback mode — trigger metrics failing: "+condMsg)
						if issueType == "" {
							issueType = "fallback"
						}
					}
				}
			}
		}

		// Check paused
		annotations := item.GetAnnotations()
		if annotations != nil {
			if v, ok := annotations["autoscaling.keda.sh/paused"]; ok && v == "true" {
				problems = append(problems, "ScaledObject is paused — will not scale up or down")
				if issueType == "" {
					issueType = "paused"
				}
			}
		}

		// Check KEDA-generated HPA for issues
		kedaHPAKey := ns + "/keda-hpa-" + soName
		if hpa, ok := hpaDiags[kedaHPAKey]; ok {
			for _, cond := range hpa.Status.Conditions {
				if cond.Type == autoscalingv2.ScalingActive && string(cond.Status) == "False" {
					problems = append(problems, "Generated HPA cannot read metrics: "+cond.Message)
					if issueType == "" {
						issueType = "hpa-metrics-failed"
					}
				}
			}
		}

		// Check CPU request vs limit mismatch
		wlKey := ns + "/" + targetKind + "/" + targetName
		cpuReqStr := "-"
		cpuLimStr := "-"
		reqLimRatio := 0.0
		currentReplicas := 0
		if wl, ok := wlPods[wlKey]; ok {
			currentReplicas = wl.replicas
			cpuReqStr = formatCPU(wl.cpuReqPod)
			cpuLimStr = formatCPU(wl.cpuLimPod)
			if wl.cpuLimPod > 0 && wl.cpuReqPod > 0 {
				reqLimRatio = float64(wl.cpuReqPod) / float64(wl.cpuLimPod)
				if reqLimRatio < 0.3 {
					problems = append(problems,
						fmt.Sprintf("CPU request (%s) is %.0f%% of limit (%s) — HPA/KEDA scales on request%%, so pod triggers scale-up even with plenty of headroom to limit",
							cpuReqStr, reqLimRatio*100, cpuLimStr))
					if issueType == "" {
						issueType = "request-limit-mismatch"
					}
				}
			}
		}

		if len(problems) == 0 {
			continue
		}

		severity := "warning"
		if issueType == "not-ready" || issueType == "trigger-inactive" {
			severity = "critical"
		}
		if issueType == "request-limit-mismatch" {
			severity = "info"
		}

		impact := strings.Join(problems, "; ")

		result = append(result, kedaIssue{
			Namespace:         ns,
			Name:              targetName,
			ScaledObject:      soName,
			CurrentReplicas:   currentReplicas,
			MinReplicas:       minReplicas,
			MaxReplicas:       maxReplicas,
			IssueType:         issueType,
			Problems:          problems,
			CPURequestM:       cpuReqStr,
			CPULimitM:         cpuLimStr,
			RequestLimitRatio: math.Round(reqLimRatio*100) / 100,
			Severity:          severity,
			Impact:            impact,
		})
	}

	sort.Slice(result, func(i, j int) bool {
		return ineffSeverityRank(result[i].Severity) > ineffSeverityRank(result[j].Severity)
	})

	if result == nil {
		return []kedaIssue{}
	}
	return result
}

// detectHPAOnlyIssues checks for HPA issues when KEDA is not installed.
func (h *InefficiencyHandler) detectHPAOnlyIssues(ctx context.Context, hpaDiags map[string]*autoscalingv2.HorizontalPodAutoscaler, wlPods map[string]*kedaWlPodInfo) []kedaIssue {
	var result []kedaIssue

	for _, hpa := range hpaDiags {
		var problems []string
		issueType := ""

		for _, cond := range hpa.Status.Conditions {
			if cond.Type == autoscalingv2.ScalingActive && string(cond.Status) == "False" {
				problems = append(problems, "HPA ScalingActive=False: "+cond.Message)
				issueType = "hpa-metrics-failed"
			}
			if cond.Type == autoscalingv2.AbleToScale && string(cond.Status) == "False" {
				problems = append(problems, "HPA AbleToScale=False: "+cond.Message)
				if issueType == "" {
					issueType = "hpa-unable"
				}
			}
		}

		if len(hpa.Status.CurrentMetrics) == 0 && hpa.Status.CurrentReplicas > 1 {
			problems = append(problems, "HPA reports no current metrics — metric source may be misconfigured")
			if issueType == "" {
				issueType = "hpa-no-metrics"
			}
		}

		if len(problems) == 0 {
			continue
		}

		ref := hpa.Spec.ScaleTargetRef
		minReplicas := int32(1)
		if hpa.Spec.MinReplicas != nil {
			minReplicas = *hpa.Spec.MinReplicas
		}

		wlKey := hpa.Namespace + "/" + ref.Kind + "/" + ref.Name
		cpuReqStr := "-"
		cpuLimStr := "-"
		reqLimRatio := 0.0
		currentReplicas := int(hpa.Status.CurrentReplicas)
		if wl, ok := wlPods[wlKey]; ok {
			cpuReqStr = formatCPU(wl.cpuReqPod)
			cpuLimStr = formatCPU(wl.cpuLimPod)
			if wl.cpuLimPod > 0 && wl.cpuReqPod > 0 {
				reqLimRatio = float64(wl.cpuReqPod) / float64(wl.cpuLimPod)
			}
		}

		severity := "warning"
		if issueType == "hpa-metrics-failed" {
			severity = "critical"
		}

		result = append(result, kedaIssue{
			Namespace:         hpa.Namespace,
			Name:              ref.Name,
			ScaledObject:      hpa.Name,
			CurrentReplicas:   currentReplicas,
			MinReplicas:       minReplicas,
			MaxReplicas:       hpa.Spec.MaxReplicas,
			IssueType:         issueType,
			Problems:          problems,
			CPURequestM:       cpuReqStr,
			CPULimitM:         cpuLimStr,
			RequestLimitRatio: math.Round(reqLimRatio*100) / 100,
			Severity:          severity,
			Impact:            strings.Join(problems, "; "),
		})
	}

	return result
}

// --- Detection: Bad PDBs ---

func (h *InefficiencyHandler) detectBadPDBs(ctx context.Context) []badPDBIssue {
	var pdbList policyv1.PodDisruptionBudgetList
	if err := h.client.List(ctx, &pdbList); err != nil {
		return []badPDBIssue{}
	}

	nodes := h.state.GetAllNodes()
	pods := h.state.GetAllPods()

	var result []badPDBIssue
	for i := range pdbList.Items {
		pdb := &pdbList.Items[i]

		reason := ""
		severity := "info"

		// Case 1: disruptionsAllowed == 0 (blocking)
		if pdb.Status.DisruptionsAllowed == 0 && pdb.Status.ExpectedPods > 0 {
			if pdb.Spec.MaxUnavailable != nil && pdb.Spec.MaxUnavailable.IntValue() == 0 {
				reason = "maxUnavailable=0 — no disruptions ever allowed"
				severity = "critical"
			} else if pdb.Spec.MinAvailable != nil {
				reason = fmt.Sprintf("minAvailable=%s with all pods at minimum", pdb.Spec.MinAvailable.String())
				severity = "critical"
			} else {
				reason = "0 disruptions allowed — pods at minimum healthy count"
				severity = "warning"
			}
		}

		// Case 2: PDB with no matching pods (orphaned)
		if pdb.Status.ExpectedPods == 0 {
			age := time.Since(pdb.CreationTimestamp.Time)
			if age > 7*24*time.Hour {
				reason = "no matching pods (orphaned PDB)"
				severity = "info"
			} else {
				continue // new PDB, pods may not be created yet
			}
		}

		// Case 3: PDB targeting single-replica deployment
		if pdb.Status.ExpectedPods == 1 && pdb.Status.DisruptionsAllowed == 0 {
			reason = "single-replica workload with PDB — blocks all eviction"
			severity = "critical"
		}

		if reason == "" {
			continue
		}

		// Count affected nodes
		affectedNodeSet := map[string]bool{}
		if pdb.Spec.Selector != nil {
			sel, err := metav1.LabelSelectorAsSelector(pdb.Spec.Selector)
			if err == nil {
				for _, p := range pods {
					if p.Namespace == pdb.Namespace && p.Pod != nil && sel.Matches(labels.Set(p.Pod.Labels)) {
						if p.NodeName != "" {
							affectedNodeSet[p.NodeName] = true
						}
					}
				}
			}
		}

		// Also count nodes that can't scale down because of PDB
		for _, n := range nodes {
			for _, pod := range n.Pods {
				if pod.Namespace != pdb.Namespace {
					continue
				}
				if pdb.Spec.Selector == nil {
					continue
				}
				sel, err := metav1.LabelSelectorAsSelector(pdb.Spec.Selector)
				if err != nil {
					continue
				}
				if sel.Matches(labels.Set(pod.Labels)) {
					affectedNodeSet[n.Node.Name] = true
				}
			}
		}

		age := time.Since(pdb.CreationTimestamp.Time)
		ageDays := int(age.Hours() / 24)

		impact := fmt.Sprintf("%s — affects %d node(s), age %dd", reason, len(affectedNodeSet), ageDays)

		result = append(result, badPDBIssue{
			Name:               pdb.Name,
			Namespace:          pdb.Namespace,
			Reason:             reason,
			ExpectedPods:       pdb.Status.ExpectedPods,
			CurrentHealthy:     pdb.Status.CurrentHealthy,
			DisruptionsAllowed: pdb.Status.DisruptionsAllowed,
			AgeDays:            ageDays,
			AffectedNodes:      len(affectedNodeSet),
			Severity:           severity,
			Impact:             impact,
		})
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].Severity != result[j].Severity {
			return ineffSeverityRank(result[i].Severity) > ineffSeverityRank(result[j].Severity)
		}
		return result[i].AffectedNodes > result[j].AffectedNodes
	})

	if result == nil {
		return []badPDBIssue{}
	}
	return result
}

// --- Detection: Bad State Pods ---

func (h *InefficiencyHandler) detectBadStatePods() []badStatePodIssue {
	nodes := h.state.GetAllNodes()
	var result []badStatePodIssue

	for _, n := range nodes {
		for _, pod := range n.Pods {
			if IsSystemPod(pod) {
				continue
			}

			status := computePodStatus(pod)
			isBad := badStatusSet[status]

			highRestarts := false
			maxRestarts := int32(0)
			for _, cs := range pod.Status.ContainerStatuses {
				if cs.RestartCount > maxRestarts {
					maxRestarts = cs.RestartCount
				}
				if cs.RestartCount > 10 {
					highRestarts = true
				}
			}

			if !isBad && !highRestarts {
				continue
			}

			readiness := computeContainerReady(pod)

			// Check if this pod could block scale-down
			blocksScaleDown := false
			if unevictableReason(pod) != "" {
				blocksScaleDown = true
			}

			severity := "warning"
			if status == "CrashLoopBackOff" || status == "OOMKilled" {
				severity = "critical"
			}
			if maxRestarts > 50 {
				severity = "critical"
			}

			impact := fmt.Sprintf("%s — %d restarts", status, maxRestarts)
			if blocksScaleDown {
				impact += " (blocks scale-down)"
			}

			result = append(result, badStatePodIssue{
				Name:            pod.Name,
				Namespace:       pod.Namespace,
				NodeName:        n.Node.Name,
				Status:          status,
				Readiness:       readiness,
				Restarts:        maxRestarts,
				Age:             timeAgoStr(pod.CreationTimestamp.Time),
				BlocksScaleDown: blocksScaleDown,
				Severity:        severity,
				Impact:          impact,
			})
		}
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].Restarts > result[j].Restarts
	})

	if result == nil {
		return []badStatePodIssue{}
	}
	return result
}

// --- Detection: Node Fragmentation ---

func (h *InefficiencyHandler) detectNodeFragmentation() []nodeFragIssue {
	nodes := h.state.GetAllNodes()
	var result []nodeFragIssue

	for _, n := range nodes {
		if n.Node == nil || n.IsGPUNode {
			continue
		}
		// Skip nodes with very few pods (likely system or draining)
		if len(n.Pods) < 3 {
			continue
		}

		cpuAllocPct := n.CPURequestUtilization()
		memAllocPct := n.MemoryRequestUtilization()
		cpuUsagePct := n.CPUUtilization()
		memUsagePct := n.MemoryUtilization()

		// Flag nodes with significant gap between allocation and actual usage
		// This indicates over-provisioned workloads wasting node capacity
		cpuGap := cpuAllocPct - cpuUsagePct

		// Only flag if allocation is moderate+ but usage is very low
		if cpuAllocPct < 40 || cpuGap < 30 {
			continue
		}
		if cpuUsagePct > 30 {
			continue // node is actually being used
		}

		severity := "info"
		if cpuAllocPct > 70 && cpuUsagePct < 15 {
			severity = "warning"
		}
		if cpuAllocPct > 80 && cpuUsagePct < 10 {
			severity = "critical"
		}

		impact := fmt.Sprintf("%.0f%% CPU allocated but only %.0f%% used (%.0f%% gap) — %.0f%% memory allocated, %.0f%% used",
			cpuAllocPct, cpuUsagePct, cpuGap, memAllocPct, memUsagePct)

		result = append(result, nodeFragIssue{
			NodeName:     n.Node.Name,
			NodeGroup:    n.NodeGroupID,
			InstanceType: n.InstanceType,
			CPUAllocPct:  math.Round(cpuAllocPct*10) / 10,
			MemAllocPct:  math.Round(memAllocPct*10) / 10,
			CPUUsagePct:  math.Round(cpuUsagePct*10) / 10,
			MemUsagePct:  math.Round(memUsagePct*10) / 10,
			PodCount:     len(n.Pods),
			MonthlyCost:  n.HourlyCostUSD * cost.HoursPerMonth,
			Severity:     severity,
			Impact:       impact,
		})
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].Severity != result[j].Severity {
			return ineffSeverityRank(result[i].Severity) > ineffSeverityRank(result[j].Severity)
		}
		return (result[i].CPUAllocPct - result[i].CPUUsagePct) > (result[j].CPUAllocPct - result[j].CPUUsagePct)
	})

	if result == nil {
		return []nodeFragIssue{}
	}
	return result
}

// --- Detection: Bad Resource Ratio Workloads ---

// detectBadRatioWorkloads finds deployments whose CPU:memory request ratio
// significantly deviates from the node's CPU:memory ratio. For example, on a
// 1:13 ratio node (1 CPU : 13 GB), a workload requesting 1:4 wastes memory
// because CPU fills up before memory does. Only high-impact workloads are returned.
func (h *InefficiencyHandler) detectBadRatioWorkloads() []badRatioIssue {
	nodes := h.state.GetAllNodes()
	pods := h.state.GetAllPods()

	// Build per-node-group average ratio (GB per CPU core)
	type ngRatio struct {
		totalCPU  int64   // millicores
		totalMem  int64   // bytes
		ratio     float64 // GB per CPU core
		instances string
		ngID      string
		costPerNode float64 // monthly
	}
	ngRatios := make(map[string]*ngRatio)
	for _, n := range nodes {
		if n.Node == nil || n.CPUCapacity == 0 {
			continue
		}
		ngID := n.NodeGroupID
		if ngID == "" {
			ngID = n.InstanceType // fallback
		}
		if ngID == "" {
			continue
		}
		ng, ok := ngRatios[ngID]
		if !ok {
			ng = &ngRatio{ngID: ngID, instances: n.InstanceType}
			ngRatios[ngID] = ng
		}
		ng.totalCPU += n.CPUCapacity
		ng.totalMem += n.MemoryCapacity
		ng.costPerNode = n.HourlyCostUSD * cost.HoursPerMonth
	}
	for _, ng := range ngRatios {
		if ng.totalCPU > 0 {
			cpuCores := float64(ng.totalCPU) / 1000
			memGB := float64(ng.totalMem) / (1024 * 1024 * 1024)
			ng.ratio = memGB / cpuCores // GB per CPU core
		}
	}

	// Group pods by owner to aggregate workload-level requests
	type wlAgg struct {
		namespace string
		kind      string
		name      string
		replicas  int
		cpuReq    int64 // total millicores
		memReq    int64 // total bytes
		nodeGroup string
		instType  string
	}
	workloads := make(map[string]*wlAgg)
	for _, p := range pods {
		if p.Pod == nil || p.Pod.Status.Phase != corev1.PodRunning {
			continue
		}
		ownerKind, ownerName := resolveOwner(p)
		if ownerKind == "Pod" {
			continue // skip standalone pods
		}
		key := p.Namespace + "/" + ownerKind + "/" + ownerName
		wl, ok := workloads[key]
		if !ok {
			// Determine which node group this workload's pods run on
			ngID := ""
			instType := ""
			if ns, ok2 := findNodeForPod(nodes, p.NodeName); ok2 {
				ngID = ns.NodeGroupID
				if ngID == "" {
					ngID = ns.InstanceType
				}
				instType = ns.InstanceType
			}
			wl = &wlAgg{
				namespace: p.Namespace,
				kind:      ownerKind,
				name:      ownerName,
				nodeGroup: ngID,
				instType:  instType,
			}
			workloads[key] = wl
		}
		wl.replicas++
		wl.cpuReq += p.CPURequest
		wl.memReq += p.MemoryRequest
	}

	var result []badRatioIssue

	for _, wl := range workloads {
		if wl.replicas < 2 || wl.cpuReq == 0 || wl.memReq == 0 {
			continue
		}
		if wl.nodeGroup == "" {
			continue
		}

		ng, ok := ngRatios[wl.nodeGroup]
		if !ok || ng.ratio == 0 {
			continue
		}

		// Calculate per-pod ratio
		cpuPerPod := float64(wl.cpuReq) / float64(wl.replicas) / 1000 // cores
		memPerPod := float64(wl.memReq) / float64(wl.replicas) / (1024 * 1024 * 1024) // GB
		if cpuPerPod == 0 {
			continue
		}

		wlRatio := memPerPod / cpuPerPod // GB per CPU core

		// Calculate deviation percentage from node ratio
		deviation := math.Abs(wlRatio-ng.ratio) / ng.ratio * 100

		// Only flag significant deviations (>50% off from node ratio)
		if deviation < 50 {
			continue
		}

		// Calculate wasted resources
		// If workload has lower mem:cpu ratio than node, it's wasting memory on the node
		// If workload has higher mem:cpu ratio than node, it's wasting CPU on the node
		var wastedMemGB, wastedCPUCores float64
		totalCPUCores := float64(wl.cpuReq) / 1000
		totalMemGB := float64(wl.memReq) / (1024 * 1024 * 1024)

		if wlRatio < ng.ratio {
			// Workload is CPU-heavy relative to node: wastes memory
			// Ideal mem for this CPU at node ratio = cpuCores * nodeRatio
			idealMem := totalCPUCores * ng.ratio
			wastedMemGB = idealMem - totalMemGB
			if wastedMemGB < 0 {
				wastedMemGB = 0
			}
		} else {
			// Workload is memory-heavy relative to node: wastes CPU
			// Ideal CPU for this mem at node ratio = memGB / nodeRatio
			idealCPU := totalMemGB / ng.ratio
			wastedCPUCores = idealCPU - totalCPUCores
			if wastedCPUCores < 0 {
				wastedCPUCores = 0
			}
		}

		// Estimate wasted cost (proportional to node cost)
		wastedFraction := 0.0
		if wastedMemGB > 0 && ng.totalMem > 0 {
			totalNodeMemGB := float64(ng.totalMem) / (1024 * 1024 * 1024)
			wastedFraction = wastedMemGB / totalNodeMemGB
		}
		if wastedCPUCores > 0 && ng.totalCPU > 0 {
			totalNodeCPUCores := float64(ng.totalCPU) / 1000
			wastedFraction = wastedCPUCores / totalNodeCPUCores
		}
		wastedCost := ng.costPerNode * wastedFraction * float64(len(nodes)) // rough estimate

		// Filter out low-impact workloads
		// Only show if wasted > 1 GB memory or > 0.5 CPU cores
		if wastedMemGB < 1 && wastedCPUCores < 0.5 {
			continue
		}

		severity := "info"
		if deviation > 100 && (wastedMemGB > 4 || wastedCPUCores > 2) {
			severity = "warning"
		}
		if deviation > 200 && (wastedMemGB > 10 || wastedCPUCores > 4) {
			severity = "critical"
		}

		dimLabel := "memory"
		if wlRatio > ng.ratio {
			dimLabel = "CPU"
		}
		impact := fmt.Sprintf("Workload ratio %.1f GB/CPU vs node ratio %.1f GB/CPU (%.0f%% off) — wastes %s on the node group",
			wlRatio, ng.ratio, deviation, dimLabel)

		result = append(result, badRatioIssue{
			Namespace:      wl.namespace,
			Name:           wl.name,
			Kind:           wl.kind,
			Replicas:       wl.replicas,
			CPURequestPod:  formatCPU(wl.cpuReq / int64(wl.replicas)),
			MemRequestPod:  formatMem(wl.memReq / int64(wl.replicas)),
			WorkloadRatio:  math.Round(wlRatio*10) / 10,
			NodeRatio:      math.Round(ng.ratio*10) / 10,
			RatioDeviation: math.Round(deviation*10) / 10,
			TotalCPUReq:    wl.cpuReq,
			TotalMemReq:    wl.memReq,
			WastedMemGB:    math.Round(wastedMemGB*10) / 10,
			WastedCPUCores: math.Round(wastedCPUCores*10) / 10,
			WastedCostUSD:  math.Round(wastedCost*100) / 100,
			NodeGroup:      wl.nodeGroup,
			InstanceType:   wl.instType,
			Severity:       severity,
			Impact:         impact,
		})
	}

	// Sort by wasted cost descending — show high-impact first, not long tail
	sort.Slice(result, func(i, j int) bool {
		return result[i].WastedCostUSD > result[j].WastedCostUSD
	})

	// Cap at top 50 to avoid long tail
	if len(result) > 50 {
		result = result[:50]
	}

	if result == nil {
		return []badRatioIssue{}
	}
	return result
}

// --- Detection: Network Hogs ---

// detectNetworkHogs finds workloads with the highest cumulative network I/O.
// Network bytes come from the kubelet /stats/summary API (cumulative since pod start).
// Only workloads above the top-N threshold are returned to filter out the long tail.
func (h *InefficiencyHandler) detectNetworkHogs() []networkHogIssue {
	pods := h.state.GetAllPods()

	const gb = 1024 * 1024 * 1024

	// Group pods by owner workload
	type wlNet struct {
		namespace string
		kind      string
		name      string
		replicas  int
		rxBytes   int64
		txBytes   int64
	}
	workloads := make(map[string]*wlNet)

	for _, p := range pods {
		if p.Pod == nil || p.Pod.Status.Phase != "Running" {
			continue
		}
		if p.NetworkRxBytes == 0 && p.NetworkTxBytes == 0 {
			continue
		}
		ownerKind, ownerName := resolveOwner(p)
		key := p.Namespace + "/" + ownerKind + "/" + ownerName
		wl, ok := workloads[key]
		if !ok {
			wl = &wlNet{
				namespace: p.Namespace,
				kind:      ownerKind,
				name:      ownerName,
			}
			workloads[key] = wl
		}
		wl.replicas++
		wl.rxBytes += p.NetworkRxBytes
		wl.txBytes += p.NetworkTxBytes
	}

	var result []networkHogIssue
	for _, wl := range workloads {
		totalBytes := wl.rxBytes + wl.txBytes
		// Only include workloads with > 1 GB total network I/O
		if totalBytes < gb {
			continue
		}

		totalRxGB := float64(wl.rxBytes) / float64(gb)
		totalTxGB := float64(wl.txBytes) / float64(gb)
		perPodRxGB := totalRxGB / float64(wl.replicas)
		perPodTxGB := totalTxGB / float64(wl.replicas)

		severity := "info"
		totalGB := totalRxGB + totalTxGB
		if totalGB > 100 {
			severity = "critical"
		} else if totalGB > 10 {
			severity = "warning"
		}

		impact := fmt.Sprintf("%d replicas, %.1f GB rx + %.1f GB tx total (%.1f GB/pod rx, %.1f GB/pod tx)",
			wl.replicas, totalRxGB, totalTxGB, perPodRxGB, perPodTxGB)

		result = append(result, networkHogIssue{
			Namespace:  wl.namespace,
			Name:       wl.name,
			Kind:       wl.kind,
			Replicas:   wl.replicas,
			TotalRxGB:  math.Round(totalRxGB*100) / 100,
			TotalTxGB:  math.Round(totalTxGB*100) / 100,
			TotalBytes: totalBytes,
			PerPodRxGB: math.Round(perPodRxGB*100) / 100,
			PerPodTxGB: math.Round(perPodTxGB*100) / 100,
			Severity:   severity,
			Impact:     impact,
		})
	}

	// Sort by total bytes descending — biggest talkers first
	sort.Slice(result, func(i, j int) bool {
		return result[i].TotalBytes > result[j].TotalBytes
	})

	// Cap at top 50
	if len(result) > 50 {
		result = result[:50]
	}

	if result == nil {
		return []networkHogIssue{}
	}
	return result
}

// findNodeForPod returns the NodeState for a given node name.
func findNodeForPod(nodes []*state.NodeState, nodeName string) (*state.NodeState, bool) {
	for _, n := range nodes {
		if n.Node != nil && n.Node.Name == nodeName {
			return n, true
		}
	}
	return nil, false
}

// --- Helpers ---

func ineffSeverityRank(s string) int {
	switch s {
	case "critical":
		return 3
	case "warning":
		return 2
	case "info":
		return 1
	default:
		return 0
	}
}

func maxSeverity[T any](items []T, getSev func(T) string) string {
	best := "info"
	for _, item := range items {
		s := getSev(item)
		if ineffSeverityRank(s) > ineffSeverityRank(best) {
			best = s
		}
	}
	if len(items) == 0 {
		return ""
	}
	return best
}

// kedaWlPodInfo aggregates per-workload pod info for KEDA/HPA analysis.
type kedaWlPodInfo struct {
	replicas  int
	cpuReqPod int64
	cpuLimPod int64
}

// ineffToInt32 converts an unstructured value (int64 or float64) to int32.
func ineffToInt32(v interface{}) int32 {
	switch n := v.(type) {
	case int64:
		return int32(n)
	case float64:
		return int32(n)
	}
	return 0
}

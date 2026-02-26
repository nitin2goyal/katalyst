package scheduler

import (
	"strconv"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Simulator performs scheduling "what-if" simulations.
type Simulator struct{}

func NewSimulator() *Simulator {
	return &Simulator{}
}

// SimulationResult contains the result of a scheduling simulation.
type SimulationResult struct {
	Feasible bool
	NodeName string
	Reason   string
}

// CanSchedule checks if a pod can be scheduled on a given node.
// existingPods are the pods already on this specific node.
func (s *Simulator) CanSchedule(pod *corev1.Pod, node *corev1.Node, existingPods []*corev1.Pod) SimulationResult {
	return s.canScheduleWithTopology(pod, node, existingPods, nil, nil)
}

// CanScheduleWithTopology is like CanSchedule but accepts allNodes and podsByNode
// for cross-node topology checks (zone-level anti-affinity, topology spread).
func (s *Simulator) CanScheduleWithTopology(pod *corev1.Pod, node *corev1.Node, existingPods []*corev1.Pod, allNodes []*corev1.Node, podsByNode map[string][]*corev1.Pod) SimulationResult {
	return s.canScheduleWithTopology(pod, node, existingPods, allNodes, podsByNode)
}

func (s *Simulator) canScheduleWithTopology(pod *corev1.Pod, node *corev1.Node, existingPods []*corev1.Pod, allNodes []*corev1.Node, podsByNode map[string][]*corev1.Pod) SimulationResult {
	// Check node conditions
	for _, cond := range node.Status.Conditions {
		if cond.Type == corev1.NodeReady && cond.Status != corev1.ConditionTrue {
			return SimulationResult{Feasible: false, Reason: "node not ready"}
		}
	}

	// Check unschedulable
	if node.Spec.Unschedulable {
		return SimulationResult{Feasible: false, Reason: "node is cordoned"}
	}

	// Check taints/tolerations
	if !toleratesTaints(pod, node.Spec.Taints) {
		return SimulationResult{Feasible: false, Reason: "pod does not tolerate node taints"}
	}

	// Check resource capacity (including init containers)
	if !hasEnoughResources(pod, node, existingPods) {
		return SimulationResult{Feasible: false, Reason: "insufficient resources"}
	}

	// Check node selector
	if !matchesNodeSelector(pod, node) {
		return SimulationResult{Feasible: false, Reason: "node selector mismatch"}
	}

	// Check node affinity
	if !matchesNodeAffinity(pod, node) {
		return SimulationResult{Feasible: false, Reason: "node affinity mismatch"}
	}

	// Check pod affinity (positive) — required co-location constraints
	if !satisfiesPodAffinity(pod, node, existingPods, allNodes, podsByNode) {
		return SimulationResult{Feasible: false, Reason: "pod affinity not satisfied"}
	}

	// Check pod anti-affinity (with cross-node topology support when available)
	if !satisfiesPodAntiAffinity(pod, node, existingPods, allNodes, podsByNode) {
		return SimulationResult{Feasible: false, Reason: "pod anti-affinity conflict"}
	}

	// Check topology spread constraints (with cross-node topology support when available)
	if !satisfiesTopologySpreadConstraints(pod, node, existingPods, allNodes, podsByNode) {
		return SimulationResult{Feasible: false, Reason: "topology spread constraint violation"}
	}

	return SimulationResult{Feasible: true, NodeName: node.Name}
}

// FindFittingNodes returns all nodes that can schedule the given pod.
// Uses CanScheduleWithTopology when nodes list is available for zone-level checks.
func (s *Simulator) FindFittingNodes(pod *corev1.Pod, nodes []*corev1.Node, podsByNode map[string][]*corev1.Pod) []string {
	var fitting []string
	for _, node := range nodes {
		result := s.CanScheduleWithTopology(pod, node, podsByNode[node.Name], nodes, podsByNode)
		if result.Feasible {
			fitting = append(fitting, node.Name)
		}
	}
	return fitting
}

// CountUnschedulable counts how many pending pods can't be scheduled.
func (s *Simulator) CountUnschedulable(pendingPods []*corev1.Pod, nodes []*corev1.Node, podsByNode map[string][]*corev1.Pod) int {
	count := 0
	for _, pod := range pendingPods {
		fitting := s.FindFittingNodes(pod, nodes, podsByNode)
		if len(fitting) == 0 {
			count++
		}
	}
	return count
}

func toleratesTaints(pod *corev1.Pod, taints []corev1.Taint) bool {
	for _, taint := range taints {
		if taint.Effect == corev1.TaintEffectNoSchedule || taint.Effect == corev1.TaintEffectNoExecute {
			tolerated := false
			for _, toleration := range pod.Spec.Tolerations {
				if tolerationMatchesTaint(toleration, taint) {
					tolerated = true
					break
				}
			}
			if !tolerated {
				return false
			}
		}
	}
	return true
}

func tolerationMatchesTaint(toleration corev1.Toleration, taint corev1.Taint) bool {
	if toleration.Operator == corev1.TolerationOpExists && toleration.Key == "" {
		return true
	}
	if toleration.Key != taint.Key {
		return false
	}
	// Empty Effect on toleration matches all effects
	if toleration.Effect != "" && toleration.Effect != taint.Effect {
		return false
	}
	if toleration.Operator == corev1.TolerationOpExists {
		return true
	}
	return toleration.Value == taint.Value
}

// effectivePodResources computes the effective resource request for a pod,
// accounting for init containers: max(max(initContainers), sum(containers)).
func effectivePodResources(pod *corev1.Pod) (cpuMilli, memBytes int64) {
	for _, c := range pod.Spec.Containers {
		if cpu, ok := c.Resources.Requests[corev1.ResourceCPU]; ok {
			cpuMilli += cpu.MilliValue()
		}
		if mem, ok := c.Resources.Requests[corev1.ResourceMemory]; ok {
			memBytes += mem.Value()
		}
	}
	for _, ic := range pod.Spec.InitContainers {
		var icCPU, icMem int64
		if cpu, ok := ic.Resources.Requests[corev1.ResourceCPU]; ok {
			icCPU = cpu.MilliValue()
		}
		if mem, ok := ic.Resources.Requests[corev1.ResourceMemory]; ok {
			icMem = mem.Value()
		}
		if icCPU > cpuMilli {
			cpuMilli = icCPU
		}
		if icMem > memBytes {
			memBytes = icMem
		}
	}
	return
}

func hasEnoughResources(pod *corev1.Pod, node *corev1.Node, existingPods []*corev1.Pod) bool {
	// Calculate available resources using Allocatable
	cpuCap := node.Status.Allocatable.Cpu().MilliValue()
	memCap := node.Status.Allocatable.Memory().Value()

	usedCPU := int64(0)
	usedMem := int64(0)
	usedGPU := int64(0)
	for _, p := range existingPods {
		pCPU, pMem := effectivePodResources(p)
		usedCPU += pCPU
		usedMem += pMem
		usedGPU += podGPURequest(p)
	}

	// Calculate pod resource requirements (including init containers)
	podCPU, podMem := effectivePodResources(pod)
	podGPU := podGPURequest(pod)

	if usedCPU+podCPU > cpuCap || usedMem+podMem > memCap {
		return false
	}

	// Check GPU resources (nvidia.com/gpu)
	if podGPU > 0 {
		gpuRes := corev1.ResourceName("nvidia.com/gpu")
		gpuCap := node.Status.Allocatable[gpuRes]
		if usedGPU+podGPU > gpuCap.Value() {
			return false
		}
	}

	return true
}

// podGPURequest returns the total nvidia.com/gpu request for a pod.
func podGPURequest(pod *corev1.Pod) int64 {
	gpuRes := corev1.ResourceName("nvidia.com/gpu")
	total := int64(0)
	for _, c := range pod.Spec.Containers {
		if qty, ok := c.Resources.Requests[gpuRes]; ok {
			total += qty.Value()
		}
	}
	// Init containers: take max (they run sequentially)
	for _, ic := range pod.Spec.InitContainers {
		if qty, ok := ic.Resources.Requests[gpuRes]; ok {
			if qty.Value() > total {
				total = qty.Value()
			}
		}
	}
	return total
}

func matchesNodeSelector(pod *corev1.Pod, node *corev1.Node) bool {
	if pod.Spec.NodeSelector == nil {
		return true
	}
	for k, v := range pod.Spec.NodeSelector {
		if nodeVal, ok := node.Labels[k]; !ok || nodeVal != v {
			return false
		}
	}
	return true
}

// matchesNodeAffinity checks requiredDuringSchedulingIgnoredDuringExecution terms.
func matchesNodeAffinity(pod *corev1.Pod, node *corev1.Node) bool {
	if pod.Spec.Affinity == nil || pod.Spec.Affinity.NodeAffinity == nil {
		return true
	}
	required := pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
	if required == nil {
		return true
	}
	// At least one NodeSelectorTerm must match (terms are ORed).
	for _, term := range required.NodeSelectorTerms {
		if matchesNodeSelectorTerm(term, node) {
			return true
		}
	}
	return false
}

func matchesNodeSelectorTerm(term corev1.NodeSelectorTerm, node *corev1.Node) bool {
	// All match expressions must match (ANDed).
	for _, expr := range term.MatchExpressions {
		if !matchesNodeSelectorRequirement(expr, node.Labels) {
			return false
		}
	}
	// All match fields must match (ANDed).
	for _, expr := range term.MatchFields {
		if !matchesNodeSelectorFieldRequirement(expr, node) {
			return false
		}
	}
	return true
}

func matchesNodeSelectorRequirement(req corev1.NodeSelectorRequirement, labels map[string]string) bool {
	val, exists := labels[req.Key]
	switch req.Operator {
	case corev1.NodeSelectorOpIn:
		if !exists {
			return false
		}
		for _, v := range req.Values {
			if v == val {
				return true
			}
		}
		return false
	case corev1.NodeSelectorOpNotIn:
		if !exists {
			return true
		}
		for _, v := range req.Values {
			if v == val {
				return false
			}
		}
		return true
	case corev1.NodeSelectorOpExists:
		return exists
	case corev1.NodeSelectorOpDoesNotExist:
		return !exists
	case corev1.NodeSelectorOpGt:
		if !exists || len(req.Values) == 0 {
			return false
		}
		// Kubernetes compares Gt/Lt as integers (used for GPU counts, etc.)
		valInt, err1 := strconv.Atoi(val)
		reqInt, err2 := strconv.Atoi(req.Values[0])
		if err1 != nil || err2 != nil {
			return val > req.Values[0] // Fallback to string comparison
		}
		return valInt > reqInt
	case corev1.NodeSelectorOpLt:
		if !exists || len(req.Values) == 0 {
			return false
		}
		valInt, err1 := strconv.Atoi(val)
		reqInt, err2 := strconv.Atoi(req.Values[0])
		if err1 != nil || err2 != nil {
			return val < req.Values[0]
		}
		return valInt < reqInt
	default:
		return false
	}
}

func matchesNodeSelectorFieldRequirement(req corev1.NodeSelectorRequirement, node *corev1.Node) bool {
	var val string
	switch req.Key {
	case "metadata.name":
		val = node.Name
	default:
		return true // Unknown field, allow by default
	}
	switch req.Operator {
	case corev1.NodeSelectorOpIn:
		for _, v := range req.Values {
			if v == val {
				return true
			}
		}
		return false
	case corev1.NodeSelectorOpNotIn:
		for _, v := range req.Values {
			if v == val {
				return false
			}
		}
		return true
	default:
		return true
	}
}

// satisfiesPodAffinity checks that placing pod on node satisfies
// requiredDuringSchedulingIgnoredDuringExecution pod affinity rules.
// Each required term demands that at least one matching pod exists in the same topology domain.
func satisfiesPodAffinity(pod *corev1.Pod, node *corev1.Node, existingPods []*corev1.Pod, allNodes []*corev1.Node, podsByNode map[string][]*corev1.Pod) bool {
	if pod.Spec.Affinity == nil || pod.Spec.Affinity.PodAffinity == nil {
		return true
	}
	required := pod.Spec.Affinity.PodAffinity.RequiredDuringSchedulingIgnoredDuringExecution
	if len(required) == 0 {
		return true
	}
	for _, term := range required {
		topologyKey := term.TopologyKey
		if topologyKey == "" {
			continue
		}
		nodeTopologyValue, ok := node.Labels[topologyKey]
		if !ok {
			return false // Node lacks the topology key — can't satisfy affinity
		}

		// Collect all pods in the same topology domain
		var podsInDomain []*corev1.Pod
		if allNodes != nil && podsByNode != nil {
			for _, n := range allNodes {
				if nv, ok := n.Labels[topologyKey]; ok && nv == nodeTopologyValue {
					podsInDomain = append(podsInDomain, podsByNode[n.Name]...)
				}
			}
		} else {
			podsInDomain = existingPods
		}

		// At least one existing pod in the domain must match the affinity term
		found := false
		for _, ep := range podsInDomain {
			if podMatchesAffinityTerm(ep, term) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// satisfiesPodAntiAffinity checks that placing pod on node doesn't violate
// requiredDuringSchedulingIgnoredDuringExecution anti-affinity rules.
// When allNodes and podsByNode are provided, zone-level anti-affinity is checked
// across all nodes in the same topology domain.
func satisfiesPodAntiAffinity(pod *corev1.Pod, node *corev1.Node, existingPods []*corev1.Pod, allNodes []*corev1.Node, podsByNode map[string][]*corev1.Pod) bool {
	if pod.Spec.Affinity == nil || pod.Spec.Affinity.PodAntiAffinity == nil {
		return true
	}
	required := pod.Spec.Affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution
	if len(required) == 0 {
		return true
	}
	for _, term := range required {
		topologyKey := term.TopologyKey
		if topologyKey == "" {
			continue
		}
		nodeTopologyValue, ok := node.Labels[topologyKey]
		if !ok {
			continue
		}

		// Collect all pods in the same topology domain
		var podsInDomain []*corev1.Pod
		if allNodes != nil && podsByNode != nil {
			for _, n := range allNodes {
				if nv, ok := n.Labels[topologyKey]; ok && nv == nodeTopologyValue {
					podsInDomain = append(podsInDomain, podsByNode[n.Name]...)
				}
			}
		} else {
			// Fallback: only check pods on the target node (hostname-level anti-affinity)
			podsInDomain = existingPods
		}

		for _, ep := range podsInDomain {
			if podMatchesAffinityTerm(ep, term) {
				return false
			}
		}
	}
	return true
}

func podMatchesAffinityTerm(pod *corev1.Pod, term corev1.PodAffinityTerm) bool {
	if term.LabelSelector == nil {
		return false
	}
	// Check namespace match: explicit Namespaces list takes priority
	if len(term.Namespaces) > 0 {
		nsMatch := false
		for _, ns := range term.Namespaces {
			if ns == pod.Namespace {
				nsMatch = true
				break
			}
		}
		if !nsMatch {
			return false
		}
	} else if term.NamespaceSelector != nil {
		// NamespaceSelector (K8s 1.24+): match based on namespace labels.
		// We don't have namespace objects here, so treat a non-nil NamespaceSelector
		// with empty MatchLabels/MatchExpressions as "all namespaces" (K8s spec).
		// Non-empty selectors are conservatively allowed (we lack namespace labels).
		if len(term.NamespaceSelector.MatchLabels) == 0 && len(term.NamespaceSelector.MatchExpressions) == 0 {
			// Matches all namespaces — no filter needed
		}
		// For non-empty selectors, we'd need namespace label data which isn't
		// available in the simulator. Conservatively allow the match.
	}
	// Check label selector match
	for _, expr := range term.LabelSelector.MatchExpressions {
		val, exists := pod.Labels[expr.Key]
		switch expr.Operator {
		case metav1.LabelSelectorOpIn:
			found := false
			for _, v := range expr.Values {
				if v == val {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		case metav1.LabelSelectorOpNotIn:
			if exists {
				for _, v := range expr.Values {
					if v == val {
						return false
					}
				}
			}
		case metav1.LabelSelectorOpExists:
			if !exists {
				return false
			}
		case metav1.LabelSelectorOpDoesNotExist:
			if exists {
				return false
			}
		}
	}
	for k, v := range term.LabelSelector.MatchLabels {
		if pod.Labels[k] != v {
			return false
		}
	}
	return true
}

// satisfiesTopologySpreadConstraints checks the pod's topology spread constraints.
// When allNodes and podsByNode are provided, it computes actual cross-domain skew.
// Otherwise falls back to a conservative per-node check.
func satisfiesTopologySpreadConstraints(pod *corev1.Pod, node *corev1.Node, existingPods []*corev1.Pod, allNodes []*corev1.Node, podsByNode map[string][]*corev1.Pod) bool {
	constraints := pod.Spec.TopologySpreadConstraints
	if len(constraints) == 0 {
		return true
	}
	for _, constraint := range constraints {
		if constraint.WhenUnsatisfiable != corev1.DoNotSchedule {
			continue // Only enforce DoNotSchedule constraints
		}
		topologyKey := constraint.TopologyKey
		if topologyKey == "" {
			continue
		}
		nodeTopologyValue, ok := node.Labels[topologyKey]
		if !ok {
			continue
		}

		if allNodes != nil && podsByNode != nil {
			// Full cross-domain skew check
			domainCounts := make(map[string]int32)
			for _, n := range allNodes {
				dv, ok := n.Labels[topologyKey]
				if !ok {
					continue
				}
				if _, exists := domainCounts[dv]; !exists {
					domainCounts[dv] = 0
				}
				for _, ep := range podsByNode[n.Name] {
					if podMatchesLabelSelector(ep, constraint.LabelSelector) {
						domainCounts[dv]++
					}
				}
			}
			// Simulate adding this pod to the target domain
			domainCounts[nodeTopologyValue]++
			// Compute skew: max - min across all domains
			var minCount, maxCount int32
			first := true
			for _, count := range domainCounts {
				if first {
					minCount = count
					maxCount = count
					first = false
				} else {
					if count < minCount {
						minCount = count
					}
					if count > maxCount {
						maxCount = count
					}
				}
			}
			if maxCount-minCount > constraint.MaxSkew {
				return false
			}
		} else {
			// Fallback: conservative per-node check
			matchCount := int32(0)
			for _, ep := range existingPods {
				if podMatchesLabelSelector(ep, constraint.LabelSelector) {
					matchCount++
				}
			}
			if matchCount+1 > constraint.MaxSkew+1 {
				return false
			}
		}
	}
	return true
}

func podMatchesLabelSelector(pod *corev1.Pod, selector *metav1.LabelSelector) bool {
	if selector == nil {
		return true
	}
	for k, v := range selector.MatchLabels {
		if pod.Labels[k] != v {
			return false
		}
	}
	for _, expr := range selector.MatchExpressions {
		val, exists := pod.Labels[expr.Key]
		switch expr.Operator {
		case metav1.LabelSelectorOpIn:
			if !exists {
				return false
			}
			found := false
			for _, v := range expr.Values {
				if v == val {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		case metav1.LabelSelectorOpNotIn:
			if exists {
				for _, v := range expr.Values {
					if v == val {
						return false
					}
				}
			}
		case metav1.LabelSelectorOpExists:
			if !exists {
				return false
			}
		case metav1.LabelSelectorOpDoesNotExist:
			if exists {
				return false
			}
		}
	}
	return true
}

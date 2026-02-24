package scheduler

import (
	corev1 "k8s.io/api/core/v1"
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
func (s *Simulator) CanSchedule(pod *corev1.Pod, node *corev1.Node, existingPods []*corev1.Pod) SimulationResult {
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

	// Check resource capacity
	if !hasEnoughResources(pod, node, existingPods) {
		return SimulationResult{Feasible: false, Reason: "insufficient resources"}
	}

	// Check node selector
	if !matchesNodeSelector(pod, node) {
		return SimulationResult{Feasible: false, Reason: "node selector mismatch"}
	}

	return SimulationResult{Feasible: true, NodeName: node.Name}
}

// FindFittingNodes returns all nodes that can schedule the given pod.
func (s *Simulator) FindFittingNodes(pod *corev1.Pod, nodes []*corev1.Node, podsByNode map[string][]*corev1.Pod) []string {
	var fitting []string
	for _, node := range nodes {
		result := s.CanSchedule(pod, node, podsByNode[node.Name])
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
	if toleration.Operator == corev1.TolerationOpExists {
		return true
	}
	return toleration.Value == taint.Value && toleration.Effect == taint.Effect
}

func hasEnoughResources(pod *corev1.Pod, node *corev1.Node, existingPods []*corev1.Pod) bool {
	// Calculate available resources
	cpuCap := node.Status.Allocatable.Cpu().MilliValue()
	memCap := node.Status.Allocatable.Memory().Value()

	usedCPU := int64(0)
	usedMem := int64(0)
	for _, p := range existingPods {
		for _, c := range p.Spec.Containers {
			if cpu, ok := c.Resources.Requests[corev1.ResourceCPU]; ok {
				usedCPU += cpu.MilliValue()
			}
			if mem, ok := c.Resources.Requests[corev1.ResourceMemory]; ok {
				usedMem += mem.Value()
			}
		}
	}

	// Calculate pod resource requirements
	podCPU := int64(0)
	podMem := int64(0)
	for _, c := range pod.Spec.Containers {
		if cpu, ok := c.Resources.Requests[corev1.ResourceCPU]; ok {
			podCPU += cpu.MilliValue()
		}
		if mem, ok := c.Resources.Requests[corev1.ResourceMemory]; ok {
			podMem += mem.Value()
		}
	}

	return (usedCPU+podCPU <= cpuCap) && (usedMem+podMem <= memCap)
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

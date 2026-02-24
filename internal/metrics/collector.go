package metrics

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	pkgmetrics "github.com/koptimizer/koptimizer/pkg/metrics"
)

// Collector implements MetricsCollector using K8s Metrics API.
type Collector struct {
	client client.Client
}

// NewCollector creates a new metrics Collector.
func NewCollector(c client.Client) *Collector {
	return &Collector{client: c}
}

func (c *Collector) GetNodeMetrics(ctx context.Context) ([]pkgmetrics.NodeMetrics, error) {
	metricsList := &metricsv1beta1.NodeMetricsList{}
	if err := c.client.List(ctx, metricsList); err != nil {
		return nil, fmt.Errorf("listing node metrics: %w", err)
	}

	result := make([]pkgmetrics.NodeMetrics, 0, len(metricsList.Items))
	for _, m := range metricsList.Items {
		result = append(result, pkgmetrics.NodeMetrics{
			Name:        m.Name,
			CPUUsage:    m.Usage.Cpu().MilliValue(),
			MemoryUsage: m.Usage.Memory().Value(),
			Timestamp:   m.Timestamp.Time,
		})
	}
	return result, nil
}

func (c *Collector) GetPodMetrics(ctx context.Context, namespace string) ([]pkgmetrics.PodMetrics, error) {
	metricsList := &metricsv1beta1.PodMetricsList{}
	opts := []client.ListOption{}
	if namespace != "" {
		opts = append(opts, client.InNamespace(namespace))
	}
	if err := c.client.List(ctx, metricsList, opts...); err != nil {
		return nil, fmt.Errorf("listing pod metrics: %w", err)
	}

	result := make([]pkgmetrics.PodMetrics, 0, len(metricsList.Items))
	for _, m := range metricsList.Items {
		pm := pkgmetrics.PodMetrics{
			Name:      m.Name,
			Namespace: m.Namespace,
			Timestamp: m.Timestamp.Time,
		}
		for _, c := range m.Containers {
			pm.Containers = append(pm.Containers, pkgmetrics.ContainerMetrics{
				Name:        c.Name,
				CPUUsage:    c.Usage.Cpu().MilliValue(),
				MemoryUsage: c.Usage.Memory().Value(),
			})
		}
		result = append(result, pm)
	}
	return result, nil
}

func (c *Collector) GetNodeMetricsByName(ctx context.Context, name string) (*pkgmetrics.NodeMetrics, error) {
	m := &metricsv1beta1.NodeMetrics{}
	if err := c.client.Get(ctx, types.NamespacedName{Name: name}, m); err != nil {
		return nil, fmt.Errorf("getting node metrics for %s: %w", name, err)
	}
	return &pkgmetrics.NodeMetrics{
		Name:        m.Name,
		CPUUsage:    m.Usage.Cpu().MilliValue(),
		MemoryUsage: m.Usage.Memory().Value(),
		Timestamp:   m.Timestamp.Time,
	}, nil
}

func (c *Collector) GetPodMetricsByName(ctx context.Context, namespace, name string) (*pkgmetrics.PodMetrics, error) {
	m := &metricsv1beta1.PodMetrics{}
	if err := c.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, m); err != nil {
		return nil, fmt.Errorf("getting pod metrics for %s/%s: %w", namespace, name, err)
	}
	pm := &pkgmetrics.PodMetrics{
		Name:      m.Name,
		Namespace: m.Namespace,
		Timestamp: m.Timestamp.Time,
	}
	for _, c := range m.Containers {
		pm.Containers = append(pm.Containers, pkgmetrics.ContainerMetrics{
			Name:        c.Name,
			CPUUsage:    c.Usage.Cpu().MilliValue(),
			MemoryUsage: c.Usage.Memory().Value(),
		})
	}
	return pm, nil
}

func (c *Collector) GetGPUMetrics(ctx context.Context, nodeName string) (*pkgmetrics.GPUMetrics, error) {
	node := &corev1.Node{}
	if err := c.client.Get(ctx, types.NamespacedName{Name: nodeName}, node); err != nil {
		return nil, fmt.Errorf("getting node %s: %w", nodeName, err)
	}

	// Determine total GPU capacity from node status.
	gpuCapacity := int64(0)
	if q, ok := node.Status.Capacity["nvidia.com/gpu"]; ok {
		gpuCapacity = q.Value()
	}

	// Count GPUs requested by pods running on this node.
	gpuRequested := int64(0)
	podList := &corev1.PodList{}
	if err := c.client.List(ctx, podList, client.MatchingFields{"spec.nodeName": nodeName}); err == nil {
		for i := range podList.Items {
			pod := &podList.Items[i]
			if pod.Status.Phase != corev1.PodRunning {
				continue
			}
			for _, container := range pod.Spec.Containers {
				if q, ok := container.Resources.Requests["nvidia.com/gpu"]; ok {
					gpuRequested += q.Value()
				}
			}
		}
	}

	// Build per-device metrics from allocation data.
	// Actual utilization requires DCGM exporter + Prometheus; here we estimate
	// from allocation: each requested GPU is considered "in use".
	var gpuDevices []pkgmetrics.GPUDeviceMetrics
	for i := int64(0); i < gpuCapacity; i++ {
		util := float64(0)
		if i < gpuRequested {
			util = 100.0 // allocated GPU is assumed in use
		}
		gpuDevices = append(gpuDevices, pkgmetrics.GPUDeviceMetrics{
			Index:       int(i),
			Utilization: util,
		})
	}

	return &pkgmetrics.GPUMetrics{
		NodeName: nodeName,
		GPUs:     gpuDevices,
	}, nil
}

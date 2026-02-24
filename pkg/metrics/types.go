package metrics

import (
	"context"
	"time"
)

// MetricsCollector collects resource usage metrics from the cluster.
type MetricsCollector interface {
	GetNodeMetrics(ctx context.Context) ([]NodeMetrics, error)
	GetPodMetrics(ctx context.Context, namespace string) ([]PodMetrics, error)
	GetNodeMetricsByName(ctx context.Context, name string) (*NodeMetrics, error)
	GetPodMetricsByName(ctx context.Context, namespace, name string) (*PodMetrics, error)
	GetGPUMetrics(ctx context.Context, nodeName string) (*GPUMetrics, error)
}

type NodeMetrics struct {
	Name        string
	CPUUsage    int64   // millicores
	MemoryUsage int64   // bytes
	GPUUsage    float64 // percentage 0-100
	Timestamp   time.Time
}

type PodMetrics struct {
	Name       string
	Namespace  string
	Containers []ContainerMetrics
	Timestamp  time.Time
}

type ContainerMetrics struct {
	Name        string
	CPUUsage    int64 // millicores
	MemoryUsage int64 // bytes
	GPUUsage    float64
}

type GPUMetrics struct {
	NodeName  string
	GPUs      []GPUDeviceMetrics
	Timestamp time.Time
}

type GPUDeviceMetrics struct {
	Index       int
	UUID        string
	Model       string
	MemoryTotal int64
	MemoryUsed  int64
	Utilization float64 // 0-100
	Temperature float64
}

// MetricsWindow represents a time-series window of metrics data.
type MetricsWindow struct {
	Start      time.Time
	End        time.Time
	DataPoints int
	P50CPU     int64
	P95CPU     int64
	P99CPU     int64
	MaxCPU     int64
	P50Memory  int64
	P95Memory  int64
	P99Memory  int64
	MaxMemory  int64
}

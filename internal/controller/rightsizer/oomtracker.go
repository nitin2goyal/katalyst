package rightsizer

import (
	"context"
	"fmt"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/koptimizer/koptimizer/internal/config"
	"github.com/koptimizer/koptimizer/pkg/optimizer"
)

// OOMTracker detects OOM kills and recommends memory increases.
type OOMTracker struct {
	client client.Client
	config *config.Config

	mu   sync.Mutex
	seen map[string]time.Time // ns/pod/container -> last rec time (dedup)
}

func NewOOMTracker(c client.Client, cfg *config.Config) *OOMTracker {
	return &OOMTracker{client: c, config: cfg, seen: make(map[string]time.Time)}
}

// Analyze checks for recent OOM kills and generates recommendations.
// It accepts a pre-fetched pod list to avoid redundant Kubernetes API calls.
func (t *OOMTracker) Analyze(ctx context.Context, pods []optimizer.PodInfo) ([]optimizer.Recommendation, error) {
	logger := log.FromContext(ctx).WithName("oomtracker")
	var recs []optimizer.Recommendation

	for _, pi := range pods {
		pod := pi.Pod
		if t.isExcluded(pod.Namespace) {
			continue
		}

		for _, cs := range pod.Status.ContainerStatuses {
			if cs.LastTerminationState.Terminated == nil ||
				cs.LastTerminationState.Terminated.Reason != "OOMKilled" {
				continue
			}

			// Dedup: skip if already processed this container's OOM within the last hour.
			// LastTerminationState persists until container replacement, so without
			// this guard, a new OOM rec would be emitted every 60s cycle.
			oomKey := fmt.Sprintf("%s/%s/%s", pod.Namespace, pod.Name, cs.Name)
			t.mu.Lock()
			if lastSeen, ok := t.seen[oomKey]; ok && time.Since(lastSeen) < 1*time.Hour {
				t.mu.Unlock()
				continue
			}
			t.seen[oomKey] = time.Now()
			t.mu.Unlock()

			// Find container spec
			for _, c := range pod.Spec.Containers {
				if c.Name != cs.Name {
					continue
				}

				currentMem := int64(0)
				if mem, ok := c.Resources.Requests[corev1.ResourceMemory]; ok {
					currentMem = mem.Value()
				}
				if currentMem == 0 {
					if mem, ok := c.Resources.Limits[corev1.ResourceMemory]; ok {
						currentMem = mem.Value()
					}
				}

				if currentMem == 0 {
					continue
				}

				// Apply bump multiplier (default 2.5x)
				suggestedMem := int64(float64(currentMem) * t.config.Rightsizer.OOMBumpMultiplier)
				if suggestedMem <= currentMem {
					continue // multiplier <= 1.0 produces no actual increase
				}

				// Use resolved owner from PodInfo (Deployment, not ReplicaSet).
				// Raw pod.OwnerReferences[0] points to the ReplicaSet, which would
				// cause the actuator to patch the wrong resource.
				ownerKind := pi.OwnerKind
				ownerName := pi.OwnerName

				logger.Info("OOM kill detected",
					"pod", pod.Name,
					"container", cs.Name,
					"currentMem", formatBytes(currentMem),
					"suggestedMem", formatBytes(suggestedMem),
				)

				recs = append(recs, optimizer.Recommendation{
					ID:              fmt.Sprintf("oom-%s-%s-%s-%d", pod.Namespace, pod.Name, cs.Name, time.Now().Unix()),
					Type:            optimizer.RecommendationPodRightsize,
					Priority:        optimizer.PriorityCritical,
					AutoExecutable:  true,
					TargetKind:      ownerKind,
					TargetName:      ownerName,
					TargetNamespace: pod.Namespace,
					Summary: fmt.Sprintf("OOM killed: increase memory for %s/%s container %s from %s to %s (%.1fx bump)",
						pod.Namespace, ownerName, cs.Name, formatBytes(currentMem), formatBytes(suggestedMem), t.config.Rightsizer.OOMBumpMultiplier),
					ActionSteps: []string{
						fmt.Sprintf("Patch memory request/limit from %s to %s", formatBytes(currentMem), formatBytes(suggestedMem)),
					},
					Details: map[string]string{
						"resource":         "memory",
						"currentRequest":   formatBytes(currentMem),
						"suggestedRequest": formatBytes(suggestedMem),
						"reason":           "OOMKilled",
						"bumpMultiplier":   fmt.Sprintf("%.1f", t.config.Rightsizer.OOMBumpMultiplier),
					},
					CreatedAt: time.Now(),
				})
			}
		}
	}

	return recs, nil
}

// Cleanup removes expired entries from the seen map.
func (t *OOMTracker) Cleanup() {
	t.mu.Lock()
	defer t.mu.Unlock()
	cutoff := time.Now().Add(-2 * time.Hour)
	for k, v := range t.seen {
		if v.Before(cutoff) {
			delete(t.seen, k)
		}
	}
}

func (t *OOMTracker) isExcluded(namespace string) bool {
	for _, ns := range t.config.Rightsizer.ExcludeNamespaces {
		if ns == namespace {
			return true
		}
	}
	return false
}

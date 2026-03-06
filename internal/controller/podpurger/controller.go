package podpurger

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/koptimizer/koptimizer/internal/config"
	"github.com/koptimizer/koptimizer/internal/state"
)

// badStatusSet mirrors the set in handler/actions.go.
// Duplicated here to keep the dependency graph clean (controller should not import handler).
var badStatusSet = map[string]bool{
	"Failed":                     true,
	"Succeeded":                  true,
	"Unknown":                    true,
	"CrashLoopBackOff":           true,
	"Error":                      true,
	"OOMKilled":                  true,
	"ImagePullBackOff":           true,
	"ErrImagePull":               true,
	"ContainerStatusUnknown":     true,
	"Evicted":                    true,
	"Completed":                  true,
	"CreateContainerConfigError": true,
	// Init container variants (prefixed by computePodStatus)
	"Init:OOMKilled":                 true,
	"Init:CrashLoopBackOff":          true,
	"Init:Error":                     true,
	"Init:ImagePullBackOff":          true,
	"Init:ErrImagePull":              true,
	"Init:ContainerStatusUnknown":    true,
	"Init:CreateContainerConfigError": true,
}

// systemNamespaces are skipped by the purger.
var systemNamespaces = map[string]bool{
	"kube-system":     true,
	"kube-public":     true,
	"kube-node-lease": true,
}

// Controller automatically deletes pods stuck in error states.
type Controller struct {
	client client.Client
	state  *state.ClusterState
	config *config.Config
}

func NewController(mgr ctrl.Manager, st *state.ClusterState, cfg *config.Config) *Controller {
	return &Controller{
		client: mgr.GetClient(),
		state:  st,
		config: cfg,
	}
}

func (c *Controller) SetupWithManager(mgr ctrl.Manager) error {
	return mgr.Add(c)
}

func (c *Controller) Start(ctx context.Context) error {
	c.run(ctx)
	return nil
}

func (c *Controller) Name() string { return "pod-purger" }

func (c *Controller) run(ctx context.Context) {
	logger := log.FromContext(ctx).WithName("pod-purger")
	ticker := time.NewTicker(c.config.PodPurger.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if !c.config.IsControllerEnabled("podPurger") {
				continue
			}
			if c.state.Breaker.IsTripped(c.Name()) {
				logger.V(1).Info("Circuit breaker tripped, skipping cycle")
				continue
			}

			allPods := c.state.GetAllPods()
			now := time.Now()
			purged := 0

			for _, ps := range allPods {
				if systemNamespaces[ps.Namespace] {
					continue
				}

				// Skip koptimizer's own pods — never purge ourselves.
				if appName, ok := ps.Pod.Labels["app.kubernetes.io/name"]; ok && appName == "koptimizer" {
					continue
				}
				if appLabel, ok := ps.Pod.Labels["app"]; ok {
					if appLabel == "koptimizer" || appLabel == "koptimizer-dashboard" || appLabel == "mockapi" {
						continue
					}
				}

				status := computePodStatus(ps.Pod)
				if !badStatusSet[status] {
					continue
				}

				age := now.Sub(ps.Pod.CreationTimestamp.Time)
				if age < c.config.PodPurger.MinPodAge {
					continue
				}

				target := fmt.Sprintf("%s/%s", ps.Namespace, ps.Name)
				pod := &corev1.Pod{}
				pod.Name = ps.Name
				pod.Namespace = ps.Namespace

				if err := c.client.Delete(ctx, pod); err != nil {
					logger.Error(err, "Failed to delete bad pod", "pod", target, "status", status)
					c.state.AuditLog.Record("auto-purge-failed", target, "pod-purger",
						fmt.Sprintf("Failed to purge %s pod (age %s): %v", status, formatAge(age), err))
					c.state.Breaker.RecordFailure(c.Name())
				} else {
					logger.Info("Purged bad pod", "pod", target, "status", status, "age", formatAge(age))
					c.state.AuditLog.Record("auto-purge-pod", target, "pod-purger",
						fmt.Sprintf("Purged %s pod (age %s)", status, formatAge(age)))
					c.state.Breaker.RecordSuccess(c.Name())
					purged++
				}
			}

			if purged > 0 {
				logger.Info("Pod purge cycle complete", "purged", purged)
			}

		case <-ctx.Done():
			return
		}
	}
}

// computePodStatus derives the effective status string for a pod.
// Mirrors handler/helpers.go computePodStatus.
func computePodStatus(pod *corev1.Pod) string {
	// Check init containers — skip successfully completed ones (exit code 0).
	for i, cs := range pod.Status.InitContainerStatuses {
		if cs.State.Waiting != nil && cs.State.Waiting.Reason != "" {
			return "Init:" + cs.State.Waiting.Reason
		}
		if cs.State.Terminated != nil && cs.State.Terminated.ExitCode == 0 {
			continue
		}
		if cs.State.Terminated != nil {
			reason := cs.State.Terminated.Reason
			if reason == "" {
				reason = "Error"
			}
			return "Init:" + reason
		}
		if cs.State.Running != nil {
			return fmt.Sprintf("Init:%d/%d", i, len(pod.Spec.InitContainers))
		}
	}
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Waiting != nil && cs.State.Waiting.Reason != "" {
			return cs.State.Waiting.Reason
		}
		if cs.State.Terminated != nil && cs.State.Terminated.Reason != "" {
			return cs.State.Terminated.Reason
		}
	}
	phase := string(pod.Status.Phase)
	if phase == "" {
		return "Unknown"
	}
	return phase
}

func formatAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

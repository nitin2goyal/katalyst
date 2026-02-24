package rebalancer

import (
	"github.com/koptimizer/koptimizer/internal/config"
)

// Scheduler manages scheduled rebalancing using cron expressions.
type Scheduler struct {
	config *config.Config
}

func NewScheduler(cfg *config.Config) *Scheduler {
	return &Scheduler{config: cfg}
}

// GetSchedule returns the cron schedule for rebalancing.
func (s *Scheduler) GetSchedule() string {
	return s.config.Rebalancer.Schedule
}

// IsEnabled returns whether scheduled rebalancing is enabled.
func (s *Scheduler) IsEnabled() bool {
	return s.config.Rebalancer.Enabled && s.config.Rebalancer.Schedule != ""
}

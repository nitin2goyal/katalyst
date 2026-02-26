package state

import (
	"fmt"
	"sync"
	"time"
)

// CircuitBreaker tracks error rates per controller with a sliding window
// and auto-trips to recommend mode when the error rate exceeds a threshold.
// Supports half-open state: after a cooldown period, one probe request is
// allowed through. If it succeeds, the breaker resets; if it fails, it re-trips.
type CircuitBreaker struct {
	mu        sync.RWMutex
	threshold float64       // error rate threshold (0.0-1.0) to trip
	window    time.Duration // sliding window duration
	cooldown  time.Duration // cooldown before half-open (default = window)
	states    map[string]*controllerState
}

type controllerState struct {
	successes []time.Time
	failures  []time.Time
	tripped   bool
	trippedAt time.Time
	halfOpen  bool // In half-open state, one probe is allowed through
}

// NewCircuitBreaker creates a new circuit breaker with the given threshold and window.
// threshold is the error rate (0.0-1.0) above which the breaker trips.
// window is the sliding window for tracking success/failure counts.
func NewCircuitBreaker(threshold float64, window time.Duration) *CircuitBreaker {
	if threshold <= 0 || threshold > 1 {
		threshold = 0.5
	}
	if window <= 0 {
		window = 5 * time.Minute
	}
	return &CircuitBreaker{
		threshold: threshold,
		window:    window,
		cooldown:  window, // Default cooldown equals the sliding window
		states:    make(map[string]*controllerState),
	}
}

func (cb *CircuitBreaker) getOrCreate(controller string) *controllerState {
	s, ok := cb.states[controller]
	if !ok {
		s = &controllerState{}
		cb.states[controller] = s
	}
	return s
}

// RecordSuccess records a successful operation for the given controller.
// If the breaker is in half-open state, a success resets it to closed.
func (cb *CircuitBreaker) RecordSuccess(controller string) {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	s := cb.getOrCreate(controller)
	s.successes = append(s.successes, time.Now())
	cb.pruneUnlocked(s)
	if s.halfOpen {
		// Probe succeeded — reset the breaker
		s.tripped = false
		s.halfOpen = false
		s.successes = nil
		s.failures = nil
	}
}

// RecordFailure records a failed operation. If the error rate exceeds
// the threshold, the breaker trips automatically. If in half-open state,
// a failure re-trips the breaker immediately.
func (cb *CircuitBreaker) RecordFailure(controller string) {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	s := cb.getOrCreate(controller)
	s.failures = append(s.failures, time.Now())

	// Half-open probe failed — re-trip immediately
	if s.halfOpen {
		s.halfOpen = false
		s.tripped = true
		s.trippedAt = time.Now()
		return
	}

	// Check if we should trip
	cb.pruneUnlocked(s)
	total := len(s.successes) + len(s.failures)
	if total >= 5 { // Require at least 5 data points before tripping
		errorRate := float64(len(s.failures)) / float64(total)
		if errorRate >= cb.threshold && !s.tripped {
			s.tripped = true
			s.trippedAt = time.Now()
		}
	}
}

// IsTripped returns true if the circuit breaker is tripped for the given controller.
// After the cooldown period, the breaker transitions to half-open state, allowing
// one probe request through. If that probe succeeds (RecordSuccess), the breaker
// resets. If it fails (RecordFailure), it re-trips.
func (cb *CircuitBreaker) IsTripped(controller string) bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	s, ok := cb.states[controller]
	if !ok {
		return false
	}
	if !s.tripped {
		return false
	}
	// Check if cooldown has elapsed — transition to half-open
	if !s.halfOpen && time.Since(s.trippedAt) >= cb.cooldown {
		s.halfOpen = true
		return false // Allow one probe through
	}
	if s.halfOpen {
		return false // Already in half-open, allow the probe
	}
	return true
}

// Trip manually trips the circuit breaker for the given controller.
func (cb *CircuitBreaker) Trip(controller string) {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	s := cb.getOrCreate(controller)
	s.tripped = true
	s.trippedAt = time.Now()
}

// Reset resets the circuit breaker for the given controller.
func (cb *CircuitBreaker) Reset(controller string) {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	s, ok := cb.states[controller]
	if !ok {
		return
	}
	s.tripped = false
	s.successes = nil
	s.failures = nil
}

// Status returns a human-readable status for the given controller.
func (cb *CircuitBreaker) Status(controller string) string {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	s, ok := cb.states[controller]
	if !ok {
		return "closed"
	}
	if s.halfOpen {
		return fmt.Sprintf("half-open (since %s)", s.trippedAt.Format(time.RFC3339))
	}
	if s.tripped {
		return fmt.Sprintf("tripped (since %s)", s.trippedAt.Format(time.RFC3339))
	}
	return "closed"
}

// pruneUnlocked removes entries outside the sliding window. Must be called with mu held.
func (cb *CircuitBreaker) pruneUnlocked(s *controllerState) {
	cutoff := time.Now().Add(-cb.window)
	s.successes = pruneOlderThan(s.successes, cutoff)
	s.failures = pruneOlderThan(s.failures, cutoff)
}

func pruneOlderThan(times []time.Time, cutoff time.Time) []time.Time {
	idx := 0
	for _, t := range times {
		if t.After(cutoff) {
			times[idx] = t
			idx++
		}
	}
	return times[:idx]
}

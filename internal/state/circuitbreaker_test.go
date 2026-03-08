package state

import (
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Circuit Breaker Tests
// ---------------------------------------------------------------------------

func TestCircuitBreaker_InitialStateClosed(t *testing.T) {
	cb := NewCircuitBreaker(0.5, 5*time.Minute)
	if cb.IsTripped("test") {
		t.Error("new breaker should not be tripped")
	}
	if cb.Status("test") != "closed" {
		t.Errorf("status = %q, want closed", cb.Status("test"))
	}
}

func TestCircuitBreaker_TripOnHighErrorRate(t *testing.T) {
	cb := NewCircuitBreaker(0.5, 5*time.Minute)

	// 5 failures, 0 successes → 100% error rate → trips at 5 data points
	for i := 0; i < 5; i++ {
		cb.RecordFailure("ctrl")
	}
	if !cb.IsTripped("ctrl") {
		t.Error("should be tripped after 5 consecutive failures")
	}
}

func TestCircuitBreaker_NoTripBelowThreshold(t *testing.T) {
	cb := NewCircuitBreaker(0.5, 5*time.Minute)

	// 2 failures, 3 successes = 40% error rate (below 50% threshold)
	cb.RecordFailure("ctrl")
	cb.RecordFailure("ctrl")
	cb.RecordSuccess("ctrl")
	cb.RecordSuccess("ctrl")
	cb.RecordSuccess("ctrl")

	if cb.IsTripped("ctrl") {
		t.Error("should not trip at 40% error rate with 50% threshold")
	}
}

func TestCircuitBreaker_NoTripBelowMinDataPoints(t *testing.T) {
	cb := NewCircuitBreaker(0.5, 5*time.Minute)

	// 4 failures (below minimum 5 data points)
	for i := 0; i < 4; i++ {
		cb.RecordFailure("ctrl")
	}
	if cb.IsTripped("ctrl") {
		t.Error("should not trip with fewer than 5 data points")
	}
}

func TestCircuitBreaker_ManualTrip(t *testing.T) {
	cb := NewCircuitBreaker(0.5, 5*time.Minute)
	cb.Trip("ctrl")
	if !cb.IsTripped("ctrl") {
		t.Error("should be tripped after manual Trip()")
	}
}

func TestCircuitBreaker_ManualReset(t *testing.T) {
	cb := NewCircuitBreaker(0.5, 5*time.Minute)

	for i := 0; i < 5; i++ {
		cb.RecordFailure("ctrl")
	}
	cb.Reset("ctrl")
	if cb.IsTripped("ctrl") {
		t.Error("should not be tripped after Reset()")
	}
}

func TestCircuitBreaker_IndependentControllers(t *testing.T) {
	cb := NewCircuitBreaker(0.5, 5*time.Minute)

	for i := 0; i < 5; i++ {
		cb.RecordFailure("failing")
	}
	if cb.IsTripped("healthy") {
		t.Error("healthy controller should not be affected by failing controller")
	}
	if !cb.IsTripped("failing") {
		t.Error("failing controller should be tripped")
	}
}

func TestCircuitBreaker_HalfOpenAfterCooldown(t *testing.T) {
	// Use a very short cooldown for testing
	cb := NewCircuitBreaker(0.5, 5*time.Minute)
	cb.cooldown = 1 * time.Millisecond

	for i := 0; i < 5; i++ {
		cb.RecordFailure("ctrl")
	}

	// Wait for cooldown
	time.Sleep(5 * time.Millisecond)

	// After cooldown, should allow one probe (return false)
	if cb.IsTripped("ctrl") {
		t.Error("after cooldown, should transition to half-open and allow probe")
	}

	status := cb.Status("ctrl")
	if status == "closed" {
		t.Error("status should be half-open, not closed")
	}
}

func TestCircuitBreaker_HalfOpenProbeSuccess(t *testing.T) {
	cb := NewCircuitBreaker(0.5, 5*time.Minute)
	cb.cooldown = 1 * time.Millisecond

	for i := 0; i < 5; i++ {
		cb.RecordFailure("ctrl")
	}

	time.Sleep(5 * time.Millisecond)
	cb.IsTripped("ctrl") // Triggers half-open

	// Successful probe should reset breaker
	cb.RecordSuccess("ctrl")
	cb.ProbeCompleted("ctrl")

	if cb.IsTripped("ctrl") {
		t.Error("successful probe should reset breaker to closed")
	}
	if cb.Status("ctrl") != "closed" {
		t.Errorf("status = %q, want closed after successful probe", cb.Status("ctrl"))
	}
}

func TestCircuitBreaker_HalfOpenProbeFailure(t *testing.T) {
	cb := NewCircuitBreaker(0.5, 5*time.Minute)
	cb.cooldown = 1 * time.Millisecond

	for i := 0; i < 5; i++ {
		cb.RecordFailure("ctrl")
	}

	time.Sleep(5 * time.Millisecond)
	cb.IsTripped("ctrl") // Triggers half-open

	// Failed probe should re-trip
	cb.RecordFailure("ctrl")
	cb.ProbeCompleted("ctrl")

	if !cb.IsTripped("ctrl") {
		t.Error("failed probe should re-trip the breaker")
	}
}

func TestCircuitBreaker_HalfOpenBlocksMultipleProbes(t *testing.T) {
	cb := NewCircuitBreaker(0.5, 5*time.Minute)
	cb.cooldown = 1 * time.Millisecond

	for i := 0; i < 5; i++ {
		cb.RecordFailure("ctrl")
	}

	time.Sleep(5 * time.Millisecond)

	// First call: transitions to half-open, allows probe
	first := cb.IsTripped("ctrl")
	if first {
		t.Error("first call after cooldown should allow through")
	}

	// Second call: probe already in flight, should block
	second := cb.IsTripped("ctrl")
	if !second {
		t.Error("second call during half-open probe should be blocked")
	}
}

func TestCircuitBreaker_DefaultThreshold(t *testing.T) {
	// Invalid threshold should default to 0.5
	cb := NewCircuitBreaker(0, 5*time.Minute)
	if cb.threshold != 0.5 {
		t.Errorf("threshold = %f, want 0.5 (default)", cb.threshold)
	}

	cb2 := NewCircuitBreaker(-1, 5*time.Minute)
	if cb2.threshold != 0.5 {
		t.Errorf("negative threshold = %f, want 0.5 (default)", cb2.threshold)
	}
}

func TestCircuitBreaker_DefaultWindow(t *testing.T) {
	cb := NewCircuitBreaker(0.5, 0)
	if cb.window != 5*time.Minute {
		t.Errorf("window = %v, want 5m (default)", cb.window)
	}

	cb2 := NewCircuitBreaker(0.5, -1*time.Second)
	if cb2.window != 5*time.Minute {
		t.Errorf("negative window = %v, want 5m (default)", cb2.window)
	}
}

func TestCircuitBreaker_StatusFormats(t *testing.T) {
	cb := NewCircuitBreaker(0.5, 5*time.Minute)

	if cb.Status("unknown") != "closed" {
		t.Errorf("unknown controller status = %q, want closed", cb.Status("unknown"))
	}

	cb.Trip("ctrl")
	status := cb.Status("ctrl")
	if len(status) < 10 {
		t.Errorf("tripped status too short: %q", status)
	}
}

func TestPruneOlderThan(t *testing.T) {
	now := time.Now()
	times := []time.Time{
		now.Add(-10 * time.Minute),
		now.Add(-3 * time.Minute),
		now.Add(-1 * time.Minute),
		now.Add(1 * time.Minute),
	}

	cutoff := now.Add(-5 * time.Minute)
	result := pruneOlderThan(times, cutoff)

	if len(result) != 3 {
		t.Errorf("expected 3 entries after pruning, got %d", len(result))
	}
}

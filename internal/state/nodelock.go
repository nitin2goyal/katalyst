package state

import (
	"fmt"
	"sync"
	"time"
)

// NodeLock tracks which controller is currently operating on each node to
// prevent concurrent destructive operations (e.g., evictor and rebalancer
// both trying to drain the same node).
type NodeLock struct {
	mu    sync.Mutex
	locks map[string]lockEntry
}

type lockEntry struct {
	Controller string
	AcquiredAt time.Time
}

// NewNodeLock creates a new NodeLock.
func NewNodeLock() *NodeLock {
	return &NodeLock{
		locks: make(map[string]lockEntry),
	}
}

// TryLock attempts to acquire the lock for the given node on behalf of the
// named controller. Returns nil on success, an error if the node is already
// locked by another controller.
func (nl *NodeLock) TryLock(nodeName, controller string) error {
	nl.mu.Lock()
	defer nl.mu.Unlock()

	if entry, ok := nl.locks[nodeName]; ok {
		// Allow the same controller to re-acquire (idempotent).
		if entry.Controller == controller {
			return nil
		}
		return fmt.Errorf("node %s is locked by %s since %s",
			nodeName, entry.Controller, entry.AcquiredAt.Format(time.RFC3339))
	}

	nl.locks[nodeName] = lockEntry{
		Controller: controller,
		AcquiredAt: time.Now(),
	}
	return nil
}

// Unlock releases the lock for the given node. Only the owning controller
// can release it; other callers are silently ignored.
func (nl *NodeLock) Unlock(nodeName, controller string) {
	nl.mu.Lock()
	defer nl.mu.Unlock()

	if entry, ok := nl.locks[nodeName]; ok && entry.Controller == controller {
		delete(nl.locks, nodeName)
	}
}

// IsLocked returns true if the node is currently locked, along with the
// owning controller name.
func (nl *NodeLock) IsLocked(nodeName string) (bool, string) {
	nl.mu.Lock()
	defer nl.mu.Unlock()

	if entry, ok := nl.locks[nodeName]; ok {
		return true, entry.Controller
	}
	return false, ""
}

// Refresh extends the lock timestamp for the given node, acting as a heartbeat
// to prevent stale lock expiry during long-running operations. Only the owning
// controller can refresh.
func (nl *NodeLock) Refresh(nodeName, controller string) {
	nl.mu.Lock()
	defer nl.mu.Unlock()

	if entry, ok := nl.locks[nodeName]; ok && entry.Controller == controller {
		nl.locks[nodeName] = lockEntry{
			Controller: controller,
			AcquiredAt: time.Now(),
		}
	}
}

// ExpireStale removes locks older than the given duration. This prevents
// permanently stuck locks if a controller crashes without releasing.
func (nl *NodeLock) ExpireStale(maxAge time.Duration) {
	nl.mu.Lock()
	defer nl.mu.Unlock()

	cutoff := time.Now().Add(-maxAge)
	for node, entry := range nl.locks {
		if entry.AcquiredAt.Before(cutoff) {
			delete(nl.locks, node)
		}
	}
}

package store

import (
	"database/sql"
	"fmt"
	"os"
	"sync"
	"time"
)

const (
	// Sanity bounds for pricing values. Prices outside these bounds are rejected
	// to prevent divide-by-zero or wildly incorrect recommendations from bad API data.
	minValidPrice = 0.001  // $0.001/hr — below this is likely an API error
	maxValidPrice = 200.0  // $200/hr — above this is likely an API error (even p5.48xlarge is ~$98/hr)
)

// ValidatePrice returns true if the price falls within sane bounds.
func ValidatePrice(price float64) bool {
	return price >= minValidPrice && price <= maxValidPrice
}

// SanitizePrices filters a pricing map, removing entries with invalid prices
// (zero, negative, or absurdly high). Returns the number of entries removed.
func SanitizePrices(prices map[string]float64) int {
	removed := 0
	for k, v := range prices {
		if !ValidatePrice(v) {
			delete(prices, k)
			removed++
		}
	}
	return removed
}

// PricingCache provides a two-layer cache (in-memory + SQLite) for cloud
// instance pricing data. All methods are nil-safe: if the underlying *sql.DB
// is nil the cache operates purely in-memory.
type PricingCache struct {
	db  *sql.DB
	ttl time.Duration

	mu      sync.RWMutex
	mem     map[string]map[string]float64 // "provider:region" -> instanceType -> price
	memTime map[string]time.Time          // "provider:region" -> last updated
}

const (
	defaultPricingCacheTTL = 24 * time.Hour
	memoryPricingCacheTTL  = 1 * time.Hour
)

// NewPricingCache creates a PricingCache backed by the given database.
// If db is nil, the cache works in-memory only.
func NewPricingCache(db *sql.DB) *PricingCache {
	pc := &PricingCache{
		db:      db,
		ttl:     defaultPricingCacheTTL,
		mem:     make(map[string]map[string]float64),
		memTime: make(map[string]time.Time),
	}
	if db != nil {
		pc.ensureTable()
	}
	return pc
}

func (c *PricingCache) ensureTable() {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS pricing_cache (
			provider TEXT NOT NULL,
			region TEXT NOT NULL,
			instance_type TEXT NOT NULL,
			price_per_hour REAL NOT NULL,
			updated_at INTEGER NOT NULL,
			PRIMARY KEY (provider, region, instance_type)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_pricing_cache_updated ON pricing_cache(provider, region, updated_at)`,
	}
	for _, s := range stmts {
		if _, err := c.db.Exec(s); err != nil {
			// Log but don't fail — cache will fall back to in-memory only
			fmt.Fprintf(os.Stderr, "pricing_cache: table init failed: %v\n", err)
		}
	}
}

func cacheKey(provider, region string) string {
	return provider + ":" + region
}

// Get returns cached prices for a provider+region. It checks the in-memory
// cache first (1h TTL), then SQLite (24h TTL). Returns nil, false on miss.
func (c *PricingCache) Get(provider, region string) (map[string]float64, bool) {
	key := cacheKey(provider, region)

	// Check in-memory cache.
	c.mu.RLock()
	if prices, ok := c.mem[key]; ok {
		if time.Since(c.memTime[key]) < memoryPricingCacheTTL {
			// Copy to avoid races on the caller side.
			cp := make(map[string]float64, len(prices))
			for k, v := range prices {
				cp[k] = v
			}
			c.mu.RUnlock()
			return cp, true
		}
	}
	c.mu.RUnlock()

	// Check SQLite cache.
	if c.db == nil {
		return nil, false
	}

	cutoff := time.Now().Add(-c.ttl).Unix()
	rows, err := c.db.Query(
		`SELECT instance_type, price_per_hour FROM pricing_cache
		 WHERE provider = ? AND region = ? AND updated_at > ?`,
		provider, region, cutoff,
	)
	if err != nil {
		return nil, false
	}
	defer rows.Close()

	prices := make(map[string]float64)
	for rows.Next() {
		var it string
		var price float64
		if err := rows.Scan(&it, &price); err != nil {
			continue
		}
		prices[it] = price
	}
	if len(prices) == 0 {
		return nil, false
	}

	// Populate in-memory cache from SQLite.
	c.mu.Lock()
	c.mem[key] = prices
	c.memTime[key] = time.Now()
	c.mu.Unlock()

	// Return a copy.
	cp := make(map[string]float64, len(prices))
	for k, v := range prices {
		cp[k] = v
	}
	return cp, true
}

// Put writes prices to both the in-memory and SQLite caches.
func (c *PricingCache) Put(provider, region string, prices map[string]float64) {
	key := cacheKey(provider, region)

	// Update in-memory cache.
	cp := make(map[string]float64, len(prices))
	for k, v := range prices {
		cp[k] = v
	}
	c.mu.Lock()
	c.mem[key] = cp
	c.memTime[key] = time.Now()
	c.mu.Unlock()

	// Persist to SQLite.
	if c.db == nil {
		return
	}

	now := time.Now().Unix()
	tx, err := c.db.Begin()
	if err != nil {
		return
	}
	stmt, err := tx.Prepare(
		`INSERT OR REPLACE INTO pricing_cache (provider, region, instance_type, price_per_hour, updated_at)
		 VALUES (?, ?, ?, ?, ?)`,
	)
	if err != nil {
		tx.Rollback()
		return
	}
	defer stmt.Close()

	for it, price := range prices {
		if _, err = stmt.Exec(provider, region, it, price, now); err != nil {
			tx.Rollback()
			return
		}
	}
	tx.Commit()
}

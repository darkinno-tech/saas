package store

import (
	"context"
	"sync"
	"time"

	"github.com/darkinno-tech/saas/core/types"
)

var _ Cache = (*MemoryCache)(nil)

// MemoryCache is a thread-safe in-memory tenant cache with TTL support.
type MemoryCache struct {
	mu         sync.Mutex
	now        func() time.Time
	maxEntries int
	entries    map[types.TenantID]cacheEntry
}

type cacheEntry struct {
	tenant    types.Tenant
	createdAt time.Time
	expiresAt time.Time
}

// NewMemoryCache creates an empty memory cache.
func NewMemoryCache() *MemoryCache {
	return newMemoryCacheWithClock(time.Now)
}

// NewBoundedMemoryCache creates a memory cache with a maximum number of entries.
func NewBoundedMemoryCache(maxEntries int) (*MemoryCache, error) {
	return newBoundedMemoryCacheWithClock(time.Now, maxEntries)
}

func newMemoryCacheWithClock(now func() time.Time) *MemoryCache {
	cache, _ := newBoundedMemoryCacheWithClock(now, 0)
	return cache
}

func newBoundedMemoryCacheWithClock(now func() time.Time, maxEntries int) (*MemoryCache, error) {
	if maxEntries < 0 {
		return nil, ErrInvalidCacheSize
	}
	return &MemoryCache{
		now:        now,
		maxEntries: maxEntries,
		entries:    make(map[types.TenantID]cacheEntry),
	}, nil
}

// Get returns cached tenant metadata.
func (cache *MemoryCache) Get(ctx context.Context, id types.TenantID) (types.Tenant, bool, error) {
	if err := ctx.Err(); err != nil {
		return types.Tenant{}, false, err
	}
	if id == "" {
		return types.Tenant{}, false, ErrInvalidTenant
	}

	cache.mu.Lock()
	defer cache.mu.Unlock()

	entry, ok := cache.entries[id]
	if !ok {
		return types.Tenant{}, false, nil
	}
	if !entry.expiresAt.IsZero() && !entry.expiresAt.After(cache.now()) {
		delete(cache.entries, id)
		return types.Tenant{}, false, nil
	}
	return cloneTenant(entry.tenant), true, nil
}

// Set stores tenant metadata with ttl. A non-positive ttl means no expiration.
func (cache *MemoryCache) Set(ctx context.Context, tenant types.Tenant, ttl time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateTenant(tenant); err != nil {
		return err
	}

	now := cache.now()
	entry := cacheEntry{tenant: cloneTenant(tenant), createdAt: now}
	if ttl > 0 {
		entry.expiresAt = now.Add(ttl)
	}

	cache.mu.Lock()
	defer cache.mu.Unlock()

	if _, ok := cache.entries[tenant.ID]; !ok && cache.maxEntries > 0 && len(cache.entries) >= cache.maxEntries {
		cache.evictExpiredLocked(now)
	}
	if _, ok := cache.entries[tenant.ID]; !ok && cache.maxEntries > 0 && len(cache.entries) >= cache.maxEntries {
		cache.evictOldestLocked()
	}
	cache.entries[tenant.ID] = entry
	return nil
}

// Delete removes cached tenant metadata by ID.
func (cache *MemoryCache) Delete(ctx context.Context, id types.TenantID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if id == "" {
		return ErrInvalidTenant
	}

	cache.mu.Lock()
	defer cache.mu.Unlock()

	delete(cache.entries, id)
	return nil
}

// Invalidate removes all cached tenant metadata.
func (cache *MemoryCache) Invalidate(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	cache.mu.Lock()
	defer cache.mu.Unlock()

	cache.entries = make(map[types.TenantID]cacheEntry)
	return nil
}

func (cache *MemoryCache) evictExpiredLocked(now time.Time) {
	for id, entry := range cache.entries {
		if !entry.expiresAt.IsZero() && !entry.expiresAt.After(now) {
			delete(cache.entries, id)
		}
	}
}

func (cache *MemoryCache) evictOldestLocked() {
	var (
		oldestID types.TenantID
		oldest   time.Time
	)
	for id, entry := range cache.entries {
		if oldestID == "" || entry.createdAt.Before(oldest) {
			oldestID = id
			oldest = entry.createdAt
		}
	}
	if oldestID != "" {
		delete(cache.entries, oldestID)
	}
}

package store

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"time"

	"github.com/darkinno-tech/saas/core/types"
)

var _ Store = (*CachedStore)(nil)
var _ CompareAndSwapStore = (*CachedStore)(nil)

// CachedStore wraps a Store with cache-aside reads and write-through invalidation.
// Read fills and writes are ordered across in-process wrappers that share the
// same non-nil pointer-backed Cache instance, even when their Store wrappers
// differ. Non-pointer Cache values are used in full-bypass mode because they do
// not provide a stable identity for coordinating wrappers. Applications with
// wrappers in different processes should configure a finite TTL or a cache with
// its own versioning/invalidation protocol. A non-positive TTL is safe across
// processes only when the cache provides that stronger coherence mechanism.
type CachedStore struct {
	next       Store
	cache      Cache
	ttl        time.Duration
	coord      *cacheCoordinator
	cacheID    cacheIdentity
	cacheReads bool
}

type cacheCoordinator struct {
	mu         sync.Mutex
	legacyMu   sync.Mutex
	generation uint64
	poisoned   map[cacheIdentity]struct{}
}

type cacheIdentity struct {
	typ reflect.Type
	ptr uintptr
}

const cacheCoordinatorStripeCount = 64

var cacheCoordinatorStripes [cacheCoordinatorStripeCount]cacheCoordinator

// NewCachedStore creates a cached store decorator.
func NewCachedStore(next Store, cache Cache, ttl time.Duration) (*CachedStore, error) {
	if next == nil {
		return nil, ErrNilStore
	}
	if cache == nil {
		return nil, ErrNilCache
	}

	coordinator, cacheID, cacheReads := sharedCacheCoordinator(cache)
	return &CachedStore{
		next:       next,
		cache:      cache,
		ttl:        ttl,
		coord:      coordinator,
		cacheID:    cacheID,
		cacheReads: cacheReads,
	}, nil
}

// Get returns tenant metadata from cache when available, otherwise from the wrapped store.
func (store *CachedStore) Get(ctx context.Context, id types.TenantID) (types.Tenant, error) {
	if !store.cacheReads {
		return store.next.Get(ctx, id)
	}

	if tenant, ok, generation := store.lookupCache(ctx, id); ok {
		return tenant, nil
	} else {
		tenant, err := store.next.Get(ctx, id)
		if err != nil {
			if errors.Is(err, ErrTenantNotFound) {
				store.invalidate(ctx, id)
			}
			return types.Tenant{}, err
		}
		store.fillIfUnchanged(ctx, tenant, generation)
		return tenant, nil
	}
}

// List delegates to the wrapped store.
func (store *CachedStore) List(ctx context.Context, filter ListFilter) ([]types.Tenant, error) {
	return store.next.List(ctx, filter)
}

// Create inserts tenant metadata and refreshes the cache.
func (store *CachedStore) Create(ctx context.Context, tenant types.Tenant) error {
	if err := store.mutateSource(func() error { return store.next.Create(ctx, tenant) }); err != nil {
		return err
	}
	generation := store.invalidate(ctx, tenant.ID)
	store.refillCurrent(ctx, tenant.ID, generation)
	return nil
}

// Update replaces tenant metadata and refreshes the cache.
func (store *CachedStore) Update(ctx context.Context, tenant types.Tenant) error {
	if err := store.mutateSource(func() error { return store.next.Update(ctx, tenant) }); err != nil {
		return err
	}
	generation := store.invalidate(ctx, tenant.ID)
	store.refillCurrent(ctx, tenant.ID, generation)
	return nil
}

// CompareAndSwap atomically replaces expected tenant metadata and refreshes the cache.
// When the wrapped store does not implement CompareAndSwapStore, CachedStore
// provides a process-local read/compare/update fallback under its write lock.
func (store *CachedStore) CompareAndSwap(ctx context.Context, expected types.Tenant, updated types.Tenant) error {
	var err error
	if conditional, ok := store.next.(CompareAndSwapStore); ok {
		err = conditional.CompareAndSwap(ctx, expected, updated)
	} else {
		err = store.compareAndSwapLegacy(ctx, expected, updated)
	}
	if err != nil {
		if errors.Is(err, ErrTenantConflict) || errors.Is(err, ErrTenantNotFound) {
			store.invalidate(ctx, expected.ID)
		}
		return err
	}

	generation := store.invalidate(ctx, updated.ID)
	store.refillCurrent(ctx, updated.ID, generation)
	return nil
}

func (store *CachedStore) compareAndSwapLegacy(ctx context.Context, expected types.Tenant, updated types.Tenant) error {
	// Preserve process-local atomicity for legacy Store implementations without
	// holding the cache-state lock across next calls. Nested CachedStore values
	// implement CompareAndSwapStore and therefore never enter this fallback.
	store.coord.legacyMu.Lock()
	defer store.coord.legacyMu.Unlock()

	current, err := store.next.Get(ctx, expected.ID)
	if err != nil {
		return err
	}
	if !tenantsEqual(current, expected) {
		return ErrTenantConflict
	}
	return store.next.Update(ctx, updated)
}

// Delete removes tenant metadata and invalidates its cache entry.
func (store *CachedStore) Delete(ctx context.Context, id types.TenantID) error {
	if err := store.mutateSource(func() error { return store.next.Delete(ctx, id) }); err != nil {
		if errors.Is(err, ErrTenantNotFound) {
			store.invalidate(ctx, id)
		}
		return err
	}
	store.invalidate(ctx, id)
	return nil
}

func (store *CachedStore) mutateSource(mutate func() error) error {
	if _, conditional := store.next.(CompareAndSwapStore); conditional {
		return mutate()
	}
	store.coord.legacyMu.Lock()
	defer store.coord.legacyMu.Unlock()
	return mutate()
}

func (store *CachedStore) lookupCache(ctx context.Context, id types.TenantID) (types.Tenant, bool, uint64) {
	store.coord.mu.Lock()
	defer store.coord.mu.Unlock()

	if !store.recoverPoisonLocked(ctx) {
		return types.Tenant{}, false, store.coord.generation
	}
	generation := store.coord.generation
	tenant, ok, err := store.cache.Get(ctx, id)
	if err != nil || !ok {
		return types.Tenant{}, false, generation
	}
	return tenant, true, generation
}

func (store *CachedStore) fillIfUnchanged(ctx context.Context, tenant types.Tenant, generation uint64) {
	if !store.cacheReads {
		return
	}

	store.coord.mu.Lock()
	defer store.coord.mu.Unlock()
	if generation != store.coord.generation || store.isPoisonedLocked() {
		return
	}
	if err := store.cache.Set(ctx, tenant, store.ttl); err == nil {
		return
	}

	// A failed Set may have left either the old or the new value behind. Delete
	// it when possible; otherwise poison the whole cache instance because the
	// adapter no longer gives us a safe per-key coherence guarantee.
	if err := store.cache.Delete(ctx, tenant.ID); err != nil {
		store.poisonLocked()
	}
}

func (store *CachedStore) invalidate(ctx context.Context, id types.TenantID) uint64 {
	if !store.cacheReads {
		return 0
	}

	store.coord.mu.Lock()
	defer store.coord.mu.Unlock()
	store.coord.generation++
	if store.isPoisonedLocked() {
		if err := store.cache.Invalidate(ctx); err == nil {
			store.clearPoisonLocked()
		}
		return store.coord.generation
	}
	if err := store.cache.Delete(ctx, id); err != nil {
		store.poisonLocked()
	}
	return store.coord.generation
}

func (store *CachedStore) refillCurrent(ctx context.Context, id types.TenantID, generation uint64) {
	if !store.cacheReads {
		return
	}
	tenant, err := store.next.Get(ctx, id)
	if err != nil {
		return
	}
	store.fillIfUnchanged(ctx, tenant, generation)
}

func (store *CachedStore) recoverPoisonLocked(ctx context.Context) bool {
	if !store.isPoisonedLocked() {
		return true
	}
	if err := store.cache.Invalidate(ctx); err != nil {
		return false
	}
	store.clearPoisonLocked()
	store.coord.generation++
	return true
}

func (store *CachedStore) isPoisonedLocked() bool {
	if store.coord.poisoned == nil {
		return false
	}
	_, poisoned := store.coord.poisoned[store.cacheID]
	return poisoned
}

func (store *CachedStore) poisonLocked() {
	if store.coord.poisoned == nil {
		store.coord.poisoned = make(map[cacheIdentity]struct{})
	}
	if _, poisoned := store.coord.poisoned[store.cacheID]; poisoned {
		return
	}
	store.coord.poisoned[store.cacheID] = struct{}{}
	store.coord.generation++
}

func (store *CachedStore) clearPoisonLocked() {
	delete(store.coord.poisoned, store.cacheID)
}

func sharedCacheCoordinator(cache Cache) (*cacheCoordinator, cacheIdentity, bool) {
	value := reflect.ValueOf(cache)
	if !value.IsValid() || value.Kind() != reflect.Pointer || value.IsNil() {
		return &cacheCoordinator{}, cacheIdentity{}, false
	}
	identity := cacheIdentity{typ: value.Type(), ptr: value.Pointer()}
	stripe := (identity.ptr >> 3) % cacheCoordinatorStripeCount
	return &cacheCoordinatorStripes[stripe], identity, true
}

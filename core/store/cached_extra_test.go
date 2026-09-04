package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/darkinno-tech/saas/core/types"
)

// faultingStore wraps a Store but does NOT implement CompareAndSwapStore, so a
// CachedStore treats it as a legacy store and uses the mutex fallback path. It
// also allows injecting read/delete errors.
type faultingStore struct {
	Store
	getErr    error
	deleteErr error
}

func (store *faultingStore) Get(ctx context.Context, id types.TenantID) (types.Tenant, error) {
	if store.getErr != nil {
		return types.Tenant{}, store.getErr
	}
	return store.Store.Get(ctx, id)
}

func (store *faultingStore) Delete(ctx context.Context, id types.TenantID) error {
	if store.deleteErr != nil {
		return store.deleteErr
	}
	return store.Store.Delete(ctx, id)
}

func TestCachedStoreListDelegates(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	backing := NewMemoryStore()
	for _, id := range []types.TenantID{"tenant-a", "tenant-b"} {
		if err := backing.Create(ctx, testTenant(id)); err != nil {
			t.Fatalf("Create(%s) error = %v", id, err)
		}
	}
	cache := NewMemoryCache()
	cached, err := NewCachedStore(backing, cache, time.Hour)
	if err != nil {
		t.Fatalf("NewCachedStore() error = %v", err)
	}

	all, err := cached.List(ctx, ListFilter{})
	if err != nil || len(all) != 2 {
		t.Fatalf("List() = %+v, %v; want two tenants", all, err)
	}
	page, err := cached.List(ctx, ListFilter{Limit: 1, Offset: 1})
	if err != nil || len(page) != 1 || page[0].ID != "tenant-b" {
		t.Fatalf("List(page) = %+v, %v; want tenant-b", page, err)
	}
}

func TestCachedStoreLegacyMutateUsesFallbackLock(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	backing := &faultingStore{Store: NewMemoryStore()}
	cache := NewMemoryCache()
	cached, err := NewCachedStore(backing, cache, time.Hour)
	if err != nil {
		t.Fatalf("NewCachedStore() error = %v", err)
	}

	tenant := testTenant("tenant-a")
	if err := cached.Create(ctx, tenant); err != nil {
		t.Fatalf("Create(legacy) error = %v", err)
	}
	got, err := cached.Get(ctx, tenant.ID)
	if err != nil || got.ID != tenant.ID {
		t.Fatalf("Get() after create = %+v, %v", got, err)
	}

	tenant.Name = "updated"
	if err := cached.Update(ctx, tenant); err != nil {
		t.Fatalf("Update(legacy) error = %v", err)
	}
	got, err = cached.Get(ctx, tenant.ID)
	if err != nil || got.Name != "updated" {
		t.Fatalf("Get() after update = %+v, %v", got, err)
	}
}

func TestCachedStoreLegacyCompareAndSwapConflict(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	backing := &faultingStore{Store: NewMemoryStore()}
	original := testTenant("tenant-a")
	if err := backing.Create(ctx, original); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	// Change the source so the CAS expectation is stale.
	stale := original
	stale.Name = "stale"
	if err := backing.Update(ctx, stale); err != nil {
		t.Fatalf("backing.Update() error = %v", err)
	}

	cached, err := NewCachedStore(backing, NewMemoryCache(), time.Hour)
	if err != nil {
		t.Fatalf("NewCachedStore() error = %v", err)
	}
	updated := original
	updated.Name = "conditional"
	if err := cached.CompareAndSwap(ctx, original, updated); !errors.Is(err, ErrTenantConflict) {
		t.Fatalf("CompareAndSwap(stale legacy) error = %v, want ErrTenantConflict", err)
	}
}

func TestCachedStoreLegacyCompareAndSwapPropagatesGetError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	readErr := errors.New("read failed")
	backing := &faultingStore{Store: NewMemoryStore(), getErr: readErr}
	cached, err := NewCachedStore(backing, NewMemoryCache(), time.Hour)
	if err != nil {
		t.Fatalf("NewCachedStore() error = %v", err)
	}
	updated := testTenant("tenant-a")
	if err := cached.CompareAndSwap(ctx, updated, updated); !errors.Is(err, readErr) {
		t.Fatalf("CompareAndSwap(get error) error = %v, want %v", err, readErr)
	}
}

func TestCachedStoreDeleteSourceNotFoundInvalidates(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cached, err := NewCachedStore(NewMemoryStore(), NewMemoryCache(), time.Hour)
	if err != nil {
		t.Fatalf("NewCachedStore() error = %v", err)
	}
	// Deleting an absent tenant must return the source error and not panic.
	if err := cached.Delete(ctx, "tenant-missing"); !errors.Is(err, ErrTenantNotFound) {
		t.Fatalf("Delete(missing) error = %v, want ErrTenantNotFound", err)
	}
}

func TestCachedStoreRecoversFromPoisonedCache(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	backing := NewMemoryStore()
	active := testTenant("tenant-a")
	if err := backing.Create(ctx, active); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	cacheDown := errors.New("cache down")
	cache := &scriptedTenantCache{
		value:     active,
		ok:        true,
		deleteErr: cacheDown, // writing poisons the cache.
	}
	cached, err := NewCachedStore(backing, cache, 0)
	if err != nil {
		t.Fatalf("NewCachedStore() error = %v", err)
	}
	suspended := active
	suspended.Status = types.TenantStatusSuspended
	if err := cached.Update(ctx, suspended); err != nil {
		t.Fatalf("Update() error = %v, source write succeeded", err)
	}

	// A second wrapper sharing the same pointer cache recovers the poison.
	second, err := NewCachedStore(backing, cache, 0)
	if err != nil {
		t.Fatalf("NewCachedStore(second) error = %v", err)
	}
	got, err := second.Get(ctx, active.ID)
	if err != nil {
		t.Fatalf("Get() after poison error = %v", err)
	}
	if got.Status != types.TenantStatusSuspended {
		t.Fatalf("Get() status = %q, want suspended source state", got.Status)
	}
}

func TestCachedStoreFillIfUnchangedPoisonsOnDoubleFailure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	backing := NewMemoryStore()
	active := testTenant("tenant-a")
	if err := backing.Create(ctx, active); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	cacheDown := errors.New("cache down")
	cache := &scriptedTenantCache{
		value:         active,
		ok:            false, // cache miss so we refill from source.
		setErr:        cacheDown,
		deleteErr:     cacheDown, // failed Set followed by failed Delete poisons.
		invalidateErr: cacheDown, // a failing invalidate keeps the cache poisoned.
	}
	cached, err := NewCachedStore(backing, cache, 0)
	if err != nil {
		t.Fatalf("NewCachedStore() error = %v", err)
	}

	got, err := cached.Get(ctx, active.ID)
	if err != nil || got.ID != active.ID {
		t.Fatalf("Get() = %+v, %v; want source tenant read through", got, err)
	}
	// The refill failed both Set and Delete, so the cache must now be poisoned
	// and bypassed on subsequent reads.
	calls := cache.getCallCount()
	if _, err := cached.Get(ctx, active.ID); err != nil {
		t.Fatalf("Get() after poison error = %v", err)
	}
	if cache.getCallCount() != calls {
		t.Fatalf("cache Get calls increased from %d after poison", calls)
	}
}

func TestCachedStoreCreateUpdatePropagateSourceErrors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	backing := NewMemoryStore()
	cached, err := NewCachedStore(backing, NewMemoryCache(), time.Hour)
	if err != nil {
		t.Fatalf("NewCachedStore() error = %v", err)
	}

	tenant := testTenant("tenant-a")
	if err := cached.Create(ctx, tenant); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	// Duplicate create must surface the source error, not the stale cache.
	if err := cached.Create(ctx, tenant); !errors.Is(err, ErrTenantAlreadyExists) {
		t.Fatalf("Create(duplicate) error = %v, want ErrTenantAlreadyExists", err)
	}
	// Updating an absent tenant must surface ErrTenantNotFound.
	if err := cached.Update(ctx, testTenant("tenant-missing")); !errors.Is(err, ErrTenantNotFound) {
		t.Fatalf("Update(missing) error = %v, want ErrTenantNotFound", err)
	}
}

func TestCachedStorePoisonIsIdempotent(t *testing.T) {
	t.Parallel()
	cached, err := NewCachedStore(NewMemoryStore(), NewMemoryCache(), time.Hour)
	if err != nil {
		t.Fatalf("NewCachedStore() error = %v", err)
	}
	cached.coord.mu.Lock()
	cached.poisonLocked()
	cached.poisonLocked() // second poison must hit the already-poisoned early return.
	poisoned := cached.isPoisonedLocked()
	cached.clearPoisonLocked()
	cleared := cached.isPoisonedLocked()
	cached.coord.mu.Unlock()
	if !poisoned {
		t.Fatal("isPoisonedLocked() = false after poisoning")
	}
	if cleared {
		t.Fatal("isPoisonedLocked() = true after clearing")
	}
}

func TestCachedStoreFillIfUnchangedSurvivesSetFailureWithDelete(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	backing := NewMemoryStore()
	active := testTenant("tenant-a")
	if err := backing.Create(ctx, active); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	// Set fails but Delete succeeds: the cache must clear the key without
	// poisoning the whole cache instance.
	cache := &scriptedTenantCache{
		value:  active,
		ok:     false,
		setErr: errors.New("set failed"),
	}
	cached, err := NewCachedStore(backing, cache, 0)
	if err != nil {
		t.Fatalf("NewCachedStore() error = %v", err)
	}
	got, err := cached.Get(ctx, active.ID)
	if err != nil || got.ID != active.ID {
		t.Fatalf("Get() = %+v, %v; want source tenant", got, err)
	}
	if cache.ok {
		t.Fatal("cache still holds a key after failed Set + successful Delete")
	}
}

func TestCachedStoreInvalidateClearsExistingPoison(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	backing := NewMemoryStore()
	active := testTenant("tenant-a")
	if err := backing.Create(ctx, active); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	cacheDown := errors.New("cache down")
	cache := &scriptedTenantCache{
		value:     active,
		ok:        true,
		deleteErr: cacheDown, // Deletes fail, so the first write poisons the cache.
	}
	cached, err := NewCachedStore(backing, cache, 0)
	if err != nil {
		t.Fatalf("NewCachedStore() error = %v", err)
	}
	suspended := active
	suspended.Status = types.TenantStatusSuspended
	if err := cached.Update(ctx, suspended); err != nil {
		t.Fatalf("Update(poison) error = %v", err)
	}
	if !cachedPoisoned(cached) {
		t.Fatal("cache not poisoned after failed invalidate Delete")
	}
	// A subsequent write's invalidate recovers via a successful Invalidate.
	if err := cached.Update(ctx, suspended); err != nil {
		t.Fatalf("Update(recovery) error = %v", err)
	}
	if cachedPoisoned(cached) {
		t.Fatal("cache still poisoned after recovery")
	}
}

func TestCachedStoreRefillCurrentSkipsOnSourceError(t *testing.T) {
	t.Parallel()
	backing := &faultingStore{
		Store:  NewMemoryStore(),
		getErr: errors.New("read after write failed"),
	}
	ctx := context.Background()
	cached, err := NewCachedStore(backing, NewMemoryCache(), time.Hour)
	if err != nil {
		t.Fatalf("NewCachedStore() error = %v", err)
	}
	// Refill propagates a clean source error without masking the failed write.
	if err := cached.Create(ctx, testTenant("tenant-a")); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
}

func TestCachedStoreFillIfUnchangedBypassedWhenReadsDisabled(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	backing := NewMemoryStore()
	tenant := testTenant("tenant-a")
	if err := backing.Create(ctx, tenant); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	// Non-pointer cache forces full bypass mode, so fill is a no-op.
	calls := 0
	cached, err := NewCachedStore(backing, valueTenantCache{calls: &calls}, time.Hour)
	if err != nil {
		t.Fatalf("NewCachedStore() error = %v", err)
	}
	cached.fillIfUnchanged(ctx, tenant, 0)
	if calls != 0 {
		t.Fatalf("fillIfUnchanged touched the bypassed cache %d times", calls)
	}
}

func cachedPoisoned(cached *CachedStore) bool {
	cached.coord.mu.Lock()
	defer cached.coord.mu.Unlock()
	return cached.isPoisonedLocked()
}

package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/darkinno-tech/saas/core/types"
)

func TestCachedStoreOrdersMissFillBeforeConcurrentUpdate(t *testing.T) {
	ctx := context.Background()
	backing := NewMemoryStore()
	active := testTenant("tenant-a")
	if err := backing.Create(ctx, active); err != nil {
		t.Fatalf("backing.Create() error = %v", err)
	}

	blocked := &blockingGetStore{
		Store:   backing,
		reached: make(chan struct{}),
		release: make(chan struct{}),
	}
	cache := NewMemoryCache()
	reader, err := NewCachedStore(blocked, cache, time.Hour)
	if err != nil {
		t.Fatalf("NewCachedStore() error = %v", err)
	}
	writer, err := NewCachedStore(backing, cache, time.Hour)
	if err != nil {
		t.Fatalf("NewCachedStore(second wrapper) error = %v", err)
	}

	getDone := make(chan error, 1)
	go func() {
		_, err := reader.Get(ctx, active.ID)
		getDone <- err
	}()
	<-blocked.reached

	suspended := active
	suspended.Status = types.TenantStatusSuspended
	updateDone := make(chan error, 1)
	go func() {
		updateDone <- writer.Update(ctx, suspended)
	}()

	select {
	case err := <-updateDone:
		if err != nil {
			t.Fatalf("concurrent Update() error = %v", err)
		}
	case <-time.After(time.Second):
		close(blocked.release)
		t.Fatal("Update() blocked behind a cache miss source read")
	}
	close(blocked.release)
	if err := <-getDone; err != nil {
		t.Fatalf("concurrent Get() error = %v", err)
	}

	got, err := reader.Get(context.Background(), active.ID)
	if err != nil {
		t.Fatalf("Get() after update error = %v", err)
	}
	if got.Status != types.TenantStatusSuspended {
		t.Fatalf("Get() status = %q, want suspended", got.Status)
	}
}

func TestCachedStoreBypassesStaleEntryWhenRefreshAndInvalidationFail(t *testing.T) {
	ctx := context.Background()
	backing := NewMemoryStore()
	active := testTenant("tenant-a")
	if err := backing.Create(ctx, active); err != nil {
		t.Fatalf("backing.Create() error = %v", err)
	}

	cacheDown := errors.New("cache down")
	cache := &scriptedTenantCache{
		value:         active,
		ok:            true,
		setErr:        cacheDown,
		deleteErr:     cacheDown,
		invalidateErr: cacheDown,
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

	second, err := NewCachedStore(backing, cache, 0)
	if err != nil {
		t.Fatalf("NewCachedStore(second wrapper) error = %v", err)
	}
	got, err := second.Get(context.Background(), active.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Status != types.TenantStatusSuspended {
		t.Fatalf("Get() status = %q, want suspended", got.Status)
	}
	if calls := cache.getCallCount(); calls != 0 {
		t.Fatalf("cache Get calls = %d, want dirty key to bypass stale cache", calls)
	}
}

func TestCachedStoreCASConflictInvalidatesStaleSnapshotForRetry(t *testing.T) {
	ctx := context.Background()
	backing := NewMemoryStore()
	active := testTenant("tenant-a")
	if err := backing.Create(ctx, active); err != nil {
		t.Fatalf("backing.Create() error = %v", err)
	}
	cache := NewMemoryCache()
	if err := cache.Set(ctx, active, 0); err != nil {
		t.Fatalf("cache.Set() error = %v", err)
	}
	cached, err := NewCachedStore(backing, cache, time.Hour)
	if err != nil {
		t.Fatalf("NewCachedStore() error = %v", err)
	}

	suspended := active
	suspended.Status = types.TenantStatusSuspended
	if err := backing.CompareAndSwap(ctx, active, suspended); err != nil {
		t.Fatalf("backing.CompareAndSwap() error = %v", err)
	}
	metadata := active
	metadata.Name = "updated"
	if err := cached.CompareAndSwap(ctx, active, metadata); !errors.Is(err, ErrTenantConflict) {
		t.Fatalf("CompareAndSwap(stale) error = %v, want ErrTenantConflict", err)
	}

	got, err := cached.Get(ctx, active.ID)
	if err != nil {
		t.Fatalf("Get() after conflict error = %v", err)
	}
	if got.Status != types.TenantStatusSuspended {
		t.Fatalf("Get() after conflict status = %q, want suspended source state", got.Status)
	}
}

func TestCachedStoreDeleteFailureCannotResurrectCachedTenant(t *testing.T) {
	ctx := context.Background()
	backing := NewMemoryStore()
	active := testTenant("tenant-a")
	if err := backing.Create(ctx, active); err != nil {
		t.Fatalf("backing.Create() error = %v", err)
	}

	cache := &scriptedTenantCache{
		value:         active,
		ok:            true,
		deleteErr:     errors.New("cache down"),
		invalidateErr: errors.New("cache down"),
	}
	cached, err := NewCachedStore(backing, cache, 0)
	if err != nil {
		t.Fatalf("NewCachedStore() error = %v", err)
	}
	if err := cached.Delete(ctx, active.ID); err != nil {
		t.Fatalf("Delete() error = %v, source delete succeeded", err)
	}

	if _, err := cached.Get(context.Background(), active.ID); !errors.Is(err, ErrTenantNotFound) {
		t.Fatalf("Get() after delete error = %v, want ErrTenantNotFound", err)
	}
	if calls := cache.getCallCount(); calls != 0 {
		t.Fatalf("cache Get calls = %d, want poisoned cache to bypass stale entry", calls)
	}
}

func TestCachedStoreFullyBypassesNonPointerCacheValue(t *testing.T) {
	ctx := context.Background()
	backing := NewMemoryStore()
	tenant := testTenant("tenant-a")
	if err := backing.Create(ctx, tenant); err != nil {
		t.Fatalf("backing.Create() error = %v", err)
	}

	calls := 0
	cached, err := NewCachedStore(backing, valueTenantCache{calls: &calls}, time.Hour)
	if err != nil {
		t.Fatalf("NewCachedStore() error = %v", err)
	}
	if _, err := cached.Get(ctx, tenant.ID); err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	tenant.Status = types.TenantStatusSuspended
	if err := cached.Update(ctx, tenant); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if calls != 0 {
		t.Fatalf("non-pointer cache calls = %d, want full bypass", calls)
	}
}

func TestCachedStoreNestedSharedCoordinatorDoesNotDeadlock(t *testing.T) {
	ctx := context.Background()
	backing := NewMemoryStore()
	tenant := testTenant("tenant-a")
	if err := backing.Create(ctx, tenant); err != nil {
		t.Fatalf("backing.Create() error = %v", err)
	}
	cache := NewMemoryCache()
	inner, err := NewCachedStore(backing, cache, time.Hour)
	if err != nil {
		t.Fatalf("NewCachedStore(inner) error = %v", err)
	}
	outer, err := NewCachedStore(inner, cache, time.Hour)
	if err != nil {
		t.Fatalf("NewCachedStore(outer) error = %v", err)
	}

	done := make(chan error, 1)
	go func() {
		tenant.Status = types.TenantStatusSuspended
		if err := outer.Update(ctx, tenant); err != nil {
			done <- err
			return
		}
		got, err := outer.Get(ctx, tenant.ID)
		if err == nil && got.Status != types.TenantStatusSuspended {
			err = errors.New("nested cached store returned stale tenant")
		}
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("nested cached store operation error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("nested cached store deadlocked on shared coordinator")
	}
}

func TestCachedStoreRefillsCurrentSourceAfterOutOfOrderWriteCompletion(t *testing.T) {
	ctx := context.Background()
	backing := NewMemoryStore()
	initial := testTenant("tenant-a")
	if err := backing.Create(ctx, initial); err != nil {
		t.Fatalf("backing.Create() error = %v", err)
	}

	delayed := &delayedUpdateStore{
		Store:   backing,
		updated: make(chan struct{}),
		release: make(chan struct{}),
	}
	cache := NewMemoryCache()
	firstWriter, err := NewCachedStore(delayed, cache, time.Hour)
	if err != nil {
		t.Fatalf("NewCachedStore(first writer) error = %v", err)
	}
	secondWriter, err := NewCachedStore(backing, cache, time.Hour)
	if err != nil {
		t.Fatalf("NewCachedStore(second writer) error = %v", err)
	}

	first := initial
	first.Name = "first"
	firstDone := make(chan error, 1)
	go func() { firstDone <- firstWriter.Update(ctx, first) }()
	<-delayed.updated // The first source write committed, but its cache work is delayed.

	second := initial
	second.Name = "second"
	secondDone := make(chan error, 1)
	go func() { secondDone <- secondWriter.Update(ctx, second) }()
	select {
	case err := <-secondDone:
		if err != nil {
			t.Fatalf("second Update() error = %v", err)
		}
	case <-time.After(time.Second):
		close(delayed.release)
		t.Fatal("second Update() blocked behind an in-flight source call")
	}

	close(delayed.release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first Update() error = %v", err)
	}
	got, err := firstWriter.Get(ctx, initial.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Name != "second" {
		t.Fatalf("Get().Name = %q, want current source value second", got.Name)
	}
}

func TestCachedStoreLegacyCASSerializesWithUpdate(t *testing.T) {
	ctx := context.Background()
	backing := NewMemoryStore()
	initial := testTenant("tenant-a")
	if err := backing.Create(ctx, initial); err != nil {
		t.Fatalf("backing.Create() error = %v", err)
	}

	legacy := &blockingGetStore{
		Store:   backing,
		reached: make(chan struct{}),
		release: make(chan struct{}),
	}
	cached, err := NewCachedStore(legacy, NewMemoryCache(), time.Hour)
	if err != nil {
		t.Fatalf("NewCachedStore() error = %v", err)
	}

	conditional := initial
	conditional.Name = "conditional"
	casDone := make(chan error, 1)
	go func() { casDone <- cached.CompareAndSwap(ctx, initial, conditional) }()
	<-legacy.reached

	later := initial
	later.Name = "later-update"
	updateDone := make(chan error, 1)
	go func() { updateDone <- cached.Update(ctx, later) }()
	select {
	case err := <-updateDone:
		t.Fatalf("Update() completed inside legacy compare-and-swap window: %v", err)
	default:
	}

	close(legacy.release)
	if err := <-casDone; err != nil {
		t.Fatalf("CompareAndSwap() error = %v", err)
	}
	if err := <-updateDone; err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	got, err := backing.Get(ctx, initial.ID)
	if err != nil {
		t.Fatalf("backing.Get() error = %v", err)
	}
	if got.Name != "later-update" {
		t.Fatalf("backing.Get().Name = %q, want later-update", got.Name)
	}
}

type blockingGetStore struct {
	Store
	once    sync.Once
	reached chan struct{}
	release chan struct{}
}

type delayedUpdateStore struct {
	Store
	once    sync.Once
	updated chan struct{}
	release chan struct{}
}

func (store *delayedUpdateStore) Update(ctx context.Context, tenant types.Tenant) error {
	if err := store.Store.Update(ctx, tenant); err != nil {
		return err
	}
	store.once.Do(func() {
		close(store.updated)
		<-store.release
	})
	return nil
}

func (store *blockingGetStore) Get(ctx context.Context, id types.TenantID) (types.Tenant, error) {
	tenant, err := store.Store.Get(ctx, id)
	store.once.Do(func() {
		close(store.reached)
		<-store.release
	})
	return tenant, err
}

type scriptedTenantCache struct {
	mu            sync.Mutex
	value         types.Tenant
	ok            bool
	getCalls      int
	setErr        error
	deleteErr     error
	invalidateErr error
}

func (cache *scriptedTenantCache) Get(context.Context, types.TenantID) (types.Tenant, bool, error) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.getCalls++
	return cloneTenant(cache.value), cache.ok, nil
}

func (cache *scriptedTenantCache) Set(_ context.Context, _ types.Tenant, _ time.Duration) error {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	return cache.setErr
}

func (cache *scriptedTenantCache) Delete(context.Context, types.TenantID) error {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.deleteErr != nil {
		return cache.deleteErr
	}
	cache.ok = false
	return nil
}

func (cache *scriptedTenantCache) Invalidate(context.Context) error {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.invalidateErr != nil {
		return cache.invalidateErr
	}
	cache.ok = false
	return nil
}

func (cache *scriptedTenantCache) getCallCount() int {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	return cache.getCalls
}

type valueTenantCache struct {
	calls *int
}

func (cache valueTenantCache) Get(context.Context, types.TenantID) (types.Tenant, bool, error) {
	(*cache.calls)++
	return types.Tenant{}, false, nil
}

func (cache valueTenantCache) Set(context.Context, types.Tenant, time.Duration) error {
	(*cache.calls)++
	return nil
}

func (cache valueTenantCache) Delete(context.Context, types.TenantID) error {
	(*cache.calls)++
	return nil
}

func (cache valueTenantCache) Invalidate(context.Context) error {
	(*cache.calls)++
	return nil
}

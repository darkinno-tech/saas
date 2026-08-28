package store

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/darkinno-tech/saas/core/types"
)

func TestMemoryStoreCopiesTenantConfig(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	tenant := testTenant("tenant-a")
	tenant.Config = map[string]string{"region": "us"}

	if err := store.Create(ctx, tenant); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	tenant.Config["region"] = "eu"

	got, err := store.Get(ctx, tenant.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Config["region"] != "us" {
		t.Fatalf("Get() config = %q, want %q", got.Config["region"], "us")
	}

	got.Config["region"] = "ap"
	again, err := store.Get(ctx, tenant.ID)
	if err != nil {
		t.Fatalf("Get() again error = %v", err)
	}
	if again.Config["region"] != "us" {
		t.Fatalf("stored config was mutated through result, got %q", again.Config["region"])
	}
}

func TestMemoryStoreRespectsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	store := NewMemoryStore()
	if err := store.Create(ctx, testTenant("tenant-a")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Create() error = %v, want context.Canceled", err)
	}
	if _, err := store.Get(ctx, "tenant-a"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Get() error = %v, want context.Canceled", err)
	}
	if _, err := store.List(ctx, ListFilter{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("List() error = %v, want context.Canceled", err)
	}
	if err := store.Update(ctx, testTenant("tenant-a")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Update() error = %v, want context.Canceled", err)
	}
	if err := store.Delete(ctx, "tenant-a"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Delete() error = %v, want context.Canceled", err)
	}
}

func TestMemoryStoreCompareAndSwapRejectsStaleSnapshot(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	original := testTenant("tenant-a")
	if err := store.Create(ctx, original); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	updated := original
	updated.Status = types.TenantStatusSuspended
	if err := store.CompareAndSwap(ctx, original, updated); err != nil {
		t.Fatalf("CompareAndSwap() error = %v", err)
	}
	staleUpdate := original
	staleUpdate.Name = "stale"
	if err := store.CompareAndSwap(ctx, original, staleUpdate); !errors.Is(err, ErrTenantConflict) {
		t.Fatalf("CompareAndSwap(stale) error = %v, want ErrTenantConflict", err)
	}

	got, err := store.Get(ctx, original.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Status != types.TenantStatusSuspended || got.Name != original.Name {
		t.Fatalf("tenant = %+v, want suspended without stale metadata", got)
	}
}

func TestMemoryStoreListPagination(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	for _, id := range []types.TenantID{"tenant-a", "tenant-b", "tenant-c"} {
		if err := store.Create(ctx, testTenant(id)); err != nil {
			t.Fatalf("Create(%s) error = %v", id, err)
		}
	}

	page, err := store.List(ctx, ListFilter{Limit: 1, Offset: 1})
	if err != nil {
		t.Fatalf("List(page) error = %v", err)
	}
	if len(page) != 1 || page[0].ID != "tenant-b" {
		t.Fatalf("List(page) = %+v, want tenant-b", page)
	}

	page, err = store.ListPage(ctx, PageFilter{Limit: 1, Cursor: "tenant-a"})
	if err != nil {
		t.Fatalf("ListPage(cursor) error = %v", err)
	}
	if len(page) != 1 || page[0].ID != "tenant-b" {
		t.Fatalf("ListPage(cursor) = %+v, want tenant-b", page)
	}

	if _, err := store.List(ctx, ListFilter{Offset: 1}); !errors.Is(err, ErrInvalidListFilter) {
		t.Fatalf("List(offset without limit) error = %v, want ErrInvalidListFilter", err)
	}
	if _, err := store.List(ctx, ListFilter{Limit: -1}); !errors.Is(err, ErrInvalidListFilter) {
		t.Fatalf("List(negative limit) error = %v, want ErrInvalidListFilter", err)
	}
	if _, err := store.ListPage(ctx, PageFilter{Offset: 1, Limit: 1, Cursor: "tenant-a"}); !errors.Is(err, ErrInvalidListFilter) {
		t.Fatalf("ListPage(cursor and offset) error = %v, want ErrInvalidListFilter", err)
	}

	page, err = store.List(ctx, ListFilter{Offset: 1, Limit: math.MaxInt})
	if err != nil {
		t.Fatalf("List(large limit) error = %v", err)
	}
	if len(page) != 2 || page[0].ID != "tenant-b" || page[1].ID != "tenant-c" {
		t.Fatalf("List(large limit) = %+v, want tenant-b and tenant-c", page)
	}
}

func TestMemoryCache(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	cache := newMemoryCacheWithClock(func() time.Time {
		return now
	})
	tenant := testTenant("tenant-a")
	tenant.Config = map[string]string{"region": "us"}

	if err := cache.Set(ctx, tenant, time.Minute); err != nil {
		t.Fatalf("Set() error = %v", err)
	}

	tenant.Config["region"] = "eu"

	got, ok, err := cache.Get(ctx, tenant.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !ok {
		t.Fatal("Get() ok = false, want true")
	}
	if got.Config["region"] != "us" {
		t.Fatalf("Get() config = %q, want %q", got.Config["region"], "us")
	}

	got.Config["region"] = "ap"
	again, ok, err := cache.Get(ctx, tenant.ID)
	if err != nil {
		t.Fatalf("Get() again error = %v", err)
	}
	if !ok {
		t.Fatal("Get() again ok = false, want true")
	}
	if again.Config["region"] != "us" {
		t.Fatalf("cached config was mutated through result, got %q", again.Config["region"])
	}

	now = now.Add(time.Minute)
	if _, ok, err := cache.Get(ctx, tenant.ID); err != nil || ok {
		t.Fatalf("Get() after ttl = _, %v, %v; want miss without error", ok, err)
	}

	if err := cache.Set(ctx, tenant, 0); err != nil {
		t.Fatalf("Set() without ttl error = %v", err)
	}
	now = now.Add(24 * time.Hour)
	if _, ok, err := cache.Get(ctx, tenant.ID); err != nil || !ok {
		t.Fatalf("Get() no-expiration = _, %v, %v; want hit without error", ok, err)
	}

	if err := cache.Delete(ctx, tenant.ID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, ok, err := cache.Get(ctx, tenant.ID); err != nil || ok {
		t.Fatalf("Get() after delete = _, %v, %v; want miss without error", ok, err)
	}

	if err := cache.Set(ctx, tenant, 0); err != nil {
		t.Fatalf("Set() before invalidate error = %v", err)
	}
	if err := cache.Invalidate(ctx); err != nil {
		t.Fatalf("Invalidate() error = %v", err)
	}
	if _, ok, err := cache.Get(ctx, tenant.ID); err != nil || ok {
		t.Fatalf("Get() after invalidate = _, %v, %v; want miss without error", ok, err)
	}
}

func TestBoundedMemoryCacheEvictsOldestEntry(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	cache, err := newBoundedMemoryCacheWithClock(func() time.Time {
		return now
	}, 2)
	if err != nil {
		t.Fatalf("newBoundedMemoryCacheWithClock() error = %v", err)
	}

	if err := cache.Set(ctx, testTenant("tenant-a"), 0); err != nil {
		t.Fatalf("Set(tenant-a) error = %v", err)
	}
	now = now.Add(time.Second)
	if err := cache.Set(ctx, testTenant("tenant-b"), 0); err != nil {
		t.Fatalf("Set(tenant-b) error = %v", err)
	}
	now = now.Add(time.Second)
	if err := cache.Set(ctx, testTenant("tenant-c"), 0); err != nil {
		t.Fatalf("Set(tenant-c) error = %v", err)
	}

	if _, ok, err := cache.Get(ctx, "tenant-a"); err != nil || ok {
		t.Fatalf("Get(evicted) = _, %v, %v; want miss", ok, err)
	}
	if _, ok, err := cache.Get(ctx, "tenant-b"); err != nil || !ok {
		t.Fatalf("Get(tenant-b) = _, %v, %v; want hit", ok, err)
	}
	if _, ok, err := cache.Get(ctx, "tenant-c"); err != nil || !ok {
		t.Fatalf("Get(tenant-c) = _, %v, %v; want hit", ok, err)
	}
}

func TestBoundedMemoryCacheEvictsExpiredEntriesFirst(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	cache, err := newBoundedMemoryCacheWithClock(func() time.Time {
		return now
	}, 2)
	if err != nil {
		t.Fatalf("newBoundedMemoryCacheWithClock() error = %v", err)
	}

	if err := cache.Set(ctx, testTenant("tenant-a"), time.Second); err != nil {
		t.Fatalf("Set(tenant-a) error = %v", err)
	}
	if err := cache.Set(ctx, testTenant("tenant-b"), 0); err != nil {
		t.Fatalf("Set(tenant-b) error = %v", err)
	}
	now = now.Add(2 * time.Second)
	if err := cache.Set(ctx, testTenant("tenant-c"), 0); err != nil {
		t.Fatalf("Set(tenant-c) error = %v", err)
	}

	if _, ok, err := cache.Get(ctx, "tenant-a"); err != nil || ok {
		t.Fatalf("Get(expired) = _, %v, %v; want miss", ok, err)
	}
	if _, ok, err := cache.Get(ctx, "tenant-b"); err != nil || !ok {
		t.Fatalf("Get(tenant-b) = _, %v, %v; want hit", ok, err)
	}
	if _, ok, err := cache.Get(ctx, "tenant-c"); err != nil || !ok {
		t.Fatalf("Get(tenant-c) = _, %v, %v; want hit", ok, err)
	}
}

func TestNewBoundedMemoryCacheValidation(t *testing.T) {
	if _, err := NewBoundedMemoryCache(-1); !errors.Is(err, ErrInvalidCacheSize) {
		t.Fatalf("NewBoundedMemoryCache(-1) error = %v, want ErrInvalidCacheSize", err)
	}
}

func TestCachedStore(t *testing.T) {
	ctx := context.Background()
	backing := NewMemoryStore()
	cache := NewMemoryCache()
	cached, err := NewCachedStore(backing, cache, time.Hour)
	if err != nil {
		t.Fatalf("NewCachedStore() error = %v", err)
	}

	tenant := testTenant("tenant-a")
	if err := cached.Create(ctx, tenant); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if err := backing.Update(ctx, types.Tenant{
		ID:     tenant.ID,
		Name:   "changed behind cache",
		Status: types.TenantStatusActive,
	}); err != nil {
		t.Fatalf("backing.Update() error = %v", err)
	}

	got, err := cached.Get(ctx, tenant.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Name != tenant.Name {
		t.Fatalf("Get() name = %q, want cached %q", got.Name, tenant.Name)
	}

	updated := testTenant("tenant-a")
	updated.Name = "updated"
	if err := cached.Update(ctx, updated); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	got, err = cached.Get(ctx, tenant.ID)
	if err != nil {
		t.Fatalf("Get() after update error = %v", err)
	}
	if got.Name != "updated" {
		t.Fatalf("Get() after update name = %q, want updated", got.Name)
	}

	if err := cached.Delete(ctx, tenant.ID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := cached.Get(ctx, tenant.ID); !errors.Is(err, ErrTenantNotFound) {
		t.Fatalf("Get() after delete error = %v, want ErrTenantNotFound", err)
	}
}

func TestNewCachedStoreRejectsNilDependencies(t *testing.T) {
	if _, err := NewCachedStore(nil, NewMemoryCache(), time.Minute); !errors.Is(err, ErrNilStore) {
		t.Fatalf("NewCachedStore(nil store) error = %v, want ErrNilStore", err)
	}
	if _, err := NewCachedStore(NewMemoryStore(), nil, time.Minute); !errors.Is(err, ErrNilCache) {
		t.Fatalf("NewCachedStore(nil cache) error = %v, want ErrNilCache", err)
	}
}

func testTenant(id types.TenantID) types.Tenant {
	return types.Tenant{
		ID:     id,
		Name:   "Tenant " + id.String(),
		Status: types.TenantStatusActive,
		PlanID: "starter",
		Config: map[string]string{"feature": "on"},
	}
}

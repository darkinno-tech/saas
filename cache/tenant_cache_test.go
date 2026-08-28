package cache

import (
	"context"
	"errors"
	"testing"
	"time"

	tenantctx "github.com/im10furry/saas/core/context"
	"github.com/im10furry/saas/core/types"
)

func TestTenantCacheScopesKeys(t *testing.T) {
	memory := NewMemory()
	cache := NewTenantCache(memory)
	ctxA := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})
	ctxB := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-b"})

	if err := cache.Set(ctxA, "profile", []byte("a"), 0); err != nil {
		t.Fatalf("Set(ctxA) error = %v", err)
	}
	if err := cache.Set(ctxB, "profile", []byte("b"), 0); err != nil {
		t.Fatalf("Set(ctxB) error = %v", err)
	}

	got, ok, err := cache.Get(ctxA, "profile")
	if err != nil {
		t.Fatalf("Get(ctxA) error = %v", err)
	}
	if !ok || string(got) != "a" {
		t.Fatalf("Get(ctxA) = %q, %v; want a, true", got, ok)
	}

	got, ok, err = cache.Get(ctxB, "profile")
	if err != nil {
		t.Fatalf("Get(ctxB) error = %v", err)
	}
	if !ok || string(got) != "b" {
		t.Fatalf("Get(ctxB) = %q, %v; want b, true", got, ok)
	}
}

func TestTenantCacheColonInputsDoNotCollide(t *testing.T) {
	memory := NewMemory()
	cache := NewTenantCache(memory)
	ctxA := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "a"})
	ctxB := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "a:b"})

	if err := cache.Set(ctxA, "b:profile", []byte("a"), 0); err != nil {
		t.Fatalf("Set(ctxA) error = %v", err)
	}
	if err := cache.Set(ctxB, "profile", []byte("b"), 0); err != nil {
		t.Fatalf("Set(ctxB) error = %v", err)
	}

	got, ok, err := cache.Get(ctxA, "b:profile")
	if err != nil {
		t.Fatalf("Get(ctxA) error = %v", err)
	}
	if !ok || string(got) != "a" {
		t.Fatalf("Get(ctxA) = %q, %v; want a, true", got, ok)
	}

	got, ok, err = cache.Get(ctxB, "profile")
	if err != nil {
		t.Fatalf("Get(ctxB) error = %v", err)
	}
	if !ok || string(got) != "b" {
		t.Fatalf("Get(ctxB) = %q, %v; want b, true", got, ok)
	}
}

func TestKeyBuilderUsesVersionedUnambiguousTenantKeys(t *testing.T) {
	builder := KeyBuilder{AllowHostGlobal: true}
	tenant := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "a:b"})

	got, err := builder.Build(tenant, "profile:summary")
	if err != nil {
		t.Fatalf("Build(tenant) error = %v", err)
	}
	if got != "t2:YTpi:profile:summary" {
		t.Fatalf("Build(tenant) = %q, want %q", got, "t2:YTpi:profile:summary")
	}

	got, err = builder.Build(tenantctx.WithHost(context.Background()), "status")
	if err != nil {
		t.Fatalf("Build(host) error = %v", err)
	}
	if got != "g:status" {
		t.Fatalf("Build(host) = %q, want %q", got, "g:status")
	}
}

func TestTenantCacheRejectsUnsafeAndUnscopedKeys(t *testing.T) {
	cache := NewTenantCache(NewMemory())
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})

	if err := cache.Set(ctx, "t:tenant-b:profile", []byte("x"), 0); !errors.Is(err, ErrUnsafeKey) {
		t.Fatalf("Set(prefixed) error = %v, want ErrUnsafeKey", err)
	}
	if err := cache.Set(ctx, "t2:dGVuYW50LWI:profile", []byte("x"), 0); !errors.Is(err, ErrUnsafeKey) {
		t.Fatalf("Set(versioned prefixed) error = %v, want ErrUnsafeKey", err)
	}
	if _, _, err := cache.Get(context.Background(), "profile"); !errors.Is(err, ErrNoTenant) {
		t.Fatalf("Get(no tenant) error = %v, want ErrNoTenant", err)
	}
	if err := cache.Set(tenantctx.WithHost(context.Background()), "profile", []byte("x"), 0); !errors.Is(err, ErrHostGlobalKeyNotAllowed) {
		t.Fatalf("Set(host global disabled) error = %v, want ErrHostGlobalKeyNotAllowed", err)
	}
}

func TestTenantCacheAllowsExplicitHostGlobalKeys(t *testing.T) {
	cache := NewTenantCache(NewMemory(), WithHostGlobalKeys(true))
	host := tenantctx.WithHost(context.Background())

	if err := cache.Set(host, "status", []byte("ok"), 0); err != nil {
		t.Fatalf("Set(host) error = %v", err)
	}
	got, ok, err := cache.Get(host, "status")
	if err != nil {
		t.Fatalf("Get(host) error = %v", err)
	}
	if !ok || string(got) != "ok" {
		t.Fatalf("Get(host) = %q, %v; want ok, true", got, ok)
	}
}

func TestMemoryCacheTTLAndCopies(t *testing.T) {
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	memory := newMemoryWithClock(func() time.Time { return now })
	ctx := context.Background()
	value := []byte("value")

	if err := memory.Set(ctx, "key", value, time.Minute); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	value[0] = 'x'

	got, ok, err := memory.Get(ctx, "key")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !ok || string(got) != "value" {
		t.Fatalf("Get() = %q, %v; want value, true", got, ok)
	}
	got[0] = 'x'
	again, ok, err := memory.Get(ctx, "key")
	if err != nil {
		t.Fatalf("Get() again error = %v", err)
	}
	if !ok || string(again) != "value" {
		t.Fatalf("Get() after mutation = %q, %v; want value, true", again, ok)
	}

	now = now.Add(time.Minute)
	if _, ok, err := memory.Get(ctx, "key"); err != nil || ok {
		t.Fatalf("Get() after ttl = _, %v, %v; want miss", ok, err)
	}
}

func TestBoundedMemoryEvictsOldestEntry(t *testing.T) {
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	memory, err := newBoundedMemoryWithClock(func() time.Time { return now }, 2)
	if err != nil {
		t.Fatalf("newBoundedMemoryWithClock() error = %v", err)
	}
	ctx := context.Background()

	if err := memory.Set(ctx, "a", []byte("a"), 0); err != nil {
		t.Fatalf("Set(a) error = %v", err)
	}
	now = now.Add(time.Second)
	if err := memory.Set(ctx, "b", []byte("b"), 0); err != nil {
		t.Fatalf("Set(b) error = %v", err)
	}
	now = now.Add(time.Second)
	if err := memory.Set(ctx, "c", []byte("c"), 0); err != nil {
		t.Fatalf("Set(c) error = %v", err)
	}

	if _, ok, err := memory.Get(ctx, "a"); err != nil || ok {
		t.Fatalf("Get(a) = _, %v, %v; want miss", ok, err)
	}
	if _, ok, err := memory.Get(ctx, "b"); err != nil || !ok {
		t.Fatalf("Get(b) = _, %v, %v; want hit", ok, err)
	}
	if _, ok, err := memory.Get(ctx, "c"); err != nil || !ok {
		t.Fatalf("Get(c) = _, %v, %v; want hit", ok, err)
	}
}

func TestBoundedMemoryEvictsExpiredEntriesFirst(t *testing.T) {
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	memory, err := newBoundedMemoryWithClock(func() time.Time { return now }, 2)
	if err != nil {
		t.Fatalf("newBoundedMemoryWithClock() error = %v", err)
	}
	ctx := context.Background()

	if err := memory.Set(ctx, "a", []byte("a"), time.Second); err != nil {
		t.Fatalf("Set(a) error = %v", err)
	}
	if err := memory.Set(ctx, "b", []byte("b"), 0); err != nil {
		t.Fatalf("Set(b) error = %v", err)
	}
	now = now.Add(2 * time.Second)
	if err := memory.Set(ctx, "c", []byte("c"), 0); err != nil {
		t.Fatalf("Set(c) error = %v", err)
	}

	if _, ok, err := memory.Get(ctx, "a"); err != nil || ok {
		t.Fatalf("Get(a) = _, %v, %v; want miss", ok, err)
	}
	if _, ok, err := memory.Get(ctx, "b"); err != nil || !ok {
		t.Fatalf("Get(b) = _, %v, %v; want hit", ok, err)
	}
	if _, ok, err := memory.Get(ctx, "c"); err != nil || !ok {
		t.Fatalf("Get(c) = _, %v, %v; want hit", ok, err)
	}
}

func TestNewBoundedMemoryValidation(t *testing.T) {
	if _, err := NewBoundedMemory(-1); !errors.Is(err, ErrInvalidCacheSize) {
		t.Fatalf("NewBoundedMemory(-1) error = %v, want ErrInvalidCacheSize", err)
	}
}

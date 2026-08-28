package cache_test

import (
	"context"
	"testing"

	"github.com/im10furry/saas/cache"
	tenantctx "github.com/im10furry/saas/core/context"
	"github.com/im10furry/saas/core/types"
)

func TestCacheIsolationAcrossTenants(t *testing.T) {
	scoped := cache.NewTenantCache(cache.NewMemory())
	ctxA := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})
	ctxB := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-b"})

	if err := scoped.Set(ctxA, "same-key", []byte("a"), 0); err != nil {
		t.Fatalf("Set(ctxA) error = %v", err)
	}
	if err := scoped.Set(ctxB, "same-key", []byte("b"), 0); err != nil {
		t.Fatalf("Set(ctxB) error = %v", err)
	}

	gotA, ok, err := scoped.Get(ctxA, "same-key")
	if err != nil || !ok || string(gotA) != "a" {
		t.Fatalf("Get(ctxA) = %q, %v, %v; want a, true, nil", gotA, ok, err)
	}
	gotB, ok, err := scoped.Get(ctxB, "same-key")
	if err != nil || !ok || string(gotB) != "b" {
		t.Fatalf("Get(ctxB) = %q, %v, %v; want b, true, nil", gotB, ok, err)
	}
}

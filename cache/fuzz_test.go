package cache

import (
	"context"
	"errors"
	"strings"
	"testing"

	tenantctx "github.com/darkinno-tech/saas/core/context"
	"github.com/darkinno-tech/saas/core/types"
)

func FuzzKeyBuilder(f *testing.F) {
	f.Add("profile")
	f.Add("t2:unsafe")
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-fuzz"})
	builder := KeyBuilder{}
	f.Fuzz(func(t *testing.T, key string) {
		built, err := builder.Build(ctx, key)
		unsafe := key == "" || strings.HasPrefix(key, tenantPrefix) || strings.HasPrefix(key, legacyTenantPrefix) || strings.HasPrefix(key, globalPrefix)
		if unsafe {
			if !errors.Is(err, ErrUnsafeKey) {
				t.Fatalf("Build(%q) error = %v, want ErrUnsafeKey", key, err)
			}
			return
		}
		if err != nil {
			t.Fatalf("Build(%q) error = %v", key, err)
		}
		if !strings.HasPrefix(built, tenantPrefix) {
			t.Fatalf("Build(%q) = %q, want tenant prefix %q", key, built, tenantPrefix)
		}
	})
}

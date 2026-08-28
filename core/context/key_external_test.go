package tenantctx_test

import (
	"context"
	"testing"

	tenantctx "github.com/im10furry/saas/core/context"
	"github.com/im10furry/saas/core/types"
)

func TestTenantContextDoesNotCollideWithForeignKeys(t *testing.T) {
	type loggingContextKey string
	type tracingContextKey struct{}

	parent := context.WithValue(context.Background(), loggingContextKey("tenant"), "logger-value")
	ctx := tenantctx.WithTenant(parent, types.Tenant{ID: "tenant-a"})
	ctx = context.WithValue(ctx, tracingContextKey{}, "trace-value")

	tenant, ok := tenantctx.FromContext(ctx)
	if !ok {
		t.Fatal("FromContext() ok = false, want true")
	}
	if tenant.ID != "tenant-a" {
		t.Fatalf("FromContext() tenant ID = %q, want tenant-a", tenant.ID)
	}
}

package concurrency_test

import (
	"context"
	"testing"

	tenantctx "github.com/im10furry/saas/core/context"
	"github.com/im10furry/saas/core/types"
)

func TestDetachPreventsTenantLeakToLongLivedWork(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	requestCtx := tenantctx.WithTenant(parent, types.Tenant{ID: "tenant-a"})
	cancel()

	detached := tenantctx.Detach(requestCtx)
	if _, ok := tenantctx.FromContext(detached); ok {
		t.Fatal("detached context still has tenant")
	}
	if err := detached.Err(); err != nil {
		t.Fatalf("detached context err = %v, want nil", err)
	}
}

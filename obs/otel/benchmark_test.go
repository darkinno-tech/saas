package otel

import (
	"context"
	"testing"

	tenantctx "github.com/darkinno-tech/saas/core/context"
	"github.com/darkinno-tech/saas/core/types"
)

func BenchmarkSpanAttributes(b *testing.B) {
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})
	b.ReportAllocs()
	for range b.N {
		_ = SpanAttributes(ctx)
	}
}

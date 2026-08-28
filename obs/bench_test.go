package obs

import (
	"context"
	"log/slog"
	"testing"

	tenantctx "github.com/darkinno-tech/saas/core/context"
	"github.com/darkinno-tech/saas/core/types"
)

func BenchmarkFields(b *testing.B) {
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})
	b.ReportAllocs()
	for range b.N {
		_ = Fields(ctx)
	}
}

func BenchmarkSlogAttrs(b *testing.B) {
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})
	b.ReportAllocs()
	for range b.N {
		_ = SlogAttrs(ctx)
	}
}

func BenchmarkRedactSlogAttrs(b *testing.B) {
	attrs := []slog.Attr{
		slog.String("tenant_id", "tenant-a"),
		slog.String("api_key", "secret"),
		slog.Group("nested", slog.String("password", "pw"), slog.String("safe", "value")),
	}
	b.ReportAllocs()
	for range b.N {
		_ = RedactSlogAttrs(attrs...)
	}
}

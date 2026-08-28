package rpc

import (
	"context"
	"errors"
	"testing"

	tenantctx "github.com/im10furry/saas/core/context"
	"github.com/im10furry/saas/core/types"
)

func TestInjectAndExtractTenant(t *testing.T) {
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})
	carrier := MapCarrier{}

	if err := InjectTenant(ctx, carrier, ""); err != nil {
		t.Fatalf("InjectTenant() error = %v", err)
	}
	id, err := ExtractTenant(carrier, "", types.TenantIDStrategyString)
	if err != nil {
		t.Fatalf("ExtractTenant() error = %v", err)
	}
	if id != "tenant-a" {
		t.Fatalf("ExtractTenant() = %q, want tenant-a", id)
	}
}

func TestInjectExtractValidation(t *testing.T) {
	if err := InjectTenant(context.Background(), MapCarrier{}, ""); !errors.Is(err, ErrNoTenantMetadata) {
		t.Fatalf("InjectTenant(no tenant) error = %v, want ErrNoTenantMetadata", err)
	}
	if err := InjectTenant(context.Background(), nil, ""); !errors.Is(err, ErrInvalidCarrier) {
		t.Fatalf("InjectTenant(nil carrier) error = %v, want ErrInvalidCarrier", err)
	}
	if _, err := ExtractTenant(MapCarrier{}, "", types.TenantIDStrategyString); !errors.Is(err, ErrNoTenantMetadata) {
		t.Fatalf("ExtractTenant(missing) error = %v, want ErrNoTenantMetadata", err)
	}
	if _, err := ExtractTenant(MapCarrier{DefaultTenantMetadataKey: "bad-int"}, "", types.TenantIDStrategyInt); !errors.Is(err, types.ErrInvalidTenantID) {
		t.Fatalf("ExtractTenant(invalid id) error = %v, want ErrInvalidTenantID", err)
	}
}

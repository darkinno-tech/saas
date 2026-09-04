package grpcsaas

import (
	"context"
	"testing"

	tenantctx "github.com/darkinno-tech/saas/core/context"
	"github.com/darkinno-tech/saas/core/store"
	"github.com/darkinno-tech/saas/core/types"
	baserpc "github.com/darkinno-tech/saas/rpc"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestTenantUnaryServerInterceptorCustomMetadataKey(t *testing.T) {
	t.Parallel()
	backing := store.NewMemoryStore()
	if err := backing.Create(context.Background(), types.Tenant{ID: "tenant-a", Status: types.TenantStatusActive}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	interceptor := TenantUnaryServerInterceptor(backing, WithMetadataKey("x-company"))
	response, err := interceptor(
		metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-company", "tenant-a")),
		nil, &grpc.UnaryServerInfo{},
		func(ctx context.Context, req any) (any, error) {
			tenant, ok := tenantctx.FromContext(ctx)
			if !ok || tenant.ID != "tenant-a" {
				t.Fatalf("tenant from context = %#v, %v", tenant, ok)
			}
			return "custom-key", nil
		})
	if err != nil || response != "custom-key" {
		t.Fatalf("response, error = %v, %v; want custom-key, nil", response, err)
	}
}

func TestTenantUnaryServerInterceptorStrategyAndParseError(t *testing.T) {
	t.Parallel()
	backing := store.NewMemoryStore()
	if err := backing.Create(context.Background(), types.Tenant{ID: "42", Status: types.TenantStatusActive}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	interceptor := TenantUnaryServerInterceptor(backing, WithTenantIDStrategy(types.TenantIDStrategyInt))

	response, err := interceptor(
		metadata.NewIncomingContext(context.Background(), metadata.Pairs(baserpc.DefaultTenantMetadataKey, "42")),
		nil, &grpc.UnaryServerInfo{},
		func(ctx context.Context, req any) (any, error) {
			tenant, ok := tenantctx.FromContext(ctx)
			if !ok || tenant.ID != "42" {
				t.Fatalf("tenant from context = %#v, %v", tenant, ok)
			}
			return "int", nil
		})
	if err != nil || response != "int" {
		t.Fatalf("response, error = %v, %v; want int, nil", response, err)
	}

	// Malformed value for the int strategy must fail extraction -> unauthenticated.
	bad := metadata.NewIncomingContext(context.Background(), metadata.Pairs(baserpc.DefaultTenantMetadataKey, "not-an-int"))
	if _, err := interceptor(bad, nil, &grpc.UnaryServerInfo{}, func(context.Context, any) (any, error) {
		return "unexpected", nil
	}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("malformed tenant code = %s, want %s", status.Code(err), codes.Unauthenticated)
	}
}

func TestTenantUnaryServerInterceptorUnknownTenant(t *testing.T) {
	t.Parallel()
	interceptor := TenantUnaryServerInterceptor(store.NewMemoryStore())
	_, err := interceptor(grpcContext("tenant-missing"), nil, &grpc.UnaryServerInfo{}, func(context.Context, any) (any, error) {
		return "unexpected", nil
	})
	if code, message := status.Code(err), status.Convert(err).Message(); code != codes.PermissionDenied || message != "tenant_forbidden" {
		t.Fatalf("unknown tenant = code %s message %q, want %s tenant_forbidden", code, message, codes.PermissionDenied)
	}
}

func TestTenantStatusUnaryServerInterceptorAllowsActive(t *testing.T) {
	t.Parallel()
	interceptor := TenantStatusUnaryServerInterceptor()
	called := false
	response, err := interceptor(
		tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a", Status: types.TenantStatusActive}),
		nil, &grpc.UnaryServerInfo{},
		func(context.Context, any) (any, error) {
			called = true
			return "active", nil
		})
	if err != nil || response != "active" || !called {
		t.Fatalf("response, error, called = %v, %v, %v; want active, nil, true", response, err, called)
	}
}

func TestMetadataCarrierGetAndSet(t *testing.T) {
	t.Parallel()
	carrier := metadataCarrier{md: metadata.MD{}}
	if _, ok := carrier.Get("missing"); ok {
		t.Fatal("Get(missing) ok = true, want false")
	}
	carrier.Set("x-key", "first")
	carrier.Set("x-key", "second") // overwrite existing value.
	value, ok := carrier.Get("x-key")
	if !ok || value != "second" {
		t.Fatalf("Get(x-key) = %q, %v; want second, true", value, ok)
	}
}
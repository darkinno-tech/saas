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

func TestTenantUnaryServerInterceptorInjectsTenant(t *testing.T) {
	backing := store.NewMemoryStore()
	if err := backing.Create(context.Background(), types.Tenant{ID: "tenant-a", Status: types.TenantStatusActive}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := backing.Create(context.Background(), types.Tenant{ID: "tenant-b", Status: types.TenantStatusSuspended}); err != nil {
		t.Fatalf("Create(suspended) error = %v", err)
	}

	interceptor := TenantUnaryServerInterceptor(backing)
	response, err := interceptor(grpcContext("tenant-a"), "request", &grpc.UnaryServerInfo{}, func(ctx context.Context, req any) (any, error) {
		tenant, ok := tenantctx.FromContext(ctx)
		if !ok {
			t.Fatal("tenant missing from context")
		}
		return tenant.ID.String(), nil
	})
	if err != nil {
		t.Fatalf("interceptor() error = %v", err)
	}
	if response != "tenant-a" {
		t.Fatalf("response = %v, want tenant-a", response)
	}

	_, err = interceptor(grpcContext("tenant-b"), "request", &grpc.UnaryServerInfo{}, func(ctx context.Context, req any) (any, error) {
		return "unexpected", nil
	})
	if code := status.Code(err); code != codes.PermissionDenied {
		t.Fatalf("suspended code = %s, want %s", code, codes.PermissionDenied)
	}
}

func TestTenantUnaryServerInterceptorRejectsMissingTenant(t *testing.T) {
	interceptor := TenantUnaryServerInterceptor(store.NewMemoryStore())

	_, err := interceptor(context.Background(), nil, &grpc.UnaryServerInfo{}, func(ctx context.Context, req any) (any, error) {
		return nil, nil
	})
	if code := status.Code(err); code != codes.Unauthenticated {
		t.Fatalf("code = %s, want %s", code, codes.Unauthenticated)
	}
}

func TestTenantStatusUnaryServerInterceptor(t *testing.T) {
	interceptor := TenantStatusUnaryServerInterceptor()
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a", Status: types.TenantStatusSuspended})

	_, err := interceptor(ctx, nil, &grpc.UnaryServerInfo{}, func(ctx context.Context, req any) (any, error) {
		return nil, nil
	})
	if code := status.Code(err); code != codes.PermissionDenied {
		t.Fatalf("code = %s, want %s", code, codes.PermissionDenied)
	}
}

func TestTenantStreamServerInterceptorInjectsTenant(t *testing.T) {
	backing := store.NewMemoryStore()
	if err := backing.Create(context.Background(), types.Tenant{ID: "tenant-a", Status: types.TenantStatusActive}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	interceptor := TenantStreamServerInterceptor(backing)
	stream := &mockServerStream{ctx: grpcContext("tenant-a")}
	err := interceptor(nil, stream, &grpc.StreamServerInfo{}, func(srv any, stream grpc.ServerStream) error {
		tenant, ok := tenantctx.FromContext(stream.Context())
		if !ok {
			t.Fatal("tenant missing from stream context")
		}
		if tenant.ID != "tenant-a" {
			t.Fatalf("tenant ID = %s, want tenant-a", tenant.ID)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("stream interceptor error = %v", err)
	}
}

func TestTenantStreamServerInterceptorRejectsMissingTenant(t *testing.T) {
	interceptor := TenantStreamServerInterceptor(store.NewMemoryStore())
	err := interceptor(nil, &mockServerStream{ctx: context.Background()}, &grpc.StreamServerInfo{}, func(srv any, stream grpc.ServerStream) error {
		return nil
	})
	if code := status.Code(err); code != codes.Unauthenticated {
		t.Fatalf("code = %s, want %s", code, codes.Unauthenticated)
	}
}

func TestTenantStatusStreamServerInterceptor(t *testing.T) {
	interceptor := TenantStatusStreamServerInterceptor()

	err := interceptor(nil, &mockServerStream{ctx: context.Background()}, &grpc.StreamServerInfo{}, func(srv any, stream grpc.ServerStream) error {
		return nil
	})
	if code := status.Code(err); code != codes.Unauthenticated {
		t.Fatalf("missing tenant code = %s, want %s", code, codes.Unauthenticated)
	}

	err = interceptor(nil, &mockServerStream{ctx: tenantctx.WithTenant(context.Background(), types.Tenant{
		ID:     "tenant-a",
		Status: types.TenantStatusSuspended,
	})}, &grpc.StreamServerInfo{}, func(srv any, stream grpc.ServerStream) error {
		return nil
	})
	if code := status.Code(err); code != codes.PermissionDenied {
		t.Fatalf("inactive tenant code = %s, want %s", code, codes.PermissionDenied)
	}

	called := false
	err = interceptor(nil, &mockServerStream{ctx: tenantctx.WithTenant(context.Background(), types.Tenant{
		ID:     "tenant-a",
		Status: types.TenantStatusActive,
	})}, &grpc.StreamServerInfo{}, func(srv any, stream grpc.ServerStream) error {
		called = true
		return nil
	})
	if err != nil {
		t.Fatalf("active tenant stream error = %v", err)
	}
	if !called {
		t.Fatal("active tenant stream handler was not called")
	}
}

func TestHostUnaryServerInterceptor(t *testing.T) {
	interceptor := HostUnaryServerInterceptor()
	_, err := interceptor(context.Background(), nil, &grpc.UnaryServerInfo{}, func(ctx context.Context, req any) (any, error) {
		return "ok", nil
	})
	if code := status.Code(err); code != codes.PermissionDenied {
		t.Fatalf("code = %s, want %s", code, codes.PermissionDenied)
	}

	response, err := interceptor(tenantctx.WithHost(context.Background()), nil, &grpc.UnaryServerInfo{}, func(ctx context.Context, req any) (any, error) {
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("host interceptor error = %v", err)
	}
	if response != "ok" {
		t.Fatalf("response = %v, want ok", response)
	}
}

func TestHostStreamServerInterceptor(t *testing.T) {
	interceptor := HostStreamServerInterceptor()

	err := interceptor(nil, &mockServerStream{ctx: context.Background()}, &grpc.StreamServerInfo{}, func(srv any, stream grpc.ServerStream) error {
		return nil
	})
	if code := status.Code(err); code != codes.PermissionDenied {
		t.Fatalf("without host code = %s, want %s", code, codes.PermissionDenied)
	}

	called := false
	err = interceptor(nil, &mockServerStream{ctx: tenantctx.WithHost(context.Background())}, &grpc.StreamServerInfo{}, func(srv any, stream grpc.ServerStream) error {
		called = true
		return nil
	})
	if err != nil {
		t.Fatalf("host stream error = %v", err)
	}
	if !called {
		t.Fatal("host stream handler was not called")
	}
}

func grpcContext(tenantID string) context.Context {
	return metadata.NewIncomingContext(context.Background(), metadata.Pairs(baserpc.DefaultTenantMetadataKey, tenantID))
}

type mockServerStream struct {
	ctx context.Context
}

func (stream *mockServerStream) SetHeader(metadata.MD) error {
	return nil
}

func (stream *mockServerStream) SendHeader(metadata.MD) error {
	return nil
}

func (stream *mockServerStream) SetTrailer(metadata.MD) {
}

func (stream *mockServerStream) Context() context.Context {
	return stream.ctx
}

func (stream *mockServerStream) SendMsg(any) error {
	return nil
}

func (stream *mockServerStream) RecvMsg(any) error {
	return nil
}

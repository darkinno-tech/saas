package grpcsaas

import (
	"context"
	"errors"
	"testing"

	tenantctx "github.com/darkinno-tech/saas/core/context"
	"github.com/darkinno-tech/saas/core/store"
	"github.com/darkinno-tech/saas/core/types"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestTenantInterceptorsDeploymentResolution(t *testing.T) {
	backing := store.NewMemoryStore()
	if err := backing.Create(context.Background(), types.Tenant{ID: "tenant-a", Status: types.TenantStatusActive}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	resolver := deploymentResolverFunc(func(context.Context, types.Tenant) (types.DeploymentUnit, error) {
		return types.DeploymentUnit{ID: "eu-central-1", Status: types.DeploymentUnitStatusActive}, nil
	})

	unary := TenantUnaryServerInterceptor(backing, WithDeploymentResolver(resolver))
	response, err := unary(grpcContext("tenant-a"), nil, &grpc.UnaryServerInfo{}, func(ctx context.Context, req any) (any, error) {
		unit, ok := tenantctx.DeploymentFromContext(ctx)
		if !ok || unit.ID != "eu-central-1" {
			t.Fatalf("unary deployment context = %#v, %v; want eu-central-1", unit, ok)
		}
		return "unary", nil
	})
	if err != nil || response != "unary" {
		t.Fatalf("unary response, error = %v, %v; want unary, nil", response, err)
	}

	streamInterceptor := TenantStreamServerInterceptor(backing, WithDeploymentResolver(resolver))
	err = streamInterceptor(nil, &mockServerStream{ctx: grpcContext("tenant-a")}, &grpc.StreamServerInfo{}, func(srv any, stream grpc.ServerStream) error {
		unit, ok := tenantctx.DeploymentFromContext(stream.Context())
		if !ok || unit.ID != "eu-central-1" {
			t.Fatalf("stream deployment context = %#v, %v; want eu-central-1", unit, ok)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("stream interceptor error = %v", err)
	}

	handlerCalled := false
	rejecting := TenantUnaryServerInterceptor(backing, WithDeploymentResolver(deploymentResolverFunc(func(context.Context, types.Tenant) (types.DeploymentUnit, error) {
		return types.DeploymentUnit{}, errors.New("assignment missing")
	})))
	_, err = rejecting(grpcContext("tenant-a"), nil, &grpc.UnaryServerInfo{}, func(context.Context, any) (any, error) {
		handlerCalled = true
		return "unexpected", nil
	})
	if code, message := status.Code(err), status.Convert(err).Message(); code != codes.PermissionDenied || message != "deployment_unavailable" {
		t.Fatalf("missing assignment error = code %s message %q, want %s deployment_unavailable", code, message, codes.PermissionDenied)
	}
	if handlerCalled {
		t.Fatal("handler ran after deployment resolution failed")
	}
}

func TestTenantInterceptorsRejectInvalidDeploymentUnit(t *testing.T) {
	backing := store.NewMemoryStore()
	if err := backing.Create(context.Background(), types.Tenant{ID: "tenant-a", Status: types.TenantStatusActive}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	for _, testCase := range []struct {
		name string
		unit types.DeploymentUnit
	}{
		{name: "empty ID", unit: types.DeploymentUnit{Status: types.DeploymentUnitStatusActive}},
		{name: "disabled", unit: types.DeploymentUnit{ID: "eu-central-1", Status: types.DeploymentUnitStatusDisabled}},
	} {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			handlerCalled := false
			interceptor := TenantUnaryServerInterceptor(backing, WithDeploymentResolver(deploymentResolverFunc(func(context.Context, types.Tenant) (types.DeploymentUnit, error) {
				return testCase.unit, nil
			})))

			_, err := interceptor(grpcContext("tenant-a"), nil, &grpc.UnaryServerInfo{}, func(context.Context, any) (any, error) {
				handlerCalled = true
				return "unexpected", nil
			})
			if code, message := status.Code(err), status.Convert(err).Message(); code != codes.PermissionDenied || message != "deployment_unavailable" {
				t.Fatalf("error = code %s message %q, want %s deployment_unavailable", code, message, codes.PermissionDenied)
			}
			if handlerCalled {
				t.Fatal("handler ran after an invalid deployment unit was resolved")
			}
		})
	}
}

type deploymentResolverFunc func(context.Context, types.Tenant) (types.DeploymentUnit, error)

func (resolver deploymentResolverFunc) Resolve(ctx context.Context, tenant types.Tenant) (types.DeploymentUnit, error) {
	return resolver(ctx, tenant)
}

package kratossaas

import (
	"context"
	"errors"
	"net/http"
	"testing"

	tenantctx "github.com/im10furry/saas/core/context"
	"github.com/im10furry/saas/core/resolver"
	"github.com/im10furry/saas/core/store"
	"github.com/im10furry/saas/core/types"

	kerrors "github.com/go-kratos/kratos/v2/errors"
)

func TestTenantMiddlewareDeploymentResolution(t *testing.T) {
	backing := store.NewMemoryStore()
	if err := backing.Create(context.Background(), types.Tenant{ID: "tenant-a", Status: types.TenantStatusActive}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	tenants := resolver.NewComposite(resolver.NewHeaderContrib("", types.TenantIDStrategyString))

	handler := TenantMiddleware(tenants, backing, WithDeploymentResolver(deploymentResolverFunc(func(context.Context, types.Tenant) (types.DeploymentUnit, error) {
		return types.DeploymentUnit{ID: "eu-central-1", Status: types.DeploymentUnitStatusActive}, nil
	})))(func(ctx context.Context, req any) (any, error) {
		unit, ok := tenantctx.DeploymentFromContext(ctx)
		if !ok || unit.ID != "eu-central-1" {
			t.Fatalf("deployment context = %#v, %v; want eu-central-1", unit, ok)
		}
		return "ok", nil
	})

	response, err := handler(kratosContext("tenant-a"), nil)
	if err != nil || response != "ok" {
		t.Fatalf("success response, error = %v, %v; want ok, nil", response, err)
	}

	handlerCalled := false
	rejecting := TenantMiddleware(tenants, backing, WithDeploymentResolver(deploymentResolverFunc(func(context.Context, types.Tenant) (types.DeploymentUnit, error) {
		return types.DeploymentUnit{}, errors.New("assignment missing")
	})))(func(context.Context, any) (any, error) {
		handlerCalled = true
		return "unexpected", nil
	})

	_, err = rejecting(kratosContext("tenant-a"), nil)
	if code, reason := kerrors.Code(err), kerrors.Reason(err); code != http.StatusForbidden || reason != reasonDeploymentUnavailable {
		t.Fatalf("missing assignment error = code %d reason %q, want %d %q", code, reason, http.StatusForbidden, reasonDeploymentUnavailable)
	}
	if handlerCalled {
		t.Fatal("handler ran after deployment resolution failed")
	}
}

func TestTenantMiddlewareRejectsInvalidDeploymentUnit(t *testing.T) {
	backing := store.NewMemoryStore()
	if err := backing.Create(context.Background(), types.Tenant{ID: "tenant-a", Status: types.TenantStatusActive}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	tenants := resolver.NewComposite(resolver.NewHeaderContrib("", types.TenantIDStrategyString))

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
			handler := TenantMiddleware(tenants, backing, WithDeploymentResolver(deploymentResolverFunc(func(context.Context, types.Tenant) (types.DeploymentUnit, error) {
				return testCase.unit, nil
			})))(func(context.Context, any) (any, error) {
				handlerCalled = true
				return "unexpected", nil
			})

			_, err := handler(kratosContext("tenant-a"), nil)
			if code, reason := kerrors.Code(err), kerrors.Reason(err); code != http.StatusForbidden || reason != reasonDeploymentUnavailable {
				t.Fatalf("error = code %d reason %q, want %d %q", code, reason, http.StatusForbidden, reasonDeploymentUnavailable)
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

package fibersaas

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	tenantctx "github.com/darkinno-tech/saas/core/context"
	"github.com/darkinno-tech/saas/core/resolver"
	"github.com/darkinno-tech/saas/core/store"
	"github.com/darkinno-tech/saas/core/types"

	"github.com/gofiber/fiber/v2"
)

func TestTenantMiddlewareDeploymentResolution(t *testing.T) {
	backing := store.NewMemoryStore()
	if err := backing.Create(context.Background(), types.Tenant{ID: "tenant-a", Status: types.TenantStatusActive}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	tenants := resolver.NewComposite(resolver.NewHeaderContrib("", types.TenantIDStrategyString))

	app := fiber.New()
	app.Use(TenantMiddleware(tenants, backing, WithDeploymentResolver(deploymentResolverFunc(func(context.Context, types.Tenant) (types.DeploymentUnit, error) {
		return types.DeploymentUnit{ID: "eu-central-1", Status: types.DeploymentUnitStatusActive}, nil
	}))))
	app.Get("/orders", func(c *fiber.Ctx) error {
		unit, ok := tenantctx.DeploymentFromContext(c.UserContext())
		if !ok || unit.ID != "eu-central-1" {
			t.Fatalf("deployment context = %#v, %v; want eu-central-1", unit, ok)
		}
		return c.SendStatus(http.StatusNoContent)
	})

	request := httptest.NewRequest(http.MethodGet, "/orders", nil)
	request.Header.Set(resolver.DefaultHeaderName, "tenant-a")
	response, err := app.Test(request)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("success status = %d, want %d", response.StatusCode, http.StatusNoContent)
	}

	handlerCalled := false
	rejecting := fiber.New()
	rejecting.Use(TenantMiddleware(tenants, backing, WithDeploymentResolver(deploymentResolverFunc(func(context.Context, types.Tenant) (types.DeploymentUnit, error) {
		return types.DeploymentUnit{}, errors.New("assignment missing")
	}))))
	rejecting.Get("/orders", func(c *fiber.Ctx) error {
		handlerCalled = true
		return c.SendStatus(http.StatusNoContent)
	})

	request = httptest.NewRequest(http.MethodGet, "/orders", nil)
	request.Header.Set(resolver.DefaultHeaderName, "tenant-a")
	response, err = rejecting.Test(request)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}
	if response.StatusCode != http.StatusForbidden || bodyString(t, response) != `{"error":"deployment_unavailable"}` {
		t.Fatalf("missing assignment response = %d, want 403 deployment_unavailable", response.StatusCode)
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
			app := fiber.New()
			app.Use(TenantMiddleware(tenants, backing, WithDeploymentResolver(deploymentResolverFunc(func(context.Context, types.Tenant) (types.DeploymentUnit, error) {
				return testCase.unit, nil
			}))))
			app.Get("/orders", func(c *fiber.Ctx) error {
				handlerCalled = true
				return c.SendStatus(http.StatusNoContent)
			})

			request := httptest.NewRequest(http.MethodGet, "/orders", nil)
			request.Header.Set(resolver.DefaultHeaderName, "tenant-a")
			response, err := app.Test(request)
			if err != nil {
				t.Fatalf("app.Test() error = %v", err)
			}
			if response.StatusCode != http.StatusForbidden || bodyString(t, response) != `{"error":"deployment_unavailable"}` {
				t.Fatalf("response = %d, want 403 deployment_unavailable", response.StatusCode)
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

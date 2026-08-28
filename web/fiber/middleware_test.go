package fibersaas

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	tenantctx "github.com/im10furry/saas/core/context"
	"github.com/im10furry/saas/core/resolver"
	"github.com/im10furry/saas/core/store"
	"github.com/im10furry/saas/core/types"

	"github.com/gofiber/fiber/v2"
)

func TestTenantMiddlewareRejectsInactiveTenantByDefault(t *testing.T) {
	backing := store.NewMemoryStore()
	if err := backing.Create(context.Background(), types.Tenant{ID: "tenant-a", Status: types.TenantStatusActive}); err != nil {
		t.Fatalf("Create(active) error = %v", err)
	}
	if err := backing.Create(context.Background(), types.Tenant{ID: "tenant-b", Status: types.TenantStatusSuspended}); err != nil {
		t.Fatalf("Create(suspended) error = %v", err)
	}

	app := fiber.New()
	app.Use(TenantMiddleware(resolver.NewComposite(resolver.NewHeaderContrib("", types.TenantIDStrategyString)), backing))
	app.Get("/ok", func(c *fiber.Ctx) error {
		tenant, ok := tenantctx.FromContext(c.UserContext())
		if !ok {
			t.Fatal("tenant missing from user context")
		}
		return c.SendString(tenant.ID.String())
	})

	response := fiberTest(t, app, "tenant-a")
	if response.StatusCode != http.StatusOK || bodyString(t, response) != "tenant-a" {
		t.Fatalf("active response = %d, want 200 tenant-a", response.StatusCode)
	}

	response = fiberTest(t, app, "tenant-b")
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("suspended status = %d, want 403", response.StatusCode)
	}
}

func TestTenantMiddlewareRejectsMissingTenant(t *testing.T) {
	app := fiber.New()
	app.Use(TenantMiddleware(resolver.NewComposite(resolver.NewHeaderContrib("", types.TenantIDStrategyString)), store.NewMemoryStore()))
	app.Get("/ok", func(c *fiber.Ctx) error {
		return c.SendStatus(http.StatusOK)
	})

	request := httptest.NewRequest(http.MethodGet, "/ok", nil)
	response, err := app.Test(request)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing tenant status = %d, want 401", response.StatusCode)
	}
}

func TestTenantMiddlewareReturnsTimeoutBeforeCallingTenantPage(t *testing.T) {
	app := fiber.New()
	called := false
	app.Use(TenantMiddleware(
		resolver.NewComposite(resolver.NewHeaderContrib("", types.TenantIDStrategyString)),
		timeoutTenantStore{err: context.DeadlineExceeded},
	))
	app.Get("/orders", func(c *fiber.Ctx) error {
		called = true
		return c.SendStatus(http.StatusOK)
	})

	request := httptest.NewRequest(http.MethodGet, "/orders", nil)
	request.Header.Set(resolver.DefaultHeaderName, "tenant-a")
	response, err := app.Test(request)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}
	if response.StatusCode != http.StatusRequestTimeout || bodyString(t, response) != `{"error":"tenant_forbidden"}` {
		t.Fatalf("timeout response = %d, want 408 tenant_forbidden", response.StatusCode)
	}
	if called {
		t.Fatal("tenant page handler was called after lookup timeout")
	}
}

func TestTenantStatusGuard(t *testing.T) {
	app := fiber.New()
	app.Get("/missing", TenantStatusGuard(), func(c *fiber.Ctx) error {
		return c.SendString("unexpected")
	})
	app.Get("/inactive", injectFiberTenant(types.TenantStatusSuspended), TenantStatusGuard(), func(c *fiber.Ctx) error {
		return c.SendString("unexpected")
	})
	app.Get("/active", injectFiberTenant(types.TenantStatusActive), TenantStatusGuard(), func(c *fiber.Ctx) error {
		return c.SendString("ok")
	})

	request := httptest.NewRequest(http.MethodGet, "/missing", nil)
	response, err := app.Test(request)
	if err != nil {
		t.Fatalf("app.Test(missing) error = %v", err)
	}
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing tenant status = %d, want 401", response.StatusCode)
	}

	request = httptest.NewRequest(http.MethodGet, "/inactive", nil)
	response, err = app.Test(request)
	if err != nil {
		t.Fatalf("app.Test(inactive) error = %v", err)
	}
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("inactive tenant status = %d, want 403", response.StatusCode)
	}

	request = httptest.NewRequest(http.MethodGet, "/active", nil)
	response, err = app.Test(request)
	if err != nil {
		t.Fatalf("app.Test(active) error = %v", err)
	}
	if response.StatusCode != http.StatusOK || bodyString(t, response) != "ok" {
		t.Fatalf("active tenant response = %d, want 200 ok", response.StatusCode)
	}
}

func TestHostGuardMiddleware(t *testing.T) {
	app := fiber.New()
	app.Get("/host", HostGuardMiddleware(), func(c *fiber.Ctx) error {
		return c.SendStatus(http.StatusOK)
	})
	app.Get("/host-ok", func(c *fiber.Ctx) error {
		c.SetUserContext(tenantctx.WithHost(c.UserContext()))
		return c.Next()
	}, HostGuardMiddleware(), func(c *fiber.Ctx) error {
		return c.SendStatus(http.StatusOK)
	})

	request := httptest.NewRequest(http.MethodGet, "/host", nil)
	response, err := app.Test(request)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("host guard without host status = %d, want 403", response.StatusCode)
	}

	request = httptest.NewRequest(http.MethodGet, "/host-ok", nil)
	response, err = app.Test(request)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("host guard with host status = %d, want 200", response.StatusCode)
	}
}

func fiberTest(t *testing.T, app *fiber.App, tenantID string) *http.Response {
	t.Helper()

	request := httptest.NewRequest(http.MethodGet, "/ok", nil)
	request.Header.Set(resolver.DefaultHeaderName, tenantID)
	response, err := app.Test(request)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}
	return response
}

func bodyString(t *testing.T, response *http.Response) string {
	t.Helper()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	return string(body)
}

func injectFiberTenant(status types.TenantStatus) fiber.Handler {
	return func(c *fiber.Ctx) error {
		c.SetUserContext(tenantctx.WithTenant(c.UserContext(), types.Tenant{
			ID:     "tenant-a",
			Status: status,
		}))
		return c.Next()
	}
}

type timeoutTenantStore struct {
	err error
}

func (store timeoutTenantStore) Get(context.Context, types.TenantID) (types.Tenant, error) {
	return types.Tenant{}, store.err
}

func (timeoutTenantStore) List(context.Context, store.ListFilter) ([]types.Tenant, error) {
	return nil, context.DeadlineExceeded
}

func (timeoutTenantStore) Create(context.Context, types.Tenant) error {
	return context.DeadlineExceeded
}

func (timeoutTenantStore) Update(context.Context, types.Tenant) error {
	return context.DeadlineExceeded
}

func (timeoutTenantStore) Delete(context.Context, types.TenantID) error {
	return context.DeadlineExceeded
}

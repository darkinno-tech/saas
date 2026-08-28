package echosaas

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	tenantctx "github.com/im10furry/saas/core/context"
	"github.com/im10furry/saas/core/resolver"
	"github.com/im10furry/saas/core/store"
	"github.com/im10furry/saas/core/types"

	"github.com/labstack/echo/v4"
)

func TestTenantMiddlewareRejectsInactiveTenantByDefault(t *testing.T) {
	backing := store.NewMemoryStore()
	if err := backing.Create(context.Background(), types.Tenant{ID: "tenant-a", Status: types.TenantStatusActive}); err != nil {
		t.Fatalf("Create(active) error = %v", err)
	}
	if err := backing.Create(context.Background(), types.Tenant{ID: "tenant-b", Status: types.TenantStatusSuspended}); err != nil {
		t.Fatalf("Create(suspended) error = %v", err)
	}

	router := echo.New()
	router.Use(TenantMiddleware(resolver.NewComposite(resolver.NewHeaderContrib("", types.TenantIDStrategyString)), backing))
	router.GET("/ok", func(c echo.Context) error {
		tenant, ok := tenantctx.FromContext(c.Request().Context())
		if !ok {
			t.Fatal("tenant missing from request context")
		}
		return c.String(http.StatusOK, tenant.ID.String())
	})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/ok", nil)
	request.Header.Set(resolver.DefaultHeaderName, "tenant-a")
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Body.String() != "tenant-a" {
		t.Fatalf("active response = %d %q, want 200 tenant-a", recorder.Code, recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/ok", nil)
	request.Header.Set(resolver.DefaultHeaderName, "tenant-b")
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("suspended status = %d, want 403", recorder.Code)
	}
}

func TestTenantMiddlewareRejectsMissingTenant(t *testing.T) {
	router := echo.New()
	router.Use(TenantMiddleware(resolver.NewComposite(resolver.NewHeaderContrib("", types.TenantIDStrategyString)), store.NewMemoryStore()))
	router.GET("/ok", func(c echo.Context) error {
		return c.NoContent(http.StatusOK)
	})

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/ok", nil))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("missing tenant status = %d, want 401", recorder.Code)
	}
}

func TestTenantMiddlewareReturnsTimeoutBeforeCallingTenantPage(t *testing.T) {
	router := echo.New()
	called := false
	router.Use(TenantMiddleware(
		resolver.NewComposite(resolver.NewHeaderContrib("", types.TenantIDStrategyString)),
		timeoutTenantStore{err: context.DeadlineExceeded},
	))
	router.GET("/orders", func(echo.Context) error {
		called = true
		return nil
	})

	request := httptest.NewRequest(http.MethodGet, "/orders", nil)
	request.Header.Set(resolver.DefaultHeaderName, "tenant-a")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusRequestTimeout || recorder.Body.String() != "{\"error\":\"tenant_forbidden\"}\n" {
		t.Fatalf("tenant timeout response = %d %q, want 408 tenant_forbidden", recorder.Code, recorder.Body.String())
	}
	if called {
		t.Fatal("tenant page handler ran after tenant lookup timeout")
	}
}

func TestTenantStatusGuard(t *testing.T) {
	router := echo.New()
	router.GET("/missing", func(c echo.Context) error {
		return c.String(http.StatusOK, "unexpected")
	}, TenantStatusGuard())
	router.GET("/inactive", func(c echo.Context) error {
		return c.String(http.StatusOK, "unexpected")
	}, injectEchoTenant(types.TenantStatusSuspended), TenantStatusGuard())
	router.GET("/active", func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	}, injectEchoTenant(types.TenantStatusActive), TenantStatusGuard())

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/missing", nil))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("missing tenant status = %d, want 401", recorder.Code)
	}

	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/inactive", nil))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("inactive tenant status = %d, want 403", recorder.Code)
	}

	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/active", nil))
	if recorder.Code != http.StatusOK || recorder.Body.String() != "ok" {
		t.Fatalf("active tenant response = %d %q, want 200 ok", recorder.Code, recorder.Body.String())
	}
}

func TestHostGuardMiddleware(t *testing.T) {
	router := echo.New()
	router.GET("/host", func(c echo.Context) error {
		return c.NoContent(http.StatusOK)
	}, HostGuardMiddleware())
	router.GET("/host-ok", func(c echo.Context) error {
		return c.NoContent(http.StatusOK)
	}, func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			request := c.Request()
			c.SetRequest(request.WithContext(tenantctx.WithHost(request.Context())))
			return next(c)
		}
	}, HostGuardMiddleware())

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/host", nil))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("host guard without host status = %d, want 403", recorder.Code)
	}

	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/host-ok", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("host guard with host status = %d, want 200", recorder.Code)
	}
}

func injectEchoTenant(status types.TenantStatus) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			request := c.Request()
			c.SetRequest(request.WithContext(tenantctx.WithTenant(request.Context(), types.Tenant{
				ID:     "tenant-a",
				Status: status,
			})))
			return next(c)
		}
	}
}

type timeoutTenantStore struct {
	err error
}

func (failing timeoutTenantStore) Get(context.Context, types.TenantID) (types.Tenant, error) {
	return types.Tenant{}, failing.err
}

func (failing timeoutTenantStore) List(context.Context, store.ListFilter) ([]types.Tenant, error) {
	return nil, failing.err
}

func (failing timeoutTenantStore) Create(context.Context, types.Tenant) error { return failing.err }

func (failing timeoutTenantStore) Update(context.Context, types.Tenant) error { return failing.err }

func (failing timeoutTenantStore) Delete(context.Context, types.TenantID) error { return failing.err }

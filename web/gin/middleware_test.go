package ginsaas

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	tenantctx "github.com/im10furry/saas/core/context"
	"github.com/im10furry/saas/core/resolver"
	"github.com/im10furry/saas/core/store"
	"github.com/im10furry/saas/core/types"

	"github.com/gin-gonic/gin"
)

func TestTenantMiddlewareRejectsInactiveTenantByDefault(t *testing.T) {
	gin.SetMode(gin.TestMode)
	backing := store.NewMemoryStore()
	active := types.Tenant{ID: "tenant-a", Name: "Tenant A", Status: types.TenantStatusActive}
	if err := backing.Create(context.Background(), active); err != nil {
		t.Fatalf("store.Create(active) error = %v", err)
	}
	if err := backing.Create(context.Background(), types.Tenant{ID: "tenant-b", Name: "Tenant B", Status: types.TenantStatusSuspended}); err != nil {
		t.Fatalf("store.Create(suspended) error = %v", err)
	}

	router := gin.New()
	router.Use(TenantMiddleware(resolver.NewComposite(resolver.NewHeaderContrib("", types.TenantIDStrategyString)), backing))
	handlerCalled := false
	router.GET("/ok", func(c *gin.Context) {
		handlerCalled = true
		tenant, ok := tenantctx.FromContext(c.Request.Context())
		if !ok {
			t.Fatal("tenant missing from request context")
		}
		c.JSON(http.StatusOK, gin.H{"tenant": tenant.ID.String()})
	})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/ok", nil)
	request.Header.Set(resolver.DefaultHeaderName, "tenant-a")
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("active tenant status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if !handlerCalled {
		t.Fatal("active tenant handler was not called")
	}

	handlerCalled = false
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/ok", nil)
	request.Header.Set(resolver.DefaultHeaderName, "tenant-b")
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden || recorder.Body.String() != `{"error":"tenant_inactive"}` {
		t.Fatalf("suspended tenant response = %d %q, want 403 tenant_inactive", recorder.Code, recorder.Body.String())
	}
	if handlerCalled {
		t.Fatal("tenant-scoped handler ran for suspended tenant")
	}
}

func TestTenantMiddlewareRejectsMissingAndUnknownTenant(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(TenantMiddleware(resolver.NewComposite(resolver.NewHeaderContrib("", types.TenantIDStrategyString)), store.NewMemoryStore()))
	handlerCalled := false
	router.GET("/ok", func(c *gin.Context) {
		handlerCalled = true
		c.Status(http.StatusOK)
	})

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/ok", nil))
	if recorder.Code != http.StatusUnauthorized || recorder.Body.String() != `{"error":"tenant_required"}` {
		t.Fatalf("missing tenant response = %d %q, want 401 tenant_required", recorder.Code, recorder.Body.String())
	}
	if handlerCalled {
		t.Fatal("tenant-scoped handler ran without tenant")
	}

	handlerCalled = false
	recorder = httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/ok", nil)
	request.Header.Set(resolver.DefaultHeaderName, "missing")
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden || recorder.Body.String() != `{"error":"tenant_forbidden"}` {
		t.Fatalf("unknown tenant response = %d %q, want 403 tenant_forbidden", recorder.Code, recorder.Body.String())
	}
	if handlerCalled {
		t.Fatal("tenant-scoped handler ran for unknown tenant")
	}
}

func TestTenantMiddlewareReturnsTimeoutWhenTenantLookupTimesOut(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handlerCalled := false
	router := gin.New()
	router.Use(TenantMiddleware(
		resolver.NewComposite(resolver.NewHeaderContrib("", types.TenantIDStrategyString)),
		failingTenantStore{err: context.DeadlineExceeded},
	))
	router.GET("/orders", func(c *gin.Context) {
		handlerCalled = true
		c.Status(http.StatusOK)
	})

	request := httptest.NewRequest(http.MethodGet, "/orders", nil)
	request.Header.Set(resolver.DefaultHeaderName, "tenant-a")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusRequestTimeout || recorder.Body.String() != `{"error":"tenant_forbidden"}` {
		t.Fatalf("tenant lookup timeout response = %d %q, want 408 tenant_forbidden", recorder.Code, recorder.Body.String())
	}
	if handlerCalled {
		t.Fatal("tenant-scoped handler ran after tenant lookup timeout")
	}
}

func TestTenantStatusGuardProtectsTenantScopedPages(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handlerCalls := make(map[string]int)
	router.GET("/missing", TenantStatusGuard(), func(c *gin.Context) {
		handlerCalls["/missing"]++
		c.String(http.StatusOK, "unexpected")
	})
	router.GET("/suspended", injectGinTenant(types.TenantStatusSuspended), TenantStatusGuard(), func(c *gin.Context) {
		handlerCalls["/suspended"]++
		c.String(http.StatusOK, "unexpected")
	})
	router.GET("/active", injectGinTenant(types.TenantStatusActive), TenantStatusGuard(), func(c *gin.Context) {
		handlerCalls["/active"]++
		c.String(http.StatusOK, "orders page")
	})

	tests := []struct {
		path      string
		wantCode  int
		wantBody  string
		wantCalls int
	}{
		{path: "/missing", wantCode: http.StatusUnauthorized, wantBody: `{"error":"tenant_required"}`, wantCalls: 0},
		{path: "/suspended", wantCode: http.StatusForbidden, wantBody: `{"error":"tenant_inactive"}`, wantCalls: 0},
		{path: "/active", wantCode: http.StatusOK, wantBody: "orders page", wantCalls: 1},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, tt.path, nil))
			if recorder.Code != tt.wantCode || recorder.Body.String() != tt.wantBody {
				t.Fatalf("GET %s = %d %q, want %d %q", tt.path, recorder.Code, recorder.Body.String(), tt.wantCode, tt.wantBody)
			}
			if handlerCalls[tt.path] != tt.wantCalls {
				t.Fatalf("GET %s handler calls = %d, want %d", tt.path, handlerCalls[tt.path], tt.wantCalls)
			}
		})
	}
}

func TestHostGuardMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/host", HostGuardMiddleware(), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})
	router.GET("/host-ok", func(c *gin.Context) {
		c.Request = c.Request.WithContext(tenantctx.WithHost(c.Request.Context()))
	}, HostGuardMiddleware(), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

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

func TestErrorHandlerHidesInternalErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(ErrorHandler())
	router.GET("/err", func(c *gin.Context) {
		_ = c.Error(errors.New("tenant tenant-a failed"))
	})

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/err", nil))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("error handler status = %d, want 500", recorder.Code)
	}
	if body := recorder.Body.String(); body != "{\"error\":\"internal_error\"}" {
		t.Fatalf("error handler body = %s, want generic error", body)
	}
}

type failingTenantStore struct {
	err error
}

func (failing failingTenantStore) Get(context.Context, types.TenantID) (types.Tenant, error) {
	return types.Tenant{}, failing.err
}

func (failing failingTenantStore) List(context.Context, store.ListFilter) ([]types.Tenant, error) {
	return nil, failing.err
}

func (failing failingTenantStore) Create(context.Context, types.Tenant) error {
	return failing.err
}

func (failing failingTenantStore) Update(context.Context, types.Tenant) error {
	return failing.err
}

func (failing failingTenantStore) Delete(context.Context, types.TenantID) error {
	return failing.err
}

func injectGinTenant(status types.TenantStatus) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request = c.Request.WithContext(tenantctx.WithTenant(c.Request.Context(), types.Tenant{
			ID:     "tenant-a",
			Status: status,
		}))
		c.Next()
	}
}

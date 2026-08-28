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

func TestTenantMiddlewareDeploymentResolution(t *testing.T) {
	gin.SetMode(gin.TestMode)
	backing := store.NewMemoryStore()
	if err := backing.Create(context.Background(), types.Tenant{ID: "tenant-a", Status: types.TenantStatusActive}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	tenants := resolver.NewComposite(resolver.NewHeaderContrib("", types.TenantIDStrategyString))

	router := gin.New()
	router.Use(TenantMiddleware(tenants, backing, WithDeploymentResolver(deploymentResolverFunc(func(context.Context, types.Tenant) (types.DeploymentUnit, error) {
		return types.DeploymentUnit{ID: "eu-central-1", Status: types.DeploymentUnitStatusActive}, nil
	}))))
	router.GET("/orders", func(c *gin.Context) {
		unit, ok := tenantctx.DeploymentFromContext(c.Request.Context())
		if !ok || unit.ID != "eu-central-1" {
			t.Fatalf("deployment context = %#v, %v; want eu-central-1", unit, ok)
		}
		c.Status(http.StatusNoContent)
	})

	request := httptest.NewRequest(http.MethodGet, "/orders", nil)
	request.Header.Set(resolver.DefaultHeaderName, "tenant-a")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("success status = %d, want %d", recorder.Code, http.StatusNoContent)
	}

	handlerCalled := false
	rejecting := gin.New()
	rejecting.Use(TenantMiddleware(tenants, backing, WithDeploymentResolver(deploymentResolverFunc(func(context.Context, types.Tenant) (types.DeploymentUnit, error) {
		return types.DeploymentUnit{}, errors.New("assignment missing")
	}))))
	rejecting.GET("/orders", func(c *gin.Context) {
		handlerCalled = true
		c.Status(http.StatusNoContent)
	})

	request = httptest.NewRequest(http.MethodGet, "/orders", nil)
	request.Header.Set(resolver.DefaultHeaderName, "tenant-a")
	recorder = httptest.NewRecorder()
	rejecting.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden || recorder.Body.String() != `{"error":"deployment_unavailable"}` {
		t.Fatalf("missing assignment response = %d %q, want 403 deployment_unavailable", recorder.Code, recorder.Body.String())
	}
	if handlerCalled {
		t.Fatal("handler ran after deployment resolution failed")
	}
}

func TestTenantMiddlewareRejectsInvalidDeploymentUnit(t *testing.T) {
	gin.SetMode(gin.TestMode)
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
			router := gin.New()
			router.Use(TenantMiddleware(tenants, backing, WithDeploymentResolver(deploymentResolverFunc(func(context.Context, types.Tenant) (types.DeploymentUnit, error) {
				return testCase.unit, nil
			}))))
			router.GET("/orders", func(c *gin.Context) {
				handlerCalled = true
				c.Status(http.StatusNoContent)
			})

			request := httptest.NewRequest(http.MethodGet, "/orders", nil)
			request.Header.Set(resolver.DefaultHeaderName, "tenant-a")
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusForbidden || recorder.Body.String() != `{"error":"deployment_unavailable"}` {
				t.Fatalf("response = %d %q, want 403 deployment_unavailable", recorder.Code, recorder.Body.String())
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

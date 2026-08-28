package echosaas

import (
	"context"
	"errors"
	"net/http"

	tenantctx "github.com/darkinno-tech/saas/core/context"
	"github.com/darkinno-tech/saas/core/resolver"
	"github.com/darkinno-tech/saas/core/store"
	"github.com/darkinno-tech/saas/core/types"
	"github.com/darkinno-tech/saas/deployment"

	"github.com/labstack/echo/v4"
)

// Config controls optional tenant middleware integrations.
type Config struct {
	DeploymentResolver deployment.Resolver
}

// Option configures TenantMiddleware.
type Option func(*Config)

// WithDeploymentResolver resolves a tenant's current deployment unit after
// tenant lookup succeeds. Passing nil leaves deployment resolution disabled.
func WithDeploymentResolver(resolver deployment.Resolver) Option {
	return func(config *Config) {
		config.DeploymentResolver = resolver
	}
}

// TenantMiddleware resolves the current tenant and stores it in request context.
func TenantMiddleware(resolver resolver.Resolver, store store.Store, opts ...Option) echo.MiddlewareFunc {
	config := newConfig(opts...)
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			tenantID, err := resolver.Resolve(c.Request())
			if err != nil {
				return c.JSON(http.StatusUnauthorized, errorBody("tenant_required"))
			}

			tenant, err := store.Get(c.Request().Context(), tenantID)
			if err != nil {
				status := http.StatusForbidden
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					status = http.StatusRequestTimeout
				}
				return c.JSON(status, errorBody("tenant_forbidden"))
			}
			if tenant.Status != types.TenantStatusActive {
				return c.JSON(http.StatusForbidden, errorBody("tenant_inactive"))
			}

			request := c.Request()
			ctx := tenantctx.WithTenant(request.Context(), tenant)
			if config.DeploymentResolver != nil {
				unit, err := config.DeploymentResolver.Resolve(request.Context(), tenant)
				if err != nil || unit.ID == "" || unit.Status != types.DeploymentUnitStatusActive {
					status := http.StatusForbidden
					if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
						status = http.StatusRequestTimeout
					}
					return c.JSON(status, errorBody("deployment_unavailable"))
				}
				ctx = tenantctx.WithTenantDeployment(request.Context(), tenant, unit)
			}

			c.SetRequest(request.WithContext(ctx))
			return next(c)
		}
	}
}

func newConfig(opts ...Option) Config {
	config := Config{}
	for _, opt := range opts {
		if opt != nil {
			opt(&config)
		}
	}
	return config
}

// TenantStatusGuard allows only active tenants.
func TenantStatusGuard() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			tenant, ok := tenantctx.FromContext(c.Request().Context())
			if !ok {
				return c.JSON(http.StatusUnauthorized, errorBody("tenant_required"))
			}
			if tenant.Status != types.TenantStatusActive {
				return c.JSON(http.StatusForbidden, errorBody("tenant_inactive"))
			}
			return next(c)
		}
	}
}

// HostGuardMiddleware allows only explicit host context.
func HostGuardMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if !tenantctx.IsHost(c.Request().Context()) {
				return c.JSON(http.StatusForbidden, errorBody("host_required"))
			}
			return next(c)
		}
	}
}

func errorBody(code string) map[string]string {
	return map[string]string{"error": code}
}

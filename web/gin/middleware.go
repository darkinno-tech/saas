package ginsaas

import (
	"context"
	"errors"
	"net/http"

	tenantctx "github.com/darkinno-tech/saas/core/context"
	"github.com/darkinno-tech/saas/core/resolver"
	"github.com/darkinno-tech/saas/core/store"
	"github.com/darkinno-tech/saas/core/types"
	"github.com/darkinno-tech/saas/deployment"

	"github.com/gin-gonic/gin"
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
func TenantMiddleware(resolver resolver.Resolver, store store.Store, opts ...Option) gin.HandlerFunc {
	config := newConfig(opts...)
	return func(c *gin.Context) {
		tenantID, err := resolver.Resolve(c.Request)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, errorBody("tenant_required"))
			return
		}

		tenant, err := store.Get(c.Request.Context(), tenantID)
		if err != nil {
			status := http.StatusForbidden
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				status = http.StatusRequestTimeout
			}
			c.AbortWithStatusJSON(status, errorBody("tenant_forbidden"))
			return
		}
		if tenant.Status != types.TenantStatusActive {
			c.AbortWithStatusJSON(http.StatusForbidden, errorBody("tenant_inactive"))
			return
		}

		ctx := tenantctx.WithTenant(c.Request.Context(), tenant)
		if config.DeploymentResolver != nil {
			unit, err := config.DeploymentResolver.Resolve(c.Request.Context(), tenant)
			if err != nil || unit.ID == "" || unit.Status != types.DeploymentUnitStatusActive {
				status := http.StatusForbidden
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					status = http.StatusRequestTimeout
				}
				c.AbortWithStatusJSON(status, errorBody("deployment_unavailable"))
				return
			}
			ctx = tenantctx.WithTenantDeployment(c.Request.Context(), tenant, unit)
		}

		c.Request = c.Request.WithContext(ctx)
		c.Next()
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
func TenantStatusGuard() gin.HandlerFunc {
	return func(c *gin.Context) {
		tenant, ok := tenantctx.FromContext(c.Request.Context())
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, errorBody("tenant_required"))
			return
		}
		if tenant.Status != types.TenantStatusActive {
			c.AbortWithStatusJSON(http.StatusForbidden, errorBody("tenant_inactive"))
			return
		}
		c.Next()
	}
}

// HostGuardMiddleware allows only explicit host context.
func HostGuardMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !tenantctx.IsHost(c.Request.Context()) {
			c.AbortWithStatusJSON(http.StatusForbidden, errorBody("host_required"))
			return
		}
		c.Next()
	}
}

func errorBody(code string) gin.H {
	return gin.H{"error": code}
}

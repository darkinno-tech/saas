package fibersaas

import (
	"context"
	"errors"
	"net/http"

	tenantctx "github.com/im10furry/saas/core/context"
	"github.com/im10furry/saas/core/resolver"
	"github.com/im10furry/saas/core/store"
	"github.com/im10furry/saas/core/types"
	"github.com/im10furry/saas/deployment"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/adaptor"
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

// TenantMiddleware resolves the current tenant and stores it in Fiber user context.
func TenantMiddleware(resolver resolver.Resolver, store store.Store, opts ...Option) fiber.Handler {
	config := newConfig(opts...)
	return func(c *fiber.Ctx) error {
		request, err := adaptor.ConvertRequest(c, false)
		if err != nil {
			return c.Status(http.StatusUnauthorized).JSON(errorBody("tenant_required"))
		}

		ctx := c.UserContext()
		request = request.WithContext(ctx)
		tenantID, err := resolver.Resolve(request)
		if err != nil {
			return c.Status(http.StatusUnauthorized).JSON(errorBody("tenant_required"))
		}

		tenant, err := store.Get(ctx, tenantID)
		if err != nil {
			status := http.StatusForbidden
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				status = http.StatusRequestTimeout
			}
			return c.Status(status).JSON(errorBody("tenant_forbidden"))
		}
		if tenant.Status != types.TenantStatusActive {
			return c.Status(http.StatusForbidden).JSON(errorBody("tenant_inactive"))
		}

		requestCtx := tenantctx.WithTenant(ctx, tenant)
		if config.DeploymentResolver != nil {
			unit, err := config.DeploymentResolver.Resolve(ctx, tenant)
			if err != nil || unit.ID == "" || unit.Status != types.DeploymentUnitStatusActive {
				status := http.StatusForbidden
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					status = http.StatusRequestTimeout
				}
				return c.Status(status).JSON(errorBody("deployment_unavailable"))
			}
			requestCtx = tenantctx.WithTenantDeployment(ctx, tenant, unit)
		}

		c.SetUserContext(requestCtx)
		return c.Next()
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
func TenantStatusGuard() fiber.Handler {
	return func(c *fiber.Ctx) error {
		tenant, ok := tenantctx.FromContext(c.UserContext())
		if !ok {
			return c.Status(http.StatusUnauthorized).JSON(errorBody("tenant_required"))
		}
		if tenant.Status != types.TenantStatusActive {
			return c.Status(http.StatusForbidden).JSON(errorBody("tenant_inactive"))
		}
		return c.Next()
	}
}

// HostGuardMiddleware allows only explicit host context.
func HostGuardMiddleware() fiber.Handler {
	return func(c *fiber.Ctx) error {
		if !tenantctx.IsHost(c.UserContext()) {
			return c.Status(http.StatusForbidden).JSON(errorBody("host_required"))
		}
		return c.Next()
	}
}

func errorBody(code string) fiber.Map {
	return fiber.Map{"error": code}
}

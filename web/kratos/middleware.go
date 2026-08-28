package kratossaas

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	tenantctx "github.com/darkinno-tech/saas/core/context"
	"github.com/darkinno-tech/saas/core/resolver"
	"github.com/darkinno-tech/saas/core/store"
	"github.com/darkinno-tech/saas/core/types"
	"github.com/darkinno-tech/saas/deployment"

	kerrors "github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/middleware"
	"github.com/go-kratos/kratos/v2/transport"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
)

const (
	reasonTenantRequired        = "TENANT_REQUIRED"
	reasonTenantForbidden       = "TENANT_FORBIDDEN"
	reasonTenantInactive        = "TENANT_INACTIVE"
	reasonDeploymentUnavailable = "DEPLOYMENT_UNAVAILABLE"
	reasonHostRequired          = "HOST_REQUIRED"
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

// TenantMiddleware resolves the current tenant and stores it in context.
func TenantMiddleware(resolver resolver.Resolver, store store.Store, opts ...Option) middleware.Middleware {
	config := newConfig(opts...)
	return func(next middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req any) (any, error) {
			request, err := requestFromContext(ctx)
			if err != nil {
				return nil, kerrors.Unauthorized(reasonTenantRequired, "tenant required")
			}

			tenantID, err := resolver.Resolve(request)
			if err != nil {
				return nil, kerrors.Unauthorized(reasonTenantRequired, "tenant required")
			}

			tenant, err := store.Get(ctx, tenantID)
			if err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return nil, kerrors.New(http.StatusRequestTimeout, reasonTenantForbidden, "tenant lookup timed out")
				}
				return nil, kerrors.Forbidden(reasonTenantForbidden, "tenant forbidden")
			}
			if tenant.Status != types.TenantStatusActive {
				return nil, kerrors.Forbidden(reasonTenantInactive, "tenant inactive")
			}

			tenantCtx := tenantctx.WithTenant(ctx, tenant)
			if config.DeploymentResolver != nil {
				unit, err := config.DeploymentResolver.Resolve(ctx, tenant)
				if err != nil || unit.ID == "" || unit.Status != types.DeploymentUnitStatusActive {
					if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
						return nil, kerrors.New(http.StatusRequestTimeout, reasonDeploymentUnavailable, "deployment resolution timed out")
					}
					return nil, kerrors.Forbidden(reasonDeploymentUnavailable, "deployment unavailable")
				}
				tenantCtx = tenantctx.WithTenantDeployment(ctx, tenant, unit)
			}

			return next(tenantCtx, req)
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
func TenantStatusGuard() middleware.Middleware {
	return func(next middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req any) (any, error) {
			tenant, ok := tenantctx.FromContext(ctx)
			if !ok {
				return nil, kerrors.Unauthorized(reasonTenantRequired, "tenant required")
			}
			if tenant.Status != types.TenantStatusActive {
				return nil, kerrors.Forbidden(reasonTenantInactive, "tenant inactive")
			}
			return next(ctx, req)
		}
	}
}

// HostGuard allows only explicit host context.
func HostGuard() middleware.Middleware {
	return func(next middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req any) (any, error) {
			if !tenantctx.IsHost(ctx) {
				return nil, kerrors.Forbidden(reasonHostRequired, "host required")
			}
			return next(ctx, req)
		}
	}
}

func requestFromContext(ctx context.Context) (*http.Request, error) {
	if request, ok := khttp.RequestFromServerContext(ctx); ok {
		return request, nil
	}

	tr, ok := transport.FromServerContext(ctx)
	if !ok || tr.RequestHeader() == nil {
		return nil, resolver.ErrNilRequest
	}

	header := make(http.Header)
	for _, key := range tr.RequestHeader().Keys() {
		for _, value := range tr.RequestHeader().Values(key) {
			header.Add(key, value)
		}
	}

	return &http.Request{
		Header: header,
		Host:   header.Get("Host"),
		URL:    &url.URL{},
	}, nil
}

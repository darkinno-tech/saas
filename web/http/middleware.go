package httpsaas

import (
	"context"
	"errors"
	"net/http"

	tenantctx "github.com/darkinno-tech/saas/core/context"
	"github.com/darkinno-tech/saas/core/resolver"
	"github.com/darkinno-tech/saas/core/store"
	"github.com/darkinno-tech/saas/core/types"
	"github.com/darkinno-tech/saas/deployment"
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
func TenantMiddleware(resolver resolver.Resolver, store store.Store, opts ...Option) func(http.Handler) http.Handler {
	config := newConfig(opts...)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tenantID, err := resolver.Resolve(r)
			if err != nil {
				writeError(w, http.StatusUnauthorized, "tenant_required")
				return
			}

			tenant, err := store.Get(r.Context(), tenantID)
			if err != nil {
				status := http.StatusForbidden
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					status = http.StatusRequestTimeout
				}
				writeError(w, status, "tenant_forbidden")
				return
			}
			if tenant.Status != types.TenantStatusActive {
				writeError(w, http.StatusForbidden, "tenant_inactive")
				return
			}

			ctx := tenantctx.WithTenant(r.Context(), tenant)
			if config.DeploymentResolver != nil {
				unit, err := config.DeploymentResolver.Resolve(r.Context(), tenant)
				if err != nil || unit.ID == "" || unit.Status != types.DeploymentUnitStatusActive {
					status := http.StatusForbidden
					if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
						status = http.StatusRequestTimeout
					}
					writeError(w, status, "deployment_unavailable")
					return
				}
				ctx = tenantctx.WithTenantDeployment(r.Context(), tenant, unit)
			}

			next.ServeHTTP(w, r.WithContext(ctx))
		})
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
func TenantStatusGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tenant, ok := tenantctx.FromContext(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "tenant_required")
			return
		}
		if tenant.Status != types.TenantStatusActive {
			writeError(w, http.StatusForbidden, "tenant_inactive")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// HostGuard allows only host context.
func HostGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !tenantctx.IsHost(r.Context()) {
			writeError(w, http.StatusForbidden, "host_required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"error":"` + code + `"}`))
}

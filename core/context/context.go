package tenantctx

import (
	"context"

	"github.com/im10furry/saas/core/types"
)

type state struct {
	side          side
	tenant        types.Tenant
	deployment    types.DeploymentUnit
	hasDeployment bool
}

// WithTenant returns a child context scoped to tenant.
func WithTenant(ctx context.Context, tenant types.Tenant) context.Context {
	return context.WithValue(ctx, stateKey{}, state{
		side:   sideTenant,
		tenant: cloneTenant(tenant),
	})
}

// WithTenantDeployment returns a child context scoped to tenant and its current
// host-managed deployment unit.
func WithTenantDeployment(ctx context.Context, tenant types.Tenant, deployment types.DeploymentUnit) context.Context {
	return context.WithValue(ctx, stateKey{}, state{
		side:          sideTenant,
		tenant:        cloneTenant(tenant),
		deployment:    cloneDeploymentUnit(deployment),
		hasDeployment: true,
	})
}

// FromContext returns the tenant stored in ctx.
func FromContext(ctx context.Context) (types.Tenant, bool) {
	current, ok := ctx.Value(stateKey{}).(state)
	if !ok || current.side != sideTenant {
		return types.Tenant{}, false
	}
	return cloneTenant(current.tenant), true
}

// DeploymentFromContext returns the deployment unit stored in a tenant context.
func DeploymentFromContext(ctx context.Context) (types.DeploymentUnit, bool) {
	current, ok := ctx.Value(stateKey{}).(state)
	if !ok || current.side != sideTenant || !current.hasDeployment {
		return types.DeploymentUnit{}, false
	}
	return cloneDeploymentUnit(current.deployment), true
}

// WithHost returns a child context explicitly marked as host-side.
func WithHost(ctx context.Context) context.Context {
	return context.WithValue(ctx, stateKey{}, state{side: sideHost})
}

// IsHost reports whether ctx is explicitly marked as host-side.
func IsHost(ctx context.Context) bool {
	current, ok := ctx.Value(stateKey{}).(state)
	return ok && current.side == sideHost
}

func cloneTenant(tenant types.Tenant) types.Tenant {
	if tenant.Config == nil {
		return tenant
	}

	cloned := make(map[string]string, len(tenant.Config))
	for key, value := range tenant.Config {
		cloned[key] = value
	}
	tenant.Config = cloned
	return tenant
}

func cloneDeploymentUnit(deployment types.DeploymentUnit) types.DeploymentUnit {
	if deployment.ResidencyTags != nil {
		deployment.ResidencyTags = append([]string(nil), deployment.ResidencyTags...)
	}

	if deployment.Metadata != nil {
		cloned := make(map[string]string, len(deployment.Metadata))
		for key, value := range deployment.Metadata {
			cloned[key] = value
		}
		deployment.Metadata = cloned
	}

	return deployment
}

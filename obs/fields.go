package obs

import (
	"context"

	tenantctx "github.com/im10furry/saas/core/context"
)

const (
	// TenantIDField is the standard observability field for tenant ID.
	TenantIDField = "tenant_id"

	// TenantSideField is the standard observability field for tenant side.
	TenantSideField = "tenant_side"

	// DeploymentUnitIDField is the standard observability field for the resolved
	// logical deployment unit. It deliberately excludes geographic labels and
	// host-owned endpoint metadata.
	DeploymentUnitIDField = "deployment_unit_id"

	hostSide   = "host"
	tenantSide = "tenant"
)

// Fields returns tenant observability fields for ctx.
func Fields(ctx context.Context) map[string]string {
	tenantID, side, deploymentUnitID := fieldValues(ctx)
	fields := make(map[string]string, 3)
	if tenantID != "" {
		fields[TenantIDField] = tenantID
	}
	if side != "" {
		fields[TenantSideField] = side
	}
	if deploymentUnitID != "" {
		fields[DeploymentUnitIDField] = deploymentUnitID
	}
	return fields
}

func fieldValues(ctx context.Context) (tenantID string, side string, deploymentUnitID string) {
	if tenant, ok := tenantctx.FromContext(ctx); ok {
		if deployment, ok := tenantctx.DeploymentFromContext(ctx); ok {
			deploymentUnitID = deployment.ID.String()
		}
		return tenant.ID.String(), tenantSide, deploymentUnitID
	}
	if tenantctx.IsHost(ctx) {
		return "", hostSide, ""
	}
	return "", "", ""
}

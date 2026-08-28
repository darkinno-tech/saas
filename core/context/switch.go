package tenantctx

import (
	"context"

	"github.com/im10furry/saas/core/types"
)

// Switch returns a child context scoped to tenant, leaving ctx unchanged.
func Switch(ctx context.Context, tenant types.Tenant) context.Context {
	return WithTenant(ctx, tenant)
}

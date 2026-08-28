package tenant

import (
	"context"

	"github.com/im10furry/saas/core/types"
)

// Suspend transitions an Active tenant to Suspended.
func (manager *Manager) Suspend(ctx context.Context, id types.TenantID) (types.Tenant, error) {
	return manager.transition(ctx, id, "suspend", suspendTransitions)
}

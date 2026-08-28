package tenant

import (
	"context"

	"github.com/darkinno-tech/saas/core/types"
)

// SoftDelete transitions an Active or Suspended tenant to SoftDeleted.
func (manager *Manager) SoftDelete(ctx context.Context, id types.TenantID) (types.Tenant, error) {
	return manager.transition(ctx, id, "soft_delete", softDeleteTransitions)
}

package tenant

import (
	"context"

	"github.com/darkinno-tech/saas/core/types"
)

// Activate transitions a Pending tenant to Active.
func (manager *Manager) Activate(ctx context.Context, id types.TenantID) (types.Tenant, error) {
	return manager.transition(ctx, id, "activate", activateTransitions)
}

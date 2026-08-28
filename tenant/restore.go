package tenant

import (
	"context"

	"github.com/darkinno-tech/saas/core/types"
)

// Restore transitions a Suspended tenant to Active.
func (manager *Manager) Restore(ctx context.Context, id types.TenantID) (types.Tenant, error) {
	return manager.transition(ctx, id, "restore", restoreTransitions)
}

package tenant

import (
	"context"

	tenantctx "github.com/darkinno-tech/saas/core/context"
	"github.com/darkinno-tech/saas/core/types"
)

// HardDelete physically removes a tenant and requires host context.
func (manager *Manager) HardDelete(ctx context.Context, id types.TenantID) error {
	if !tenantctx.IsHost(ctx) {
		return ErrHostRequired
	}

	current, err := manager.store.Get(ctx, id)
	if err != nil {
		return err
	}
	if current.Status != types.TenantStatusActive && current.Status != types.TenantStatusSuspended {
		return ErrInvalidState
	}
	if err := manager.emit(ctx, Event{TenantID: id, Action: "hard_delete", From: current.Status, To: types.TenantStatusHardDeleted}); err != nil {
		return err
	}
	return manager.store.Delete(ctx, id)
}

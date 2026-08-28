package tenant

import "github.com/im10furry/saas/core/types"

var (
	activateTransitions = map[types.TenantStatus]types.TenantStatus{
		types.TenantStatusPending: types.TenantStatusActive,
	}
	suspendTransitions = map[types.TenantStatus]types.TenantStatus{
		types.TenantStatusActive: types.TenantStatusSuspended,
	}
	restoreTransitions = map[types.TenantStatus]types.TenantStatus{
		types.TenantStatusSuspended: types.TenantStatusActive,
	}
	softDeleteTransitions = map[types.TenantStatus]types.TenantStatus{
		types.TenantStatusActive:    types.TenantStatusSoftDeleted,
		types.TenantStatusSuspended: types.TenantStatusSoftDeleted,
	}
)

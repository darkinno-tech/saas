package rbac

import (
	"context"

	"github.com/im10furry/saas/core/types"
)

type Service interface {
	CreateRole(ctx context.Context, role Role) error
	GetRole(ctx context.Context, tenantID types.TenantID, key string) (Role, error)
	DeleteRole(ctx context.Context, tenantID types.TenantID, key string) error
	Authorize(ctx context.Context, tenantID types.TenantID, roles []string, permission Permission) error
}

// Enforcer checks whether roles grant a permission for a tenant.
type Enforcer interface {
	Enforce(ctx context.Context, tenantID types.TenantID, roles []string, permission Permission) error
}

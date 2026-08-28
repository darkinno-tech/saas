package tenant

import (
	"context"

	"github.com/darkinno-tech/saas/core/types"
)

// Service manages tenant lifecycle operations.
type Service interface {
	Create(ctx context.Context, input CreateInput) (types.Tenant, error)
	Get(ctx context.Context, id types.TenantID) (types.Tenant, error)
	Update(ctx context.Context, input UpdateInput) (types.Tenant, error)
	Delete(ctx context.Context, id types.TenantID) error
	Activate(ctx context.Context, id types.TenantID) (types.Tenant, error)
	Suspend(ctx context.Context, id types.TenantID) (types.Tenant, error)
	Restore(ctx context.Context, id types.TenantID) (types.Tenant, error)
	SoftDelete(ctx context.Context, id types.TenantID) (types.Tenant, error)
	HardDelete(ctx context.Context, id types.TenantID) error
}

// CreateInput describes a tenant to create.
type CreateInput struct {
	ID     types.TenantID
	Name   string
	PlanID string
	Config map[string]string
}

// UpdateInput describes tenant metadata changes.
type UpdateInput struct {
	ID     types.TenantID
	Name   string
	PlanID string
	Config map[string]string
}

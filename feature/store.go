package feature

import (
	"context"

	"github.com/darkinno-tech/saas/core/types"
)

// Store persists plan defaults and tenant overrides.
type Store interface {
	SetPlanDefaults(ctx context.Context, planID string, flags []Flag) error
	SetTenantOverrides(ctx context.Context, tenantID types.TenantID, flags []Flag) error
	Resolve(ctx context.Context, tenantID types.TenantID, planID string, key string) (Flag, error)
	List(ctx context.Context, tenantID types.TenantID, planID string) ([]Flag, error)
}

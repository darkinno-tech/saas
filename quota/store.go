package quota

import (
	"context"

	"github.com/im10furry/saas/core/types"
)

// Store tracks quota usage.
type Store interface {
	Add(ctx context.Context, tenantID types.TenantID, resource string, period Period, amount int64) (int64, error)
	Consume(ctx context.Context, limit Limit, amount int64) (int64, error)
	Get(ctx context.Context, tenantID types.TenantID, resource string, period Period) (int64, error)
	Reset(ctx context.Context, tenantID types.TenantID, resource string, period Period) error
}

package store

import (
	"context"
	"time"

	"github.com/im10furry/saas/core/types"
)

// Cache stores tenant metadata for faster lookups.
type Cache interface {
	Get(ctx context.Context, id types.TenantID) (types.Tenant, bool, error)
	Set(ctx context.Context, tenant types.Tenant, ttl time.Duration) error
	Delete(ctx context.Context, id types.TenantID) error
	Invalidate(ctx context.Context) error
}

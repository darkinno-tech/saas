package identity

import (
	"context"

	"github.com/darkinno-tech/saas/core/types"
)

type Store interface {
	Link(ctx context.Context, link Link) error
	GetByExternal(ctx context.Context, tenantID types.TenantID, provider ProviderKey, subject string) (Link, error)
	GetByUser(ctx context.Context, tenantID types.TenantID, userID string) ([]Link, error)
	Unlink(ctx context.Context, tenantID types.TenantID, provider ProviderKey, subject string) error
}

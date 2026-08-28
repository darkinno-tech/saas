package store

import (
	"context"

	"github.com/darkinno-tech/saas/core/types"
)

// Store persists tenant metadata.
type Store interface {
	Get(ctx context.Context, id types.TenantID) (types.Tenant, error)
	List(ctx context.Context, filter ListFilter) ([]types.Tenant, error)
	Create(ctx context.Context, tenant types.Tenant) error
	Update(ctx context.Context, tenant types.Tenant) error
	Delete(ctx context.Context, id types.TenantID) error
}

// CompareAndSwapStore extends Store with an atomic conditional replacement.
// CompareAndSwap replaces expected with updated only when the persisted tenant
// still equals expected. Implementations return ErrTenantConflict when another
// writer changed the tenant first.
type CompareAndSwapStore interface {
	Store
	CompareAndSwap(ctx context.Context, expected types.Tenant, updated types.Tenant) error
}

// PagedStore extends Store with cursor-based tenant listing.
type PagedStore interface {
	Store
	ListPage(ctx context.Context, filter PageFilter) ([]types.Tenant, error)
}

// ListFilter restricts tenant list queries.
type ListFilter struct {
	Statuses []types.TenantStatus
	Limit    int
	Offset   int
}

// PageFilter restricts cursor-based tenant list queries.
type PageFilter struct {
	Statuses []types.TenantStatus
	Limit    int
	Offset   int
	// Cursor returns rows ordered after the tenant ID cursor.
	Cursor types.TenantID
}

func (filter ListFilter) matches(tenant types.Tenant) bool {
	if len(filter.Statuses) == 0 {
		return true
	}

	for _, status := range filter.Statuses {
		if tenant.Status == status {
			return true
		}
	}
	return false
}

func (filter ListFilter) validate() error {
	if filter.Limit < 0 || filter.Offset < 0 {
		return ErrInvalidListFilter
	}
	if filter.Offset > 0 && filter.Limit == 0 {
		return ErrInvalidListFilter
	}
	return nil
}

func (filter PageFilter) validate() error {
	if err := filter.listFilter().validate(); err != nil {
		return err
	}
	if filter.Cursor != "" && filter.Offset > 0 {
		return ErrInvalidListFilter
	}
	return nil
}

func (filter PageFilter) listFilter() ListFilter {
	return ListFilter{
		Statuses: filter.Statuses,
		Limit:    filter.Limit,
		Offset:   filter.Offset,
	}
}

func pageTenants(tenants []types.Tenant, filter ListFilter) []types.Tenant {
	if filter.Offset >= len(tenants) {
		return []types.Tenant{}
	}

	start := filter.Offset
	end := len(tenants)
	if filter.Limit > 0 && filter.Limit < end-start {
		end = start + filter.Limit
	}
	return tenants[start:end]
}

func seekTenants(tenants []types.Tenant, cursor types.TenantID) []types.Tenant {
	if cursor == "" {
		return tenants
	}
	start := len(tenants)
	for i, tenant := range tenants {
		if tenant.ID > cursor {
			start = i
			break
		}
	}
	return tenants[start:]
}

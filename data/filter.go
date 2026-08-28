package data

import (
	"context"

	tenantctx "github.com/im10furry/saas/core/context"
	"github.com/im10furry/saas/core/types"
)

// DataFilter provides an ORM-independent tenant filter.
type DataFilter interface {
	Condition() Condition
	TenantID() (types.TenantID, bool)
	IsHost() bool
}

// Filter is the default DataFilter implementation.
type Filter struct {
	tenantID types.TenantID
	host     bool
	opts     filterOptions
}

var _ DataFilter = (*Filter)(nil)

// NewFilter creates a tenant filter from context.
func NewFilter(ctx context.Context, opts ...FilterOption) (*Filter, error) {
	options := defaultFilterOptions()
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		if err := opt(&options); err != nil {
			return nil, err
		}
	}

	if ctx == nil {
		return nil, ErrNoTenant
	}
	if tenantctx.IsHost(ctx) {
		return &Filter{host: true, opts: options}, nil
	}

	tenant, ok := tenantctx.FromContext(ctx)
	if !ok || tenant.ID == "" {
		return nil, ErrNoTenant
	}

	return &Filter{tenantID: tenant.ID, opts: options}, nil
}

// Condition returns the parameterized tenant condition.
func (filter *Filter) Condition() Condition {
	if filter == nil || filter.host {
		return Condition{}
	}

	condition := Condition{
		Expression: filter.opts.tenantField + " = ?",
		Args:       []any{filter.tenantID.String()},
	}
	if filter.opts.softDeleteField != "" && !filter.opts.includeSoftDeleted {
		condition.Expression += " AND " + filter.opts.softDeleteField + " IS NULL"
	}
	return condition
}

// TenantID returns the scoped tenant ID when the filter is tenant-scoped.
func (filter *Filter) TenantID() (types.TenantID, bool) {
	if filter == nil || filter.host || filter.tenantID == "" {
		return "", false
	}
	return filter.tenantID, true
}

// IsHost reports whether this filter represents explicit host-side access.
func (filter *Filter) IsHost() bool {
	return filter != nil && filter.host
}

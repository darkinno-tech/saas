package enttenant

import (
	"context"

	"entgo.io/ent/dialect/sql"

	"github.com/im10furry/saas/core/types"
	"github.com/im10furry/saas/data"
)

// SelectorPredicate is an Ent custom predicate that mutates a SQL selector.
type SelectorPredicate = func(*sql.Selector)

// Predicate builds an Ent custom predicate from the tenant context.
func Predicate(ctx context.Context, config Config) (SelectorPredicate, error) {
	config = config.normalize()
	filter, err := data.NewFilter(ctx, config.filterOptions()...)
	if err != nil {
		return nil, err
	}
	if filter.IsHost() {
		return func(*sql.Selector) {}, nil
	}

	tenantID, ok := filter.TenantID()
	if !ok {
		return nil, data.ErrNoTenant
	}

	return selectorPredicate(config, tenantID), nil
}

func selectorPredicate(config Config, tenantID types.TenantID) SelectorPredicate {
	return func(selector *sql.Selector) {
		if selector == nil {
			return
		}
		selector.Where(sql.EQ(selector.C(config.TenantField), tenantID.String()))
		if config.SoftDeleteField != "" && !config.IncludeSoftDeleted {
			selector.Where(sql.IsNull(selector.C(config.SoftDeleteField)))
		}
	}
}

// Apply adds the current tenant predicate directly to selector.
func Apply(ctx context.Context, selector *sql.Selector, config Config) error {
	predicate, err := Predicate(ctx, config)
	if err != nil {
		return err
	}
	predicate(selector)
	return nil
}

// TenantPredicate builds an Ent custom predicate with default configuration.
func TenantPredicate(ctx context.Context) (SelectorPredicate, error) {
	return Predicate(ctx, Config{})
}

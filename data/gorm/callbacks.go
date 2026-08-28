package gormtenant

import (
	"context"

	"github.com/im10furry/saas/data"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (plugin *Plugin) addTenantCondition(tx *gorm.DB) {
	if tx.Statement.SQL.Len() > 0 {
		plugin.requireHostForRaw(tx)
		return
	}

	filter, err := plugin.newFilter(tx.Statement.Context)
	if err != nil {
		addDBError(tx, err)
		return
	}
	if filter.IsHost() {
		return
	}
	if tx.Statement.Unscoped {
		addDBError(tx, ErrUnscopedRequiresHost)
		return
	}

	condition := filter.Condition()
	if condition.Empty() {
		return
	}
	tx.Statement.AddClause(clause.Where{Exprs: []clause.Expression{
		clause.Expr{SQL: condition.Expression, Vars: condition.Args},
	}})
}

func (plugin *Plugin) requireHostForRaw(tx *gorm.DB) {
	if isSafeSQL(tx) {
		return
	}

	filter, err := plugin.newFilter(tx.Statement.Context)
	if err != nil {
		addDBError(tx, err)
		return
	}
	if !filter.IsHost() {
		addDBError(tx, ErrRawRequiresHost)
	}
}

func (plugin *Plugin) newFilter(ctx context.Context) (*data.Filter, error) {
	opts := []data.FilterOption{data.WithTenantField(plugin.config.TenantField)}
	if plugin.config.SoftDeleteField != "" {
		opts = append(opts, data.WithSoftDeleteField(plugin.config.SoftDeleteField))
	}
	if plugin.config.IncludeSoftDeleted {
		opts = append(opts, data.WithIncludeSoftDeleted(true))
	}
	return data.NewFilter(ctx, opts...)
}

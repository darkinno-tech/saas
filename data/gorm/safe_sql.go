package gormtenant

import (
	"context"

	tenantctx "github.com/im10furry/saas/core/context"

	"gorm.io/gorm"
)

const safeSQLKey = "saas:safe_sql"

// SafeRaw executes raw SQL only with explicit host context.
func SafeRaw(ctx context.Context, db *gorm.DB, sqlText string, values ...interface{}) *gorm.DB {
	tx := db.WithContext(ctx)
	if !tenantctx.IsHost(ctx) {
		addDBError(tx, ErrRawRequiresHost)
		return tx
	}
	if err := ctx.Err(); err != nil {
		addDBError(tx, err)
		return tx
	}
	return tx.Set(safeSQLKey, true).Raw(sqlText, values...)
}

// SafeExec executes raw SQL only with explicit host context.
func SafeExec(ctx context.Context, db *gorm.DB, sqlText string, values ...interface{}) *gorm.DB {
	tx := db.WithContext(ctx)
	if !tenantctx.IsHost(ctx) {
		addDBError(tx, ErrRawRequiresHost)
		return tx
	}
	if err := ctx.Err(); err != nil {
		addDBError(tx, err)
		return tx
	}
	return tx.Set(safeSQLKey, true).Exec(sqlText, values...)
}

func isSafeSQL(tx *gorm.DB) bool {
	value, ok := tx.Get(safeSQLKey)
	allowed, _ := value.(bool)
	return ok && allowed
}

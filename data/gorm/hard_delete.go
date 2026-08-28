package gormtenant

import (
	"context"

	"github.com/darkinno-tech/saas"
	tenantctx "github.com/darkinno-tech/saas/core/context"

	"gorm.io/gorm"
)

// HardDelete physically deletes rows and requires explicit host context.
func HardDelete(ctx context.Context, db *gorm.DB, value interface{}, conds ...interface{}) *gorm.DB {
	tx := db.WithContext(ctx)
	if !tenantctx.IsHost(ctx) {
		addDBError(tx, saas.ErrHostRequired)
		return tx
	}
	return tx.Unscoped().Delete(value, conds...)
}

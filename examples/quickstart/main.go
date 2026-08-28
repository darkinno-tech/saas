package main

import (
	"context"

	tenantctx "github.com/darkinno-tech/saas/core/context"
	"github.com/darkinno-tech/saas/core/types"
	gormtenant "github.com/darkinno-tech/saas/data/gorm"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

type Order struct {
	ID       uint
	TenantID string `gorm:"column:tenant_id"`
	Name     string
}

func main() {
	db, _ := gorm.Open(mysql.New(mysql.Config{DSN: "user:pass@tcp(localhost:3306)/app", SkipInitializeWithVersion: true}), &gorm.Config{DryRun: true, DisableAutomaticPing: true, SkipDefaultTransaction: true})
	_ = db.Use(gormtenant.New(gormtenant.Config{}))
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a", Status: types.TenantStatusActive})
	_ = gormtenant.BulkCreate(ctx, db, &[]Order{{Name: "first"}}).Error
}

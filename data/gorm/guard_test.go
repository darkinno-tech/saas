package gormtenant_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	tenantctx "github.com/im10furry/saas/core/context"
	"github.com/im10furry/saas/core/types"
	gormtenant "github.com/im10furry/saas/data/gorm"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

type guardedOrder struct {
	ID       uint
	TenantID string `gorm:"column:tenant_id"`
	Name     string
}

func TestGORMGuardsScopeQueryUpdateDeleteAndCount(t *testing.T) {
	db := securityDryRunDB(t)
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})

	var orders []guardedOrder
	query := db.WithContext(ctx).Find(&orders)
	if query.Error != nil {
		t.Fatalf("Find() error = %v", query.Error)
	}
	assertSQLContains(t, query.Statement.SQL.String(), "tenant_id = ?")
	assertVarsContain(t, query.Statement.Vars, "tenant-a")

	update := db.WithContext(ctx).Model(&guardedOrder{}).Updates(map[string]any{"name": "updated"})
	if update.Error != nil {
		t.Fatalf("Updates(map) error = %v", update.Error)
	}
	assertSQLContains(t, update.Statement.SQL.String(), "tenant_id = ?")
	assertVarsContain(t, update.Statement.Vars, "tenant-a")

	deleteTx := db.WithContext(ctx).Delete(&guardedOrder{}, "id = ?", 1)
	if deleteTx.Error != nil {
		t.Fatalf("Delete() error = %v", deleteTx.Error)
	}
	assertSQLContains(t, deleteTx.Statement.SQL.String(), "tenant_id = ?")
	assertVarsContain(t, deleteTx.Statement.Vars, "tenant-a")

	var count int64
	countTx := db.WithContext(ctx).Model(&guardedOrder{}).Count(&count)
	if countTx.Error != nil {
		t.Fatalf("Count() error = %v", countTx.Error)
	}
	assertSQLContains(t, countTx.Statement.SQL.String(), "tenant_id = ?")
	assertVarsContain(t, countTx.Statement.Vars, "tenant-a")
}

func TestGORMGuardsBlockUnscoped(t *testing.T) {
	db := securityDryRunDB(t)
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})

	var orders []guardedOrder
	tx := db.WithContext(ctx).Unscoped().Find(&orders)
	if !errors.Is(tx.Error, gormtenant.ErrUnscopedRequiresHost) {
		t.Fatalf("Unscoped Find() error = %v, want ErrUnscopedRequiresHost", tx.Error)
	}
}

func TestGORMRawRequiresHost(t *testing.T) {
	db := securityDryRunDB(t)
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})
	exec := db.WithContext(ctx).Exec("DELETE FROM guarded_orders")
	if !errors.Is(exec.Error, gormtenant.ErrRawRequiresHost) {
		t.Fatalf("Exec() error = %v, want ErrRawRequiresHost", exec.Error)
	}

	safe := gormtenant.SafeExec(ctx, db, "DELETE FROM guarded_orders WHERE tenant_id = ?", "tenant-a")
	if !errors.Is(safe.Error, gormtenant.ErrRawRequiresHost) {
		t.Fatalf("SafeExec(tenant) error = %v, want ErrRawRequiresHost", safe.Error)
	}

	host := gormtenant.SafeExec(tenantctx.WithHost(context.Background()), db, "DELETE FROM guarded_orders")
	if host.Error != nil {
		t.Fatalf("SafeExec(host) error = %v", host.Error)
	}
}

func securityDryRunDB(t *testing.T) *gorm.DB {
	t.Helper()

	sqlDB, err := sql.Open("mysql", "gorm:gorm@tcp(localhost:9910)/gorm?charset=utf8&parseTime=True&loc=Local")
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	t.Cleanup(func() {
		_ = sqlDB.Close()
	})

	db, err := gorm.Open(mysql.New(mysql.Config{Conn: sqlDB, SkipInitializeWithVersion: true}), &gorm.Config{DryRun: true, DisableAutomaticPing: true, SkipDefaultTransaction: true})
	if err != nil {
		t.Fatalf("gorm.Open() error = %v", err)
	}
	if err := db.Use(gormtenant.New(gormtenant.Config{})); err != nil {
		t.Fatalf("db.Use() error = %v", err)
	}
	return db
}

func assertSQLContains(t *testing.T, sql string, fragment string) {
	t.Helper()
	if !strings.Contains(sql, fragment) {
		t.Fatalf("SQL = %s, want fragment %q", sql, fragment)
	}
}

func assertVarsContain(t *testing.T, vars []any, want string) {
	t.Helper()
	for _, value := range vars {
		if value == want {
			return
		}
	}
	t.Fatalf("Vars = %#v, want value %q", vars, want)
}

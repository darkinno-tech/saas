package gormtenant

import (
	"context"
	"errors"
	"strings"
	"testing"

	tenantctx "github.com/im10furry/saas/core/context"
	"github.com/im10furry/saas/core/types"

	"gorm.io/gorm"
)

type tenantOrderWithItems struct {
	ID       uint
	TenantID string `gorm:"column:tenant_id"`
	Name     string
	Items    []tenantOrderItem `gorm:"foreignKey:OrderID;references:ID"`
}

func (tenantOrderWithItems) TableName() string {
	return "tenant_orders"
}

type tenantOrderItem struct {
	ID       uint
	TenantID string `gorm:"column:tenant_id"`
	OrderID  uint   `gorm:"column:order_id"`
	Name     string
}

func (tenantOrderItem) TableName() string {
	return "tenant_order_items"
}

func TestTenantMutationBoundaryRejectsPartitionKeyChangesAcrossPayloadShapes(t *testing.T) {
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})

	for _, test := range []struct {
		name   string
		mutate func(*gorm.DB) *gorm.DB
	}{
		{
			name: "map database column",
			mutate: func(db *gorm.DB) *gorm.DB {
				return db.WithContext(ctx).Model(&tenantOrder{}).Where("id = ?", 1).Updates(map[string]any{
					"tenant_id": "tenant-b",
					"name":      "forbidden",
				})
			},
		},
		{
			name: "map model field name",
			mutate: func(db *gorm.DB) *gorm.DB {
				return db.WithContext(ctx).Model(&tenantOrder{}).Where("id = ?", 1).Updates(map[string]any{
					"TenantID": "tenant-b",
					"Name":     "forbidden",
				})
			},
		},
		{
			name: "struct value",
			mutate: func(db *gorm.DB) *gorm.DB {
				return db.WithContext(ctx).Model(&tenantOrder{}).Where("id = ?", 1).Updates(tenantOrder{TenantID: "tenant-b", Name: "forbidden"})
			},
		},
		{
			name: "struct pointer",
			mutate: func(db *gorm.DB) *gorm.DB {
				return db.WithContext(ctx).Model(&tenantOrder{}).Where("id = ?", 1).Updates(&tenantOrder{TenantID: "tenant-b", Name: "forbidden"})
			},
		},
		{
			name: "bulk model update",
			mutate: func(db *gorm.DB) *gorm.DB {
				return db.WithContext(ctx).Model(&tenantOrder{}).Where("id IN ?", []uint{1, 2}).Updates(map[string]any{
					"tenant_id": "tenant-b",
					"name":      "forbidden",
				})
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			tx := test.mutate(newDryRunDB(t))
			if !errors.Is(tx.Error, ErrTenantFieldUpdate) {
				t.Fatalf("tenant key update error = %v, want ErrTenantFieldUpdate", tx.Error)
			}
			if sql := tx.Statement.SQL.String(); strings.Contains(strings.ToUpper(sql), "UPDATE") {
				t.Fatalf("blocked tenant key update still built SQL: %s", sql)
			}
		})
	}

	db := newDryRunDB(t)
	allowed := db.WithContext(ctx).Model(&tenantOrder{}).Where("id = ?", 1).Updates(map[string]any{
		"tenant_id": "tenant-a",
		"name":      "allowed",
	})
	if allowed.Error != nil {
		t.Fatalf("Updates(same tenant map) error = %v", allowed.Error)
	}
	assertSQLContains(t, allowed.Statement.SQL.String(), "UPDATE")
	assertSQLContains(t, allowed.Statement.SQL.String(), "tenant_id = ?")
	assertVarsContain(t, allowed.Statement.Vars, "tenant-a")
}

func TestTenantPreloadScopesAssociationsAtPublicQueryBoundary(t *testing.T) {
	tenantContext := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})
	hostContext := tenantctx.WithHost(context.Background())

	t.Run("tenant preload receives scoped association query", func(t *testing.T) {
		db := newDryRunDB(t)
		var orders []tenantOrderWithItems
		tx := db.WithContext(tenantContext).Preload("Items", "name <> ?", "hidden").Find(&orders)
		if tx.Error != nil {
			t.Fatalf("Preload(tenant) error = %v", tx.Error)
		}
		assertSQLContains(t, tx.Statement.SQL.String(), "tenant_id = ?")
		assertVarsContain(t, tx.Statement.Vars, "tenant-a")

		preloadArgs := tx.Statement.Preloads["Items"]
		if len(preloadArgs) != 3 || preloadArgs[0] != "name <> ?" || preloadArgs[1] != "hidden" {
			t.Fatalf("Preload(tenant) args = %#v, want original condition plus tenant scope", preloadArgs)
		}
		scope, ok := preloadArgs[2].(func(*gorm.DB) *gorm.DB)
		if !ok {
			t.Fatalf("Preload(tenant) added scope = %T, want func(*gorm.DB) *gorm.DB", preloadArgs[2])
		}

		var items []tenantOrderItem
		associationQuery := db.WithContext(hostContext).Scopes(scope).Find(&items)
		if associationQuery.Error != nil {
			t.Fatalf("association scope query error = %v", associationQuery.Error)
		}
		assertSQLContains(t, associationQuery.Statement.SQL.String(), "tenant_id = ?")
		assertVarsContain(t, associationQuery.Statement.Vars, "tenant-a")
	})

	t.Run("host preload remains unscoped", func(t *testing.T) {
		db := newDryRunDB(t)
		var orders []tenantOrderWithItems
		tx := db.WithContext(hostContext).Preload("Items", "name <> ?", "hidden").Find(&orders)
		if tx.Error != nil {
			t.Fatalf("Preload(host) error = %v", tx.Error)
		}
		if strings.Contains(tx.Statement.SQL.String(), "tenant_id = ?") {
			t.Fatalf("host preload root query unexpectedly scoped: %s", tx.Statement.SQL.String())
		}
		if args := tx.Statement.Preloads["Items"]; len(args) != 2 || args[0] != "name <> ?" || args[1] != "hidden" {
			t.Fatalf("Preload(host) args = %#v, want only original condition", args)
		}
	})

	t.Run("tenant unscoped preload is rejected", func(t *testing.T) {
		db := newDryRunDB(t)
		var orders []tenantOrderWithItems
		tx := db.WithContext(tenantContext).Unscoped().Preload("Items").Find(&orders)
		if !errors.Is(tx.Error, ErrUnscopedRequiresHost) {
			t.Fatalf("Unscoped().Preload(tenant) error = %v, want ErrUnscopedRequiresHost", tx.Error)
		}
		if sql := tx.Statement.SQL.String(); sql != "" {
			t.Fatalf("rejected tenant unscoped preload built SQL: %s", sql)
		}
	})

	t.Run("host unscoped preload remains allowed", func(t *testing.T) {
		db := newDryRunDB(t)
		var orders []tenantOrderWithItems
		tx := db.WithContext(hostContext).Unscoped().Preload("Items").Find(&orders)
		if tx.Error != nil {
			t.Fatalf("Unscoped().Preload(host) error = %v", tx.Error)
		}
		if strings.Contains(tx.Statement.SQL.String(), "tenant_id = ?") {
			t.Fatalf("host unscoped preload root query unexpectedly scoped: %s", tx.Statement.SQL.String())
		}
		if args := tx.Statement.Preloads["Items"]; len(args) != 0 {
			t.Fatalf("Unscoped().Preload(host) args = %#v, want no injected scope", args)
		}
	})
}

func TestSafeRawAndExecRejectCancelledHostContextBeforeBuildingSQL(t *testing.T) {
	db := newDryRunDB(t)
	hostContext, cancel := context.WithCancel(tenantctx.WithHost(context.Background()))
	cancel()

	raw := SafeRaw(hostContext, db, "SELECT * FROM tenant_orders WHERE id = ?", 1)
	if !errors.Is(raw.Error, context.Canceled) {
		t.Fatalf("SafeRaw(cancelled host) error = %v, want context.Canceled", raw.Error)
	}
	if sql := raw.Statement.SQL.String(); sql != "" {
		t.Fatalf("SafeRaw(cancelled host) built SQL: %s", sql)
	}

	exec := SafeExec(hostContext, db, "UPDATE tenant_orders SET name = ? WHERE id = ?", "blocked", 1)
	if !errors.Is(exec.Error, context.Canceled) {
		t.Fatalf("SafeExec(cancelled host) error = %v, want context.Canceled", exec.Error)
	}
	if sql := exec.Statement.SQL.String(); sql != "" {
		t.Fatalf("SafeExec(cancelled host) built SQL: %s", sql)
	}
}

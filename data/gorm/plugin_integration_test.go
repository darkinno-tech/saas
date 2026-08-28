package gormtenant

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	tenantctx "github.com/im10furry/saas/core/context"
	"github.com/im10furry/saas/core/types"
	"github.com/im10furry/saas/data"

	_ "github.com/go-sql-driver/mysql"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func TestMySQLIntegrationEnforcesTenantIsolation(t *testing.T) {
	dsn := os.Getenv("SAAS_MYSQL_DSN")
	if dsn == "" {
		t.Skip("set SAAS_MYSQL_DSN to run MySQL integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sqlDB, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Errorf("sqlDB.Close() error = %v", err)
		}
	})
	if err := pingMySQLUntilReady(ctx, sqlDB); err != nil {
		t.Fatalf("mysql not ready: %v", err)
	}
	resetTenantOrdersTable(t, ctx, sqlDB)

	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{SkipDefaultTransaction: true})
	if err != nil {
		t.Fatalf("gorm.Open() error = %v", err)
	}
	if err := db.Use(New(Config{})); err != nil {
		t.Fatalf("db.Use() error = %v", err)
	}

	ctxA := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})
	ctxB := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-b"})
	host := tenantctx.WithHost(context.Background())

	orderA := tenantOrder{Name: "a-original"}
	if err := db.WithContext(ctxA).Create(&orderA).Error; err != nil {
		t.Fatalf("Create(tenant-a) error = %v", err)
	}
	if orderA.TenantID != "tenant-a" {
		t.Fatalf("Create(tenant-a) tenant_id = %q, want tenant-a", orderA.TenantID)
	}

	orderB := tenantOrder{Name: "b-original"}
	if err := db.WithContext(ctxB).Create(&orderB).Error; err != nil {
		t.Fatalf("Create(tenant-b) error = %v", err)
	}

	mismatch := tenantOrder{TenantID: "tenant-b", Name: "mismatch"}
	if err := db.WithContext(ctxA).Create(&mismatch).Error; !errors.Is(err, ErrTenantMismatch) {
		t.Fatalf("Create(mismatch) error = %v, want ErrTenantMismatch", err)
	}

	var tenantAOrders []tenantOrder
	if err := db.WithContext(ctxA).Order("id").Find(&tenantAOrders).Error; err != nil {
		t.Fatalf("Find(tenant-a) error = %v", err)
	}
	if len(tenantAOrders) != 1 || tenantAOrders[0].ID != orderA.ID {
		t.Fatalf("Find(tenant-a) = %+v, want only orderA", tenantAOrders)
	}

	var hostOrders []tenantOrder
	if err := db.WithContext(host).Order("id").Find(&hostOrders).Error; err != nil {
		t.Fatalf("Find(host) error = %v", err)
	}
	if len(hostOrders) != 2 {
		t.Fatalf("Find(host) len = %d, want 2; orders=%+v", len(hostOrders), hostOrders)
	}

	updateOther := db.WithContext(ctxA).Model(&tenantOrder{}).Where("id = ?", orderB.ID).Update("name", "hacked")
	if updateOther.Error != nil {
		t.Fatalf("Update(other tenant) error = %v", updateOther.Error)
	}
	if updateOther.RowsAffected != 0 {
		t.Fatalf("Update(other tenant) rows = %d, want 0", updateOther.RowsAffected)
	}

	updateOwn := db.WithContext(ctxA).Model(&tenantOrder{}).Where("id = ?", orderA.ID).Update("name", "a-updated")
	if updateOwn.Error != nil {
		t.Fatalf("Update(own tenant) error = %v", updateOwn.Error)
	}
	if updateOwn.RowsAffected != 1 {
		t.Fatalf("Update(own tenant) rows = %d, want 1", updateOwn.RowsAffected)
	}

	var orderBCheck tenantOrder
	if err := db.WithContext(host).First(&orderBCheck, orderB.ID).Error; err != nil {
		t.Fatalf("First(orderB host) error = %v", err)
	}
	if orderBCheck.Name != "b-original" {
		t.Fatalf("orderB name = %q, want b-original", orderBCheck.Name)
	}

	deleteOther := db.WithContext(ctxA).Where("id = ?", orderB.ID).Delete(&tenantOrder{})
	if deleteOther.Error != nil {
		t.Fatalf("Delete(other tenant) error = %v", deleteOther.Error)
	}
	if deleteOther.RowsAffected != 0 {
		t.Fatalf("Delete(other tenant) rows = %d, want 0", deleteOther.RowsAffected)
	}

	deleteOwn := db.WithContext(ctxA).Where("id = ?", orderA.ID).Delete(&tenantOrder{})
	if deleteOwn.Error != nil {
		t.Fatalf("Delete(own tenant) error = %v", deleteOwn.Error)
	}
	if deleteOwn.RowsAffected != 1 {
		t.Fatalf("Delete(own tenant) rows = %d, want 1", deleteOwn.RowsAffected)
	}

	var deletedA tenantOrder
	if err := db.WithContext(host).Unscoped().First(&deletedA, orderA.ID).Error; err != nil {
		t.Fatalf("Unscoped First(orderA) error = %v", err)
	}
	if !deletedA.DeletedAt.Valid {
		t.Fatalf("orderA deleted_at should be set")
	}

	var rawCount int64
	err = db.WithContext(ctxA).Raw("SELECT COUNT(*) FROM tenant_orders").Scan(&rawCount).Error
	if !errors.Is(err, ErrRawRequiresHost) {
		t.Fatalf("Raw(tenant) error = %v, want ErrRawRequiresHost", err)
	}

	if err := SafeRaw(host, db, "SELECT COUNT(*) FROM tenant_orders").Scan(&rawCount).Error; err != nil {
		t.Fatalf("SafeRaw(host) error = %v", err)
	}
	if rawCount != 2 {
		t.Fatalf("SafeRaw(host) count = %d, want 2 including soft-deleted rows", rawCount)
	}

	if err := db.Find(&[]tenantOrder{}).Error; !errors.Is(err, data.ErrNoTenant) {
		t.Fatalf("Find(no context) error = %v, want ErrNoTenant", err)
	}
}

func pingMySQLUntilReady(ctx context.Context, db *sql.DB) error {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		if err := db.PingContext(ctx); err == nil {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func resetTenantOrdersTable(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()

	statements := []string{
		"DROP TABLE IF EXISTS tenant_orders",
		`CREATE TABLE tenant_orders (
			id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
			tenant_id VARCHAR(191) NOT NULL,
			name VARCHAR(255) NOT NULL,
			deleted_at DATETIME(3) NULL,
			INDEX idx_tenant_orders_tenant_deleted (tenant_id, deleted_at)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("exec %q error = %v", statement, err)
		}
	}
}

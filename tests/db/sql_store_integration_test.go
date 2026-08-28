package db_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/darkinno-tech/saas/core/store"
	"github.com/darkinno-tech/saas/internal/testcontract"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
)

func TestSQLStoreMySQLIntegration(t *testing.T) {
	dsn := os.Getenv("SAAS_MYSQL_DSN")
	if dsn == "" {
		t.Skip("set SAAS_MYSQL_DSN to run MySQL integration test")
	}

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("db.Close() error = %v", err)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := pingUntilReady(ctx, db); err != nil {
		t.Fatalf("mysql not ready: %v", err)
	}
	resetTenantsTable(t, ctx, db)

	sqlStore, err := store.NewSQLStore(db)
	if err != nil {
		t.Fatalf("NewSQLStore() error = %v", err)
	}
	testcontract.RunStoreContract(t, func() store.Store { return sqlStore })
	runSQLStoreCompareAndSwapContract(t, ctx, sqlStore)
}

func TestSQLStorePostgresIntegration(t *testing.T) {
	dsn := os.Getenv("SAAS_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set SAAS_POSTGRES_DSN to run PostgreSQL integration test")
	}

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("db.Close() error = %v", err)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := pingUntilReady(ctx, db); err != nil {
		t.Fatalf("postgres not ready: %v", err)
	}
	resetPostgresTenantsTable(t, ctx, db)

	sqlStore, err := store.NewSQLStore(db, store.WithSQLDialect(store.SQLDialectPostgres))
	if err != nil {
		t.Fatalf("NewSQLStore() error = %v", err)
	}
	testcontract.RunStoreContract(t, func() store.Store { return sqlStore })
	runSQLStoreCompareAndSwapContract(t, ctx, sqlStore)
}

func runSQLStoreCompareAndSwapContract(t *testing.T, ctx context.Context, sqlStore *store.SQLStore) {
	t.Helper()

	expected := testcontract.ContractTenant("tenant-cas")
	if err := sqlStore.Create(ctx, expected); err != nil {
		t.Fatalf("Create(tenant-cas) error = %v", err)
	}

	updated := expected
	updated.Name = "Tenant CAS Updated"
	updated.PlanID = "growth"
	updated.Config = map[string]string{"feature": "advanced"}
	if err := sqlStore.CompareAndSwap(ctx, expected, updated); err != nil {
		t.Fatalf("CompareAndSwap(success) error = %v", err)
	}
	got, err := sqlStore.Get(ctx, expected.ID)
	if err != nil {
		t.Fatalf("Get(after CompareAndSwap) error = %v", err)
	}
	if got.Name != updated.Name || got.PlanID != updated.PlanID || got.Config["feature"] != "advanced" {
		t.Fatalf("Get(after CompareAndSwap) = %+v, want updated tenant", got)
	}

	staleUpdate := expected
	staleUpdate.Name = "Stale update must not apply"
	if err := sqlStore.CompareAndSwap(ctx, expected, staleUpdate); !errors.Is(err, store.ErrTenantConflict) {
		t.Fatalf("CompareAndSwap(stale) error = %v, want ErrTenantConflict", err)
	}
	if err := sqlStore.CompareAndSwap(ctx, updated, updated); err != nil {
		t.Fatalf("CompareAndSwap(no-op) error = %v", err)
	}

	missing := testcontract.ContractTenant("tenant-cas-missing")
	missingUpdated := missing
	missingUpdated.Name = "Missing"
	if err := sqlStore.CompareAndSwap(ctx, missing, missingUpdated); !errors.Is(err, store.ErrTenantNotFound) {
		t.Fatalf("CompareAndSwap(missing) error = %v, want ErrTenantNotFound", err)
	}
}

func pingUntilReady(ctx context.Context, db *sql.DB) error {
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

func resetPostgresTenantsTable(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()

	statements := []string{
		"DROP TABLE IF EXISTS tenants",
		`CREATE TABLE tenants (
			id VARCHAR(191) PRIMARY KEY,
			name VARCHAR(255) NOT NULL,
			status VARCHAR(32) NOT NULL,
			plan_id VARCHAR(191) NOT NULL,
			config JSONB NOT NULL
		)`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("exec %q error = %v", statement, err)
		}
	}
}

func resetTenantsTable(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()

	statements := []string{
		"DROP TABLE IF EXISTS tenants",
		`CREATE TABLE tenants (
			id VARCHAR(191) NOT NULL PRIMARY KEY,
			name VARCHAR(255) NOT NULL,
			status VARCHAR(32) NOT NULL,
			plan_id VARCHAR(191) NOT NULL,
			config JSON NOT NULL
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("exec %q error = %v", statement, err)
		}
	}
}

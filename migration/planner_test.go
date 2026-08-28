package migration

import (
	"errors"
	"testing"

	"github.com/darkinno-tech/saas/core/types"
)

func TestAddTenantColumn(t *testing.T) {
	sql, err := NewPlanner(DialectPostgres).AddTenantColumn("public.orders", "", "")
	if err != nil {
		t.Fatalf("AddTenantColumn() error = %v", err)
	}
	want := "ALTER TABLE public.orders ADD COLUMN tenant_id VARCHAR(64)"
	if sql != want {
		t.Fatalf("AddTenantColumn() = %q, want %q", sql, want)
	}

	if _, err := NewPlanner(DialectPostgres).AddTenantColumn("orders;drop", "", ""); !errors.Is(err, ErrInvalidIdentifier) {
		t.Fatalf("AddTenantColumn(unsafe) error = %v, want ErrInvalidIdentifier", err)
	}
}

func TestCreateUniqueIndexes(t *testing.T) {
	postgres := NewPlanner(DialectPostgres)
	sql, err := postgres.CreateSoftDeleteUniqueIndex("orders", "idx_orders_name", "", []string{"name"}, "")
	if err != nil {
		t.Fatalf("CreateSoftDeleteUniqueIndex(postgres) error = %v", err)
	}
	want := "CREATE UNIQUE INDEX idx_orders_name ON orders (tenant_id, name) WHERE deleted_at IS NULL"
	if sql != want {
		t.Fatalf("soft index = %q, want %q", sql, want)
	}

	mysql := NewPlanner(DialectMySQL)
	sql, err = mysql.CreateSoftDeleteUniqueIndex("orders", "idx_orders_name", "", []string{"name"}, "")
	if err != nil {
		t.Fatalf("CreateSoftDeleteUniqueIndex(mysql) error = %v", err)
	}
	want = "CREATE UNIQUE INDEX idx_orders_name ON orders (tenant_id, name, deleted_flag)"
	if sql != want {
		t.Fatalf("mysql soft index = %q, want %q", sql, want)
	}

	sql, err = postgres.CreateHardDeleteUniqueIndex("orders", "idx_orders_name", "", []string{"name"})
	if err != nil {
		t.Fatalf("CreateHardDeleteUniqueIndex() error = %v", err)
	}
	want = "CREATE UNIQUE INDEX idx_orders_name ON orders (tenant_id, name)"
	if sql != want {
		t.Fatalf("hard index = %q, want %q", sql, want)
	}
}

func TestSeedTenants(t *testing.T) {
	statements, err := NewPlanner(DialectSQLite).SeedTenants("tenants", []types.Tenant{{
		ID:     "tenant-a",
		Name:   "Tenant A",
		Status: types.TenantStatusActive,
		PlanID: "starter",
		Config: map[string]string{"region": "us"},
	}})
	if err != nil {
		t.Fatalf("SeedTenants() error = %v", err)
	}
	if len(statements) != 1 {
		t.Fatalf("SeedTenants() len = %d, want 1", len(statements))
	}
	wantSQL := "INSERT INTO tenants (id, name, status, plan_id, config) VALUES (?, ?, ?, ?, ?)"
	if statements[0].SQL != wantSQL {
		t.Fatalf("SeedTenants SQL = %q, want %q", statements[0].SQL, wantSQL)
	}
	if got := statements[0].Args[0]; got != "tenant-a" {
		t.Fatalf("SeedTenants first arg = %v, want tenant-a", got)
	}
	if got := statements[0].Args[4]; got != `{"region":"us"}` {
		t.Fatalf("SeedTenants config arg = %v, want encoded config", got)
	}

	postgres, err := NewPlanner(DialectPostgres).SeedTenants("tenants", []types.Tenant{{
		ID: "tenant-a", Status: types.TenantStatusActive,
	}})
	if err != nil {
		t.Fatalf("SeedTenants(postgres) error = %v", err)
	}
	wantPostgres := "INSERT INTO tenants (id, name, status, plan_id, config) VALUES ($1, $2, $3, $4, $5)"
	if postgres[0].SQL != wantPostgres {
		t.Fatalf("SeedTenants(postgres) SQL = %q, want %q", postgres[0].SQL, wantPostgres)
	}
	if got := postgres[0].Args[4]; got != `{}` {
		t.Fatalf("SeedTenants(postgres) nil config = %v, want {}", got)
	}
}

func TestDetectDeletePolicy(t *testing.T) {
	if got := DetectDeletePolicy([]string{"id", "deleted_at"}, ""); got != "soft" {
		t.Fatalf("DetectDeletePolicy(soft) = %q, want soft", got)
	}
	if got := DetectDeletePolicy([]string{"id"}, ""); got != "hard" {
		t.Fatalf("DetectDeletePolicy(hard) = %q, want hard", got)
	}
}

func TestPlannerValidation(t *testing.T) {
	if _, err := NewPlanner("oracle").AddTenantColumn("orders", "", ""); !errors.Is(err, ErrUnsupportedDialect) {
		t.Fatalf("unsupported dialect error = %v, want ErrUnsupportedDialect", err)
	}
	if _, err := NewPlanner(DialectPostgres).CreateHardDeleteUniqueIndex("orders", "idx", "", nil); !errors.Is(err, ErrInvalidMigration) {
		t.Fatalf("missing business fields error = %v, want ErrInvalidMigration", err)
	}
	if _, err := NewPlanner(DialectPostgres).CreateHardDeleteUniqueIndex("orders", "idx", "", []string{"bad-name"}); !errors.Is(err, ErrInvalidIdentifier) {
		t.Fatalf("unsafe business field error = %v, want ErrInvalidIdentifier", err)
	}
	if _, err := NewPlanner(DialectMySQL).CreateMySQLSoftDeleteUniqueIndex("orders", "idx", "", []string{"name"}, "bad-name"); !errors.Is(err, ErrInvalidIdentifier) {
		t.Fatalf("unsafe marker field error = %v, want ErrInvalidIdentifier", err)
	}
	if _, err := NewPlanner(DialectPostgres).CreateMySQLSoftDeleteUniqueIndex("orders", "idx", "", []string{"name"}, "deleted_flag"); !errors.Is(err, ErrUnsupportedDialect) {
		t.Fatalf("mysql helper on postgres error = %v, want ErrUnsupportedDialect", err)
	}
}

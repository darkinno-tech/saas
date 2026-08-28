package db_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/darkinno-tech/saas/core/types"
	"github.com/darkinno-tech/saas/deployment"
)

func TestDeploymentSQLStoreMySQLIntegration(t *testing.T) {
	runDeploymentSQLStoreIntegration(t, "mysql", os.Getenv("SAAS_MYSQL_DSN"), resetMySQLDeploymentTables, func(db *sql.DB) (*deployment.SQLStore, error) {
		return deployment.NewSQLStore(db)
	})
}

func TestDeploymentSQLStorePostgresIntegration(t *testing.T) {
	runDeploymentSQLStoreIntegration(t, "postgres", os.Getenv("SAAS_POSTGRES_DSN"), resetPostgresDeploymentTables, func(db *sql.DB) (*deployment.SQLStore, error) {
		return deployment.NewSQLStore(db, deployment.WithSQLDialect(deployment.SQLDialectPostgres))
	})
}

func runDeploymentSQLStoreIntegration(t *testing.T, driver, dsn string, reset func(*testing.T, context.Context, *sql.DB), newStore func(*sql.DB) (*deployment.SQLStore, error)) {
	t.Helper()
	if dsn == "" {
		t.Skipf("set the %s DSN to run deployment SQL integration tests", driver)
	}

	db, err := sql.Open(driver, dsn)
	if err != nil {
		t.Fatalf("sql.Open(%s) error = %v", driver, err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("db.Close() error = %v", err)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := pingUntilReady(ctx, db); err != nil {
		t.Fatalf("%s not ready: %v", driver, err)
	}
	reset(t, ctx, db)

	store, err := newStore(db)
	if err != nil {
		t.Fatalf("NewSQLStore() error = %v", err)
	}
	runDeploymentSQLStoreContract(t, ctx, store)
}

func runDeploymentSQLStoreContract(t *testing.T, ctx context.Context, store *deployment.SQLStore) {
	t.Helper()
	service := deployment.New(store)
	source := types.DeploymentUnit{
		ID:            "cn-a",
		Status:        types.DeploymentUnitStatusActive,
		Region:        "cn-east-1",
		ResidencyTags: []string{"cn", "pipl"},
		Metadata:      map[string]string{"operator": "example-cn", "tier": "production"},
	}
	target := types.DeploymentUnit{
		ID:            "eu-a",
		Status:        types.DeploymentUnitStatusActive,
		Region:        "eu-central-1",
		ResidencyTags: []string{"eu", "gdpr"},
		Metadata:      map[string]string{"operator": "example-eu", "tier": "production"},
	}
	for _, unit := range []types.DeploymentUnit{source, target} {
		if err := service.CreateUnit(ctx, unit); err != nil {
			t.Fatalf("CreateUnit(%q) error = %v", unit.ID, err)
		}
	}

	gotSource, err := store.GetUnit(ctx, source.ID)
	if err != nil {
		t.Fatalf("GetUnit(source) error = %v", err)
	}
	assertDeploymentUnitEqual(t, gotSource, source)
	units, err := store.ListUnits(ctx)
	if err != nil {
		t.Fatalf("ListUnits() error = %v", err)
	}
	if len(units) != 2 || units[0].ID != source.ID || units[1].ID != target.ID {
		t.Fatalf("ListUnits() = %#v, want units ordered by ID", units)
	}

	tenant := types.Tenant{ID: "tenant-a", Status: types.TenantStatusActive}
	assignment, err := service.Assign(ctx, tenant, source.ID)
	if err != nil {
		t.Fatalf("Assign() error = %v", err)
	}
	if want := (deployment.Assignment{TenantID: tenant.ID, UnitID: source.ID, Version: 1}); assignment != want {
		t.Fatalf("Assign() = %#v, want %#v", assignment, want)
	}
	assignments, err := store.ListAssignmentsByUnit(ctx, source.ID)
	if err != nil {
		t.Fatalf("ListAssignmentsByUnit(source) error = %v", err)
	}
	if len(assignments) != 1 || assignments[0] != assignment {
		t.Fatalf("ListAssignmentsByUnit(source) = %#v, want %#v", assignments, assignment)
	}
	resolved, err := service.Resolve(ctx, tenant)
	if err != nil {
		t.Fatalf("Resolve(before move) error = %v", err)
	}
	assertDeploymentUnitEqual(t, resolved, source)

	move, err := service.PrepareMove(ctx, tenant, target.ID)
	if err != nil {
		t.Fatalf("PrepareMove() error = %v", err)
	}
	if want := (deployment.Move{TenantID: tenant.ID, SourceUnitID: source.ID, TargetUnitID: target.ID}); move != want {
		t.Fatalf("PrepareMove() = %#v, want %#v", move, want)
	}
	moves, err := store.ListMovesByUnit(ctx, target.ID)
	if err != nil {
		t.Fatalf("ListMovesByUnit(target) error = %v", err)
	}
	if len(moves) != 1 || moves[0] != move {
		t.Fatalf("ListMovesByUnit(target) = %#v, want %#v", moves, move)
	}
	resolved, err = service.Resolve(ctx, tenant)
	if err != nil {
		t.Fatalf("Resolve(prepared move) error = %v", err)
	}
	assertDeploymentUnitEqual(t, resolved, source)

	assignment, err = service.CutoverMove(ctx, tenant)
	if err != nil {
		t.Fatalf("CutoverMove() error = %v", err)
	}
	if want := (deployment.Assignment{TenantID: tenant.ID, UnitID: target.ID, Version: 2}); assignment != want {
		t.Fatalf("CutoverMove() = %#v, want %#v", assignment, want)
	}
	resolved, err = service.Resolve(ctx, tenant)
	if err != nil {
		t.Fatalf("Resolve(after cutover) error = %v", err)
	}
	assertDeploymentUnitEqual(t, resolved, target)
	assignments, err = store.ListAssignmentsByUnit(ctx, target.ID)
	if err != nil {
		t.Fatalf("ListAssignmentsByUnit(target) error = %v", err)
	}
	if len(assignments) != 1 || assignments[0] != assignment {
		t.Fatalf("ListAssignmentsByUnit(target) = %#v, want %#v", assignments, assignment)
	}
	moves, err = store.ListMovesByUnit(ctx, target.ID)
	if err != nil {
		t.Fatalf("ListMovesByUnit(target after cutover) error = %v", err)
	}
	if len(moves) != 0 {
		t.Fatalf("ListMovesByUnit(target after cutover) = %#v, want no prepared moves", moves)
	}
	if _, err := store.GetMove(ctx, tenant.ID); !errors.Is(err, deployment.ErrMoveNotFound) {
		t.Fatalf("GetMove(after cutover) error = %v, want ErrMoveNotFound", err)
	}

	if _, err := service.DisableUnit(ctx, source.ID); err != nil {
		t.Fatalf("DisableUnit(old source) error = %v", err)
	}
	if err := service.DeleteUnit(ctx, source.ID); err != nil {
		t.Fatalf("DeleteUnit(old source) error = %v", err)
	}
	units, err = store.ListUnits(ctx)
	if err != nil {
		t.Fatalf("ListUnits(after source deletion) error = %v", err)
	}
	if len(units) != 1 || units[0].ID != target.ID {
		t.Fatalf("ListUnits(after source deletion) = %#v, want only target", units)
	}
}

func assertDeploymentUnitEqual(t *testing.T, got, want types.DeploymentUnit) {
	t.Helper()
	if got.ID != want.ID || got.Status != want.Status || got.Region != want.Region || !reflect.DeepEqual(got.ResidencyTags, want.ResidencyTags) || !reflect.DeepEqual(got.Metadata, want.Metadata) {
		t.Fatalf("deployment unit = %#v, want %#v", got, want)
	}
}

func resetMySQLDeploymentTables(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	resetDeploymentTables(t, ctx, db, []string{
		"DROP TABLE IF EXISTS saas_deployment_moves",
		"DROP TABLE IF EXISTS saas_tenant_deployments",
		"DROP TABLE IF EXISTS saas_deployment_units",
		`CREATE TABLE saas_deployment_units (
			id VARCHAR(191) NOT NULL PRIMARY KEY,
			status VARCHAR(32) NOT NULL,
			region VARCHAR(191) NOT NULL,
			residency_tags JSON NOT NULL,
			metadata JSON NOT NULL
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE saas_tenant_deployments (
			tenant_id VARCHAR(191) NOT NULL PRIMARY KEY,
			deployment_unit_id VARCHAR(191) NOT NULL,
			version BIGINT UNSIGNED NOT NULL,
			INDEX saas_tenant_deployments_unit_idx (deployment_unit_id),
			CONSTRAINT saas_tenant_deployments_unit_fk
				FOREIGN KEY (deployment_unit_id) REFERENCES saas_deployment_units (id)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE saas_deployment_moves (
			tenant_id VARCHAR(191) NOT NULL PRIMARY KEY,
			source_unit_id VARCHAR(191) NOT NULL,
			target_unit_id VARCHAR(191) NOT NULL,
			INDEX saas_deployment_moves_source_idx (source_unit_id),
			INDEX saas_deployment_moves_target_idx (target_unit_id),
			CONSTRAINT saas_deployment_moves_source_fk
				FOREIGN KEY (source_unit_id) REFERENCES saas_deployment_units (id),
			CONSTRAINT saas_deployment_moves_target_fk
				FOREIGN KEY (target_unit_id) REFERENCES saas_deployment_units (id)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	})
}

func resetPostgresDeploymentTables(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	resetDeploymentTables(t, ctx, db, []string{
		"DROP TABLE IF EXISTS saas_deployment_moves",
		"DROP TABLE IF EXISTS saas_tenant_deployments",
		"DROP TABLE IF EXISTS saas_deployment_units",
		`CREATE TABLE saas_deployment_units (
			id VARCHAR(191) PRIMARY KEY,
			status VARCHAR(32) NOT NULL,
			region VARCHAR(191) NOT NULL,
			residency_tags JSONB NOT NULL,
			metadata JSONB NOT NULL
		)`,
		`CREATE TABLE saas_tenant_deployments (
			tenant_id VARCHAR(191) PRIMARY KEY,
			deployment_unit_id VARCHAR(191) NOT NULL,
			version BIGINT NOT NULL,
			CONSTRAINT saas_tenant_deployments_unit_fk
				FOREIGN KEY (deployment_unit_id) REFERENCES saas_deployment_units (id)
		)`,
		"CREATE INDEX saas_tenant_deployments_unit_idx ON saas_tenant_deployments (deployment_unit_id)",
		`CREATE TABLE saas_deployment_moves (
			tenant_id VARCHAR(191) PRIMARY KEY,
			source_unit_id VARCHAR(191) NOT NULL,
			target_unit_id VARCHAR(191) NOT NULL,
			CONSTRAINT saas_deployment_moves_source_fk
				FOREIGN KEY (source_unit_id) REFERENCES saas_deployment_units (id),
			CONSTRAINT saas_deployment_moves_target_fk
				FOREIGN KEY (target_unit_id) REFERENCES saas_deployment_units (id)
		)`,
		"CREATE INDEX saas_deployment_moves_source_idx ON saas_deployment_moves (source_unit_id)",
		"CREATE INDEX saas_deployment_moves_target_idx ON saas_deployment_moves (target_unit_id)",
	})
}

func resetDeploymentTables(t *testing.T, ctx context.Context, db *sql.DB, statements []string) {
	t.Helper()
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("exec %q error = %v", statement, err)
		}
	}
}

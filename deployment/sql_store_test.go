package deployment

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/im10furry/saas/core/types"
)

func TestNewSQLStoreValidation(t *testing.T) {
	if _, err := NewSQLStore(nil); !errors.Is(err, ErrNilDB) {
		t.Fatalf("NewSQLStore(nil) error = %v, want ErrNilDB", err)
	}

	db := &sql.DB{}
	store, err := NewSQLStore(db)
	if err != nil {
		t.Fatalf("NewSQLStore() error = %v", err)
	}
	if store.unitTable != DefaultSQLUnitTableName || store.assignmentTable != DefaultSQLAssignmentTableName || store.moveTable != DefaultSQLMoveTableName {
		t.Fatalf("NewSQLStore() tables = %#v, want defaults", store)
	}
	if _, err := NewSQLStore(db, WithUnitTableName("units;drop")); !errors.Is(err, ErrInvalidTableName) {
		t.Fatalf("NewSQLStore(unsafe unit table) error = %v, want ErrInvalidTableName", err)
	}
	if _, err := NewSQLStore(db, WithSQLDialect("oracle")); !errors.Is(err, ErrUnsupportedSQLDialect) {
		t.Fatalf("NewSQLStore(unsupported dialect) error = %v, want ErrUnsupportedSQLDialect", err)
	}
}

func TestSQLStoreUnitRoundTripUsesConfiguredDialect(t *testing.T) {
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectPostgres)
	unit := types.DeploymentUnit{
		ID:            "eu-central-1",
		Status:        types.DeploymentUnitStatusActive,
		Region:        "eu-central-1",
		ResidencyTags: []string{"eu", "gdpr"},
		Metadata:      map[string]string{"provider": "example"},
	}

	create := mock.ExpectExec(regexp.QuoteMeta("INSERT INTO saas_deployment_units (id, status, region, residency_tags, metadata) VALUES ($1, $2, $3, $4, $5)"))
	create.WithArgs("eu-central-1", "active", "eu-central-1", `["eu","gdpr"]`, `{"provider":"example"}`).WillReturnResult(sqlmock.NewResult(1, 1))
	if err := store.CreateUnit(ctx, unit); err != nil {
		t.Fatalf("CreateUnit() error = %v", err)
	}

	get := mock.ExpectQuery(regexp.QuoteMeta("SELECT id, status, region, residency_tags, metadata FROM saas_deployment_units WHERE id = $1"))
	get.WithArgs("eu-central-1").WillReturnRows(sqlmock.NewRows([]string{"id", "status", "region", "residency_tags", "metadata"}).AddRow("eu-central-1", "active", "eu-central-1", `["eu","gdpr"]`, `{"provider":"example"}`))
	got, err := store.GetUnit(ctx, unit.ID)
	if err != nil {
		t.Fatalf("GetUnit() error = %v", err)
	}
	if !unitsEqual(got, unit) {
		t.Fatalf("GetUnit() = %#v, want %#v", got, unit)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreCompareAndSwapAssignmentDetectsConflict(t *testing.T) {
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectPostgres)
	expected := Assignment{TenantID: "tenant-a", UnitID: "cn-a", Version: 1}
	updated := Assignment{TenantID: "tenant-a", UnitID: "eu-a", Version: 2}

	mock.ExpectBegin()
	unit := mock.ExpectQuery(regexp.QuoteMeta("SELECT id, status, region, residency_tags, metadata FROM saas_deployment_units WHERE id = $1 FOR UPDATE"))
	unit.WithArgs("eu-a").WillReturnRows(sqlmock.NewRows([]string{"id", "status", "region", "residency_tags", "metadata"}).AddRow("eu-a", "active", "eu-central-1", `[]`, `{}`))
	get := mock.ExpectQuery(regexp.QuoteMeta("SELECT tenant_id, deployment_unit_id, version FROM saas_tenant_deployments WHERE tenant_id = $1 FOR UPDATE"))
	get.WithArgs("tenant-a").WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "deployment_unit_id", "version"}).AddRow("tenant-a", "cn-a", uint64(3)))
	mock.ExpectRollback()

	if err := store.CompareAndSwapAssignment(ctx, expected, updated); !errors.Is(err, ErrAssignmentConflict) {
		t.Fatalf("CompareAndSwapAssignment() error = %v, want ErrAssignmentConflict", err)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreCutoverMoveUsesOneTransaction(t *testing.T) {
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectPostgres)
	expected := Assignment{TenantID: "tenant-a", UnitID: "cn-a", Version: 1}
	move := Move{TenantID: "tenant-a", SourceUnitID: "cn-a", TargetUnitID: "eu-a"}
	updated := Assignment{TenantID: "tenant-a", UnitID: "eu-a", Version: 2}

	mock.ExpectBegin()
	unit := mock.ExpectQuery(regexp.QuoteMeta("SELECT id, status, region, residency_tags, metadata FROM saas_deployment_units WHERE id = $1 FOR UPDATE"))
	unit.WithArgs("eu-a").WillReturnRows(sqlmock.NewRows([]string{"id", "status", "region", "residency_tags", "metadata"}).AddRow("eu-a", "active", "eu-central-1", `[]`, `{}`))
	assignment := mock.ExpectQuery(regexp.QuoteMeta("SELECT tenant_id, deployment_unit_id, version FROM saas_tenant_deployments WHERE tenant_id = $1 FOR UPDATE"))
	assignment.WithArgs("tenant-a").WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "deployment_unit_id", "version"}).AddRow("tenant-a", "cn-a", uint64(1)))
	prepared := mock.ExpectQuery(regexp.QuoteMeta("SELECT tenant_id, source_unit_id, target_unit_id FROM saas_deployment_moves WHERE tenant_id = $1 FOR UPDATE"))
	prepared.WithArgs("tenant-a").WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "source_unit_id", "target_unit_id"}).AddRow("tenant-a", "cn-a", "eu-a"))
	update := mock.ExpectExec(regexp.QuoteMeta("UPDATE saas_tenant_deployments SET deployment_unit_id = $1, version = $2 WHERE tenant_id = $3"))
	update.WithArgs("eu-a", uint64(2), "tenant-a").WillReturnResult(sqlmock.NewResult(0, 1))
	deleteMove := mock.ExpectExec(regexp.QuoteMeta("DELETE FROM saas_deployment_moves WHERE tenant_id = $1 AND source_unit_id = $2 AND target_unit_id = $3"))
	deleteMove.WithArgs("tenant-a", "cn-a", "eu-a").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := store.CutoverMove(ctx, expected, move, updated); err != nil {
		t.Fatalf("CutoverMove() error = %v", err)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreCreateAssignmentRejectsDisabledUnitInTransaction(t *testing.T) {
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectPostgres)
	assignment := Assignment{TenantID: "tenant-a", UnitID: "eu-a", Version: 1}

	mock.ExpectBegin()
	unit := mock.ExpectQuery(regexp.QuoteMeta("SELECT id, status, region, residency_tags, metadata FROM saas_deployment_units WHERE id = $1 FOR UPDATE"))
	unit.WithArgs("eu-a").WillReturnRows(sqlmock.NewRows([]string{"id", "status", "region", "residency_tags", "metadata"}).AddRow("eu-a", "disabled", "eu-central-1", `[]`, `{}`))
	mock.ExpectRollback()

	if err := store.CreateAssignment(ctx, assignment); !errors.Is(err, ErrDeploymentUnitUnavailable) {
		t.Fatalf("CreateAssignment() error = %v, want ErrDeploymentUnitUnavailable", err)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreDeleteUnitRejectsReferencesInTransaction(t *testing.T) {
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectPostgres)

	mock.ExpectBegin()
	unit := mock.ExpectQuery(regexp.QuoteMeta("SELECT id, status, region, residency_tags, metadata FROM saas_deployment_units WHERE id = $1 FOR UPDATE"))
	unit.WithArgs("eu-a").WillReturnRows(sqlmock.NewRows([]string{"id", "status", "region", "residency_tags", "metadata"}).AddRow("eu-a", "active", "eu-central-1", `[]`, `{}`))
	reference := mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM saas_tenant_deployments WHERE deployment_unit_id = $1 LIMIT 1"))
	reference.WithArgs("eu-a").WillReturnRows(sqlmock.NewRows([]string{"one"}).AddRow(1))
	mock.ExpectRollback()

	if err := store.DeleteUnit(ctx, "eu-a"); !errors.Is(err, ErrDeploymentUnitInUse) {
		t.Fatalf("DeleteUnit() error = %v, want ErrDeploymentUnitInUse", err)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreDisableUnitChecksReferencesInTransaction(t *testing.T) {
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectPostgres)

	mock.ExpectBegin()
	unit := mock.ExpectQuery(regexp.QuoteMeta("SELECT id, status, region, residency_tags, metadata FROM saas_deployment_units WHERE id = $1 FOR UPDATE"))
	unit.WithArgs("eu-a").WillReturnRows(sqlmock.NewRows([]string{"id", "status", "region", "residency_tags", "metadata"}).AddRow("eu-a", "active", "eu-central-1", `[]`, `{}`))
	assignmentReference := mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM saas_tenant_deployments WHERE deployment_unit_id = $1 LIMIT 1"))
	assignmentReference.WithArgs("eu-a").WillReturnRows(sqlmock.NewRows([]string{"one"}).AddRow(1))
	mock.ExpectRollback()

	if _, _, err := store.DisableUnit(ctx, "eu-a"); !errors.Is(err, ErrDeploymentUnitInUse) {
		t.Fatalf("DisableUnit() error = %v, want ErrDeploymentUnitInUse", err)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreListsMovesBySourceOrTarget(t *testing.T) {
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectMySQL)
	rows := sqlmock.NewRows([]string{"tenant_id", "source_unit_id", "target_unit_id"})
	rows.AddRow("tenant-a", "cn-a", "eu-a")
	rows.AddRow("tenant-b", "us-a", "cn-a")
	list := mock.ExpectQuery(regexp.QuoteMeta("SELECT tenant_id, source_unit_id, target_unit_id FROM saas_deployment_moves WHERE source_unit_id = ? OR target_unit_id = ? ORDER BY tenant_id"))
	list.WithArgs("cn-a", "cn-a").WillReturnRows(rows)

	moves, err := store.ListMovesByUnit(ctx, "cn-a")
	if err != nil {
		t.Fatalf("ListMovesByUnit() error = %v", err)
	}
	if len(moves) != 2 || moves[0].TenantID != "tenant-a" || moves[1].TargetUnitID != "cn-a" {
		t.Fatalf("ListMovesByUnit() = %#v, want source and target matches", moves)
	}
	assertSQLMockExpectations(t, mock)
}

func newMockSQLStore(t *testing.T, dialect SQLDialect) (*SQLStore, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := NewSQLStore(db, WithSQLDialect(dialect))
	if err != nil {
		t.Fatalf("NewSQLStore() error = %v", err)
	}
	return store, mock
}

func assertSQLMockExpectations(t *testing.T, mock sqlmock.Sqlmock) {
	t.Helper()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sqlmock expectations: %v", err)
	}
}

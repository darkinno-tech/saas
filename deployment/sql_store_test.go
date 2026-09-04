package deployment

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/darkinno-tech/saas/core/types"
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

func TestSQLStoreListUnits(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectMySQL)
	rows := sqlmock.NewRows([]string{"id", "status", "region", "residency_tags", "metadata"})
	rows.AddRow("eu-a", "active", "eu-central-1", `["eu"]`, `{"a":"b"}`)
	rows.AddRow("us-a", "disabled", "us-east-1", `[]`, `{}`)
	list := mock.ExpectQuery(regexp.QuoteMeta("SELECT id, status, region, residency_tags, metadata FROM saas_deployment_units ORDER BY id"))
	list.WillReturnRows(rows)

	units, err := store.ListUnits(ctx)
	if err != nil {
		t.Fatalf("ListUnits() error = %v", err)
	}
	if len(units) != 2 || units[0].ID != "eu-a" || units[1].ID != "us-a" || units[1].Status != types.DeploymentUnitStatusDisabled {
		t.Fatalf("ListUnits() = %#v, want 2 ordered units", units)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreListUnitsEmpty(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectMySQL)
	empty := mock.ExpectQuery(regexp.QuoteMeta("SELECT id, status, region, residency_tags, metadata FROM saas_deployment_units ORDER BY id"))
	empty.WillReturnRows(sqlmock.NewRows([]string{"id", "status", "region", "residency_tags", "metadata"}))

	units, err := store.ListUnits(ctx)
	if err != nil {
		t.Fatalf("ListUnits() error = %v", err)
	}
	if len(units) != 0 {
		t.Fatalf("ListUnits() = %#v, want empty", units)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreListUnitsQueryError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectMySQL)
	sentinel := errors.New("query failed")
	list := mock.ExpectQuery(regexp.QuoteMeta("SELECT id, status, region, residency_tags, metadata FROM saas_deployment_units ORDER BY id"))
	list.WillReturnError(sentinel)

	if _, err := store.ListUnits(ctx); !errors.Is(err, sentinel) {
		t.Fatalf("ListUnits() error = %v, want sentinel", err)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreGetUnitNotFound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectMySQL)
	get := mock.ExpectQuery(regexp.QuoteMeta("SELECT id, status, region, residency_tags, metadata FROM saas_deployment_units WHERE id = ?"))
	get.WithArgs("missing").WillReturnRows(sqlmock.NewRows([]string{"id", "status", "region", "residency_tags", "metadata"}))

	if _, err := store.GetUnit(ctx, "missing"); !errors.Is(err, ErrDeploymentUnitNotFound) {
		t.Fatalf("GetUnit() error = %v, want ErrDeploymentUnitNotFound", err)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreGetUnitInvalidID(t *testing.T) {
	t.Parallel()
	store, _ := newMockSQLStore(t, SQLDialectPostgres)
	if _, err := store.GetUnit(context.Background(), ""); !errors.Is(err, ErrInvalidDeploymentUnit) {
		t.Fatalf("GetUnit() error = %v, want ErrInvalidDeploymentUnit", err)
	}
}

func TestSQLStoreGetUnitCanceledContext(t *testing.T) {
	t.Parallel()
	store, _ := newMockSQLStore(t, SQLDialectPostgres)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.GetUnit(ctx, "eu-a"); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetUnit() error = %v, want context.Canceled", err)
	}
}

func TestSQLStoreCreateUnitInvalidUnit(t *testing.T) {
	t.Parallel()
	store, _ := newMockSQLStore(t, SQLDialectPostgres)
	bad := types.DeploymentUnit{ID: "eu-a", Status: "weird", Region: ""}
	if err := store.CreateUnit(context.Background(), bad); !errors.Is(err, ErrInvalidDeploymentUnit) {
		t.Fatalf("CreateUnit() error = %v, want ErrInvalidDeploymentUnit", err)
	}
}

func TestSQLStoreCreateUnitDuplicateKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectPostgres)
	unit := types.DeploymentUnit{ID: "eu-a", Status: types.DeploymentUnitStatusActive, Region: "eu-central-1"}
	create := mock.ExpectExec(regexp.QuoteMeta("INSERT INTO saas_deployment_units (id, status, region, residency_tags, metadata) VALUES ($1, $2, $3, $4, $5)"))
	create.WithArgs("eu-a", "active", "eu-central-1", `[]`, `{}`).WillReturnError(errors.New("duplicate key value violates unique constraint"))

	if err := store.CreateUnit(ctx, unit); !errors.Is(err, ErrDeploymentUnitAlreadyExists) {
		t.Fatalf("CreateUnit() error = %v, want ErrDeploymentUnitAlreadyExists", err)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreUpdateUnitSuccess(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectPostgres)
	unit := types.DeploymentUnit{ID: "eu-a", Status: types.DeploymentUnitStatusActive, Region: "eu-central-1", ResidencyTags: []string{"eu"}, Metadata: map[string]string{"k": "v"}}

	mock.ExpectBegin()
	get := mock.ExpectQuery(regexp.QuoteMeta("SELECT id, status, region, residency_tags, metadata FROM saas_deployment_units WHERE id = $1 FOR UPDATE"))
	get.WithArgs("eu-a").WillReturnRows(sqlmock.NewRows([]string{"id", "status", "region", "residency_tags", "metadata"}).AddRow("eu-a", "active", "eu-central-1", `["eu"]`, `{"k":"v"}`))
	update := mock.ExpectExec(regexp.QuoteMeta("UPDATE saas_deployment_units SET status = $1, region = $2, residency_tags = $3, metadata = $4 WHERE id = $5"))
	update.WithArgs("active", "eu-central-1", `["eu"]`, `{"k":"v"}`, "eu-a").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := store.UpdateUnit(ctx, unit); err != nil {
		t.Fatalf("UpdateUnit() error = %v", err)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreUpdateUnitStatusMismatch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectPostgres)
	unit := types.DeploymentUnit{ID: "eu-a", Status: types.DeploymentUnitStatusActive, Region: "eu-central-1"}

	mock.ExpectBegin()
	get := mock.ExpectQuery(regexp.QuoteMeta("SELECT id, status, region, residency_tags, metadata FROM saas_deployment_units WHERE id = $1 FOR UPDATE"))
	get.WithArgs("eu-a").WillReturnRows(sqlmock.NewRows([]string{"id", "status", "region", "residency_tags", "metadata"}).AddRow("eu-a", "disabled", "eu-central-1", `[]`, `{}`))
	mock.ExpectRollback()

	if err := store.UpdateUnit(ctx, unit); !errors.Is(err, ErrInvalidDeploymentUnit) {
		t.Fatalf("UpdateUnit() error = %v, want ErrInvalidDeploymentUnit", err)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreUpdateUnitNotFound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectPostgres)
	unit := types.DeploymentUnit{ID: "eu-a", Status: types.DeploymentUnitStatusActive, Region: "eu-central-1"}

	mock.ExpectBegin()
	get := mock.ExpectQuery(regexp.QuoteMeta("SELECT id, status, region, residency_tags, metadata FROM saas_deployment_units WHERE id = $1 FOR UPDATE"))
	get.WithArgs("eu-a").WillReturnRows(sqlmock.NewRows([]string{"id", "status", "region", "residency_tags", "metadata"}))
	mock.ExpectRollback()

	if err := store.UpdateUnit(ctx, unit); !errors.Is(err, ErrDeploymentUnitNotFound) {
		t.Fatalf("UpdateUnit() error = %v, want ErrDeploymentUnitNotFound", err)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreUpdateUnitBeginError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectPostgres)
	unit := types.DeploymentUnit{ID: "eu-a", Status: types.DeploymentUnitStatusActive, Region: "eu-central-1"}
	err := errors.New("begin failed")
	mock.ExpectBegin().WillReturnError(err)

	if got := store.UpdateUnit(ctx, unit); !errors.Is(got, err) {
		t.Fatalf("UpdateUnit() error = %v, want begin error", got)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreUpdateUnitCommitError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectPostgres)
	unit := types.DeploymentUnit{ID: "eu-a", Status: types.DeploymentUnitStatusActive, Region: "eu-central-1"}

	mock.ExpectBegin()
	get := mock.ExpectQuery(regexp.QuoteMeta("SELECT id, status, region, residency_tags, metadata FROM saas_deployment_units WHERE id = $1 FOR UPDATE"))
	get.WithArgs("eu-a").WillReturnRows(sqlmock.NewRows([]string{"id", "status", "region", "residency_tags", "metadata"}).AddRow("eu-a", "active", "eu-central-1", `[]`, `{}`))
	update := mock.ExpectExec(regexp.QuoteMeta("UPDATE saas_deployment_units SET status = $1, region = $2, residency_tags = $3, metadata = $4 WHERE id = $5"))
	update.WithArgs("active", "eu-central-1", `[]`, `{}`, "eu-a").WillReturnResult(sqlmock.NewResult(0, 1))
	err := errors.New("commit failed")
	mock.ExpectCommit().WillReturnError(err)

	if got := store.UpdateUnit(ctx, unit); !errors.Is(got, err) {
		t.Fatalf("UpdateUnit() error = %v, want commit error", got)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreDisableUnitAlreadyDisabled(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectPostgres)

	mock.ExpectBegin()
	get := mock.ExpectQuery(regexp.QuoteMeta("SELECT id, status, region, residency_tags, metadata FROM saas_deployment_units WHERE id = $1 FOR UPDATE"))
	get.WithArgs("eu-a").WillReturnRows(sqlmock.NewRows([]string{"id", "status", "region", "residency_tags", "metadata"}).AddRow("eu-a", "disabled", "eu-central-1", `[]`, `{}`))
	mock.ExpectCommit()

	unit, changed, err := store.DisableUnit(ctx, "eu-a")
	if err != nil {
		t.Fatalf("DisableUnit() error = %v", err)
	}
	if changed || unit.Status != types.DeploymentUnitStatusDisabled {
		t.Fatalf("DisableUnit() = (%#v, %v), want unchanged disabled", unit, changed)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreDisableUnitSuccess(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectPostgres)

	mock.ExpectBegin()
	get := mock.ExpectQuery(regexp.QuoteMeta("SELECT id, status, region, residency_tags, metadata FROM saas_deployment_units WHERE id = $1 FOR UPDATE"))
	get.WithArgs("eu-a").WillReturnRows(sqlmock.NewRows([]string{"id", "status", "region", "residency_tags", "metadata"}).AddRow("eu-a", "active", "eu-central-1", `[]`, `{}`))
	asg := mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM saas_tenant_deployments WHERE deployment_unit_id = $1 LIMIT 1"))
	asg.WithArgs("eu-a").WillReturnRows(sqlmock.NewRows([]string{"one"}))
	mv := mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM saas_deployment_moves WHERE source_unit_id = $1 OR target_unit_id = $2 LIMIT 1"))
	mv.WithArgs("eu-a", "eu-a").WillReturnRows(sqlmock.NewRows([]string{"one"}))
	upd := mock.ExpectExec(regexp.QuoteMeta("UPDATE saas_deployment_units SET status = $1 WHERE id = $2"))
	upd.WithArgs("disabled", "eu-a").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	unit, changed, err := store.DisableUnit(ctx, "eu-a")
	if err != nil {
		t.Fatalf("DisableUnit() error = %v", err)
	}
	if !changed || unit.Status != types.DeploymentUnitStatusDisabled {
		t.Fatalf("DisableUnit() = (%#v, %v), want changed disabled", unit, changed)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreDisableUnitReferencedByMove(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectPostgres)

	mock.ExpectBegin()
	get := mock.ExpectQuery(regexp.QuoteMeta("SELECT id, status, region, residency_tags, metadata FROM saas_deployment_units WHERE id = $1 FOR UPDATE"))
	get.WithArgs("eu-a").WillReturnRows(sqlmock.NewRows([]string{"id", "status", "region", "residency_tags", "metadata"}).AddRow("eu-a", "active", "eu-central-1", `[]`, `{}`))
	asg := mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM saas_tenant_deployments WHERE deployment_unit_id = $1 LIMIT 1"))
	asg.WithArgs("eu-a").WillReturnRows(sqlmock.NewRows([]string{"one"}))
	mv := mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM saas_deployment_moves WHERE source_unit_id = $1 OR target_unit_id = $2 LIMIT 1"))
	mv.WithArgs("eu-a", "eu-a").WillReturnRows(sqlmock.NewRows([]string{"one"}).AddRow(1))
	mock.ExpectRollback()

	if _, _, err := store.DisableUnit(ctx, "eu-a"); !errors.Is(err, ErrDeploymentUnitInUse) {
		t.Fatalf("DisableUnit() error = %v, want ErrDeploymentUnitInUse", err)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreDisableUnitNotFound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectPostgres)

	mock.ExpectBegin()
	get := mock.ExpectQuery(regexp.QuoteMeta("SELECT id, status, region, residency_tags, metadata FROM saas_deployment_units WHERE id = $1 FOR UPDATE"))
	get.WithArgs("eu-a").WillReturnRows(sqlmock.NewRows([]string{"id", "status", "region", "residency_tags", "metadata"}))
	mock.ExpectRollback()

	if _, _, err := store.DisableUnit(ctx, "eu-a"); !errors.Is(err, ErrDeploymentUnitNotFound) {
		t.Fatalf("DisableUnit() error = %v, want ErrDeploymentUnitNotFound", err)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreDisableUnitExecCommitError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectPostgres)

	mock.ExpectBegin()
	get := mock.ExpectQuery(regexp.QuoteMeta("SELECT id, status, region, residency_tags, metadata FROM saas_deployment_units WHERE id = $1 FOR UPDATE"))
	get.WithArgs("eu-a").WillReturnRows(sqlmock.NewRows([]string{"id", "status", "region", "residency_tags", "metadata"}).AddRow("eu-a", "active", "eu-central-1", `[]`, `{}`))
	asg := mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM saas_tenant_deployments WHERE deployment_unit_id = $1 LIMIT 1"))
	asg.WithArgs("eu-a").WillReturnRows(sqlmock.NewRows([]string{"one"}))
	mv := mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM saas_deployment_moves WHERE source_unit_id = $1 OR target_unit_id = $2 LIMIT 1"))
	mv.WithArgs("eu-a", "eu-a").WillReturnRows(sqlmock.NewRows([]string{"one"}))
	err := errors.New("exec failed")
	upd := mock.ExpectExec(regexp.QuoteMeta("UPDATE saas_deployment_units SET status = $1 WHERE id = $2"))
	upd.WithArgs("disabled", "eu-a").WillReturnError(err)
	mock.ExpectRollback()

	if _, _, got := store.DisableUnit(ctx, "eu-a"); !errors.Is(got, err) {
		t.Fatalf("DisableUnit() error = %v, want exec error", got)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreDeleteUnitSuccess(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectPostgres)

	mock.ExpectBegin()
	get := mock.ExpectQuery(regexp.QuoteMeta("SELECT id, status, region, residency_tags, metadata FROM saas_deployment_units WHERE id = $1 FOR UPDATE"))
	get.WithArgs("eu-a").WillReturnRows(sqlmock.NewRows([]string{"id", "status", "region", "residency_tags", "metadata"}).AddRow("eu-a", "active", "eu-central-1", `[]`, `{}`))
	asg := mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM saas_tenant_deployments WHERE deployment_unit_id = $1 LIMIT 1"))
	asg.WithArgs("eu-a").WillReturnRows(sqlmock.NewRows([]string{"one"}))
	mv := mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM saas_deployment_moves WHERE source_unit_id = $1 OR target_unit_id = $2 LIMIT 1"))
	mv.WithArgs("eu-a", "eu-a").WillReturnRows(sqlmock.NewRows([]string{"one"}))
	del := mock.ExpectExec(regexp.QuoteMeta("DELETE FROM saas_deployment_units WHERE id = $1"))
	del.WithArgs("eu-a").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := store.DeleteUnit(ctx, "eu-a"); err != nil {
		t.Fatalf("DeleteUnit() error = %v", err)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreDeleteUnitReferencedByMove(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectPostgres)

	mock.ExpectBegin()
	get := mock.ExpectQuery(regexp.QuoteMeta("SELECT id, status, region, residency_tags, metadata FROM saas_deployment_units WHERE id = $1 FOR UPDATE"))
	get.WithArgs("eu-a").WillReturnRows(sqlmock.NewRows([]string{"id", "status", "region", "residency_tags", "metadata"}).AddRow("eu-a", "active", "eu-central-1", `[]`, `{}`))
	asg := mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM saas_tenant_deployments WHERE deployment_unit_id = $1 LIMIT 1"))
	asg.WithArgs("eu-a").WillReturnRows(sqlmock.NewRows([]string{"one"}))
	mv := mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM saas_deployment_moves WHERE source_unit_id = $1 OR target_unit_id = $2 LIMIT 1"))
	mv.WithArgs("eu-a", "eu-a").WillReturnRows(sqlmock.NewRows([]string{"one"}).AddRow(1))
	mock.ExpectRollback()

	if err := store.DeleteUnit(ctx, "eu-a"); !errors.Is(err, ErrDeploymentUnitInUse) {
		t.Fatalf("DeleteUnit() error = %v, want ErrDeploymentUnitInUse", err)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreDeleteUnitNotFound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectPostgres)

	mock.ExpectBegin()
	get := mock.ExpectQuery(regexp.QuoteMeta("SELECT id, status, region, residency_tags, metadata FROM saas_deployment_units WHERE id = $1 FOR UPDATE"))
	get.WithArgs("eu-a").WillReturnRows(sqlmock.NewRows([]string{"id", "status", "region", "residency_tags", "metadata"}).AddRow("eu-a", "active", "eu-central-1", `[]`, `{}`))
	asg := mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM saas_tenant_deployments WHERE deployment_unit_id = $1 LIMIT 1"))
	asg.WithArgs("eu-a").WillReturnRows(sqlmock.NewRows([]string{"one"}))
	mv := mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM saas_deployment_moves WHERE source_unit_id = $1 OR target_unit_id = $2 LIMIT 1"))
	mv.WithArgs("eu-a", "eu-a").WillReturnRows(sqlmock.NewRows([]string{"one"}))
	del := mock.ExpectExec(regexp.QuoteMeta("DELETE FROM saas_deployment_units WHERE id = $1"))
	del.WithArgs("eu-a").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()

	if err := store.DeleteUnit(ctx, "eu-a"); !errors.Is(err, ErrDeploymentUnitNotFound) {
		t.Fatalf("DeleteUnit() error = %v, want ErrDeploymentUnitNotFound", err)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreDeleteUnitInvalidID(t *testing.T) {
	t.Parallel()
	store, _ := newMockSQLStore(t, SQLDialectPostgres)
	if err := store.DeleteUnit(context.Background(), ""); !errors.Is(err, ErrInvalidDeploymentUnit) {
		t.Fatalf("DeleteUnit() error = %v, want ErrInvalidDeploymentUnit", err)
	}
}

func TestSQLStoreGetAssignmentNotFound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectPostgres)
	get := mock.ExpectQuery(regexp.QuoteMeta("SELECT tenant_id, deployment_unit_id, version FROM saas_tenant_deployments WHERE tenant_id = $1"))
	get.WithArgs("tenant-a").WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "deployment_unit_id", "version"}))

	if _, err := store.GetAssignment(ctx, "tenant-a"); !errors.Is(err, ErrAssignmentNotFound) {
		t.Fatalf("GetAssignment() error = %v, want ErrAssignmentNotFound", err)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreGetAssignmentInvalidTenant(t *testing.T) {
	t.Parallel()
	store, _ := newMockSQLStore(t, SQLDialectPostgres)
	if _, err := store.GetAssignment(context.Background(), ""); !errors.Is(err, ErrInvalidAssignment) {
		t.Fatalf("GetAssignment() error = %v, want ErrInvalidAssignment", err)
	}
}

func TestSQLStoreGetAssignmentCanceledContext(t *testing.T) {
	t.Parallel()
	store, _ := newMockSQLStore(t, SQLDialectPostgres)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.GetAssignment(ctx, "tenant-a"); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetAssignment() error = %v, want context.Canceled", err)
	}
}

func TestSQLStoreListAssignmentsByUnit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectPostgres)
	rows := sqlmock.NewRows([]string{"tenant_id", "deployment_unit_id", "version"})
	rows.AddRow("tenant-a", "eu-a", uint64(1))
	rows.AddRow("tenant-b", "eu-a", uint64(2))
	list := mock.ExpectQuery(regexp.QuoteMeta("SELECT tenant_id, deployment_unit_id, version FROM saas_tenant_deployments WHERE deployment_unit_id = $1 ORDER BY tenant_id"))
	list.WithArgs("eu-a").WillReturnRows(rows)

	assignments, err := store.ListAssignmentsByUnit(ctx, "eu-a")
	if err != nil {
		t.Fatalf("ListAssignmentsByUnit() error = %v", err)
	}
	if len(assignments) != 2 || assignments[0].TenantID != "tenant-a" || assignments[1].Version != 2 {
		t.Fatalf("ListAssignmentsByUnit() = %#v", assignments)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreListAssignmentsByUnitQueryError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectPostgres)
	err := errors.New("query failed")
	list := mock.ExpectQuery(regexp.QuoteMeta("SELECT tenant_id, deployment_unit_id, version FROM saas_tenant_deployments WHERE deployment_unit_id = $1 ORDER BY tenant_id"))
	list.WithArgs("eu-a").WillReturnError(err)

	if _, got := store.ListAssignmentsByUnit(ctx, "eu-a"); !errors.Is(got, err) {
		t.Fatalf("ListAssignmentsByUnit() error = %v, want sentinel", got)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreCreateAssignmentSuccess(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectPostgres)
	assignment := Assignment{TenantID: "tenant-a", UnitID: "eu-a", Version: 1}

	mock.ExpectBegin()
	unit := mock.ExpectQuery(regexp.QuoteMeta("SELECT id, status, region, residency_tags, metadata FROM saas_deployment_units WHERE id = $1 FOR UPDATE"))
	unit.WithArgs("eu-a").WillReturnRows(sqlmock.NewRows([]string{"id", "status", "region", "residency_tags", "metadata"}).AddRow("eu-a", "active", "eu-central-1", `[]`, `{}`))
	ins := mock.ExpectExec(regexp.QuoteMeta("INSERT INTO saas_tenant_deployments (tenant_id, deployment_unit_id, version) VALUES ($1, $2, $3)"))
	ins.WithArgs("tenant-a", "eu-a", uint64(1)).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	if err := store.CreateAssignment(ctx, assignment); err != nil {
		t.Fatalf("CreateAssignment() error = %v", err)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreCreateAssignmentUnitNotFound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectPostgres)
	assignment := Assignment{TenantID: "tenant-a", UnitID: "eu-a", Version: 1}

	mock.ExpectBegin()
	unit := mock.ExpectQuery(regexp.QuoteMeta("SELECT id, status, region, residency_tags, metadata FROM saas_deployment_units WHERE id = $1 FOR UPDATE"))
	unit.WithArgs("eu-a").WillReturnRows(sqlmock.NewRows([]string{"id", "status", "region", "residency_tags", "metadata"}))
	mock.ExpectRollback()

	if err := store.CreateAssignment(ctx, assignment); !errors.Is(err, ErrDeploymentUnitNotFound) {
		t.Fatalf("CreateAssignment() error = %v, want ErrDeploymentUnitNotFound", err)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreCreateAssignmentDuplicateKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectPostgres)
	assignment := Assignment{TenantID: "tenant-a", UnitID: "eu-a", Version: 1}

	mock.ExpectBegin()
	unit := mock.ExpectQuery(regexp.QuoteMeta("SELECT id, status, region, residency_tags, metadata FROM saas_deployment_units WHERE id = $1 FOR UPDATE"))
	unit.WithArgs("eu-a").WillReturnRows(sqlmock.NewRows([]string{"id", "status", "region", "residency_tags", "metadata"}).AddRow("eu-a", "active", "eu-central-1", `[]`, `{}`))
	ins := mock.ExpectExec(regexp.QuoteMeta("INSERT INTO saas_tenant_deployments (tenant_id, deployment_unit_id, version) VALUES ($1, $2, $3)"))
	ins.WithArgs("tenant-a", "eu-a", uint64(1)).WillReturnError(errors.New("duplicate key value violates unique constraint"))
	mock.ExpectRollback()

	if err := store.CreateAssignment(ctx, assignment); !errors.Is(err, ErrAssignmentAlreadyExists) {
		t.Fatalf("CreateAssignment() error = %v, want ErrAssignmentAlreadyExists", err)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreCompareAndSwapAssignmentSameUnit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectPostgres)
	expected := Assignment{TenantID: "tenant-a", UnitID: "eu-a", Version: 1}
	updated := Assignment{TenantID: "tenant-a", UnitID: "eu-a", Version: 2}

	mock.ExpectBegin()
	get := mock.ExpectQuery(regexp.QuoteMeta("SELECT tenant_id, deployment_unit_id, version FROM saas_tenant_deployments WHERE tenant_id = $1 FOR UPDATE"))
	get.WithArgs("tenant-a").WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "deployment_unit_id", "version"}).AddRow("tenant-a", "eu-a", uint64(1)))
	upd := mock.ExpectExec(regexp.QuoteMeta("UPDATE saas_tenant_deployments SET deployment_unit_id = $1, version = $2 WHERE tenant_id = $3"))
	upd.WithArgs("eu-a", uint64(2), "tenant-a").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := store.CompareAndSwapAssignment(ctx, expected, updated); err != nil {
		t.Fatalf("CompareAndSwapAssignment() error = %v", err)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreCompareAndSwapAssignmentUnavailable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectPostgres)
	expected := Assignment{TenantID: "tenant-a", UnitID: "cn-a", Version: 1}
	updated := Assignment{TenantID: "tenant-a", UnitID: "eu-a", Version: 2}

	mock.ExpectBegin()
	unit := mock.ExpectQuery(regexp.QuoteMeta("SELECT id, status, region, residency_tags, metadata FROM saas_deployment_units WHERE id = $1 FOR UPDATE"))
	unit.WithArgs("eu-a").WillReturnRows(sqlmock.NewRows([]string{"id", "status", "region", "residency_tags", "metadata"}).AddRow("eu-a", "disabled", "eu-central-1", `[]`, `{}`))
	mock.ExpectRollback()

	if err := store.CompareAndSwapAssignment(ctx, expected, updated); !errors.Is(err, ErrDeploymentUnitUnavailable) {
		t.Fatalf("CompareAndSwapAssignment() error = %v, want ErrDeploymentUnitUnavailable", err)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreCompareAndSwapAssignmentTenantMismatch(t *testing.T) {
	t.Parallel()
	store, _ := newMockSQLStore(t, SQLDialectPostgres)
	expected := Assignment{TenantID: "tenant-a", UnitID: "eu-a", Version: 1}
	updated := Assignment{TenantID: "tenant-b", UnitID: "eu-a", Version: 2}
	if err := store.CompareAndSwapAssignment(context.Background(), expected, updated); !errors.Is(err, ErrInvalidAssignment) {
		t.Fatalf("CompareAndSwapAssignment() error = %v, want ErrInvalidAssignment", err)
	}
}

func TestSQLStoreCompareAndSwapAssignmentEqualAssignment(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectPostgres)
	expected := Assignment{TenantID: "tenant-a", UnitID: "eu-a", Version: 1}
	updated := Assignment{TenantID: "tenant-a", UnitID: "eu-a", Version: 1}

	mock.ExpectBegin()
	get := mock.ExpectQuery(regexp.QuoteMeta("SELECT tenant_id, deployment_unit_id, version FROM saas_tenant_deployments WHERE tenant_id = $1 FOR UPDATE"))
	get.WithArgs("tenant-a").WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "deployment_unit_id", "version"}).AddRow("tenant-a", "eu-a", uint64(1)))
	mock.ExpectCommit()

	if err := store.CompareAndSwapAssignment(ctx, expected, updated); err != nil {
		t.Fatalf("CompareAndSwapAssignment() error = %v", err)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreCutoverMoveUnitUnavailable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectPostgres)
	expected := Assignment{TenantID: "tenant-a", UnitID: "cn-a", Version: 1}
	move := Move{TenantID: "tenant-a", SourceUnitID: "cn-a", TargetUnitID: "eu-a"}
	updated := Assignment{TenantID: "tenant-a", UnitID: "eu-a", Version: 2}

	mock.ExpectBegin()
	unit := mock.ExpectQuery(regexp.QuoteMeta("SELECT id, status, region, residency_tags, metadata FROM saas_deployment_units WHERE id = $1 FOR UPDATE"))
	unit.WithArgs("eu-a").WillReturnRows(sqlmock.NewRows([]string{"id", "status", "region", "residency_tags", "metadata"}).AddRow("eu-a", "disabled", "eu-central-1", `[]`, `{}`))
	mock.ExpectRollback()

	if err := store.CutoverMove(ctx, expected, move, updated); !errors.Is(err, ErrDeploymentUnitUnavailable) {
		t.Fatalf("CutoverMove() error = %v, want ErrDeploymentUnitUnavailable", err)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreCutoverMoveConflict(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectPostgres)
	expected := Assignment{TenantID: "tenant-a", UnitID: "cn-a", Version: 1}
	move := Move{TenantID: "tenant-a", SourceUnitID: "cn-a", TargetUnitID: "eu-a"}
	// Version not incremented by one => move conflict before the transaction.
	badUpdated := Assignment{TenantID: "tenant-a", UnitID: "eu-a", Version: 3}
	if err := store.CutoverMove(ctx, expected, move, badUpdated); !errors.Is(err, ErrMoveConflict) {
		t.Fatalf("CutoverMove() error = %v, want ErrMoveConflict", err)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreCutoverMoveAssignmentConflict(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectPostgres)
	expected := Assignment{TenantID: "tenant-a", UnitID: "cn-a", Version: 1}
	move := Move{TenantID: "tenant-a", SourceUnitID: "cn-a", TargetUnitID: "eu-a"}
	updated := Assignment{TenantID: "tenant-a", UnitID: "eu-a", Version: 2}

	mock.ExpectBegin()
	unit := mock.ExpectQuery(regexp.QuoteMeta("SELECT id, status, region, residency_tags, metadata FROM saas_deployment_units WHERE id = $1 FOR UPDATE"))
	unit.WithArgs("eu-a").WillReturnRows(sqlmock.NewRows([]string{"id", "status", "region", "residency_tags", "metadata"}).AddRow("eu-a", "active", "eu-central-1", `[]`, `{}`))
	asg := mock.ExpectQuery(regexp.QuoteMeta("SELECT tenant_id, deployment_unit_id, version FROM saas_tenant_deployments WHERE tenant_id = $1 FOR UPDATE"))
	asg.WithArgs("tenant-a").WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "deployment_unit_id", "version"}).AddRow("tenant-a", "cn-a", uint64(3)))
	mock.ExpectRollback()

	if err := store.CutoverMove(ctx, expected, move, updated); !errors.Is(err, ErrAssignmentConflict) {
		t.Fatalf("CutoverMove() error = %v, want ErrAssignmentConflict", err)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreCutoverMoveMoveConflict(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectPostgres)
	expected := Assignment{TenantID: "tenant-a", UnitID: "cn-a", Version: 1}
	move := Move{TenantID: "tenant-a", SourceUnitID: "cn-a", TargetUnitID: "eu-a"}
	updated := Assignment{TenantID: "tenant-a", UnitID: "eu-a", Version: 2}

	mock.ExpectBegin()
	unit := mock.ExpectQuery(regexp.QuoteMeta("SELECT id, status, region, residency_tags, metadata FROM saas_deployment_units WHERE id = $1 FOR UPDATE"))
	unit.WithArgs("eu-a").WillReturnRows(sqlmock.NewRows([]string{"id", "status", "region", "residency_tags", "metadata"}).AddRow("eu-a", "active", "eu-central-1", `[]`, `{}`))
	asg := mock.ExpectQuery(regexp.QuoteMeta("SELECT tenant_id, deployment_unit_id, version FROM saas_tenant_deployments WHERE tenant_id = $1 FOR UPDATE"))
	asg.WithArgs("tenant-a").WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "deployment_unit_id", "version"}).AddRow("tenant-a", "cn-a", uint64(1)))
	mv := mock.ExpectQuery(regexp.QuoteMeta("SELECT tenant_id, source_unit_id, target_unit_id FROM saas_deployment_moves WHERE tenant_id = $1 FOR UPDATE"))
	mv.WithArgs("tenant-a").WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "source_unit_id", "target_unit_id"}).AddRow("tenant-a", "cn-a", "us-a"))
	mock.ExpectRollback()

	if err := store.CutoverMove(ctx, expected, move, updated); !errors.Is(err, ErrMoveConflict) {
		t.Fatalf("CutoverMove() error = %v, want ErrMoveConflict", err)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreGetMoveNotFound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectPostgres)
	get := mock.ExpectQuery(regexp.QuoteMeta("SELECT tenant_id, source_unit_id, target_unit_id FROM saas_deployment_moves WHERE tenant_id = $1"))
	get.WithArgs("tenant-a").WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "source_unit_id", "target_unit_id"}))

	if _, err := store.GetMove(ctx, "tenant-a"); !errors.Is(err, ErrMoveNotFound) {
		t.Fatalf("GetMove() error = %v, want ErrMoveNotFound", err)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreGetMoveInvalidTenant(t *testing.T) {
	t.Parallel()
	store, _ := newMockSQLStore(t, SQLDialectPostgres)
	if _, err := store.GetMove(context.Background(), ""); !errors.Is(err, ErrInvalidMove) {
		t.Fatalf("GetMove() error = %v, want ErrInvalidMove", err)
	}
}

func TestSQLStoreDeleteMoveSuccess(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectPostgres)
	del := mock.ExpectExec(regexp.QuoteMeta("DELETE FROM saas_deployment_moves WHERE tenant_id = $1"))
	del.WithArgs("tenant-a").WillReturnResult(sqlmock.NewResult(0, 1))

	if err := store.DeleteMove(ctx, "tenant-a"); err != nil {
		t.Fatalf("DeleteMove() error = %v", err)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreDeleteMoveNotFound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectPostgres)
	del := mock.ExpectExec(regexp.QuoteMeta("DELETE FROM saas_deployment_moves WHERE tenant_id = $1"))
	del.WithArgs("tenant-a").WillReturnResult(sqlmock.NewResult(0, 0))

	if err := store.DeleteMove(ctx, "tenant-a"); !errors.Is(err, ErrMoveNotFound) {
		t.Fatalf("DeleteMove() error = %v, want ErrMoveNotFound", err)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreDeleteMoveExecError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectPostgres)
	err := errors.New("exec failed")
	del := mock.ExpectExec(regexp.QuoteMeta("DELETE FROM saas_deployment_moves WHERE tenant_id = $1"))
	del.WithArgs("tenant-a").WillReturnError(err)

	if got := store.DeleteMove(ctx, "tenant-a"); !errors.Is(got, err) {
		t.Fatalf("DeleteMove() error = %v, want sentinel", got)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreCreateMoveSuccess(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectPostgres)
	move := Move{TenantID: "tenant-a", SourceUnitID: "cn-a", TargetUnitID: "eu-a"}

	mock.ExpectBegin()
	unit := mock.ExpectQuery(regexp.QuoteMeta("SELECT id, status, region, residency_tags, metadata FROM saas_deployment_units WHERE id = $1 FOR UPDATE"))
	unit.WithArgs("eu-a").WillReturnRows(sqlmock.NewRows([]string{"id", "status", "region", "residency_tags", "metadata"}).AddRow("eu-a", "active", "eu-central-1", `[]`, `{}`))
	asg := mock.ExpectQuery(regexp.QuoteMeta("SELECT tenant_id, deployment_unit_id, version FROM saas_tenant_deployments WHERE tenant_id = $1 FOR UPDATE"))
	asg.WithArgs("tenant-a").WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "deployment_unit_id", "version"}).AddRow("tenant-a", "cn-a", uint64(1)))
	ins := mock.ExpectExec(regexp.QuoteMeta("INSERT INTO saas_deployment_moves (tenant_id, source_unit_id, target_unit_id) VALUES ($1, $2, $3)"))
	ins.WithArgs("tenant-a", "cn-a", "eu-a").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	if err := store.CreateMove(ctx, move); err != nil {
		t.Fatalf("CreateMove() error = %v", err)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreCreateMoveSourceConflict(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectPostgres)
	move := Move{TenantID: "tenant-a", SourceUnitID: "cn-a", TargetUnitID: "eu-a"}

	mock.ExpectBegin()
	unit := mock.ExpectQuery(regexp.QuoteMeta("SELECT id, status, region, residency_tags, metadata FROM saas_deployment_units WHERE id = $1 FOR UPDATE"))
	unit.WithArgs("eu-a").WillReturnRows(sqlmock.NewRows([]string{"id", "status", "region", "residency_tags", "metadata"}).AddRow("eu-a", "active", "eu-central-1", `[]`, `{}`))
	asg := mock.ExpectQuery(regexp.QuoteMeta("SELECT tenant_id, deployment_unit_id, version FROM saas_tenant_deployments WHERE tenant_id = $1 FOR UPDATE"))
	asg.WithArgs("tenant-a").WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "deployment_unit_id", "version"}).AddRow("tenant-a", "us-a", uint64(1)))
	mock.ExpectRollback()

	if err := store.CreateMove(ctx, move); !errors.Is(err, ErrMoveConflict) {
		t.Fatalf("CreateMove() error = %v, want ErrMoveConflict", err)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreCreateMoveDuplicateKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectPostgres)
	move := Move{TenantID: "tenant-a", SourceUnitID: "cn-a", TargetUnitID: "eu-a"}

	mock.ExpectBegin()
	unit := mock.ExpectQuery(regexp.QuoteMeta("SELECT id, status, region, residency_tags, metadata FROM saas_deployment_units WHERE id = $1 FOR UPDATE"))
	unit.WithArgs("eu-a").WillReturnRows(sqlmock.NewRows([]string{"id", "status", "region", "residency_tags", "metadata"}).AddRow("eu-a", "active", "eu-central-1", `[]`, `{}`))
	asg := mock.ExpectQuery(regexp.QuoteMeta("SELECT tenant_id, deployment_unit_id, version FROM saas_tenant_deployments WHERE tenant_id = $1 FOR UPDATE"))
	asg.WithArgs("tenant-a").WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "deployment_unit_id", "version"}).AddRow("tenant-a", "cn-a", uint64(1)))
	ins := mock.ExpectExec(regexp.QuoteMeta("INSERT INTO saas_deployment_moves (tenant_id, source_unit_id, target_unit_id) VALUES ($1, $2, $3)"))
	ins.WithArgs("tenant-a", "cn-a", "eu-a").WillReturnError(errors.New("duplicate key value violates unique constraint"))
	mock.ExpectRollback()

	if err := store.CreateMove(ctx, move); !errors.Is(err, ErrMoveAlreadyExists) {
		t.Fatalf("CreateMove() error = %v, want ErrMoveAlreadyExists", err)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreDeleteMoveIfMatchSuccess(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectPostgres)
	move := Move{TenantID: "tenant-a", SourceUnitID: "cn-a", TargetUnitID: "eu-a"}

	mock.ExpectBegin()
	mv := mock.ExpectQuery(regexp.QuoteMeta("SELECT tenant_id, source_unit_id, target_unit_id FROM saas_deployment_moves WHERE tenant_id = $1 FOR UPDATE"))
	mv.WithArgs("tenant-a").WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "source_unit_id", "target_unit_id"}).AddRow("tenant-a", "cn-a", "eu-a"))
	del := mock.ExpectExec(regexp.QuoteMeta("DELETE FROM saas_deployment_moves WHERE tenant_id = $1 AND source_unit_id = $2 AND target_unit_id = $3"))
	del.WithArgs("tenant-a", "cn-a", "eu-a").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := store.DeleteMoveIfMatch(ctx, move); err != nil {
		t.Fatalf("DeleteMoveIfMatch() error = %v", err)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreDeleteMoveIfMatchConflict(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectPostgres)
	move := Move{TenantID: "tenant-a", SourceUnitID: "cn-a", TargetUnitID: "eu-a"}

	mock.ExpectBegin()
	mv := mock.ExpectQuery(regexp.QuoteMeta("SELECT tenant_id, source_unit_id, target_unit_id FROM saas_deployment_moves WHERE tenant_id = $1 FOR UPDATE"))
	mv.WithArgs("tenant-a").WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "source_unit_id", "target_unit_id"}).AddRow("tenant-a", "cn-a", "us-a"))
	mock.ExpectRollback()

	if err := store.DeleteMoveIfMatch(ctx, move); !errors.Is(err, ErrMoveConflict) {
		t.Fatalf("DeleteMoveIfMatch() error = %v, want ErrMoveConflict", err)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreDeleteMoveIfMatchNotFound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectPostgres)
	move := Move{TenantID: "tenant-a", SourceUnitID: "cn-a", TargetUnitID: "eu-a"}

	mock.ExpectBegin()
	mv := mock.ExpectQuery(regexp.QuoteMeta("SELECT tenant_id, source_unit_id, target_unit_id FROM saas_deployment_moves WHERE tenant_id = $1 FOR UPDATE"))
	mv.WithArgs("tenant-a").WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "source_unit_id", "target_unit_id"}).AddRow("tenant-a", "cn-a", "eu-a"))
	del := mock.ExpectExec(regexp.QuoteMeta("DELETE FROM saas_deployment_moves WHERE tenant_id = $1 AND source_unit_id = $2 AND target_unit_id = $3"))
	del.WithArgs("tenant-a", "cn-a", "eu-a").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()

	if err := store.DeleteMoveIfMatch(ctx, move); !errors.Is(err, ErrMoveNotFound) {
		t.Fatalf("DeleteMoveIfMatch() error = %v, want ErrMoveNotFound", err)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreWithTableNameOptions(t *testing.T) {
	t.Parallel()
	db := &sql.DB{}
	if _, err := NewSQLStore(db, WithAssignmentTableName("asg;drop")); !errors.Is(err, ErrInvalidTableName) {
		t.Fatalf("WithAssignmentTableName(unsafe) error = %v, want ErrInvalidTableName", err)
	}
	if _, err := NewSQLStore(db, WithMoveTableName("mv;drop")); !errors.Is(err, ErrInvalidTableName) {
		t.Fatalf("WithMoveTableName(unsafe) error = %v, want ErrInvalidTableName", err)
	}
	store, err := NewSQLStore(db,
		WithUnitTableName("custom_units"),
		WithAssignmentTableName("custom_asg"),
		WithMoveTableName("custom_mv"),
		WithSQLDialect(SQLDialectSQLite),
		nil, // a nil option is ignored
	)
	if err != nil {
		t.Fatalf("NewSQLStore(opts) error = %v", err)
	}
	if store.unitTable != "custom_units" || store.assignmentTable != "custom_asg" || store.moveTable != "custom_mv" || store.dialect != SQLDialectSQLite {
		t.Fatalf("NewSQLStore() = %#v, want overridden tables/dialect", store)
	}
}

func TestSQLStoreSQLiteDialectOmitsForUpdate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectSQLite)
	assignment := Assignment{TenantID: "tenant-a", UnitID: "eu-a", Version: 1}

	mock.ExpectBegin()
	unit := mock.ExpectQuery(regexp.QuoteMeta("SELECT id, status, region, residency_tags, metadata FROM saas_deployment_units WHERE id = ?"))
	unit.WithArgs("eu-a").WillReturnRows(sqlmock.NewRows([]string{"id", "status", "region", "residency_tags", "metadata"}).AddRow("eu-a", "active", "eu-central-1", `[]`, `{}`))
	ins := mock.ExpectExec(regexp.QuoteMeta("INSERT INTO saas_tenant_deployments (tenant_id, deployment_unit_id, version) VALUES (?, ?, ?)"))
	ins.WithArgs("tenant-a", "eu-a", uint64(1)).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	if err := store.CreateAssignment(ctx, assignment); err != nil {
		t.Fatalf("CreateAssignment(sqlite) error = %v", err)
	}
	assertSQLMockExpectations(t, mock)
}

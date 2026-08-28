package deployment

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/im10furry/saas/core/types"
	"github.com/im10furry/saas/internal/sqlutil"
)

const (
	// DefaultSQLUnitTableName is the default deployment-unit directory table.
	DefaultSQLUnitTableName = "saas_deployment_units"

	// DefaultSQLAssignmentTableName is the default current tenant-placement table.
	DefaultSQLAssignmentTableName = "saas_tenant_deployments"

	// DefaultSQLMoveTableName is the default prepared tenant-move table.
	DefaultSQLMoveTableName = "saas_deployment_moves"
)

// SQLDialect controls SQL placeholder rendering for SQLStore.
type SQLDialect = sqlutil.Dialect

const (
	// SQLDialectMySQL uses question-mark placeholders and is the default.
	SQLDialectMySQL = sqlutil.DialectMySQL

	// SQLDialectSQLite uses question-mark placeholders.
	SQLDialectSQLite = sqlutil.DialectSQLite

	// SQLDialectPostgres uses numbered placeholders.
	SQLDialectPostgres = sqlutil.DialectPostgres
)

var _ Store = (*SQLStore)(nil)

// SQLStore persists deployment units, tenant assignments, and prepared moves
// through database/sql. The host owns the schema and migrations.
//
// The unit table requires: id, status, region, residency_tags, metadata.
// The assignment table requires: tenant_id, deployment_unit_id, version.
// The move table requires: tenant_id, source_unit_id, target_unit_id.
type SQLStore struct {
	db              *sql.DB
	unitTable       string
	assignmentTable string
	moveTable       string
	dialect         SQLDialect
}

// SQLStoreOption configures SQLStore.
type SQLStoreOption func(*SQLStore) error

// WithUnitTableName overrides the deployment-unit directory table name.
func WithUnitTableName(table string) SQLStoreOption {
	return func(store *SQLStore) error {
		if !sqlutil.IsSafeQualifiedIdentifier(table) {
			return fmt.Errorf("%w: %q", ErrInvalidTableName, table)
		}
		store.unitTable = table
		return nil
	}
}

// WithAssignmentTableName overrides the tenant-placement table name.
func WithAssignmentTableName(table string) SQLStoreOption {
	return func(store *SQLStore) error {
		if !sqlutil.IsSafeQualifiedIdentifier(table) {
			return fmt.Errorf("%w: %q", ErrInvalidTableName, table)
		}
		store.assignmentTable = table
		return nil
	}
}

// WithMoveTableName overrides the prepared tenant-move table name.
func WithMoveTableName(table string) SQLStoreOption {
	return func(store *SQLStore) error {
		if !sqlutil.IsSafeQualifiedIdentifier(table) {
			return fmt.Errorf("%w: %q", ErrInvalidTableName, table)
		}
		store.moveTable = table
		return nil
	}
}

// WithSQLDialect configures SQL placeholder rendering.
func WithSQLDialect(dialect SQLDialect) SQLStoreOption {
	return func(store *SQLStore) error {
		normalized, ok := sqlutil.NormalizeDialect(dialect)
		if !ok {
			return fmt.Errorf("%w: %s", ErrUnsupportedSQLDialect, dialect)
		}
		store.dialect = normalized
		return nil
	}
}

// NewSQLStore creates a SQL-backed deployment store.
func NewSQLStore(db *sql.DB, opts ...SQLStoreOption) (*SQLStore, error) {
	if db == nil {
		return nil, ErrNilDB
	}

	store := &SQLStore{
		db:              db,
		unitTable:       DefaultSQLUnitTableName,
		assignmentTable: DefaultSQLAssignmentTableName,
		moveTable:       DefaultSQLMoveTableName,
		dialect:         SQLDialectMySQL,
	}
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		if err := opt(store); err != nil {
			return nil, err
		}
	}
	return store, nil
}

// GetUnit returns one deployment unit.
func (store *SQLStore) GetUnit(ctx context.Context, id types.DeploymentUnitID) (types.DeploymentUnit, error) {
	if err := ctx.Err(); err != nil {
		return types.DeploymentUnit{}, err
	}
	if id == "" {
		return types.DeploymentUnit{}, ErrInvalidDeploymentUnit
	}

	query := fmt.Sprintf("SELECT id, status, region, residency_tags, metadata FROM %s WHERE id = %s", store.unitTable, store.placeholder(1))
	unit, err := scanUnit(store.db.QueryRowContext(ctx, query, id.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return types.DeploymentUnit{}, ErrDeploymentUnitNotFound
	}
	if err != nil {
		return types.DeploymentUnit{}, err
	}
	return unit, nil
}

// ListUnits returns all deployment units in ID order.
func (store *SQLStore) ListUnits(ctx context.Context) (units []types.DeploymentUnit, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	query := fmt.Sprintf("SELECT id, status, region, residency_tags, metadata FROM %s ORDER BY id", store.unitTable)
	rows, err := store.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	units = []types.DeploymentUnit{}
	for rows.Next() {
		unit, err := scanUnit(rows)
		if err != nil {
			return nil, err
		}
		units = append(units, unit)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return units, nil
}

// CreateUnit inserts a deployment unit.
func (store *SQLStore) CreateUnit(ctx context.Context, unit types.DeploymentUnit) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateUnit(unit); err != nil {
		return err
	}

	residencyTags, metadata, err := marshalUnitParts(unit)
	if err != nil {
		return err
	}
	query := fmt.Sprintf("INSERT INTO %s (id, status, region, residency_tags, metadata) VALUES (%s)", store.unitTable, store.placeholders(5, 1))
	_, err = store.db.ExecContext(ctx, query, unit.ID.String(), string(unit.Status), unit.Region, residencyTags, metadata)
	return sqlutil.NormalizeDuplicateKeyError(err, ErrDeploymentUnitAlreadyExists)
}

// UpdateUnit replaces mutable deployment-unit metadata. Status transitions are
// rejected here and must use DisableUnit, preventing a stale metadata write
// from re-enabling a unit changed by another service instance.
func (store *SQLStore) UpdateUnit(ctx context.Context, unit types.DeploymentUnit) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateUnit(unit); err != nil {
		return err
	}

	residencyTags, metadata, err := marshalUnitParts(unit)
	if err != nil {
		return err
	}
	tx, err := store.beginPlacementTx(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, tx.Rollback())
		}
	}()

	current, err := store.getUnitForUpdate(ctx, tx, unit.ID)
	if err != nil {
		return err
	}
	if current.Status != unit.Status {
		return ErrInvalidDeploymentUnit
	}
	query := fmt.Sprintf(
		"UPDATE %s SET status = %s, region = %s, residency_tags = %s, metadata = %s WHERE id = %s",
		store.unitTable,
		store.placeholder(1),
		store.placeholder(2),
		store.placeholder(3),
		store.placeholder(4),
		store.placeholder(5),
	)
	result, err := tx.ExecContext(ctx, query, string(unit.Status), unit.Region, residencyTags, metadata, unit.ID.String())
	if err != nil {
		return err
	}
	if _, err := result.RowsAffected(); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return nil
}

// DisableUnit makes an unreferenced deployment unit unavailable. It locks the
// unit before checking references, so concurrent placement writes cannot bind
// a tenant after the in-use check and before the status transition.
func (store *SQLStore) DisableUnit(ctx context.Context, id types.DeploymentUnitID) (unit types.DeploymentUnit, changed bool, err error) {
	if err := ctx.Err(); err != nil {
		return types.DeploymentUnit{}, false, err
	}
	if id == "" {
		return types.DeploymentUnit{}, false, ErrInvalidDeploymentUnit
	}
	tx, err := store.beginPlacementTx(ctx)
	if err != nil {
		return types.DeploymentUnit{}, false, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, tx.Rollback())
		}
	}()
	unit, err = store.getUnitForUpdate(ctx, tx, id)
	if err != nil {
		return types.DeploymentUnit{}, false, err
	}
	if unit.Status == types.DeploymentUnitStatusDisabled {
		if err = tx.Commit(); err != nil {
			return types.DeploymentUnit{}, false, err
		}
		return unit, false, nil
	}
	referenced, err := store.unitReferencedInTx(ctx, tx, id)
	if err != nil {
		return types.DeploymentUnit{}, false, err
	}
	if referenced {
		return types.DeploymentUnit{}, false, ErrDeploymentUnitInUse
	}
	query := fmt.Sprintf("UPDATE %s SET status = %s WHERE id = %s", store.unitTable, store.placeholder(1), store.placeholder(2))
	result, err := tx.ExecContext(ctx, query, string(types.DeploymentUnitStatusDisabled), id.String())
	if err != nil {
		return types.DeploymentUnit{}, false, err
	}
	if err = requireAffectedRow(result, ErrDeploymentUnitNotFound); err != nil {
		return types.DeploymentUnit{}, false, err
	}
	unit.Status = types.DeploymentUnitStatusDisabled
	if err = tx.Commit(); err != nil {
		return types.DeploymentUnit{}, false, err
	}
	return unit, true, nil
}

// DeleteUnit removes an unreferenced deployment unit. The row lock used here
// is also acquired by assignment and move creation for the referenced unit.
func (store *SQLStore) DeleteUnit(ctx context.Context, id types.DeploymentUnitID) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if id == "" {
		return ErrInvalidDeploymentUnit
	}
	tx, err := store.beginPlacementTx(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, tx.Rollback())
		}
	}()
	if _, err = store.getUnitForUpdate(ctx, tx, id); err != nil {
		return err
	}
	referenced, err := store.unitReferencedInTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if referenced {
		return ErrDeploymentUnitInUse
	}

	query := fmt.Sprintf("DELETE FROM %s WHERE id = %s", store.unitTable, store.placeholder(1))
	result, err := tx.ExecContext(ctx, query, id.String())
	if err != nil {
		return err
	}
	if err = requireAffectedRow(result, ErrDeploymentUnitNotFound); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return nil
}

// GetAssignment returns the current assignment for a tenant.
func (store *SQLStore) GetAssignment(ctx context.Context, tenantID types.TenantID) (Assignment, error) {
	if err := ctx.Err(); err != nil {
		return Assignment{}, err
	}
	if tenantID == "" {
		return Assignment{}, ErrInvalidAssignment
	}

	query := fmt.Sprintf("SELECT tenant_id, deployment_unit_id, version FROM %s WHERE tenant_id = %s", store.assignmentTable, store.placeholder(1))
	assignment, err := scanAssignment(store.db.QueryRowContext(ctx, query, tenantID.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return Assignment{}, ErrAssignmentNotFound
	}
	if err != nil {
		return Assignment{}, err
	}
	return assignment, nil
}

// ListAssignmentsByUnit returns assignments in tenant ID order.
func (store *SQLStore) ListAssignmentsByUnit(ctx context.Context, unitID types.DeploymentUnitID) (assignments []Assignment, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if unitID == "" {
		return nil, ErrInvalidDeploymentUnit
	}

	query := fmt.Sprintf("SELECT tenant_id, deployment_unit_id, version FROM %s WHERE deployment_unit_id = %s ORDER BY tenant_id", store.assignmentTable, store.placeholder(1))
	rows, err := store.db.QueryContext(ctx, query, unitID.String())
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	assignments = []Assignment{}
	for rows.Next() {
		assignment, err := scanAssignment(rows)
		if err != nil {
			return nil, err
		}
		assignments = append(assignments, assignment)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return assignments, nil
}

// CreateAssignment creates a tenant's initial deployment assignment. It locks
// the target unit and verifies that the unit is active before inserting, which
// serializes the operation with disable and delete transitions.
func (store *SQLStore) CreateAssignment(ctx context.Context, assignment Assignment) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateAssignment(assignment); err != nil {
		return err
	}
	tx, err := store.beginPlacementTx(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, tx.Rollback())
		}
	}()
	unit, err := store.getUnitForUpdate(ctx, tx, assignment.UnitID)
	if err != nil {
		return err
	}
	if unit.Status != types.DeploymentUnitStatusActive {
		return ErrDeploymentUnitUnavailable
	}

	query := fmt.Sprintf("INSERT INTO %s (tenant_id, deployment_unit_id, version) VALUES (%s)", store.assignmentTable, store.placeholders(3, 1))
	_, err = tx.ExecContext(ctx, query, assignment.TenantID.String(), assignment.UnitID.String(), assignment.Version)
	if err != nil {
		return sqlutil.NormalizeDuplicateKeyError(err, ErrAssignmentAlreadyExists)
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return nil
}

// CompareAndSwapAssignment replaces an assignment when its source unit and
// version still equal expected. Service callers should use CutoverMove for a
// placement change so the prepared move is removed in the same transaction.
func (store *SQLStore) CompareAndSwapAssignment(ctx context.Context, expected Assignment, updated Assignment) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateAssignment(expected); err != nil {
		return err
	}
	if err := validateAssignment(updated); err != nil {
		return err
	}
	if expected.TenantID != updated.TenantID {
		return ErrInvalidAssignment
	}
	tx, err := store.beginPlacementTx(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, tx.Rollback())
		}
	}()
	if updated.UnitID != expected.UnitID {
		unit, err := store.getUnitForUpdate(ctx, tx, updated.UnitID)
		if err != nil {
			return err
		}
		if unit.Status != types.DeploymentUnitStatusActive {
			return ErrDeploymentUnitUnavailable
		}
	}
	current, err := store.getAssignmentForUpdate(ctx, tx, expected.TenantID)
	if err != nil {
		return err
	}
	if current != expected {
		return ErrAssignmentConflict
	}
	if current == updated {
		if err = tx.Commit(); err != nil {
			return err
		}
		return nil
	}

	query := fmt.Sprintf(
		"UPDATE %s SET deployment_unit_id = %s, version = %s WHERE tenant_id = %s",
		store.assignmentTable,
		store.placeholder(1),
		store.placeholder(2),
		store.placeholder(3),
	)
	result, err := tx.ExecContext(ctx, query, updated.UnitID.String(), updated.Version, expected.TenantID.String())
	if err != nil {
		return err
	}
	if err = requireAffectedRow(result, ErrAssignmentNotFound); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return nil
}

// CutoverMove atomically updates an assignment and removes the exact prepared
// move. The unit, assignment, and move are locked inside one transaction so a
// concurrent cancellation or lifecycle transition cannot leave a split state.
func (store *SQLStore) CutoverMove(ctx context.Context, expected Assignment, move Move, updated Assignment) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateAssignment(expected); err != nil {
		return err
	}
	if err := validateMove(move); err != nil {
		return err
	}
	if err := validateAssignment(updated); err != nil {
		return err
	}
	if expected.TenantID != updated.TenantID || move.TenantID != expected.TenantID || expected.UnitID != move.SourceUnitID || updated.UnitID != move.TargetUnitID || updated.Version != expected.Version+1 {
		return ErrMoveConflict
	}

	tx, err := store.beginPlacementTx(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, tx.Rollback())
		}
	}()
	unit, err := store.getUnitForUpdate(ctx, tx, updated.UnitID)
	if err != nil {
		return err
	}
	if unit.Status != types.DeploymentUnitStatusActive {
		return ErrDeploymentUnitUnavailable
	}
	current, err := store.getAssignmentForUpdate(ctx, tx, expected.TenantID)
	if err != nil {
		return err
	}
	if current != expected {
		return ErrAssignmentConflict
	}
	currentMove, err := store.getMoveForUpdate(ctx, tx, move.TenantID)
	if err != nil {
		return err
	}
	if currentMove != move {
		return ErrMoveConflict
	}

	updateQuery := fmt.Sprintf(
		"UPDATE %s SET deployment_unit_id = %s, version = %s WHERE tenant_id = %s",
		store.assignmentTable,
		store.placeholder(1),
		store.placeholder(2),
		store.placeholder(3),
	)
	result, err := tx.ExecContext(ctx, updateQuery, updated.UnitID.String(), updated.Version, updated.TenantID.String())
	if err != nil {
		return err
	}
	if err = requireAffectedRow(result, ErrAssignmentNotFound); err != nil {
		return err
	}
	deleteQuery := fmt.Sprintf(
		"DELETE FROM %s WHERE tenant_id = %s AND source_unit_id = %s AND target_unit_id = %s",
		store.moveTable,
		store.placeholder(1),
		store.placeholder(2),
		store.placeholder(3),
	)
	result, err = tx.ExecContext(ctx, deleteQuery, move.TenantID.String(), move.SourceUnitID.String(), move.TargetUnitID.String())
	if err != nil {
		return err
	}
	if err = requireAffectedRow(result, ErrMoveNotFound); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return nil
}

// GetMove returns a tenant's prepared deployment move.
func (store *SQLStore) GetMove(ctx context.Context, tenantID types.TenantID) (Move, error) {
	if err := ctx.Err(); err != nil {
		return Move{}, err
	}
	if tenantID == "" {
		return Move{}, ErrInvalidMove
	}

	query := fmt.Sprintf("SELECT tenant_id, source_unit_id, target_unit_id FROM %s WHERE tenant_id = %s", store.moveTable, store.placeholder(1))
	move, err := scanMove(store.db.QueryRowContext(ctx, query, tenantID.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return Move{}, ErrMoveNotFound
	}
	if err != nil {
		return Move{}, err
	}
	return move, nil
}

// ListMovesByUnit returns moves that use a unit as source or target in tenant
// ID order.
func (store *SQLStore) ListMovesByUnit(ctx context.Context, unitID types.DeploymentUnitID) (moves []Move, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if unitID == "" {
		return nil, ErrInvalidDeploymentUnit
	}

	query := fmt.Sprintf(
		"SELECT tenant_id, source_unit_id, target_unit_id FROM %s WHERE source_unit_id = %s OR target_unit_id = %s ORDER BY tenant_id",
		store.moveTable,
		store.placeholder(1),
		store.placeholder(2),
	)
	rows, err := store.db.QueryContext(ctx, query, unitID.String(), unitID.String())
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	moves = []Move{}
	for rows.Next() {
		move, err := scanMove(rows)
		if err != nil {
			return nil, err
		}
		moves = append(moves, move)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return moves, nil
}

// CreateMove records a prepared deployment move. It locks the active target
// unit and current assignment before inserting, which prevents a stale source
// assignment or concurrent unit disable from producing an invalid move.
func (store *SQLStore) CreateMove(ctx context.Context, move Move) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateMove(move); err != nil {
		return err
	}
	tx, err := store.beginPlacementTx(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, tx.Rollback())
		}
	}()
	unit, err := store.getUnitForUpdate(ctx, tx, move.TargetUnitID)
	if err != nil {
		return err
	}
	if unit.Status != types.DeploymentUnitStatusActive {
		return ErrDeploymentUnitUnavailable
	}
	assignment, err := store.getAssignmentForUpdate(ctx, tx, move.TenantID)
	if err != nil {
		return err
	}
	if assignment.UnitID != move.SourceUnitID {
		return ErrMoveConflict
	}

	query := fmt.Sprintf("INSERT INTO %s (tenant_id, source_unit_id, target_unit_id) VALUES (%s)", store.moveTable, store.placeholders(3, 1))
	_, err = tx.ExecContext(ctx, query, move.TenantID.String(), move.SourceUnitID.String(), move.TargetUnitID.String())
	if err != nil {
		return sqlutil.NormalizeDuplicateKeyError(err, ErrMoveAlreadyExists)
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return nil
}

// DeleteMove removes a prepared deployment move.
func (store *SQLStore) DeleteMove(ctx context.Context, tenantID types.TenantID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if tenantID == "" {
		return ErrInvalidMove
	}

	query := fmt.Sprintf("DELETE FROM %s WHERE tenant_id = %s", store.moveTable, store.placeholder(1))
	result, err := store.db.ExecContext(ctx, query, tenantID.String())
	if err != nil {
		return err
	}
	return requireAffectedRow(result, ErrMoveNotFound)
}

// DeleteMoveIfMatch removes a prepared move only when it still equals move.
// It prevents a stale cancellation request from deleting a replacement move.
func (store *SQLStore) DeleteMoveIfMatch(ctx context.Context, move Move) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateMove(move); err != nil {
		return err
	}
	tx, err := store.beginPlacementTx(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, tx.Rollback())
		}
	}()
	current, err := store.getMoveForUpdate(ctx, tx, move.TenantID)
	if err != nil {
		return err
	}
	if current != move {
		return ErrMoveConflict
	}
	query := fmt.Sprintf(
		"DELETE FROM %s WHERE tenant_id = %s AND source_unit_id = %s AND target_unit_id = %s",
		store.moveTable,
		store.placeholder(1),
		store.placeholder(2),
		store.placeholder(3),
	)
	result, err := tx.ExecContext(ctx, query, move.TenantID.String(), move.SourceUnitID.String(), move.TargetUnitID.String())
	if err != nil {
		return err
	}
	if err = requireAffectedRow(result, ErrMoveNotFound); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return nil
}

func (store *SQLStore) beginPlacementTx(ctx context.Context) (*sql.Tx, error) {
	return store.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
}

func (store *SQLStore) getUnitForUpdate(ctx context.Context, tx *sql.Tx, id types.DeploymentUnitID) (types.DeploymentUnit, error) {
	query := fmt.Sprintf("SELECT id, status, region, residency_tags, metadata FROM %s WHERE id = %s", store.unitTable, store.placeholder(1))
	if store.dialect != SQLDialectSQLite {
		query += " FOR UPDATE"
	}
	unit, err := scanUnit(tx.QueryRowContext(ctx, query, id.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return types.DeploymentUnit{}, ErrDeploymentUnitNotFound
	}
	if err != nil {
		return types.DeploymentUnit{}, err
	}
	return unit, nil
}

func (store *SQLStore) getAssignmentForUpdate(ctx context.Context, tx *sql.Tx, tenantID types.TenantID) (Assignment, error) {
	query := fmt.Sprintf("SELECT tenant_id, deployment_unit_id, version FROM %s WHERE tenant_id = %s", store.assignmentTable, store.placeholder(1))
	if store.dialect != SQLDialectSQLite {
		query += " FOR UPDATE"
	}
	assignment, err := scanAssignment(tx.QueryRowContext(ctx, query, tenantID.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return Assignment{}, ErrAssignmentNotFound
	}
	if err != nil {
		return Assignment{}, err
	}
	return assignment, nil
}

func (store *SQLStore) getMoveForUpdate(ctx context.Context, tx *sql.Tx, tenantID types.TenantID) (Move, error) {
	query := fmt.Sprintf("SELECT tenant_id, source_unit_id, target_unit_id FROM %s WHERE tenant_id = %s", store.moveTable, store.placeholder(1))
	if store.dialect != SQLDialectSQLite {
		query += " FOR UPDATE"
	}
	move, err := scanMove(tx.QueryRowContext(ctx, query, tenantID.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return Move{}, ErrMoveNotFound
	}
	if err != nil {
		return Move{}, err
	}
	return move, nil
}

func (store *SQLStore) unitReferencedInTx(ctx context.Context, tx *sql.Tx, id types.DeploymentUnitID) (bool, error) {
	assignmentQuery := fmt.Sprintf("SELECT 1 FROM %s WHERE deployment_unit_id = %s LIMIT 1", store.assignmentTable, store.placeholder(1))
	referenced, err := queryExists(ctx, tx, assignmentQuery, id.String())
	if err != nil || referenced {
		return referenced, err
	}
	moveQuery := fmt.Sprintf(
		"SELECT 1 FROM %s WHERE source_unit_id = %s OR target_unit_id = %s LIMIT 1",
		store.moveTable,
		store.placeholder(1),
		store.placeholder(2),
	)
	return queryExists(ctx, tx, moveQuery, id.String(), id.String())
}

func queryExists(ctx context.Context, tx *sql.Tx, query string, args ...any) (bool, error) {
	var value int
	err := tx.QueryRowContext(ctx, query, args...).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

type deploymentScanner interface {
	Scan(dest ...any) error
}

func scanUnit(scanner deploymentScanner) (types.DeploymentUnit, error) {
	var (
		id            string
		status        string
		region        string
		residencyTags string
		metadata      string
	)
	if err := scanner.Scan(&id, &status, &region, &residencyTags, &metadata); err != nil {
		return types.DeploymentUnit{}, err
	}
	unit, err := unmarshalUnitParts(id, status, region, residencyTags, metadata)
	if err != nil {
		return types.DeploymentUnit{}, err
	}
	if err := validateUnit(unit); err != nil {
		return types.DeploymentUnit{}, err
	}
	return unit, nil
}

func scanAssignment(scanner deploymentScanner) (Assignment, error) {
	var (
		tenantID string
		unitID   string
		version  uint64
	)
	if err := scanner.Scan(&tenantID, &unitID, &version); err != nil {
		return Assignment{}, err
	}
	assignment := Assignment{TenantID: types.TenantID(tenantID), UnitID: types.DeploymentUnitID(unitID), Version: version}
	if err := validateAssignment(assignment); err != nil {
		return Assignment{}, err
	}
	return assignment, nil
}

func scanMove(scanner deploymentScanner) (Move, error) {
	var (
		tenantID string
		sourceID string
		targetID string
	)
	if err := scanner.Scan(&tenantID, &sourceID, &targetID); err != nil {
		return Move{}, err
	}
	move := Move{TenantID: types.TenantID(tenantID), SourceUnitID: types.DeploymentUnitID(sourceID), TargetUnitID: types.DeploymentUnitID(targetID)}
	if err := validateMove(move); err != nil {
		return Move{}, err
	}
	return move, nil
}

func marshalUnitParts(unit types.DeploymentUnit) (string, string, error) {
	tags := unit.ResidencyTags
	if tags == nil {
		tags = []string{}
	}
	encodedTags, err := json.Marshal(tags)
	if err != nil {
		return "", "", err
	}
	metadata, err := sqlutil.MarshalStringMap(unit.Metadata)
	if err != nil {
		return "", "", err
	}
	return string(encodedTags), metadata, nil
}

func unmarshalUnitParts(id string, status string, region string, rawTags string, rawMetadata string) (types.DeploymentUnit, error) {
	tags := []string{}
	if strings.TrimSpace(rawTags) != "" {
		if err := json.Unmarshal([]byte(rawTags), &tags); err != nil {
			return types.DeploymentUnit{}, err
		}
	}
	metadata, err := sqlutil.UnmarshalStringMap(rawMetadata)
	if err != nil {
		return types.DeploymentUnit{}, err
	}
	return types.DeploymentUnit{
		ID:            types.DeploymentUnitID(id),
		Status:        types.DeploymentUnitStatus(status),
		Region:        region,
		ResidencyTags: tags,
		Metadata:      metadata,
	}, nil
}

func unitsEqual(a types.DeploymentUnit, b types.DeploymentUnit) bool {
	if a.ID != b.ID || a.Status != b.Status || a.Region != b.Region || len(a.ResidencyTags) != len(b.ResidencyTags) || len(a.Metadata) != len(b.Metadata) {
		return false
	}
	for index, tag := range a.ResidencyTags {
		if tag != b.ResidencyTags[index] {
			return false
		}
	}
	for key, value := range a.Metadata {
		if b.Metadata[key] != value {
			return false
		}
	}
	return true
}

func requireAffectedRow(result sql.Result, notFound error) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return notFound
	}
	return nil
}

func (store *SQLStore) placeholder(index int) string {
	return sqlutil.Placeholder(store.dialect, index)
}

func (store *SQLStore) placeholders(count int, start int) string {
	return sqlutil.Placeholders(store.dialect, count, start)
}

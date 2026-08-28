package plan

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/darkinno-tech/saas/internal/sqlutil"
)

const (
	// DefaultSQLTableName is the default plan table name.
	DefaultSQLTableName = "saas_plans"
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
var _ PagedStore = (*SQLStore)(nil)

// SQLStore persists SaaS plans through database/sql.
//
// The table is expected to contain these columns:
// id, name, features, quotas.
// The features and quotas columns store JSON arrays.
type SQLStore struct {
	db      *sql.DB
	table   string
	dialect SQLDialect
}

// SQLStoreOption configures SQLStore.
type SQLStoreOption func(*SQLStore) error

// WithTableName overrides the default plan table name.
func WithTableName(table string) SQLStoreOption {
	return func(store *SQLStore) error {
		if !sqlutil.IsSafeQualifiedIdentifier(table) {
			return fmt.Errorf("%w: %q", ErrInvalidTableName, table)
		}
		store.table = table
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

// NewSQLStore creates a SQL-backed plan store.
func NewSQLStore(db *sql.DB, opts ...SQLStoreOption) (*SQLStore, error) {
	if db == nil {
		return nil, ErrNilDB
	}

	store := &SQLStore{db: db, table: DefaultSQLTableName, dialect: SQLDialectMySQL}
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

// Create inserts a plan.
func (store *SQLStore) Create(ctx context.Context, plan Plan) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validatePlan(plan); err != nil {
		return err
	}

	features, quotas, err := marshalPlanParts(plan)
	if err != nil {
		return err
	}

	query := fmt.Sprintf(
		"INSERT INTO %s (id, name, features, quotas) VALUES (%s)",
		store.table,
		store.placeholders(4, 1),
	)
	_, err = store.db.ExecContext(ctx, query, plan.ID, plan.Name, features, quotas)
	return sqlutil.NormalizeDuplicateKeyError(err, ErrPlanAlreadyExists)
}

// Get returns a plan by ID.
func (store *SQLStore) Get(ctx context.Context, id string) (Plan, error) {
	if err := ctx.Err(); err != nil {
		return Plan{}, err
	}
	if id == "" {
		return Plan{}, ErrInvalidPlan
	}

	query := fmt.Sprintf("SELECT id, name, features, quotas FROM %s WHERE id = %s", store.table, store.placeholder(1))
	plan, err := scanPlan(store.db.QueryRowContext(ctx, query, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Plan{}, ErrPlanNotFound
	}
	if err != nil {
		return Plan{}, err
	}
	return plan, nil
}

// List returns plans matching filter.
func (store *SQLStore) List(ctx context.Context, filter ListFilter) (plans []Plan, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := filter.validate(); err != nil {
		return nil, err
	}
	return store.list(ctx, filter, "")
}

// ListPage returns plans after the cursor while preserving List filtering semantics.
func (store *SQLStore) ListPage(ctx context.Context, filter PageFilter) (plans []Plan, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := filter.validate(); err != nil {
		return nil, err
	}
	return store.list(ctx, filter.listFilter(), filter.Cursor)
}

func (store *SQLStore) list(ctx context.Context, filter ListFilter, cursor string) (plans []Plan, err error) {
	query := fmt.Sprintf("SELECT id, name, features, quotas FROM %s", store.table)
	args := make([]any, 0, len(filter.IDs)+2)
	where := []string{}
	if len(filter.IDs) > 0 {
		where = append(where, "id IN ("+store.placeholders(len(filter.IDs), len(args)+1)+")")
		for _, id := range filter.IDs {
			args = append(args, id)
		}
	}
	if cursor != "" {
		where = append(where, "id > "+store.placeholder(len(args)+1))
		args = append(args, cursor)
	}
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY id"
	if filter.Limit > 0 {
		query += " LIMIT " + store.placeholder(len(args)+1)
		args = append(args, filter.Limit)
		if filter.Offset > 0 {
			query += " OFFSET " + store.placeholder(len(args)+1)
			args = append(args, filter.Offset)
		}
	}

	rows, err := store.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() {
		err = errors.Join(err, rows.Close())
	}()

	plans = []Plan{}
	for rows.Next() {
		plan, err := scanPlan(rows)
		if err != nil {
			return nil, err
		}
		plans = append(plans, plan)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return plans, nil
}

// Update replaces a plan.
func (store *SQLStore) Update(ctx context.Context, plan Plan) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validatePlan(plan); err != nil {
		return err
	}

	features, quotas, err := marshalPlanParts(plan)
	if err != nil {
		return err
	}

	query := fmt.Sprintf(
		"UPDATE %s SET name = %s, features = %s, quotas = %s WHERE id = %s",
		store.table,
		store.placeholder(1),
		store.placeholder(2),
		store.placeholder(3),
		store.placeholder(4),
	)
	result, err := store.db.ExecContext(ctx, query, plan.Name, features, quotas, plan.ID)
	if err != nil {
		return err
	}
	return store.requireUpdatedRow(ctx, plan, result)
}

// Delete removes a plan by ID.
func (store *SQLStore) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if id == "" {
		return ErrInvalidPlan
	}

	query := fmt.Sprintf("DELETE FROM %s WHERE id = %s", store.table, store.placeholder(1))
	result, err := store.db.ExecContext(ctx, query, id)
	if err != nil {
		return err
	}
	return store.requireAffectedRow(result)
}

type planScanner interface {
	Scan(dest ...any) error
}

func scanPlan(scanner planScanner) (Plan, error) {
	var (
		id       string
		name     string
		features string
		quotas   string
	)
	if err := scanner.Scan(&id, &name, &features, &quotas); err != nil {
		return Plan{}, err
	}

	decodedFeatures, err := unmarshalFeatures(features)
	if err != nil {
		return Plan{}, err
	}
	decodedQuotas, err := unmarshalQuotas(quotas)
	if err != nil {
		return Plan{}, err
	}
	return Plan{ID: id, Name: name, Features: decodedFeatures, Quotas: decodedQuotas}, nil
}

func marshalPlanParts(plan Plan) (string, string, error) {
	features, err := json.Marshal(plan.Features)
	if err != nil {
		return "", "", err
	}
	quotas, err := json.Marshal(plan.Quotas)
	if err != nil {
		return "", "", err
	}
	return string(features), string(quotas), nil
}

func unmarshalFeatures(raw string) ([]Feature, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	features := []Feature{}
	if err := json.Unmarshal([]byte(raw), &features); err != nil {
		return nil, err
	}
	return features, nil
}

func unmarshalQuotas(raw string) ([]Quota, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	quotas := []Quota{}
	if err := json.Unmarshal([]byte(raw), &quotas); err != nil {
		return nil, err
	}
	return quotas, nil
}

func (store *SQLStore) requireAffectedRow(result sql.Result) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrPlanNotFound
	}
	return nil
}

func (store *SQLStore) requireUpdatedRow(ctx context.Context, desired Plan, result sql.Result) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected > 0 {
		return nil
	}
	current, err := store.Get(ctx, desired.ID)
	if err != nil {
		return err
	}
	return confirmUpdatedPlan(current, desired)
}

func confirmUpdatedPlan(current Plan, desired Plan) error {
	if !reflect.DeepEqual(current, desired) {
		return ErrPlanConflict
	}
	return nil
}

func (store *SQLStore) placeholders(count int, start int) string {
	return sqlutil.Placeholders(store.dialect, count, start)
}

func (store *SQLStore) placeholder(index int) string {
	return sqlutil.Placeholder(store.dialect, index)
}

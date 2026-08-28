package feature

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"

	"github.com/im10furry/saas/core/types"
	"github.com/im10furry/saas/internal/sqlutil"
)

const (
	// DefaultSQLTableName is the default feature flag table name.
	DefaultSQLTableName = "saas_feature_flags"

	sqlScopePlan   = "plan"
	sqlScopeTenant = "tenant"
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

// SQLStore persists plan feature defaults and tenant overrides through database/sql.
//
// The table is expected to contain these columns:
// scope, owner_id, key, enabled, config.
// Use a unique key on (scope, owner_id, key). The config column stores a JSON object.
type SQLStore struct {
	db      *sql.DB
	table   string
	dialect SQLDialect
}

// SQLStoreOption configures SQLStore.
type SQLStoreOption func(*SQLStore) error

// WithTableName overrides the default feature flag table name.
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

// NewSQLStore creates a SQL-backed feature store.
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

// SetPlanDefaults replaces default flags for a plan.
func (store *SQLStore) SetPlanDefaults(ctx context.Context, planID string, flags []Flag) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if planID == "" {
		return ErrInvalidFeature
	}
	return store.replaceFlags(ctx, sqlScopePlan, planID, flags)
}

// SetTenantOverrides replaces tenant-level overrides.
func (store *SQLStore) SetTenantOverrides(ctx context.Context, tenantID types.TenantID, flags []Flag) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if tenantID == "" {
		return ErrInvalidFeature
	}
	return store.replaceFlags(ctx, sqlScopeTenant, tenantID.String(), flags)
}

// Resolve returns the tenant override when present, otherwise the plan default.
func (store *SQLStore) Resolve(ctx context.Context, tenantID types.TenantID, planID string, key string) (Flag, error) {
	if err := ctx.Err(); err != nil {
		return Flag{}, err
	}
	if tenantID == "" || planID == "" || key == "" {
		return Flag{}, ErrInvalidFeature
	}

	flag, err := store.getFlag(ctx, sqlScopeTenant, tenantID.String(), key)
	if err == nil {
		return flag, nil
	}
	if !errors.Is(err, ErrFeatureNotFound) {
		return Flag{}, err
	}

	return store.getFlag(ctx, sqlScopePlan, planID, key)
}

// List returns merged feature flags.
func (store *SQLStore) List(ctx context.Context, tenantID types.TenantID, planID string) ([]Flag, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if tenantID == "" || planID == "" {
		return nil, ErrInvalidFeature
	}

	defaults, err := store.loadFlags(ctx, sqlScopePlan, planID)
	if err != nil {
		return nil, err
	}
	overrides, err := store.loadFlags(ctx, sqlScopeTenant, tenantID.String())
	if err != nil {
		return nil, err
	}

	for key, flag := range overrides {
		defaults[key] = flag
	}

	keys := make([]string, 0, len(defaults))
	for key := range defaults {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	flags := make([]Flag, 0, len(keys))
	for _, key := range keys {
		flags = append(flags, defaults[key])
	}
	return flags, nil
}

func (store *SQLStore) replaceFlags(ctx context.Context, scope string, ownerID string, flags []Flag) (err error) {
	index, err := indexFlags(flags)
	if err != nil {
		return err
	}

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, tx.Rollback())
		}
	}()

	deleteQuery := fmt.Sprintf("DELETE FROM %s WHERE scope = %s AND owner_id = %s", store.table, store.placeholder(1), store.placeholder(2))
	if _, err = tx.ExecContext(ctx, deleteQuery, scope, ownerID); err != nil {
		return err
	}

	keys := make([]string, 0, len(index))
	for key := range index {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	insertQuery := fmt.Sprintf(
		"INSERT INTO %s (scope, owner_id, %s, enabled, config) VALUES (%s)",
		store.table,
		store.keyColumn(),
		store.placeholders(5, 1),
	)
	for _, key := range keys {
		flag := index[key]
		config, err := sqlutil.MarshalStringMap(flag.Config)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, insertQuery, scope, ownerID, flag.Key, flag.Enabled, config); err != nil {
			return err
		}
	}

	if err = tx.Commit(); err != nil {
		return err
	}
	return nil
}

func (store *SQLStore) getFlag(ctx context.Context, scope string, ownerID string, key string) (Flag, error) {
	query := fmt.Sprintf(
		"SELECT %s, enabled, config FROM %s WHERE scope = %s AND owner_id = %s AND %s = %s",
		store.keyColumn(),
		store.table,
		store.placeholder(1),
		store.placeholder(2),
		store.keyColumn(),
		store.placeholder(3),
	)
	flag, err := scanFlag(store.db.QueryRowContext(ctx, query, scope, ownerID, key))
	if errors.Is(err, sql.ErrNoRows) {
		return Flag{}, ErrFeatureNotFound
	}
	if err != nil {
		return Flag{}, err
	}
	return flag, nil
}

func (store *SQLStore) loadFlags(ctx context.Context, scope string, ownerID string) (flags map[string]Flag, err error) {
	query := fmt.Sprintf(
		"SELECT %s, enabled, config FROM %s WHERE scope = %s AND owner_id = %s ORDER BY %s",
		store.keyColumn(),
		store.table,
		store.placeholder(1),
		store.placeholder(2),
		store.keyColumn(),
	)
	rows, err := store.db.QueryContext(ctx, query, scope, ownerID)
	if err != nil {
		return nil, err
	}
	defer func() {
		err = errors.Join(err, rows.Close())
	}()

	flags = map[string]Flag{}
	for rows.Next() {
		flag, err := scanFlag(rows)
		if err != nil {
			return nil, err
		}
		flags[flag.Key] = flag
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return flags, nil
}

type flagScanner interface {
	Scan(dest ...any) error
}

func scanFlag(scanner flagScanner) (Flag, error) {
	var (
		key     string
		enabled bool
		config  string
	)
	if err := scanner.Scan(&key, &enabled, &config); err != nil {
		return Flag{}, err
	}
	if key == "" {
		return Flag{}, ErrInvalidFeature
	}

	decoded, err := sqlutil.UnmarshalStringMap(config)
	if err != nil {
		return Flag{}, err
	}
	return Flag{Key: key, Enabled: enabled, Config: decoded}, nil
}

func (store *SQLStore) placeholders(count int, start int) string {
	return sqlutil.Placeholders(store.dialect, count, start)
}

func (store *SQLStore) placeholder(index int) string {
	return sqlutil.Placeholder(store.dialect, index)
}

func (store *SQLStore) keyColumn() string {
	if store.dialect == SQLDialectMySQL {
		return "`key`"
	}
	return "key"
}

package quota

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/darkinno-tech/saas/core/types"
	"github.com/darkinno-tech/saas/internal/sqlutil"
)

const (
	// DefaultSQLTableName is the default quota usage table name.
	DefaultSQLTableName = "saas_quota_usage"

	quotaMutationMaxAttempts = 16
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

// SQLStore tracks quota usage through database/sql.
//
// The table is expected to contain these columns:
// tenant_id, resource, period, used.
// Use a unique key on (tenant_id, resource, period).
type SQLStore struct {
	db      *sql.DB
	table   string
	dialect SQLDialect
}

// SQLStoreOption configures SQLStore.
type SQLStoreOption func(*SQLStore) error

// WithTableName overrides the default quota usage table name.
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

// NewSQLStore creates a SQL-backed quota store.
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

// Add increments usage.
func (store *SQLStore) Add(ctx context.Context, tenantID types.TenantID, resource string, period Period, amount int64) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if tenantID == "" || resource == "" || period == "" || amount < 0 {
		return 0, ErrInvalidQuota
	}
	return store.mutateUsage(ctx, tenantID, resource, period, amount, nil)
}

// Consume increments usage only when the configured limit allows it.
func (store *SQLStore) Consume(ctx context.Context, limit Limit, amount int64) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := validateLimit(limit); err != nil {
		return 0, err
	}
	if amount < 0 {
		return 0, ErrInvalidQuota
	}
	return store.mutateUsage(ctx, limit.TenantID, limit.Resource, limit.Period, amount, &limit)
}

// Get returns current usage.
func (store *SQLStore) Get(ctx context.Context, tenantID types.TenantID, resource string, period Period) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if tenantID == "" || resource == "" || period == "" {
		return 0, ErrInvalidQuota
	}

	query := fmt.Sprintf(
		"SELECT used FROM %s WHERE tenant_id = %s AND resource = %s AND period = %s",
		store.table,
		store.placeholder(1),
		store.placeholder(2),
		store.placeholder(3),
	)
	var used int64
	err := store.db.QueryRowContext(ctx, query, tenantID.String(), resource, string(period)).Scan(&used)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if used < 0 {
		return 0, ErrInvalidQuota
	}
	return used, nil
}

// Reset clears usage.
func (store *SQLStore) Reset(ctx context.Context, tenantID types.TenantID, resource string, period Period) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if tenantID == "" || resource == "" || period == "" {
		return ErrInvalidQuota
	}

	query := fmt.Sprintf(
		"DELETE FROM %s WHERE tenant_id = %s AND resource = %s AND period = %s",
		store.table,
		store.placeholder(1),
		store.placeholder(2),
		store.placeholder(3),
	)
	_, err := store.db.ExecContext(ctx, query, tenantID.String(), resource, string(period))
	return err
}

func (store *SQLStore) mutateUsage(ctx context.Context, tenantID types.TenantID, resource string, period Period, amount int64, limit *Limit) (int64, error) {
	return retryQuotaMutation(ctx, func() (int64, error) {
		return store.mutateUsageOnce(ctx, tenantID, resource, period, amount, limit)
	}, waitForQuotaMutationRetry)
}

func retryQuotaMutation(ctx context.Context, mutate func() (int64, error), wait func(context.Context, int) error) (int64, error) {
	var (
		used int64
		err  error
	)
	for attempt := 0; attempt < quotaMutationMaxAttempts; attempt++ {
		used, err = mutate()
		if !sqlutil.IsDuplicateKeyError(err) && !sqlutil.IsRetryableTransactionError(err) {
			return used, err
		}
		if attempt == quotaMutationMaxAttempts-1 {
			return used, err
		}
		if err := wait(ctx, attempt); err != nil {
			return 0, err
		}
	}
	return used, err
}

func waitForQuotaMutationRetry(ctx context.Context, attempt int) error {
	shift := attempt
	if shift > 4 {
		shift = 4
	}
	timer := time.NewTimer(time.Millisecond << shift)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (store *SQLStore) mutateUsageOnce(ctx context.Context, tenantID types.TenantID, resource string, period Period, amount int64, limit *Limit) (used int64, err error) {
	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return 0, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, tx.Rollback())
		}
	}()

	current, found, err := store.getUsageForUpdate(ctx, tx, tenantID, resource, period)
	if err != nil {
		return 0, err
	}
	if limit != nil && amount > limit.Limit-current {
		return current, ErrQuotaExceeded
	}
	if amount > math.MaxInt64-current {
		return 0, ErrInvalidQuota
	}

	next := current + amount
	if found {
		if next != current {
			if err := store.updateUsage(ctx, tx, tenantID, resource, period, next); err != nil {
				return 0, err
			}
		}
	} else {
		if err := store.insertUsage(ctx, tx, tenantID, resource, period, next); err != nil {
			return 0, err
		}
	}

	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return next, nil
}

func (store *SQLStore) getUsageForUpdate(ctx context.Context, tx *sql.Tx, tenantID types.TenantID, resource string, period Period) (int64, bool, error) {
	query := fmt.Sprintf(
		"SELECT used FROM %s WHERE tenant_id = %s AND resource = %s AND period = %s",
		store.table,
		store.placeholder(1),
		store.placeholder(2),
		store.placeholder(3),
	)
	if store.dialect != SQLDialectSQLite {
		query += " FOR UPDATE"
	}

	var used int64
	err := tx.QueryRowContext(ctx, query, tenantID.String(), resource, string(period)).Scan(&used)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	if used < 0 {
		return 0, false, ErrInvalidQuota
	}
	return used, true, nil
}

func (store *SQLStore) insertUsage(ctx context.Context, tx *sql.Tx, tenantID types.TenantID, resource string, period Period, used int64) error {
	query := fmt.Sprintf(
		"INSERT INTO %s (tenant_id, resource, period, used) VALUES (%s)",
		store.table,
		store.placeholders(4, 1),
	)
	_, err := tx.ExecContext(ctx, query, tenantID.String(), resource, string(period), used)
	return err
}

func (store *SQLStore) updateUsage(ctx context.Context, tx *sql.Tx, tenantID types.TenantID, resource string, period Period, used int64) error {
	query := fmt.Sprintf(
		"UPDATE %s SET used = %s WHERE tenant_id = %s AND resource = %s AND period = %s",
		store.table,
		store.placeholder(1),
		store.placeholder(2),
		store.placeholder(3),
		store.placeholder(4),
	)
	result, err := tx.ExecContext(ctx, query, used, tenantID.String(), resource, string(period))
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrInvalidQuota
	}
	return nil
}

func (store *SQLStore) placeholders(count int, start int) string {
	return sqlutil.Placeholders(store.dialect, count, start)
}

func (store *SQLStore) placeholder(index int) string {
	return sqlutil.Placeholder(store.dialect, index)
}

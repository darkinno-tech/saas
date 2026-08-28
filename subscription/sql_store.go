package subscription

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/im10furry/saas/core/types"
	"github.com/im10furry/saas/internal/sqlutil"
)

const (
	// DefaultSQLTableName is the default subscription table name.
	DefaultSQLTableName = "saas_subscriptions"
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

// SQLStore persists tenant subscriptions through database/sql.
//
// The table is expected to contain these columns:
// tenant_id, plan_id, status, start_date, end_date, current_period_end, grace_period_end.
type SQLStore struct {
	db      *sql.DB
	table   string
	dialect SQLDialect
}

// SQLStoreOption configures SQLStore.
type SQLStoreOption func(*SQLStore) error

// WithTableName overrides the default subscription table name.
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

// NewSQLStore creates a SQL-backed subscription store.
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

// Create inserts a subscription.
func (store *SQLStore) Create(ctx context.Context, subscription Subscription) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateSubscription(subscription); err != nil {
		return err
	}

	query := fmt.Sprintf(
		"INSERT INTO %s (tenant_id, plan_id, status, start_date, end_date, current_period_end, grace_period_end) VALUES (%s)",
		store.table,
		store.placeholders(7, 1),
	)
	_, err := store.db.ExecContext(
		ctx,
		query,
		subscription.TenantID.String(),
		subscription.PlanID,
		string(subscription.Status),
		subscription.StartDate,
		timePtrValue(subscription.EndDate),
		timePtrValue(subscription.CurrentPeriodEnd),
		timePtrValue(subscription.GracePeriodEnd),
	)
	return sqlutil.NormalizeDuplicateKeyError(err, ErrSubscriptionAlreadyExists)
}

// Get returns a subscription by tenant ID.
func (store *SQLStore) Get(ctx context.Context, tenantID types.TenantID) (Subscription, error) {
	if err := ctx.Err(); err != nil {
		return Subscription{}, err
	}
	if tenantID == "" {
		return Subscription{}, ErrInvalidSubscription
	}

	query := fmt.Sprintf(
		"SELECT tenant_id, plan_id, status, start_date, end_date, current_period_end, grace_period_end FROM %s WHERE tenant_id = %s",
		store.table,
		store.placeholder(1),
	)
	subscription, err := scanSubscription(store.db.QueryRowContext(ctx, query, tenantID.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return Subscription{}, ErrSubscriptionNotFound
	}
	if err != nil {
		return Subscription{}, err
	}
	return subscription, nil
}

// List returns subscriptions matching filter.
func (store *SQLStore) List(ctx context.Context, filter ListFilter) (subscriptions []Subscription, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := filter.validate(); err != nil {
		return nil, err
	}
	return store.list(ctx, filter, "")
}

// ListPage returns subscriptions after the cursor while preserving List filtering semantics.
func (store *SQLStore) ListPage(ctx context.Context, filter PageFilter) (subscriptions []Subscription, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := filter.validate(); err != nil {
		return nil, err
	}
	return store.list(ctx, filter.listFilter(), filter.Cursor)
}

func (store *SQLStore) list(ctx context.Context, filter ListFilter, cursor types.TenantID) (subscriptions []Subscription, err error) {
	args := []any{}
	where := []string{}
	if len(filter.TenantIDs) > 0 {
		where = append(where, "tenant_id IN ("+store.placeholders(len(filter.TenantIDs), len(args)+1)+")")
		for _, tenantID := range filter.TenantIDs {
			args = append(args, tenantID.String())
		}
	}
	if len(filter.PlanIDs) > 0 {
		where = append(where, "plan_id IN ("+store.placeholders(len(filter.PlanIDs), len(args)+1)+")")
		for _, planID := range filter.PlanIDs {
			args = append(args, planID)
		}
	}
	if len(filter.Statuses) > 0 {
		where = append(where, "status IN ("+store.placeholders(len(filter.Statuses), len(args)+1)+")")
		for _, status := range filter.Statuses {
			args = append(args, string(status))
		}
	}
	if cursor != "" {
		where = append(where, "tenant_id > "+store.placeholder(len(args)+1))
		args = append(args, cursor.String())
	}

	query := fmt.Sprintf("SELECT tenant_id, plan_id, status, start_date, end_date, current_period_end, grace_period_end FROM %s", store.table)
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY tenant_id"
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

	subscriptions = []Subscription{}
	for rows.Next() {
		subscription, err := scanSubscription(rows)
		if err != nil {
			return nil, err
		}
		subscriptions = append(subscriptions, subscription)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return subscriptions, nil
}

// Update replaces a subscription.
func (store *SQLStore) Update(ctx context.Context, subscription Subscription) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateSubscription(subscription); err != nil {
		return err
	}

	query := fmt.Sprintf(
		"UPDATE %s SET plan_id = %s, status = %s, start_date = %s, end_date = %s, current_period_end = %s, grace_period_end = %s WHERE tenant_id = %s",
		store.table,
		store.placeholder(1),
		store.placeholder(2),
		store.placeholder(3),
		store.placeholder(4),
		store.placeholder(5),
		store.placeholder(6),
		store.placeholder(7),
	)
	result, err := store.db.ExecContext(
		ctx,
		query,
		subscription.PlanID,
		string(subscription.Status),
		subscription.StartDate,
		timePtrValue(subscription.EndDate),
		timePtrValue(subscription.CurrentPeriodEnd),
		timePtrValue(subscription.GracePeriodEnd),
		subscription.TenantID.String(),
	)
	if err != nil {
		return err
	}
	return store.requireUpdatedRow(ctx, subscription, result)
}

// Delete removes a subscription by tenant ID.
func (store *SQLStore) Delete(ctx context.Context, tenantID types.TenantID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if tenantID == "" {
		return ErrInvalidSubscription
	}

	query := fmt.Sprintf("DELETE FROM %s WHERE tenant_id = %s", store.table, store.placeholder(1))
	result, err := store.db.ExecContext(ctx, query, tenantID.String())
	if err != nil {
		return err
	}
	return store.requireAffectedRow(result)
}

type subscriptionScanner interface {
	Scan(dest ...any) error
}

func scanSubscription(scanner subscriptionScanner) (Subscription, error) {
	var (
		tenantID         string
		planID           string
		status           string
		startDate        time.Time
		endDate          sql.NullTime
		currentPeriodEnd sql.NullTime
		gracePeriodEnd   sql.NullTime
	)
	if err := scanner.Scan(&tenantID, &planID, &status, &startDate, &endDate, &currentPeriodEnd, &gracePeriodEnd); err != nil {
		return Subscription{}, err
	}

	subscription := Subscription{
		TenantID:         types.TenantID(tenantID),
		PlanID:           planID,
		Status:           Status(status),
		StartDate:        startDate,
		EndDate:          nullTimePtr(endDate),
		CurrentPeriodEnd: nullTimePtr(currentPeriodEnd),
		GracePeriodEnd:   nullTimePtr(gracePeriodEnd),
	}
	if err := validateSubscription(subscription); err != nil {
		return Subscription{}, err
	}
	return subscription, nil
}

func timePtrValue(value *time.Time) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullTimePtr(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	return &value.Time
}

func (store *SQLStore) requireAffectedRow(result sql.Result) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrSubscriptionNotFound
	}
	return nil
}

func (store *SQLStore) requireUpdatedRow(ctx context.Context, desired Subscription, result sql.Result) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected > 0 {
		return nil
	}
	current, err := store.Get(ctx, desired.TenantID)
	if err != nil {
		return err
	}
	return confirmUpdatedSubscription(current, desired)
}

func confirmUpdatedSubscription(current Subscription, desired Subscription) error {
	if !subscriptionsEqual(current, desired) {
		return ErrSubscriptionConflict
	}
	return nil
}

func (store *SQLStore) placeholders(count int, start int) string {
	return sqlutil.Placeholders(store.dialect, count, start)
}

func (store *SQLStore) placeholder(index int) string {
	return sqlutil.Placeholder(store.dialect, index)
}

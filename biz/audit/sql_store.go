package audit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/darkinno-tech/saas/core/types"
	"github.com/darkinno-tech/saas/internal/sqlutil"
)

const (
	// DefaultSQLTableName is the default audit event table name.
	DefaultSQLTableName = "audit_events"
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

// SQLStore persists audit events through database/sql.
//
// The table is expected to contain these columns:
// id, tenant_id, actor_id, action, resource, created_at, metadata.
// The metadata column stores a JSON object. If Event.ID is empty, SQLStore omits
// the id column so databases with generated IDs can apply their default.
type SQLStore struct {
	db      *sql.DB
	table   string
	dialect SQLDialect
	now     func() time.Time
}

// SQLStoreOption configures SQLStore.
type SQLStoreOption func(*SQLStore) error

// WithTableName overrides the default audit event table name.
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

// NewSQLStore creates a SQL-backed audit store.
func NewSQLStore(db *sql.DB, opts ...SQLStoreOption) (*SQLStore, error) {
	if db == nil {
		return nil, ErrNilDB
	}

	store := &SQLStore{db: db, table: DefaultSQLTableName, dialect: SQLDialectMySQL, now: time.Now}
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

// Record inserts an audit event.
func (store *SQLStore) Record(ctx context.Context, event Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if event.TenantID == "" || event.Action == "" || event.Resource == "" {
		return ErrInvalidEvent
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = store.now()
	}

	metadata, err := sqlutil.MarshalStringMap(event.Metadata)
	if err != nil {
		return err
	}

	if event.ID == "" {
		query := fmt.Sprintf(
			"INSERT INTO %s (tenant_id, actor_id, action, resource, created_at, metadata) VALUES (%s)",
			store.table,
			store.placeholders(6, 1),
		)
		_, err = store.db.ExecContext(ctx, query, event.TenantID.String(), nullableString(event.ActorID), event.Action, event.Resource, event.CreatedAt, metadata)
		return err
	}

	query := fmt.Sprintf(
		"INSERT INTO %s (id, tenant_id, actor_id, action, resource, created_at, metadata) VALUES (%s)",
		store.table,
		store.placeholders(7, 1),
	)
	_, err = store.db.ExecContext(ctx, query, event.ID, event.TenantID.String(), nullableString(event.ActorID), event.Action, event.Resource, event.CreatedAt, metadata)
	return err
}

// List returns audit events for a tenant ordered by creation time.
func (store *SQLStore) List(ctx context.Context, tenantID types.TenantID) (events []Event, err error) {
	return store.ListPage(ctx, tenantID, ListFilter{})
}

// ListPage returns a bounded page of audit events for a tenant ordered by creation time and ID.
func (store *SQLStore) ListPage(ctx context.Context, tenantID types.TenantID, filter ListFilter) (events []Event, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if tenantID == "" {
		return nil, ErrInvalidEvent
	}
	if err := filter.validate(); err != nil {
		return nil, err
	}

	args := []any{tenantID.String()}
	where := []string{"tenant_id = " + store.placeholder(1)}
	if !filter.Cursor.empty() {
		where = append(where, fmt.Sprintf(
			"(created_at > %s OR (created_at = %s AND id > %s))",
			store.placeholder(len(args)+1),
			store.placeholder(len(args)+2),
			store.placeholder(len(args)+3),
		))
		args = append(args, filter.Cursor.CreatedAt, filter.Cursor.CreatedAt, filter.Cursor.ID)
	}
	query := fmt.Sprintf(
		"SELECT id, tenant_id, actor_id, action, resource, created_at, metadata FROM %s WHERE %s ORDER BY created_at, id",
		store.table,
		strings.Join(where, " AND "),
	)
	if filter.Limit > 0 {
		query += " LIMIT " + store.placeholder(len(args)+1)
		args = append(args, filter.Limit)
	}
	rows, err := store.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() {
		err = errors.Join(err, rows.Close())
	}()

	events = []Event{}
	for rows.Next() {
		event, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

type eventScanner interface {
	Scan(dest ...any) error
}

func scanEvent(scanner eventScanner) (Event, error) {
	var (
		id        sql.NullString
		tenantID  string
		actorID   sql.NullString
		action    string
		resource  string
		createdAt time.Time
		metadata  string
	)
	if err := scanner.Scan(&id, &tenantID, &actorID, &action, &resource, &createdAt, &metadata); err != nil {
		return Event{}, err
	}

	decoded, err := sqlutil.UnmarshalStringMap(metadata)
	if err != nil {
		return Event{}, err
	}
	event := Event{
		ID:        id.String,
		TenantID:  types.TenantID(tenantID),
		ActorID:   actorID.String,
		Action:    action,
		Resource:  resource,
		CreatedAt: createdAt,
		Metadata:  decoded,
	}
	if event.TenantID == "" || event.Action == "" || event.Resource == "" || event.CreatedAt.IsZero() {
		return Event{}, ErrInvalidEvent
	}
	return event, nil
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (store *SQLStore) placeholders(count int, start int) string {
	return sqlutil.Placeholders(store.dialect, count, start)
}

func (store *SQLStore) placeholder(index int) string {
	return sqlutil.Placeholder(store.dialect, index)
}

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/darkinno-tech/saas/core/types"
)

const (
	// DefaultSQLTableName is the default tenant metadata table name.
	DefaultSQLTableName = "tenants"
)

// SQLDialect controls SQL placeholder rendering for SQLStore.
type SQLDialect string

const (
	// SQLDialectMySQL uses question-mark placeholders and is the default.
	SQLDialectMySQL SQLDialect = "mysql"

	// SQLDialectSQLite uses question-mark placeholders.
	SQLDialectSQLite SQLDialect = "sqlite"

	// SQLDialectPostgres uses numbered placeholders.
	SQLDialectPostgres SQLDialect = "postgres"
)

var _ Store = (*SQLStore)(nil)
var _ PagedStore = (*SQLStore)(nil)
var _ CompareAndSwapStore = (*SQLStore)(nil)

// SQLStore persists tenant metadata through database/sql.
//
// The table is expected to contain these columns:
// id, name, status, plan_id, config.
// The config column stores a JSON object with string keys and values.
type SQLStore struct {
	db      *sql.DB
	table   string
	dialect SQLDialect
}

// SQLStoreOption configures SQLStore.
type SQLStoreOption func(*SQLStore) error

// WithTableName overrides the default tenant metadata table name.
func WithTableName(table string) SQLStoreOption {
	return func(store *SQLStore) error {
		if !isSafeQualifiedIdentifier(table) {
			return fmt.Errorf("%w: %q", ErrInvalidTableName, table)
		}
		store.table = table
		return nil
	}
}

// WithSQLDialect configures SQL placeholder rendering.
func WithSQLDialect(dialect SQLDialect) SQLStoreOption {
	return func(store *SQLStore) error {
		switch dialect {
		case "", SQLDialectMySQL, SQLDialectSQLite, SQLDialectPostgres:
			if dialect == "" {
				store.dialect = SQLDialectMySQL
			} else {
				store.dialect = dialect
			}
			return nil
		default:
			return fmt.Errorf("%w: %s", ErrUnsupportedSQLDialect, dialect)
		}
	}
}

// NewSQLStore creates a SQL-backed tenant metadata store.
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

// Get returns tenant metadata by ID.
func (store *SQLStore) Get(ctx context.Context, id types.TenantID) (types.Tenant, error) {
	if err := ctx.Err(); err != nil {
		return types.Tenant{}, err
	}
	if id == "" {
		return types.Tenant{}, ErrInvalidTenant
	}

	query := fmt.Sprintf("SELECT id, name, status, plan_id, config FROM %s WHERE id = %s", store.table, store.placeholder(1))
	tenant, err := scanTenant(store.db.QueryRowContext(ctx, query, id.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return types.Tenant{}, ErrTenantNotFound
	}
	if err != nil {
		return types.Tenant{}, err
	}
	return tenant, nil
}

// List returns tenants matching filter.
func (store *SQLStore) List(ctx context.Context, filter ListFilter) (tenants []types.Tenant, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := filter.validate(); err != nil {
		return nil, err
	}
	return store.list(ctx, filter, "")
}

// ListPage returns tenants after the cursor while preserving List filtering semantics.
func (store *SQLStore) ListPage(ctx context.Context, filter PageFilter) (tenants []types.Tenant, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := filter.validate(); err != nil {
		return nil, err
	}
	return store.list(ctx, filter.listFilter(), filter.Cursor)
}

func (store *SQLStore) list(ctx context.Context, filter ListFilter, cursor types.TenantID) (tenants []types.Tenant, err error) {
	query := fmt.Sprintf("SELECT id, name, status, plan_id, config FROM %s", store.table)
	where := []string{}
	args := make([]any, 0, len(filter.Statuses)+3)
	if len(filter.Statuses) > 0 {
		where = append(where, "status IN ("+store.placeholders(len(filter.Statuses), len(args)+1)+")")
		for _, status := range filter.Statuses {
			args = append(args, string(status))
		}
	}
	if cursor != "" {
		where = append(where, "id > "+store.placeholder(len(args)+1))
		args = append(args, cursor.String())
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

	tenants = []types.Tenant{}
	for rows.Next() {
		tenant, err := scanTenant(rows)
		if err != nil {
			return nil, err
		}
		tenants = append(tenants, tenant)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return tenants, nil
}

// Create inserts tenant metadata.
func (store *SQLStore) Create(ctx context.Context, tenant types.Tenant) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateTenant(tenant); err != nil {
		return err
	}

	config, err := marshalConfig(tenant.Config)
	if err != nil {
		return err
	}

	query := fmt.Sprintf("INSERT INTO %s (id, name, status, plan_id, config) VALUES (%s)", store.table, store.placeholders(5, 1))
	_, err = store.db.ExecContext(ctx, query, tenant.ID.String(), tenant.Name, string(tenant.Status), tenant.PlanID, config)
	return normalizeCreateError(err)
}

// Update replaces existing tenant metadata.
func (store *SQLStore) Update(ctx context.Context, tenant types.Tenant) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateTenant(tenant); err != nil {
		return err
	}

	config, err := marshalConfig(tenant.Config)
	if err != nil {
		return err
	}

	query := fmt.Sprintf(
		"UPDATE %s SET name = %s, status = %s, plan_id = %s, config = %s WHERE id = %s",
		store.table,
		store.placeholder(1),
		store.placeholder(2),
		store.placeholder(3),
		store.placeholder(4),
		store.placeholder(5),
	)
	result, err := store.db.ExecContext(ctx, query, tenant.Name, string(tenant.Status), tenant.PlanID, config, tenant.ID.String())
	if err != nil {
		return err
	}
	return store.requireUpdatedTenant(ctx, tenant, result)
}

// CompareAndSwap atomically replaces expected tenant metadata with updated.
func (store *SQLStore) CompareAndSwap(ctx context.Context, expected types.Tenant, updated types.Tenant) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateTenant(expected); err != nil {
		return err
	}
	if err := validateTenant(updated); err != nil {
		return err
	}
	if expected.ID != updated.ID {
		return ErrInvalidTenant
	}

	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return normalizeCompareAndSwapError(err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, tx.Rollback())
		}
	}()

	query := fmt.Sprintf("SELECT id, name, status, plan_id, config FROM %s WHERE id = %s", store.table, store.placeholder(1))
	if store.dialect != SQLDialectSQLite {
		query += " FOR UPDATE"
	}
	current, err := scanTenant(tx.QueryRowContext(ctx, query, expected.ID.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrTenantNotFound
	}
	if err != nil {
		return normalizeCompareAndSwapError(err)
	}
	if !tenantsEqual(current, expected) {
		return ErrTenantConflict
	}

	if !tenantsEqual(current, updated) {
		config, marshalErr := marshalConfig(updated.Config)
		if marshalErr != nil {
			return marshalErr
		}
		updateQuery := fmt.Sprintf(
			"UPDATE %s SET name = %s, status = %s, plan_id = %s, config = %s WHERE id = %s",
			store.table,
			store.placeholder(1),
			store.placeholder(2),
			store.placeholder(3),
			store.placeholder(4),
			store.placeholder(5),
		)
		if _, err = tx.ExecContext(ctx, updateQuery, updated.Name, string(updated.Status), updated.PlanID, config, updated.ID.String()); err != nil {
			return normalizeCompareAndSwapError(err)
		}
	}

	if err = tx.Commit(); err != nil {
		return normalizeCompareAndSwapError(err)
	}
	return nil
}

// Delete removes tenant metadata by ID.
func (store *SQLStore) Delete(ctx context.Context, id types.TenantID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if id == "" {
		return ErrInvalidTenant
	}

	query := fmt.Sprintf("DELETE FROM %s WHERE id = %s", store.table, store.placeholder(1))
	result, err := store.db.ExecContext(ctx, query, id.String())
	if err != nil {
		return err
	}
	return requireAffectedRow(result)
}

type tenantScanner interface {
	Scan(dest ...any) error
}

func scanTenant(scanner tenantScanner) (types.Tenant, error) {
	var (
		id     string
		name   string
		status string
		planID string
		config string
	)
	if err := scanner.Scan(&id, &name, &status, &planID, &config); err != nil {
		return types.Tenant{}, err
	}

	decoded, err := unmarshalConfig(config)
	if err != nil {
		return types.Tenant{}, err
	}
	return types.Tenant{
		ID:     types.TenantID(id),
		Name:   name,
		Status: types.TenantStatus(status),
		PlanID: planID,
		Config: decoded,
	}, nil
}

func requireAffectedRow(result sql.Result) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrTenantNotFound
	}
	return nil
}

func (store *SQLStore) requireUpdatedTenant(ctx context.Context, desired types.Tenant, result sql.Result) error {
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
	return confirmUpdatedTenant(current, desired)
}

func confirmUpdatedTenant(current types.Tenant, desired types.Tenant) error {
	if !tenantsEqual(current, desired) {
		return ErrTenantConflict
	}
	return nil
}

func normalizeCompareAndSwapError(err error) error {
	if err == nil {
		return nil
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{
		"deadlock",
		"serialization failure",
		"sqlstate 40001",
		"could not serialize access",
		"database is locked",
		"database table is locked",
		"sqlite_busy",
		"lock wait timeout exceeded",
		"try restarting transaction",
	} {
		if strings.Contains(message, marker) {
			return errors.Join(ErrTenantConflict, err)
		}
	}
	return err
}

func marshalConfig(config map[string]string) (string, error) {
	if config == nil {
		config = map[string]string{}
	}
	data, err := json.Marshal(config)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func unmarshalConfig(raw string) (map[string]string, error) {
	if strings.TrimSpace(raw) == "" {
		return map[string]string{}, nil
	}

	config := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		return nil, err
	}
	return config, nil
}

func (store *SQLStore) placeholders(count int, start int) string {
	if count <= 0 {
		return ""
	}

	parts := make([]string, count)
	for i := range parts {
		parts[i] = store.placeholder(start + i)
	}
	return strings.Join(parts, ", ")
}

func (store *SQLStore) placeholder(index int) string {
	if store.dialect == SQLDialectPostgres {
		return fmt.Sprintf("$%d", index)
	}
	return "?"
}

func isSafeQualifiedIdentifier(value string) bool {
	if value == "" {
		return false
	}

	parts := strings.Split(value, ".")
	for _, part := range parts {
		if !isSafeIdentifier(part) {
			return false
		}
	}
	return true
}

func isSafeIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for i, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_' {
			continue
		}
		if i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}

func normalizeCreateError(err error) error {
	if err == nil {
		return nil
	}
	if isDuplicateKeyError(err) {
		return ErrTenantAlreadyExists
	}
	return err
}

func isDuplicateKeyError(err error) bool {
	for err != nil {
		message := strings.ToLower(err.Error())
		switch {
		case strings.Contains(message, "duplicate") && strings.Contains(message, "key"):
			return true
		case strings.Contains(message, "duplicate") && strings.Contains(message, "entry"):
			return true
		case strings.Contains(message, "unique constraint"):
			return true
		case strings.Contains(message, "constraint failed") && strings.Contains(message, "unique"):
			return true
		case strings.Contains(message, "primary key constraint"):
			return true
		}
		err = errors.Unwrap(err)
	}
	return false
}

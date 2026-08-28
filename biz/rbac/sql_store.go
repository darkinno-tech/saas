package rbac

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/darkinno-tech/saas/core/types"
	"github.com/darkinno-tech/saas/internal/sqlutil"
)

const (
	// DefaultSQLTableName is the default RBAC role table name.
	DefaultSQLTableName = "rbac_roles"
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

var _ Service = (*SQLStore)(nil)
var _ Enforcer = (*SQLStore)(nil)

// SQLStore persists tenant-scoped roles through database/sql.
//
// The table is expected to contain these columns:
// tenant_id, role_key, permissions.
// Use a unique key on (tenant_id, role_key). The permissions column stores a JSON array.
type SQLStore struct {
	db      *sql.DB
	table   string
	dialect SQLDialect
}

// SQLStoreOption configures SQLStore.
type SQLStoreOption func(*SQLStore) error

// WithTableName overrides the default RBAC role table name.
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

// NewSQLStore creates a SQL-backed RBAC store and enforcer.
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

// CreateRole inserts a tenant-scoped role.
func (store *SQLStore) CreateRole(ctx context.Context, role Role) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateRole(role); err != nil {
		return err
	}

	permissions, err := marshalPermissions(role.Permissions)
	if err != nil {
		return err
	}

	query := fmt.Sprintf(
		"INSERT INTO %s (tenant_id, role_key, permissions) VALUES (%s)",
		store.table,
		store.placeholders(3, 1),
	)
	_, err = store.db.ExecContext(ctx, query, role.TenantID.String(), role.Key, permissions)
	return sqlutil.NormalizeDuplicateKeyError(err, ErrRoleExists)
}

// GetRole returns a tenant-scoped role by key.
func (store *SQLStore) GetRole(ctx context.Context, tenantID types.TenantID, key string) (Role, error) {
	if err := ctx.Err(); err != nil {
		return Role{}, err
	}
	if tenantID == "" || key == "" {
		return Role{}, ErrInvalidRole
	}

	query := fmt.Sprintf(
		"SELECT tenant_id, role_key, permissions FROM %s WHERE tenant_id = %s AND role_key = %s",
		store.table,
		store.placeholder(1),
		store.placeholder(2),
	)
	role, err := scanRole(store.db.QueryRowContext(ctx, query, tenantID.String(), key))
	if errors.Is(err, sql.ErrNoRows) {
		return Role{}, ErrRoleNotFound
	}
	if err != nil {
		return Role{}, err
	}
	return role, nil
}

// DeleteRole removes a tenant-scoped role by key.
func (store *SQLStore) DeleteRole(ctx context.Context, tenantID types.TenantID, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if tenantID == "" || key == "" {
		return ErrInvalidRole
	}

	query := fmt.Sprintf("DELETE FROM %s WHERE tenant_id = %s AND role_key = %s", store.table, store.placeholder(1), store.placeholder(2))
	result, err := store.db.ExecContext(ctx, query, tenantID.String(), key)
	if err != nil {
		return err
	}
	return requireAffectedRole(result)
}

// Authorize checks whether roles grant a permission for a tenant.
func (store *SQLStore) Authorize(ctx context.Context, tenantID types.TenantID, roles []string, permission Permission) error {
	return store.Enforce(ctx, tenantID, roles, permission)
}

// Enforce checks whether roles grant a permission for a tenant.
func (store *SQLStore) Enforce(ctx context.Context, tenantID types.TenantID, roles []string, permission Permission) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if tenantID == "" || permission == "" {
		return ErrInvalidRole
	}

	for _, key := range roles {
		if key == "" {
			continue
		}
		role, err := store.GetRole(ctx, tenantID, key)
		if errors.Is(err, ErrRoleNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if role.HasPermission(permission) {
			return nil
		}
	}
	return ErrPermissionDeny
}

type roleScanner interface {
	Scan(dest ...any) error
}

func scanRole(scanner roleScanner) (Role, error) {
	var (
		tenantID    string
		key         string
		permissions string
	)
	if err := scanner.Scan(&tenantID, &key, &permissions); err != nil {
		return Role{}, err
	}

	decoded, err := unmarshalPermissions(permissions)
	if err != nil {
		return Role{}, err
	}
	role := Role{TenantID: types.TenantID(tenantID), Key: key, Permissions: decoded}
	if err := validateRole(role); err != nil {
		return Role{}, err
	}
	return role, nil
}

func marshalPermissions(permissions []Permission) (string, error) {
	if permissions == nil {
		permissions = []Permission{}
	}
	data, err := json.Marshal(permissions)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func unmarshalPermissions(raw string) ([]Permission, error) {
	if raw == "" {
		return []Permission{}, nil
	}
	permissions := []Permission{}
	if err := json.Unmarshal([]byte(raw), &permissions); err != nil {
		return nil, err
	}
	return permissions, nil
}

func requireAffectedRole(result sql.Result) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrRoleNotFound
	}
	return nil
}

func (store *SQLStore) placeholders(count int, start int) string {
	return sqlutil.Placeholders(store.dialect, count, start)
}

func (store *SQLStore) placeholder(index int) string {
	return sqlutil.Placeholder(store.dialect, index)
}

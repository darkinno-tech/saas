package user

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
	// DefaultSQLUsersTableName is the default user table name.
	DefaultSQLUsersTableName = "biz_users"

	// DefaultSQLMembersTableName is the default tenant member table name.
	DefaultSQLMembersTableName = "biz_members"
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
var _ PagedService = (*SQLStore)(nil)

// SQLStore persists users and tenant memberships through database/sql.
//
// The users table is expected to contain: id, email, name.
// The members table is expected to contain: tenant_id, user_id, roles.
// Use a unique key on members (tenant_id, user_id). The roles column stores a JSON array.
type SQLStore struct {
	db           *sql.DB
	usersTable   string
	membersTable string
	dialect      SQLDialect
}

// SQLStoreOption configures SQLStore.
type SQLStoreOption func(*SQLStore) error

// WithUsersTableName overrides the default user table name.
func WithUsersTableName(table string) SQLStoreOption {
	return func(store *SQLStore) error {
		if !sqlutil.IsSafeQualifiedIdentifier(table) {
			return fmt.Errorf("%w: %q", ErrInvalidTableName, table)
		}
		store.usersTable = table
		return nil
	}
}

// WithMembersTableName overrides the default tenant member table name.
func WithMembersTableName(table string) SQLStoreOption {
	return func(store *SQLStore) error {
		if !sqlutil.IsSafeQualifiedIdentifier(table) {
			return fmt.Errorf("%w: %q", ErrInvalidTableName, table)
		}
		store.membersTable = table
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

// NewSQLStore creates a SQL-backed user service.
func NewSQLStore(db *sql.DB, opts ...SQLStoreOption) (*SQLStore, error) {
	if db == nil {
		return nil, ErrNilDB
	}

	store := &SQLStore{
		db:           db,
		usersTable:   DefaultSQLUsersTableName,
		membersTable: DefaultSQLMembersTableName,
		dialect:      SQLDialectMySQL,
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

// CreateUser inserts a user.
func (store *SQLStore) CreateUser(ctx context.Context, user User) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if user.ID == "" || user.Email == "" {
		return ErrInvalidUser
	}

	query := fmt.Sprintf(
		"INSERT INTO %s (id, email, name) VALUES (%s)",
		store.usersTable,
		store.placeholders(3, 1),
	)
	_, err := store.db.ExecContext(ctx, query, user.ID, user.Email, user.Name)
	return sqlutil.NormalizeDuplicateKeyError(err, ErrUserExists)
}

// GetUser returns a user by ID.
func (store *SQLStore) GetUser(ctx context.Context, id string) (User, error) {
	if err := ctx.Err(); err != nil {
		return User{}, err
	}
	if id == "" {
		return User{}, ErrInvalidUser
	}

	query := fmt.Sprintf("SELECT id, email, name FROM %s WHERE id = %s", store.usersTable, store.placeholder(1))
	user, err := scanUser(store.db.QueryRowContext(ctx, query, id))
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrUserNotFound
	}
	if err != nil {
		return User{}, err
	}
	return user, nil
}

// AddMember inserts a tenant membership.
func (store *SQLStore) AddMember(ctx context.Context, member Member) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if member.TenantID == "" || member.UserID == "" {
		return ErrInvalidUser
	}
	if _, err := store.GetUser(ctx, member.UserID); err != nil {
		return err
	}

	roles, err := marshalRoles(member.Roles)
	if err != nil {
		return err
	}
	query := fmt.Sprintf(
		"INSERT INTO %s (tenant_id, user_id, roles) VALUES (%s)",
		store.membersTable,
		store.placeholders(3, 1),
	)
	_, err = store.db.ExecContext(ctx, query, member.TenantID.String(), member.UserID, roles)
	return sqlutil.NormalizeDuplicateKeyError(err, ErrMemberExists)
}

// GetMember returns a tenant membership by tenant and user ID.
func (store *SQLStore) GetMember(ctx context.Context, tenantID types.TenantID, userID string) (Member, error) {
	if err := ctx.Err(); err != nil {
		return Member{}, err
	}
	if tenantID == "" || userID == "" {
		return Member{}, ErrInvalidUser
	}

	query := fmt.Sprintf(
		"SELECT tenant_id, user_id, roles FROM %s WHERE tenant_id = %s AND user_id = %s",
		store.membersTable,
		store.placeholder(1),
		store.placeholder(2),
	)
	member, err := scanMember(store.db.QueryRowContext(ctx, query, tenantID.String(), userID))
	if errors.Is(err, sql.ErrNoRows) {
		return Member{}, ErrMemberNotFound
	}
	if err != nil {
		return Member{}, err
	}
	return member, nil
}

// ListMembers returns tenant memberships ordered by user ID.
func (store *SQLStore) ListMembers(ctx context.Context, tenantID types.TenantID) (members []Member, err error) {
	return store.ListMembersPage(ctx, tenantID, MemberListFilter{})
}

// ListMembersPage returns a bounded page of tenant memberships ordered by user ID.
func (store *SQLStore) ListMembersPage(ctx context.Context, tenantID types.TenantID, filter MemberListFilter) (members []Member, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if tenantID == "" {
		return nil, ErrInvalidUser
	}
	if err := filter.validate(); err != nil {
		return nil, err
	}

	args := []any{tenantID.String()}
	where := []string{"tenant_id = " + store.placeholder(1)}
	if filter.Cursor != "" {
		where = append(where, "user_id > "+store.placeholder(len(args)+1))
		args = append(args, filter.Cursor)
	}
	query := fmt.Sprintf(
		"SELECT tenant_id, user_id, roles FROM %s WHERE %s ORDER BY user_id",
		store.membersTable,
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

	members = []Member{}
	for rows.Next() {
		member, err := scanMember(rows)
		if err != nil {
			return nil, err
		}
		members = append(members, member)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return members, nil
}

// RemoveMember removes a tenant membership.
func (store *SQLStore) RemoveMember(ctx context.Context, tenantID types.TenantID, userID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if tenantID == "" || userID == "" {
		return ErrInvalidUser
	}

	query := fmt.Sprintf("DELETE FROM %s WHERE tenant_id = %s AND user_id = %s", store.membersTable, store.placeholder(1), store.placeholder(2))
	result, err := store.db.ExecContext(ctx, query, tenantID.String(), userID)
	if err != nil {
		return err
	}
	return requireAffectedMember(result)
}

type userScanner interface {
	Scan(dest ...any) error
}

func scanUser(scanner userScanner) (User, error) {
	var user User
	if err := scanner.Scan(&user.ID, &user.Email, &user.Name); err != nil {
		return User{}, err
	}
	if user.ID == "" || user.Email == "" {
		return User{}, ErrInvalidUser
	}
	return user, nil
}

func scanMember(scanner userScanner) (Member, error) {
	var (
		tenantID string
		userID   string
		roles    string
	)
	if err := scanner.Scan(&tenantID, &userID, &roles); err != nil {
		return Member{}, err
	}
	decoded, err := unmarshalRoles(roles)
	if err != nil {
		return Member{}, err
	}
	member := Member{TenantID: types.TenantID(tenantID), UserID: userID, Roles: decoded}
	if member.TenantID == "" || member.UserID == "" {
		return Member{}, ErrInvalidUser
	}
	return member, nil
}

func marshalRoles(roles []string) (string, error) {
	if roles == nil {
		roles = []string{}
	}
	data, err := json.Marshal(roles)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func unmarshalRoles(raw string) ([]string, error) {
	if raw == "" {
		return []string{}, nil
	}
	roles := []string{}
	if err := json.Unmarshal([]byte(raw), &roles); err != nil {
		return nil, err
	}
	return roles, nil
}

func requireAffectedMember(result sql.Result) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrMemberNotFound
	}
	return nil
}

func (store *SQLStore) placeholders(count int, start int) string {
	return sqlutil.Placeholders(store.dialect, count, start)
}

func (store *SQLStore) placeholder(index int) string {
	return sqlutil.Placeholder(store.dialect, index)
}

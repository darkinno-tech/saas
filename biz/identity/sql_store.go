package identity

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/darkinno-tech/saas/core/types"
	"github.com/darkinno-tech/saas/internal/sqlutil"
)

const (
	// DefaultSQLTableName is the default identity link table name.
	DefaultSQLTableName = "identity_links"
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

// SQLStore persists external identity links through database/sql.
//
// The table is expected to contain these columns:
// tenant_id, provider, subject, user_id, email, name, email_verified, metadata.
// Use a unique key on (tenant_id, provider, subject). The metadata column stores a JSON object.
type SQLStore struct {
	db      *sql.DB
	table   string
	dialect SQLDialect
}

// SQLStoreOption configures SQLStore.
type SQLStoreOption func(*SQLStore) error

// WithTableName overrides the default identity link table name.
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

// NewSQLStore creates a SQL-backed identity link store.
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

// Link creates or refreshes an external identity link.
func (store *SQLStore) Link(ctx context.Context, link Link) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	link = normalizeLink(link)
	if err := link.validate(); err != nil {
		return err
	}

	for attempt := 0; attempt < 2; attempt++ {
		current, err := store.GetByExternal(ctx, link.TenantID, link.Provider, link.Subject)
		switch {
		case err == nil && current.UserID != link.UserID:
			return ErrIdentityConflict
		case err == nil:
			return store.updateLink(ctx, link)
		case errors.Is(err, ErrIdentityNotFound):
			err = store.insertLink(ctx, link)
			if !errors.Is(err, ErrIdentityConflict) || attempt == 1 {
				return err
			}
		default:
			return err
		}
	}
	return ErrIdentityConflict
}

// GetByExternal returns an identity link by external provider subject.
func (store *SQLStore) GetByExternal(ctx context.Context, tenantID types.TenantID, provider ProviderKey, subject string) (Link, error) {
	if err := ctx.Err(); err != nil {
		return Link{}, err
	}
	provider = ProviderKey(strings.TrimSpace(string(provider)))
	subject = strings.TrimSpace(subject)
	if tenantID == "" || provider == "" || subject == "" {
		return Link{}, ErrInvalidIdentity
	}

	query := fmt.Sprintf(
		"SELECT tenant_id, provider, subject, user_id, email, name, email_verified, metadata FROM %s WHERE tenant_id = %s AND provider = %s AND subject = %s",
		store.table,
		store.placeholder(1),
		store.placeholder(2),
		store.placeholder(3),
	)
	link, err := scanLink(store.db.QueryRowContext(ctx, query, tenantID.String(), string(provider), subject))
	if errors.Is(err, sql.ErrNoRows) {
		return Link{}, ErrIdentityNotFound
	}
	if err != nil {
		return Link{}, err
	}
	return link, nil
}

// GetByUser returns identity links for a tenant user ordered by provider and subject.
func (store *SQLStore) GetByUser(ctx context.Context, tenantID types.TenantID, userID string) (links []Link, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	userID = strings.TrimSpace(userID)
	if tenantID == "" || userID == "" {
		return nil, ErrInvalidIdentity
	}

	query := fmt.Sprintf(
		"SELECT tenant_id, provider, subject, user_id, email, name, email_verified, metadata FROM %s WHERE tenant_id = %s AND user_id = %s ORDER BY provider, subject",
		store.table,
		store.placeholder(1),
		store.placeholder(2),
	)
	rows, err := store.db.QueryContext(ctx, query, tenantID.String(), userID)
	if err != nil {
		return nil, err
	}
	defer func() {
		err = errors.Join(err, rows.Close())
	}()

	links = []Link{}
	for rows.Next() {
		link, err := scanLink(rows)
		if err != nil {
			return nil, err
		}
		links = append(links, link)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return links, nil
}

// Unlink removes an identity link by external provider subject.
func (store *SQLStore) Unlink(ctx context.Context, tenantID types.TenantID, provider ProviderKey, subject string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	provider = ProviderKey(strings.TrimSpace(string(provider)))
	subject = strings.TrimSpace(subject)
	if tenantID == "" || provider == "" || subject == "" {
		return ErrInvalidIdentity
	}

	query := fmt.Sprintf(
		"DELETE FROM %s WHERE tenant_id = %s AND provider = %s AND subject = %s",
		store.table,
		store.placeholder(1),
		store.placeholder(2),
		store.placeholder(3),
	)
	result, err := store.db.ExecContext(ctx, query, tenantID.String(), string(provider), subject)
	if err != nil {
		return err
	}
	return requireAffectedLink(result)
}

func (store *SQLStore) insertLink(ctx context.Context, link Link) error {
	metadata, err := sqlutil.MarshalStringMap(link.Metadata)
	if err != nil {
		return err
	}
	query := fmt.Sprintf(
		"INSERT INTO %s (tenant_id, provider, subject, user_id, email, name, email_verified, metadata) VALUES (%s)",
		store.table,
		store.placeholders(8, 1),
	)
	_, err = store.db.ExecContext(ctx, query, link.TenantID.String(), string(link.Provider), link.Subject, link.UserID, link.Email, link.Name, link.EmailVerified, metadata)
	return sqlutil.NormalizeDuplicateKeyError(err, ErrIdentityConflict)
}

func (store *SQLStore) updateLink(ctx context.Context, link Link) error {
	metadata, err := sqlutil.MarshalStringMap(link.Metadata)
	if err != nil {
		return err
	}
	query := fmt.Sprintf(
		"UPDATE %s SET email = %s, name = %s, email_verified = %s, metadata = %s WHERE tenant_id = %s AND provider = %s AND subject = %s AND user_id = %s",
		store.table,
		store.placeholder(1),
		store.placeholder(2),
		store.placeholder(3),
		store.placeholder(4),
		store.placeholder(5),
		store.placeholder(6),
		store.placeholder(7),
		store.placeholder(8),
	)
	result, err := store.db.ExecContext(ctx, query, link.Email, link.Name, link.EmailVerified, metadata, link.TenantID.String(), string(link.Provider), link.Subject, link.UserID)
	if err != nil {
		return err
	}
	return store.requireUpdatedLink(ctx, link, result)
}

type linkScanner interface {
	Scan(dest ...any) error
}

func scanLink(scanner linkScanner) (Link, error) {
	var (
		tenantID      string
		provider      string
		subject       string
		userID        string
		email         string
		name          string
		emailVerified bool
		metadata      string
	)
	if err := scanner.Scan(&tenantID, &provider, &subject, &userID, &email, &name, &emailVerified, &metadata); err != nil {
		return Link{}, err
	}
	decoded, err := sqlutil.UnmarshalStringMap(metadata)
	if err != nil {
		return Link{}, err
	}
	link := Link{
		TenantID:      types.TenantID(tenantID),
		Provider:      ProviderKey(provider),
		Subject:       subject,
		UserID:        userID,
		Email:         email,
		Name:          name,
		EmailVerified: emailVerified,
		Metadata:      decoded,
	}
	if err := link.validate(); err != nil {
		return Link{}, err
	}
	return link, nil
}

func normalizeLink(link Link) Link {
	link.Provider = ProviderKey(strings.TrimSpace(string(link.Provider)))
	link.Subject = strings.TrimSpace(link.Subject)
	link.UserID = strings.TrimSpace(link.UserID)
	link.Email = strings.TrimSpace(link.Email)
	link.Name = strings.TrimSpace(link.Name)
	link.Metadata = cloneStringMap(link.Metadata)
	return link
}

func requireAffectedLink(result sql.Result) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrIdentityNotFound
	}
	return nil
}

func (store *SQLStore) requireUpdatedLink(ctx context.Context, expected Link, result sql.Result) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected > 0 {
		return nil
	}
	current, err := store.GetByExternal(ctx, expected.TenantID, expected.Provider, expected.Subject)
	if err != nil {
		return err
	}
	return confirmUpdatedLink(current, expected)
}

func confirmUpdatedLink(current Link, expected Link) error {
	if !linksEqual(current, expected) {
		return ErrIdentityConflict
	}
	return nil
}

func (store *SQLStore) placeholders(count int, start int) string {
	return sqlutil.Placeholders(store.dialect, count, start)
}

func (store *SQLStore) placeholder(index int) string {
	return sqlutil.Placeholder(store.dialect, index)
}

package oidc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/im10furry/saas/core/types"
	"github.com/im10furry/saas/internal/sqlutil"
)

const (
	// DefaultSQLLoginTableName is the default OIDC login state table name.
	DefaultSQLLoginTableName = "oidc_logins"

	sqlLoginConsumeMaxAttempts = 16
)

// SQLDialect controls SQL placeholder rendering for SQLLoginStore.
type SQLDialect = sqlutil.Dialect

const (
	// SQLDialectMySQL uses question-mark placeholders and is the default.
	SQLDialectMySQL = sqlutil.DialectMySQL

	// SQLDialectSQLite uses question-mark placeholders.
	SQLDialectSQLite = sqlutil.DialectSQLite

	// SQLDialectPostgres uses numbered placeholders.
	SQLDialectPostgres = sqlutil.DialectPostgres
)

var _ LoginStore = (*SQLLoginStore)(nil)

// SQLLoginStore persists one-time OIDC login state through database/sql.
//
// The table is expected to contain these columns:
// state, auth_url, nonce, pkce_verifier, tenant_id, user_id, roles, expires_at.
// Use a unique key on state. The roles column stores a JSON array.
type SQLLoginStore struct {
	db      *sql.DB
	table   string
	dialect SQLDialect
	now     func() time.Time
	ttl     time.Duration
}

// SQLLoginStoreOption configures SQLLoginStore.
type SQLLoginStoreOption func(*SQLLoginStore) error

// WithLoginTableName overrides the default login state table name.
func WithLoginTableName(table string) SQLLoginStoreOption {
	return func(store *SQLLoginStore) error {
		if !sqlutil.IsSafeQualifiedIdentifier(table) {
			return fmt.Errorf("%w: %q", ErrInvalidTableName, table)
		}
		store.table = table
		return nil
	}
}

// WithSQLDialect configures SQL placeholder rendering.
func WithSQLDialect(dialect SQLDialect) SQLLoginStoreOption {
	return func(store *SQLLoginStore) error {
		normalized, ok := sqlutil.NormalizeDialect(dialect)
		if !ok {
			return fmt.Errorf("%w: %s", ErrUnsupportedSQLDialect, dialect)
		}
		store.dialect = normalized
		return nil
	}
}

// WithLoginTTL configures the default expiration when Login.ExpiresAt is empty.
func WithLoginTTL(ttl time.Duration) SQLLoginStoreOption {
	return func(store *SQLLoginStore) error {
		if ttl > 0 {
			store.ttl = ttl
		}
		return nil
	}
}

// NewSQLLoginStore creates a SQL-backed OIDC login store.
func NewSQLLoginStore(db *sql.DB, opts ...SQLLoginStoreOption) (*SQLLoginStore, error) {
	if db == nil {
		return nil, ErrNilDB
	}

	store := &SQLLoginStore{
		db:      db,
		table:   DefaultSQLLoginTableName,
		dialect: SQLDialectMySQL,
		now:     time.Now,
		ttl:     DefaultLoginTTL,
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

// SaveLogin saves a one-time login state.
func (store *SQLLoginStore) SaveLogin(ctx context.Context, login Login) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if store == nil || login.State == "" || login.Nonce == "" || login.PKCEVerifier == "" || login.TenantID == "" {
		return ErrInvalidConfig
	}

	now := store.currentTime()
	if login.ExpiresAt.IsZero() {
		login.ExpiresAt = now.Add(store.effectiveTTL())
	}
	if !login.ExpiresAt.After(now) {
		return ErrLoginExpired
	}
	login.UserID = strings.TrimSpace(login.UserID)
	login.Roles = cloneStrings(login.Roles)

	roles, err := marshalLoginRoles(login.Roles)
	if err != nil {
		return err
	}
	query := fmt.Sprintf(
		"INSERT INTO %s (state, auth_url, nonce, pkce_verifier, tenant_id, user_id, roles, expires_at) VALUES (%s)",
		store.table,
		store.placeholders(8, 1),
	)
	_, err = store.db.ExecContext(ctx, query, login.State, login.URL, login.Nonce, login.PKCEVerifier, login.TenantID.String(), nullableLoginString(login.UserID), roles, login.ExpiresAt)
	return sqlutil.NormalizeDuplicateKeyError(err, ErrDuplicateParam)
}

// ConsumeLogin atomically loads and removes a login state.
func (store *SQLLoginStore) ConsumeLogin(ctx context.Context, state string) (login Login, err error) {
	if err := ctx.Err(); err != nil {
		return Login{}, err
	}
	if store == nil || state == "" {
		return Login{}, ErrInvalidCallback
	}

	return retrySQLLoginConsume(ctx, func() (Login, error) {
		return store.consumeLoginOnce(ctx, state)
	}, waitForSQLLoginConsumeRetry)
}

func retrySQLLoginConsume(ctx context.Context, consume func() (Login, error), wait func(context.Context, int) error) (Login, error) {
	var (
		login Login
		err   error
	)
	for attempt := 0; attempt < sqlLoginConsumeMaxAttempts; attempt++ {
		login, err = consume()
		if !sqlutil.IsRetryableTransactionError(err) {
			return login, err
		}
		if attempt == sqlLoginConsumeMaxAttempts-1 {
			return login, err
		}
		if err := wait(ctx, attempt); err != nil {
			return Login{}, err
		}
	}
	return login, err
}

func waitForSQLLoginConsumeRetry(ctx context.Context, attempt int) error {
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

func (store *SQLLoginStore) consumeLoginOnce(ctx context.Context, state string) (login Login, err error) {

	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return Login{}, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, tx.Rollback())
		}
	}()

	login, err = store.getLoginForUpdate(ctx, tx, state)
	if err != nil {
		return Login{}, err
	}
	if err := store.deleteLogin(ctx, tx, state); err != nil {
		return Login{}, err
	}
	if err = tx.Commit(); err != nil {
		return Login{}, err
	}
	if !login.ExpiresAt.After(store.currentTime()) {
		return Login{}, ErrLoginExpired
	}
	login.Roles = cloneStrings(login.Roles)
	return login, nil
}

func (store *SQLLoginStore) getLoginForUpdate(ctx context.Context, tx *sql.Tx, state string) (Login, error) {
	query := fmt.Sprintf(
		"SELECT state, auth_url, nonce, pkce_verifier, tenant_id, user_id, roles, expires_at FROM %s WHERE state = %s",
		store.table,
		store.placeholder(1),
	)
	if store.dialect != SQLDialectSQLite {
		query += " FOR UPDATE"
	}

	login, err := scanLogin(tx.QueryRowContext(ctx, query, state))
	if errors.Is(err, sql.ErrNoRows) {
		return Login{}, ErrLoginNotFound
	}
	if err != nil {
		return Login{}, err
	}
	return login, nil
}

func (store *SQLLoginStore) deleteLogin(ctx context.Context, tx *sql.Tx, state string) error {
	query := fmt.Sprintf("DELETE FROM %s WHERE state = %s", store.table, store.placeholder(1))
	result, err := tx.ExecContext(ctx, query, state)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrLoginNotFound
	}
	return nil
}

type loginScanner interface {
	Scan(dest ...any) error
}

func scanLogin(scanner loginScanner) (Login, error) {
	var (
		state        string
		authURL      string
		nonce        string
		pkceVerifier string
		tenantID     string
		userID       sql.NullString
		roles        string
		expiresAt    time.Time
	)
	if err := scanner.Scan(&state, &authURL, &nonce, &pkceVerifier, &tenantID, &userID, &roles, &expiresAt); err != nil {
		return Login{}, err
	}
	decodedRoles, err := unmarshalLoginRoles(roles)
	if err != nil {
		return Login{}, err
	}
	login := Login{
		AuthRequest: AuthRequest{
			URL:          authURL,
			State:        state,
			Nonce:        nonce,
			PKCEVerifier: pkceVerifier,
		},
		TenantID:  types.TenantID(tenantID),
		UserID:    userID.String,
		Roles:     decodedRoles,
		ExpiresAt: expiresAt,
	}
	if login.State == "" || login.Nonce == "" || login.PKCEVerifier == "" || login.TenantID == "" || login.ExpiresAt.IsZero() {
		return Login{}, ErrInvalidConfig
	}
	return login, nil
}

func marshalLoginRoles(roles []string) (string, error) {
	if roles == nil {
		roles = []string{}
	}
	data, err := json.Marshal(roles)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func unmarshalLoginRoles(raw string) ([]string, error) {
	if raw == "" {
		return []string{}, nil
	}
	roles := []string{}
	if err := json.Unmarshal([]byte(raw), &roles); err != nil {
		return nil, err
	}
	return roles, nil
}

func nullableLoginString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (store *SQLLoginStore) currentTime() time.Time {
	if store.now == nil {
		return time.Now()
	}
	return store.now()
}

func (store *SQLLoginStore) effectiveTTL() time.Duration {
	if store.ttl <= 0 {
		return DefaultLoginTTL
	}
	return store.ttl
}

func (store *SQLLoginStore) placeholders(count int, start int) string {
	return sqlutil.Placeholders(store.dialect, count, start)
}

func (store *SQLLoginStore) placeholder(index int) string {
	return sqlutil.Placeholder(store.dialect, index)
}

package oidc

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

const oidcLoginColumns = "state, auth_url, nonce, pkce_verifier, tenant_id, user_id, roles, expires_at"

func newOIDCMockLoginStore(t *testing.T) (*SQLLoginStore, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := NewSQLLoginStore(db)
	if err != nil {
		t.Fatalf("NewSQLLoginStore() error = %v", err)
	}
	return store, mock
}

func assertOIDCMock(t *testing.T, mock sqlmock.Sqlmock) {
	t.Helper()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sqlmock expectations: %v", err)
	}
}

func oidcLoginRow(expiresAt time.Time) *sqlmock.Rows {
	return sqlmock.NewRows([]string{"state", "auth_url", "nonce", "pkce_verifier", "tenant_id", "user_id", "roles", "expires_at"}).
		AddRow("state", "https://issuer.example.com/authorize", "nonce", "verifier", "tenant-a",
			sql.NullString{String: "u1", Valid: true}, `["member"]`, expiresAt)
}

func newOIDCLogin(expiresAt time.Time) Login {
	return Login{
		AuthRequest: AuthRequest{
			URL:          "https://issuer.example.com/authorize",
			State:        "state",
			Nonce:        "nonce",
			PKCEVerifier: "verifier",
		},
		TenantID:  "tenant-a",
		UserID:    "u1",
		Roles:     []string{"member"},
		ExpiresAt: expiresAt,
	}
}

const oidcInsertSQL = "INSERT INTO oidc_logins (state, auth_url, nonce, pkce_verifier, tenant_id, user_id, roles, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)"

func TestOIDCSQLLoginStoreSaveLoginSuccess(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newOIDCMockLoginStore(t)
	now := time.Now().Truncate(time.Millisecond).UTC()
	store.now = func() time.Time { return now }
	login := newOIDCLogin(now.Add(time.Minute))

	ins := mock.ExpectExec(regexp.QuoteMeta(oidcInsertSQL))
	ins.WithArgs("state", "https://issuer.example.com/authorize", "nonce", "verifier", "tenant-a", "u1", `["member"]`, login.ExpiresAt).
		WillReturnResult(sqlmock.NewResult(1, 1))
	if err := store.SaveLogin(ctx, login); err != nil {
		t.Fatalf("SaveLogin() error = %v", err)
	}
	assertOIDCMock(t, mock)
}

func TestOIDCSQLLoginStoreSaveLoginDefaultsExpiryAndNilUser(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newOIDCMockLoginStore(t)
	now := time.Now().Truncate(time.Millisecond).UTC()
	store.now = func() time.Time { return now }
	login := Login{
		AuthRequest: AuthRequest{URL: "https://issuer.example.com/authorize", State: "state2", Nonce: "nonce2", PKCEVerifier: "verifier2"},
		TenantID:    "tenant-b",
	}

	wantExpires := now.Add(10 * time.Minute)
	ins := mock.ExpectExec(regexp.QuoteMeta(oidcInsertSQL))
	ins.WithArgs("state2", "https://issuer.example.com/authorize", "nonce2", "verifier2", "tenant-b", nil, `[]`, wantExpires).
		WillReturnResult(sqlmock.NewResult(1, 1))
	if err := store.SaveLogin(ctx, login); err != nil {
		t.Fatalf("SaveLogin(default ttl) error = %v", err)
	}
	assertOIDCMock(t, mock)
}

func TestOIDCSQLLoginStoreSaveLoginDuplicateParam(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newOIDCMockLoginStore(t)
	now := time.Now().Truncate(time.Millisecond).UTC()
	store.now = func() time.Time { return now }
	login := newOIDCLogin(now.Add(time.Minute))
	dupErr := errors.New("Error 1062: Duplicate entry 'state' for key 'PRIMARY'")

	ins := mock.ExpectExec(regexp.QuoteMeta(oidcInsertSQL))
	ins.WithArgs("state", "https://issuer.example.com/authorize", "nonce", "verifier", "tenant-a", "u1", `["member"]`, login.ExpiresAt).
		WillReturnError(dupErr)
	if err := store.SaveLogin(ctx, login); !errors.Is(err, ErrDuplicateParam) {
		t.Fatalf("SaveLogin(duplicate) error = %v, want ErrDuplicateParam", err)
	}
	assertOIDCMock(t, mock)
}

func TestOIDCSQLLoginStoreSaveLoginExecError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newOIDCMockLoginStore(t)
	now := time.Now().Truncate(time.Millisecond).UTC()
	store.now = func() time.Time { return now }
	login := newOIDCLogin(now.Add(time.Minute))
	boom := errors.New("boom")

	ins := mock.ExpectExec(regexp.QuoteMeta(oidcInsertSQL))
	ins.WithArgs("state", "https://issuer.example.com/authorize", "nonce", "verifier", "tenant-a", "u1", `["member"]`, login.ExpiresAt).
		WillReturnError(boom)
	if err := store.SaveLogin(ctx, login); !errors.Is(err, boom) {
		t.Fatalf("SaveLogin(exec error) error = %v, want boom", err)
	}
	assertOIDCMock(t, mock)
}

func TestOIDCSQLLoginStoreSaveLoginRejectsInvalid(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newOIDCMockLoginStore(t)
	now := time.Now().Truncate(time.Millisecond).UTC()
	store.now = func() time.Time { return now }

	if err := store.SaveLogin(ctx, Login{}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("SaveLogin(empty) error = %v, want ErrInvalidConfig", err)
	}
	expired := newOIDCLogin(now.Add(-time.Minute))
	if err := store.SaveLogin(ctx, expired); !errors.Is(err, ErrLoginExpired) {
		t.Fatalf("SaveLogin(expired) error = %v, want ErrLoginExpired", err)
	}
	assertOIDCMock(t, mock)
}

func TestOIDCSQLLoginStoreSaveLoginHonorsCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store, mock := newOIDCMockLoginStore(t)
	login := newOIDCLogin(time.Now().Add(time.Minute))
	if err := store.SaveLogin(ctx, login); !errors.Is(err, context.Canceled) {
		t.Fatalf("SaveLogin(cancelled) error = %v, want context.Canceled", err)
	}
	assertOIDCMock(t, mock)
}

func TestOIDCSQLLoginStoreConsumeLoginSuccess(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newOIDCMockLoginStore(t)
	now := time.Now().Truncate(time.Millisecond).UTC()
	store.now = func() time.Time { return now }
	expiresAt := now.Add(time.Minute)

	mock.ExpectBegin()
	get := mock.ExpectQuery(regexp.QuoteMeta("SELECT " + oidcLoginColumns + " FROM oidc_logins WHERE state = ? FOR UPDATE"))
	get.WithArgs("state").WillReturnRows(oidcLoginRow(expiresAt))
	del := mock.ExpectExec(regexp.QuoteMeta("DELETE FROM oidc_logins WHERE state = ?"))
	del.WithArgs("state").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	login, err := store.ConsumeLogin(ctx, "state")
	if err != nil {
		t.Fatalf("ConsumeLogin() error = %v", err)
	}
	if login.State != "state" || login.TenantID != "tenant-a" || login.UserID != "u1" || !reflect.DeepEqual(login.Roles, []string{"member"}) {
		t.Fatalf("ConsumeLogin() = %+v, want decoded login", login)
	}
	assertOIDCMock(t, mock)
}

func TestOIDCSQLLoginStoreConsumeLoginExpiredAfterCommit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newOIDCMockLoginStore(t)
	now := time.Now().Truncate(time.Millisecond).UTC()
	store.now = func() time.Time { return now }
	expiresAt := now.Add(-time.Minute)

	mock.ExpectBegin()
	get := mock.ExpectQuery(regexp.QuoteMeta("SELECT " + oidcLoginColumns + " FROM oidc_logins WHERE state = ? FOR UPDATE"))
	get.WithArgs("state").WillReturnRows(oidcLoginRow(expiresAt))
	del := mock.ExpectExec(regexp.QuoteMeta("DELETE FROM oidc_logins WHERE state = ?"))
	del.WithArgs("state").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	_, err := store.ConsumeLogin(ctx, "state")
	if !errors.Is(err, ErrLoginExpired) {
		t.Fatalf("ConsumeLogin(expired) error = %v, want ErrLoginExpired", err)
	}
}

func TestOIDCSQLLoginStoreConsumeLoginNotFound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newOIDCMockLoginStore(t)

	mock.ExpectBegin()
	get := mock.ExpectQuery(regexp.QuoteMeta("SELECT " + oidcLoginColumns + " FROM oidc_logins WHERE state = ? FOR UPDATE"))
	get.WithArgs("state").WillReturnRows(sqlmock.NewRows([]string{"state"}))
	mock.ExpectRollback()

	if _, err := store.ConsumeLogin(ctx, "state"); !errors.Is(err, ErrLoginNotFound) {
		t.Fatalf("ConsumeLogin(not found) error = %v, want ErrLoginNotFound", err)
	}
	assertOIDCMock(t, mock)
}

func TestOIDCSQLLoginStoreConsumeLoginQueryError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newOIDCMockLoginStore(t)
	boom := errors.New("boom")

	mock.ExpectBegin()
	get := mock.ExpectQuery(regexp.QuoteMeta("SELECT " + oidcLoginColumns + " FROM oidc_logins WHERE state = ? FOR UPDATE"))
	get.WithArgs("state").WillReturnError(boom)
	mock.ExpectRollback()

	if _, err := store.ConsumeLogin(ctx, "state"); !errors.Is(err, boom) {
		t.Fatalf("ConsumeLogin(query error) error = %v, want boom", err)
	}
	assertOIDCMock(t, mock)
}

func TestOIDCSQLLoginStoreConsumeLoginBeginError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newOIDCMockLoginStore(t)
	boom := errors.New("boom")

	mock.ExpectBegin().WillReturnError(boom)
	if _, err := store.ConsumeLogin(ctx, "state"); !errors.Is(err, boom) {
		t.Fatalf("ConsumeLogin(begin error) error = %v, want boom", err)
	}
	assertOIDCMock(t, mock)
}

func TestOIDCSQLLoginStoreConsumeLoginRejectsInvalid(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newOIDCMockLoginStore(t)
	if _, err := store.ConsumeLogin(ctx, ""); !errors.Is(err, ErrInvalidCallback) {
		t.Fatalf("ConsumeLogin(empty state) error = %v, want ErrInvalidCallback", err)
	}
	assertOIDCMock(t, mock)
}

func TestOIDCSQLLoginStoreConsumeLoginHonorsCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store, mock := newOIDCMockLoginStore(t)
	if _, err := store.ConsumeLogin(ctx, "state"); !errors.Is(err, context.Canceled) {
		t.Fatalf("ConsumeLogin(cancelled) error = %v, want context.Canceled", err)
	}
	assertOIDCMock(t, mock)
}

func TestOIDCSQLLoginStoreConsumeLoginSqliteOmitsForUpdate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := NewSQLLoginStore(db, WithSQLDialect(SQLDialectSQLite))
	if err != nil {
		t.Fatalf("NewSQLLoginStore() error = %v", err)
	}
	now := time.Now().Truncate(time.Millisecond).UTC()
	store.now = func() time.Time { return now }
	expiresAt := now.Add(time.Minute)

	// sqlite variant omits FOR UPDATE on the SELECT.
	mock.ExpectBegin()
	get := mock.ExpectQuery(regexp.QuoteMeta("SELECT " + oidcLoginColumns + " FROM oidc_logins WHERE state = ?"))
	get.WithArgs("state").WillReturnRows(oidcLoginRow(expiresAt))
	del := mock.ExpectExec(regexp.QuoteMeta("DELETE FROM oidc_logins WHERE state = ?"))
	del.WithArgs("state").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	login, err := store.ConsumeLogin(ctx, "state")
	if err != nil {
		t.Fatalf("ConsumeLogin(sqlite) error = %v", err)
	}
	if login.State != "state" {
		t.Fatalf("ConsumeLogin(sqlite) = %+v, want state", login)
	}
	assertOIDCMock(t, mock)
}

func TestOIDCSQLLoginStoreDeleteLoginNotFound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newOIDCMockLoginStore(t)
	expiresAt := time.Now().Add(time.Minute)

	mock.ExpectBegin()
	get := mock.ExpectQuery(regexp.QuoteMeta("SELECT " + oidcLoginColumns + " FROM oidc_logins WHERE state = ? FOR UPDATE"))
	get.WithArgs("state").WillReturnRows(oidcLoginRow(expiresAt))
	del := mock.ExpectExec(regexp.QuoteMeta("DELETE FROM oidc_logins WHERE state = ?"))
	del.WithArgs("state").WillReturnResult(sqlmock.NewResult(0, 0))

	if _, err := store.ConsumeLogin(ctx, "state"); !errors.Is(err, ErrLoginNotFound) {
		t.Fatalf("ConsumeLogin(delete 0 rows) error = %v, want ErrLoginNotFound", err)
	}
	// deleteLogin's affected==0 return bypasses commit and the deferred rollback
	// (the named return err is nil at that point), so no commit/rollback expected.
	mock.ExpectationsWereMet()
}

func TestOIDCSQLLoginStoreScanLoginErrorBranches(t *testing.T) {
	t.Parallel()
	scanErr := errors.New("scan failed")
	if _, err := scanLogin(loginScannerFunc(func(...any) error { return scanErr })); !errors.Is(err, scanErr) {
		t.Fatalf("scanLogin(scan error) error = %v, want scan failed", err)
	}
	expiresAt := time.Now().Add(time.Minute)
	if _, err := scanLogin(loginScannerFunc(func(dest ...any) error {
		*(dest[0].(*string)) = "state"
		*(dest[1].(*string)) = "url"
		*(dest[2].(*string)) = "nonce"
		*(dest[3].(*string)) = "verifier"
		*(dest[4].(*string)) = "tenant-a"
		*(dest[5].(*sql.NullString)) = sql.NullString{String: "u1", Valid: true}
		*(dest[6].(*string)) = "not-json"
		*(dest[7].(*time.Time)) = expiresAt
		return nil
	})); err == nil {
		t.Fatal("scanLogin(bad roles) error = nil, want unmarshal error")
	}
	if _, err := scanLogin(loginScannerFunc(func(dest ...any) error {
		*(dest[0].(*string)) = ""
		*(dest[1].(*string)) = "url"
		*(dest[2].(*string)) = "nonce"
		*(dest[3].(*string)) = "verifier"
		*(dest[4].(*string)) = "tenant-a"
		*(dest[5].(*sql.NullString)) = sql.NullString{}
		*(dest[6].(*string)) = "[]"
		*(dest[7].(*time.Time)) = expiresAt
		return nil
	})); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("scanLogin(empty state) error = %v, want ErrInvalidConfig", err)
	}
}
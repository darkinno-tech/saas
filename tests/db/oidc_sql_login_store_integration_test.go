package db_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/im10furry/saas/biz/identity/oidc"
)

func TestOIDCSQLLoginStoreMySQLIntegration(t *testing.T) {
	runOIDCSQLLoginStoreIntegration(t, "mysql", os.Getenv("SAAS_MYSQL_DSN"), resetMySQLOIDCLoginsTable, func(db *sql.DB) (*oidc.SQLLoginStore, error) {
		return oidc.NewSQLLoginStore(db)
	})
}

func TestOIDCSQLLoginStorePostgresIntegration(t *testing.T) {
	runOIDCSQLLoginStoreIntegration(t, "postgres", os.Getenv("SAAS_POSTGRES_DSN"), resetPostgresOIDCLoginsTable, func(db *sql.DB) (*oidc.SQLLoginStore, error) {
		return oidc.NewSQLLoginStore(db, oidc.WithSQLDialect(oidc.SQLDialectPostgres))
	})
}

func runOIDCSQLLoginStoreIntegration(t *testing.T, driver, dsn string, reset func(*testing.T, context.Context, *sql.DB), newStore func(*sql.DB) (*oidc.SQLLoginStore, error)) {
	t.Helper()
	if dsn == "" {
		t.Skipf("set the %s DSN to run OIDC SQL login integration tests", driver)
	}

	db, err := sql.Open(driver, dsn)
	if err != nil {
		t.Fatalf("sql.Open(%s) error = %v", driver, err)
	}
	db.SetMaxOpenConns(32)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("db.Close() error = %v", err)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := pingUntilReady(ctx, db); err != nil {
		t.Fatalf("%s not ready: %v", driver, err)
	}
	reset(t, ctx, db)

	store, err := newStore(db)
	if err != nil {
		t.Fatalf("NewSQLLoginStore() error = %v", err)
	}
	runOIDCSQLLoginStoreContract(t, ctx, driver, db, store)
}

func runOIDCSQLLoginStoreContract(t *testing.T, ctx context.Context, driver string, db *sql.DB, store *oidc.SQLLoginStore) {
	t.Helper()
	expiresAt := time.Now().Add(time.Hour).UTC().Round(time.Microsecond)
	login := oidc.Login{
		AuthRequest: oidc.AuthRequest{
			URL:          "https://issuer.example.com/authorize?state=state-one",
			State:        "state-one",
			Nonce:        "nonce-one",
			PKCEVerifier: "verifier-one",
		},
		TenantID:  "tenant-a",
		UserID:    "user-a",
		Roles:     []string{"owner", "member"},
		ExpiresAt: expiresAt,
	}
	if err := store.SaveLogin(ctx, login); err != nil {
		t.Fatalf("SaveLogin() error = %v", err)
	}
	if err := store.SaveLogin(ctx, login); !errors.Is(err, oidc.ErrDuplicateParam) {
		t.Fatalf("SaveLogin(duplicate state) error = %v, want ErrDuplicateParam", err)
	}

	consumed, err := store.ConsumeLogin(ctx, login.State)
	if err != nil {
		t.Fatalf("ConsumeLogin() error = %v", err)
	}
	if consumed.State != login.State || consumed.URL != login.URL || consumed.Nonce != login.Nonce || consumed.PKCEVerifier != login.PKCEVerifier || consumed.TenantID != login.TenantID || consumed.UserID != login.UserID || !reflect.DeepEqual(consumed.Roles, login.Roles) || !consumed.ExpiresAt.Equal(login.ExpiresAt) {
		t.Fatalf("ConsumeLogin() = %+v, want saved login", consumed)
	}
	if _, err := store.ConsumeLogin(ctx, login.State); !errors.Is(err, oidc.ErrLoginNotFound) {
		t.Fatalf("ConsumeLogin(replay) error = %v, want ErrLoginNotFound", err)
	}

	seedExpiredOIDCLogin(t, ctx, driver, db, "expired-state", time.Now().Add(-time.Minute).UTC())
	if _, err := store.ConsumeLogin(ctx, "expired-state"); !errors.Is(err, oidc.ErrLoginExpired) {
		t.Fatalf("ConsumeLogin(expired) error = %v, want ErrLoginExpired", err)
	}
	if _, err := store.ConsumeLogin(ctx, "expired-state"); !errors.Is(err, oidc.ErrLoginNotFound) {
		t.Fatalf("ConsumeLogin(expired replay) error = %v, want ErrLoginNotFound", err)
	}

	concurrent := login
	concurrent.State = "concurrent-state"
	concurrent.Nonce = "concurrent-nonce"
	concurrent.PKCEVerifier = "concurrent-verifier"
	if err := store.SaveLogin(ctx, concurrent); err != nil {
		t.Fatalf("SaveLogin(concurrent) error = %v", err)
	}

	const workers = 16
	start := make(chan struct{})
	unexpected := make(chan error, workers)
	var successes int64
	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			<-start
			_, err := store.ConsumeLogin(ctx, concurrent.State)
			switch {
			case err == nil:
				atomic.AddInt64(&successes, 1)
			case errors.Is(err, oidc.ErrLoginNotFound):
			default:
				unexpected <- err
			}
		}()
	}
	close(start)
	wg.Wait()
	close(unexpected)
	for err := range unexpected {
		t.Errorf("concurrent ConsumeLogin() error = %v", err)
	}
	if got := atomic.LoadInt64(&successes); got != 1 {
		t.Fatalf("concurrent ConsumeLogin() successes = %d, want 1", got)
	}
}

func seedExpiredOIDCLogin(t *testing.T, ctx context.Context, driver string, db *sql.DB, state string, expiresAt time.Time) {
	t.Helper()
	placeholders := "?, ?, ?, ?, ?, ?, ?, ?"
	if driver == "postgres" {
		placeholders = "$1, $2, $3, $4, $5, $6, $7, $8"
	}
	query := fmt.Sprintf("INSERT INTO oidc_logins (state, auth_url, nonce, pkce_verifier, tenant_id, user_id, roles, expires_at) VALUES (%s)", placeholders)
	if _, err := db.ExecContext(ctx, query, state, "https://issuer.example.com/authorize", "expired-nonce", "expired-verifier", "tenant-a", nil, `[]`, expiresAt); err != nil {
		t.Fatalf("seed expired OIDC login error = %v", err)
	}
}

func resetMySQLOIDCLoginsTable(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	resetOIDCLoginsTable(t, ctx, db, []string{
		"DROP TABLE IF EXISTS oidc_logins",
		`CREATE TABLE oidc_logins (
			state VARCHAR(255) NOT NULL PRIMARY KEY,
			auth_url TEXT NOT NULL,
			nonce VARCHAR(255) NOT NULL,
			pkce_verifier VARCHAR(255) NOT NULL,
			tenant_id VARCHAR(191) NOT NULL,
			user_id VARCHAR(191) NULL,
			roles JSON NOT NULL,
			expires_at DATETIME(6) NOT NULL
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	})
}

func resetPostgresOIDCLoginsTable(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	resetOIDCLoginsTable(t, ctx, db, []string{
		"DROP TABLE IF EXISTS oidc_logins",
		`CREATE TABLE oidc_logins (
			state VARCHAR(255) PRIMARY KEY,
			auth_url TEXT NOT NULL,
			nonce VARCHAR(255) NOT NULL,
			pkce_verifier VARCHAR(255) NOT NULL,
			tenant_id VARCHAR(191) NOT NULL,
			user_id VARCHAR(191) NULL,
			roles JSONB NOT NULL,
			expires_at TIMESTAMPTZ NOT NULL
		)`,
	})
}

func resetOIDCLoginsTable(t *testing.T, ctx context.Context, db *sql.DB, statements []string) {
	t.Helper()
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("exec %q error = %v", statement, err)
		}
	}
}

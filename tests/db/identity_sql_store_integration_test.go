package db_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/im10furry/saas/biz/identity"
)

func TestIdentitySQLStoreMySQLIntegration(t *testing.T) {
	runIdentitySQLStoreIntegration(t, "mysql", os.Getenv("SAAS_MYSQL_DSN"), resetMySQLIdentityLinksTable, func(db *sql.DB) (*identity.SQLStore, error) {
		return identity.NewSQLStore(db)
	})
}

func TestIdentitySQLStorePostgresIntegration(t *testing.T) {
	runIdentitySQLStoreIntegration(t, "postgres", os.Getenv("SAAS_POSTGRES_DSN"), resetPostgresIdentityLinksTable, func(db *sql.DB) (*identity.SQLStore, error) {
		return identity.NewSQLStore(db, identity.WithSQLDialect(identity.SQLDialectPostgres))
	})
}

func runIdentitySQLStoreIntegration(t *testing.T, driver, dsn string, reset func(*testing.T, context.Context, *sql.DB), newStore func(*sql.DB) (*identity.SQLStore, error)) {
	t.Helper()
	if dsn == "" {
		t.Skipf("set the %s DSN to run identity SQL integration tests", driver)
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
		t.Fatalf("NewSQLStore() error = %v", err)
	}
	runIdentitySQLStoreContract(t, ctx, store)
}

func runIdentitySQLStoreContract(t *testing.T, ctx context.Context, store *identity.SQLStore) {
	t.Helper()
	linkA := identity.Link{
		TenantID:      "tenant-a",
		UserID:        "shared-user",
		Provider:      identity.ProviderGoogle,
		Subject:       "shared-subject",
		Email:         "user@example.com",
		Name:          "Tenant A User",
		EmailVerified: true,
		Metadata:      map[string]string{"tenant": "a"},
	}
	linkB := linkA
	linkB.TenantID = "tenant-b"
	linkB.Name = "Tenant B User"
	linkB.Metadata = map[string]string{"tenant": "b"}
	secondA := linkA
	secondA.Provider = identity.ProviderGitHub
	secondA.Subject = "github-subject"

	for _, link := range []identity.Link{linkA, linkB, secondA} {
		if err := store.Link(ctx, link); err != nil {
			t.Fatalf("Link(%s, %s, %s) error = %v", link.TenantID, link.Provider, link.Subject, err)
		}
	}

	links, err := store.GetByUser(ctx, "tenant-a", "shared-user")
	if err != nil {
		t.Fatalf("GetByUser(tenant A) error = %v", err)
	}
	if len(links) != 2 || links[0].Provider != identity.ProviderGitHub || links[1].Provider != identity.ProviderGoogle {
		t.Fatalf("GetByUser(tenant A) = %+v, want tenant-A links sorted by provider", links)
	}
	links, err = store.GetByUser(ctx, "tenant-b", "shared-user")
	if err != nil {
		t.Fatalf("GetByUser(tenant B) error = %v", err)
	}
	if len(links) != 1 || links[0].TenantID != "tenant-b" || links[0].Provider != identity.ProviderGoogle || !reflect.DeepEqual(links[0].Metadata, map[string]string{"tenant": "b"}) {
		t.Fatalf("GetByUser(tenant B) = %+v, want only tenant-B link", links)
	}

	updatedA := linkA
	updatedA.Name = "Tenant A Refreshed"
	updatedA.Metadata = map[string]string{"tenant": "a", "revision": "2"}
	if err := store.Link(ctx, updatedA); err != nil {
		t.Fatalf("Link(update tenant A) error = %v", err)
	}
	gotA, err := store.GetByExternal(ctx, "tenant-a", identity.ProviderGoogle, "shared-subject")
	if err != nil {
		t.Fatalf("GetByExternal(tenant A) error = %v", err)
	}
	if gotA.Name != "Tenant A Refreshed" || !reflect.DeepEqual(gotA.Metadata, updatedA.Metadata) {
		t.Fatalf("GetByExternal(tenant A) = %+v, want refreshed metadata", gotA)
	}
	gotB, err := store.GetByExternal(ctx, "tenant-b", identity.ProviderGoogle, "shared-subject")
	if err != nil {
		t.Fatalf("GetByExternal(tenant B) error = %v", err)
	}
	if gotB.Name != "Tenant B User" || !reflect.DeepEqual(gotB.Metadata, linkB.Metadata) {
		t.Fatalf("GetByExternal(tenant B) = %+v, want unchanged tenant-B link", gotB)
	}

	conflict := updatedA
	conflict.UserID = "other-user"
	if err := store.Link(ctx, conflict); !errors.Is(err, identity.ErrIdentityConflict) {
		t.Fatalf("Link(conflicting tenant A identity) error = %v, want ErrIdentityConflict", err)
	}

	if err := store.Unlink(ctx, "tenant-a", identity.ProviderGoogle, "shared-subject"); err != nil {
		t.Fatalf("Unlink(tenant A) error = %v", err)
	}
	if _, err := store.GetByExternal(ctx, "tenant-a", identity.ProviderGoogle, "shared-subject"); !errors.Is(err, identity.ErrIdentityNotFound) {
		t.Fatalf("GetByExternal(unlinked tenant A) error = %v, want ErrIdentityNotFound", err)
	}
	if _, err := store.GetByExternal(ctx, "tenant-b", identity.ProviderGoogle, "shared-subject"); err != nil {
		t.Fatalf("GetByExternal(tenant B after tenant-A unlink) error = %v", err)
	}

	concurrent := identity.Link{
		TenantID:      "tenant-concurrent",
		UserID:        "shared-user",
		Provider:      identity.ProviderGoogle,
		Subject:       "concurrent-subject",
		Email:         "user@example.com",
		EmailVerified: true,
		Metadata:      map[string]string{"source": "concurrent"},
	}
	const workers = 24
	start := make(chan struct{})
	unexpected := make(chan error, workers)
	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			<-start
			if err := store.Link(ctx, concurrent); err != nil {
				unexpected <- err
			}
		}()
	}
	close(start)
	wg.Wait()
	close(unexpected)
	for err := range unexpected {
		t.Errorf("concurrent Link() error = %v", err)
	}
	if _, err := store.GetByExternal(ctx, concurrent.TenantID, concurrent.Provider, concurrent.Subject); err != nil {
		t.Fatalf("GetByExternal(concurrent link) error = %v", err)
	}
}

func resetMySQLIdentityLinksTable(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	resetIdentityLinksTable(t, ctx, db, []string{
		"DROP TABLE IF EXISTS identity_links",
		`CREATE TABLE identity_links (
			tenant_id VARCHAR(191) NOT NULL,
			provider VARCHAR(191) NOT NULL,
			subject VARCHAR(255) NOT NULL,
			user_id VARCHAR(191) NOT NULL,
			email VARCHAR(255) NOT NULL,
			name VARCHAR(255) NOT NULL,
			email_verified BOOLEAN NOT NULL,
			metadata JSON NOT NULL,
			PRIMARY KEY (tenant_id, provider, subject)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	})
}

func resetPostgresIdentityLinksTable(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	resetIdentityLinksTable(t, ctx, db, []string{
		"DROP TABLE IF EXISTS identity_links",
		`CREATE TABLE identity_links (
			tenant_id VARCHAR(191) NOT NULL,
			provider VARCHAR(191) NOT NULL,
			subject VARCHAR(255) NOT NULL,
			user_id VARCHAR(191) NOT NULL,
			email VARCHAR(255) NOT NULL,
			name VARCHAR(255) NOT NULL,
			email_verified BOOLEAN NOT NULL,
			metadata JSONB NOT NULL,
			PRIMARY KEY (tenant_id, provider, subject)
		)`,
	})
}

func resetIdentityLinksTable(t *testing.T, ctx context.Context, db *sql.DB, statements []string) {
	t.Helper()
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("exec %q error = %v", statement, err)
		}
	}
}

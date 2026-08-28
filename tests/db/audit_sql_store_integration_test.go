package db_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/darkinno-tech/saas/biz/audit"
)

func TestAuditSQLStoreMySQLIntegration(t *testing.T) {
	runAuditSQLStoreIntegration(t, "mysql", os.Getenv("SAAS_MYSQL_DSN"), resetMySQLAuditEventsTable, func(db *sql.DB) (*audit.SQLStore, error) {
		return audit.NewSQLStore(db)
	})
}

func TestAuditSQLStorePostgresIntegration(t *testing.T) {
	runAuditSQLStoreIntegration(t, "postgres", os.Getenv("SAAS_POSTGRES_DSN"), resetPostgresAuditEventsTable, func(db *sql.DB) (*audit.SQLStore, error) {
		return audit.NewSQLStore(db, audit.WithSQLDialect(audit.SQLDialectPostgres))
	})
}

func runAuditSQLStoreIntegration(t *testing.T, driver, dsn string, reset func(*testing.T, context.Context, *sql.DB), newStore func(*sql.DB) (*audit.SQLStore, error)) {
	t.Helper()
	if dsn == "" {
		t.Skipf("set the %s DSN to run audit SQL integration tests", driver)
	}

	db, err := sql.Open(driver, dsn)
	if err != nil {
		t.Fatalf("sql.Open(%s) error = %v", driver, err)
	}
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
	runAuditSQLStoreContract(t, ctx, store)
}

func runAuditSQLStoreContract(t *testing.T, ctx context.Context, store *audit.SQLStore) {
	t.Helper()
	base := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	events := []audit.Event{
		{
			ID:        "event-1",
			TenantID:  "tenant-a",
			ActorID:   "user-a",
			Action:    "orders.create",
			Resource:  "order:1",
			CreatedAt: base,
			Metadata:  map[string]string{"source": "api"},
		},
		{
			ID:        "event-2",
			TenantID:  "tenant-a",
			ActorID:   "user-b",
			Action:    "orders.update",
			Resource:  "order:1",
			CreatedAt: base,
			Metadata:  map[string]string{"source": "worker"},
		},
		{
			TenantID:  "tenant-a",
			Action:    "billing.renew",
			Resource:  "subscription:1",
			CreatedAt: base.Add(time.Second),
		},
		{
			ID:        "event-other-tenant",
			TenantID:  "tenant-b",
			Action:    "orders.create",
			Resource:  "order:2",
			CreatedAt: base,
		},
	}
	for _, event := range events {
		if err := store.Record(ctx, event); err != nil {
			t.Fatalf("Record(%q) error = %v", event.ID, err)
		}
	}

	all, err := store.List(ctx, "tenant-a")
	if err != nil {
		t.Fatalf("List(tenant-a) error = %v", err)
	}
	if len(all) != 3 || all[0].ID != "event-1" || all[1].ID != "event-2" || all[2].ID != "generated-event" {
		t.Fatalf("List(tenant-a) = %+v, want tenant-local ordered events", all)
	}
	if all[2].ActorID != "" || len(all[2].Metadata) != 0 {
		t.Fatalf("List(default ID/null actor) = %+v, want null actor and empty metadata", all[2])
	}
	if all[0].Metadata["source"] != "api" || !all[0].CreatedAt.Equal(base) {
		t.Fatalf("List(first event) = %+v, want preserved metadata and timestamp", all[0])
	}

	first, err := store.ListPage(ctx, "tenant-a", audit.ListFilter{Limit: 1})
	if err != nil {
		t.Fatalf("ListPage(first) error = %v", err)
	}
	if len(first) != 1 || first[0].ID != "event-1" {
		t.Fatalf("ListPage(first) = %+v, want event-1", first)
	}
	next, err := store.ListPage(ctx, "tenant-a", audit.ListFilter{Cursor: audit.CursorFor(first[0]), Limit: 1})
	if err != nil {
		t.Fatalf("ListPage(cursor) error = %v", err)
	}
	if len(next) != 1 || next[0].ID != "event-2" {
		t.Fatalf("ListPage(cursor) = %+v, want event-2", next)
	}

	if err := store.Record(ctx, audit.Event{}); !errors.Is(err, audit.ErrInvalidEvent) {
		t.Fatalf("Record(invalid) error = %v, want ErrInvalidEvent", err)
	}
	if _, err := store.List(ctx, ""); !errors.Is(err, audit.ErrInvalidEvent) {
		t.Fatalf("List(empty tenant) error = %v, want ErrInvalidEvent", err)
	}
	if _, err := store.ListPage(ctx, "tenant-a", audit.ListFilter{Limit: -1}); !errors.Is(err, audit.ErrInvalidListFilter) {
		t.Fatalf("ListPage(invalid filter) error = %v, want ErrInvalidListFilter", err)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.Record(canceled, events[0]); !errors.Is(err, context.Canceled) {
		t.Fatalf("Record(canceled) error = %v, want context.Canceled", err)
	}
	if _, err := store.ListPage(canceled, "tenant-a", audit.ListFilter{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("ListPage(canceled) error = %v, want context.Canceled", err)
	}
}

func resetMySQLAuditEventsTable(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	resetAuditEventsTable(t, ctx, db, []string{
		"DROP TABLE IF EXISTS audit_events",
		`CREATE TABLE audit_events (
			id VARCHAR(191) NOT NULL PRIMARY KEY DEFAULT 'generated-event',
			tenant_id VARCHAR(191) NOT NULL,
			actor_id VARCHAR(191) NULL,
			action VARCHAR(191) NOT NULL,
			resource VARCHAR(191) NOT NULL,
			created_at DATETIME(6) NOT NULL,
			metadata JSON NOT NULL
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	})
}

func resetPostgresAuditEventsTable(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	resetAuditEventsTable(t, ctx, db, []string{
		"DROP TABLE IF EXISTS audit_events",
		`CREATE TABLE audit_events (
			id VARCHAR(191) PRIMARY KEY DEFAULT 'generated-event',
			tenant_id VARCHAR(191) NOT NULL,
			actor_id VARCHAR(191) NULL,
			action VARCHAR(191) NOT NULL,
			resource VARCHAR(191) NOT NULL,
			created_at TIMESTAMPTZ NOT NULL,
			metadata JSONB NOT NULL
		)`,
	})
}

func resetAuditEventsTable(t *testing.T, ctx context.Context, db *sql.DB, statements []string) {
	t.Helper()
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("exec %q error = %v", statement, err)
		}
	}
}

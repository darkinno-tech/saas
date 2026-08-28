package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/darkinno-tech/saas/core/types"
)

const tenantColumns = "id, name, status, plan_id, config"

func TestSQLStoreGetMapsMissingAndRejectsMalformedStoredConfig(t *testing.T) {
	ctx := context.Background()

	t.Run("invalid tenant does not reach the database", func(t *testing.T) {
		store, mock := newMockSQLStore(t, SQLDialectMySQL)

		if _, err := store.Get(ctx, ""); !errors.Is(err, ErrInvalidTenant) {
			t.Fatalf("Get(empty id) error = %v, want ErrInvalidTenant", err)
		}
		assertSQLMockExpectations(t, mock)
	})

	t.Run("mysql missing tenant maps not found", func(t *testing.T) {
		store, mock := newMockSQLStore(t, SQLDialectMySQL)
		mock.ExpectQuery(regexp.QuoteMeta("SELECT " + tenantColumns + " FROM tenants WHERE id = ?")).
			WithArgs("missing").
			WillReturnError(sql.ErrNoRows)

		if _, err := store.Get(ctx, "missing"); !errors.Is(err, ErrTenantNotFound) {
			t.Fatalf("Get(missing) error = %v, want ErrTenantNotFound", err)
		}
		assertSQLMockExpectations(t, mock)
	})

	t.Run("postgres malformed config is surfaced", func(t *testing.T) {
		store, mock := newMockSQLStore(t, SQLDialectPostgres)
		mock.ExpectQuery(regexp.QuoteMeta("SELECT " + tenantColumns + " FROM tenants WHERE id = $1")).
			WithArgs("tenant-a").
			WillReturnRows(sqlmock.NewRows(tenantColumnNames()).
				AddRow("tenant-a", "Tenant tenant-a", "active", "starter", `{"feature":`))

		if _, err := store.Get(ctx, "tenant-a"); err == nil {
			t.Fatal("Get(malformed config) error = nil, want JSON decoding error")
		} else {
			var syntaxErr *json.SyntaxError
			if !errors.As(err, &syntaxErr) {
				t.Fatalf("Get(malformed config) error = %T %v, want *json.SyntaxError", err, err)
			}
		}
		assertSQLMockExpectations(t, mock)
	})
}

func TestSQLStoreListAndPageUseExpectedSQLAndSurfaceDatabaseFailures(t *testing.T) {
	ctx := context.Background()

	t.Run("postgres cursor page applies status filter before cursor", func(t *testing.T) {
		store, mock := newMockSQLStore(t, SQLDialectPostgres)
		mock.ExpectQuery(regexp.QuoteMeta("SELECT "+tenantColumns+" FROM tenants WHERE status IN ($1, $2) AND id > $3 ORDER BY id LIMIT $4")).
			WithArgs("active", "suspended", "tenant-a", 2).
			WillReturnRows(sqlmock.NewRows(tenantColumnNames()).
				AddRow("tenant-b", "Tenant tenant-b", "active", "starter", `{"feature":"on"}`).
				AddRow("tenant-c", "Tenant tenant-c", "suspended", "pro", `{"feature":"on"}`))

		got, err := store.ListPage(ctx, PageFilter{
			Statuses: []types.TenantStatus{types.TenantStatusActive, types.TenantStatusSuspended},
			Cursor:   "tenant-a",
			Limit:    2,
		})
		if err != nil {
			t.Fatalf("ListPage() error = %v", err)
		}
		if len(got) != 2 || got[0].ID != "tenant-b" || got[0].Status != types.TenantStatusActive || got[1].ID != "tenant-c" || got[1].Status != types.TenantStatusSuspended {
			t.Fatalf("ListPage() = %+v, want active tenant-b then suspended tenant-c", got)
		}
		assertSQLMockExpectations(t, mock)
	})

	t.Run("mysql offset page binds filter and pagination arguments", func(t *testing.T) {
		store, mock := newMockSQLStore(t, SQLDialectMySQL)
		mock.ExpectQuery(regexp.QuoteMeta("SELECT "+tenantColumns+" FROM tenants WHERE status IN (?) ORDER BY id LIMIT ? OFFSET ?")).
			WithArgs("active", 1, 1).
			WillReturnRows(sqlmock.NewRows(tenantColumnNames()).
				AddRow("tenant-b", "Tenant tenant-b", "active", "starter", `{"feature":"on"}`))

		got, err := store.List(ctx, ListFilter{Statuses: []types.TenantStatus{types.TenantStatusActive}, Limit: 1, Offset: 1})
		if err != nil {
			t.Fatalf("List(offset page) error = %v", err)
		}
		if len(got) != 1 || got[0].ID != "tenant-b" || got[0].Config["feature"] != "on" {
			t.Fatalf("List(offset page) = %+v, want decoded tenant-b page", got)
		}
		assertSQLMockExpectations(t, mock)
	})

	t.Run("query error is returned", func(t *testing.T) {
		store, mock := newMockSQLStore(t, SQLDialectMySQL)
		wantErr := errors.New("database unavailable")
		mock.ExpectQuery(regexp.QuoteMeta("SELECT " + tenantColumns + " FROM tenants ORDER BY id")).
			WillReturnError(wantErr)

		if _, err := store.List(ctx, ListFilter{}); !errors.Is(err, wantErr) {
			t.Fatalf("List() error = %v, want %v", err, wantErr)
		}
		assertSQLMockExpectations(t, mock)
	})

	t.Run("row scan error is returned", func(t *testing.T) {
		store, mock := newMockSQLStore(t, SQLDialectMySQL)
		mock.ExpectQuery(regexp.QuoteMeta("SELECT " + tenantColumns + " FROM tenants ORDER BY id LIMIT ?")).
			WithArgs(1).
			WillReturnRows(sqlmock.NewRows(tenantColumnNames()).
				AddRow("tenant-a", nil, "active", "starter", `{"feature":"on"}`))

		if _, err := store.List(ctx, ListFilter{Limit: 1}); err == nil {
			t.Fatal("List(row with NULL name) error = nil, want scan error")
		}
		assertSQLMockExpectations(t, mock)
	})
}

func TestSQLStoreCreateMapsDuplicateDatabaseError(t *testing.T) {
	ctx := context.Background()
	tenant := testTenant("tenant-a")
	store, mock := newMockSQLStore(t, SQLDialectPostgres)
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO tenants (id, name, status, plan_id, config) VALUES ($1, $2, $3, $4, $5)")).
		WithArgs("tenant-a", tenant.Name, "active", tenant.PlanID, `{"feature":"on"}`).
		WillReturnError(errors.New("pq: duplicate key value violates unique constraint tenants_pkey"))

	if err := store.Create(ctx, tenant); !errors.Is(err, ErrTenantAlreadyExists) {
		t.Fatalf("Create(duplicate) error = %v, want ErrTenantAlreadyExists", err)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreUpdateZeroRowsDistinguishesNoOpFromConflict(t *testing.T) {
	ctx := context.Background()
	desired := testTenant("tenant-a")

	t.Run("mysql no op is accepted after matching read back", func(t *testing.T) {
		store, mock := newMockSQLStore(t, SQLDialectMySQL)
		mock.ExpectExec(regexp.QuoteMeta("UPDATE tenants SET name = ?, status = ?, plan_id = ?, config = ? WHERE id = ?")).
			WithArgs(desired.Name, "active", desired.PlanID, `{"feature":"on"}`, "tenant-a").
			WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(regexp.QuoteMeta("SELECT " + tenantColumns + " FROM tenants WHERE id = ?")).
			WithArgs("tenant-a").
			WillReturnRows(mockTenantRows(desired))

		if err := store.Update(ctx, desired); err != nil {
			t.Fatalf("Update(no-op) error = %v", err)
		}
		assertSQLMockExpectations(t, mock)
	})

	t.Run("postgres changed read back is a conflict", func(t *testing.T) {
		store, mock := newMockSQLStore(t, SQLDialectPostgres)
		current := desired
		current.Status = types.TenantStatusSuspended
		mock.ExpectExec(regexp.QuoteMeta("UPDATE tenants SET name = $1, status = $2, plan_id = $3, config = $4 WHERE id = $5")).
			WithArgs(desired.Name, "active", desired.PlanID, `{"feature":"on"}`, "tenant-a").
			WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(regexp.QuoteMeta("SELECT " + tenantColumns + " FROM tenants WHERE id = $1")).
			WithArgs("tenant-a").
			WillReturnRows(mockTenantRows(current))

		if err := store.Update(ctx, desired); !errors.Is(err, ErrTenantConflict) {
			t.Fatalf("Update(concurrent replacement) error = %v, want ErrTenantConflict", err)
		}
		assertSQLMockExpectations(t, mock)
	})
}

func TestSQLStoreDeleteZeroRowsMapsNotFound(t *testing.T) {
	store, mock := newMockSQLStore(t, SQLDialectMySQL)
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM tenants WHERE id = ?")).
		WithArgs("tenant-a").
		WillReturnResult(sqlmock.NewResult(0, 0))

	if err := store.Delete(context.Background(), "tenant-a"); !errors.Is(err, ErrTenantNotFound) {
		t.Fatalf("Delete(missing) error = %v, want ErrTenantNotFound", err)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreCompareAndSwapTransactionOutcomes(t *testing.T) {
	ctx := context.Background()
	expected := testTenant("tenant-a")
	updated := expected
	updated.Name = "Renamed tenant"

	t.Run("mysql missing tenant rolls back and maps not found", func(t *testing.T) {
		store, mock := newMockSQLStore(t, SQLDialectMySQL)
		mock.ExpectBegin()
		mock.ExpectQuery(regexp.QuoteMeta("SELECT " + tenantColumns + " FROM tenants WHERE id = ? FOR UPDATE")).
			WithArgs("tenant-a").
			WillReturnError(sql.ErrNoRows)
		mock.ExpectRollback()

		if err := store.CompareAndSwap(ctx, expected, updated); !errors.Is(err, ErrTenantNotFound) {
			t.Fatalf("CompareAndSwap(missing) error = %v, want ErrTenantNotFound", err)
		}
		assertSQLMockExpectations(t, mock)
	})

	t.Run("postgres stale snapshot rolls back and maps conflict", func(t *testing.T) {
		store, mock := newMockSQLStore(t, SQLDialectPostgres)
		current := expected
		current.Status = types.TenantStatusSuspended
		mock.ExpectBegin()
		mock.ExpectQuery(regexp.QuoteMeta("SELECT " + tenantColumns + " FROM tenants WHERE id = $1 FOR UPDATE")).
			WithArgs("tenant-a").
			WillReturnRows(mockTenantRows(current))
		mock.ExpectRollback()

		if err := store.CompareAndSwap(ctx, expected, updated); !errors.Is(err, ErrTenantConflict) {
			t.Fatalf("CompareAndSwap(stale) error = %v, want ErrTenantConflict", err)
		}
		assertSQLMockExpectations(t, mock)
	})

	t.Run("sqlite equal replacement commits without write", func(t *testing.T) {
		store, mock := newMockSQLStore(t, SQLDialectSQLite)
		mock.ExpectBegin()
		mock.ExpectQuery(regexp.QuoteMeta("SELECT " + tenantColumns + " FROM tenants WHERE id = ?")).
			WithArgs("tenant-a").
			WillReturnRows(mockTenantRows(expected))
		mock.ExpectCommit()

		if err := store.CompareAndSwap(ctx, expected, expected); err != nil {
			t.Fatalf("CompareAndSwap(no-op) error = %v", err)
		}
		assertSQLMockExpectations(t, mock)
	})

	t.Run("commit error is returned after a successful conditional update", func(t *testing.T) {
		store, mock := newMockSQLStore(t, SQLDialectPostgres)
		commitErr := errors.New("connection lost while committing")
		mock.ExpectBegin()
		mock.ExpectQuery(regexp.QuoteMeta("SELECT " + tenantColumns + " FROM tenants WHERE id = $1 FOR UPDATE")).
			WithArgs("tenant-a").
			WillReturnRows(mockTenantRows(expected))
		mock.ExpectExec(regexp.QuoteMeta("UPDATE tenants SET name = $1, status = $2, plan_id = $3, config = $4 WHERE id = $5")).
			WithArgs(updated.Name, "active", updated.PlanID, `{"feature":"on"}`, "tenant-a").
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit().WillReturnError(commitErr)

		if err := store.CompareAndSwap(ctx, expected, updated); !errors.Is(err, commitErr) {
			t.Fatalf("CompareAndSwap(commit error) = %v, want %v", err, commitErr)
		}
		assertSQLMockExpectations(t, mock)
	})
}

func newMockSQLStore(t *testing.T, dialect SQLDialect) (*SQLStore, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	store, err := NewSQLStore(db, WithSQLDialect(dialect))
	if err != nil {
		_ = db.Close()
		t.Fatalf("NewSQLStore() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return store, mock
}

func assertSQLMockExpectations(t *testing.T, mock sqlmock.Sqlmock) {
	t.Helper()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func mockTenantRows(tenant types.Tenant) *sqlmock.Rows {
	return sqlmock.NewRows(tenantColumnNames()).
		AddRow(tenant.ID.String(), tenant.Name, string(tenant.Status), tenant.PlanID, `{"feature":"on"}`)
}

func tenantColumnNames() []string {
	return []string{"id", "name", "status", "plan_id", "config"}
}

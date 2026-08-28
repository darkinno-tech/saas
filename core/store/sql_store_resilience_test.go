package store

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/im10furry/saas/core/types"
)

func TestSQLStoreValidationAndDriverFailuresDoNotChangeTenantState(t *testing.T) {
	ctx := context.Background()
	tenant := testTenant("tenant-a")

	t.Run("invalid and canceled requests stop before a database write", func(t *testing.T) {
		store, mock := newMockSQLStore(t, SQLDialectMySQL)
		if err := store.Create(ctx, types.Tenant{}); !errors.Is(err, ErrInvalidTenant) {
			t.Fatalf("Create(invalid) error = %v, want ErrInvalidTenant", err)
		}
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		if err := store.Update(canceled, tenant); !errors.Is(err, context.Canceled) {
			t.Fatalf("Update(canceled) error = %v, want context.Canceled", err)
		}
		if err := store.Delete(ctx, ""); !errors.Is(err, ErrInvalidTenant) {
			t.Fatalf("Delete(blank) error = %v, want ErrInvalidTenant", err)
		}
		assertSQLMockExpectations(t, mock)
	})

	t.Run("write driver errors are returned instead of being converted to success", func(t *testing.T) {
		store, mock := newMockSQLStore(t, SQLDialectPostgres)
		writeErr := errors.New("database connection lost")
		mock.ExpectExec(regexp.QuoteMeta("INSERT INTO tenants (id, name, status, plan_id, config) VALUES ($1, $2, $3, $4, $5)")).
			WithArgs(tenant.ID.String(), tenant.Name, string(tenant.Status), tenant.PlanID, `{"feature":"on"}`).
			WillReturnError(writeErr)
		if err := store.Create(ctx, tenant); !errors.Is(err, writeErr) {
			t.Fatalf("Create(driver error) = %v, want %v", err, writeErr)
		}

		mock.ExpectExec(regexp.QuoteMeta("UPDATE tenants SET name = $1, status = $2, plan_id = $3, config = $4 WHERE id = $5")).
			WithArgs(tenant.Name, string(tenant.Status), tenant.PlanID, `{"feature":"on"}`, tenant.ID.String()).
			WillReturnError(writeErr)
		if err := store.Update(ctx, tenant); !errors.Is(err, writeErr) {
			t.Fatalf("Update(driver error) = %v, want %v", err, writeErr)
		}

		mock.ExpectExec(regexp.QuoteMeta("DELETE FROM tenants WHERE id = $1")).
			WithArgs(tenant.ID.String()).
			WillReturnResult(sqlmock.NewErrorResult(writeErr))
		if err := store.Delete(ctx, tenant.ID); !errors.Is(err, writeErr) {
			t.Fatalf("Delete(rows affected error) = %v, want %v", err, writeErr)
		}
		assertSQLMockExpectations(t, mock)
	})
}

func TestSQLStoreCompareAndSwapNormalizesTransientDatabaseConflicts(t *testing.T) {
	ctx := context.Background()
	expected := testTenant("tenant-a")
	updated := expected
	updated.Name = "Renamed tenant"

	t.Run("transaction cannot begin because database is locked", func(t *testing.T) {
		store, mock := newMockSQLStore(t, SQLDialectSQLite)
		lockErr := errors.New("database is locked")
		mock.ExpectBegin().WillReturnError(lockErr)

		err := store.CompareAndSwap(ctx, expected, updated)
		if !errors.Is(err, ErrTenantConflict) || !errors.Is(err, lockErr) {
			t.Fatalf("CompareAndSwap(begin lock) = %v, want joined ErrTenantConflict and driver error", err)
		}
		assertSQLMockExpectations(t, mock)
	})

	t.Run("row lock is lost while reading the expected snapshot", func(t *testing.T) {
		store, mock := newMockSQLStore(t, SQLDialectMySQL)
		deadlockErr := errors.New("deadlock found when trying to get lock")
		mock.ExpectBegin()
		mock.ExpectQuery(regexp.QuoteMeta("SELECT " + tenantColumns + " FROM tenants WHERE id = ? FOR UPDATE")).
			WithArgs(expected.ID.String()).
			WillReturnError(deadlockErr)
		mock.ExpectRollback()

		err := store.CompareAndSwap(ctx, expected, updated)
		if !errors.Is(err, ErrTenantConflict) || !errors.Is(err, deadlockErr) {
			t.Fatalf("CompareAndSwap(read deadlock) = %v, want joined ErrTenantConflict and driver error", err)
		}
		assertSQLMockExpectations(t, mock)
	})

	t.Run("write deadlock does not commit a partial replacement", func(t *testing.T) {
		store, mock := newMockSQLStore(t, SQLDialectPostgres)
		deadlockErr := errors.New("lock wait timeout exceeded")
		mock.ExpectBegin()
		mock.ExpectQuery(regexp.QuoteMeta("SELECT " + tenantColumns + " FROM tenants WHERE id = $1 FOR UPDATE")).
			WithArgs(expected.ID.String()).
			WillReturnRows(mockTenantRows(expected))
		mock.ExpectExec(regexp.QuoteMeta("UPDATE tenants SET name = $1, status = $2, plan_id = $3, config = $4 WHERE id = $5")).
			WithArgs(updated.Name, string(updated.Status), updated.PlanID, `{"feature":"on"}`, updated.ID.String()).
			WillReturnError(deadlockErr)
		mock.ExpectRollback()

		err := store.CompareAndSwap(ctx, expected, updated)
		if !errors.Is(err, ErrTenantConflict) || !errors.Is(err, deadlockErr) {
			t.Fatalf("CompareAndSwap(write deadlock) = %v, want joined ErrTenantConflict and driver error", err)
		}
		assertSQLMockExpectations(t, mock)
	})
}

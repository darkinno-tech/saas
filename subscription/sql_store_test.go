package subscription

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/darkinno-tech/saas/core/types"
)

// failingResult implements driver.Result and reports a RowsAffected error so the
// requireAffectedRow / requireUpdatedRow error branches can be exercised.
type failingResult struct{}

func (failingResult) LastInsertId() (int64, error) { return 0, nil }
func (failingResult) RowsAffected() (int64, error) { return 0, errors.New("fake rows affected error") }

func TestNewSQLStoreValidationOptions(t *testing.T) {
	if _, err := NewSQLStore(nil); !errors.Is(err, ErrNilDB) {
		t.Fatalf("NewSQLStore(nil) error = %v, want ErrNilDB", err)
	}

	db := &sql.DB{}
	store, err := NewSQLStore(db)
	if err != nil {
		t.Fatalf("NewSQLStore() error = %v", err)
	}
	if store.table != DefaultSQLTableName || store.dialect != SQLDialectMySQL {
		t.Fatalf("NewSQLStore() defaults = %#v, want defaults", store)
	}

	if _, err := NewSQLStore(db, WithTableName("subs;drop")); !errors.Is(err, ErrInvalidTableName) {
		t.Fatalf("NewSQLStore(unsafe table) error = %v, want ErrInvalidTableName", err)
	}
	if _, err := NewSQLStore(db, WithTableName("")); !errors.Is(err, ErrInvalidTableName) {
		t.Fatalf("NewSQLStore(empty table) error = %v, want ErrInvalidTableName", err)
	}
	if _, err := NewSQLStore(db, WithSQLDialect("oracle")); !errors.Is(err, ErrUnsupportedSQLDialect) {
		t.Fatalf("NewSQLStore(unsupported dialect) error = %v, want ErrUnsupportedSQLDialect", err)
	}
}

func TestSQLStoreCreateRoundTripUsesConfiguredDialect(t *testing.T) {
	cases := []struct {
		name    string
		dialect SQLDialect
	}{
		{"mysql", SQLDialectMySQL},
		{"sqlite", SQLDialectSQLite},
		{"postgres", SQLDialectPostgres},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store, mock := newMockSQLStore(t, tc.dialect)
			sub := testSubscription()

			ph := placeholderFor(t, store, 7, 1)
			expected := "INSERT INTO saas_subscriptions (tenant_id, plan_id, status, start_date, end_date, current_period_end, grace_period_end) VALUES (" + ph + ")"
			create := mock.ExpectExec(regexp.QuoteMeta(expected))
			create.WithArgs("tenant-a", "plan-1", "active", sub.StartDate, *sub.EndDate, *sub.CurrentPeriodEnd, *sub.GracePeriodEnd).WillReturnResult(sqlmock.NewResult(1, 1))

			if err := store.Create(ctx, sub); err != nil {
				t.Fatalf("Create() error = %v", err)
			}
			assertSQLMockExpectations(t, mock)
		})
	}
}

func TestSQLStoreCreateValidationAndErrors(t *testing.T) {
	store, _ := newMockSQLStore(t, SQLDialectMySQL)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.Create(cancelled, testSubscription()); err == nil {
		t.Fatal("Create() cancelled ctx error = nil, want error")
	}

	bad := testSubscription()
	bad.PlanID = ""
	if err := store.Create(context.Background(), bad); !errors.Is(err, ErrInvalidSubscription) {
		t.Fatalf("Create(invalid) error = %v, want ErrInvalidSubscription", err)
	}
}

func TestSQLStoreCreateExecError(t *testing.T) {
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectMySQL)
	sub := testSubscription()

	create := mock.ExpectExec(regexp.QuoteMeta("INSERT INTO saas_subscriptions (tenant_id, plan_id, status, start_date, end_date, current_period_end, grace_period_end) VALUES (?, ?, ?, ?, ?, ?, ?)"))
	create.WithArgs("tenant-a", "plan-1", "active", sub.StartDate, *sub.EndDate, *sub.CurrentPeriodEnd, *sub.GracePeriodEnd).WillReturnError(errors.New("boom"))

	if err := store.Create(ctx, sub); err == nil || err.Error() != "boom" {
		t.Fatalf("Create() error = %v, want boom", err)
	}
}

func TestSQLStoreCreateDuplicateKey(t *testing.T) {
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectMySQL)
	sub := testSubscription()

	create := mock.ExpectExec(regexp.QuoteMeta("INSERT INTO saas_subscriptions (tenant_id, plan_id, status, start_date, end_date, current_period_end, grace_period_end) VALUES (?, ?, ?, ?, ?, ?, ?)"))
	create.WithArgs("tenant-a", "plan-1", "active", sub.StartDate, *sub.EndDate, *sub.CurrentPeriodEnd, *sub.GracePeriodEnd).WillReturnError(errors.New("Duplicate entry 'x' for key 'prime'"))

	if err := store.Create(ctx, sub); !errors.Is(err, ErrSubscriptionAlreadyExists) {
		t.Fatalf("Create() duplicate error = %v, want ErrSubscriptionAlreadyExists", err)
	}
}

func TestSQLStoreGetRoundTrip(t *testing.T) {
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectPostgres)
	end := time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC)
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	get := mock.ExpectQuery(regexp.QuoteMeta("SELECT tenant_id, plan_id, status, start_date, end_date, current_period_end, grace_period_end FROM saas_subscriptions WHERE tenant_id = $1"))
	get.WithArgs("tenant-a").WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "plan_id", "status", "start_date", "end_date", "current_period_end", "grace_period_end"}).AddRow("tenant-a", "plan-1", "active", start, end, nil, nil))

	got, err := store.Get(ctx, "tenant-a")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	want := Subscription{TenantID: "tenant-a", PlanID: "plan-1", Status: StatusActive, StartDate: start, EndDate: &end, CurrentPeriodEnd: nil, GracePeriodEnd: nil}
	if !subscriptionsEqual(got, want) {
		t.Fatalf("Get() = %#v, want %#v", got, want)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreGetValidationAndErrors(t *testing.T) {
	store, _ := newMockSQLStore(t, SQLDialectMySQL)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Get(cancelled, "tenant-a"); err == nil {
		t.Fatal("Get() cancelled ctx error = nil, want error")
	}
	if _, err := store.Get(context.Background(), ""); !errors.Is(err, ErrInvalidSubscription) {
		t.Fatalf("Get(empty) error = %v, want ErrInvalidSubscription", err)
	}
}

func TestSQLStoreGetNotFound(t *testing.T) {
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectMySQL)
	get := mock.ExpectQuery(regexp.QuoteMeta("SELECT tenant_id, plan_id, status, start_date, end_date, current_period_end, grace_period_end FROM saas_subscriptions WHERE tenant_id = ?"))
	get.WithArgs("missing").WillReturnRows(sqlmock.NewRows([]string{"tenant_id"})) // no rows

	if _, err := store.Get(ctx, "missing"); !errors.Is(err, ErrSubscriptionNotFound) {
		t.Fatalf("Get(not found) error = %v, want ErrSubscriptionNotFound", err)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreGetScanError(t *testing.T) {
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectMySQL)
	get := mock.ExpectQuery(regexp.QuoteMeta("SELECT tenant_id, plan_id, status, start_date, end_date, current_period_end, grace_period_end FROM saas_subscriptions WHERE tenant_id = ?"))
	get.WithArgs("tenant-a").WillReturnRows(sqlmock.NewRows([]string{"tenant_id"}).AddRow("tenant-a")) // too few columns

	if _, err := store.Get(ctx, "tenant-a"); err == nil {
		t.Fatal("Get(scan error) error = nil, want error")
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreGetInvalidRowvalidated(t *testing.T) {
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectMySQL)
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	// plan_id empty -> validation fails after a successful scan
	get := mock.ExpectQuery(regexp.QuoteMeta("SELECT tenant_id, plan_id, status, start_date, end_date, current_period_end, grace_period_end FROM saas_subscriptions WHERE tenant_id = ?"))
	get.WithArgs("tenant-a").WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "plan_id", "status", "start_date", "end_date", "current_period_end", "grace_period_end"}).AddRow("tenant-a", "", "active", start, nil, nil, nil))

	if _, err := store.Get(ctx, "tenant-a"); !errors.Is(err, ErrInvalidSubscription) {
		t.Fatalf("Get(invalid row) error = %v, want ErrInvalidSubscription", err)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreListAll(t *testing.T) {
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectMySQL)
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	rows := sqlmock.NewRows([]string{"tenant_id", "plan_id", "status", "start_date", "end_date", "current_period_end", "grace_period_end"})
	rows.AddRow("tenant-a", "plan-1", "active", start, nil, nil, nil)
	rows.AddRow("tenant-b", "plan-2", "expired", start, nil, nil, nil)

	list := mock.ExpectQuery(regexp.QuoteMeta("SELECT tenant_id, plan_id, status, start_date, end_date, current_period_end, grace_period_end FROM saas_subscriptions ORDER BY tenant_id"))
	list.WillReturnRows(rows)

	got, err := store.List(ctx, ListFilter{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(got) != 2 || got[0].TenantID != "tenant-a" || got[1].TenantID != "tenant-b" {
		t.Fatalf("List() = %#v, want two ordered rows", got)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreListWithFiltersLimitOffset(t *testing.T) {
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectPostgres)
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	query := "SELECT tenant_id, plan_id, status, start_date, end_date, current_period_end, grace_period_end FROM saas_subscriptions WHERE tenant_id IN ($1, $2) AND plan_id IN ($3) AND status IN ($4) ORDER BY tenant_id LIMIT $5 OFFSET $6"
	rows := sqlmock.NewRows([]string{"tenant_id", "plan_id", "status", "start_date", "end_date", "current_period_end", "grace_period_end"})
	rows.AddRow("tenant-a", "plan-1", "active", start, nil, nil, nil)

	list := mock.ExpectQuery(regexp.QuoteMeta(query))
	list.WithArgs("tenant-a", "tenant-b", "plan-1", "active", 10, 2).WillReturnRows(rows)

	filter := ListFilter{
		TenantIDs: []types.TenantID{"tenant-a", "tenant-b"},
		PlanIDs:   []string{"plan-1"},
		Statuses:  []Status{StatusActive},
		Limit:     10,
		Offset:    2,
	}
	got, err := store.List(ctx, filter)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(got) != 1 || got[0].TenantID != "tenant-a" {
		t.Fatalf("List() = %#v, want one row", got)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreListQueryError(t *testing.T) {
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectMySQL)
	list := mock.ExpectQuery(regexp.QuoteMeta("SELECT tenant_id, plan_id, status, start_date, end_date, current_period_end, grace_period_end FROM saas_subscriptions ORDER BY tenant_id"))
	list.WillReturnError(errors.New("boom"))

	if _, err := store.List(ctx, ListFilter{}); err == nil || err.Error() != "boom" {
		t.Fatalf("List() error = %v, want boom", err)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreListScanAndRowsErrors(t *testing.T) {
	t.Run("scan error", func(t *testing.T) {
		ctx := context.Background()
		store, mock := newMockSQLStore(t, SQLDialectMySQL)
		rows := sqlmock.NewRows([]string{"tenant_id"}).AddRow("tenant-a") // too few columns
		list := mock.ExpectQuery(regexp.QuoteMeta("SELECT tenant_id, plan_id, status, start_date, end_date, current_period_end, grace_period_end FROM saas_subscriptions ORDER BY tenant_id"))
		list.WillReturnRows(rows)

		if _, err := store.List(ctx, ListFilter{}); err == nil {
			t.Fatal("List(scan error) error = nil, want error")
		}
		assertSQLMockExpectations(t, mock)
	})

	t.Run("rows err", func(t *testing.T) {
		ctx := context.Background()
		store, mock := newMockSQLStore(t, SQLDialectMySQL)
		start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
		rows := sqlmock.NewRows([]string{"tenant_id", "plan_id", "status", "start_date", "end_date", "current_period_end", "grace_period_end"})
		rows.AddRow("tenant-a", "plan-1", "active", start, nil, nil, nil)
		// RowError(0,...) makes Next() fail before the row is scanned,
		// which surfaces via rows.Err() rather than the scan path.
		rows.RowError(0, errors.New("row boom"))
		list := mock.ExpectQuery(regexp.QuoteMeta("SELECT tenant_id, plan_id, status, start_date, end_date, current_period_end, grace_period_end FROM saas_subscriptions ORDER BY tenant_id"))
		list.WillReturnRows(rows)

		if _, err := store.List(ctx, ListFilter{}); err == nil || err.Error() != "row boom" {
			t.Fatalf("List() rows err = %v, want row boom", err)
		}
		assertSQLMockExpectations(t, mock)
	})
}

func TestSQLStoreListValidation(t *testing.T) {
	store, _ := newMockSQLStore(t, SQLDialectMySQL)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.List(cancelled, ListFilter{}); err == nil {
		t.Fatal("List() cancelled ctx error = nil, want error")
	}
	if _, err := store.List(context.Background(), ListFilter{Limit: -1}); !errors.Is(err, ErrInvalidListFilter) {
		t.Fatalf("List(negative limit) error = %v, want ErrInvalidListFilter", err)
	}
	if _, err := store.List(context.Background(), ListFilter{Offset: 1}); !errors.Is(err, ErrInvalidListFilter) {
		t.Fatalf("List(offset w/o limit) error = %v, want ErrInvalidListFilter", err)
	}
	if _, err := store.List(context.Background(), ListFilter{Statuses: []Status{Status("bogus")}}); !errors.Is(err, ErrInvalidListFilter) {
		t.Fatalf("List(bad status) error = %v, want ErrInvalidListFilter", err)
	}
}

func TestSQLStoreListPageWithCursor(t *testing.T) {
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectMySQL)
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	query := "SELECT tenant_id, plan_id, status, start_date, end_date, current_period_end, grace_period_end FROM saas_subscriptions WHERE tenant_id > ? ORDER BY tenant_id LIMIT ?"
	rows := sqlmock.NewRows([]string{"tenant_id", "plan_id", "status", "start_date", "end_date", "current_period_end", "grace_period_end"})
	rows.AddRow("tenant-b", "plan-2", "active", start, nil, nil, nil)

	list := mock.ExpectQuery(regexp.QuoteMeta(query))
	list.WithArgs("tenant-a", 5).WillReturnRows(rows)

	got, err := store.ListPage(ctx, PageFilter{Limit: 5, Cursor: "tenant-a"})
	if err != nil {
		t.Fatalf("ListPage() error = %v", err)
	}
	if len(got) != 1 || got[0].TenantID != "tenant-b" {
		t.Fatalf("ListPage() = %#v, want one row after cursor", got)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreListPageValidationAndErrors(t *testing.T) {
	store, _ := newMockSQLStore(t, SQLDialectMySQL)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.ListPage(cancelled, PageFilter{}); err == nil {
		t.Fatal("ListPage() cancelled ctx error = nil, want error")
	}
	if _, err := store.ListPage(context.Background(), PageFilter{Cursor: "t", Offset: 1}); !errors.Is(err, ErrInvalidListFilter) {
		t.Fatalf("ListPage(cursor+offset) error = %v, want ErrInvalidListFilter", err)
	}
}

func TestSQLStoreUpdateSuccess(t *testing.T) {
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectMySQL)
	sub := testSubscription()

	update := mock.ExpectExec(regexp.QuoteMeta("UPDATE saas_subscriptions SET plan_id = ?, status = ?, start_date = ?, end_date = ?, current_period_end = ?, grace_period_end = ? WHERE tenant_id = ?"))
	update.WithArgs("plan-1", "active", sub.StartDate, *sub.EndDate, *sub.CurrentPeriodEnd, *sub.GracePeriodEnd, "tenant-a").WillReturnResult(sqlmock.NewResult(0, 1))

	if err := store.Update(ctx, sub); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreUpdateValidationAndErrors(t *testing.T) {
	t.Run("cancelled ctx", func(t *testing.T) {
		store, _ := newMockSQLStore(t, SQLDialectMySQL)
		cancelled, cancel := context.WithCancel(context.Background())
		cancel()
		if err := store.Update(cancelled, testSubscription()); err == nil {
			t.Fatal("Update() cancelled ctx error = nil, want error")
		}
	})

	t.Run("invalid", func(t *testing.T) {
		store, _ := newMockSQLStore(t, SQLDialectMySQL)
		bad := testSubscription()
		bad.TenantID = ""
		if err := store.Update(context.Background(), bad); !errors.Is(err, ErrInvalidSubscription) {
			t.Fatalf("Update(invalid) error = %v, want ErrInvalidSubscription", err)
		}
	})

	t.Run("exec error", func(t *testing.T) {
		ctx := context.Background()
		store, mock := newMockSQLStore(t, SQLDialectMySQL)
		sub := testSubscription()
		update := mock.ExpectExec(regexp.QuoteMeta("UPDATE saas_subscriptions SET plan_id = ?, status = ?, start_date = ?, end_date = ?, current_period_end = ?, grace_period_end = ? WHERE tenant_id = ?"))
		update.WithArgs("plan-1", "active", sub.StartDate, *sub.EndDate, *sub.CurrentPeriodEnd, *sub.GracePeriodEnd, "tenant-a").WillReturnError(errors.New("boom"))
		if err := store.Update(ctx, sub); err == nil || err.Error() != "boom" {
			t.Fatalf("Update(exec error) error = %v, want boom", err)
		}
	})
}

func TestSQLStoreUpdateRowsAffectedError(t *testing.T) {
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectMySQL)
	sub := testSubscription()
	update := mock.ExpectExec(regexp.QuoteMeta("UPDATE saas_subscriptions SET plan_id = ?, status = ?, start_date = ?, end_date = ?, current_period_end = ?, grace_period_end = ? WHERE tenant_id = ?"))
	update.WithArgs("plan-1", "active", sub.StartDate, *sub.EndDate, *sub.CurrentPeriodEnd, *sub.GracePeriodEnd, "tenant-a").WillReturnResult(failingResult{})

	if err := store.Update(ctx, sub); err == nil {
		t.Fatal("Update(rows affected error) error = nil, want error")
	}
}

func TestSQLStoreUpdateConfirmEqual(t *testing.T) {
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectMySQL)
	sub := testSubscription()

	update := mock.ExpectExec(regexp.QuoteMeta("UPDATE saas_subscriptions SET plan_id = ?, status = ?, start_date = ?, end_date = ?, current_period_end = ?, grace_period_end = ? WHERE tenant_id = ?"))
	update.WithArgs("plan-1", "active", sub.StartDate, *sub.EndDate, *sub.CurrentPeriodEnd, *sub.GracePeriodEnd, "tenant-a").WillReturnResult(sqlmock.NewResult(0, 0))

	// requireUpdatedRow falls back to Get and confirms equality.
	get := mock.ExpectQuery(regexp.QuoteMeta("SELECT tenant_id, plan_id, status, start_date, end_date, current_period_end, grace_period_end FROM saas_subscriptions WHERE tenant_id = ?"))
	get.WithArgs("tenant-a").WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "plan_id", "status", "start_date", "end_date", "current_period_end", "grace_period_end"}).AddRow("tenant-a", "plan-1", "active", sub.StartDate, *sub.EndDate, *sub.CurrentPeriodEnd, *sub.GracePeriodEnd))

	if err := store.Update(ctx, sub); err != nil {
		t.Fatalf("Update(confirm equal) error = %v, want nil", err)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreUpdateConfirmConflict(t *testing.T) {
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectMySQL)
	sub := testSubscription()

	update := mock.ExpectExec(regexp.QuoteMeta("UPDATE saas_subscriptions SET plan_id = ?, status = ?, start_date = ?, end_date = ?, current_period_end = ?, grace_period_end = ? WHERE tenant_id = ?"))
	update.WithArgs("plan-1", "active", sub.StartDate, *sub.EndDate, *sub.CurrentPeriodEnd, *sub.GracePeriodEnd, "tenant-a").WillReturnResult(sqlmock.NewResult(0, 0))

	// Current row differs from desired -> conflict.
	other := time.Date(1990, 1, 1, 0, 0, 0, 0, time.UTC)
	get := mock.ExpectQuery(regexp.QuoteMeta("SELECT tenant_id, plan_id, status, start_date, end_date, current_period_end, grace_period_end FROM saas_subscriptions WHERE tenant_id = ?"))
	get.WithArgs("tenant-a").WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "plan_id", "status", "start_date", "end_date", "current_period_end", "grace_period_end"}).AddRow("tenant-a", "plan-different", "active", other, nil, nil, nil))

	if err := store.Update(ctx, sub); !errors.Is(err, ErrSubscriptionConflict) {
		t.Fatalf("Update(confirm conflict) error = %v, want ErrSubscriptionConflict", err)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreUpdateConfirmGetError(t *testing.T) {
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectMySQL)
	sub := testSubscription()

	update := mock.ExpectExec(regexp.QuoteMeta("UPDATE saas_subscriptions SET plan_id = ?, status = ?, start_date = ?, end_date = ?, current_period_end = ?, grace_period_end = ? WHERE tenant_id = ?"))
	update.WithArgs("plan-1", "active", sub.StartDate, *sub.EndDate, *sub.CurrentPeriodEnd, *sub.GracePeriodEnd, "tenant-a").WillReturnResult(sqlmock.NewResult(0, 0))

	get := mock.ExpectQuery(regexp.QuoteMeta("SELECT tenant_id, plan_id, status, start_date, end_date, current_period_end, grace_period_end FROM saas_subscriptions WHERE tenant_id = ?"))
	get.WithArgs("tenant-a").WillReturnError(errors.New("get boom"))

	if err := store.Update(ctx, sub); err == nil || err.Error() != "get boom" {
		t.Fatalf("Update(confirm get error) error = %v, want get boom", err)
	}
}

func TestSQLStoreDeleteRoundTrip(t *testing.T) {
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectMySQL)
	del := mock.ExpectExec(regexp.QuoteMeta("DELETE FROM saas_subscriptions WHERE tenant_id = ?"))
	del.WithArgs("tenant-a").WillReturnResult(sqlmock.NewResult(0, 1))

	if err := store.Delete(ctx, "tenant-a"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreDeleteValidationAndErrors(t *testing.T) {
	t.Run("cancelled ctx", func(t *testing.T) {
		store, _ := newMockSQLStore(t, SQLDialectMySQL)
		cancelled, cancel := context.WithCancel(context.Background())
		cancel()
		if err := store.Delete(cancelled, "tenant-a"); err == nil {
			t.Fatal("Delete() cancelled ctx error = nil, want error")
		}
	})

	t.Run("empty tenant", func(t *testing.T) {
		store, _ := newMockSQLStore(t, SQLDialectMySQL)
		if err := store.Delete(context.Background(), ""); !errors.Is(err, ErrInvalidSubscription) {
			t.Fatalf("Delete(empty) error = %v, want ErrInvalidSubscription", err)
		}
	})

	t.Run("exec error", func(t *testing.T) {
		ctx := context.Background()
		store, mock := newMockSQLStore(t, SQLDialectMySQL)
		del := mock.ExpectExec(regexp.QuoteMeta("DELETE FROM saas_subscriptions WHERE tenant_id = ?"))
		del.WithArgs("tenant-a").WillReturnError(errors.New("boom"))
		if err := store.Delete(ctx, "tenant-a"); err == nil || err.Error() != "boom" {
			t.Fatalf("Delete(exec error) error = %v, want boom", err)
		}
	})

	t.Run("rows affected error", func(t *testing.T) {
		ctx := context.Background()
		store, mock := newMockSQLStore(t, SQLDialectMySQL)
		del := mock.ExpectExec(regexp.QuoteMeta("DELETE FROM saas_subscriptions WHERE tenant_id = ?"))
		del.WithArgs("tenant-a").WillReturnResult(failingResult{})
		if err := store.Delete(ctx, "tenant-a"); err == nil {
			t.Fatal("Delete(rows affected error) error = nil, want error")
		}
	})

	t.Run("not found", func(t *testing.T) {
		ctx := context.Background()
		store, mock := newMockSQLStore(t, SQLDialectMySQL)
		del := mock.ExpectExec(regexp.QuoteMeta("DELETE FROM saas_subscriptions WHERE tenant_id = ?"))
		del.WithArgs("missing").WillReturnResult(sqlmock.NewResult(0, 0))
		if err := store.Delete(ctx, "missing"); !errors.Is(err, ErrSubscriptionNotFound) {
			t.Fatalf("Delete(not found) error = %v, want ErrSubscriptionNotFound", err)
		}
		assertSQLMockExpectations(t, mock)
	})
}

// placeholderFor builds the comma-separated placeholders exactly as the store would.
func placeholderFor(t *testing.T, store *SQLStore, count, start int) string {
	t.Helper()
	return store.placeholders(count, start)
}

func testSubscription() Subscription {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC)
	currentPeriodEnd := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	gracePeriodEnd := time.Date(2025, 1, 15, 0, 0, 0, 0, time.UTC)
	return Subscription{
		TenantID:         "tenant-a",
		PlanID:           "plan-1",
		Status:           StatusActive,
		StartDate:        start,
		EndDate:          &end,
		CurrentPeriodEnd: &currentPeriodEnd,
		GracePeriodEnd:   &gracePeriodEnd,
	}
}

func newMockSQLStore(t *testing.T, dialect SQLDialect) (*SQLStore, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := NewSQLStore(db, WithSQLDialect(dialect))
	if err != nil {
		t.Fatalf("NewSQLStore() error = %v", err)
	}
	return store, mock
}

func assertSQLMockExpectations(t *testing.T, mock sqlmock.Sqlmock) {
	t.Helper()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sqlmock expectations: %v", err)
	}
}

// compile-time check that failingResult satisfies driver.Result
var _ driver.Result = failingResult{}

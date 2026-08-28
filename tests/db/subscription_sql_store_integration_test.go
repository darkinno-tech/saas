package db_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/im10furry/saas/core/types"
	"github.com/im10furry/saas/subscription"
)

func TestSubscriptionSQLStoreMySQLIntegration(t *testing.T) {
	runSubscriptionSQLStoreIntegration(t, "mysql", os.Getenv("SAAS_MYSQL_DSN"), resetMySQLSubscriptionsTable, func(db *sql.DB) (*subscription.SQLStore, error) {
		return subscription.NewSQLStore(db)
	})
}

func TestSubscriptionSQLStorePostgresIntegration(t *testing.T) {
	runSubscriptionSQLStoreIntegration(t, "postgres", os.Getenv("SAAS_POSTGRES_DSN"), resetPostgresSubscriptionsTable, func(db *sql.DB) (*subscription.SQLStore, error) {
		return subscription.NewSQLStore(db, subscription.WithSQLDialect(subscription.SQLDialectPostgres))
	})
}

func runSubscriptionSQLStoreIntegration(t *testing.T, driver, dsn string, reset func(*testing.T, context.Context, *sql.DB), newStore func(*sql.DB) (*subscription.SQLStore, error)) {
	t.Helper()
	if dsn == "" {
		t.Skipf("set the %s DSN to run subscription SQL integration tests", driver)
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
	runSubscriptionSQLStoreContract(t, ctx, store)
}

func runSubscriptionSQLStoreContract(t *testing.T, ctx context.Context, store *subscription.SQLStore) {
	t.Helper()
	start := time.Date(2026, 7, 17, 8, 9, 10, 123456000, time.UTC)
	periodEnd := start.Add(30 * 24 * time.Hour)
	graceEnd := periodEnd.Add(72 * time.Hour)
	endB := start.Add(90 * 24 * time.Hour)

	subscriptions := []subscription.Subscription{
		{
			TenantID:         "tenant-a",
			PlanID:           "starter",
			Status:           subscription.StatusActive,
			StartDate:        start,
			CurrentPeriodEnd: &periodEnd,
			GracePeriodEnd:   &graceEnd,
		},
		{
			TenantID:  "tenant-b",
			PlanID:    "pro",
			Status:    subscription.StatusActive,
			StartDate: start.Add(time.Hour),
			EndDate:   &endB,
		},
		{
			TenantID:  "tenant-c",
			PlanID:    "starter",
			Status:    subscription.StatusExpired,
			StartDate: start.Add(2 * time.Hour),
		},
		{
			TenantID:  "tenant-d",
			PlanID:    "starter",
			Status:    subscription.StatusActive,
			StartDate: start.Add(3 * time.Hour),
		},
	}
	for _, value := range subscriptions {
		if err := store.Create(ctx, value); err != nil {
			t.Fatalf("Create(%s) error = %v", value.TenantID, err)
		}
	}
	if err := store.Create(ctx, subscriptions[0]); !errors.Is(err, subscription.ErrSubscriptionAlreadyExists) {
		t.Fatalf("Create(duplicate) error = %v, want ErrSubscriptionAlreadyExists", err)
	}

	gotA, err := store.Get(ctx, "tenant-a")
	if err != nil {
		t.Fatalf("Get(tenant A) error = %v", err)
	}
	assertSubscriptionEqual(t, gotA, subscriptions[0])
	if gotA.EndDate != nil {
		t.Fatalf("Get(tenant A) EndDate = %v, want nil", gotA.EndDate)
	}

	matching, err := store.List(ctx, subscription.ListFilter{
		TenantIDs: []types.TenantID{"tenant-a", "tenant-b", "tenant-c"},
		PlanIDs:   []string{"starter"},
		Statuses:  []subscription.Status{subscription.StatusActive},
	})
	if err != nil {
		t.Fatalf("List(intersection) error = %v", err)
	}
	assertSubscriptionTenantIDs(t, matching, "tenant-a")

	offsetPage, err := store.List(ctx, subscription.ListFilter{Limit: 2, Offset: 1})
	if err != nil {
		t.Fatalf("List(offset) error = %v", err)
	}
	assertSubscriptionTenantIDs(t, offsetPage, "tenant-b", "tenant-c")

	cursorPage, err := store.ListPage(ctx, subscription.PageFilter{Cursor: "tenant-a", Limit: 2})
	if err != nil {
		t.Fatalf("ListPage(cursor) error = %v", err)
	}
	assertSubscriptionTenantIDs(t, cursorPage, "tenant-b", "tenant-c")

	cursorFiltered, err := store.ListPage(ctx, subscription.PageFilter{
		TenantIDs: []types.TenantID{"tenant-b", "tenant-c", "tenant-d"},
		PlanIDs:   []string{"starter"},
		Statuses:  []subscription.Status{subscription.StatusActive},
		Cursor:    "tenant-a",
		Limit:     1,
	})
	if err != nil {
		t.Fatalf("ListPage(cursor and intersection) error = %v", err)
	}
	assertSubscriptionTenantIDs(t, cursorFiltered, "tenant-d")

	updatedA := gotA
	updatedEnd := start.Add(120 * 24 * time.Hour)
	updatedA.PlanID = "enterprise"
	updatedA.Status = subscription.StatusCancelled
	updatedA.EndDate = &updatedEnd
	updatedA.CurrentPeriodEnd = nil
	updatedA.GracePeriodEnd = nil
	if err := store.Update(ctx, updatedA); err != nil {
		t.Fatalf("Update(tenant A) error = %v", err)
	}
	gotA, err = store.Get(ctx, "tenant-a")
	if err != nil {
		t.Fatalf("Get(updated tenant A) error = %v", err)
	}
	assertSubscriptionEqual(t, gotA, updatedA)
	if err := store.Update(ctx, gotA); err != nil {
		t.Fatalf("Update(identical tenant A) error = %v", err)
	}

	missing := updatedA
	missing.TenantID = "missing"
	if err := store.Update(ctx, missing); !errors.Is(err, subscription.ErrSubscriptionNotFound) {
		t.Fatalf("Update(missing) error = %v, want ErrSubscriptionNotFound", err)
	}

	if err := store.Delete(ctx, "tenant-a"); err != nil {
		t.Fatalf("Delete(tenant A) error = %v", err)
	}
	if _, err := store.Get(ctx, "tenant-a"); !errors.Is(err, subscription.ErrSubscriptionNotFound) {
		t.Fatalf("Get(deleted tenant A) error = %v, want ErrSubscriptionNotFound", err)
	}
	if err := store.Delete(ctx, "tenant-a"); !errors.Is(err, subscription.ErrSubscriptionNotFound) {
		t.Fatalf("Delete(missing tenant A) error = %v, want ErrSubscriptionNotFound", err)
	}
	gotB, err := store.Get(ctx, "tenant-b")
	if err != nil {
		t.Fatalf("Get(tenant B after tenant-A changes) error = %v", err)
	}
	assertSubscriptionEqual(t, gotB, subscriptions[1])
}

func assertSubscriptionTenantIDs(t *testing.T, got []subscription.Subscription, want ...types.TenantID) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("subscriptions = %+v, want %d rows", got, len(want))
	}
	for index, tenantID := range want {
		if got[index].TenantID != tenantID {
			t.Fatalf("subscriptions[%d].TenantID = %q, want %q; rows = %+v", index, got[index].TenantID, tenantID, got)
		}
	}
}

func assertSubscriptionEqual(t *testing.T, got, want subscription.Subscription) {
	t.Helper()
	if got.TenantID != want.TenantID || got.PlanID != want.PlanID || got.Status != want.Status || !got.StartDate.Equal(want.StartDate) || !equalTimePointer(got.EndDate, want.EndDate) || !equalTimePointer(got.CurrentPeriodEnd, want.CurrentPeriodEnd) || !equalTimePointer(got.GracePeriodEnd, want.GracePeriodEnd) {
		t.Fatalf("subscription = %+v, want %+v", got, want)
	}
}

func equalTimePointer(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

func resetMySQLSubscriptionsTable(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	resetSubscriptionsTable(t, ctx, db, []string{
		"DROP TABLE IF EXISTS saas_subscriptions",
		`CREATE TABLE saas_subscriptions (
			tenant_id VARCHAR(191) NOT NULL PRIMARY KEY,
			plan_id VARCHAR(191) NOT NULL,
			status VARCHAR(32) NOT NULL,
			start_date DATETIME(6) NOT NULL,
			end_date DATETIME(6) NULL,
			current_period_end DATETIME(6) NULL,
			grace_period_end DATETIME(6) NULL
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	})
}

func resetPostgresSubscriptionsTable(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	resetSubscriptionsTable(t, ctx, db, []string{
		"DROP TABLE IF EXISTS saas_subscriptions",
		`CREATE TABLE saas_subscriptions (
			tenant_id VARCHAR(191) PRIMARY KEY,
			plan_id VARCHAR(191) NOT NULL,
			status VARCHAR(32) NOT NULL,
			start_date TIMESTAMPTZ NOT NULL,
			end_date TIMESTAMPTZ NULL,
			current_period_end TIMESTAMPTZ NULL,
			grace_period_end TIMESTAMPTZ NULL
		)`,
	})
}

func resetSubscriptionsTable(t *testing.T, ctx context.Context, db *sql.DB, statements []string) {
	t.Helper()
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("exec %q error = %v", statement, err)
		}
	}
}

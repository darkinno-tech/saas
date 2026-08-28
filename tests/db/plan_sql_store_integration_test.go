package db_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/darkinno-tech/saas/plan"
)

func TestPlanSQLStoreMySQLIntegration(t *testing.T) {
	runPlanSQLStoreIntegration(t, "mysql", os.Getenv("SAAS_MYSQL_DSN"), resetMySQLPlansTable, func(db *sql.DB) (*plan.SQLStore, error) {
		return plan.NewSQLStore(db)
	})
}

func TestPlanSQLStorePostgresIntegration(t *testing.T) {
	runPlanSQLStoreIntegration(t, "postgres", os.Getenv("SAAS_POSTGRES_DSN"), resetPostgresPlansTable, func(db *sql.DB) (*plan.SQLStore, error) {
		return plan.NewSQLStore(db, plan.WithSQLDialect(plan.SQLDialectPostgres))
	})
}

func runPlanSQLStoreIntegration(t *testing.T, driver, dsn string, reset func(*testing.T, context.Context, *sql.DB), newStore func(*sql.DB) (*plan.SQLStore, error)) {
	t.Helper()
	if dsn == "" {
		t.Skipf("set the %s DSN to run plan SQL integration tests", driver)
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
	runPlanSQLStoreContract(t, ctx, store)
}

func runPlanSQLStoreContract(t *testing.T, ctx context.Context, store *plan.SQLStore) {
	t.Helper()
	starter := sqlIntegrationPlan("starter", "Starter")
	enterprise := sqlIntegrationPlan("enterprise", "Enterprise")
	pro := sqlIntegrationPlan("pro", "Pro")
	pro.Features[0].Config = map[string]string{"retention_days": "180", "regions": "2"}
	pro.Quotas[0].Limit = 1_000

	// Insert out of order so every read path has to preserve the SQL ORDER BY id contract.
	for _, candidate := range []plan.Plan{starter, enterprise, pro} {
		if err := store.Create(ctx, candidate); err != nil {
			t.Fatalf("Create(%q) error = %v", candidate.ID, err)
		}
	}
	if err := store.Create(ctx, starter); !errors.Is(err, plan.ErrPlanAlreadyExists) {
		t.Fatalf("Create(duplicate starter) error = %v, want ErrPlanAlreadyExists", err)
	}

	got, err := store.Get(ctx, "starter")
	if err != nil {
		t.Fatalf("Get(starter) error = %v", err)
	}
	if !reflect.DeepEqual(got, starter) {
		t.Fatalf("Get(starter) = %#v, want %#v; JSON round trip changed the plan", got, starter)
	}

	plans, err := store.List(ctx, plan.ListFilter{})
	if err != nil {
		t.Fatalf("List(all) error = %v", err)
	}
	if gotIDs, wantIDs := planIDs(plans), []string{"enterprise", "pro", "starter"}; !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("List(all) IDs = %v, want sorted %v", gotIDs, wantIDs)
	}

	plans, err = store.List(ctx, plan.ListFilter{IDs: []string{"starter", "enterprise"}})
	if err != nil {
		t.Fatalf("List(filtered) error = %v", err)
	}
	if gotIDs, wantIDs := planIDs(plans), []string{"enterprise", "starter"}; !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("List(filtered) IDs = %v, want %v", gotIDs, wantIDs)
	}

	plans, err = store.List(ctx, plan.ListFilter{Limit: 1, Offset: 1})
	if err != nil {
		t.Fatalf("List(offset page) error = %v", err)
	}
	if gotIDs, wantIDs := planIDs(plans), []string{"pro"}; !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("List(offset page) IDs = %v, want %v", gotIDs, wantIDs)
	}

	plans, err = store.ListPage(ctx, plan.PageFilter{Cursor: "enterprise", Limit: 1})
	if err != nil {
		t.Fatalf("ListPage(cursor) error = %v", err)
	}
	if gotIDs, wantIDs := planIDs(plans), []string{"pro"}; !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("ListPage(cursor) IDs = %v, want %v", gotIDs, wantIDs)
	}

	plans, err = store.ListPage(ctx, plan.PageFilter{IDs: []string{"enterprise", "starter"}, Cursor: "enterprise", Limit: 1})
	if err != nil {
		t.Fatalf("ListPage(filtered cursor) error = %v", err)
	}
	if gotIDs, wantIDs := planIDs(plans), []string{"starter"}; !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("ListPage(filtered cursor) IDs = %v, want %v", gotIDs, wantIDs)
	}

	updatedStarter := sqlIntegrationPlan("starter", "Starter Plus")
	updatedStarter.Features = []plan.Feature{
		{Key: "analytics", Enabled: true, Config: map[string]string{"retention_days": "365", "regions": "3"}},
		{Key: "members", Enabled: true, Config: map[string]string{"seats": "25"}},
	}
	updatedStarter.Quotas = []plan.Quota{
		{Resource: "api_calls", Limit: 2_000, Period: plan.QuotaPeriodMonth},
		{Resource: "storage_gb", Limit: 500, Period: plan.QuotaPeriodNone},
	}
	if err := store.Update(ctx, updatedStarter); err != nil {
		t.Fatalf("Update(starter) error = %v", err)
	}
	got, err = store.Get(ctx, "starter")
	if err != nil {
		t.Fatalf("Get(updated starter) error = %v", err)
	}
	if !reflect.DeepEqual(got, updatedStarter) {
		t.Fatalf("Get(updated starter) = %#v, want %#v; JSON round trip changed the update", got, updatedStarter)
	}

	// MySQL reports zero affected rows for an identical UPDATE by default. The
	// store must read the row back and accept it rather than report it missing.
	if err := store.Update(ctx, updatedStarter); err != nil {
		t.Fatalf("Update(unchanged starter) error = %v, want successful read-back", err)
	}

	got, err = store.Get(ctx, "pro")
	if err != nil {
		t.Fatalf("Get(pro after starter update) error = %v", err)
	}
	if !reflect.DeepEqual(got, pro) {
		t.Fatalf("Get(pro after starter update) = %#v, want %#v; update leaked across plans", got, pro)
	}

	missing := sqlIntegrationPlan("missing", "Missing")
	if err := store.Update(ctx, missing); !errors.Is(err, plan.ErrPlanNotFound) {
		t.Fatalf("Update(missing) error = %v, want ErrPlanNotFound", err)
	}

	if err := store.Delete(ctx, "starter"); err != nil {
		t.Fatalf("Delete(starter) error = %v", err)
	}
	if _, err := store.Get(ctx, "starter"); !errors.Is(err, plan.ErrPlanNotFound) {
		t.Fatalf("Get(deleted starter) error = %v, want ErrPlanNotFound", err)
	}
	got, err = store.Get(ctx, "pro")
	if err != nil {
		t.Fatalf("Get(pro after starter delete) error = %v", err)
	}
	if !reflect.DeepEqual(got, pro) {
		t.Fatalf("Get(pro after starter delete) = %#v, want %#v; delete leaked across plans", got, pro)
	}
	if err := store.Delete(ctx, "missing"); !errors.Is(err, plan.ErrPlanNotFound) {
		t.Fatalf("Delete(missing) error = %v, want ErrPlanNotFound", err)
	}
}

func sqlIntegrationPlan(id, name string) plan.Plan {
	return plan.Plan{
		ID:   id,
		Name: name,
		Features: []plan.Feature{
			{Key: "analytics", Enabled: true, Config: map[string]string{"retention_days": "90", "regions": "1"}},
			{Key: "members", Enabled: true, Config: map[string]string{"seats": "5"}},
		},
		Quotas: []plan.Quota{
			{Resource: "api_calls", Limit: 100, Period: plan.QuotaPeriodMonth},
			{Resource: "storage_gb", Limit: 10, Period: plan.QuotaPeriodNone},
		},
	}
}

func planIDs(plans []plan.Plan) []string {
	ids := make([]string, len(plans))
	for index, candidate := range plans {
		ids[index] = candidate.ID
	}
	return ids
}

func resetMySQLPlansTable(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	resetPlansTable(t, ctx, db, []string{
		"DROP TABLE IF EXISTS saas_plans",
		`CREATE TABLE saas_plans (
			id VARCHAR(191) NOT NULL PRIMARY KEY,
			name VARCHAR(255) NOT NULL,
			features JSON NOT NULL,
			quotas JSON NOT NULL
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	})
}

func resetPostgresPlansTable(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	resetPlansTable(t, ctx, db, []string{
		"DROP TABLE IF EXISTS saas_plans",
		`CREATE TABLE saas_plans (
			id VARCHAR(191) PRIMARY KEY,
			name VARCHAR(255) NOT NULL,
			features JSONB NOT NULL,
			quotas JSONB NOT NULL
		)`,
	})
}

func resetPlansTable(t *testing.T, ctx context.Context, db *sql.DB, statements []string) {
	t.Helper()
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("exec %q error = %v", statement, err)
		}
	}
}

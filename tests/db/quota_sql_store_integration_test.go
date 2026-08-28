package db_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/darkinno-tech/saas/quota"
)

func TestQuotaSQLStoreMySQLIntegration(t *testing.T) {
	runQuotaSQLStoreIntegration(t, "mysql", os.Getenv("SAAS_MYSQL_DSN"), resetMySQLQuotaUsageTable, func(db *sql.DB) (*quota.SQLStore, error) {
		return quota.NewSQLStore(db)
	})
}

func TestQuotaSQLStorePostgresIntegration(t *testing.T) {
	runQuotaSQLStoreIntegration(t, "postgres", os.Getenv("SAAS_POSTGRES_DSN"), resetPostgresQuotaUsageTable, func(db *sql.DB) (*quota.SQLStore, error) {
		return quota.NewSQLStore(db, quota.WithSQLDialect(quota.SQLDialectPostgres))
	})
}

func runQuotaSQLStoreIntegration(t *testing.T, driver, dsn string, reset func(*testing.T, context.Context, *sql.DB), newStore func(*sql.DB) (*quota.SQLStore, error)) {
	t.Helper()
	if dsn == "" {
		t.Skipf("set the %s DSN to run quota SQL integration tests", driver)
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
	runQuotaStoreContract(t, ctx, quota.NewService(store))
}

func runQuotaStoreContract(t *testing.T, ctx context.Context, service *quota.Service) {
	t.Helper()
	limit := quota.Limit{TenantID: "tenant-a", Resource: "api_calls", Limit: 7, Period: quota.PeriodDay}

	usage, err := service.Check(ctx, limit, 0)
	if err != nil {
		t.Fatalf("Check(initial) error = %v", err)
	}
	if usage.Used != 0 {
		t.Fatalf("Check(initial) used = %d, want 0", usage.Used)
	}

	for _, amount := range []int64{2, 3} {
		usage, err = service.Consume(ctx, limit, amount)
		if err != nil {
			t.Fatalf("Consume(%d) error = %v", amount, err)
		}
	}
	if usage.Used != 5 || usage.Limit != limit.Limit {
		t.Fatalf("usage after sequential consumes = %+v, want used 5 limit %d", usage, limit.Limit)
	}

	if _, err := service.Consume(ctx, limit, 3); !errors.Is(err, quota.ErrQuotaExceeded) {
		t.Fatalf("Consume(over limit) error = %v, want ErrQuotaExceeded", err)
	}
	usage, err = service.Check(ctx, limit, 0)
	if err != nil {
		t.Fatalf("Check(after rejected consume) error = %v", err)
	}
	if usage.Used != 5 {
		t.Fatalf("rejected consume changed usage to %d, want 5", usage.Used)
	}

	otherTenant := limit
	otherTenant.TenantID = "tenant-b"
	usage, err = service.Consume(ctx, otherTenant, 4)
	if err != nil {
		t.Fatalf("Consume(other tenant) error = %v", err)
	}
	if usage.Used != 4 {
		t.Fatalf("other tenant usage = %d, want 4", usage.Used)
	}
	usage, err = service.Check(ctx, limit, 0)
	if err != nil {
		t.Fatalf("Check(original tenant) error = %v", err)
	}
	if usage.Used != 5 {
		t.Fatalf("other tenant consume changed original usage to %d, want 5", usage.Used)
	}

	if err := service.Reset(ctx, limit.TenantID, limit.Resource, limit.Period); err != nil {
		t.Fatalf("Reset() error = %v", err)
	}
	usage, err = service.Check(ctx, limit, 0)
	if err != nil {
		t.Fatalf("Check(after reset) error = %v", err)
	}
	if usage.Used != 0 {
		t.Fatalf("usage after reset = %d, want 0", usage.Used)
	}

	concurrent := quota.Limit{TenantID: "tenant-concurrent", Resource: "requests", Limit: 8, Period: quota.PeriodMonth}
	if _, err := service.Consume(ctx, concurrent, 0); err != nil {
		t.Fatalf("Consume(seed zero) error = %v", err)
	}

	const workers = 16
	start := make(chan struct{})
	var wg sync.WaitGroup
	var successes int64
	unexpected := make(chan error, workers)
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			<-start
			_, err := service.Consume(ctx, concurrent, 1)
			switch {
			case err == nil:
				atomic.AddInt64(&successes, 1)
			case errors.Is(err, quota.ErrQuotaExceeded):
			default:
				unexpected <- err
			}
		}()
	}
	close(start)
	wg.Wait()
	close(unexpected)
	for err := range unexpected {
		t.Errorf("concurrent Consume() unexpected error = %v", err)
	}
	if got := atomic.LoadInt64(&successes); got != concurrent.Limit {
		t.Fatalf("concurrent Consume() successes = %d, want %d", got, concurrent.Limit)
	}
	usage, err = service.Check(ctx, concurrent, 0)
	if err != nil {
		t.Fatalf("Check(after concurrent consumes) error = %v", err)
	}
	if usage.Used != concurrent.Limit {
		t.Fatalf("concurrent final usage = %d, want %d", usage.Used, concurrent.Limit)
	}
}

func resetMySQLQuotaUsageTable(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	resetQuotaUsageTable(t, ctx, db, []string{
		"DROP TABLE IF EXISTS saas_quota_usage",
		`CREATE TABLE saas_quota_usage (
			tenant_id VARCHAR(191) NOT NULL,
			resource VARCHAR(191) NOT NULL,
			period VARCHAR(32) NOT NULL,
			used BIGINT NOT NULL,
			PRIMARY KEY (tenant_id, resource, period)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	})
}

func resetPostgresQuotaUsageTable(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	resetQuotaUsageTable(t, ctx, db, []string{
		"DROP TABLE IF EXISTS saas_quota_usage",
		`CREATE TABLE saas_quota_usage (
			tenant_id VARCHAR(191) NOT NULL,
			resource VARCHAR(191) NOT NULL,
			period VARCHAR(32) NOT NULL,
			used BIGINT NOT NULL,
			PRIMARY KEY (tenant_id, resource, period)
		)`,
	})
}

func resetQuotaUsageTable(t *testing.T, ctx context.Context, db *sql.DB, statements []string) {
	t.Helper()
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("exec %q error = %v", statement, err)
		}
	}
}

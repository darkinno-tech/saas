//go:build chaos

package db_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/im10furry/saas/core/store"
	"github.com/im10furry/saas/core/types"
	"github.com/im10furry/saas/internal/testcontract"
	"github.com/im10furry/saas/internal/testtoxiproxy"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
)

const (
	chaosToxiproxyURL = "http://127.0.0.1:58474"
	chaosMySQLDSN     = "root:saas@tcp(127.0.0.1:58666)/saas_test?parseTime=true&timeout=1s&readTimeout=1s&writeTimeout=1s"
	chaosPostgresDSN  = "postgres://saas:saas@127.0.0.1:58667/saas_test?sslmode=disable&connect_timeout=1"
)

type sqlChaosBackend struct {
	name        string
	driver      string
	dsnEnv      string
	expectedDSN string
	proxy       string
	listen      string
	upstream    string
	dialect     store.SQLDialect
	reset       func(*testing.T, context.Context, *sql.DB)
}

func TestSQLChaosEnvironmentValidation(t *testing.T) {
	tests := []struct {
		name     string
		value    string
		expected string
		wantErr  bool
	}{
		{name: "expected loopback DSN", value: "postgres://saas:saas@127.0.0.1:58667/saas_test?sslmode=disable&connect_timeout=1", expected: "postgres://saas:saas@127.0.0.1:58667/saas_test?sslmode=disable&connect_timeout=1"},
		{name: "missing", value: "", expected: "root:saas@tcp(127.0.0.1:58666)/saas_test?parseTime=true&timeout=1s&readTimeout=1s&writeTimeout=1s", wantErr: true},
		{name: "external host", value: "root:saas@tcp(db.example.com:3306)/saas_test", expected: "root:saas@tcp(127.0.0.1:58666)/saas_test?parseTime=true&timeout=1s&readTimeout=1s&writeTimeout=1s", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSQLChaosValue("SAAS_CHAOS_DSN", tt.value, tt.expected)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateSQLChaosValue(%q) error = %v, wantErr %v", tt.value, err, tt.wantErr)
			}
		})
	}
}

func TestSQLStoreChaos(t *testing.T) {
	if os.Getenv("SAAS_CHAOS") != "1" {
		t.Skip("set SAAS_CHAOS=1 to run SQLStore chaos tests")
	}

	toxiproxyURL := requireSQLChaosEnvironment(t, "SAAS_TOXIPROXY_URL", chaosToxiproxyURL)
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer waitCancel()
	toxiproxy := testtoxiproxy.New(toxiproxyURL)
	if err := toxiproxy.Wait(waitCtx); err != nil {
		t.Fatalf("wait for Toxiproxy: %v", err)
	}

	backends := []sqlChaosBackend{
		{
			name:        "mysql",
			driver:      "mysql",
			dsnEnv:      "SAAS_CHAOS_MYSQL_DSN",
			expectedDSN: chaosMySQLDSN,
			proxy:       "saas_mysql",
			listen:      "0.0.0.0:8666",
			upstream:    "mysql:3306",
			dialect:     store.SQLDialectMySQL,
			reset:       resetTenantsTable,
		},
		{
			name:        "postgres",
			driver:      "postgres",
			dsnEnv:      "SAAS_CHAOS_POSTGRES_DSN",
			expectedDSN: chaosPostgresDSN,
			proxy:       "saas_postgres",
			listen:      "0.0.0.0:8667",
			upstream:    "postgres:5432",
			dialect:     store.SQLDialectPostgres,
			reset:       resetPostgresTenantsTable,
		},
	}

	for _, backend := range backends {
		backend := backend
		t.Run(backend.name, func(t *testing.T) {
			dsn := requireSQLChaosEnvironment(t, backend.dsnEnv, backend.expectedDSN)
			runSQLStoreChaos(t, toxiproxy, backend, dsn)
		})
	}
}

func runSQLStoreChaos(t *testing.T, toxiproxy *testtoxiproxy.Client, backend sqlChaosBackend, dsn string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, err := toxiproxy.CreateProxy(ctx, backend.proxy, backend.listen, backend.upstream); err != nil {
		t.Fatalf("CreateProxy(%s) error = %v", backend.name, err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if err := toxiproxy.DeleteProxy(cleanupCtx, backend.proxy); err != nil {
			t.Errorf("DeleteProxy(%s) error = %v", backend.name, err)
		}
	})

	db := openChaosDB(t, backend.driver, dsn)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("db.Close() error = %v", err)
		}
	})
	if err := pingUntilReady(ctx, db); err != nil {
		t.Fatalf("%s proxy not ready: %v", backend.name, err)
	}
	backend.reset(t, ctx, db)

	sqlStore, err := store.NewSQLStore(db, store.WithSQLDialect(backend.dialect))
	if err != nil {
		t.Fatalf("NewSQLStore() error = %v", err)
	}
	original := testcontract.ContractTenant(types.TenantID("sql-chaos-" + backend.name))
	if err := sqlStore.Create(ctx, original); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	expected, err := sqlStore.Get(ctx, original.ID)
	if err != nil {
		t.Fatalf("Get() snapshot error = %v", err)
	}

	first := expected
	first.Name = "First CAS winner"
	first.Status = types.TenantStatusSuspended
	first.PlanID = "first-plan"
	first.Config = map[string]string{"winner": "first", "feature": "off"}
	second := expected
	second.Name = "Second CAS winner"
	second.Status = types.TenantStatusPending
	second.PlanID = "second-plan"
	second.Config = map[string]string{"winner": "second", "feature": "on"}
	runSameSnapshotCAS(t, sqlStore, expected, first, second)

	got, err := sqlStore.Get(ctx, original.ID)
	if err != nil {
		t.Fatalf("Get() after CAS error = %v", err)
	}
	if !reflect.DeepEqual(got, first) && !reflect.DeepEqual(got, second) {
		t.Fatalf("final tenant = %#v, want exactly one complete CAS candidate", got)
	}

	if err := toxiproxy.SetEnabled(ctx, backend.proxy, false); err != nil {
		t.Fatalf("SetEnabled(false) error = %v", err)
	}
	disabledDB := openChaosDB(t, backend.driver, dsn)
	t.Cleanup(func() {
		if err := disabledDB.Close(); err != nil {
			t.Errorf("disabled db.Close() error = %v", err)
		}
	})
	disabledCtx, disabledCancel := context.WithTimeout(context.Background(), 5*time.Second)
	err = disabledDB.PingContext(disabledCtx)
	expired := disabledCtx.Err()
	disabledCancel()
	if err == nil {
		t.Fatal("PingContext() succeeded after the proxy was disabled")
	}
	if expired != nil {
		t.Fatalf("PingContext() did not fail before its deadline: %v", err)
	}

	if err := toxiproxy.SetEnabled(ctx, backend.proxy, true); err != nil {
		t.Fatalf("SetEnabled(true) error = %v", err)
	}
	recoveredDB := openChaosDB(t, backend.driver, dsn)
	t.Cleanup(func() {
		if err := recoveredDB.Close(); err != nil {
			t.Errorf("recovered db.Close() error = %v", err)
		}
	})
	recoveryCtx, recoveryCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer recoveryCancel()
	if err := pingUntilReady(recoveryCtx, recoveredDB); err != nil {
		t.Fatalf("%s proxy did not recover: %v", backend.name, err)
	}
	recoveredStore, err := store.NewSQLStore(recoveredDB, store.WithSQLDialect(backend.dialect))
	if err != nil {
		t.Fatalf("NewSQLStore() after recovery error = %v", err)
	}
	recovered, err := recoveredStore.Get(recoveryCtx, original.ID)
	if err != nil {
		t.Fatalf("Get() through recovered proxy error = %v", err)
	}
	if !reflect.DeepEqual(recovered, got) {
		t.Fatalf("recovered tenant = %#v, want %#v", recovered, got)
	}
}

func requireSQLChaosEnvironment(t *testing.T, name, expected string) string {
	t.Helper()
	value := os.Getenv(name)
	if err := validateSQLChaosValue(name, value, expected); err != nil {
		t.Fatal(err)
	}
	return value
}

func validateSQLChaosValue(name, value, expected string) error {
	if value == "" {
		return fmt.Errorf("%s must be set when SAAS_CHAOS=1", name)
	}
	if value != expected {
		return fmt.Errorf("%s must target the local disposable Compose address", name)
	}
	return nil
}

func openChaosDB(t *testing.T, driver, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open(driver, dsn)
	if err != nil {
		t.Fatalf("sql.Open(%s) error = %v", driver, err)
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(0)
	return db
}

func runSameSnapshotCAS(t *testing.T, sqlStore *store.SQLStore, expected types.Tenant, candidates ...types.Tenant) {
	t.Helper()
	if len(candidates) != 2 {
		t.Fatalf("CAS candidates = %d, want 2", len(candidates))
	}

	start := make(chan struct{})
	results := make(chan error, len(candidates))
	operationsCtx, operationsCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer operationsCancel()
	for _, candidate := range candidates {
		candidate := candidate
		go func() {
			<-start
			results <- sqlStore.CompareAndSwap(operationsCtx, expected, candidate)
		}()
	}
	close(start)

	successes := 0
	conflicts := 0
	for range candidates {
		select {
		case err := <-results:
			switch {
			case err == nil:
				successes++
			case errors.Is(err, store.ErrTenantConflict):
				conflicts++
			default:
				t.Fatalf("CompareAndSwap() error = %v, want nil or ErrTenantConflict", err)
			}
		case <-operationsCtx.Done():
			t.Fatalf("CompareAndSwap() operations did not finish before deadline: %v", operationsCtx.Err())
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("CAS results = %d successes, %d conflicts; want 1 each", successes, conflicts)
	}
}

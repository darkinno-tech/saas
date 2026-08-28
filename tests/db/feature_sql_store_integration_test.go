package db_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/im10furry/saas/feature"
)

func TestFeatureSQLStoreMySQLIntegration(t *testing.T) {
	runFeatureSQLStoreIntegration(t, "mysql", os.Getenv("SAAS_MYSQL_DSN"), resetMySQLFeatureFlagsTable, func(db *sql.DB) (*feature.SQLStore, error) {
		return feature.NewSQLStore(db)
	})
}

func TestFeatureSQLStorePostgresIntegration(t *testing.T) {
	runFeatureSQLStoreIntegration(t, "postgres", os.Getenv("SAAS_POSTGRES_DSN"), resetPostgresFeatureFlagsTable, func(db *sql.DB) (*feature.SQLStore, error) {
		return feature.NewSQLStore(db, feature.WithSQLDialect(feature.SQLDialectPostgres))
	})
}

func runFeatureSQLStoreIntegration(t *testing.T, driver, dsn string, reset func(*testing.T, context.Context, *sql.DB), newStore func(*sql.DB) (*feature.SQLStore, error)) {
	t.Helper()
	if dsn == "" {
		t.Skipf("set the %s DSN to run feature SQL integration tests", driver)
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
	runFeatureSQLStoreContract(t, ctx, store)
}

func runFeatureSQLStoreContract(t *testing.T, ctx context.Context, store *feature.SQLStore) {
	t.Helper()
	starter := []feature.Flag{
		{Key: "exports", Enabled: false, Config: map[string]string{"format": "csv", "limit": "10"}},
		{Key: "members", Enabled: true, Config: map[string]string{"limit": "5"}},
	}
	if err := store.SetPlanDefaults(ctx, "starter", starter); err != nil {
		t.Fatalf("SetPlanDefaults(starter) error = %v", err)
	}
	if err := store.SetPlanDefaults(ctx, "enterprise", []feature.Flag{{Key: "analytics", Enabled: true, Config: map[string]string{"retention": "365"}}}); err != nil {
		t.Fatalf("SetPlanDefaults(enterprise) error = %v", err)
	}
	if err := store.SetTenantOverrides(ctx, "tenant-a", []feature.Flag{{Key: "exports", Enabled: true, Config: map[string]string{"format": "json", "limit": "20"}}}); err != nil {
		t.Fatalf("SetTenantOverrides(tenant A) error = %v", err)
	}
	if err := store.SetTenantOverrides(ctx, "tenant-b", []feature.Flag{{Key: "members", Enabled: false, Config: map[string]string{"limit": "1"}}}); err != nil {
		t.Fatalf("SetTenantOverrides(tenant B) error = %v", err)
	}

	flag, err := store.Resolve(ctx, "tenant-a", "starter", "exports")
	if err != nil {
		t.Fatalf("Resolve(tenant A exports) error = %v", err)
	}
	if !flag.Enabled || !reflect.DeepEqual(flag.Config, map[string]string{"format": "json", "limit": "20"}) {
		t.Fatalf("Resolve(tenant A exports) = %+v, want tenant override", flag)
	}
	flag, err = store.Resolve(ctx, "tenant-b", "starter", "exports")
	if err != nil {
		t.Fatalf("Resolve(tenant B exports) error = %v", err)
	}
	if flag.Enabled || !reflect.DeepEqual(flag.Config, starter[0].Config) {
		t.Fatalf("Resolve(tenant B exports) = %+v, want starter default", flag)
	}
	flag, err = store.Resolve(ctx, "tenant-a", "enterprise", "analytics")
	if err != nil {
		t.Fatalf("Resolve(tenant A enterprise analytics) error = %v", err)
	}
	if !flag.Enabled || !reflect.DeepEqual(flag.Config, map[string]string{"retention": "365"}) {
		t.Fatalf("Resolve(tenant A enterprise analytics) = %+v, want enterprise default", flag)
	}

	flags, err := store.List(ctx, "tenant-a", "starter")
	if err != nil {
		t.Fatalf("List(tenant A starter) error = %v", err)
	}
	if len(flags) != 2 || flags[0].Key != "exports" || flags[1].Key != "members" || !flags[0].Enabled || !reflect.DeepEqual(flags[0].Config, map[string]string{"format": "json", "limit": "20"}) {
		t.Fatalf("List(tenant A starter) = %+v, want sorted merged flags", flags)
	}
	flags, err = store.List(ctx, "tenant-b", "starter")
	if err != nil {
		t.Fatalf("List(tenant B starter) error = %v", err)
	}
	if len(flags) != 2 || flags[0].Key != "exports" || flags[0].Enabled || flags[1].Key != "members" || flags[1].Enabled || !reflect.DeepEqual(flags[1].Config, map[string]string{"limit": "1"}) {
		t.Fatalf("List(tenant B starter) = %+v, want tenant-B-only override", flags)
	}

	if err := store.SetTenantOverrides(ctx, "tenant-a", []feature.Flag{{Key: "analytics", Enabled: true, Config: map[string]string{"beta": "yes"}}}); err != nil {
		t.Fatalf("SetTenantOverrides(replace tenant A) error = %v", err)
	}
	flag, err = store.Resolve(ctx, "tenant-a", "starter", "exports")
	if err != nil {
		t.Fatalf("Resolve(tenant A exports after replace) error = %v", err)
	}
	if flag.Enabled || !reflect.DeepEqual(flag.Config, starter[0].Config) {
		t.Fatalf("Resolve(tenant A exports after replace) = %+v, want starter fallback", flag)
	}
	flag, err = store.Resolve(ctx, "tenant-a", "starter", "analytics")
	if err != nil {
		t.Fatalf("Resolve(tenant A analytics override) error = %v", err)
	}
	if !flag.Enabled || !reflect.DeepEqual(flag.Config, map[string]string{"beta": "yes"}) {
		t.Fatalf("Resolve(tenant A analytics override) = %+v, want replacement override", flag)
	}

	if err := store.SetPlanDefaults(ctx, "starter", nil); err != nil {
		t.Fatalf("SetPlanDefaults(clear starter) error = %v", err)
	}
	if _, err := store.Resolve(ctx, "tenant-a", "starter", "exports"); !errors.Is(err, feature.ErrFeatureNotFound) {
		t.Fatalf("Resolve(cleared starter exports) error = %v, want ErrFeatureNotFound", err)
	}
	flag, err = store.Resolve(ctx, "tenant-b", "starter", "members")
	if err != nil {
		t.Fatalf("Resolve(tenant B members after plan clear) error = %v", err)
	}
	if flag.Enabled || !reflect.DeepEqual(flag.Config, map[string]string{"limit": "1"}) {
		t.Fatalf("Resolve(tenant B members after plan clear) = %+v, want tenant-B override", flag)
	}
	if err := store.SetTenantOverrides(ctx, "tenant-a", nil); err != nil {
		t.Fatalf("SetTenantOverrides(clear tenant A) error = %v", err)
	}
	if _, err := store.Resolve(ctx, "tenant-a", "starter", "analytics"); !errors.Is(err, feature.ErrFeatureNotFound) {
		t.Fatalf("Resolve(cleared tenant A override) error = %v, want ErrFeatureNotFound", err)
	}
	flag, err = store.Resolve(ctx, "tenant-b", "starter", "members")
	if err != nil {
		t.Fatalf("Resolve(tenant B after tenant-A clear) error = %v", err)
	}
	if flag.Enabled {
		t.Fatalf("Resolve(tenant B after tenant-A clear) = %+v, want unaffected tenant-B override", flag)
	}
}

func resetMySQLFeatureFlagsTable(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	resetFeatureFlagsTable(t, ctx, db, []string{
		"DROP TABLE IF EXISTS saas_feature_flags",
		`CREATE TABLE saas_feature_flags (
			scope VARCHAR(32) NOT NULL,
			owner_id VARCHAR(191) NOT NULL,
			` + "`key`" + ` VARCHAR(191) NOT NULL,
			enabled BOOLEAN NOT NULL,
			config JSON NOT NULL,
			PRIMARY KEY (scope, owner_id, ` + "`key`" + `)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	})
}

func resetPostgresFeatureFlagsTable(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	resetFeatureFlagsTable(t, ctx, db, []string{
		"DROP TABLE IF EXISTS saas_feature_flags",
		`CREATE TABLE saas_feature_flags (
			scope VARCHAR(32) NOT NULL,
			owner_id VARCHAR(191) NOT NULL,
			key VARCHAR(191) NOT NULL,
			enabled BOOLEAN NOT NULL,
			config JSONB NOT NULL,
			PRIMARY KEY (scope, owner_id, key)
		)`,
	})
}

func resetFeatureFlagsTable(t *testing.T, ctx context.Context, db *sql.DB, statements []string) {
	t.Helper()
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("exec %q error = %v", statement, err)
		}
	}
}

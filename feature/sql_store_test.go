package feature

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/darkinno-tech/saas/internal/sqlutil"
)

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

	if _, err := NewSQLStore(db, WithTableName("flags;drop")); !errors.Is(err, ErrInvalidTableName) {
		t.Fatalf("NewSQLStore(unsafe table) error = %v, want ErrInvalidTableName", err)
	}
	if _, err := NewSQLStore(db, WithTableName("")); !errors.Is(err, ErrInvalidTableName) {
		t.Fatalf("NewSQLStore(empty table) error = %v, want ErrInvalidTableName", err)
	}
	if _, err := NewSQLStore(db, WithSQLDialect("oracle")); !errors.Is(err, ErrUnsupportedSQLDialect) {
		t.Fatalf("NewSQLStore(unsupported dialect) error = %v, want ErrUnsupportedSQLDialect", err)
	}
}

func TestSQLStoreSetPlanDefaultsRoundTrip(t *testing.T) {
	ctx := context.Background()
	store, mock := newMockFeatureStore(t, SQLDialectMySQL)

	mock.ExpectBegin()
	del := mock.ExpectExec(regexp.QuoteMeta("DELETE FROM saas_feature_flags WHERE scope = ? AND owner_id = ?"))
	del.WithArgs("plan", "plan-one").WillReturnResult(sqlmock.NewResult(0, 2))

	insertQuery := "INSERT INTO saas_feature_flags (scope, owner_id, `key`, enabled, config) VALUES (?, ?, ?, ?, ?)"
	insA := mock.ExpectExec(regexp.QuoteMeta(insertQuery))
	insA.WithArgs("plan", "plan-one", "a", true, `{"region":"eu"}`).WillReturnResult(sqlmock.NewResult(1, 1))
	insB := mock.ExpectExec(regexp.QuoteMeta(insertQuery))
	insB.WithArgs("plan", "plan-one", "b", false, `{}`).WillReturnResult(sqlmock.NewResult(2, 1))
	mock.ExpectCommit()

	if err := store.SetPlanDefaults(ctx, "plan-one", newFlags(t)); err != nil {
		t.Fatalf("SetPlanDefaults() error = %v", err)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreSetTenantOverridesRoundTripPostgres(t *testing.T) {
	ctx := context.Background()
	store, mock := newMockFeatureStore(t, SQLDialectPostgres)

	mock.ExpectBegin()
	del := mock.ExpectExec(regexp.QuoteMeta("DELETE FROM saas_feature_flags WHERE scope = $1 AND owner_id = $2"))
	del.WithArgs("tenant", "tenant-a").WillReturnResult(sqlmock.NewResult(0, 1))
	ins := mock.ExpectExec(regexp.QuoteMeta("INSERT INTO saas_feature_flags (scope, owner_id, key, enabled, config) VALUES ($1, $2, $3, $4, $5)"))
	ins.WithArgs("tenant", "tenant-a", "k", true, `{"zone":"a"}`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	if err := store.SetTenantOverrides(ctx, "tenant-a", []Flag{{Key: "k", Enabled: true, Config: map[string]string{"zone": "a"}}}); err != nil {
		t.Fatalf("SetTenantOverrides() error = %v", err)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreSetValidationAndErrors(t *testing.T) {
	t.Run("cancelled ctx", func(t *testing.T) {
		store, _ := newMockFeatureStore(t, SQLDialectMySQL)
		cancelled, cancel := context.WithCancel(context.Background())
		cancel()
		if err := store.SetPlanDefaults(cancelled, "plan-one", nil); err == nil {
			t.Fatal("SetPlanDefaults() cancelled ctx error = nil, want error")
		}
		cancelled2, cancel2 := context.WithCancel(context.Background())
		cancel2()
		if err := store.SetTenantOverrides(cancelled2, "tenant-a", nil); err == nil {
			t.Fatal("SetTenantOverrides() cancelled ctx error = nil, want error")
		}
	})

	t.Run("empty owner", func(t *testing.T) {
		store, _ := newMockFeatureStore(t, SQLDialectMySQL)
		if err := store.SetPlanDefaults(context.Background(), "", nil); !errors.Is(err, ErrInvalidFeature) {
			t.Fatalf("SetPlanDefaults(empty) error = %v, want ErrInvalidFeature", err)
		}
		if err := store.SetTenantOverrides(context.Background(), "", nil); !errors.Is(err, ErrInvalidFeature) {
			t.Fatalf("SetTenantOverrides(empty) error = %v, want ErrInvalidFeature", err)
		}
	})

	t.Run("invalid flag keys", func(t *testing.T) {
		store, _ := newMockFeatureStore(t, SQLDialectMySQL)
		if err := store.SetPlanDefaults(context.Background(), "plan-one", []Flag{{Key: ""}}); !errors.Is(err, ErrInvalidFeature) {
			t.Fatalf("SetPlanDefaults(empty key) error = %v, want ErrInvalidFeature", err)
		}
		if err := store.SetTenantOverrides(context.Background(), "tenant-a", []Flag{{Key: ""}}); !errors.Is(err, ErrInvalidFeature) {
			t.Fatalf("SetTenantOverrides(empty key) error = %v, want ErrInvalidFeature", err)
		}
	})
}

func TestSQLStoreSetPlanDefaultsTransactionErrors(t *testing.T) {
	t.Run("begin error", func(t *testing.T) {
		ctx := context.Background()
		store, mock := newMockFeatureStore(t, SQLDialectMySQL)
		mock.ExpectBegin().WillReturnError(errors.New("begin boom"))
		if err := store.SetPlanDefaults(ctx, "plan-one", nil); err == nil || err.Error() != "begin boom" {
			t.Fatalf("SetPlanDefaults(begin error) error = %v, want begin boom", err)
		}
		assertSQLMockExpectations(t, mock)
	})

	t.Run("delete error rollback", func(t *testing.T) {
		ctx := context.Background()
		store, mock := newMockFeatureStore(t, SQLDialectMySQL)
		mock.ExpectBegin()
		del := mock.ExpectExec(regexp.QuoteMeta("DELETE FROM saas_feature_flags WHERE scope = ? AND owner_id = ?"))
		del.WithArgs("plan", "plan-one").WillReturnError(errors.New("delete boom"))
		mock.ExpectRollback()
		if err := store.SetPlanDefaults(ctx, "plan-one", newFlags(t)); err == nil || err.Error() != "delete boom" {
			t.Fatalf("SetPlanDefaults(delete error) error = %v, want delete boom", err)
		}
		assertSQLMockExpectations(t, mock)
	})

	t.Run("insert error rollback", func(t *testing.T) {
		ctx := context.Background()
		store, mock := newMockFeatureStore(t, SQLDialectMySQL)
		mock.ExpectBegin()
		del := mock.ExpectExec(regexp.QuoteMeta("DELETE FROM saas_feature_flags WHERE scope = ? AND owner_id = ?"))
		del.WithArgs("plan", "plan-one").WillReturnResult(sqlmock.NewResult(0, 2))
		ins := mock.ExpectExec(regexp.QuoteMeta("INSERT INTO saas_feature_flags (scope, owner_id, `key`, enabled, config) VALUES (?, ?, ?, ?, ?)"))
		ins.WithArgs("plan", "plan-one", "a", true, `{"region":"eu"}`).WillReturnError(errors.New("insert boom"))
		mock.ExpectRollback()
		if err := store.SetPlanDefaults(ctx, "plan-one", newFlags(t)); err == nil || err.Error() != "insert boom" {
			t.Fatalf("SetPlanDefaults(insert error) error = %v, want insert boom", err)
		}
		assertSQLMockExpectations(t, mock)
	})

	t.Run("commit error rollback", func(t *testing.T) {
		ctx := context.Background()
		store, mock := newMockFeatureStore(t, SQLDialectMySQL)
		mock.ExpectBegin()
		del := mock.ExpectExec(regexp.QuoteMeta("DELETE FROM saas_feature_flags WHERE scope = ? AND owner_id = ?"))
		del.WithArgs("plan", "plan-one").WillReturnResult(sqlmock.NewResult(0, 1))
		ins := mock.ExpectExec(regexp.QuoteMeta("INSERT INTO saas_feature_flags (scope, owner_id, `key`, enabled, config) VALUES (?, ?, ?, ?, ?)"))
		ins.WithArgs("plan", "plan-one", "a", true, `{"region":"eu"}`).WillReturnResult(sqlmock.NewResult(1, 1))
		commitErr := errors.New("commit boom")
		mock.ExpectCommit().WillReturnError(commitErr)
		if err := store.SetPlanDefaults(ctx, "plan-one", []Flag{{Key: "a", Enabled: true, Config: map[string]string{"region": "eu"}}}); !errors.Is(err, commitErr) {
			t.Fatalf("SetPlanDefaults(commit error) error = %v, want an error wrapping commit boom", err)
		}
		assertSQLMockExpectations(t, mock)
	})
}

func TestSQLStoreResolveTenantOverride(t *testing.T) {
	ctx := context.Background()
	store, mock := newMockFeatureStore(t, SQLDialectMySQL)
	query := "SELECT `key`, enabled, config FROM saas_feature_flags WHERE scope = ? AND owner_id = ? AND `key` = ?"

	tenant := mock.ExpectQuery(regexp.QuoteMeta(query))
	tenant.WithArgs("tenant", "tenant-a", "k").WillReturnRows(sqlmock.NewRows([]string{"key", "enabled", "config"}).AddRow("k", true, `{"zone":"eu"}`))

	got, err := store.Resolve(ctx, "tenant-a", "plan-one", "k")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if got.Key != "k" || !got.Enabled || got.Config["zone"] != "eu" {
		t.Fatalf("Resolve() = %#v, want tenant override", got)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreResolveFallsBackToPlanDefault(t *testing.T) {
	ctx := context.Background()
	store, mock := newMockFeatureStore(t, SQLDialectMySQL)
	query := "SELECT `key`, enabled, config FROM saas_feature_flags WHERE scope = ? AND owner_id = ? AND `key` = ?"

	tenant := mock.ExpectQuery(regexp.QuoteMeta(query))
	tenant.WithArgs("tenant", "tenant-a", "k").WillReturnRows(sqlmock.NewRows([]string{"key", "enabled", "config"})) // no override

	plan := mock.ExpectQuery(regexp.QuoteMeta(query))
	plan.WithArgs("plan", "plan-one", "k").WillReturnRows(sqlmock.NewRows([]string{"key", "enabled", "config"}).AddRow("k", false, `{}`))

	got, err := store.Resolve(ctx, "tenant-a", "plan-one", "k")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if got.Key != "k" || got.Enabled {
		t.Fatalf("Resolve() = %#v, want plan default", got)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreResolveNotFound(t *testing.T) {
	ctx := context.Background()
	store, mock := newMockFeatureStore(t, SQLDialectMySQL)
	query := "SELECT `key`, enabled, config FROM saas_feature_flags WHERE scope = ? AND owner_id = ? AND `key` = ?"

	tenant := mock.ExpectQuery(regexp.QuoteMeta(query))
	tenant.WithArgs("tenant", "tenant-a", "k").WillReturnRows(sqlmock.NewRows([]string{"key", "enabled", "config"}))
	plan := mock.ExpectQuery(regexp.QuoteMeta(query))
	plan.WithArgs("plan", "plan-one", "k").WillReturnRows(sqlmock.NewRows([]string{"key", "enabled", "config"}))

	if _, err := store.Resolve(ctx, "tenant-a", "plan-one", "k"); !errors.Is(err, ErrFeatureNotFound) {
		t.Fatalf("Resolve(not found) error = %v, want ErrFeatureNotFound", err)
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreResolveErrors(t *testing.T) {
	ctx := context.Background()
	query := "SELECT `key`, enabled, config FROM saas_feature_flags WHERE scope = ? AND owner_id = ? AND `key` = ?"

	t.Run("cancelled ctx", func(t *testing.T) {
		store, _ := newMockFeatureStore(t, SQLDialectMySQL)
		cancelled, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := store.Resolve(cancelled, "tenant-a", "plan-one", "k"); err == nil {
			t.Fatal("Resolve() cancelled ctx error = nil, want error")
		}
	})

	t.Run("invalid input", func(t *testing.T) {
		store, _ := newMockFeatureStore(t, SQLDialectMySQL)
		if _, err := store.Resolve(context.Background(), "", "plan-one", "k"); !errors.Is(err, ErrInvalidFeature) {
			t.Fatalf("Resolve(empty tenant) error = %v, want ErrInvalidFeature", err)
		}
		if _, err := store.Resolve(context.Background(), "t", "", "k"); !errors.Is(err, ErrInvalidFeature) {
			t.Fatalf("Resolve(empty plan) error = %v, want ErrInvalidFeature", err)
		}
		if _, err := store.Resolve(context.Background(), "t", "p", ""); !errors.Is(err, ErrInvalidFeature) {
			t.Fatalf("Resolve(empty key) error = %v, want ErrInvalidFeature", err)
		}
	})

	t.Run("tenant query failure", func(t *testing.T) {
		store, mock := newMockFeatureStore(t, SQLDialectMySQL)
		q := mock.ExpectQuery(regexp.QuoteMeta(query))
		q.WithArgs("tenant", "tenant-a", "k").WillReturnError(errors.New("boom"))
		if _, err := store.Resolve(ctx, "tenant-a", "plan-one", "k"); err == nil || err.Error() != "boom" {
			t.Fatalf("Resolve(tenant query error) error = %v, want boom", err)
		}
		assertSQLMockExpectations(t, mock)
	})

	t.Run("tenant scan error", func(t *testing.T) {
		store, mock := newMockFeatureStore(t, SQLDialectMySQL)
		q := mock.ExpectQuery(regexp.QuoteMeta(query))
		q.WithArgs("tenant", "tenant-a", "k").WillReturnRows(sqlmock.NewRows([]string{"key", "enabled", "config"}).AddRow("k", false, "not-json{"))
		if _, err := store.Resolve(ctx, "tenant-a", "plan-one", "k"); err == nil {
			t.Fatal("Resolve(tenant scan error) error = nil, want error")
		}
		assertSQLMockExpectations(t, mock)
	})

	t.Run("empty config key", func(t *testing.T) {
		store, mock := newMockFeatureStore(t, SQLDialectMySQL)
		q := mock.ExpectQuery(regexp.QuoteMeta(query))
		q.WithArgs("tenant", "tenant-a", "k").WillReturnRows(sqlmock.NewRows([]string{"key", "enabled", "config"}).AddRow("", false, `{}`))
		if _, err := store.Resolve(ctx, "tenant-a", "plan-one", "k"); !errors.Is(err, ErrInvalidFeature) {
			t.Fatalf("Resolve(empty config key) error = %v, want ErrInvalidFeature", err)
		}
		assertSQLMockExpectations(t, mock)
	})
}

func TestSQLStoreListRoundTrip(t *testing.T) {
	ctx := context.Background()
	store, mock := newMockFeatureStore(t, SQLDialectMySQL)

	planQuery := "SELECT `key`, enabled, config FROM saas_feature_flags WHERE scope = ? AND owner_id = ? ORDER BY `key`"
	plan := mock.ExpectQuery(regexp.QuoteMeta(planQuery))
	plan.WithArgs("plan", "plan-one").WillReturnRows(sqlmock.NewRows([]string{"key", "enabled", "config"}).
		AddRow("a", true, `{"region":"eu"}`).
		AddRow("c", false, `{}`))

	tenantQuery := "SELECT `key`, enabled, config FROM saas_feature_flags WHERE scope = ? AND owner_id = ? ORDER BY `key`"
	tenant := mock.ExpectQuery(regexp.QuoteMeta(tenantQuery))
	// tenant override for "a" wins; a brand new "b" is introduced.
	tenant.WithArgs("tenant", "tenant-a").WillReturnRows(sqlmock.NewRows([]string{"key", "enabled", "config"}).
		AddRow("a", false, `{"region":"na"}`).
		AddRow("b", true, `{}`))

	got, err := store.List(ctx, "tenant-a", "plan-one")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(got) != 3 || got[0].Key != "a" || got[1].Key != "b" || got[2].Key != "c" {
		t.Fatalf("List() = %#v, want merged sorted [a b c]", got)
	}
	if got[0].Enabled || got[0].Config["region"] != "na" {
		t.Fatalf("List() override not applied: %#v", got[0])
	}
	assertSQLMockExpectations(t, mock)
}

func TestSQLStoreListErrors(t *testing.T) {
	tenantQuery := "SELECT `key`, enabled, config FROM saas_feature_flags WHERE scope = ? AND owner_id = ? ORDER BY `key`"
	planQuery := "SELECT `key`, enabled, config FROM saas_feature_flags WHERE scope = ? AND owner_id = ? ORDER BY `key`"

	t.Run("cancelled ctx", func(t *testing.T) {
		store, _ := newMockFeatureStore(t, SQLDialectMySQL)
		cancelled, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := store.List(cancelled, "tenant-a", "plan-one"); err == nil {
			t.Fatal("List() cancelled ctx error = nil, want error")
		}
	})

	t.Run("invalid input", func(t *testing.T) {
		store, _ := newMockFeatureStore(t, SQLDialectMySQL)
		if _, err := store.List(context.Background(), "", "plan-one"); !errors.Is(err, ErrInvalidFeature) {
			t.Fatalf("List(empty tenant) error = %v, want ErrInvalidFeature", err)
		}
		if _, err := store.List(context.Background(), "tenant-a", ""); !errors.Is(err, ErrInvalidFeature) {
			t.Fatalf("List(empty plan) error = %v, want ErrInvalidFeature", err)
		}
	})

	t.Run("plan defaults query error", func(t *testing.T) {
		store, mock := newMockFeatureStore(t, SQLDialectMySQL)
		p := mock.ExpectQuery(regexp.QuoteMeta(planQuery))
		p.WithArgs("plan", "plan-one").WillReturnError(errors.New("boom"))
		if _, err := store.List(context.Background(), "tenant-a", "plan-one"); err == nil || err.Error() != "boom" {
			t.Fatalf("List(plan query error) error = %v, want boom", err)
		}
		assertSQLMockExpectations(t, mock)
	})

	t.Run("overrides query error", func(t *testing.T) {
		store, mock := newMockFeatureStore(t, SQLDialectMySQL)
		p := mock.ExpectQuery(regexp.QuoteMeta(planQuery))
		p.WithArgs("plan", "plan-one").WillReturnRows(sqlmock.NewRows([]string{"key", "enabled", "config"}).AddRow("a", true, `{}`))
		tq := mock.ExpectQuery(regexp.QuoteMeta(tenantQuery))
		tq.WithArgs("tenant", "tenant-a").WillReturnError(errors.New("boom"))
		if _, err := store.List(context.Background(), "tenant-a", "plan-one"); err == nil || err.Error() != "boom" {
			t.Fatalf("List(overrides query error) error = %v, want boom", err)
		}
		assertSQLMockExpectations(t, mock)
	})

	t.Run("scan error", func(t *testing.T) {
		store, mock := newMockFeatureStore(t, SQLDialectMySQL)
		p := mock.ExpectQuery(regexp.QuoteMeta(planQuery))
		p.WithArgs("plan", "plan-one").WillReturnRows(sqlmock.NewRows([]string{"key", "enabled", "config"}).AddRow("a", true, "not-json{"))
		if _, err := store.List(context.Background(), "tenant-a", "plan-one"); err == nil {
			t.Fatal("List(scan error) error = nil, want error")
		}
		assertSQLMockExpectations(t, mock)
	})

	t.Run("rows err", func(t *testing.T) {
		store, mock := newMockFeatureStore(t, SQLDialectMySQL)
		rows := sqlmock.NewRows([]string{"key", "enabled", "config"}).AddRow("a", true, `{}`)
		rows.RowError(0, errors.New("row boom"))
		p := mock.ExpectQuery(regexp.QuoteMeta(planQuery))
		p.WithArgs("plan", "plan-one").WillReturnRows(rows)
		if _, err := store.List(context.Background(), "tenant-a", "plan-one"); err == nil || err.Error() != "row boom" {
			t.Fatalf("List(rows err) error = %v, want row boom", err)
		}
		assertSQLMockExpectations(t, mock)
	})
}

func newFlags(t *testing.T) []Flag {
	t.Helper()
	return []Flag{
		{Key: "a", Enabled: true, Config: map[string]string{"region": "eu"}},
		{Key: "b", Enabled: false, Config: map[string]string{}},
	}
}

// mustConfigString bridges to sqlutil.MarshalStringMap for expected config strings.
func mustConfigString(t *testing.T, values map[string]string) string {
	t.Helper()
	raw, err := sqlutil.MarshalStringMap(values)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	return raw
}

func newMockFeatureStore(t *testing.T, dialect SQLDialect) (*SQLStore, sqlmock.Sqlmock) {
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

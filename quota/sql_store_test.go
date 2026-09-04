package quota

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

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

func TestQuotaNewSQLStoreValidation(t *testing.T) {
	t.Parallel()
	if _, err := NewSQLStore(nil); !errors.Is(err, ErrNilDB) {
		t.Fatalf("NewSQLStore(nil) error = %v, want ErrNilDB", err)
	}

	db := &sql.DB{}
	store, err := NewSQLStore(db)
	if err != nil {
		t.Fatalf("NewSQLStore() error = %v", err)
	}
	if store.table != DefaultSQLTableName || store.dialect != SQLDialectMySQL {
		t.Fatalf("NewSQLStore() = %#v, want default table/dialect", store)
	}
	if _, err := NewSQLStore(db, WithTableName("usage;drop")); !errors.Is(err, ErrInvalidTableName) {
		t.Fatalf("NewSQLStore(unsafe table) error = %v, want ErrInvalidTableName", err)
	}
	if _, err := NewSQLStore(db, WithSQLDialect("oracle")); !errors.Is(err, ErrUnsupportedSQLDialect) {
		t.Fatalf("NewSQLStore(unsupported dialect) error = %v, want ErrUnsupportedSQLDialect", err)
	}
	store, err = NewSQLStore(db, WithTableName("custom_usage"), WithSQLDialect(SQLDialectPostgres), nil)
	if err != nil {
		t.Fatalf("NewSQLStore(opts) error = %v", err)
	}
	if store.table != "custom_usage" || store.dialect != SQLDialectPostgres {
		t.Fatalf("NewSQLStore() = %#v, want custom table/dialect", store)
	}
}

func TestQuotaSQLStoreAddValidation(t *testing.T) {
	t.Parallel()
	store, _ := newMockSQLStore(t, SQLDialectPostgres)

	if _, err := store.Add(context.Background(), "", "api", PeriodDay, 1); !errors.Is(err, ErrInvalidQuota) {
		t.Fatalf("Add(empty tenant) error = %v, want ErrInvalidQuota", err)
	}
	if _, err := store.Add(context.Background(), "tenant-a", "", PeriodDay, 1); !errors.Is(err, ErrInvalidQuota) {
		t.Fatalf("Add(empty resource) error = %v, want ErrInvalidQuota", err)
	}
	if _, err := store.Add(context.Background(), "tenant-a", "api", "", 1); !errors.Is(err, ErrInvalidQuota) {
		t.Fatalf("Add(empty period) error = %v, want ErrInvalidQuota", err)
	}
	if _, err := store.Add(context.Background(), "tenant-a", "api", PeriodDay, -1); !errors.Is(err, ErrInvalidQuota) {
		t.Fatalf("Add(negative amount) error = %v, want ErrInvalidQuota", err)
	}
	if _, err := store.Consume(context.Background(), Limit{}, 1); !errors.Is(err, ErrInvalidQuota) {
		t.Fatalf("Consume(invalid limit) error = %v, want ErrInvalidQuota", err)
	}
	if _, err := store.Consume(context.Background(), Limit{TenantID: "tenant-a", Resource: "api", Limit: 1, Period: PeriodDay}, -1); !errors.Is(err, ErrInvalidQuota) {
		t.Fatalf("Consume(negative amount) error = %v, want ErrInvalidQuota", err)
	}
	if _, err := store.Get(context.Background(), "", "api", PeriodDay); !errors.Is(err, ErrInvalidQuota) {
		t.Fatalf("Get(empty tenant) error = %v, want ErrInvalidQuota", err)
	}
	if err := store.Reset(context.Background(), "tenant-a", "", PeriodDay); !errors.Is(err, ErrInvalidQuota) {
		t.Fatalf("Reset(empty resource) error = %v, want ErrInvalidQuota", err)
	}
}

func TestQuotaSQLStoreCanceledContext(t *testing.T) {
	t.Parallel()
	store, _ := newMockSQLStore(t, SQLDialectPostgres)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := store.Add(ctx, "tenant-a", "api", PeriodDay, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("Add(canceled) error = %v, want context.Canceled", err)
	}
	if _, err := store.Consume(ctx, Limit{TenantID: "tenant-a", Resource: "api", Limit: 1, Period: PeriodDay}, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("Consume(canceled) error = %v, want context.Canceled", err)
	}
	if _, err := store.Get(ctx, "tenant-a", "api", PeriodDay); !errors.Is(err, context.Canceled) {
		t.Fatalf("Get(canceled) error = %v, want context.Canceled", err)
	}
	if err := store.Reset(ctx, "tenant-a", "api", PeriodDay); !errors.Is(err, context.Canceled) {
		t.Fatalf("Reset(canceled) error = %v, want context.Canceled", err)
	}
}

// Add inserts a new usage row when no prior usage exists.
func TestQuotaSQLStoreAddInsertsNewUsage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectPostgres)

	mock.ExpectBegin()
	get := mock.ExpectQuery(regexp.QuoteMeta("SELECT used FROM saas_quota_usage WHERE tenant_id = $1 AND resource = $2 AND period = $3 FOR UPDATE"))
	get.WithArgs("tenant-a", "api_calls", "day").WillReturnRows(sqlmock.NewRows([]string{"used"}))
	ins := mock.ExpectExec(regexp.QuoteMeta("INSERT INTO saas_quota_usage (tenant_id, resource, period, used) VALUES ($1, $2, $3, $4)"))
	ins.WithArgs("tenant-a", "api_calls", "day", int64(5)).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	used, err := store.Add(ctx, "tenant-a", "api_calls", PeriodDay, 5)
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if used != 5 {
		t.Fatalf("Add() used = %d, want 5", used)
	}
	assertSQLMockExpectations(t, mock)
}

// Consume updates an existing usage row when usage already exists.
func TestQuotaSQLStoreConsumeUpdatesExistingUsage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectPostgres)
	limit := Limit{TenantID: "tenant-a", Resource: "api_calls", Limit: 10, Period: PeriodDay}

	mock.ExpectBegin()
	get := mock.ExpectQuery(regexp.QuoteMeta("SELECT used FROM saas_quota_usage WHERE tenant_id = $1 AND resource = $2 AND period = $3 FOR UPDATE"))
	get.WithArgs("tenant-a", "api_calls", "day").WillReturnRows(sqlmock.NewRows([]string{"used"}).AddRow(int64(3)))
	upd := mock.ExpectExec(regexp.QuoteMeta("UPDATE saas_quota_usage SET used = $1 WHERE tenant_id = $2 AND resource = $3 AND period = $4"))
	upd.WithArgs(int64(5), "tenant-a", "api_calls", "day").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	used, err := store.Consume(ctx, limit, 2)
	if err != nil {
		t.Fatalf("Consume() error = %v", err)
	}
	if used != 5 {
		t.Fatalf("Consume() used = %d, want 5", used)
	}
	assertSQLMockExpectations(t, mock)
}

// Consume with amount 0 leaves usage unchanged and skips the UPDATE.
func TestQuotaSQLStoreConsumeZeroAmountSkipsUpdate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectPostgres)
	limit := Limit{TenantID: "tenant-a", Resource: "api_calls", Limit: 10, Period: PeriodDay}

	mock.ExpectBegin()
	get := mock.ExpectQuery(regexp.QuoteMeta("SELECT used FROM saas_quota_usage WHERE tenant_id = $1 AND resource = $2 AND period = $3 FOR UPDATE"))
	get.WithArgs("tenant-a", "api_calls", "day").WillReturnRows(sqlmock.NewRows([]string{"used"}).AddRow(int64(5)))
	mock.ExpectCommit()

	used, err := store.Consume(ctx, limit, 0)
	if err != nil {
		t.Fatalf("Consume(0) error = %v", err)
	}
	if used != 5 {
		t.Fatalf("Consume(0) used = %d, want 5", used)
	}
	assertSQLMockExpectations(t, mock)
}

func TestQuotaSQLStoreConsumeExceedsLimit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectPostgres)
	limit := Limit{TenantID: "tenant-a", Resource: "api_calls", Limit: 10, Period: PeriodDay}

	mock.ExpectBegin()
	get := mock.ExpectQuery(regexp.QuoteMeta("SELECT used FROM saas_quota_usage WHERE tenant_id = $1 AND resource = $2 AND period = $3 FOR UPDATE"))
	get.WithArgs("tenant-a", "api_calls", "day").WillReturnRows(sqlmock.NewRows([]string{"used"}).AddRow(int64(8)))
	mock.ExpectRollback()

	used, err := store.Consume(ctx, limit, 3)
	if !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("Consume() error = %v, want ErrQuotaExceeded", err)
	}
	if used != 8 {
		t.Fatalf("Consume() used = %d, want current 8", used)
	}
	assertSQLMockExpectations(t, mock)
}

func TestQuotaSQLStoreAddIntegerOverflow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectPostgres)

	mock.ExpectBegin()
	get := mock.ExpectQuery(regexp.QuoteMeta("SELECT used FROM saas_quota_usage WHERE tenant_id = $1 AND resource = $2 AND period = $3 FOR UPDATE"))
	get.WithArgs("tenant-a", "api_calls", "day").WillReturnRows(sqlmock.NewRows([]string{"used"}).AddRow(int64(9223372036854775807))) // math.MaxInt64
	mock.ExpectRollback()

	if _, err := store.Add(ctx, "tenant-a", "api_calls", PeriodDay, 1); !errors.Is(err, ErrInvalidQuota) {
		t.Fatalf("Add(overflow) error = %v, want ErrInvalidQuota", err)
	}
	assertSQLMockExpectations(t, mock)
}

func TestQuotaSQLStoreAddNegativeExistingUsage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectPostgres)

	mock.ExpectBegin()
	get := mock.ExpectQuery(regexp.QuoteMeta("SELECT used FROM saas_quota_usage WHERE tenant_id = $1 AND resource = $2 AND period = $3 FOR UPDATE"))
	get.WithArgs("tenant-a", "api_calls", "day").WillReturnRows(sqlmock.NewRows([]string{"used"}).AddRow(int64(-1)))
	mock.ExpectRollback()

	if _, err := store.Add(ctx, "tenant-a", "api_calls", PeriodDay, 1); !errors.Is(err, ErrInvalidQuota) {
		t.Fatalf("Add(negative usage) error = %v, want ErrInvalidQuota", err)
	}
	assertSQLMockExpectations(t, mock)
}

func TestQuotaSQLStoreAddBeginError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectPostgres)
	err := errors.New("begin failed")
	mock.ExpectBegin().WillReturnError(err)

	if _, got := store.Add(ctx, "tenant-a", "api_calls", PeriodDay, 1); !errors.Is(got, err) {
		t.Fatalf("Add(begin error) = %v, want begin error", got)
	}
	assertSQLMockExpectations(t, mock)
}

func TestQuotaSQLStoreAddCommitError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectPostgres)

	mock.ExpectBegin()
	get := mock.ExpectQuery(regexp.QuoteMeta("SELECT used FROM saas_quota_usage WHERE tenant_id = $1 AND resource = $2 AND period = $3 FOR UPDATE"))
	get.WithArgs("tenant-a", "api_calls", "day").WillReturnRows(sqlmock.NewRows([]string{"used"}).AddRow(int64(3)))
	upd := mock.ExpectExec(regexp.QuoteMeta("UPDATE saas_quota_usage SET used = $1 WHERE tenant_id = $2 AND resource = $3 AND period = $4"))
	upd.WithArgs(int64(4), "tenant-a", "api_calls", "day").WillReturnResult(sqlmock.NewResult(0, 1))
	err := errors.New("commit failed")
	mock.ExpectCommit().WillReturnError(err)

	if _, got := store.Add(ctx, "tenant-a", "api_calls", PeriodDay, 1); !errors.Is(got, err) {
		t.Fatalf("Add(commit error) = %v, want commit error", got)
	}
	assertSQLMockExpectations(t, mock)
}

func TestQuotaSQLStoreAddUpdateRowsAffectedZero(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectPostgres)

	mock.ExpectBegin()
	get := mock.ExpectQuery(regexp.QuoteMeta("SELECT used FROM saas_quota_usage WHERE tenant_id = $1 AND resource = $2 AND period = $3 FOR UPDATE"))
	get.WithArgs("tenant-a", "api_calls", "day").WillReturnRows(sqlmock.NewRows([]string{"used"}).AddRow(int64(3)))
	upd := mock.ExpectExec(regexp.QuoteMeta("UPDATE saas_quota_usage SET used = $1 WHERE tenant_id = $2 AND resource = $3 AND period = $4"))
	upd.WithArgs(int64(4), "tenant-a", "api_calls", "day").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()

	if _, err := store.Add(ctx, "tenant-a", "api_calls", PeriodDay, 1); !errors.Is(err, ErrInvalidQuota) {
		t.Fatalf("Add(update zero rows) error = %v, want ErrInvalidQuota", err)
	}
	assertSQLMockExpectations(t, mock)
}

func TestQuotaSQLStoreSQLiteDialectOmitsForUpdate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectSQLite)

	mock.ExpectBegin()
	get := mock.ExpectQuery(regexp.QuoteMeta("SELECT used FROM saas_quota_usage WHERE tenant_id = ? AND resource = ? AND period = ?"))
	get.WithArgs("tenant-a", "api_calls", "day").WillReturnRows(sqlmock.NewRows([]string{"used"}))
	ins := mock.ExpectExec(regexp.QuoteMeta("INSERT INTO saas_quota_usage (tenant_id, resource, period, used) VALUES (?, ?, ?, ?)"))
	ins.WithArgs("tenant-a", "api_calls", "day", int64(2)).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	if _, err := store.Add(ctx, "tenant-a", "api_calls", PeriodDay, 2); err != nil {
		t.Fatalf("Add(sqlite) error = %v", err)
	}
	assertSQLMockExpectations(t, mock)
}

func TestQuotaSQLStoreGet(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectPostgres)
	get := mock.ExpectQuery(regexp.QuoteMeta("SELECT used FROM saas_quota_usage WHERE tenant_id = $1 AND resource = $2 AND period = $3"))
	get.WithArgs("tenant-a", "api_calls", "day").WillReturnRows(sqlmock.NewRows([]string{"used"}).AddRow(int64(7)))

	used, err := store.Get(ctx, "tenant-a", "api_calls", PeriodDay)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if used != 7 {
		t.Fatalf("Get() used = %d, want 7", used)
	}
	assertSQLMockExpectations(t, mock)
}

func TestQuotaSQLStoreGetNoRows(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectPostgres)
	get := mock.ExpectQuery(regexp.QuoteMeta("SELECT used FROM saas_quota_usage WHERE tenant_id = $1 AND resource = $2 AND period = $3"))
	get.WithArgs("tenant-a", "api_calls", "day").WillReturnRows(sqlmock.NewRows([]string{"used"}))

	used, err := store.Get(ctx, "tenant-a", "api_calls", PeriodDay)
	if err != nil {
		t.Fatalf("Get(no rows) error = %v, want nil", err)
	}
	if used != 0 {
		t.Fatalf("Get(no rows) used = %d, want 0", used)
	}
	assertSQLMockExpectations(t, mock)
}

func TestQuotaSQLStoreGetNegativeUsage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectPostgres)
	get := mock.ExpectQuery(regexp.QuoteMeta("SELECT used FROM saas_quota_usage WHERE tenant_id = $1 AND resource = $2 AND period = $3"))
	get.WithArgs("tenant-a", "api_calls", "day").WillReturnRows(sqlmock.NewRows([]string{"used"}).AddRow(int64(-1)))

	if _, err := store.Get(ctx, "tenant-a", "api_calls", PeriodDay); !errors.Is(err, ErrInvalidQuota) {
		t.Fatalf("Get(negative) error = %v, want ErrInvalidQuota", err)
	}
	assertSQLMockExpectations(t, mock)
}

func TestQuotaSQLStoreGetQueryError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectPostgres)
	err := errors.New("query failed")
	get := mock.ExpectQuery(regexp.QuoteMeta("SELECT used FROM saas_quota_usage WHERE tenant_id = $1 AND resource = $2 AND period = $3"))
	get.WithArgs("tenant-a", "api_calls", "day").WillReturnError(err)

	if _, got := store.Get(ctx, "tenant-a", "api_calls", PeriodDay); !errors.Is(got, err) {
		t.Fatalf("Get(query error) = %v, want sentinel", got)
	}
	assertSQLMockExpectations(t, mock)
}

func TestQuotaSQLStoreReset(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectPostgres)
	del := mock.ExpectExec(regexp.QuoteMeta("DELETE FROM saas_quota_usage WHERE tenant_id = $1 AND resource = $2 AND period = $3"))
	del.WithArgs("tenant-a", "api_calls", "day").WillReturnResult(sqlmock.NewResult(0, 1))

	if err := store.Reset(ctx, "tenant-a", "api_calls", PeriodDay); err != nil {
		t.Fatalf("Reset() error = %v", err)
	}
	assertSQLMockExpectations(t, mock)
}

func TestQuotaSQLStoreResetExecError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectPostgres)
	err := errors.New("exec failed")
	del := mock.ExpectExec(regexp.QuoteMeta("DELETE FROM saas_quota_usage WHERE tenant_id = $1 AND resource = $2 AND period = $3"))
	del.WithArgs("tenant-a", "api_calls", "day").WillReturnError(err)

	if got := store.Reset(ctx, "tenant-a", "api_calls", PeriodDay); !errors.Is(got, err) {
		t.Fatalf("Reset(exec error) = %v, want sentinel", got)
	}
	assertSQLMockExpectations(t, mock)
}

func TestQuotaSQLStoreRetryOnTransientTransactionError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectPostgres)

	// First mutation attempt fails with a retryable deadlock error after Begin.
	mock.ExpectBegin()
	get1 := mock.ExpectQuery(regexp.QuoteMeta("SELECT used FROM saas_quota_usage WHERE tenant_id = $1 AND resource = $2 AND period = $3 FOR UPDATE"))
	get1.WithArgs("tenant-a", "api_calls", "day").WillReturnRows(sqlmock.NewRows([]string{"used"}))
	err := errors.New("deadlock detected")
	ins1 := mock.ExpectExec(regexp.QuoteMeta("INSERT INTO saas_quota_usage (tenant_id, resource, period, used) VALUES ($1, $2, $3, $4)"))
	ins1.WithArgs("tenant-a", "api_calls", "day", int64(1)).WillReturnError(err)
	mock.ExpectRollback()

	// Second attempt succeeds.
	mock.ExpectBegin()
	get2 := mock.ExpectQuery(regexp.QuoteMeta("SELECT used FROM saas_quota_usage WHERE tenant_id = $1 AND resource = $2 AND period = $3 FOR UPDATE"))
	get2.WithArgs("tenant-a", "api_calls", "day").WillReturnRows(sqlmock.NewRows([]string{"used"}))
	ins2 := mock.ExpectExec(regexp.QuoteMeta("INSERT INTO saas_quota_usage (tenant_id, resource, period, used) VALUES ($1, $2, $3, $4)"))
	ins2.WithArgs("tenant-a", "api_calls", "day", int64(1)).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	used, got := store.Add(ctx, "tenant-a", "api_calls", PeriodDay, 1)
	if got != nil {
		t.Fatalf("Add(transient retry) error = %v, want nil", got)
	}
	if used != 1 {
		t.Fatalf("Add(transient retry) used = %d, want 1", used)
	}
	assertSQLMockExpectations(t, mock)
}

func TestQuotaSQLStoreConsumeMySQLDialect(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mock := newMockSQLStore(t, SQLDialectMySQL)
	limit := Limit{TenantID: "tenant-a", Resource: "api_calls", Limit: 5, Period: PeriodMonth}

	mock.ExpectBegin()
	get := mock.ExpectQuery(regexp.QuoteMeta("SELECT used FROM saas_quota_usage WHERE tenant_id = ? AND resource = ? AND period = ? FOR UPDATE"))
	get.WithArgs("tenant-a", "api_calls", "month").WillReturnRows(sqlmock.NewRows([]string{"used"}).AddRow(int64(1)))
	upd := mock.ExpectExec(regexp.QuoteMeta("UPDATE saas_quota_usage SET used = ? WHERE tenant_id = ? AND resource = ? AND period = ?"))
	upd.WithArgs(int64(3), "tenant-a", "api_calls", "month").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	used, err := store.Consume(ctx, limit, 2)
	if err != nil {
		t.Fatalf("Consume(mysql) error = %v", err)
	}
	if used != 3 {
		t.Fatalf("Consume(mysql) used = %d, want 3", used)
	}
	assertSQLMockExpectations(t, mock)
}
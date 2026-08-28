package store

import (
	"database/sql"
	"errors"
	"reflect"
	"testing"

	"github.com/im10furry/saas/core/types"
)

func TestNewSQLStoreValidation(t *testing.T) {
	if _, err := NewSQLStore(nil); !errors.Is(err, ErrNilDB) {
		t.Fatalf("NewSQLStore(nil) error = %v, want ErrNilDB", err)
	}

	db := &sql.DB{}
	store, err := NewSQLStore(db)
	if err != nil {
		t.Fatalf("NewSQLStore() error = %v", err)
	}
	if store.table != DefaultSQLTableName {
		t.Fatalf("default table = %q, want %q", store.table, DefaultSQLTableName)
	}

	store, err = NewSQLStore(db, WithTableName("public.tenants_2026"))
	if err != nil {
		t.Fatalf("NewSQLStore(custom table) error = %v", err)
	}
	if store.table != "public.tenants_2026" {
		t.Fatalf("custom table = %q, want public.tenants_2026", store.table)
	}

	store, err = NewSQLStore(db, WithSQLDialect(SQLDialectPostgres))
	if err != nil {
		t.Fatalf("NewSQLStore(postgres dialect) error = %v", err)
	}
	if store.dialect != SQLDialectPostgres {
		t.Fatalf("dialect = %q, want %q", store.dialect, SQLDialectPostgres)
	}

	if _, err := NewSQLStore(db, WithTableName("tenants; drop table tenants")); !errors.Is(err, ErrInvalidTableName) {
		t.Fatalf("NewSQLStore(unsafe table) error = %v, want ErrInvalidTableName", err)
	}
	if _, err := NewSQLStore(db, WithSQLDialect("oracle")); !errors.Is(err, ErrUnsupportedSQLDialect) {
		t.Fatalf("NewSQLStore(unsupported dialect) error = %v, want ErrUnsupportedSQLDialect", err)
	}
}

func TestNormalizeCompareAndSwapError(t *testing.T) {
	for _, message := range []string{
		"database is locked",
		"SQLITE_BUSY: database is locked",
		"deadlock found when trying to get lock",
		"Lock wait timeout exceeded; try restarting transaction",
		"could not serialize access due to concurrent update",
		"transaction failed with SQLSTATE 40001",
	} {
		err := normalizeCompareAndSwapError(errors.New(message))
		if !errors.Is(err, ErrTenantConflict) {
			t.Fatalf("normalizeCompareAndSwapError(%q) = %v, want ErrTenantConflict", message, err)
		}
	}
	wantErr := errors.New("connection refused")
	if err := normalizeCompareAndSwapError(wantErr); !errors.Is(err, wantErr) || errors.Is(err, ErrTenantConflict) {
		t.Fatalf("normalizeCompareAndSwapError(non-conflict) = %v, want original error", err)
	}
}

func TestConfirmUpdatedTenantRejectsConcurrentReplacement(t *testing.T) {
	desired := testTenant("tenant-a")
	current := desired
	current.Status = types.TenantStatusSuspended
	if err := confirmUpdatedTenant(current, desired); !errors.Is(err, ErrTenantConflict) {
		t.Fatalf("confirmUpdatedTenant() error = %v, want ErrTenantConflict", err)
	}
}

func TestSQLStoreConfigCodec(t *testing.T) {
	raw, err := marshalConfig(map[string]string{"region": "us", "feature": "on"})
	if err != nil {
		t.Fatalf("marshalConfig() error = %v", err)
	}

	got, err := unmarshalConfig(raw)
	if err != nil {
		t.Fatalf("unmarshalConfig() error = %v", err)
	}
	want := map[string]string{"region": "us", "feature": "on"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unmarshalConfig() = %#v, want %#v", got, want)
	}

	empty, err := marshalConfig(nil)
	if err != nil {
		t.Fatalf("marshalConfig(nil) error = %v", err)
	}
	if empty != "{}" {
		t.Fatalf("marshalConfig(nil) = %q, want {}", empty)
	}

	got, err = unmarshalConfig("")
	if err != nil {
		t.Fatalf("unmarshalConfig(empty) error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("unmarshalConfig(empty) = %#v, want empty map", got)
	}
}

func TestSQLStorePlaceholders(t *testing.T) {
	mysqlStore := &SQLStore{dialect: SQLDialectMySQL}
	postgresStore := &SQLStore{dialect: SQLDialectPostgres}

	tests := []struct {
		name  string
		store *SQLStore
		count int
		start int
		want  string
	}{
		{name: "empty", store: mysqlStore, count: 0, start: 1, want: ""},
		{name: "mysql one", store: mysqlStore, count: 1, start: 1, want: "?"},
		{name: "mysql many", store: mysqlStore, count: 3, start: 1, want: "?, ?, ?"},
		{name: "postgres one", store: postgresStore, count: 1, start: 1, want: "$1"},
		{name: "postgres offset", store: postgresStore, count: 3, start: 2, want: "$2, $3, $4"},
	}

	for _, tt := range tests {
		if got := tt.store.placeholders(tt.count, tt.start); got != tt.want {
			t.Fatalf("%s placeholders(%d, %d) = %q, want %q", tt.name, tt.count, tt.start, got, tt.want)
		}
	}
}

func TestSafeQualifiedIdentifier(t *testing.T) {
	tests := []struct {
		value string
		want  bool
	}{
		{value: "tenants", want: true},
		{value: "public.tenants_2026", want: true},
		{value: "_tenants", want: true},
		{value: "2026_tenants", want: false},
		{value: "tenants-name", want: false},
		{value: "public.", want: false},
		{value: "tenants;drop", want: false},
	}

	for _, tt := range tests {
		if got := isSafeQualifiedIdentifier(tt.value); got != tt.want {
			t.Fatalf("isSafeQualifiedIdentifier(%q) = %v, want %v", tt.value, got, tt.want)
		}
	}
}

func TestSQLStoreDuplicateKeyErrorDetection(t *testing.T) {
	tests := []string{
		"Error 1062 (23000): Duplicate entry 'tenant-a' for key 'PRIMARY'",
		"pq: duplicate key value violates unique constraint \"tenants_pkey\"",
		"UNIQUE constraint failed: tenants.id",
		"Violation of PRIMARY KEY constraint 'PK_tenants'",
	}

	for _, message := range tests {
		if !errors.Is(normalizeCreateError(errors.New(message)), ErrTenantAlreadyExists) {
			t.Fatalf("normalizeCreateError(%q) did not return ErrTenantAlreadyExists", message)
		}
	}
}

package gormtenant

import (
	"context"
	"errors"
	"reflect"
	"testing"

	tenantctx "github.com/darkinno-tech/saas/core/context"
	"github.com/darkinno-tech/saas/core/types"
	"github.com/darkinno-tech/saas/data"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/schema"
)

type tenantlessOrder struct {
	ID   uint
	Name string
}

type numericTenantOrder struct {
	ID       uint
	TenantID int `gorm:"column:tenant_id"`
}

func TestFillTenantOnCreateFailsClosedForInvalidPayloads(t *testing.T) {
	db := newDryRunDB(t)
	plugin := New(Config{})
	tenantContext := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})

	t.Run("missing tenant context", func(t *testing.T) {
		order := &tenantOrder{}
		tx := newCallbackStatement(t, db, context.Background(), order, order)

		plugin.fillTenantOnCreate(tx)
		if !errors.Is(tx.Error, data.ErrNoTenant) {
			t.Fatalf("fillTenantOnCreate() error = %v, want ErrNoTenant", tx.Error)
		}
	})

	t.Run("host context leaves partition key untouched", func(t *testing.T) {
		order := &tenantOrder{TenantID: "host-supplied"}
		tx := newCallbackStatement(t, db, tenantctx.WithHost(context.Background()), order, order)

		plugin.fillTenantOnCreate(tx)
		if tx.Error != nil {
			t.Fatalf("fillTenantOnCreate(host) error = %v", tx.Error)
		}
		if order.TenantID != "host-supplied" {
			t.Fatalf("host TenantID = %q, want host-supplied", order.TenantID)
		}
	})

	t.Run("model without tenant field is rejected", func(t *testing.T) {
		order := &tenantlessOrder{Name: "unpartitioned"}
		tx := newCallbackStatement(t, db, tenantContext, order, order)

		plugin.fillTenantOnCreate(tx)
		if !errors.Is(tx.Error, ErrTenantFieldNotFound) {
			t.Fatalf("fillTenantOnCreate(tenantless) error = %v, want ErrTenantFieldNotFound", tx.Error)
		}
	})

	t.Run("nil pointer is rejected without panic", func(t *testing.T) {
		var order *tenantOrder
		tx := newCallbackStatement(t, db, tenantContext, tenantOrder{}, order)

		plugin.fillTenantOnCreate(tx)
		if !errors.Is(tx.Error, ErrTenantFieldNotFound) {
			t.Fatalf("fillTenantOnCreate(nil) error = %v, want ErrTenantFieldNotFound", tx.Error)
		}
	})

	t.Run("invalid tenant storage is surfaced through the callback", func(t *testing.T) {
		order := &numericTenantOrder{}
		tx := newCallbackStatement(t, db, tenantContext, order, order)

		plugin.fillTenantOnCreate(tx)
		if tx.Error == nil {
			t.Fatal("fillTenantOnCreate(numeric tenant field) error = nil, want conversion error")
		}
	})
}

func TestSetTenantFieldSupportsPointerAndCollectionPayloads(t *testing.T) {
	db := newDryRunDB(t)
	plugin := New(Config{})
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})

	t.Run("pointer chain", func(t *testing.T) {
		order := &tenantOrder{}
		payload := &order
		tx := newCallbackStatement(t, db, ctx, tenantOrder{}, payload)

		if err := plugin.setTenantField(tx, "tenant-a"); err != nil {
			t.Fatalf("setTenantField(pointer chain) error = %v", err)
		}
		if order.TenantID != "tenant-a" {
			t.Fatalf("pointer chain TenantID = %q, want tenant-a", order.TenantID)
		}
	})

	t.Run("slice of pointers", func(t *testing.T) {
		orders := []*tenantOrder{{Name: "first"}, {Name: "second"}}
		tx := newCallbackStatement(t, db, ctx, tenantOrder{}, orders)

		if err := plugin.setTenantField(tx, "tenant-a"); err != nil {
			t.Fatalf("setTenantField(slice) error = %v", err)
		}
		for index, order := range orders {
			if order.TenantID != "tenant-a" {
				t.Fatalf("orders[%d].TenantID = %q, want tenant-a", index, order.TenantID)
			}
		}
	})

	t.Run("array", func(t *testing.T) {
		orders := [2]tenantOrder{{Name: "first"}, {Name: "second"}}
		tx := newCallbackStatement(t, db, ctx, tenantOrder{}, &orders)

		if err := plugin.setTenantField(tx, "tenant-a"); err != nil {
			t.Fatalf("setTenantField(array) error = %v", err)
		}
		for index, order := range orders {
			if order.TenantID != "tenant-a" {
				t.Fatalf("orders[%d].TenantID = %q, want tenant-a", index, order.TenantID)
			}
		}
	})

	t.Run("array value fails closed instead of panicking", func(t *testing.T) {
		orders := [1]tenantOrder{{Name: "unaddressable"}}
		tx := newCallbackStatement(t, db, ctx, tenantOrder{}, orders)

		if err := plugin.setTenantField(tx, "tenant-a"); !errors.Is(err, ErrTenantFieldNotFound) {
			t.Fatalf("setTenantField(array value) error = %v, want ErrTenantFieldNotFound", err)
		}
	})

	t.Run("nil element fails closed", func(t *testing.T) {
		orders := []*tenantOrder{nil}
		tx := newCallbackStatement(t, db, ctx, tenantOrder{}, orders)

		if err := plugin.setTenantField(tx, "tenant-a"); !errors.Is(err, ErrTenantFieldNotFound) {
			t.Fatalf("setTenantField(nil element) error = %v, want ErrTenantFieldNotFound", err)
		}
	})

	t.Run("unsupported slice element fails closed", func(t *testing.T) {
		tx := newCallbackStatement(t, db, ctx, tenantOrder{}, []string{"untrusted"})

		if err := plugin.setTenantField(tx, "tenant-a"); !errors.Is(err, ErrTenantFieldNotFound) {
			t.Fatalf("setTenantField(non-model slice) error = %v, want ErrTenantFieldNotFound", err)
		}
	})

	t.Run("missing schema fails closed", func(t *testing.T) {
		tx := db.Session(&gorm.Session{}).WithContext(ctx)
		tx.Statement = &gorm.Statement{DB: tx, Context: ctx, ReflectValue: reflect.ValueOf(&tenantOrder{})}

		if err := plugin.setTenantField(tx, "tenant-a"); !errors.Is(err, ErrTenantFieldNotFound) {
			t.Fatalf("setTenantField(no schema) error = %v, want ErrTenantFieldNotFound", err)
		}
	})
}

func TestSetTenantFieldValuePreservesExistingPartitionAndRejectsInvalidStorage(t *testing.T) {
	db := newDryRunDB(t)
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})

	t.Run("same tenant is accepted", func(t *testing.T) {
		order := &tenantOrder{TenantID: "tenant-a"}
		tx := newCallbackStatement(t, db, ctx, order, order)
		field := lookupField(tx.Statement.Schema, "tenant_id")

		if err := setTenantFieldValue(tx, field, dereference(tx.Statement.ReflectValue), "tenant-a"); err != nil {
			t.Fatalf("setTenantFieldValue(same tenant) error = %v", err)
		}
	})

	t.Run("different tenant is rejected", func(t *testing.T) {
		order := &tenantOrder{TenantID: "tenant-b"}
		tx := newCallbackStatement(t, db, ctx, order, order)
		field := lookupField(tx.Statement.Schema, "tenant_id")

		if err := setTenantFieldValue(tx, field, dereference(tx.Statement.ReflectValue), "tenant-a"); !errors.Is(err, ErrTenantMismatch) {
			t.Fatalf("setTenantFieldValue(mismatch) error = %v, want ErrTenantMismatch", err)
		}
	})

	t.Run("non-text tenant storage is rejected", func(t *testing.T) {
		order := &numericTenantOrder{}
		tx := newCallbackStatement(t, db, ctx, order, order)
		field := lookupField(tx.Statement.Schema, "tenant_id")

		if err := setTenantFieldValue(tx, field, dereference(tx.Statement.ReflectValue), "tenant-a"); err == nil {
			t.Fatal("setTenantFieldValue(numeric tenant field) error = nil, want conversion error")
		}
	})
}

func TestCreateCallbackReflectionHelpersHandleNilAndFallbackSchemas(t *testing.T) {
	order := tenantOrder{}
	pointer := &order
	pointerChain := &pointer
	if got := dereference(reflect.ValueOf(pointerChain)); got.Kind() != reflect.Struct {
		t.Fatalf("dereference(pointer chain) kind = %s, want struct", got.Kind())
	}

	var nilOrder *tenantOrder
	if got := dereference(reflect.ValueOf(nilOrder)); got.Kind() != reflect.Pointer || !got.IsNil() {
		t.Fatalf("dereference(nil pointer) = %#v, want nil pointer", got)
	}
	if got := dereference(reflect.Value{}); got.IsValid() {
		t.Fatalf("dereference(invalid) = %#v, want invalid", got)
	}

	fallback := &schema.Field{Name: "TenantID", DBName: "legacy_tenant_id"}
	fallbackSchema := &schema.Schema{Fields: []*schema.Field{fallback}}
	if got := lookupField(fallbackSchema, "legacy_tenant_id"); got != fallback {
		t.Fatalf("lookupField(fallback schema) = %#v, want fallback field", got)
	}
	if got := lookupField(fallbackSchema, "missing"); got != nil {
		t.Fatalf("lookupField(missing) = %#v, want nil", got)
	}
}

func TestUpdateGuardHelpersFailClosedForAmbiguousTenantChanges(t *testing.T) {
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})
	plugin := New(Config{})
	db := newDryRunDB(t)

	for _, test := range []struct {
		name  string
		value any
		want  bool
	}{
		{name: "string", value: "tenant-a", want: true},
		{name: "tenant identifier", value: types.TenantID("tenant-a"), want: true},
		{name: "pointer", value: stringPointer("tenant-a"), want: true},
		{name: "nil pointer", value: (*string)(nil), want: false},
		{name: "wrong tenant", value: "tenant-b", want: false},
		{name: "non-string", value: 1, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := tenantUpdateValueMatches(test.value, "tenant-a"); got != test.want {
				t.Fatalf("tenantUpdateValueMatches(%#v) = %v, want %v", test.value, got, test.want)
			}
		})
	}

	if plugin.invalidTenantFieldUpdate(nil, "tenant-a") {
		t.Fatal("invalidTenantFieldUpdate(nil) = true, want false")
	}

	t.Run("opaque SET clause", func(t *testing.T) {
		tx := newCallbackStatement(t, db, ctx, tenantOrder{}, map[string]any{})
		tx.Statement.Clauses["SET"] = clause.Clause{Expression: clause.Expr{SQL: "tenant_id = ?", Vars: []any{"tenant-a"}}}
		if !plugin.invalidTenantFieldUpdate(tx.Statement, "tenant-a") {
			t.Fatal("opaque SET clause was accepted")
		}
	})

	t.Run("explicit SET assignment", func(t *testing.T) {
		mismatch := newCallbackStatement(t, db, ctx, tenantOrder{}, map[string]any{})
		mismatch.Statement.Clauses["SET"] = clause.Clause{Expression: clause.Set{{Column: clause.Column{Name: "tenant_id"}, Value: "tenant-b"}}}
		if !plugin.invalidTenantFieldUpdate(mismatch.Statement, "tenant-a") {
			t.Fatal("cross-tenant SET assignment was accepted")
		}

		matching := newCallbackStatement(t, db, ctx, tenantOrder{}, map[string]any{})
		matching.Statement.Clauses["SET"] = clause.Clause{Expression: clause.Set{{Column: clause.Column{Name: "tenant_id"}, Value: "tenant-a"}}}
		if plugin.invalidTenantFieldUpdate(matching.Statement, "tenant-a") {
			t.Fatal("same-tenant SET assignment was rejected")
		}
	})

	t.Run("map update", func(t *testing.T) {
		mismatch := newCallbackStatement(t, db, ctx, tenantOrder{}, map[string]any{"tenant_id": "tenant-b"})
		if !plugin.invalidTenantFieldUpdate(mismatch.Statement, "tenant-a") {
			t.Fatal("cross-tenant map update was accepted")
		}

		matching := newCallbackStatement(t, db, ctx, tenantOrder{}, map[string]any{"tenant_id": "tenant-a"})
		if plugin.invalidTenantFieldUpdate(matching.Statement, "tenant-a") {
			t.Fatal("same-tenant map update was rejected")
		}
	})

	t.Run("struct update", func(t *testing.T) {
		mismatch := newCallbackStatement(t, db, ctx, tenantOrder{}, &tenantOrder{TenantID: "tenant-b"})
		if !plugin.invalidTenantFieldUpdate(mismatch.Statement, "tenant-a") {
			t.Fatal("cross-tenant struct update was accepted")
		}

		zero := newCallbackStatement(t, db, ctx, tenantOrder{}, &tenantOrder{})
		if plugin.invalidTenantFieldUpdate(zero.Statement, "tenant-a") {
			t.Fatal("zero-value tenant field should not be considered an update")
		}
	})

	t.Run("nil and restricted payloads are not mistaken for tenant changes", func(t *testing.T) {
		var nilOrder *tenantOrder
		nilPayload := newCallbackStatement(t, db, ctx, tenantOrder{}, nilOrder)
		if plugin.invalidTenantFieldUpdate(nilPayload.Statement, "tenant-a") {
			t.Fatal("nil payload was treated as a tenant update")
		}

		emptyPayload := newCallbackStatement(t, db, ctx, tenantOrder{}, nil)
		if plugin.invalidTenantFieldUpdate(emptyPayload.Statement, "tenant-a") {
			t.Fatal("empty payload was treated as a tenant update")
		}

		restricted := newCallbackStatement(t, db, ctx, tenantOrder{}, map[string]any{"tenant_id": "tenant-b"})
		restricted.Statement.Selects = []string{"name"}
		if plugin.invalidTenantFieldUpdate(restricted.Statement, "tenant-a") {
			t.Fatal("unselected tenant key was treated as an update")
		}
	})

	if field := plugin.tenantSchemaField(nil); field != nil {
		t.Fatalf("tenantSchemaField(nil) = %#v, want nil", field)
	}
	if updateColumnIncluded(&gorm.Statement{}, "tenant_id", &schema.Field{DBName: "tenant_id", Updatable: false}) {
		t.Fatal("non-updatable tenant field was included in update")
	}
}

func TestUpdateAndQueryCallbacksRejectMissingOrUnsafeContext(t *testing.T) {
	db := newDryRunDB(t)
	plugin := New(Config{})
	tenantContext := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})

	missing := newCallbackStatement(t, db, context.Background(), tenantOrder{}, map[string]any{"name": "changed"})
	plugin.guardUpdate(missing)
	if !errors.Is(missing.Error, data.ErrNoTenant) {
		t.Fatalf("guardUpdate(missing context) error = %v, want ErrNoTenant", missing.Error)
	}

	host := newCallbackStatement(t, db, tenantctx.WithHost(context.Background()), tenantOrder{}, map[string]any{"name": "changed"})
	plugin.guardUpdate(host)
	if host.Error != nil {
		t.Fatalf("guardUpdate(host) error = %v", host.Error)
	}

	mutation := newCallbackStatement(t, db, tenantContext, tenantOrder{}, map[string]any{"tenant_id": "tenant-b"})
	plugin.guardUpdate(mutation)
	if !errors.Is(mutation.Error, ErrTenantFieldUpdate) {
		t.Fatalf("guardUpdate(cross-tenant mutation) error = %v, want ErrTenantFieldUpdate", mutation.Error)
	}

	raw := newCallbackStatement(t, db, tenantContext, tenantOrder{}, &tenantOrder{})
	raw.Statement.SQL.WriteString("UPDATE tenant_orders SET name = ?")
	plugin.guardUpdate(raw)
	if !errors.Is(raw.Error, ErrRawRequiresHost) {
		t.Fatalf("guardUpdate(raw tenant SQL) error = %v, want ErrRawRequiresHost", raw.Error)
	}

	missingRaw := newCallbackStatement(t, db, context.Background(), tenantOrder{}, &tenantOrder{})
	plugin.requireHostForRaw(missingRaw)
	if !errors.Is(missingRaw.Error, data.ErrNoTenant) {
		t.Fatalf("requireHostForRaw(missing context) error = %v, want ErrNoTenant", missingRaw.Error)
	}

	filter, err := New(Config{SoftDeleteField: "deleted_at", IncludeSoftDeleted: true}).newFilter(tenantContext)
	if err != nil {
		t.Fatalf("newFilter(include deleted) error = %v", err)
	}
	if condition := filter.Condition(); condition.Expression != "tenant_id = ?" {
		t.Fatalf("include-deleted condition = %q, want only tenant filter", condition.Expression)
	}
}

func TestScopesAndPreloadGuardsFailClosed(t *testing.T) {
	db := newDryRunDB(t)
	hostContext := tenantctx.WithHost(context.Background())

	missingScope := NewScopes(Config{}).TenantScope(context.Background())
	missing := db.WithContext(hostContext).Scopes(missingScope).Find(&[]tenantOrder{})
	if !errors.Is(missing.Error, data.ErrNoTenant) {
		t.Fatalf("TenantScope(missing context) error = %v, want ErrNoTenant", missing.Error)
	}

	hostScope := NewScopes(Config{}).TenantScope(hostContext)
	host := db.WithContext(hostContext).Scopes(hostScope).Find(&[]tenantOrder{})
	if host.Error != nil {
		t.Fatalf("TenantScope(host) error = %v", host.Error)
	}

	plugin := New(Config{})
	preload := db.Session(&gorm.Session{}).WithContext(context.Background())
	preload.Statement = &gorm.Statement{
		DB:       preload,
		Context:  context.Background(),
		Preloads: map[string][]interface{}{"Items": nil},
	}
	plugin.guardPreloads(preload)
	if !errors.Is(preload.Error, data.ErrNoTenant) {
		t.Fatalf("guardPreloads(missing context) error = %v, want ErrNoTenant", preload.Error)
	}

	marker := callbackTestTenantScopeMarker{}
	if !hasTenantScope([]interface{}{marker}) {
		t.Fatal("hasTenantScope(marker) = false, want true")
	}
	if hasTenantScope([]interface{}{"not a scope"}) {
		t.Fatal("hasTenantScope(non-marker) = true, want false")
	}

	addDBError(host, nil)
	if host.Error != nil {
		t.Fatalf("addDBError(nil) changed DB error to %v", host.Error)
	}
}

func TestMySQLSoftDeleteIndexRejectsUnsafeFields(t *testing.T) {
	for _, test := range []struct {
		name         string
		tenantField  string
		business     []string
		markerField  string
		wantSafeName bool
	}{
		{name: "unsafe marker", tenantField: "tenant_id", business: []string{"email"}, markerField: "deleted-flag"},
		{name: "unsafe business field", tenantField: "tenant_id", business: []string{"email;drop"}, markerField: "deleted_flag"},
		{name: "empty name", wantSafeName: false},
		{name: "leading digit", wantSafeName: false, markerField: "1deleted"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.name == "empty name" || test.name == "leading digit" {
				if isSafeFieldName(test.markerField) != test.wantSafeName {
					t.Fatalf("isSafeFieldName(%q) = %v, want %v", test.markerField, isSafeFieldName(test.markerField), test.wantSafeName)
				}
				return
			}

			if _, err := NewMySQLSoftDeleteUniqueIndex(test.tenantField, test.business, test.markerField); !errors.Is(err, data.ErrInvalidFieldName) {
				t.Fatalf("NewMySQLSoftDeleteUniqueIndex() error = %v, want ErrInvalidFieldName", err)
			}
		})
	}
}

type callbackTestTenantScopeMarker struct{}

func (callbackTestTenantScopeMarker) tenantScopeMarker() {}

func stringPointer(value string) *string {
	return &value
}

func newCallbackStatement(t *testing.T, db *gorm.DB, ctx context.Context, schemaValue any, destination any) *gorm.DB {
	t.Helper()

	tx := db.Session(&gorm.Session{}).WithContext(ctx)
	statement := &gorm.Statement{
		DB:           tx,
		Clauses:      map[string]clause.Clause{},
		Context:      ctx,
		Dest:         destination,
		ReflectValue: reflect.ValueOf(destination),
	}
	if err := statement.Parse(schemaValue); err != nil {
		t.Fatalf("Statement.Parse(%T) error = %v", schemaValue, err)
	}
	tx.Statement = statement
	return tx
}

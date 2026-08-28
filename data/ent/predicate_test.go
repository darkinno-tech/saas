package enttenant

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	entgo "entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/sql"

	"github.com/im10furry/saas"
	tenantctx "github.com/im10furry/saas/core/context"
	"github.com/im10furry/saas/core/types"
	"github.com/im10furry/saas/data"
)

func TestPredicateAddsTenantFilter(t *testing.T) {
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})
	selector := testSelector()

	predicate, err := Predicate(ctx, Config{})
	if err != nil {
		t.Fatalf("Predicate() error = %v", err)
	}
	predicate(selector)

	query, args := selector.Query()
	if !strings.Contains(query, "`orders`.`tenant_id` = ?") {
		t.Fatalf("query missing tenant predicate: %s", query)
	}
	if !reflect.DeepEqual(args, []any{"tenant-a"}) {
		t.Fatalf("args = %#v", args)
	}
}

func TestPredicateAddsSoftDeleteFilter(t *testing.T) {
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})
	selector := testSelector()

	err := Apply(ctx, selector, Config{SoftDeleteField: "deleted_at"})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}

	query, args := selector.Query()
	if !strings.Contains(query, "`orders`.`tenant_id` = ?") {
		t.Fatalf("query missing tenant predicate: %s", query)
	}
	if !strings.Contains(query, "`orders`.`deleted_at` IS NULL") {
		t.Fatalf("query missing soft-delete predicate: %s", query)
	}
	if !reflect.DeepEqual(args, []any{"tenant-a"}) {
		t.Fatalf("args = %#v", args)
	}
}

func TestPredicateCanIncludeSoftDeleted(t *testing.T) {
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})
	selector := testSelector()

	err := Apply(ctx, selector, Config{SoftDeleteField: "deleted_at", IncludeSoftDeleted: true})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}

	query, args := selector.Query()
	if strings.Contains(query, "deleted_at") {
		t.Fatalf("query should not include soft-delete predicate: %s", query)
	}
	if !reflect.DeepEqual(args, []any{"tenant-a"}) {
		t.Fatalf("args = %#v", args)
	}
}

func TestPredicateHostContextIsNoop(t *testing.T) {
	ctx := tenantctx.WithHost(context.Background())
	selector := testSelector()

	err := Apply(ctx, selector, Config{})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}

	query, args := selector.Query()
	if strings.Contains(query, "WHERE") {
		t.Fatalf("host query should not be filtered: %s", query)
	}
	if len(args) != 0 {
		t.Fatalf("args = %#v", args)
	}
}

func TestPredicateRequiresTenantContext(t *testing.T) {
	_, err := Predicate(context.Background(), Config{})
	if !errors.Is(err, data.ErrNoTenant) {
		t.Fatalf("Predicate() error = %v, want %v", err, data.ErrNoTenant)
	}
}

func TestPredicateValidatesFieldNames(t *testing.T) {
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})

	_, err := Predicate(ctx, Config{TenantField: "tenant_id;drop"})
	if !errors.Is(err, data.ErrInvalidFieldName) {
		t.Fatalf("Predicate() error = %v, want %v", err, data.ErrInvalidFieldName)
	}
}

func TestTenantPredicateAndMutationHelpersCoverTypedValues(t *testing.T) {
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})
	predicate, err := TenantPredicate(ctx)
	if err != nil {
		t.Fatalf("TenantPredicate() error = %v", err)
	}
	selector := testSelector()
	predicate(selector)
	query, args := selector.Query()
	if !strings.Contains(query, "`orders`.`tenant_id` = ?") || !reflect.DeepEqual(args, []any{"tenant-a"}) {
		t.Fatalf("TenantPredicate() query = %q args = %#v", query, args)
	}

	value := "tenant-a"
	var nilValue *string
	for _, test := range []struct {
		name  string
		value entgo.Value
		want  bool
	}{
		{name: "string", value: "tenant-a", want: true},
		{name: "tenant id", value: types.TenantID("tenant-a"), want: true},
		{name: "pointer", value: &value, want: true},
		{name: "nil pointer", value: nilValue, want: false},
		{name: "wrong type", value: 42, want: false},
		{name: "wrong tenant", value: "tenant-b", want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := tenantMutationValueMatches(test.value, "tenant-a"); got != test.want {
				t.Fatalf("tenantMutationValueMatches(%#v) = %v, want %v", test.value, got, test.want)
			}
		})
	}
	if !tenantMutationFieldMatches("TENANT_ID", []string{"tenant_id"}) {
		t.Fatal("tenantMutationFieldMatches() did not ignore case")
	}
	if tenantMutationFieldMatches("owner_id", []string{"tenant_id"}) {
		t.Fatal("tenantMutationFieldMatches() matched an unrelated field")
	}
}

func TestFiltersRejectNilAndUnsupportedValues(t *testing.T) {
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})
	if err := NewQueryFilter(Config{}).Apply(ctx, nil); !errors.Is(err, ErrNilQuery) {
		t.Fatalf("QueryFilter.Apply(nil) error = %v, want ErrNilQuery", err)
	}
	if err := NewMutationFilter(Config{}).Apply(ctx, nil); !errors.Is(err, ErrNilMutation) {
		t.Fatalf("MutationFilter.Apply(nil) error = %v, want ErrNilMutation", err)
	}
	if err := NewMutationFilter(Config{}).Apply(ctx, newFakeMutation(entgo.Op(0))); !errors.Is(err, ErrUnsupportedMutation) {
		t.Fatalf("MutationFilter.Apply(unsupported) error = %v, want ErrUnsupportedMutation", err)
	}

	unsupported := Hook(Config{})(entgo.MutateFunc(func(context.Context, entgo.Mutation) (entgo.Value, error) {
		t.Fatal("next mutator should not run for unsupported mutation")
		return nil, nil
	}))
	if _, err := unsupported.Mutate(ctx, &unsupportedEntMutation{}); !errors.Is(err, ErrUnsupportedMutation) {
		t.Fatalf("Hook(unsupported mutation) error = %v, want ErrUnsupportedMutation", err)
	}
}

func TestFilterQueryAddsTenantPredicate(t *testing.T) {
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})
	query := &fakeQuery{}

	err := FilterQuery(ctx, query, Config{})
	if err != nil {
		t.Fatalf("FilterQuery() error = %v", err)
	}

	sqlText, args := query.sql()
	if !strings.Contains(sqlText, "`orders`.`tenant_id` = ?") {
		t.Fatalf("query missing tenant predicate: %s", sqlText)
	}
	if !reflect.DeepEqual(args, []any{"tenant-a"}) {
		t.Fatalf("args = %#v", args)
	}
}

func TestFilterQueryHostContextIsNoop(t *testing.T) {
	query := &fakeQuery{}

	err := FilterQuery(tenantctx.WithHost(context.Background()), query, Config{})
	if err != nil {
		t.Fatalf("FilterQuery(host) error = %v", err)
	}

	sqlText, args := query.sql()
	if strings.Contains(sqlText, "WHERE") {
		t.Fatalf("host query should not be filtered: %s", sqlText)
	}
	if len(args) != 0 {
		t.Fatalf("args = %#v", args)
	}
}

func TestFilterMutationCreateSetsTenantField(t *testing.T) {
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})
	mutation := newFakeMutation(entgo.OpCreate)

	err := FilterMutation(ctx, mutation, Config{})
	if err != nil {
		t.Fatalf("FilterMutation(create) error = %v", err)
	}

	value, ok := mutation.Field("tenant_id")
	if !ok || value != "tenant-a" {
		t.Fatalf("tenant_id = %#v, %v", value, ok)
	}
	if len(mutation.predicates) != 0 {
		t.Fatalf("create mutation should not add predicates")
	}
}

func TestFilterMutationCreateRejectsTenantMismatch(t *testing.T) {
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})
	mutation := newFakeMutation(entgo.OpCreate)
	mutation.fields["tenant_id"] = "tenant-b"

	err := FilterMutation(ctx, mutation, Config{})
	if !errors.Is(err, saas.ErrTenantMismatch) {
		t.Fatalf("FilterMutation(create mismatch) error = %v, want %v", err, saas.ErrTenantMismatch)
	}
}

func TestFilterMutationCreateReportsMissingTenantField(t *testing.T) {
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})
	mutation := newFakeMutation(entgo.OpCreate)
	mutation.setErr = errors.New("unknown field")

	err := FilterMutation(ctx, mutation, Config{})
	if !errors.Is(err, ErrTenantFieldNotFound) {
		t.Fatalf("FilterMutation(missing field) error = %v, want %v", err, ErrTenantFieldNotFound)
	}
}

func TestFilterMutationUpdateAddsTenantPredicate(t *testing.T) {
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})
	mutation := newFakeMutation(entgo.OpUpdate)

	err := FilterMutation(ctx, mutation, Config{})
	if err != nil {
		t.Fatalf("FilterMutation(update) error = %v", err)
	}

	sqlText, args := mutation.sql()
	if !strings.Contains(sqlText, "`orders`.`tenant_id` = ?") {
		t.Fatalf("mutation missing tenant predicate: %s", sqlText)
	}
	if !reflect.DeepEqual(args, []any{"tenant-a"}) {
		t.Fatalf("args = %#v", args)
	}
}

func TestFilterMutationUpdateRejectsTenantFieldChange(t *testing.T) {
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})

	for _, test := range []struct {
		name   string
		config Config
		field  string
	}{
		{name: "default field", field: "tenant_id"},
		{name: "custom field", config: Config{TenantField: "account_id"}, field: "account_id"},
	} {
		t.Run(test.name, func(t *testing.T) {
			mutation := newFakeMutation(entgo.OpUpdate)
			mutation.fields[test.field] = "tenant-b"

			err := FilterMutation(ctx, mutation, test.config)
			if !errors.Is(err, ErrTenantFieldUpdate) {
				t.Fatalf("FilterMutation(update tenant field) error = %v, want ErrTenantFieldUpdate", err)
			}
			if len(mutation.predicates) != 0 {
				t.Fatalf("rejected mutation predicates = %d, want 0", len(mutation.predicates))
			}
		})
	}

	sameTenantMutation := newFakeMutation(entgo.OpUpdate)
	sameTenantMutation.fields["tenant_id"] = "tenant-a"
	if err := FilterMutation(ctx, sameTenantMutation, Config{}); err != nil {
		t.Fatalf("FilterMutation(same tenant field update) error = %v", err)
	}
	if len(sameTenantMutation.predicates) != 1 {
		t.Fatalf("same-tenant mutation predicates = %d, want 1", len(sameTenantMutation.predicates))
	}

	hostMutation := newFakeMutation(entgo.OpUpdate)
	hostMutation.fields["tenant_id"] = "tenant-b"
	if err := FilterMutation(tenantctx.WithHost(context.Background()), hostMutation, Config{}); err != nil {
		t.Fatalf("FilterMutation(host tenant field update) error = %v", err)
	}
}

func TestFilterMutationUpdateRejectsUnprovableTenantFieldChanges(t *testing.T) {
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})
	tests := []struct {
		name    string
		fields  []string
		added   []string
		cleared []string
	}{
		{name: "set without value", fields: []string{"tenant_id"}},
		{name: "add", added: []string{"tenant_id"}},
		{name: "clear", cleared: []string{"tenant_id"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mutation := &fakeTrackedMutation{
				fakeMutation: newFakeMutation(entgo.OpUpdate),
				fields:       test.fields,
				added:        test.added,
				cleared:      test.cleared,
			}
			if err := FilterMutation(ctx, mutation, Config{}); !errors.Is(err, ErrTenantFieldUpdate) {
				t.Fatalf("FilterMutation(%s tenant field) error = %v, want ErrTenantFieldUpdate", test.name, err)
			}
		})
	}
}

func TestFilterMutationDeleteAddsSoftDeletePredicate(t *testing.T) {
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})
	mutation := newFakeMutation(entgo.OpDelete)

	err := FilterMutation(ctx, mutation, Config{SoftDeleteField: "deleted_at"})
	if err != nil {
		t.Fatalf("FilterMutation(delete) error = %v", err)
	}

	sqlText, args := mutation.sql()
	if !strings.Contains(sqlText, "`orders`.`tenant_id` = ?") {
		t.Fatalf("mutation missing tenant predicate: %s", sqlText)
	}
	if !strings.Contains(sqlText, "`orders`.`deleted_at` IS NULL") {
		t.Fatalf("mutation missing soft-delete predicate: %s", sqlText)
	}
	if !reflect.DeepEqual(args, []any{"tenant-a"}) {
		t.Fatalf("args = %#v", args)
	}
}

func TestFilterMutationRequiresTenantContext(t *testing.T) {
	err := FilterMutation(context.Background(), newFakeMutation(entgo.OpUpdate), Config{})
	if !errors.Is(err, data.ErrNoTenant) {
		t.Fatalf("FilterMutation(no tenant) error = %v, want %v", err, data.ErrNoTenant)
	}
}

func TestFilterMutationHostContextIsNoop(t *testing.T) {
	mutation := newFakeMutation(entgo.OpUpdate)

	err := FilterMutation(tenantctx.WithHost(context.Background()), mutation, Config{})
	if err != nil {
		t.Fatalf("FilterMutation(host) error = %v", err)
	}
	if len(mutation.predicates) != 0 {
		t.Fatalf("host mutation should not be filtered")
	}
}

func TestHookAppliesMutationFilter(t *testing.T) {
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})
	mutation := &fakeEntMutation{fakeMutation: newFakeMutation(entgo.OpUpdate)}
	called := false

	mutator := Hook(Config{})(entgo.MutateFunc(func(context.Context, entgo.Mutation) (entgo.Value, error) {
		called = true
		return "ok", nil
	}))

	value, err := mutator.Mutate(ctx, mutation)
	if err != nil {
		t.Fatalf("Mutate() error = %v", err)
	}
	if value != "ok" {
		t.Fatalf("value = %#v", value)
	}
	if !called {
		t.Fatalf("next mutator was not called")
	}
	if len(mutation.predicates) != 1 {
		t.Fatalf("predicates = %d, want 1", len(mutation.predicates))
	}
}

func testSelector() *sql.Selector {
	return sql.Dialect(dialect.MySQL).
		Select("*").
		From(sql.Table("orders"))
}

type fakeQuery struct {
	predicates []SelectorPredicate
}

func (query *fakeQuery) WhereP(predicates ...SelectorPredicate) {
	query.predicates = append(query.predicates, predicates...)
}

func (query *fakeQuery) sql() (string, []any) {
	selector := testSelector()
	for _, predicate := range query.predicates {
		predicate(selector)
	}
	return selector.Query()
}

type fakeMutation struct {
	op         entgo.Op
	fields     map[string]entgo.Value
	setErr     error
	predicates []SelectorPredicate
}

func newFakeMutation(op entgo.Op) *fakeMutation {
	return &fakeMutation{
		op:     op,
		fields: make(map[string]entgo.Value),
	}
}

func (mutation *fakeMutation) Op() entgo.Op {
	return mutation.op
}

func (mutation *fakeMutation) WhereP(predicates ...SelectorPredicate) {
	mutation.predicates = append(mutation.predicates, predicates...)
}

func (mutation *fakeMutation) Field(name string) (entgo.Value, bool) {
	value, ok := mutation.fields[name]
	return value, ok
}

func (mutation *fakeMutation) SetField(name string, value entgo.Value) error {
	if mutation.setErr != nil {
		return mutation.setErr
	}
	mutation.fields[name] = value
	return nil
}

func (mutation *fakeMutation) sql() (string, []any) {
	selector := testSelector()
	for _, predicate := range mutation.predicates {
		predicate(selector)
	}
	return selector.Query()
}

type fakeEntMutation struct {
	*fakeMutation
}

type unsupportedEntMutation struct{}

func (unsupportedEntMutation) Op() entgo.Op                                          { return entgo.OpCreate }
func (unsupportedEntMutation) Type() string                                          { return "Order" }
func (unsupportedEntMutation) Fields() []string                                      { return nil }
func (unsupportedEntMutation) Field(string) (entgo.Value, bool)                      { return nil, false }
func (unsupportedEntMutation) SetField(string, entgo.Value) error                    { return nil }
func (unsupportedEntMutation) AddedFields() []string                                 { return nil }
func (unsupportedEntMutation) AddedField(string) (entgo.Value, bool)                 { return nil, false }
func (unsupportedEntMutation) AddField(string, entgo.Value) error                    { return nil }
func (unsupportedEntMutation) ClearedFields() []string                               { return nil }
func (unsupportedEntMutation) FieldCleared(string) bool                              { return false }
func (unsupportedEntMutation) ClearField(string) error                               { return nil }
func (unsupportedEntMutation) ResetField(string) error                               { return nil }
func (unsupportedEntMutation) AddedEdges() []string                                  { return nil }
func (unsupportedEntMutation) AddedIDs(string) []entgo.Value                         { return nil }
func (unsupportedEntMutation) RemovedEdges() []string                                { return nil }
func (unsupportedEntMutation) RemovedIDs(string) []entgo.Value                       { return nil }
func (unsupportedEntMutation) ClearedEdges() []string                                { return nil }
func (unsupportedEntMutation) EdgeCleared(string) bool                               { return false }
func (unsupportedEntMutation) ClearEdge(string) error                                { return nil }
func (unsupportedEntMutation) ResetEdge(string) error                                { return nil }
func (unsupportedEntMutation) OldField(context.Context, string) (entgo.Value, error) { return nil, nil }

type fakeTrackedMutation struct {
	*fakeMutation
	fields  []string
	added   []string
	cleared []string
}

func (mutation *fakeTrackedMutation) Fields() []string {
	return mutation.fields
}

func (mutation *fakeTrackedMutation) AddedFields() []string {
	return mutation.added
}

func (mutation *fakeTrackedMutation) ClearedFields() []string {
	return mutation.cleared
}

func (mutation *fakeEntMutation) Type() string {
	return "Order"
}

func (mutation *fakeEntMutation) Fields() []string {
	return nil
}

func (mutation *fakeEntMutation) AddedFields() []string {
	return nil
}

func (mutation *fakeEntMutation) AddedField(string) (entgo.Value, bool) {
	return nil, false
}

func (mutation *fakeEntMutation) AddField(string, entgo.Value) error {
	return nil
}

func (mutation *fakeEntMutation) ClearedFields() []string {
	return nil
}

func (mutation *fakeEntMutation) FieldCleared(string) bool {
	return false
}

func (mutation *fakeEntMutation) ClearField(string) error {
	return nil
}

func (mutation *fakeEntMutation) ResetField(string) error {
	return nil
}

func (mutation *fakeEntMutation) AddedEdges() []string {
	return nil
}

func (mutation *fakeEntMutation) AddedIDs(string) []entgo.Value {
	return nil
}

func (mutation *fakeEntMutation) RemovedEdges() []string {
	return nil
}

func (mutation *fakeEntMutation) RemovedIDs(string) []entgo.Value {
	return nil
}

func (mutation *fakeEntMutation) ClearedEdges() []string {
	return nil
}

func (mutation *fakeEntMutation) EdgeCleared(string) bool {
	return false
}

func (mutation *fakeEntMutation) ClearEdge(string) error {
	return nil
}

func (mutation *fakeEntMutation) ResetEdge(string) error {
	return nil
}

func (mutation *fakeEntMutation) OldField(context.Context, string) (entgo.Value, error) {
	return nil, errors.New("old field is not implemented")
}

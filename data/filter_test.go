package data

import (
	"context"
	"errors"
	"testing"

	tenantctx "github.com/darkinno-tech/saas/core/context"
	"github.com/darkinno-tech/saas/core/types"
)

func TestNewFilterTenantCondition(t *testing.T) {
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})

	filter, err := NewFilter(ctx)
	if err != nil {
		t.Fatalf("NewFilter() error = %v", err)
	}

	condition := filter.Condition()
	if condition.Expression != "tenant_id = ?" {
		t.Fatalf("Condition().Expression = %q, want tenant_id = ?", condition.Expression)
	}
	if len(condition.Args) != 1 || condition.Args[0] != "tenant-a" {
		t.Fatalf("Condition().Args = %#v, want tenant-a", condition.Args)
	}

	id, ok := filter.TenantID()
	if !ok || id != "tenant-a" {
		t.Fatalf("TenantID() = %q, %v; want tenant-a, true", id, ok)
	}
	if filter.IsHost() {
		t.Fatal("IsHost() = true, want false")
	}
}

func TestNewFilterSoftDeleteCondition(t *testing.T) {
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})

	filter, err := NewFilter(ctx, WithTenantField("orders.tenant_id"), WithSoftDeleteField("orders.deleted_at"))
	if err != nil {
		t.Fatalf("NewFilter() error = %v", err)
	}

	condition := filter.Condition()
	want := "orders.tenant_id = ? AND orders.deleted_at IS NULL"
	if condition.Expression != want {
		t.Fatalf("Condition().Expression = %q, want %q", condition.Expression, want)
	}

	filter, err = NewFilter(ctx, WithSoftDeleteField("deleted_at"), WithIncludeSoftDeleted(true))
	if err != nil {
		t.Fatalf("NewFilter(include soft deleted) error = %v", err)
	}
	if got := filter.Condition().Expression; got != "tenant_id = ?" {
		t.Fatalf("Condition().Expression with soft deleted included = %q, want tenant_id = ?", got)
	}
}

func TestNewFilterHostContext(t *testing.T) {
	filter, err := NewFilter(tenantctx.WithHost(context.Background()))
	if err != nil {
		t.Fatalf("NewFilter(host) error = %v", err)
	}

	if !filter.IsHost() {
		t.Fatal("IsHost() = false, want true")
	}
	if id, ok := filter.TenantID(); ok || id != "" {
		t.Fatalf("TenantID() = %q, %v; want empty, false", id, ok)
	}
	if condition := filter.Condition(); !condition.Empty() {
		t.Fatalf("Condition() = %+v, want empty", condition)
	}
}

func TestNewFilterRequiresTenantOrHost(t *testing.T) {
	if _, err := NewFilter(context.Background()); !errors.Is(err, ErrNoTenant) {
		t.Fatalf("NewFilter(background) error = %v, want ErrNoTenant", err)
	}

	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{})
	if _, err := NewFilter(ctx); !errors.Is(err, ErrNoTenant) {
		t.Fatalf("NewFilter(empty tenant id) error = %v, want ErrNoTenant", err)
	}
}

func TestNewFilterRejectsUnsafeFieldNames(t *testing.T) {
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})

	if _, err := NewFilter(ctx, WithTenantField("tenant_id;drop")); !errors.Is(err, ErrInvalidFieldName) {
		t.Fatalf("NewFilter(unsafe tenant field) error = %v, want ErrInvalidFieldName", err)
	}
	if _, err := NewFilter(ctx, WithSoftDeleteField("1deleted_at")); !errors.Is(err, ErrInvalidFieldName) {
		t.Fatalf("NewFilter(unsafe soft delete field) error = %v, want ErrInvalidFieldName", err)
	}
}

func TestNilFilter(t *testing.T) {
	var filter *Filter
	if condition := filter.Condition(); !condition.Empty() {
		t.Fatalf("nil filter Condition() = %+v, want empty", condition)
	}
	if id, ok := filter.TenantID(); ok || id != "" {
		t.Fatalf("nil filter TenantID() = %q, %v; want empty, false", id, ok)
	}
	if filter.IsHost() {
		t.Fatal("nil filter IsHost() = true, want false")
	}
}

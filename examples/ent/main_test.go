package main

import (
	"context"
	"strings"
	"testing"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"

	tenantctx "github.com/darkinno-tech/saas/core/context"
	"github.com/darkinno-tech/saas/core/types"
	enttenant "github.com/darkinno-tech/saas/data/ent"
)

func TestEntExampleFakesApplyTenantRulesLikeGeneratedBuilders(t *testing.T) {
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a", Status: types.TenantStatusActive})
	query := &fakeQuery{}
	if err := enttenant.FilterQuery(ctx, query, enttenant.Config{}); err != nil {
		t.Fatalf("FilterQuery() error = %v", err)
	}
	selector := entsql.Dialect(dialect.MySQL).Select("*").From(entsql.Table("orders"))
	for _, predicate := range query.predicates {
		predicate(selector)
	}
	statement, args := selector.Query()
	if !strings.Contains(statement, "`orders`.`tenant_id` = ?") || len(args) != 1 || args[0] != "tenant-a" {
		t.Fatalf("generated query = %q %#v, want tenant predicate", statement, args)
	}

	mutation := &fakeMutation{op: ent.OpCreate, fields: make(map[string]ent.Value)}
	if err := enttenant.FilterMutation(ctx, mutation, enttenant.Config{}); err != nil {
		t.Fatalf("FilterMutation(create) error = %v", err)
	}
	if got, ok := mutation.Field("tenant_id"); !ok || got != "tenant-a" {
		t.Fatalf("generated mutation tenant_id = %#v, %t; want tenant-a", got, ok)
	}
	if len(mutation.predicates) != 0 {
		t.Fatalf("create mutation predicates = %d, want no read predicate", len(mutation.predicates))
	}
}

package main

import (
	"context"
	"fmt"
	"log"

	tenantctx "github.com/im10furry/saas/core/context"
	"github.com/im10furry/saas/core/types"
	enttenant "github.com/im10furry/saas/data/ent"

	"entgo.io/ent"
)

func main() {
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{
		ID:     "tenant-a",
		Name:   "Tenant A",
		Status: types.TenantStatusActive,
	})

	query := &fakeQuery{}
	if err := enttenant.FilterQuery(ctx, query, enttenant.Config{}); err != nil {
		log.Fatal(err)
	}

	mutation := &fakeMutation{
		op:     ent.OpCreate,
		fields: make(map[string]ent.Value),
	}
	if err := enttenant.FilterMutation(ctx, mutation, enttenant.Config{}); err != nil {
		log.Fatal(err)
	}

	fmt.Printf("query predicates: %d\n", len(query.predicates))
	fmt.Printf("mutation tenant_id: %v\n", mutation.fields["tenant_id"])
}

// Ent-generated builders expose WhereP on query and mutation builders. This
// small fake keeps the example runnable without a generated schema package.
type fakeQuery struct {
	predicates []enttenant.SelectorPredicate
}

func (query *fakeQuery) WhereP(predicates ...enttenant.SelectorPredicate) {
	query.predicates = append(query.predicates, predicates...)
}

type fakeMutation struct {
	op         ent.Op
	fields     map[string]ent.Value
	predicates []enttenant.SelectorPredicate
}

func (mutation *fakeMutation) Op() ent.Op {
	return mutation.op
}

func (mutation *fakeMutation) WhereP(predicates ...enttenant.SelectorPredicate) {
	mutation.predicates = append(mutation.predicates, predicates...)
}

func (mutation *fakeMutation) Field(name string) (ent.Value, bool) {
	value, ok := mutation.fields[name]
	return value, ok
}

func (mutation *fakeMutation) SetField(name string, value ent.Value) error {
	mutation.fields[name] = value
	return nil
}

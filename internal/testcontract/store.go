package testcontract

import (
	"context"
	"errors"
	"testing"

	"github.com/darkinno-tech/saas/core/store"
	"github.com/darkinno-tech/saas/core/types"
)

// StoreFactory creates an empty Store implementation for contract tests.
type StoreFactory func() store.Store

// RunStoreContract verifies common Store behavior.
func RunStoreContract(t *testing.T, factory StoreFactory) {
	t.Helper()

	ctx := context.Background()
	st := factory()
	tenantA := ContractTenant("tenant-a")
	tenantB := ContractTenant("tenant-b")
	tenantB.Status = types.TenantStatusSuspended

	if err := st.Create(ctx, tenantA); err != nil {
		t.Fatalf("Create(tenantA) error = %v", err)
	}
	if err := st.Create(ctx, tenantB); err != nil {
		t.Fatalf("Create(tenantB) error = %v", err)
	}
	if err := st.Create(ctx, tenantA); !errors.Is(err, store.ErrTenantAlreadyExists) {
		t.Fatalf("Create(duplicate) error = %v, want ErrTenantAlreadyExists", err)
	}

	got, err := st.Get(ctx, tenantA.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.ID != tenantA.ID || got.Name != tenantA.Name || got.Status != tenantA.Status {
		t.Fatalf("Get() = %+v, want tenantA", got)
	}

	all, err := st.List(ctx, store.ListFilter{})
	if err != nil {
		t.Fatalf("List(all) error = %v", err)
	}
	if len(all) != 2 || all[0].ID != tenantA.ID || all[1].ID != tenantB.ID {
		t.Fatalf("List(all) = %+v, want tenantA then tenantB", all)
	}

	active, err := st.List(ctx, store.ListFilter{Statuses: []types.TenantStatus{types.TenantStatusActive}})
	if err != nil {
		t.Fatalf("List(active) error = %v", err)
	}
	if len(active) != 1 || active[0].ID != tenantA.ID {
		t.Fatalf("List(active) = %+v, want tenantA", active)
	}

	page, err := st.List(ctx, store.ListFilter{Limit: 1, Offset: 1})
	if err != nil {
		t.Fatalf("List(page) error = %v", err)
	}
	if len(page) != 1 || page[0].ID != tenantB.ID {
		t.Fatalf("List(page) = %+v, want tenantB", page)
	}

	if _, err := st.List(ctx, store.ListFilter{Offset: 1}); !errors.Is(err, store.ErrInvalidListFilter) {
		t.Fatalf("List(invalid page) error = %v, want ErrInvalidListFilter", err)
	}

	paged, ok := st.(store.PagedStore)
	if !ok {
		t.Fatalf("store does not implement PagedStore")
	}
	cursorPage, err := paged.ListPage(ctx, store.PageFilter{Limit: 1, Cursor: tenantA.ID})
	if err != nil {
		t.Fatalf("ListPage(cursor) error = %v", err)
	}
	if len(cursorPage) != 1 || cursorPage[0].ID != tenantB.ID {
		t.Fatalf("ListPage(cursor) = %+v, want tenantB", cursorPage)
	}
	if _, err := paged.ListPage(ctx, store.PageFilter{Limit: 1, Offset: 1, Cursor: tenantA.ID}); !errors.Is(err, store.ErrInvalidListFilter) {
		t.Fatalf("ListPage(cursor with offset) error = %v, want ErrInvalidListFilter", err)
	}

	tenantA.Name = "Tenant A Updated"
	if err := st.Update(ctx, tenantA); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	got, err = st.Get(ctx, tenantA.ID)
	if err != nil {
		t.Fatalf("Get() after update error = %v", err)
	}
	if got.Name != tenantA.Name {
		t.Fatalf("Get() after update name = %q, want %q", got.Name, tenantA.Name)
	}

	if err := st.Update(ctx, ContractTenant("missing")); !errors.Is(err, store.ErrTenantNotFound) {
		t.Fatalf("Update(missing) error = %v, want ErrTenantNotFound", err)
	}
	if _, err := st.Get(ctx, "missing"); !errors.Is(err, store.ErrTenantNotFound) {
		t.Fatalf("Get(missing) error = %v, want ErrTenantNotFound", err)
	}

	if err := st.Delete(ctx, tenantA.ID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := st.Get(ctx, tenantA.ID); !errors.Is(err, store.ErrTenantNotFound) {
		t.Fatalf("Get(deleted) error = %v, want ErrTenantNotFound", err)
	}
	if err := st.Delete(ctx, tenantA.ID); !errors.Is(err, store.ErrTenantNotFound) {
		t.Fatalf("Delete(deleted) error = %v, want ErrTenantNotFound", err)
	}
}

// ContractTenant returns valid tenant metadata for contract tests.
func ContractTenant(id types.TenantID) types.Tenant {
	return types.Tenant{
		ID:     id,
		Name:   "Tenant " + id.String(),
		Status: types.TenantStatusActive,
		PlanID: "starter",
		Config: map[string]string{"feature": "on"},
	}
}

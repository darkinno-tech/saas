package store

import (
	"context"
	"errors"
	"sort"
	"sync"

	"github.com/im10furry/saas/core/types"
)

var _ Store = (*MemoryStore)(nil)
var _ PagedStore = (*MemoryStore)(nil)
var _ CompareAndSwapStore = (*MemoryStore)(nil)

// MemoryStore is a thread-safe in-memory tenant metadata store.
type MemoryStore struct {
	mu      sync.RWMutex
	tenants map[types.TenantID]types.Tenant
}

// NewMemoryStore creates an empty memory store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		tenants: make(map[types.TenantID]types.Tenant),
	}
}

// Get returns tenant metadata by ID.
func (store *MemoryStore) Get(ctx context.Context, id types.TenantID) (types.Tenant, error) {
	if err := ctx.Err(); err != nil {
		return types.Tenant{}, err
	}
	if id == "" {
		return types.Tenant{}, ErrInvalidTenant
	}

	store.mu.RLock()
	defer store.mu.RUnlock()

	tenant, ok := store.tenants[id]
	if !ok {
		return types.Tenant{}, ErrTenantNotFound
	}
	return cloneTenant(tenant), nil
}

// List returns tenants matching filter.
func (store *MemoryStore) List(ctx context.Context, filter ListFilter) ([]types.Tenant, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := filter.validate(); err != nil {
		return nil, err
	}
	return store.list(filter, "")
}

// ListPage returns tenants after the cursor while preserving List filtering semantics.
func (store *MemoryStore) ListPage(ctx context.Context, filter PageFilter) ([]types.Tenant, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := filter.validate(); err != nil {
		return nil, err
	}
	return store.list(filter.listFilter(), filter.Cursor)
}

func (store *MemoryStore) list(filter ListFilter, cursor types.TenantID) ([]types.Tenant, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()

	tenants := make([]types.Tenant, 0, len(store.tenants))
	for _, tenant := range store.tenants {
		if filter.matches(tenant) {
			tenants = append(tenants, cloneTenant(tenant))
		}
	}

	sort.Slice(tenants, func(i, j int) bool {
		return tenants[i].ID < tenants[j].ID
	})
	tenants = seekTenants(tenants, cursor)
	return pageTenants(tenants, filter), nil
}

// Create inserts tenant metadata.
func (store *MemoryStore) Create(ctx context.Context, tenant types.Tenant) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateTenant(tenant); err != nil {
		return err
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	if _, ok := store.tenants[tenant.ID]; ok {
		return ErrTenantAlreadyExists
	}
	store.tenants[tenant.ID] = cloneTenant(tenant)
	return nil
}

// Update replaces existing tenant metadata.
func (store *MemoryStore) Update(ctx context.Context, tenant types.Tenant) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateTenant(tenant); err != nil {
		return err
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	if _, ok := store.tenants[tenant.ID]; !ok {
		return ErrTenantNotFound
	}
	store.tenants[tenant.ID] = cloneTenant(tenant)
	return nil
}

// CompareAndSwap atomically replaces expected tenant metadata with updated.
func (store *MemoryStore) CompareAndSwap(ctx context.Context, expected types.Tenant, updated types.Tenant) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateTenant(expected); err != nil {
		return err
	}
	if err := validateTenant(updated); err != nil {
		return err
	}
	if expected.ID != updated.ID {
		return ErrInvalidTenant
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	current, ok := store.tenants[expected.ID]
	if !ok {
		return ErrTenantNotFound
	}
	if !tenantsEqual(current, expected) {
		return ErrTenantConflict
	}
	store.tenants[updated.ID] = cloneTenant(updated)
	return nil
}

// Delete removes tenant metadata by ID.
func (store *MemoryStore) Delete(ctx context.Context, id types.TenantID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if id == "" {
		return ErrInvalidTenant
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	if _, ok := store.tenants[id]; !ok {
		return ErrTenantNotFound
	}
	delete(store.tenants, id)
	return nil
}

func validateTenant(tenant types.Tenant) error {
	if tenant.ID == "" {
		return ErrInvalidTenant
	}
	if tenant.Status == "" {
		return errors.Join(ErrInvalidTenant, errors.New("tenant status is required"))
	}
	return nil
}

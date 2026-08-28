package tenant

import (
	"context"
	"errors"
	"maps"
	"sync"

	tenantctx "github.com/darkinno-tech/saas/core/context"
	"github.com/darkinno-tech/saas/core/store"
	"github.com/darkinno-tech/saas/core/types"
)

var _ Service = (*Manager)(nil)

const maxCompareAndSwapAttempts = 16

// Manager implements tenant lifecycle operations.
type Manager struct {
	store      store.Store
	generateID IDGenerator
	seed       Seeder
	audit      Auditor
	fallbackMu sync.Mutex
}

// New creates a tenant manager.
func New(store store.Store, opts ...Option) *Manager {
	manager := &Manager{
		store:      store,
		generateID: defaultIDGenerator,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(manager)
		}
	}
	return manager
}

// Create creates a Pending tenant and runs the optional seeder.
func (manager *Manager) Create(ctx context.Context, input CreateInput) (types.Tenant, error) {
	id := input.ID
	if id == "" {
		generated, err := manager.generateID(ctx)
		if err != nil {
			return types.Tenant{}, err
		}
		id = generated
	}

	tenant := types.Tenant{
		ID:     id,
		Name:   input.Name,
		Status: types.TenantStatusPending,
		PlanID: input.PlanID,
		Config: cloneConfig(input.Config),
	}
	if err := manager.store.Create(ctx, tenant); err != nil {
		return types.Tenant{}, err
	}
	if manager.seed != nil {
		if seedErr := manager.seed(tenantctx.WithTenant(ctx, tenant), tenant); seedErr != nil {
			if deleteErr := manager.store.Delete(ctx, tenant.ID); deleteErr != nil {
				return tenant, errors.Join(seedErr, deleteErr)
			}
			return types.Tenant{}, seedErr
		}
	}
	if err := manager.emit(ctx, Event{TenantID: tenant.ID, Action: "create", To: tenant.Status}); err != nil {
		return tenant, err
	}
	return tenant, nil
}

// Get returns tenant metadata.
func (manager *Manager) Get(ctx context.Context, id types.TenantID) (types.Tenant, error) {
	return manager.store.Get(ctx, id)
}

// Update updates tenant metadata without changing lifecycle status.
func (manager *Manager) Update(ctx context.Context, input UpdateInput) (types.Tenant, error) {
	for attempt := 0; attempt < maxCompareAndSwapAttempts; attempt++ {
		current, err := manager.store.Get(ctx, input.ID)
		if err != nil {
			return types.Tenant{}, err
		}

		updated := current
		updated.Name = input.Name
		updated.PlanID = input.PlanID
		updated.Config = cloneConfig(input.Config)
		if err := manager.compareAndSwap(ctx, current, updated); err != nil {
			if errors.Is(err, store.ErrTenantConflict) && attempt+1 < maxCompareAndSwapAttempts {
				continue
			}
			return types.Tenant{}, err
		}
		if err := manager.emit(ctx, Event{TenantID: updated.ID, Action: "update", From: updated.Status, To: updated.Status}); err != nil {
			return types.Tenant{}, err
		}
		return updated, nil
	}
	return types.Tenant{}, store.ErrTenantConflict
}

// Delete soft-deletes tenant metadata.
func (manager *Manager) Delete(ctx context.Context, id types.TenantID) error {
	_, err := manager.SoftDelete(ctx, id)
	return err
}

func (manager *Manager) transition(ctx context.Context, id types.TenantID, action string, allowed map[types.TenantStatus]types.TenantStatus) (types.Tenant, error) {
	for attempt := 0; attempt < maxCompareAndSwapAttempts; attempt++ {
		current, err := manager.store.Get(ctx, id)
		if err != nil {
			return types.Tenant{}, err
		}

		next, ok := allowed[current.Status]
		if !ok {
			return types.Tenant{}, ErrInvalidState
		}

		updated := current
		updated.Status = next
		if err := manager.compareAndSwap(ctx, current, updated); err != nil {
			if errors.Is(err, store.ErrTenantConflict) && attempt+1 < maxCompareAndSwapAttempts {
				continue
			}
			return types.Tenant{}, err
		}
		if err := manager.emit(ctx, Event{TenantID: id, Action: action, From: current.Status, To: next}); err != nil {
			return types.Tenant{}, err
		}
		return updated, nil
	}
	return types.Tenant{}, store.ErrTenantConflict
}

func (manager *Manager) compareAndSwap(ctx context.Context, expected types.Tenant, updated types.Tenant) error {
	if conditional, ok := manager.store.(store.CompareAndSwapStore); ok {
		return conditional.CompareAndSwap(ctx, expected, updated)
	}

	// Third-party Store implementations keep source compatibility. Serialize the
	// fallback inside this Manager and verify the snapshot immediately before the
	// legacy Update call. Stores shared by multiple Manager instances should add
	// CompareAndSwapStore support for cross-instance atomicity.
	manager.fallbackMu.Lock()
	defer manager.fallbackMu.Unlock()

	current, err := manager.store.Get(ctx, expected.ID)
	if err != nil {
		return err
	}
	if !tenantEqual(current, expected) {
		return store.ErrTenantConflict
	}
	return manager.store.Update(ctx, updated)
}

func tenantEqual(a types.Tenant, b types.Tenant) bool {
	return a.ID == b.ID &&
		a.Name == b.Name &&
		a.Status == b.Status &&
		a.PlanID == b.PlanID &&
		maps.Equal(a.Config, b.Config)
}

func (manager *Manager) emit(ctx context.Context, event Event) error {
	if manager.audit == nil {
		return nil
	}
	return manager.audit(ctx, event)
}

func cloneConfig(config map[string]string) map[string]string {
	if config == nil {
		return nil
	}
	cloned := make(map[string]string, len(config))
	for key, value := range config {
		cloned[key] = value
	}
	return cloned
}

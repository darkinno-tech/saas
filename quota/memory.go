package quota

import (
	"context"
	"math"
	"sync"

	"github.com/im10furry/saas/core/types"
)

var _ Store = (*MemoryStore)(nil)

// MemoryStore tracks usage in memory.
type MemoryStore struct {
	mu    sync.Mutex
	usage map[key]int64
}

type key struct {
	tenantID types.TenantID
	resource string
	period   Period
}

// NewMemoryStore creates an empty usage store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{usage: map[key]int64{}}
}

// Add increments usage.
func (store *MemoryStore) Add(ctx context.Context, tenantID types.TenantID, resource string, period Period, amount int64) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if amount < 0 || tenantID == "" || resource == "" || period == "" {
		return 0, ErrInvalidQuota
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	k := key{tenantID: tenantID, resource: resource, period: period}
	if amount > math.MaxInt64-store.usage[k] {
		return 0, ErrInvalidQuota
	}
	store.usage[k] += amount
	return store.usage[k], nil
}

// Consume increments usage only when the configured limit allows it.
func (store *MemoryStore) Consume(ctx context.Context, limit Limit, amount int64) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := validateLimit(limit); err != nil {
		return 0, err
	}
	if amount < 0 {
		return 0, ErrInvalidQuota
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	k := key{tenantID: limit.TenantID, resource: limit.Resource, period: limit.Period}
	used := store.usage[k]
	if amount > limit.Limit-used {
		return used, ErrQuotaExceeded
	}
	used += amount
	store.usage[k] = used
	return used, nil
}

// Get returns current usage.
func (store *MemoryStore) Get(ctx context.Context, tenantID types.TenantID, resource string, period Period) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if tenantID == "" || resource == "" || period == "" {
		return 0, ErrInvalidQuota
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	return store.usage[key{tenantID: tenantID, resource: resource, period: period}], nil
}

// Reset clears usage.
func (store *MemoryStore) Reset(ctx context.Context, tenantID types.TenantID, resource string, period Period) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if tenantID == "" || resource == "" || period == "" {
		return ErrInvalidQuota
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	delete(store.usage, key{tenantID: tenantID, resource: resource, period: period})
	return nil
}

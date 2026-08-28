package feature

import (
	"context"
	"sort"
	"sync"

	"github.com/im10furry/saas/core/types"
)

var _ Store = (*MemoryStore)(nil)

// MemoryStore stores feature flags in memory.
type MemoryStore struct {
	mu              sync.RWMutex
	planDefaults    map[string]map[string]Flag
	tenantOverrides map[types.TenantID]map[string]Flag
}

// NewMemoryStore creates an empty feature store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		planDefaults:    map[string]map[string]Flag{},
		tenantOverrides: map[types.TenantID]map[string]Flag{},
	}
}

// SetPlanDefaults replaces default flags for a plan.
func (store *MemoryStore) SetPlanDefaults(ctx context.Context, planID string, flags []Flag) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if planID == "" {
		return ErrInvalidFeature
	}

	index, err := indexFlags(flags)
	if err != nil {
		return err
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	store.planDefaults[planID] = index
	return nil
}

// SetTenantOverrides replaces tenant-level overrides.
func (store *MemoryStore) SetTenantOverrides(ctx context.Context, tenantID types.TenantID, flags []Flag) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if tenantID == "" {
		return ErrInvalidFeature
	}

	index, err := indexFlags(flags)
	if err != nil {
		return err
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	store.tenantOverrides[tenantID] = index
	return nil
}

// Resolve returns the tenant override when present, otherwise the plan default.
func (store *MemoryStore) Resolve(ctx context.Context, tenantID types.TenantID, planID string, key string) (Flag, error) {
	if err := ctx.Err(); err != nil {
		return Flag{}, err
	}
	if tenantID == "" || planID == "" || key == "" {
		return Flag{}, ErrInvalidFeature
	}

	store.mu.RLock()
	defer store.mu.RUnlock()

	if flags := store.tenantOverrides[tenantID]; flags != nil {
		if flag, ok := flags[key]; ok {
			return cloneFlag(flag), nil
		}
	}
	if flags := store.planDefaults[planID]; flags != nil {
		if flag, ok := flags[key]; ok {
			return cloneFlag(flag), nil
		}
	}
	return Flag{}, ErrFeatureNotFound
}

// List returns merged feature flags.
func (store *MemoryStore) List(ctx context.Context, tenantID types.TenantID, planID string) ([]Flag, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if tenantID == "" || planID == "" {
		return nil, ErrInvalidFeature
	}

	store.mu.RLock()
	defer store.mu.RUnlock()

	merged := map[string]Flag{}
	for key, flag := range store.planDefaults[planID] {
		merged[key] = cloneFlag(flag)
	}
	for key, flag := range store.tenantOverrides[tenantID] {
		merged[key] = cloneFlag(flag)
	}

	keys := make([]string, 0, len(merged))
	for key := range merged {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	flags := make([]Flag, 0, len(keys))
	for _, key := range keys {
		flags = append(flags, merged[key])
	}
	return flags, nil
}

func indexFlags(flags []Flag) (map[string]Flag, error) {
	index := make(map[string]Flag, len(flags))
	for _, flag := range flags {
		if flag.Key == "" {
			return nil, ErrInvalidFeature
		}
		index[flag.Key] = cloneFlag(flag)
	}
	return index, nil
}

func cloneFlag(flag Flag) Flag {
	return Flag{
		Key:     flag.Key,
		Enabled: flag.Enabled,
		Config:  cloneStringMap(flag.Config),
	}
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

package identity

import (
	"context"
	"slices"
	"sync"

	"github.com/darkinno-tech/saas/core/types"
)

var _ Store = (*MemoryStore)(nil)

type MemoryStore struct {
	mu    sync.RWMutex
	links map[externalKey]Link
}

type externalKey struct {
	tenantID types.TenantID
	provider ProviderKey
	subject  string
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{links: map[externalKey]Link{}}
}

func (store *MemoryStore) Link(ctx context.Context, link Link) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := link.validate(); err != nil {
		return err
	}

	key := externalKey{tenantID: link.TenantID, provider: link.Provider, subject: link.Subject}

	store.mu.Lock()
	defer store.mu.Unlock()

	if current, ok := store.links[key]; ok && current.UserID != link.UserID {
		return ErrIdentityConflict
	}
	store.links[key] = cloneLink(link)
	return nil
}

func (store *MemoryStore) GetByExternal(ctx context.Context, tenantID types.TenantID, provider ProviderKey, subject string) (Link, error) {
	if err := ctx.Err(); err != nil {
		return Link{}, err
	}
	if tenantID == "" || provider == "" || subject == "" {
		return Link{}, ErrInvalidIdentity
	}

	store.mu.RLock()
	defer store.mu.RUnlock()

	link, ok := store.links[externalKey{tenantID: tenantID, provider: provider, subject: subject}]
	if !ok {
		return Link{}, ErrIdentityNotFound
	}
	return cloneLink(link), nil
}

func (store *MemoryStore) GetByUser(ctx context.Context, tenantID types.TenantID, userID string) ([]Link, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if tenantID == "" || userID == "" {
		return nil, ErrInvalidIdentity
	}

	store.mu.RLock()
	defer store.mu.RUnlock()

	links := []Link{}
	for _, link := range store.links {
		if link.TenantID == tenantID && link.UserID == userID {
			links = append(links, cloneLink(link))
		}
	}
	slices.SortFunc(links, func(a Link, b Link) int {
		if a.Provider < b.Provider {
			return -1
		}
		if a.Provider > b.Provider {
			return 1
		}
		if a.Subject < b.Subject {
			return -1
		}
		if a.Subject > b.Subject {
			return 1
		}
		return 0
	})
	return links, nil
}

func (store *MemoryStore) Unlink(ctx context.Context, tenantID types.TenantID, provider ProviderKey, subject string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if tenantID == "" || provider == "" || subject == "" {
		return ErrInvalidIdentity
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	key := externalKey{tenantID: tenantID, provider: provider, subject: subject}
	if _, ok := store.links[key]; !ok {
		return ErrIdentityNotFound
	}
	delete(store.links, key)
	return nil
}

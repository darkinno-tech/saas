package audit

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/im10furry/saas/core/types"
)

var _ Store = (*MemoryStore)(nil)
var _ PagedStore = (*MemoryStore)(nil)

type MemoryStore struct {
	mu     sync.RWMutex
	events []Event
	now    func() time.Time
}

type Option func(*MemoryStore)

func WithClock(clock func() time.Time) Option {
	return func(store *MemoryStore) {
		if clock != nil {
			store.now = clock
		}
	}
}

func NewMemoryStore(opts ...Option) *MemoryStore {
	store := &MemoryStore{now: time.Now}
	for _, opt := range opts {
		if opt != nil {
			opt(store)
		}
	}
	return store
}

func (store *MemoryStore) Record(ctx context.Context, event Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if event.TenantID == "" || event.Action == "" || event.Resource == "" {
		return ErrInvalidEvent
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = store.now()
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	store.events = append(store.events, cloneEvent(event))
	return nil
}

func (store *MemoryStore) List(ctx context.Context, tenantID types.TenantID) ([]Event, error) {
	return store.ListPage(ctx, tenantID, ListFilter{})
}

func (store *MemoryStore) ListPage(ctx context.Context, tenantID types.TenantID, filter ListFilter) ([]Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if tenantID == "" {
		return nil, ErrInvalidEvent
	}
	if err := filter.validate(); err != nil {
		return nil, err
	}

	store.mu.RLock()
	defer store.mu.RUnlock()

	events := []Event{}
	for _, event := range store.events {
		if event.TenantID == tenantID && eventAfterCursor(event, filter.Cursor) {
			events = append(events, cloneEvent(event))
		}
	}
	sort.Slice(events, func(i, j int) bool {
		if events[i].CreatedAt.Equal(events[j].CreatedAt) {
			return events[i].ID < events[j].ID
		}
		return events[i].CreatedAt.Before(events[j].CreatedAt)
	})
	return pageEvents(events, filter), nil
}

func cloneEvent(event Event) Event {
	if event.Metadata == nil {
		return event
	}
	metadata := make(map[string]string, len(event.Metadata))
	for key, value := range event.Metadata {
		metadata[key] = value
	}
	event.Metadata = metadata
	return event
}

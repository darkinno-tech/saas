package audit

import (
	"context"
	"time"

	"github.com/darkinno-tech/saas/core/types"
)

type Store interface {
	Record(ctx context.Context, event Event) error
	List(ctx context.Context, tenantID types.TenantID) ([]Event, error)
}

// PagedStore extends Store with cursor-based event listing.
type PagedStore interface {
	Store
	ListPage(ctx context.Context, tenantID types.TenantID, filter ListFilter) ([]Event, error)
}

// Cursor identifies the last audit event from a previous ordered page.
type Cursor struct {
	CreatedAt time.Time
	ID        string
}

// ListFilter restricts paged audit event list queries.
type ListFilter struct {
	Cursor Cursor
	Limit  int
}

// CursorFor returns a cursor for continuing after event.
func CursorFor(event Event) Cursor {
	return Cursor{CreatedAt: event.CreatedAt, ID: event.ID}
}

func (filter ListFilter) validate() error {
	if filter.Limit < 0 {
		return ErrInvalidListFilter
	}
	if filter.Cursor.CreatedAt.IsZero() != (filter.Cursor.ID == "") {
		return ErrInvalidListFilter
	}
	return nil
}

func (cursor Cursor) empty() bool {
	return cursor.CreatedAt.IsZero() && cursor.ID == ""
}

func eventAfterCursor(event Event, cursor Cursor) bool {
	if cursor.empty() {
		return true
	}
	if event.CreatedAt.After(cursor.CreatedAt) {
		return true
	}
	return event.CreatedAt.Equal(cursor.CreatedAt) && event.ID > cursor.ID
}

func pageEvents(events []Event, filter ListFilter) []Event {
	if filter.Limit > 0 && len(events) > filter.Limit {
		return events[:filter.Limit]
	}
	return events
}

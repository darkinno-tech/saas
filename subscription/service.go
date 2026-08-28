package subscription

import (
	"context"
	"time"

	"github.com/darkinno-tech/saas/core/types"
)

// Service manages tenant subscriptions.
type Service interface {
	Subscribe(ctx context.Context, tenantID types.TenantID, planID string) (Subscription, error)
	Unsubscribe(ctx context.Context, tenantID types.TenantID) (Subscription, error)
	Upgrade(ctx context.Context, tenantID types.TenantID, planID string) (Subscription, error)
	Downgrade(ctx context.Context, tenantID types.TenantID, planID string) (Subscription, error)
	Get(ctx context.Context, tenantID types.TenantID) (Subscription, error)
}

// LifecycleService extends Service with billing-period renewal and expiration operations.
type LifecycleService interface {
	Service
	SubscribeWithPeriod(ctx context.Context, tenantID types.TenantID, planID string, currentPeriodEnd time.Time) (Subscription, error)
	Renew(ctx context.Context, tenantID types.TenantID, currentPeriodEnd time.Time) (Subscription, error)
	Expire(ctx context.Context, tenantID types.TenantID) (Subscription, error)
	ExpireDue(ctx context.Context) ([]Subscription, error)
}

// Store persists tenant subscriptions.
type Store interface {
	Create(ctx context.Context, subscription Subscription) error
	Get(ctx context.Context, tenantID types.TenantID) (Subscription, error)
	List(ctx context.Context, filter ListFilter) ([]Subscription, error)
	Update(ctx context.Context, subscription Subscription) error
	Delete(ctx context.Context, tenantID types.TenantID) error
}

// PagedStore extends Store with cursor-based subscription listing.
type PagedStore interface {
	Store
	ListPage(ctx context.Context, filter PageFilter) ([]Subscription, error)
}

// ListFilter restricts subscription list queries.
type ListFilter struct {
	TenantIDs []types.TenantID
	PlanIDs   []string
	Statuses  []Status
	Limit     int
	Offset    int
}

// PageFilter restricts cursor-based subscription list queries.
type PageFilter struct {
	TenantIDs []types.TenantID
	PlanIDs   []string
	Statuses  []Status
	Limit     int
	Offset    int
	// Cursor returns rows ordered after the tenant ID cursor.
	Cursor types.TenantID
}

func (filter ListFilter) matches(subscription Subscription) bool {
	if len(filter.TenantIDs) > 0 {
		matched := false
		for _, tenantID := range filter.TenantIDs {
			if subscription.TenantID == tenantID {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	if len(filter.PlanIDs) > 0 {
		matched := false
		for _, planID := range filter.PlanIDs {
			if subscription.PlanID == planID {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	if len(filter.Statuses) > 0 {
		matched := false
		for _, status := range filter.Statuses {
			if subscription.Status == status {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

func (filter ListFilter) validate() error {
	if filter.Limit < 0 || filter.Offset < 0 {
		return ErrInvalidListFilter
	}
	if filter.Offset > 0 && filter.Limit == 0 {
		return ErrInvalidListFilter
	}
	for _, tenantID := range filter.TenantIDs {
		if tenantID == "" {
			return ErrInvalidListFilter
		}
	}
	for _, planID := range filter.PlanIDs {
		if planID == "" {
			return ErrInvalidListFilter
		}
	}
	for _, status := range filter.Statuses {
		if !validStatus(status) {
			return ErrInvalidListFilter
		}
	}
	return nil
}

func (filter PageFilter) validate() error {
	if err := filter.listFilter().validate(); err != nil {
		return err
	}
	if filter.Cursor != "" && filter.Offset > 0 {
		return ErrInvalidListFilter
	}
	return nil
}

func (filter PageFilter) listFilter() ListFilter {
	return ListFilter{
		TenantIDs: filter.TenantIDs,
		PlanIDs:   filter.PlanIDs,
		Statuses:  filter.Statuses,
		Limit:     filter.Limit,
		Offset:    filter.Offset,
	}
}

func pageSubscriptions(subscriptions []Subscription, filter ListFilter) []Subscription {
	if filter.Offset >= len(subscriptions) {
		return []Subscription{}
	}

	start := filter.Offset
	end := len(subscriptions)
	if filter.Limit > 0 && filter.Limit < end-start {
		end = start + filter.Limit
	}
	return subscriptions[start:end]
}

func seekSubscriptions(subscriptions []Subscription, cursor types.TenantID) []Subscription {
	if cursor == "" {
		return subscriptions
	}
	start := len(subscriptions)
	for i, subscription := range subscriptions {
		if subscription.TenantID > cursor {
			start = i
			break
		}
	}
	return subscriptions[start:]
}

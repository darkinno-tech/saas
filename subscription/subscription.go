package subscription

import (
	"time"

	"github.com/im10furry/saas/core/types"
)

// Subscription binds a tenant to a plan.
type Subscription struct {
	TenantID         types.TenantID
	PlanID           string
	Status           Status
	StartDate        time.Time
	EndDate          *time.Time
	CurrentPeriodEnd *time.Time
	GracePeriodEnd   *time.Time
}

func subscriptionsEqual(a Subscription, b Subscription) bool {
	return a.TenantID == b.TenantID &&
		a.PlanID == b.PlanID &&
		a.Status == b.Status &&
		a.StartDate.Equal(b.StartDate) &&
		timePointersEqual(a.EndDate, b.EndDate) &&
		timePointersEqual(a.CurrentPeriodEnd, b.CurrentPeriodEnd) &&
		timePointersEqual(a.GracePeriodEnd, b.GracePeriodEnd)
}

func timePointersEqual(a *time.Time, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

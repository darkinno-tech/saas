package subscription

import (
	"context"
	"time"

	"github.com/darkinno-tech/saas/core/types"
)

// BillingEvent describes a subscription change for external billing systems.
type BillingEvent struct {
	TenantID         types.TenantID
	Action           string
	FromPlan         string
	ToPlan           string
	Status           Status
	CurrentPeriodEnd *time.Time
}

// BillingHook receives subscription lifecycle changes. Hooks that call back
// into MemoryService must propagate the received context: it carries the
// staged-read and re-entrant-mutation guard for the in-flight event. Replacing
// it with context.Background can make a same-tenant mutation wait on itself.
type BillingHook func(context.Context, BillingEvent) error

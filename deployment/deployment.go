package deployment

import (
	"context"

	"github.com/darkinno-tech/saas/core/types"
)

// Resolver resolves a tenant to its active deployment unit.
type Resolver interface {
	Resolve(context.Context, types.Tenant) (types.DeploymentUnit, error)
}

// Policy approves a tenant's use of a deployment unit. Hosts can use it to
// enforce data-residency, contractual, or other placement rules.
type Policy interface {
	Validate(context.Context, types.Tenant, types.DeploymentUnit) error
}

// PolicyFunc adapts a function into a Policy.
type PolicyFunc func(context.Context, types.Tenant, types.DeploymentUnit) error

// Validate implements Policy.
func (policy PolicyFunc) Validate(ctx context.Context, tenant types.Tenant, unit types.DeploymentUnit) error {
	if policy == nil {
		return nil
	}
	return policy(ctx, tenant, unit)
}

// Assignment is the current deployment unit selected for a tenant. Version is
// incremented on every cutover and supports compare-and-swap persistence.
type Assignment struct {
	TenantID types.TenantID
	UnitID   types.DeploymentUnitID
	Version  uint64
}

// Move is a prepared, not-yet-cut-over tenant placement change.
type Move struct {
	TenantID     types.TenantID
	SourceUnitID types.DeploymentUnitID
	TargetUnitID types.DeploymentUnitID
}

// Event describes a committed deployment control-plane change.
type Event struct {
	Action       string
	TenantID     types.TenantID
	UnitID       types.DeploymentUnitID
	SourceUnitID types.DeploymentUnitID
	TargetUnitID types.DeploymentUnitID
}

// Auditor observes committed deployment control-plane changes. If Record
// returns an error, the change remains committed and the service returns the
// error to the caller so the audit failure is visible.
type Auditor interface {
	Record(context.Context, Event) error
}

// AuditorFunc adapts a function into an Auditor.
type AuditorFunc func(context.Context, Event) error

// Record implements Auditor.
func (auditor AuditorFunc) Record(ctx context.Context, event Event) error {
	if auditor == nil {
		return nil
	}
	return auditor(ctx, event)
}

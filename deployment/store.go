package deployment

import (
	"context"

	"github.com/darkinno-tech/saas/core/types"
)

// Store persists deployment units, current assignments, and prepared moves.
// Implementations must preserve the placement invariants across processes:
// assignment and move creation may reference only active units, disabling or
// deleting a unit must reject references, and CutoverMove must atomically
// replace the expected assignment and remove the exact prepared move.
type Store interface {
	GetUnit(ctx context.Context, id types.DeploymentUnitID) (types.DeploymentUnit, error)
	ListUnits(ctx context.Context) ([]types.DeploymentUnit, error)
	CreateUnit(ctx context.Context, unit types.DeploymentUnit) error
	UpdateUnit(ctx context.Context, unit types.DeploymentUnit) error
	DisableUnit(ctx context.Context, id types.DeploymentUnitID) (types.DeploymentUnit, bool, error)
	DeleteUnit(ctx context.Context, id types.DeploymentUnitID) error

	GetAssignment(ctx context.Context, tenantID types.TenantID) (Assignment, error)
	ListAssignmentsByUnit(ctx context.Context, unitID types.DeploymentUnitID) ([]Assignment, error)
	CreateAssignment(ctx context.Context, assignment Assignment) error
	CompareAndSwapAssignment(ctx context.Context, expected Assignment, updated Assignment) error
	CutoverMove(ctx context.Context, expected Assignment, move Move, updated Assignment) error

	GetMove(ctx context.Context, tenantID types.TenantID) (Move, error)
	ListMovesByUnit(ctx context.Context, unitID types.DeploymentUnitID) ([]Move, error)
	CreateMove(ctx context.Context, move Move) error
	DeleteMove(ctx context.Context, tenantID types.TenantID) error
	DeleteMoveIfMatch(ctx context.Context, move Move) error
}

package deployment

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"

	"github.com/darkinno-tech/saas/core/types"
)

var _ Resolver = (*Service)(nil)

// Service manages deployment units and tenant placement changes.
type Service struct {
	store   Store
	policy  Policy
	auditor Auditor

	// mutationMu makes compound operations safe for a single service instance.
	// The Store's atomic placement operations remain the cross-instance
	// concurrency boundary.
	mutationMu sync.Mutex
}

// Option configures a deployment service.
type Option func(*Service)

// WithPolicy sets the host-owned placement policy.
func WithPolicy(policy Policy) Option {
	return func(service *Service) {
		service.policy = policy
	}
}

// WithAuditor sets the optional committed-change auditor.
func WithAuditor(auditor Auditor) Option {
	return func(service *Service) {
		service.auditor = auditor
	}
}

// New creates a deployment service backed by store.
func New(store Store, opts ...Option) *Service {
	service := &Service{store: store}
	for _, opt := range opts {
		if opt != nil {
			opt(service)
		}
	}
	return service
}

// CreateUnit adds a deployment unit to the directory.
func (service *Service) CreateUnit(ctx context.Context, unit types.DeploymentUnit) error {
	if err := service.ready(ctx); err != nil {
		return err
	}
	if err := validateUnit(unit); err != nil {
		return err
	}

	service.mutationMu.Lock()
	err := service.store.CreateUnit(ctx, unit)
	service.mutationMu.Unlock()
	if err != nil {
		return err
	}
	return service.emit(ctx, Event{Action: "deployment_unit.create", UnitID: unit.ID})
}

// UpdateUnit replaces mutable deployment unit metadata. Status changes are
// reserved for DisableUnit so assignment and move safety checks cannot be
// bypassed.
func (service *Service) UpdateUnit(ctx context.Context, unit types.DeploymentUnit) error {
	if err := service.ready(ctx); err != nil {
		return err
	}
	if err := validateUnit(unit); err != nil {
		return err
	}

	service.mutationMu.Lock()
	err := service.store.UpdateUnit(ctx, unit)
	service.mutationMu.Unlock()
	if err != nil {
		return err
	}
	return service.emit(ctx, Event{Action: "deployment_unit.update", UnitID: unit.ID})
}

// DisableUnit makes an unreferenced deployment unit unavailable for new
// assignments and cutovers. It is idempotent for an already-disabled unit.
func (service *Service) DisableUnit(ctx context.Context, id types.DeploymentUnitID) (types.DeploymentUnit, error) {
	if err := service.ready(ctx); err != nil {
		return types.DeploymentUnit{}, err
	}
	if id == "" {
		return types.DeploymentUnit{}, ErrInvalidDeploymentUnit
	}

	service.mutationMu.Lock()
	unit, changed, err := service.store.DisableUnit(ctx, id)
	service.mutationMu.Unlock()
	if err != nil {
		return types.DeploymentUnit{}, err
	}
	if changed {
		if err := service.emit(ctx, Event{Action: "deployment_unit.disable", UnitID: id}); err != nil {
			return unit, err
		}
	}
	return unit, nil
}

// DeleteUnit removes an unreferenced deployment unit.
func (service *Service) DeleteUnit(ctx context.Context, id types.DeploymentUnitID) error {
	if err := service.ready(ctx); err != nil {
		return err
	}
	if id == "" {
		return ErrInvalidDeploymentUnit
	}

	service.mutationMu.Lock()
	_, err := service.store.GetUnit(ctx, id)
	if err == nil {
		err = service.store.DeleteUnit(ctx, id)
	}
	service.mutationMu.Unlock()
	if err != nil {
		return err
	}
	return service.emit(ctx, Event{Action: "deployment_unit.delete", UnitID: id})
}

// Assign records a tenant's initial deployment unit. Existing assignments are
// intentionally immutable outside the prepared move workflow.
func (service *Service) Assign(ctx context.Context, tenant types.Tenant, unitID types.DeploymentUnitID) (Assignment, error) {
	if err := service.ready(ctx); err != nil {
		return Assignment{}, err
	}
	if tenant.ID == "" || unitID == "" {
		return Assignment{}, ErrInvalidAssignment
	}

	service.mutationMu.Lock()
	unit, err := service.activeUnitFor(ctx, tenant, unitID)
	if err == nil {
		assignment := Assignment{TenantID: tenant.ID, UnitID: unit.ID, Version: 1}
		err = service.store.CreateAssignment(ctx, assignment)
		if err == nil {
			service.mutationMu.Unlock()
			if auditErr := service.emit(ctx, Event{Action: "assignment.create", TenantID: tenant.ID, UnitID: unit.ID}); auditErr != nil {
				return assignment, auditErr
			}
			return assignment, nil
		}
	}
	service.mutationMu.Unlock()
	return Assignment{}, err
}

// Resolve returns the active deployment unit assigned to tenant.
func (service *Service) Resolve(ctx context.Context, tenant types.Tenant) (types.DeploymentUnit, error) {
	if err := service.ready(ctx); err != nil {
		return types.DeploymentUnit{}, err
	}
	if tenant.ID == "" {
		return types.DeploymentUnit{}, ErrInvalidAssignment
	}

	assignment, err := service.store.GetAssignment(ctx, tenant.ID)
	if err != nil {
		return types.DeploymentUnit{}, err
	}
	unit, err := service.store.GetUnit(ctx, assignment.UnitID)
	if err != nil {
		return types.DeploymentUnit{}, err
	}
	if unit.Status != types.DeploymentUnitStatusActive {
		return types.DeploymentUnit{}, ErrDeploymentUnitUnavailable
	}
	return unit, nil
}

// PrepareMove records a target deployment unit while resolution continues to
// use the current source assignment. The host performs data copying and
// validation before calling CutoverMove.
func (service *Service) PrepareMove(ctx context.Context, tenant types.Tenant, targetID types.DeploymentUnitID) (Move, error) {
	if err := service.ready(ctx); err != nil {
		return Move{}, err
	}
	if tenant.ID == "" || targetID == "" {
		return Move{}, ErrInvalidMove
	}

	service.mutationMu.Lock()
	assignment, err := service.store.GetAssignment(ctx, tenant.ID)
	if err == nil && assignment.UnitID == targetID {
		err = ErrInvalidMove
	}
	var target types.DeploymentUnit
	if err == nil {
		target, err = service.activeUnitFor(ctx, tenant, targetID)
	}
	move := Move{TenantID: tenant.ID, SourceUnitID: assignment.UnitID, TargetUnitID: target.ID}
	if err == nil {
		err = service.store.CreateMove(ctx, move)
	}
	service.mutationMu.Unlock()
	if err != nil {
		return Move{}, err
	}
	if err := service.emit(ctx, Event{
		Action:       "move.prepare",
		TenantID:     tenant.ID,
		SourceUnitID: move.SourceUnitID,
		TargetUnitID: move.TargetUnitID,
	}); err != nil {
		return move, err
	}
	return move, nil
}

// CutoverMove atomically changes the current assignment from a prepared move's
// source to its target and clears the move through the Store's transactional
// cutover operation.
func (service *Service) CutoverMove(ctx context.Context, tenant types.Tenant) (Assignment, error) {
	if err := service.ready(ctx); err != nil {
		return Assignment{}, err
	}
	if tenant.ID == "" {
		return Assignment{}, ErrInvalidMove
	}

	service.mutationMu.Lock()
	assignment, move, cutover, err := service.cutover(ctx, tenant)
	service.mutationMu.Unlock()
	if err != nil {
		return assignment, err
	}
	if cutover {
		if err := service.emit(ctx, Event{
			Action:       "move.cutover",
			TenantID:     tenant.ID,
			UnitID:       assignment.UnitID,
			SourceUnitID: move.SourceUnitID,
			TargetUnitID: move.TargetUnitID,
		}); err != nil {
			return assignment, err
		}
	}
	return assignment, nil
}

func (service *Service) cutover(ctx context.Context, tenant types.Tenant) (Assignment, Move, bool, error) {
	move, err := service.store.GetMove(ctx, tenant.ID)
	if err != nil {
		return Assignment{}, Move{}, false, err
	}
	assignment, err := service.store.GetAssignment(ctx, tenant.ID)
	if err != nil {
		return Assignment{}, Move{}, false, err
	}

	if assignment.UnitID != move.SourceUnitID {
		return Assignment{}, Move{}, false, ErrMoveConflict
	}
	if assignment.Version == math.MaxUint64 {
		return Assignment{}, Move{}, false, ErrAssignmentConflict
	}

	if _, err := service.activeUnitFor(ctx, tenant, move.TargetUnitID); err != nil {
		return Assignment{}, Move{}, false, err
	}
	next := assignment
	next.UnitID = move.TargetUnitID
	next.Version++
	if err := service.store.CutoverMove(ctx, assignment, move, next); err != nil {
		return Assignment{}, Move{}, false, err
	}
	return next, move, true, nil
}

// CancelMove discards a prepared move without altering the current assignment.
func (service *Service) CancelMove(ctx context.Context, tenantID types.TenantID) error {
	if err := service.ready(ctx); err != nil {
		return err
	}
	if tenantID == "" {
		return ErrInvalidMove
	}

	service.mutationMu.Lock()
	move, err := service.store.GetMove(ctx, tenantID)
	if err == nil {
		err = service.store.DeleteMoveIfMatch(ctx, move)
	}
	service.mutationMu.Unlock()
	if err != nil {
		return err
	}
	return service.emit(ctx, Event{
		Action:       "move.cancel",
		TenantID:     tenantID,
		SourceUnitID: move.SourceUnitID,
		TargetUnitID: move.TargetUnitID,
	})
}

func (service *Service) ready(ctx context.Context) error {
	if service == nil || service.store == nil {
		return ErrNilStore
	}
	return ctx.Err()
}

func (service *Service) activeUnitFor(ctx context.Context, tenant types.Tenant, unitID types.DeploymentUnitID) (types.DeploymentUnit, error) {
	unit, err := service.store.GetUnit(ctx, unitID)
	if err != nil {
		return types.DeploymentUnit{}, err
	}
	if unit.Status != types.DeploymentUnitStatusActive {
		return types.DeploymentUnit{}, ErrDeploymentUnitUnavailable
	}
	if service.policy != nil {
		if err := service.policy.Validate(ctx, tenant, unit); err != nil {
			return types.DeploymentUnit{}, fmt.Errorf("%w: %w", ErrPolicyDenied, err)
		}
	}
	return unit, nil
}

func (service *Service) emit(ctx context.Context, event Event) error {
	if service.auditor == nil {
		return nil
	}
	return service.auditor.Record(ctx, event)
}

func validateUnit(unit types.DeploymentUnit) error {
	if unit.ID == "" || strings.TrimSpace(unit.Region) == "" {
		return ErrInvalidDeploymentUnit
	}
	switch unit.Status {
	case types.DeploymentUnitStatusActive, types.DeploymentUnitStatusDisabled:
		return nil
	default:
		return ErrInvalidDeploymentUnit
	}
}

func validateAssignment(assignment Assignment) error {
	if assignment.TenantID == "" || assignment.UnitID == "" || assignment.Version == 0 {
		return ErrInvalidAssignment
	}
	return nil
}

func validateMove(move Move) error {
	if move.TenantID == "" || move.SourceUnitID == "" || move.TargetUnitID == "" || move.SourceUnitID == move.TargetUnitID {
		return ErrInvalidMove
	}
	return nil
}

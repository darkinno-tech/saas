package deployment

import (
	"context"
	"sort"
	"sync"

	"github.com/im10furry/saas/core/types"
)

var _ Store = (*MemoryStore)(nil)

// MemoryStore is a thread-safe Store for tests, examples, and single-process
// host integrations.
type MemoryStore struct {
	mu          sync.RWMutex
	units       map[types.DeploymentUnitID]types.DeploymentUnit
	assignments map[types.TenantID]Assignment
	moves       map[types.TenantID]Move
}

// NewMemoryStore creates an empty in-memory deployment store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		units:       make(map[types.DeploymentUnitID]types.DeploymentUnit),
		assignments: make(map[types.TenantID]Assignment),
		moves:       make(map[types.TenantID]Move),
	}
}

// GetUnit returns one deployment unit.
func (store *MemoryStore) GetUnit(ctx context.Context, id types.DeploymentUnitID) (types.DeploymentUnit, error) {
	if err := ctx.Err(); err != nil {
		return types.DeploymentUnit{}, err
	}
	if id == "" {
		return types.DeploymentUnit{}, ErrInvalidDeploymentUnit
	}

	store.mu.RLock()
	defer store.mu.RUnlock()
	unit, ok := store.units[id]
	if !ok {
		return types.DeploymentUnit{}, ErrDeploymentUnitNotFound
	}
	return cloneUnit(unit), nil
}

// ListUnits returns all deployment units in ID order.
func (store *MemoryStore) ListUnits(ctx context.Context) ([]types.DeploymentUnit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	store.mu.RLock()
	defer store.mu.RUnlock()
	units := make([]types.DeploymentUnit, 0, len(store.units))
	for _, unit := range store.units {
		units = append(units, cloneUnit(unit))
	}
	sort.Slice(units, func(i, j int) bool { return units[i].ID < units[j].ID })
	return units, nil
}

// CreateUnit inserts a deployment unit.
func (store *MemoryStore) CreateUnit(ctx context.Context, unit types.DeploymentUnit) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateUnit(unit); err != nil {
		return err
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if _, ok := store.units[unit.ID]; ok {
		return ErrDeploymentUnitAlreadyExists
	}
	store.units[unit.ID] = cloneUnit(unit)
	return nil
}

// UpdateUnit replaces mutable metadata on an existing deployment unit. Status
// transitions must use DisableUnit so a stale metadata write cannot re-enable
// a unit changed by another service instance.
func (store *MemoryStore) UpdateUnit(ctx context.Context, unit types.DeploymentUnit) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateUnit(unit); err != nil {
		return err
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	current, ok := store.units[unit.ID]
	if !ok {
		return ErrDeploymentUnitNotFound
	}
	if current.Status != unit.Status {
		return ErrInvalidDeploymentUnit
	}
	store.units[unit.ID] = cloneUnit(unit)
	return nil
}

// DisableUnit makes an unreferenced deployment unit unavailable. It returns
// whether the status changed and is idempotent for a disabled unit.
func (store *MemoryStore) DisableUnit(ctx context.Context, id types.DeploymentUnitID) (types.DeploymentUnit, bool, error) {
	if err := ctx.Err(); err != nil {
		return types.DeploymentUnit{}, false, err
	}
	if id == "" {
		return types.DeploymentUnit{}, false, ErrInvalidDeploymentUnit
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	unit, ok := store.units[id]
	if !ok {
		return types.DeploymentUnit{}, false, ErrDeploymentUnitNotFound
	}
	if unit.Status == types.DeploymentUnitStatusDisabled {
		return cloneUnit(unit), false, nil
	}
	if store.unitReferencedLocked(id) {
		return types.DeploymentUnit{}, false, ErrDeploymentUnitInUse
	}
	unit.Status = types.DeploymentUnitStatusDisabled
	store.units[id] = unit
	return cloneUnit(unit), true, nil
}

// DeleteUnit removes an existing deployment unit.
func (store *MemoryStore) DeleteUnit(ctx context.Context, id types.DeploymentUnitID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if id == "" {
		return ErrInvalidDeploymentUnit
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if _, ok := store.units[id]; !ok {
		return ErrDeploymentUnitNotFound
	}
	if store.unitReferencedLocked(id) {
		return ErrDeploymentUnitInUse
	}
	delete(store.units, id)
	return nil
}

// GetAssignment returns the current deployment assignment for a tenant.
func (store *MemoryStore) GetAssignment(ctx context.Context, tenantID types.TenantID) (Assignment, error) {
	if err := ctx.Err(); err != nil {
		return Assignment{}, err
	}
	if tenantID == "" {
		return Assignment{}, ErrInvalidAssignment
	}

	store.mu.RLock()
	defer store.mu.RUnlock()
	assignment, ok := store.assignments[tenantID]
	if !ok {
		return Assignment{}, ErrAssignmentNotFound
	}
	return assignment, nil
}

// ListAssignmentsByUnit returns current assignments for a deployment unit in
// tenant ID order.
func (store *MemoryStore) ListAssignmentsByUnit(ctx context.Context, unitID types.DeploymentUnitID) ([]Assignment, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if unitID == "" {
		return nil, ErrInvalidDeploymentUnit
	}

	store.mu.RLock()
	defer store.mu.RUnlock()
	assignments := []Assignment{}
	for _, assignment := range store.assignments {
		if assignment.UnitID == unitID {
			assignments = append(assignments, assignment)
		}
	}
	sort.Slice(assignments, func(i, j int) bool { return assignments[i].TenantID < assignments[j].TenantID })
	return assignments, nil
}

// CreateAssignment creates a tenant's initial deployment assignment.
func (store *MemoryStore) CreateAssignment(ctx context.Context, assignment Assignment) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateAssignment(assignment); err != nil {
		return err
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	unit, ok := store.units[assignment.UnitID]
	if !ok {
		return ErrDeploymentUnitNotFound
	}
	if unit.Status != types.DeploymentUnitStatusActive {
		return ErrDeploymentUnitUnavailable
	}
	if _, ok := store.assignments[assignment.TenantID]; ok {
		return ErrAssignmentAlreadyExists
	}
	store.assignments[assignment.TenantID] = assignment
	return nil
}

// CompareAndSwapAssignment replaces an assignment only if it still equals
// expected. Expected and updated must retain the same tenant ID.
func (store *MemoryStore) CompareAndSwapAssignment(ctx context.Context, expected Assignment, updated Assignment) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateAssignment(expected); err != nil {
		return err
	}
	if err := validateAssignment(updated); err != nil {
		return err
	}
	if expected.TenantID != updated.TenantID {
		return ErrInvalidAssignment
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	current, ok := store.assignments[expected.TenantID]
	if !ok {
		return ErrAssignmentNotFound
	}
	if current != expected {
		return ErrAssignmentConflict
	}
	unit, ok := store.units[updated.UnitID]
	if !ok {
		return ErrDeploymentUnitNotFound
	}
	if unit.Status != types.DeploymentUnitStatusActive {
		return ErrDeploymentUnitUnavailable
	}
	store.assignments[updated.TenantID] = updated
	return nil
}

// CutoverMove atomically replaces expected with updated and removes the exact
// prepared move. It keeps a cancelled or replaced move from changing the
// current assignment after the caller has observed it.
func (store *MemoryStore) CutoverMove(ctx context.Context, expected Assignment, move Move, updated Assignment) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateAssignment(expected); err != nil {
		return err
	}
	if err := validateMove(move); err != nil {
		return err
	}
	if err := validateAssignment(updated); err != nil {
		return err
	}
	if expected.TenantID != updated.TenantID || move.TenantID != expected.TenantID || expected.UnitID != move.SourceUnitID || updated.UnitID != move.TargetUnitID || updated.Version != expected.Version+1 {
		return ErrMoveConflict
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	currentMove, ok := store.moves[move.TenantID]
	if !ok {
		return ErrMoveNotFound
	}
	if currentMove != move {
		return ErrMoveConflict
	}
	currentAssignment, ok := store.assignments[expected.TenantID]
	if !ok {
		return ErrAssignmentNotFound
	}
	if currentAssignment != expected {
		return ErrAssignmentConflict
	}
	unit, ok := store.units[updated.UnitID]
	if !ok {
		return ErrDeploymentUnitNotFound
	}
	if unit.Status != types.DeploymentUnitStatusActive {
		return ErrDeploymentUnitUnavailable
	}
	store.assignments[updated.TenantID] = updated
	delete(store.moves, move.TenantID)
	return nil
}

// GetMove returns a tenant's prepared move.
func (store *MemoryStore) GetMove(ctx context.Context, tenantID types.TenantID) (Move, error) {
	if err := ctx.Err(); err != nil {
		return Move{}, err
	}
	if tenantID == "" {
		return Move{}, ErrInvalidMove
	}

	store.mu.RLock()
	defer store.mu.RUnlock()
	move, ok := store.moves[tenantID]
	if !ok {
		return Move{}, ErrMoveNotFound
	}
	return move, nil
}

// ListMovesByUnit returns prepared moves referencing a deployment unit in
// tenant ID order.
func (store *MemoryStore) ListMovesByUnit(ctx context.Context, unitID types.DeploymentUnitID) ([]Move, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if unitID == "" {
		return nil, ErrInvalidDeploymentUnit
	}

	store.mu.RLock()
	defer store.mu.RUnlock()
	moves := []Move{}
	for _, move := range store.moves {
		if move.SourceUnitID == unitID || move.TargetUnitID == unitID {
			moves = append(moves, move)
		}
	}
	sort.Slice(moves, func(i, j int) bool { return moves[i].TenantID < moves[j].TenantID })
	return moves, nil
}

// CreateMove creates a prepared move for a tenant.
func (store *MemoryStore) CreateMove(ctx context.Context, move Move) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateMove(move); err != nil {
		return err
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	unit, ok := store.units[move.TargetUnitID]
	if !ok {
		return ErrDeploymentUnitNotFound
	}
	if unit.Status != types.DeploymentUnitStatusActive {
		return ErrDeploymentUnitUnavailable
	}
	assignment, ok := store.assignments[move.TenantID]
	if !ok {
		return ErrAssignmentNotFound
	}
	if assignment.UnitID != move.SourceUnitID {
		return ErrMoveConflict
	}
	if _, ok := store.moves[move.TenantID]; ok {
		return ErrMoveAlreadyExists
	}
	store.moves[move.TenantID] = move
	return nil
}

// DeleteMove removes a tenant's prepared move.
func (store *MemoryStore) DeleteMove(ctx context.Context, tenantID types.TenantID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if tenantID == "" {
		return ErrInvalidMove
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if _, ok := store.moves[tenantID]; !ok {
		return ErrMoveNotFound
	}
	delete(store.moves, tenantID)
	return nil
}

// DeleteMoveIfMatch removes a prepared move only when it still equals move.
func (store *MemoryStore) DeleteMoveIfMatch(ctx context.Context, move Move) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateMove(move); err != nil {
		return err
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	current, ok := store.moves[move.TenantID]
	if !ok {
		return ErrMoveNotFound
	}
	if current != move {
		return ErrMoveConflict
	}
	delete(store.moves, move.TenantID)
	return nil
}

func (store *MemoryStore) unitReferencedLocked(unitID types.DeploymentUnitID) bool {
	for _, assignment := range store.assignments {
		if assignment.UnitID == unitID {
			return true
		}
	}
	for _, move := range store.moves {
		if move.SourceUnitID == unitID || move.TargetUnitID == unitID {
			return true
		}
	}
	return false
}

func cloneUnit(unit types.DeploymentUnit) types.DeploymentUnit {
	if unit.ResidencyTags != nil {
		unit.ResidencyTags = append([]string(nil), unit.ResidencyTags...)
	}
	if unit.Metadata != nil {
		metadata := make(map[string]string, len(unit.Metadata))
		for key, value := range unit.Metadata {
			metadata[key] = value
		}
		unit.Metadata = metadata
	}
	return unit
}

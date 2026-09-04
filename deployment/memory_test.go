package deployment

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/darkinno-tech/saas/core/types"
)

func TestMemoryStoreClonesDeploymentUnits(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemoryStore()
	unit := activeUnit("cn-a")
	unit.ResidencyTags = []string{"cn", "pii"}
	unit.Metadata = map[string]string{"owner": "platform"}

	if err := store.CreateUnit(ctx, unit); err != nil {
		t.Fatalf("CreateUnit() error = %v", err)
	}
	unit.ResidencyTags[0] = "changed"
	unit.Metadata["owner"] = "changed"

	got, err := store.GetUnit(ctx, unit.ID)
	if err != nil {
		t.Fatalf("GetUnit() error = %v", err)
	}
	if got.ResidencyTags[0] != "cn" || got.Metadata["owner"] != "platform" {
		t.Fatalf("stored unit leaked caller mutation: %#v", got)
	}
	got.ResidencyTags[1] = "changed-again"
	got.Metadata["owner"] = "changed-again"

	units, err := store.ListUnits(ctx)
	if err != nil {
		t.Fatalf("ListUnits() error = %v", err)
	}
	if len(units) != 1 || units[0].ResidencyTags[1] != "pii" || units[0].Metadata["owner"] != "platform" {
		t.Fatalf("returned unit leaked mutation: %#v", units)
	}
}

func TestMemoryStoreCompareAndSwapAssignment(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemoryStore()
	for _, unit := range []types.DeploymentUnit{activeUnit("cn-a"), activeUnit("eu-a")} {
		if err := store.CreateUnit(ctx, unit); err != nil {
			t.Fatalf("CreateUnit(%q) error = %v", unit.ID, err)
		}
	}
	expected := Assignment{TenantID: "tenant-a", UnitID: "cn-a", Version: 1}
	if err := store.CreateAssignment(ctx, expected); err != nil {
		t.Fatalf("CreateAssignment() error = %v", err)
	}
	next := Assignment{TenantID: "tenant-a", UnitID: "eu-a", Version: 2}
	if err := store.CompareAndSwapAssignment(ctx, expected, next); err != nil {
		t.Fatalf("CompareAndSwapAssignment() error = %v", err)
	}
	if err := store.CompareAndSwapAssignment(ctx, expected, next); !errors.Is(err, ErrAssignmentConflict) {
		t.Fatalf("second CompareAndSwapAssignment() error = %v, want ErrAssignmentConflict", err)
	}
	got, err := store.GetAssignment(ctx, "tenant-a")
	if err != nil {
		t.Fatalf("GetAssignment() error = %v", err)
	}
	if got != next {
		t.Fatalf("GetAssignment() = %#v, want %#v", got, next)
	}
}

func TestMemoryStoreEnforcesPlacementLifecycleInvariants(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemoryStore()
	active := activeUnit("cn-a")
	disabled := disabledUnit("eu-a")
	for _, unit := range []types.DeploymentUnit{active, disabled} {
		if err := store.CreateUnit(ctx, unit); err != nil {
			t.Fatalf("CreateUnit(%q) error = %v", unit.ID, err)
		}
	}

	if err := store.CreateAssignment(ctx, Assignment{TenantID: "tenant-disabled", UnitID: disabled.ID, Version: 1}); !errors.Is(err, ErrDeploymentUnitUnavailable) {
		t.Fatalf("CreateAssignment(disabled) error = %v, want ErrDeploymentUnitUnavailable", err)
	}
	assignment := Assignment{TenantID: "tenant-a", UnitID: active.ID, Version: 1}
	if err := store.CreateAssignment(ctx, assignment); err != nil {
		t.Fatalf("CreateAssignment(active) error = %v", err)
	}
	active.Status = types.DeploymentUnitStatusDisabled
	if err := store.UpdateUnit(ctx, active); !errors.Is(err, ErrInvalidDeploymentUnit) {
		t.Fatalf("UpdateUnit(status transition) error = %v, want ErrInvalidDeploymentUnit", err)
	}
	if _, _, err := store.DisableUnit(ctx, active.ID); !errors.Is(err, ErrDeploymentUnitInUse) {
		t.Fatalf("DisableUnit(referenced) error = %v, want ErrDeploymentUnitInUse", err)
	}
	if err := store.DeleteUnit(ctx, active.ID); !errors.Is(err, ErrDeploymentUnitInUse) {
		t.Fatalf("DeleteUnit(referenced) error = %v, want ErrDeploymentUnitInUse", err)
	}
	if err := store.CreateMove(ctx, Move{TenantID: assignment.TenantID, SourceUnitID: assignment.UnitID, TargetUnitID: disabled.ID}); !errors.Is(err, ErrDeploymentUnitUnavailable) {
		t.Fatalf("CreateMove(disabled target) error = %v, want ErrDeploymentUnitUnavailable", err)
	}
}

func TestMemoryStoreRejectsStaleMetadataUpdateAfterDisable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemoryStore()
	unit := activeUnit("cn-a")
	unit.Metadata = map[string]string{"owner": "platform"}
	if err := store.CreateUnit(ctx, unit); err != nil {
		t.Fatalf("CreateUnit() error = %v", err)
	}
	stale := unit
	stale.Metadata = map[string]string{"owner": "new-owner"}
	if _, changed, err := store.DisableUnit(ctx, unit.ID); err != nil || !changed {
		t.Fatalf("DisableUnit() = _, %v, %v; want _, true, nil", changed, err)
	}
	if err := store.UpdateUnit(ctx, stale); !errors.Is(err, ErrInvalidDeploymentUnit) {
		t.Fatalf("UpdateUnit(stale active status) error = %v, want ErrInvalidDeploymentUnit", err)
	}
	current, err := store.GetUnit(ctx, unit.ID)
	if err != nil {
		t.Fatalf("GetUnit() error = %v", err)
	}
	if current.Status != types.DeploymentUnitStatusDisabled || current.Metadata["owner"] != "platform" {
		t.Fatalf("stale update changed disabled unit: %#v", current)
	}
}

func TestServicePolicyAndUnitLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	policyErr := errors.New("residency policy blocked placement")
	store := NewMemoryStore()
	service := New(store, WithPolicy(PolicyFunc(func(_ context.Context, _ types.Tenant, unit types.DeploymentUnit) error {
		if unit.ID == "us-a" {
			return policyErr
		}
		return nil
	})))

	for _, unit := range []types.DeploymentUnit{activeUnit("cn-a"), activeUnit("us-a"), disabledUnit("eu-disabled"), activeUnit("unused")} {
		if err := service.CreateUnit(ctx, unit); err != nil {
			t.Fatalf("CreateUnit(%q) error = %v", unit.ID, err)
		}
	}
	if err := service.CreateUnit(ctx, types.DeploymentUnit{ID: "no-region", Status: types.DeploymentUnitStatusActive}); !errors.Is(err, ErrInvalidDeploymentUnit) {
		t.Fatalf("CreateUnit() without region error = %v, want ErrInvalidDeploymentUnit", err)
	}

	tenant := testTenant("tenant-a")
	if _, err := service.Assign(ctx, tenant, "us-a"); !errors.Is(err, ErrPolicyDenied) || !errors.Is(err, policyErr) {
		t.Fatalf("Assign(policy denied) error = %v, want wrapped policy denial", err)
	}
	if _, err := service.Assign(ctx, tenant, "eu-disabled"); !errors.Is(err, ErrDeploymentUnitUnavailable) {
		t.Fatalf("Assign(disabled) error = %v, want ErrDeploymentUnitUnavailable", err)
	}
	assignment, err := service.Assign(ctx, tenant, "cn-a")
	if err != nil {
		t.Fatalf("Assign() error = %v", err)
	}
	if want := (Assignment{TenantID: tenant.ID, UnitID: "cn-a", Version: 1}); assignment != want {
		t.Fatalf("Assign() = %#v, want %#v", assignment, want)
	}
	if _, err := service.Assign(ctx, tenant, "cn-a"); !errors.Is(err, ErrAssignmentAlreadyExists) {
		t.Fatalf("second Assign() error = %v, want ErrAssignmentAlreadyExists", err)
	}

	resolved, err := service.Resolve(ctx, tenant)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolved.ID != "cn-a" {
		t.Fatalf("Resolve().ID = %q, want cn-a", resolved.ID)
	}

	unit, err := store.GetUnit(ctx, "cn-a")
	if err != nil {
		t.Fatalf("GetUnit() error = %v", err)
	}
	unit.Status = types.DeploymentUnitStatusDisabled
	if err := service.UpdateUnit(ctx, unit); !errors.Is(err, ErrInvalidDeploymentUnit) {
		t.Fatalf("UpdateUnit(status change) error = %v, want ErrInvalidDeploymentUnit", err)
	}
	if _, err := service.DisableUnit(ctx, "cn-a"); !errors.Is(err, ErrDeploymentUnitInUse) {
		t.Fatalf("DisableUnit(assigned) error = %v, want ErrDeploymentUnitInUse", err)
	}
	if err := service.DeleteUnit(ctx, "cn-a"); !errors.Is(err, ErrDeploymentUnitInUse) {
		t.Fatalf("DeleteUnit(assigned) error = %v, want ErrDeploymentUnitInUse", err)
	}

	disabled, err := service.DisableUnit(ctx, "unused")
	if err != nil {
		t.Fatalf("DisableUnit(unused) error = %v", err)
	}
	if disabled.Status != types.DeploymentUnitStatusDisabled {
		t.Fatalf("DisableUnit().Status = %q, want disabled", disabled.Status)
	}
	if _, err := service.DisableUnit(ctx, "unused"); err != nil {
		t.Fatalf("DisableUnit(idempotent) error = %v", err)
	}
	if err := service.DeleteUnit(ctx, "unused"); err != nil {
		t.Fatalf("DeleteUnit(unused) error = %v", err)
	}
}

func TestServiceMoveLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemoryStore()
	service := New(store)
	for _, unit := range []types.DeploymentUnit{activeUnit("cn-a"), activeUnit("eu-a")} {
		if err := service.CreateUnit(ctx, unit); err != nil {
			t.Fatalf("CreateUnit(%q) error = %v", unit.ID, err)
		}
	}
	tenant := testTenant("tenant-a")
	if _, err := service.Assign(ctx, tenant, "cn-a"); err != nil {
		t.Fatalf("Assign() error = %v", err)
	}

	move, err := service.PrepareMove(ctx, tenant, "eu-a")
	if err != nil {
		t.Fatalf("PrepareMove() error = %v", err)
	}
	wantMove := Move{TenantID: tenant.ID, SourceUnitID: "cn-a", TargetUnitID: "eu-a"}
	if move != wantMove {
		t.Fatalf("PrepareMove() = %#v, want %#v", move, wantMove)
	}
	resolved, err := service.Resolve(ctx, tenant)
	if err != nil {
		t.Fatalf("Resolve() during move error = %v", err)
	}
	if resolved.ID != "cn-a" {
		t.Fatalf("Resolve() during move = %q, want source", resolved.ID)
	}
	if _, err := service.PrepareMove(ctx, tenant, "eu-a"); !errors.Is(err, ErrMoveAlreadyExists) {
		t.Fatalf("second PrepareMove() error = %v, want ErrMoveAlreadyExists", err)
	}
	if _, err := service.DisableUnit(ctx, "eu-a"); !errors.Is(err, ErrDeploymentUnitInUse) {
		t.Fatalf("DisableUnit(move target) error = %v, want ErrDeploymentUnitInUse", err)
	}

	if err := service.CancelMove(ctx, tenant.ID); err != nil {
		t.Fatalf("CancelMove() error = %v", err)
	}
	assignment, err := store.GetAssignment(ctx, tenant.ID)
	if err != nil {
		t.Fatalf("GetAssignment() after CancelMove error = %v", err)
	}
	if assignment.UnitID != "cn-a" || assignment.Version != 1 {
		t.Fatalf("CancelMove changed assignment: %#v", assignment)
	}
	if _, err := store.GetMove(ctx, tenant.ID); !errors.Is(err, ErrMoveNotFound) {
		t.Fatalf("GetMove() after CancelMove error = %v, want ErrMoveNotFound", err)
	}

	if _, err := service.PrepareMove(ctx, tenant, "eu-a"); err != nil {
		t.Fatalf("PrepareMove(after cancel) error = %v", err)
	}
	assignment, err = service.CutoverMove(ctx, tenant)
	if err != nil {
		t.Fatalf("CutoverMove() error = %v", err)
	}
	if want := (Assignment{TenantID: tenant.ID, UnitID: "eu-a", Version: 2}); assignment != want {
		t.Fatalf("CutoverMove() = %#v, want %#v", assignment, want)
	}
	resolved, err = service.Resolve(ctx, tenant)
	if err != nil {
		t.Fatalf("Resolve() after cutover error = %v", err)
	}
	if resolved.ID != "eu-a" {
		t.Fatalf("Resolve() after cutover = %q, want target", resolved.ID)
	}
	if _, err := store.GetMove(ctx, tenant.ID); !errors.Is(err, ErrMoveNotFound) {
		t.Fatalf("GetMove() after cutover error = %v, want ErrMoveNotFound", err)
	}
	if _, err := service.CutoverMove(ctx, tenant); !errors.Is(err, ErrMoveNotFound) {
		t.Fatalf("second CutoverMove() error = %v, want ErrMoveNotFound", err)
	}
	if _, err := service.DisableUnit(ctx, "eu-a"); !errors.Is(err, ErrDeploymentUnitInUse) {
		t.Fatalf("DisableUnit(current target) error = %v, want ErrDeploymentUnitInUse", err)
	}

	if _, err := service.DisableUnit(ctx, "cn-a"); err != nil {
		t.Fatalf("DisableUnit(old source) error = %v", err)
	}
	if err := service.DeleteUnit(ctx, "cn-a"); err != nil {
		t.Fatalf("DeleteUnit(old source) error = %v", err)
	}
}

func TestServiceCutoverFailureDoesNotSplitPlacementState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := &failCutoverStore{Store: NewMemoryStore()}
	service := New(store)
	for _, unit := range []types.DeploymentUnit{activeUnit("cn-a"), activeUnit("eu-a")} {
		if err := service.CreateUnit(ctx, unit); err != nil {
			t.Fatalf("CreateUnit(%q) error = %v", unit.ID, err)
		}
	}
	tenant := testTenant("tenant-a")
	if _, err := service.Assign(ctx, tenant, "cn-a"); err != nil {
		t.Fatalf("Assign() error = %v", err)
	}
	if _, err := service.PrepareMove(ctx, tenant, "eu-a"); err != nil {
		t.Fatalf("PrepareMove() error = %v", err)
	}
	cutover, err := service.CutoverMove(ctx, tenant)
	if !errors.Is(err, errInjectedCutover) {
		t.Fatalf("CutoverMove() error = %v, want injected cutover error", err)
	}
	if cutover != (Assignment{}) {
		t.Fatalf("CutoverMove() result = %#v, want no committed assignment", cutover)
	}
	assignment, err := store.GetAssignment(ctx, tenant.ID)
	if err != nil {
		t.Fatalf("GetAssignment() after failed cutover error = %v", err)
	}
	if assignment.UnitID != "cn-a" || assignment.Version != 1 {
		t.Fatalf("failed cutover changed assignment: %#v", assignment)
	}
	if _, err := store.GetMove(ctx, tenant.ID); err != nil {
		t.Fatalf("GetMove() after failed cutover error = %v, want prepared move retained", err)
	}
}

func TestServiceCutoverAndCancelAcrossInstancesCommitOnlyOne(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemoryStore()
	cutoverService := New(store)
	cancelService := New(store)
	for _, unit := range []types.DeploymentUnit{activeUnit("cn-a"), activeUnit("eu-a")} {
		if err := cutoverService.CreateUnit(ctx, unit); err != nil {
			t.Fatalf("CreateUnit(%q) error = %v", unit.ID, err)
		}
	}
	tenant := testTenant("tenant-a")
	if _, err := cutoverService.Assign(ctx, tenant, "cn-a"); err != nil {
		t.Fatalf("Assign() error = %v", err)
	}
	if _, err := cutoverService.PrepareMove(ctx, tenant, "eu-a"); err != nil {
		t.Fatalf("PrepareMove() error = %v", err)
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	go func() {
		<-start
		_, err := cutoverService.CutoverMove(ctx, tenant)
		results <- err
	}()
	go func() {
		<-start
		results <- cancelService.CancelMove(ctx, tenant.ID)
	}()
	close(start)

	successes := 0
	for range 2 {
		if err := <-results; err == nil {
			successes++
			continue
		} else if !errors.Is(err, ErrMoveNotFound) && !errors.Is(err, ErrMoveConflict) {
			t.Fatalf("concurrent cutover/cancel error = %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent cutover/cancel successes = %d, want 1", successes)
	}
	assignment, err := store.GetAssignment(ctx, tenant.ID)
	if err != nil {
		t.Fatalf("GetAssignment() error = %v", err)
	}
	if assignment.UnitID != "cn-a" && assignment.UnitID != "eu-a" {
		t.Fatalf("final assignment = %#v, want source or target", assignment)
	}
	if _, err := store.GetMove(ctx, tenant.ID); !errors.Is(err, ErrMoveNotFound) {
		t.Fatalf("GetMove() after concurrent cutover/cancel error = %v, want ErrMoveNotFound", err)
	}
}

func TestServiceConcurrentMovePreparationAndCutover(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemoryStore()
	service := New(store)
	for _, unit := range []types.DeploymentUnit{activeUnit("cn-a"), activeUnit("eu-a")} {
		if err := service.CreateUnit(ctx, unit); err != nil {
			t.Fatalf("CreateUnit(%q) error = %v", unit.ID, err)
		}
	}
	tenant := testTenant("tenant-a")
	if _, err := service.Assign(ctx, tenant, "cn-a"); err != nil {
		t.Fatalf("Assign() error = %v", err)
	}

	const workers = 24
	start := make(chan struct{})
	results := make(chan error, workers)
	var prepareWG sync.WaitGroup
	for range workers {
		prepareWG.Add(1)
		go func() {
			defer prepareWG.Done()
			<-start
			_, err := service.PrepareMove(ctx, tenant, "eu-a")
			results <- err
		}()
	}
	close(start)
	prepareWG.Wait()
	close(results)
	prepared := 0
	for err := range results {
		if err == nil {
			prepared++
			continue
		}
		if !errors.Is(err, ErrMoveAlreadyExists) {
			t.Fatalf("PrepareMove() concurrent error = %v, want ErrMoveAlreadyExists", err)
		}
	}
	if prepared != 1 {
		t.Fatalf("successful concurrent PrepareMove calls = %d, want 1", prepared)
	}

	start = make(chan struct{})
	results = make(chan error, workers)
	var cutoverWG sync.WaitGroup
	for range workers {
		cutoverWG.Add(1)
		go func() {
			defer cutoverWG.Done()
			<-start
			_, err := service.CutoverMove(ctx, tenant)
			results <- err
		}()
	}
	close(start)
	cutoverWG.Wait()
	close(results)
	cutovers := 0
	for err := range results {
		if err == nil {
			cutovers++
			continue
		}
		if !errors.Is(err, ErrMoveNotFound) {
			t.Fatalf("CutoverMove() concurrent error = %v, want ErrMoveNotFound", err)
		}
	}
	if cutovers != 1 {
		t.Fatalf("successful concurrent CutoverMove calls = %d, want 1", cutovers)
	}
	assignment, err := store.GetAssignment(ctx, tenant.ID)
	if err != nil {
		t.Fatalf("GetAssignment() error = %v", err)
	}
	if assignment.UnitID != "eu-a" || assignment.Version != 2 {
		t.Fatalf("concurrent cutover assignment = %#v, want target version 2", assignment)
	}
}

func TestServiceAuditorObservesCommittedChanges(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var (
		mu     sync.Mutex
		events []Event
	)
	service := New(NewMemoryStore(), WithAuditor(AuditorFunc(func(_ context.Context, event Event) error {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, event)
		return nil
	})))
	if err := service.CreateUnit(ctx, activeUnit("cn-a")); err != nil {
		t.Fatalf("CreateUnit() error = %v", err)
	}
	tenant := testTenant("tenant-a")
	if _, err := service.Assign(ctx, tenant, "cn-a"); err != nil {
		t.Fatalf("Assign() error = %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 2 {
		t.Fatalf("audited events = %#v, want create and assignment", events)
	}
	if events[0].Action != "deployment_unit.create" || events[1].Action != "assignment.create" || events[1].TenantID != tenant.ID {
		t.Fatalf("unexpected events: %#v", events)
	}
}

func activeUnit(id types.DeploymentUnitID) types.DeploymentUnit {
	return types.DeploymentUnit{
		ID:     id,
		Status: types.DeploymentUnitStatusActive,
		Region: "region-" + id.String(),
	}
}

func disabledUnit(id types.DeploymentUnitID) types.DeploymentUnit {
	unit := activeUnit(id)
	unit.Status = types.DeploymentUnitStatusDisabled
	return unit
}

func testTenant(id types.TenantID) types.Tenant {
	return types.Tenant{ID: id, Status: types.TenantStatusPending}
}

var errInjectedCutover = errors.New("injected cutover failure")

type failCutoverStore struct {
	Store
}

func (*failCutoverStore) CutoverMove(context.Context, Assignment, Move, Assignment) error {
	return errInjectedCutover
}

func TestMemoryStoreListAssignmentsAndMovesByUnit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemoryStore()
	for _, id := range []types.DeploymentUnitID{"cn-a", "eu-a"} {
		if err := store.CreateUnit(ctx, activeUnit(id)); err != nil {
			t.Fatalf("CreateUnit(%q) error = %v", id, err)
		}
	}
	for _, assignment := range []Assignment{
		{TenantID: "tenant-b", UnitID: "cn-a", Version: 1},
		{TenantID: "tenant-a", UnitID: "cn-a", Version: 1},
	} {
		if err := store.CreateAssignment(ctx, assignment); err != nil {
			t.Fatalf("CreateAssignment() error = %v", err)
		}
	}

	if _, err := store.ListAssignmentsByUnit(ctx, ""); !errors.Is(err, ErrInvalidDeploymentUnit) {
		t.Fatalf("ListAssignmentsByUnit(\"\") error = %v, want ErrInvalidDeploymentUnit", err)
	}
	assignments, err := store.ListAssignmentsByUnit(ctx, "cn-a")
	if err != nil {
		t.Fatalf("ListAssignmentsByUnit() error = %v", err)
	}
	if len(assignments) != 2 || assignments[0].TenantID != "tenant-a" || assignments[1].TenantID != "tenant-b" {
		t.Fatalf("ListAssignmentsByUnit() = %#v, want sorted tenant-a, tenant-b", assignments)
	}
	if got, err := store.ListAssignmentsByUnit(ctx, "eu-a"); err != nil || len(got) != 0 {
		t.Fatalf("ListAssignmentsByUnit(empty unit) = %#v, %v; want [], nil", got, err)
	}

	move := Move{TenantID: "tenant-a", SourceUnitID: "cn-a", TargetUnitID: "eu-a"}
	if err := store.CreateMove(ctx, move); err != nil {
		t.Fatalf("CreateMove() error = %v", err)
	}
	if _, err := store.ListMovesByUnit(ctx, ""); !errors.Is(err, ErrInvalidDeploymentUnit) {
		t.Fatalf("ListMovesByUnit(\"\") error = %v, want ErrInvalidDeploymentUnit", err)
	}
	moves, err := store.ListMovesByUnit(ctx, "cn-a")
	if err != nil {
		t.Fatalf("ListMovesByUnit() error = %v", err)
	}
	if len(moves) != 1 || moves[0] != move {
		t.Fatalf("ListMovesByUnit() = %#v, want [%#v]", moves, move)
	}
	got, err := store.GetMove(ctx, "tenant-a")
	if err != nil {
		t.Fatalf("GetMove() error = %v", err)
	}
	if got != move {
		t.Fatalf("GetMove() = %#v, want %#v", got, move)
	}
	if _, err := store.GetMove(ctx, ""); !errors.Is(err, ErrInvalidMove) {
		t.Fatalf("GetMove(\"\") error = %v, want ErrInvalidMove", err)
	}
	if _, err := store.GetMove(ctx, "absent"); !errors.Is(err, ErrMoveNotFound) {
		t.Fatalf("GetMove(absent) error = %v, want ErrMoveNotFound", err)
	}

	if err := store.DeleteMove(ctx, ""); !errors.Is(err, ErrInvalidMove) {
		t.Fatalf("DeleteMove(\"\") error = %v, want ErrInvalidMove", err)
	}
	if err := store.DeleteMove(ctx, "tenant-a"); err != nil {
		t.Fatalf("DeleteMove() error = %v", err)
	}
	if err := store.DeleteMove(ctx, "tenant-a"); !errors.Is(err, ErrMoveNotFound) {
		t.Fatalf("second DeleteMove() error = %v, want ErrMoveNotFound", err)
	}
	if _, err := store.GetMove(ctx, "tenant-a"); !errors.Is(err, ErrMoveNotFound) {
		t.Fatalf("GetMove() after DeleteMove error = %v, want ErrMoveNotFound", err)
	}
}

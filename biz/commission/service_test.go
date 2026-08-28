package commission

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/im10furry/saas/core/types"
)

func TestServiceRecordEventIsIdempotentAndHonorsFreezePeriod(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	service := newTestService(store, func() time.Time { return now })
	setupActiveProgram(t, ctx, service, "tenant-a", time.Hour)

	event := CommissionEvent{
		TenantID:       "tenant-a",
		SourceType:     "payment.succeeded",
		SourceID:       "payment-1",
		OccurredAt:     now,
		Commissionable: Amount{Currency: "USD", Minor: 10_000},
		Attributes:     map[string]string{"order_id": "order-1"},
	}
	first, err := service.RecordEvent(ctx, hostActor(), "program-a", event)
	if err != nil {
		t.Fatalf("RecordEvent() error = %v", err)
	}
	if len(first) != 1 || first[0].Amount.Minor != 500 || first[0].Status != EarningStatusPending {
		t.Fatalf("RecordEvent() = %+v, want one pending USD 500 earning", first)
	}
	event.Attributes["order_id"] = "changed"
	// A changed payload must not silently reuse the original source key.
	if _, err := service.RecordEvent(ctx, hostActor(), "program-a", event); !errors.Is(err, ErrEventConflict) {
		t.Fatalf("RecordEvent(changed duplicate) error = %v, want ErrEventConflict", err)
	}
	event.Attributes["order_id"] = "order-1"
	duplicate, err := service.RecordEvent(ctx, hostActor(), "program-a", event)
	if err != nil {
		t.Fatalf("RecordEvent(duplicate) error = %v", err)
	}
	if len(duplicate) != 1 || duplicate[0].ID != first[0].ID {
		t.Fatalf("RecordEvent(duplicate) = %+v, want original earning", duplicate)
	}

	if released, err := service.MakeAvailableDue(ctx, hostActor()); err != nil || len(released) != 0 {
		t.Fatalf("MakeAvailableDue(before freeze) = %+v, %v; want empty nil", released, err)
	}
	now = now.Add(time.Hour)
	released, err := service.MakeAvailableDue(ctx, hostActor())
	if err != nil {
		t.Fatalf("MakeAvailableDue() error = %v", err)
	}
	if len(released) != 1 || released[0].Status != EarningStatusAvailable || released[0].Version != 2 {
		t.Fatalf("MakeAvailableDue() = %+v, want available version 2", released)
	}

	got, actions, err := service.GetEarning(ctx, hostActor(), "tenant-a", first[0].ID)
	if err != nil {
		t.Fatalf("GetEarning() error = %v", err)
	}
	if got.Status != EarningStatusAvailable || !containsEarningAction(actions, EarningActionHold) || containsEarningAction(actions, EarningActionStartSettlement) {
		t.Fatalf("GetEarning() = %+v, actions=%v; want only individually executable available actions", got, actions)
	}
	journal, err := store.ListJournalEntries(ctx, "tenant-a", first[0].ID)
	if err != nil {
		t.Fatalf("ListJournalEntries() error = %v", err)
	}
	if len(journal) != 2 || journal[0].Kind != JournalKindAccrual || journal[1].Kind != JournalKindAvailable {
		t.Fatalf("ListJournalEntries() = %+v, want accrual then available", journal)
	}
}

func TestPendingEarningCannotBypassFreezeWithManualTransition(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	service := newTestService(NewMemoryStore(), func() time.Time { return now })
	setupActiveProgram(t, ctx, service, "tenant-a", time.Hour)
	earnings, err := service.RecordEvent(ctx, hostActor(), "program-a", CommissionEvent{
		TenantID:       "tenant-a",
		SourceType:     "payment.succeeded",
		SourceID:       "payment-1",
		OccurredAt:     now,
		Commissionable: Amount{Currency: "USD", Minor: 10_000},
	})
	if err != nil {
		t.Fatalf("RecordEvent() error = %v", err)
	}
	if _, err := service.TransitionEarning(ctx, hostActor(), "tenant-a", earnings[0].ID, earnings[0].Version, EarningActionMakeAvailable); !errors.Is(err, ErrInvalidEarningTransition) {
		t.Fatalf("TransitionEarning(make_available) error = %v, want ErrInvalidEarningTransition", err)
	}
}

func TestZeroCommissionEventIsPersistedIdempotently(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	service := newTestService(store, func() time.Time { return now })
	setupActiveProgram(t, ctx, service, "tenant-a", 0)
	event := CommissionEvent{
		TenantID:       "tenant-a",
		SourceType:     "payment.succeeded",
		SourceID:       "payment-zero",
		OccurredAt:     now,
		Commissionable: Amount{Currency: "USD", Minor: 1}, // 5% rounds down to zero.
		Attributes:     map[string]string{"order_id": "order-zero"},
	}
	first, err := service.RecordEvent(ctx, hostActor(), "program-a", event)
	if err != nil || len(first) != 0 {
		t.Fatalf("RecordEvent(zero commission) = %+v, %v; want empty nil", first, err)
	}
	second, err := service.RecordEvent(ctx, hostActor(), "program-a", event)
	if err != nil || len(second) != 0 {
		t.Fatalf("RecordEvent(zero duplicate) = %+v, %v; want original empty result", second, err)
	}
	event.Attributes["order_id"] = "changed"
	if _, err := service.RecordEvent(ctx, hostActor(), "program-a", event); !errors.Is(err, ErrEventConflict) {
		t.Fatalf("RecordEvent(changed zero duplicate) error = %v, want ErrEventConflict", err)
	}
	earnings, err := store.ListEarnings(ctx, "tenant-a", EarningFilter{})
	if err != nil || len(earnings) != 0 {
		t.Fatalf("ListEarnings() = %+v, %v; want no zero-value earnings", earnings, err)
	}
}

func TestServiceProgramCannotExceedPlatformTemplate(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	service := newTestService(store, func() time.Time { return now })
	template := testTemplate(time.Hour)
	if err := service.CreateTemplate(ctx, hostActor(), template); err != nil {
		t.Fatalf("CreateTemplate() error = %v", err)
	}

	program := testProgram("tenant-a")
	program.Rules[0].Tiers[0].BasisPoints = 1_001
	if err := service.CreateProgram(ctx, hostActor(), program); !errors.Is(err, ErrProgramExceedsTemplate) {
		t.Fatalf("CreateProgram() error = %v, want ErrProgramExceedsTemplate", err)
	}
}

func TestServicePlatformActionsRequireHostActor(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	service := newTestService(NewMemoryStore(), func() time.Time { return now })
	if err := service.CreateTemplate(ctx, Actor{ID: "user-a", TenantID: "tenant-a"}, testTemplate(0)); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("CreateTemplate(non-host) error = %v, want ErrUnauthorized", err)
	}
}

func TestServiceCommandsFailClosedWithoutAuthorizer(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	service := NewService(NewMemoryStore(), WithClock(func() time.Time { return now }))
	if err := service.CreateTemplate(ctx, Actor{ID: "forged-host", Host: true}, testTemplate(0)); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("CreateTemplate(without authorizer) error = %v, want ErrUnauthorized", err)
	}
	if err := service.CreateProgram(ctx, Actor{ID: "tenant-admin", TenantID: "tenant-a"}, testProgram("tenant-a")); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("CreateProgram(without authorizer) error = %v, want ErrUnauthorized", err)
	}
	if _, _, err := service.GetEarning(ctx, Actor{ID: "tenant-user", TenantID: "tenant-a"}, "tenant-a", "earning-a"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("GetEarning(without authorizer) error = %v, want ErrUnauthorized", err)
	}
}

func TestServiceTemplateMustStartDraft(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	service := newTestService(NewMemoryStore(), func() time.Time { return now })
	template := testTemplate(0)
	template.Status = TemplateStatusActive
	if err := service.CreateTemplate(ctx, hostActor(), template); !errors.Is(err, ErrInvalidTemplate) {
		t.Fatalf("CreateTemplate(active) error = %v, want ErrInvalidTemplate", err)
	}
	template.Status = ""
	if err := service.CreateTemplate(ctx, hostActor(), template); err != nil {
		t.Fatalf("CreateTemplate(default draft) error = %v", err)
	}
}

func TestServiceProgramMustStartDraftAndHostApproves(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	service := newTestService(NewMemoryStore(), func() time.Time { return now })
	template := testTemplate(0)
	if err := service.CreateTemplate(ctx, hostActor(), template); err != nil {
		t.Fatalf("CreateTemplate() error = %v", err)
	}
	if _, err := service.TransitionTemplate(ctx, hostActor(), template.ID, template.Version, TemplateActionActivate); err != nil {
		t.Fatalf("TransitionTemplate(activate) error = %v", err)
	}
	program := testProgram("tenant-a")
	program.Status = ProgramStatusActive
	if err := service.CreateProgram(ctx, hostActor(), program); !errors.Is(err, ErrInvalidProgram) {
		t.Fatalf("CreateProgram(active) error = %v, want ErrInvalidProgram", err)
	}

	tenantActor := Actor{ID: "tenant-admin", TenantID: "tenant-a"}
	program = testProgram("tenant-a")
	if err := service.CreateProgram(ctx, tenantActor, program); err != nil {
		t.Fatalf("CreateProgram(draft) error = %v", err)
	}
	pending, err := service.TransitionProgram(ctx, tenantActor, program.TenantID, program.ID, program.Version+1, ProgramActionSubmit)
	if err != nil {
		t.Fatalf("TransitionProgram(submit) error = %v", err)
	}
	if _, err := service.TransitionProgram(ctx, tenantActor, program.TenantID, program.ID, pending.Version, ProgramActionApprove); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("TransitionProgram(tenant approve) error = %v, want ErrUnauthorized", err)
	}
	active, err := service.TransitionProgram(ctx, hostActor(), program.TenantID, program.ID, pending.Version, ProgramActionApprove)
	if err != nil || active.Status != ProgramStatusActive {
		t.Fatalf("TransitionProgram(host approve) = %+v, %v; want active nil", active, err)
	}
}

func TestProgramCannotResumeWhenTemplateRetired(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	service := newTestService(store, func() time.Time { return now })
	setupActiveProgram(t, ctx, service, "tenant-a", 0)
	program, err := store.GetProgram(ctx, "tenant-a", "program-a")
	if err != nil {
		t.Fatalf("GetProgram() error = %v", err)
	}
	suspended, err := service.TransitionProgram(ctx, hostActor(), program.TenantID, program.ID, program.Version, ProgramActionSuspend)
	if err != nil {
		t.Fatalf("TransitionProgram(suspend) error = %v", err)
	}
	if _, err := service.TransitionTemplate(ctx, hostActor(), program.TemplateID, program.TemplateVersion, TemplateActionRetire); err != nil {
		t.Fatalf("TransitionTemplate(retire) error = %v", err)
	}
	if _, err := service.TransitionProgram(ctx, hostActor(), program.TenantID, program.ID, suspended.Version, ProgramActionResume); !errors.Is(err, ErrTemplateNotActive) {
		t.Fatalf("TransitionProgram(resume retired template) error = %v, want ErrTemplateNotActive", err)
	}
	stored, err := store.GetProgram(ctx, program.TenantID, program.ID)
	if err != nil || stored.Status != ProgramStatusSuspended || stored.Version != suspended.Version {
		t.Fatalf("GetProgram(after failed resume) = %+v, %v; want unchanged suspended program", stored, err)
	}
}

func TestProgramTierCapComparisonIgnoresInputOrder(t *testing.T) {
	template := testTemplate(0)
	template.Rules[0].Tiers = []Tier{
		{MinMinor: 0, MaxMinor: 1_000, BasisPoints: 500},
		{MinMinor: 1_000, BasisPoints: 1_000},
	}
	program := testProgram("tenant-a")
	program.Rules[0].Tiers = []Tier{
		{MinMinor: 1_000, BasisPoints: 1_000},
		{MinMinor: 0, MaxMinor: 1_000, BasisPoints: 500},
	}
	if err := validateProgramAgainstTemplate(program, template); err != nil {
		t.Fatalf("validateProgramAgainstTemplate(reordered tiers) error = %v", err)
	}
}

func TestSettlementBatchUsesOptimisticVersionsAndPreservesEarnings(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	service := newTestService(store, func() time.Time { return now })
	setupActiveProgram(t, ctx, service, "tenant-a", 0)

	earnings, err := service.RecordEvent(ctx, hostActor(), "program-a", CommissionEvent{
		TenantID:       "tenant-a",
		SourceType:     "payment.succeeded",
		SourceID:       "payment-1",
		OccurredAt:     now,
		Commissionable: Amount{Currency: "USD", Minor: 10_000},
	})
	if err != nil {
		t.Fatalf("RecordEvent() error = %v", err)
	}
	if len(earnings) != 1 || earnings[0].Status != EarningStatusAvailable {
		t.Fatalf("RecordEvent() = %+v, want one available earning", earnings)
	}

	batch, err := service.StartSettlement(ctx, hostActor(), "tenant-a", "settlement-1", []string{earnings[0].ID})
	if err != nil {
		t.Fatalf("StartSettlement() error = %v", err)
	}
	if batch.Status != SettlementStatusSubmitted || batch.Amount.Minor != 500 {
		t.Fatalf("StartSettlement() = %+v, want submitted USD 500", batch)
	}
	if _, err := service.TransitionEarning(ctx, hostActor(), "tenant-a", earnings[0].ID, earnings[0].Version+1, EarningActionReverse); !errors.Is(err, ErrInvalidEarningTransition) {
		t.Fatalf("TransitionEarning(reverse settling) error = %v, want ErrInvalidEarningTransition", err)
	}
	if _, err := service.CompleteSettlement(ctx, hostActor(), "tenant-a", batch.ID, batch.Version+1, false, ""); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("CompleteSettlement(stale) error = %v, want ErrVersionConflict", err)
	}
	rejected, err := service.CompleteSettlement(ctx, hostActor(), "tenant-a", batch.ID, batch.Version, false, "provider-declined")
	if err != nil {
		t.Fatalf("CompleteSettlement(rejected) error = %v", err)
	}
	if rejected.Status != SettlementStatusRejected || rejected.Version != 2 {
		t.Fatalf("CompleteSettlement(rejected) = %+v, want rejected version 2", rejected)
	}
	available, _, err := service.GetEarning(ctx, hostActor(), "tenant-a", earnings[0].ID)
	if err != nil {
		t.Fatalf("GetEarning(released) error = %v", err)
	}
	if available.Status != EarningStatusAvailable {
		t.Fatalf("earning after rejection = %+v, want available", available)
	}

	second, err := service.StartSettlement(ctx, hostActor(), "tenant-a", "settlement-2", []string{earnings[0].ID})
	if err != nil {
		t.Fatalf("StartSettlement(second) error = %v", err)
	}
	settled, err := service.CompleteSettlement(ctx, hostActor(), "tenant-a", second.ID, second.Version, true, "provider-ref-1")
	if err != nil {
		t.Fatalf("CompleteSettlement(settled) error = %v", err)
	}
	if settled.Status != SettlementStatusSettled || settled.ProviderReference != "provider-ref-1" {
		t.Fatalf("CompleteSettlement(settled) = %+v, want settled provider reference", settled)
	}
	final, _, err := service.GetEarning(ctx, hostActor(), "tenant-a", earnings[0].ID)
	if err != nil {
		t.Fatalf("GetEarning(final) error = %v", err)
	}
	if final.Status != EarningStatusSettled {
		t.Fatalf("final earning = %+v, want settled", final)
	}
	duplicate, err := service.CompleteSettlement(ctx, hostActor(), "tenant-a", second.ID, second.Version, true, "provider-ref-1")
	if err != nil || duplicate.Status != SettlementStatusSettled {
		t.Fatalf("CompleteSettlement(duplicate) = %+v, %v; want original settled batch", duplicate, err)
	}
	if _, err := service.CompleteSettlement(ctx, hostActor(), "tenant-a", second.ID, second.Version, false, "provider-ref-1"); !errors.Is(err, ErrSettlementOutcomeConflict) {
		t.Fatalf("CompleteSettlement(conflicting duplicate) error = %v, want ErrSettlementOutcomeConflict", err)
	}
}

func TestSettlementCompletionRequiresTrustedHost(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	service := newTestService(NewMemoryStore(), func() time.Time { return now })
	setupActiveProgram(t, ctx, service, "tenant-a", 0)
	earnings, err := service.RecordEvent(ctx, hostActor(), "program-a", CommissionEvent{
		TenantID:       "tenant-a",
		SourceType:     "payment.succeeded",
		SourceID:       "payment-1",
		OccurredAt:     now,
		Commissionable: Amount{Currency: "USD", Minor: 10_000},
	})
	if err != nil {
		t.Fatalf("RecordEvent() error = %v", err)
	}
	tenantActor := Actor{ID: "tenant-finance", TenantID: "tenant-a"}
	batch, err := service.StartSettlement(ctx, tenantActor, "tenant-a", "settlement-1", []string{earnings[0].ID})
	if err != nil {
		t.Fatalf("StartSettlement() error = %v", err)
	}
	if _, err := service.CompleteSettlement(ctx, tenantActor, "tenant-a", batch.ID, batch.Version, true, "provider-ref"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("CompleteSettlement(non-host) error = %v, want ErrUnauthorized", err)
	}
	completed, err := service.CompleteSettlement(ctx, hostActor(), "tenant-a", batch.ID, batch.Version, true, "provider-ref")
	if err != nil || completed.Status != SettlementStatusSettled {
		t.Fatalf("CompleteSettlement(host) = %+v, %v; want settled nil", completed, err)
	}
}

func TestSettleLeavesAmbiguousProviderFailureSubmitted(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	service := newTestService(store, func() time.Time { return now }, WithSettlementAdapter(testSettlementAdapter{err: errors.New("provider timeout")}))
	setupActiveProgram(t, ctx, service, "tenant-a", 0)
	earnings, err := service.RecordEvent(ctx, hostActor(), "program-a", CommissionEvent{
		TenantID:       "tenant-a",
		SourceType:     "payment.succeeded",
		SourceID:       "payment-1",
		OccurredAt:     now,
		Commissionable: Amount{Currency: "USD", Minor: 10_000},
	})
	if err != nil {
		t.Fatalf("RecordEvent() error = %v", err)
	}
	batch, err := service.Settle(ctx, hostActor(), "tenant-a", "settlement-1", []string{earnings[0].ID})
	if err == nil || batch.Status != SettlementStatusSubmitted {
		t.Fatalf("Settle(ambiguous error) = %+v, %v; want submitted batch and error", batch, err)
	}
	earning, _, err := service.GetEarning(ctx, hostActor(), "tenant-a", earnings[0].ID)
	if err != nil || earning.Status != EarningStatusSettling {
		t.Fatalf("GetEarning() = %+v, %v; want settling", earning, err)
	}
}

func TestSettleRequiresExplicitProviderOutcome(t *testing.T) {
	for _, test := range []struct {
		name        string
		receipt     SettlementReceipt
		wantStatus  SettlementStatus
		wantEarning EarningStatus
		wantErr     error
	}{
		{
			name:        "pending remains submitted",
			receipt:     SettlementReceipt{Status: SettlementSubmissionPending, ProviderReference: "provider-pending-1"},
			wantStatus:  SettlementStatusSubmitted,
			wantEarning: EarningStatusSettling,
		},
		{
			name:        "settled requires provider reference",
			receipt:     SettlementReceipt{Status: SettlementSubmissionSettled},
			wantStatus:  SettlementStatusSubmitted,
			wantEarning: EarningStatusSettling,
			wantErr:     ErrInvalidSettlementReceipt,
		},
		{
			name:        "settled completes batch",
			receipt:     SettlementReceipt{Status: SettlementSubmissionSettled, ProviderReference: "provider-settled-1"},
			wantStatus:  SettlementStatusSettled,
			wantEarning: EarningStatusSettled,
		},
		{
			name:        "rejected releases earning",
			receipt:     SettlementReceipt{Status: SettlementSubmissionRejected, ProviderReference: "provider-rejected-1"},
			wantStatus:  SettlementStatusRejected,
			wantEarning: EarningStatusAvailable,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
			store := NewMemoryStore()
			service := newTestService(store, func() time.Time { return now }, WithSettlementAdapter(testSettlementAdapter{receipt: test.receipt}))
			setupActiveProgram(t, ctx, service, "tenant-a", 0)
			earnings, err := service.RecordEvent(ctx, hostActor(), "program-a", CommissionEvent{
				TenantID:       "tenant-a",
				SourceType:     "payment.succeeded",
				SourceID:       "payment-1",
				OccurredAt:     now,
				Commissionable: Amount{Currency: "USD", Minor: 10_000},
			})
			if err != nil {
				t.Fatalf("RecordEvent() error = %v", err)
			}

			batch, err := service.Settle(ctx, hostActor(), "tenant-a", "settlement-1", []string{earnings[0].ID})
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Settle() error = %v, want %v", err, test.wantErr)
			}
			if batch.Status != test.wantStatus {
				t.Fatalf("Settle() batch status = %q, want %q", batch.Status, test.wantStatus)
			}
			earning, _, getErr := service.GetEarning(ctx, hostActor(), "tenant-a", earnings[0].ID)
			if getErr != nil || earning.Status != test.wantEarning {
				t.Fatalf("GetEarning() = %+v, %v; want %q", earning, getErr, test.wantEarning)
			}
		})
	}
}

func TestCompleteSettlementRequiresSuccessReference(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	service := newTestService(NewMemoryStore(), func() time.Time { return now })
	setupActiveProgram(t, ctx, service, "tenant-a", 0)
	earnings, err := service.RecordEvent(ctx, hostActor(), "program-a", CommissionEvent{
		TenantID:       "tenant-a",
		SourceType:     "payment.succeeded",
		SourceID:       "payment-1",
		OccurredAt:     now,
		Commissionable: Amount{Currency: "USD", Minor: 10_000},
	})
	if err != nil {
		t.Fatalf("RecordEvent() error = %v", err)
	}
	batch, err := service.StartSettlement(ctx, hostActor(), "tenant-a", "settlement-1", []string{earnings[0].ID})
	if err != nil {
		t.Fatalf("StartSettlement() error = %v", err)
	}
	if _, err := service.CompleteSettlement(ctx, hostActor(), "tenant-a", batch.ID, batch.Version, true, ""); !errors.Is(err, ErrInvalidSettlementReceipt) {
		t.Fatalf("CompleteSettlement(empty success reference) error = %v, want ErrInvalidSettlementReceipt", err)
	}
}

func TestServiceLimitsBoundCommandsAndPages(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	service := newTestService(store, func() time.Time { return now }, WithLimits(ServiceLimits{
		MaxEventAttributes: 1,
		MaxSettlementItems: 1,
		DefaultPageSize:    1,
		MaxPageSize:        2,
	}))
	setupActiveProgram(t, ctx, service, "tenant-a", 0)
	if _, err := service.RecordEvent(ctx, hostActor(), "program-a", CommissionEvent{
		TenantID:       "tenant-a",
		SourceType:     "payment.succeeded",
		SourceID:       "too-many-attributes",
		OccurredAt:     now,
		Commissionable: Amount{Currency: "USD", Minor: 10_000},
		Attributes:     map[string]string{"one": "1", "two": "2"},
	}); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("RecordEvent(too many attributes) error = %v, want ErrLimitExceeded", err)
	}

	for _, sourceID := range []string{"payment-1", "payment-2"} {
		if _, err := service.RecordEvent(ctx, hostActor(), "program-a", CommissionEvent{
			TenantID:       "tenant-a",
			SourceType:     "payment.succeeded",
			SourceID:       sourceID,
			OccurredAt:     now,
			Commissionable: Amount{Currency: "USD", Minor: 10_000},
		}); err != nil {
			t.Fatalf("RecordEvent(%s) error = %v", sourceID, err)
		}
	}
	page, err := service.ListEarnings(ctx, hostActor(), "tenant-a", EarningFilter{})
	if err != nil || len(page) != 1 {
		t.Fatalf("ListEarnings(default page) = %+v, %v; want one result", page, err)
	}
	if _, err := service.ListEarnings(ctx, hostActor(), "tenant-a", EarningFilter{Limit: 3}); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("ListEarnings(too large page) error = %v, want ErrLimitExceeded", err)
	}
	if _, err := service.StartSettlement(ctx, hostActor(), "tenant-a", "settlement-limit", []string{"one", "two"}); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("StartSettlement(too many items) error = %v, want ErrLimitExceeded", err)
	}
}

func TestMakeAvailableDueUsesBoundedBatch(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	service := newTestService(NewMemoryStore(), func() time.Time { return now }, WithLimits(ServiceLimits{MaxDueBatch: 1}))
	setupActiveProgram(t, ctx, service, "tenant-a", time.Hour)
	for _, sourceID := range []string{"payment-1", "payment-2"} {
		if _, err := service.RecordEvent(ctx, hostActor(), "program-a", CommissionEvent{
			TenantID:       "tenant-a",
			SourceType:     "payment.succeeded",
			SourceID:       sourceID,
			OccurredAt:     now,
			Commissionable: Amount{Currency: "USD", Minor: 10_000},
		}); err != nil {
			t.Fatalf("RecordEvent(%s) error = %v", sourceID, err)
		}
	}
	now = now.Add(time.Hour)
	first, err := service.MakeAvailableDue(ctx, hostActor())
	if err != nil || len(first) != 1 || first[0].Status != EarningStatusAvailable {
		t.Fatalf("MakeAvailableDue(first batch) = %+v, %v; want one available earning", first, err)
	}
	second, err := service.MakeAvailableDue(ctx, hostActor())
	if err != nil || len(second) != 1 || second[0].Status != EarningStatusAvailable || second[0].ID == first[0].ID {
		t.Fatalf("MakeAvailableDue(second batch) = %+v, %v; want the other available earning", second, err)
	}
}

func TestMemoryStoreTransitionCollisionsAreAtomic(t *testing.T) {
	for _, collision := range []string{"journal", "outbox"} {
		t.Run(collision, func(t *testing.T) {
			ctx := context.Background()
			now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
			store := NewMemoryStore()
			service := newTestService(store, func() time.Time { return now })
			setupActiveProgram(t, ctx, service, "tenant-a", 0)
			earnings, err := service.RecordEvent(ctx, hostActor(), "program-a", CommissionEvent{
				TenantID:       "tenant-a",
				SourceType:     "payment.succeeded",
				SourceID:       "payment-1",
				OccurredAt:     now,
				Commissionable: Amount{Currency: "USD", Minor: 10_000},
			})
			if err != nil {
				t.Fatalf("RecordEvent() error = %v", err)
			}
			candidate := cloneEarning(earnings[0])
			candidate.Status = EarningStatusHeld
			candidate.Version++
			candidate.UpdatedAt = now
			transition := newEarningTransition(candidate, EarningActionHold, now)
			store.mu.Lock()
			if collision == "journal" {
				store.journalIDs[transition.journal.ID] = struct{}{}
			} else {
				store.outbox[outboxKey{tenantID: transition.outbox.TenantID, id: transition.outbox.ID}] = transition.outbox
			}
			store.mu.Unlock()

			if _, err := service.TransitionEarning(ctx, hostActor(), "tenant-a", earnings[0].ID, earnings[0].Version, EarningActionHold); !errors.Is(err, ErrEventConflict) {
				t.Fatalf("TransitionEarning(%s collision) error = %v, want ErrEventConflict", collision, err)
			}
			after, _, err := service.GetEarning(ctx, hostActor(), "tenant-a", earnings[0].ID)
			if err != nil || after.Status != EarningStatusAvailable || after.Version != earnings[0].Version {
				t.Fatalf("GetEarning(after collision) = %+v, %v; want unchanged available earning", after, err)
			}
		})
	}
}

func TestMemoryStoreSettlementOutboxCollisionIsAtomic(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	service := newTestService(store, func() time.Time { return now })
	setupActiveProgram(t, ctx, service, "tenant-a", 0)
	earnings, err := service.RecordEvent(ctx, hostActor(), "program-a", CommissionEvent{
		TenantID:       "tenant-a",
		SourceType:     "payment.succeeded",
		SourceID:       "payment-1",
		OccurredAt:     now,
		Commissionable: Amount{Currency: "USD", Minor: 10_000},
	})
	if err != nil {
		t.Fatalf("RecordEvent() error = %v", err)
	}
	id := "settlement-1"
	conflictID := outboxID("tenant-a", id, "commission.settlement.submitted", 1)
	store.mu.Lock()
	store.outbox[outboxKey{tenantID: "tenant-a", id: conflictID}] = OutboxEvent{ID: conflictID, TenantID: "tenant-a", Type: "test", AggregateID: "reserved", CreatedAt: now}
	store.mu.Unlock()
	if _, err := service.StartSettlement(ctx, hostActor(), "tenant-a", id, []string{earnings[0].ID}); !errors.Is(err, ErrEventConflict) {
		t.Fatalf("StartSettlement(outbox collision) error = %v, want ErrEventConflict", err)
	}
	after, _, err := service.GetEarning(ctx, hostActor(), "tenant-a", earnings[0].ID)
	if err != nil || after.Status != EarningStatusAvailable || after.Version != earnings[0].Version {
		t.Fatalf("GetEarning(after collision) = %+v, %v; want unchanged available earning", after, err)
	}
	if _, err := store.GetSettlement(ctx, "tenant-a", id); !errors.Is(err, ErrSettlementNotFound) {
		t.Fatalf("GetSettlement(after collision) error = %v, want ErrSettlementNotFound", err)
	}
}

func TestMemoryStoreOutboxCursorKeepsCreatedOrder(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	base := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	store.mu.Lock()
	store.outbox[outboxKey{tenantID: "tenant-a", id: "z"}] = OutboxEvent{ID: "z", TenantID: "tenant-a", Type: "test", AggregateID: "one", CreatedAt: base}
	store.outbox[outboxKey{tenantID: "tenant-a", id: "a"}] = OutboxEvent{ID: "a", TenantID: "tenant-a", Type: "test", AggregateID: "two", CreatedAt: base.Add(time.Second)}
	store.mu.Unlock()
	first, err := store.ListOutbox(ctx, "tenant-a", OutboxFilter{Limit: 1})
	if err != nil || len(first) != 1 || first[0].ID != "z" {
		t.Fatalf("ListOutbox(first) = %+v, %v; want z", first, err)
	}
	next, err := store.ListOutbox(ctx, "tenant-a", OutboxFilter{Cursor: OutboxCursorFor(first[0]), Limit: 1})
	if err != nil || len(next) != 1 || next[0].ID != "a" {
		t.Fatalf("ListOutbox(next) = %+v, %v; want a", next, err)
	}
}

func TestSettlementOutboxIDsAreTenantScoped(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	service := newTestService(store, func() time.Time { return now })
	template := testTemplate(0)
	if err := service.CreateTemplate(ctx, hostActor(), template); err != nil {
		t.Fatalf("CreateTemplate() error = %v", err)
	}
	if _, err := service.TransitionTemplate(ctx, hostActor(), template.ID, template.Version, TemplateActionActivate); err != nil {
		t.Fatalf("TransitionTemplate() error = %v", err)
	}

	for _, tenantID := range []types.TenantID{"tenant-a", "tenant-b"} {
		program := testProgram(tenantID)
		if err := service.CreateProgram(ctx, hostActor(), program); err != nil {
			t.Fatalf("CreateProgram(%s) error = %v", tenantID, err)
		}
		pending, err := service.TransitionProgram(ctx, hostActor(), tenantID, program.ID, 1, ProgramActionSubmit)
		if err != nil {
			t.Fatalf("TransitionProgram(%s submit) error = %v", tenantID, err)
		}
		if _, err := service.TransitionProgram(ctx, hostActor(), tenantID, program.ID, pending.Version, ProgramActionApprove); err != nil {
			t.Fatalf("TransitionProgram(%s approve) error = %v", tenantID, err)
		}
		earnings, err := service.RecordEvent(ctx, hostActor(), program.ID, CommissionEvent{
			TenantID:       tenantID,
			SourceType:     "payment.succeeded",
			SourceID:       "payment-1",
			OccurredAt:     now,
			Commissionable: Amount{Currency: "USD", Minor: 10_000},
		})
		if err != nil {
			t.Fatalf("RecordEvent(%s) error = %v", tenantID, err)
		}
		if _, err := service.StartSettlement(ctx, hostActor(), tenantID, "settlement-1", []string{earnings[0].ID}); err != nil {
			t.Fatalf("StartSettlement(%s) error = %v", tenantID, err)
		}
	}

	for _, tenantID := range []types.TenantID{"tenant-a", "tenant-b"} {
		events, err := store.ListOutbox(ctx, tenantID, OutboxFilter{})
		if err != nil {
			t.Fatalf("ListOutbox(%s) error = %v", tenantID, err)
		}
		if !hasOutboxType(events, "commission.settlement.submitted") {
			t.Fatalf("ListOutbox(%s) = %+v, want settlement submitted event", tenantID, events)
		}
	}
}

func TestServiceTenantBoundaryAndConcurrentEventDedupe(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	service := newTestService(store, func() time.Time { return now })
	setupActiveProgram(t, ctx, service, "tenant-a", 0)
	event := CommissionEvent{
		TenantID:       "tenant-a",
		SourceType:     "payment.succeeded",
		SourceID:       "payment-1",
		OccurredAt:     now,
		Commissionable: Amount{Currency: "USD", Minor: 10_000},
	}
	if _, err := service.RecordEvent(ctx, Actor{ID: "user-b", TenantID: "tenant-b"}, "program-a", event); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("RecordEvent(cross tenant) error = %v, want ErrUnauthorized", err)
	}
	if _, err := service.RecordEvent(ctx, Actor{ID: "user-a", TenantID: "tenant-a"}, "program-a", event); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("RecordEvent(non-host) error = %v, want ErrUnauthorized", err)
	}

	const workers = 12
	errs := make(chan error, workers)
	var wait sync.WaitGroup
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			got, err := service.RecordEvent(ctx, hostActor(), "program-a", event)
			if err != nil {
				errs <- err
				return
			}
			if len(got) != 1 {
				errs <- errors.New("expected one earning")
			}
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent RecordEvent() error = %v", err)
	}
	all, err := store.ListEarnings(ctx, "tenant-a", EarningFilter{})
	if err != nil {
		t.Fatalf("ListEarnings() error = %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("ListEarnings() = %+v, want one idempotent earning", all)
	}
}

func TestCommitEventRejectsChangedAttributionSnapshot(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	service := newTestService(store, func() time.Time { return now })
	setupActiveProgram(t, ctx, service, "tenant-a", 0)
	program, err := store.GetProgram(ctx, "tenant-a", "program-a")
	if err != nil {
		t.Fatalf("GetProgram() error = %v", err)
	}
	template, err := store.GetTemplate(ctx, program.TemplateID, program.TemplateVersion)
	if err != nil {
		t.Fatalf("GetTemplate() error = %v", err)
	}
	event := CommissionEvent{
		TenantID:       "tenant-a",
		SourceType:     "payment.succeeded",
		SourceID:       "payment-1",
		OccurredAt:     now,
		Commissionable: Amount{Currency: "USD", Minor: 10_000},
	}
	effective, err := effectiveTemplate(template, program, nil)
	if err != nil {
		t.Fatalf("effectiveTemplate() error = %v", err)
	}
	calculations, err := Calculate(effective, event)
	if err != nil {
		t.Fatalf("Calculate() error = %v", err)
	}
	earnings, journals, outbox := buildEventEntries(event, program, template, calculations, now)
	if _, err := store.SetAttribution(ctx, Attribution{
		TenantID:    "tenant-a",
		ProgramID:   "program-a",
		Slot:        "referral",
		Beneficiary: BeneficiaryRef{Kind: BeneficiaryKindExternal, ID: "new-partner"},
		Active:      true,
		CreatedAt:   now,
		UpdatedAt:   now,
	}, 0); err != nil {
		t.Fatalf("SetAttribution() error = %v", err)
	}
	if _, err := store.CommitEvent(ctx, EventCommit{
		Event:               event,
		ProgramID:           program.ID,
		ProgramVersion:      program.Version,
		TemplateID:          template.ID,
		TemplateVersion:     template.Version,
		AttributionVersions: map[string]int64{"referral": 0},
		Earnings:            earnings,
		Journals:            journals,
		Outbox:              outbox,
	}); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("CommitEvent(stale attribution) error = %v, want ErrVersionConflict", err)
	}
}

func TestMemoryStoreOutboxCopiesAndMarksPublished(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	service := newTestService(store, func() time.Time { return now })
	setupActiveProgram(t, ctx, service, "tenant-a", 0)
	if _, err := service.RecordEvent(ctx, hostActor(), "program-a", CommissionEvent{
		TenantID:       "tenant-a",
		SourceType:     "payment.succeeded",
		SourceID:       "payment-1",
		OccurredAt:     now,
		Commissionable: Amount{Currency: "USD", Minor: 10_000},
	}); err != nil {
		t.Fatalf("RecordEvent() error = %v", err)
	}
	events, err := store.ListOutbox(ctx, "tenant-a", OutboxFilter{UnpublishedOnly: true})
	if err != nil {
		t.Fatalf("ListOutbox() error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("ListOutbox() = %+v, want one event", events)
	}
	events[0].Payload["status"] = "changed"
	if err := store.MarkOutboxPublished(ctx, "tenant-a", events[0].ID, now); err != nil {
		t.Fatalf("MarkOutboxPublished() error = %v", err)
	}
	remaining, err := store.ListOutbox(ctx, "tenant-a", OutboxFilter{UnpublishedOnly: true})
	if err != nil || len(remaining) != 0 {
		t.Fatalf("ListOutbox(unpublished) = %+v, %v; want empty nil", remaining, err)
	}
}

func TestOutboxDeliveryCommandsRequireHostActor(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	service := newTestService(NewMemoryStore(), func() time.Time { return now })
	setupActiveProgram(t, ctx, service, "tenant-a", 0)
	if _, err := service.RecordEvent(ctx, hostActor(), "program-a", CommissionEvent{
		TenantID:       "tenant-a",
		SourceType:     "payment.succeeded",
		SourceID:       "payment-1",
		OccurredAt:     now,
		Commissionable: Amount{Currency: "USD", Minor: 10_000},
	}); err != nil {
		t.Fatalf("RecordEvent() error = %v", err)
	}
	tenantActor := Actor{ID: "tenant-worker", TenantID: "tenant-a"}
	if _, err := service.ListOutbox(ctx, tenantActor, "tenant-a", OutboxFilter{UnpublishedOnly: true}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("ListOutbox(non-host) error = %v, want ErrUnauthorized", err)
	}
	events, err := service.ListOutbox(ctx, hostActor(), "tenant-a", OutboxFilter{UnpublishedOnly: true})
	if err != nil || len(events) != 1 {
		t.Fatalf("ListOutbox(host) = %+v, %v; want one unpublished event", events, err)
	}
	if err := service.MarkOutboxPublished(ctx, tenantActor, "tenant-a", events[0].ID, now); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("MarkOutboxPublished(non-host) error = %v, want ErrUnauthorized", err)
	}
	if err := service.MarkOutboxPublished(ctx, hostActor(), "tenant-a", events[0].ID, now); err != nil {
		t.Fatalf("MarkOutboxPublished(host) error = %v", err)
	}
	remaining, err := service.ListOutbox(ctx, hostActor(), "tenant-a", OutboxFilter{UnpublishedOnly: true})
	if err != nil || len(remaining) != 0 {
		t.Fatalf("ListOutbox(after mark) = %+v, %v; want empty nil", remaining, err)
	}
}

func setupActiveProgram(t *testing.T, ctx context.Context, service *Service, tenantID types.TenantID, freeze time.Duration) {
	t.Helper()
	template := testTemplate(freeze)
	if err := service.CreateTemplate(ctx, hostActor(), template); err != nil {
		t.Fatalf("CreateTemplate() error = %v", err)
	}
	if _, err := service.TransitionTemplate(ctx, hostActor(), template.ID, template.Version, TemplateActionActivate); err != nil {
		t.Fatalf("TransitionTemplate() error = %v", err)
	}
	program := testProgram(tenantID)
	if err := service.CreateProgram(ctx, hostActor(), program); err != nil {
		t.Fatalf("CreateProgram() error = %v", err)
	}
	pending, err := service.TransitionProgram(ctx, hostActor(), tenantID, program.ID, 1, ProgramActionSubmit)
	if err != nil {
		t.Fatalf("TransitionProgram(submit) error = %v", err)
	}
	if _, err := service.TransitionProgram(ctx, hostActor(), tenantID, program.ID, pending.Version, ProgramActionApprove); err != nil {
		t.Fatalf("TransitionProgram(approve) error = %v", err)
	}
}

func testTemplate(freeze time.Duration) Template {
	return Template{
		ID:                "template-a",
		Version:           1,
		Status:            TemplateStatusDraft,
		AllowedEventTypes: []string{"payment.succeeded"},
		MaxCommission:     Amount{Currency: "USD", Minor: 1_000},
		FreezePeriod:      freeze,
		Rules: []Rule{{
			Slot: "referral",
			Tiers: []Tier{{
				MinMinor:    0,
				BasisPoints: 1_000,
			}},
		}},
	}
}

func testProgram(tenantID types.TenantID) Program {
	return Program{
		ID:              "program-a",
		TenantID:        tenantID,
		TemplateID:      "template-a",
		TemplateVersion: 1,
		Status:          ProgramStatusDraft,
		Rules: []Rule{{
			Slot:        "referral",
			Beneficiary: BeneficiaryRef{Kind: BeneficiaryKindExternal, ID: "partner-1"},
			Tiers: []Tier{{
				MinMinor:    0,
				BasisPoints: 500,
			}},
		}},
	}
}

func hostActor() Actor {
	return Actor{ID: "host", Host: true}
}

type allowCommissionAuthorizer struct{}

func (allowCommissionAuthorizer) Authorize(context.Context, Actor, Permission, types.TenantID) error {
	return nil
}

func newTestService(store Store, clock func() time.Time, opts ...Option) *Service {
	options := []Option{WithClock(clock), WithAuthorizer(allowCommissionAuthorizer{})}
	options = append(options, opts...)
	return NewService(store, options...)
}

func containsEarningAction(actions []EarningAction, wanted EarningAction) bool {
	for _, action := range actions {
		if action == wanted {
			return true
		}
	}
	return false
}

func hasOutboxType(events []OutboxEvent, wanted string) bool {
	for _, event := range events {
		if event.Type == wanted {
			return true
		}
	}
	return false
}

type testSettlementAdapter struct {
	receipt SettlementReceipt
	err     error
}

func (adapter testSettlementAdapter) Submit(context.Context, Settlement) (SettlementReceipt, error) {
	return adapter.receipt, adapter.err
}

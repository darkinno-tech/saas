package commission

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/darkinno-tech/saas/core/types"
)

// Permission identifies a host authorization decision required by Service.
// It deliberately does not depend on a particular RBAC implementation.
type Permission string

const (
	// PermissionRead allows tenant-scoped commission reads.
	PermissionRead Permission = "commission.read"
	// PermissionManageTemplates allows platform template administration.
	PermissionManageTemplates Permission = "commission.templates.manage"
	// PermissionManagePrograms allows tenant program administration.
	PermissionManagePrograms Permission = "commission.programs.manage"
	// PermissionApprovePrograms allows a trusted platform actor to approve a
	// tenant program after the tenant has submitted it for review.
	PermissionApprovePrograms Permission = "commission.programs.approve"
	// PermissionManageAttributions allows beneficiary attribution changes.
	PermissionManageAttributions Permission = "commission.attributions.manage"
	// PermissionRecordEvents allows recording commissionable business facts.
	PermissionRecordEvents Permission = "commission.events.record"
	// PermissionManageEarnings allows hold, release, and reversal actions.
	PermissionManageEarnings Permission = "commission.earnings.manage"
	// PermissionSettle allows settlement batch submission.
	PermissionSettle Permission = "commission.settle"
	// PermissionCompleteSettlement allows a trusted host callback to record a
	// provider-verified settlement outcome.
	PermissionCompleteSettlement Permission = "commission.settlements.complete"
	// PermissionDeliverOutbox allows a trusted host worker to read and
	// acknowledge internal delivery records.
	PermissionDeliverOutbox Permission = "commission.outbox.deliver"
)

// Actor is the host-provided identity that initiates a Service command. Host
// is an identity claim, not a credential: every host-only command also
// requires a configured Authorizer to verify the claim in the host runtime.
type Actor struct {
	ID       string
	TenantID types.TenantID
	Host     bool
}

// Authorizer checks whether actor may perform a commission action for tenantID.
// It is required for every Service command so an application never treats a
// caller-controlled Actor or Actor.Host flag as an authorization decision.
type Authorizer interface {
	Authorize(ctx context.Context, actor Actor, permission Permission, tenantID types.TenantID) error
}

// SettlementAdapter performs an optional host-owned settlement submission.
// The commission package neither stores credentials nor imports payment SDKs.
// Implementations should use Settlement.ID as their provider idempotency key.
type SettlementAdapter interface {
	Submit(ctx context.Context, settlement Settlement) (SettlementReceipt, error)
}

// VerifiedSettlementRejection marks an adapter error as a confirmed provider
// rejection. Unclassified errors are treated as ambiguous: Service leaves the
// settlement submitted so the host can reconcile it without risking a retry
// payout.
type VerifiedSettlementRejection interface {
	error
	SettlementRejected() bool
}

// SettlementSubmissionStatus identifies what a provider has durably accepted
// or completed. A successful transport call is not itself a settlement
// outcome: asynchronous providers must return pending and complete the batch
// only after a verified callback or reconciliation result.
type SettlementSubmissionStatus string

const (
	SettlementSubmissionPending  SettlementSubmissionStatus = "pending"
	SettlementSubmissionSettled  SettlementSubmissionStatus = "settled"
	SettlementSubmissionRejected SettlementSubmissionStatus = "rejected"
)

func validSettlementSubmissionStatus(status SettlementSubmissionStatus) bool {
	switch status {
	case SettlementSubmissionPending, SettlementSubmissionSettled, SettlementSubmissionRejected:
		return true
	default:
		return false
	}
}

// SettlementReceipt is an explicit provider result. Pending leaves the
// settlement submitted; only Settled or Rejected records a terminal outcome.
type SettlementReceipt struct {
	Status            SettlementSubmissionStatus
	ProviderReference string
}

// TemplateAction describes a legal template lifecycle operation.
type TemplateAction string

const (
	TemplateActionActivate TemplateAction = "activate"
	TemplateActionRetire   TemplateAction = "retire"
)

// ProgramAction describes a legal tenant-program lifecycle operation.
type ProgramAction string

const (
	ProgramActionSubmit  ProgramAction = "submit"
	ProgramActionApprove ProgramAction = "approve"
	ProgramActionSuspend ProgramAction = "suspend"
	ProgramActionResume  ProgramAction = "resume"
	ProgramActionRetire  ProgramAction = "retire"
)

// Program selects a versioned platform Template for a tenant and supplies the
// tenant's constrained rules. Rules are immutable once the program is created;
// changing a rule requires a new program rather than editing financial history.
type Program struct {
	ID              string
	TenantID        types.TenantID
	TemplateID      string
	TemplateVersion int64
	Status          ProgramStatus
	Version         int64
	Rules           []Rule
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// Attribution overrides the beneficiary for one program rule slot. Setting
// Active false revokes the attribution without removing its audit trail.
type Attribution struct {
	TenantID    types.TenantID
	ProgramID   string
	Slot        string
	Beneficiary BeneficiaryRef
	Active      bool
	Version     int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Earning is the mutable projection of immutable journal entries for one
// source event, rule slot, and beneficiary.
type Earning struct {
	ID              string
	TenantID        types.TenantID
	ProgramID       string
	TemplateID      string
	TemplateVersion int64
	SourceType      string
	SourceID        string
	Slot            string
	Beneficiary     BeneficiaryRef
	Amount          Amount
	Status          EarningStatus
	AvailableAt     time.Time
	Version         int64
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// JournalKind describes why an immutable accounting entry was created.
type JournalKind string

const (
	JournalKindAccrual    JournalKind = "accrual"
	JournalKindAvailable  JournalKind = "available"
	JournalKindHeld       JournalKind = "held"
	JournalKindSettlement JournalKind = "settlement"
	JournalKindReversal   JournalKind = "reversal"
	JournalKindRecovery   JournalKind = "recovery"
)

// JournalEntry is append-only. Corrections are recorded as new entries and
// never rewrite an earlier financial fact.
type JournalEntry struct {
	ID        string
	TenantID  types.TenantID
	EarningID string
	Kind      JournalKind
	Amount    Amount
	CreatedAt time.Time
}

// OutboxEvent is a durable host-deliverable commission fact. Publication is
// outside this package and is marked only after the host has delivered it.
type OutboxEvent struct {
	ID          string
	TenantID    types.TenantID
	Type        string
	AggregateID string
	Payload     map[string]string
	CreatedAt   time.Time
	PublishedAt *time.Time
}

// SettlementStatus describes the host settlement lifecycle.
type SettlementStatus string

const (
	SettlementStatusSubmitted SettlementStatus = "submitted"
	SettlementStatusSettled   SettlementStatus = "settled"
	SettlementStatusRejected  SettlementStatus = "rejected"
)

// Settlement groups available earnings for one beneficiary and currency.
type Settlement struct {
	ID                string
	TenantID          types.TenantID
	Beneficiary       BeneficiaryRef
	Amount            Amount
	EarningIDs        []string
	Status            SettlementStatus
	ProviderReference string
	Version           int64
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// EventCommit is a fully calculated, immutable event application. Store
// implementations persist it atomically after re-checking program/template
// versions and the source-event uniqueness key.
type EventCommit struct {
	Event               CommissionEvent
	ProgramID           string
	ProgramVersion      int64
	TemplateID          string
	TemplateVersion     int64
	AttributionVersions map[string]int64
	Earnings            []Earning
	Journals            []JournalEntry
	Outbox              []OutboxEvent
}

// EarningFilter restricts earnings to a tenant and optional program,
// beneficiary, status, or cursor/limit window.
type EarningFilter struct {
	ProgramID   string
	Beneficiary *BeneficiaryRef
	Statuses    []EarningStatus
	Cursor      string
	Limit       int
}

// OutboxCursor identifies the last outbox event from a previous ordered page.
type OutboxCursor struct {
	CreatedAt time.Time
	ID        string
}

// OutboxFilter restricts outbox reads. UnpublishedOnly is the normal host
// delivery path; an empty filter returns all tenant events.
type OutboxFilter struct {
	UnpublishedOnly bool
	Cursor          OutboxCursor
	Limit           int
}

// OutboxCursorFor returns a cursor for continuing after event.
func OutboxCursorFor(event OutboxEvent) OutboxCursor {
	return OutboxCursor{CreatedAt: event.CreatedAt, ID: event.ID}
}

// Store is a trusted persistence boundary. Its aggregate operations
// intentionally avoid exposing a generic CRUD repository or a caller-owned
// sql.Tx. It is not an authorization boundary: request handlers must call
// Service rather than constructing EventCommit or settlement state directly.
type Store interface {
	CreateTemplate(ctx context.Context, template Template) error
	GetTemplate(ctx context.Context, id string, version int64) (Template, error)
	TransitionTemplate(ctx context.Context, id string, version int64, action TemplateAction, now time.Time) (Template, error)

	CreateProgram(ctx context.Context, program Program) error
	GetProgram(ctx context.Context, tenantID types.TenantID, id string) (Program, error)
	TransitionProgram(ctx context.Context, tenantID types.TenantID, id string, expectedVersion int64, action ProgramAction, now time.Time) (Program, error)

	SetAttribution(ctx context.Context, attribution Attribution, expectedVersion int64) (Attribution, error)
	ListAttributions(ctx context.Context, tenantID types.TenantID, programID string) ([]Attribution, error)

	CommitEvent(ctx context.Context, commit EventCommit) ([]Earning, error)
	GetEarning(ctx context.Context, tenantID types.TenantID, id string) (Earning, error)
	ListEarnings(ctx context.Context, tenantID types.TenantID, filter EarningFilter) ([]Earning, error)
	ListJournalEntries(ctx context.Context, tenantID types.TenantID, earningID string) ([]JournalEntry, error)
	TransitionEarning(ctx context.Context, tenantID types.TenantID, id string, expectedVersion int64, action EarningAction, now time.Time) (Earning, error)
	MakeAvailableDue(ctx context.Context, now time.Time, limit int) ([]Earning, error)

	StartSettlement(ctx context.Context, settlement Settlement, expectedVersions map[string]int64) (Settlement, error)
	GetSettlement(ctx context.Context, tenantID types.TenantID, id string) (Settlement, error)
	FinishSettlement(ctx context.Context, tenantID types.TenantID, id string, expectedVersion int64, settled bool, providerReference string, now time.Time) (Settlement, error)

	ListOutbox(ctx context.Context, tenantID types.TenantID, filter OutboxFilter) ([]OutboxEvent, error)
	MarkOutboxPublished(ctx context.Context, tenantID types.TenantID, id string, publishedAt time.Time) error
}

// Service applies authorization, rule composition, calculation, and host
// settlement orchestration around a Store.
type Service struct {
	store      Store
	authorizer Authorizer
	adapter    SettlementAdapter
	now        func() time.Time
	limits     ServiceLimits
	limitsErr  error
}

// Option configures a Service.
type Option func(*Service)

// WithAuthorizer installs the authorization boundary for all Service calls.
// Every Service command fails closed when no Authorizer is configured.
func WithAuthorizer(authorizer Authorizer) Option {
	return func(service *Service) {
		service.authorizer = authorizer
	}
}

// WithSettlementAdapter installs an optional host-owned settlement adapter.
func WithSettlementAdapter(adapter SettlementAdapter) Option {
	return func(service *Service) {
		service.adapter = adapter
	}
}

// WithClock sets the clock used for event availability and state timestamps.
func WithClock(clock func() time.Time) Option {
	return func(service *Service) {
		if clock != nil {
			service.now = clock
		}
	}
}

// WithLimits configures request and page bounds. Omitted or zero-valued fields
// use DefaultServiceLimits. Invalid bounds make all Service commands fail
// closed with ErrLimitExceeded instead of falling back to unbounded behavior.
func WithLimits(limits ServiceLimits) Option {
	return func(service *Service) {
		normalized, err := normalizeServiceLimits(limits)
		service.limits = normalized
		service.limitsErr = err
	}
}

// NewService creates a commission application service around store. Configure
// WithAuthorizer before using any command. The Store remains a trusted
// infrastructure port and should not be exposed to request handlers.
func NewService(store Store, opts ...Option) *Service {
	service := &Service{store: store, now: time.Now, limits: DefaultServiceLimits()}
	for _, opt := range opts {
		if opt != nil {
			opt(service)
		}
	}
	return service
}

// CreateTemplate creates a platform-owned template.
func (service *Service) CreateTemplate(ctx context.Context, actor Actor, template Template) error {
	if err := service.authorizeHost(ctx, actor, PermissionManageTemplates, ""); err != nil {
		return err
	}
	if service.store == nil {
		return ErrNilStore
	}
	if template.Status == "" {
		template.Status = TemplateStatusDraft
	}
	if template.Status != TemplateStatusDraft {
		return ErrInvalidTemplate
	}
	if template.CreatedAt.IsZero() {
		template.CreatedAt = service.now()
	}
	if template.UpdatedAt.IsZero() {
		template.UpdatedAt = template.CreatedAt
	}
	if err := service.limits.validateTemplate(template); err != nil {
		return err
	}
	return service.store.CreateTemplate(ctx, template)
}

// TransitionTemplate moves a template through its lifecycle.
func (service *Service) TransitionTemplate(ctx context.Context, actor Actor, id string, version int64, action TemplateAction) (Template, error) {
	if err := service.authorizeHost(ctx, actor, PermissionManageTemplates, ""); err != nil {
		return Template{}, err
	}
	if service.store == nil {
		return Template{}, ErrNilStore
	}
	if !service.limits.identifierOK(id) {
		return Template{}, ErrLimitExceeded
	}
	return service.store.TransitionTemplate(ctx, id, version, action, service.now())
}

// CreateProgram creates a tenant-scoped program under a template version.
func (service *Service) CreateProgram(ctx context.Context, actor Actor, program Program) error {
	if err := service.authorize(ctx, actor, PermissionManagePrograms, program.TenantID); err != nil {
		return err
	}
	if service.store == nil {
		return ErrNilStore
	}
	if err := service.limits.validateProgram(program); err != nil {
		return err
	}
	if program.Status == "" {
		program.Status = ProgramStatusDraft
	}
	if program.Status != ProgramStatusDraft {
		return ErrInvalidProgram
	}
	if program.Version == 0 {
		program.Version = 1
	}
	if program.Version != 1 {
		return ErrInvalidProgram
	}
	template, err := service.store.GetTemplate(ctx, program.TemplateID, program.TemplateVersion)
	if err != nil {
		return err
	}
	if err := validateProgramAgainstTemplate(program, template); err != nil {
		return err
	}
	if program.CreatedAt.IsZero() {
		program.CreatedAt = service.now()
	}
	if program.UpdatedAt.IsZero() {
		program.UpdatedAt = program.CreatedAt
	}
	return service.store.CreateProgram(ctx, program)
}

// TransitionProgram moves a tenant program through review and operating states.
func (service *Service) TransitionProgram(ctx context.Context, actor Actor, tenantID types.TenantID, id string, expectedVersion int64, action ProgramAction) (Program, error) {
	if action == ProgramActionApprove {
		if err := service.authorizeHost(ctx, actor, PermissionApprovePrograms, tenantID); err != nil {
			return Program{}, err
		}
	} else {
		if err := service.authorize(ctx, actor, PermissionManagePrograms, tenantID); err != nil {
			return Program{}, err
		}
	}
	if service.store == nil {
		return Program{}, ErrNilStore
	}
	if !service.limits.identifierOK(id) {
		return Program{}, ErrLimitExceeded
	}
	return service.store.TransitionProgram(ctx, tenantID, id, expectedVersion, action, service.now())
}

// SetAttribution changes the active beneficiary for one program slot.
func (service *Service) SetAttribution(ctx context.Context, actor Actor, tenantID types.TenantID, attribution Attribution, expectedVersion int64) (Attribution, error) {
	if err := service.authorize(ctx, actor, PermissionManageAttributions, tenantID); err != nil {
		return Attribution{}, err
	}
	if service.store == nil {
		return Attribution{}, ErrNilStore
	}
	if attribution.TenantID != tenantID {
		return Attribution{}, ErrInvalidAttribution
	}
	if err := service.limits.validateAttribution(attribution); err != nil {
		return Attribution{}, err
	}
	program, err := service.store.GetProgram(ctx, tenantID, attribution.ProgramID)
	if err != nil {
		return Attribution{}, err
	}
	if !programHasSlot(program, attribution.Slot) {
		return Attribution{}, ErrInvalidAttribution
	}
	if err := validateAttribution(attribution); err != nil {
		return Attribution{}, err
	}
	if attribution.CreatedAt.IsZero() {
		attribution.CreatedAt = service.now()
	}
	attribution.UpdatedAt = service.now()
	return service.store.SetAttribution(ctx, attribution, expectedVersion)
}

// RecordEvent records a host-normalized business event. Repeating the same
// tenant/source type/source ID returns the original earnings without issuing a
// second commission; a changed payload is rejected by Store.
func (service *Service) RecordEvent(ctx context.Context, actor Actor, programID string, event CommissionEvent) ([]Earning, error) {
	if err := service.authorizeHost(ctx, actor, PermissionRecordEvents, event.TenantID); err != nil {
		return nil, err
	}
	if service.store == nil {
		return nil, ErrNilStore
	}
	if !service.limits.identifierOK(programID) {
		return nil, ErrLimitExceeded
	}
	if err := validateCommissionEvent(event); err != nil {
		return nil, err
	}
	if err := service.limits.validateEvent(event); err != nil {
		return nil, err
	}
	program, err := service.store.GetProgram(ctx, event.TenantID, programID)
	if err != nil {
		return nil, err
	}
	if program.Status != ProgramStatusActive {
		return nil, ErrProgramNotActive
	}
	template, err := service.store.GetTemplate(ctx, program.TemplateID, program.TemplateVersion)
	if err != nil {
		return nil, err
	}
	if template.Status != TemplateStatusActive {
		return nil, ErrTemplateNotActive
	}
	attributions, err := service.store.ListAttributions(ctx, event.TenantID, program.ID)
	if err != nil {
		return nil, err
	}
	effective, err := effectiveTemplate(template, program, attributions)
	if err != nil {
		return nil, err
	}
	calculations, err := Calculate(effective, event)
	if err != nil {
		return nil, err
	}
	now := service.now()
	earnings, journals, outbox := buildEventEntries(event, program, template, calculations, now)
	return service.store.CommitEvent(ctx, EventCommit{
		Event:               cloneCommissionEvent(event),
		ProgramID:           program.ID,
		ProgramVersion:      program.Version,
		TemplateID:          template.ID,
		TemplateVersion:     template.Version,
		AttributionVersions: attributionVersions(program, attributions),
		Earnings:            earnings,
		Journals:            journals,
		Outbox:              outbox,
	})
}

// MakeAvailableDue releases at most the configured MaxDueBatch pending
// earnings whose freeze period has elapsed. Repeated calls continue with the
// next due rows because a completed row no longer matches the pending scan.
func (service *Service) MakeAvailableDue(ctx context.Context, actor Actor) ([]Earning, error) {
	if err := service.authorizeHost(ctx, actor, PermissionManageEarnings, ""); err != nil {
		return nil, err
	}
	if service.store == nil {
		return nil, ErrNilStore
	}
	return service.store.MakeAvailableDue(ctx, service.now(), service.limits.MaxDueBatch)
}

// TransitionEarning applies a user-directed lifecycle operation with an
// optimistic version precondition.
func (service *Service) TransitionEarning(ctx context.Context, actor Actor, tenantID types.TenantID, id string, expectedVersion int64, action EarningAction) (Earning, error) {
	if err := service.authorize(ctx, actor, PermissionManageEarnings, tenantID); err != nil {
		return Earning{}, err
	}
	if service.store == nil {
		return Earning{}, ErrNilStore
	}
	if !service.limits.identifierOK(id) {
		return Earning{}, ErrLimitExceeded
	}
	if action == EarningActionMakeAvailable {
		return Earning{}, ErrInvalidEarningTransition
	}
	return service.store.TransitionEarning(ctx, tenantID, id, expectedVersion, action, service.now())
}

// StartSettlement atomically groups available earnings for one beneficiary and
// moves them to settling. The host may call CompleteSettlement later when an
// asynchronous provider callback is verified.
func (service *Service) StartSettlement(ctx context.Context, actor Actor, tenantID types.TenantID, id string, earningIDs []string) (Settlement, error) {
	if err := service.authorize(ctx, actor, PermissionSettle, tenantID); err != nil {
		return Settlement{}, err
	}
	if service.store == nil {
		return Settlement{}, ErrNilStore
	}
	if id == "" || len(earningIDs) == 0 {
		return Settlement{}, ErrInvalidSettlement
	}
	if !service.limits.identifierOK(id) || len(earningIDs) > service.limits.MaxSettlementItems {
		return Settlement{}, ErrLimitExceeded
	}

	earnings := make([]Earning, 0, len(earningIDs))
	expected := make(map[string]int64, len(earningIDs))
	seen := make(map[string]struct{}, len(earningIDs))
	for _, earningID := range earningIDs {
		if earningID == "" {
			return Settlement{}, ErrInvalidSettlement
		}
		if !service.limits.identifierOK(earningID) {
			return Settlement{}, ErrLimitExceeded
		}
		if _, ok := seen[earningID]; ok {
			return Settlement{}, ErrInvalidSettlement
		}
		seen[earningID] = struct{}{}
		earning, err := service.store.GetEarning(ctx, tenantID, earningID)
		if err != nil {
			return Settlement{}, err
		}
		if earning.Status != EarningStatusAvailable {
			return Settlement{}, ErrInvalidSettlement
		}
		earnings = append(earnings, earning)
		expected[earningID] = earning.Version
	}

	beneficiary := earnings[0].Beneficiary
	amount := Amount{Currency: earnings[0].Amount.Currency}
	for _, earning := range earnings {
		if earning.Beneficiary != beneficiary || earning.Amount.Currency != amount.Currency {
			return Settlement{}, ErrInvalidSettlement
		}
		if earning.Amount.Minor > int64(^uint64(0)>>1)-amount.Minor {
			return Settlement{}, ErrInvalidSettlement
		}
		amount.Minor += earning.Amount.Minor
	}

	now := service.now()
	return service.store.StartSettlement(ctx, Settlement{
		ID:          id,
		TenantID:    tenantID,
		Beneficiary: beneficiary,
		Amount:      amount,
		EarningIDs:  cloneStrings(earningIDs),
		Status:      SettlementStatusSubmitted,
		Version:     1,
		CreatedAt:   now,
		UpdatedAt:   now,
	}, expected)
}

// CompleteSettlement records the host-verified provider outcome. A rejection
// returns the constituent earnings to available; it never erases the batch.
func (service *Service) CompleteSettlement(ctx context.Context, actor Actor, tenantID types.TenantID, id string, expectedVersion int64, settled bool, providerReference string) (Settlement, error) {
	if err := service.authorizeHost(ctx, actor, PermissionCompleteSettlement, tenantID); err != nil {
		return Settlement{}, err
	}
	if service.store == nil {
		return Settlement{}, ErrNilStore
	}
	if !service.limits.identifierOK(id) {
		return Settlement{}, ErrLimitExceeded
	}
	if settled && providerReference == "" {
		return Settlement{}, ErrInvalidSettlementReceipt
	}
	if providerReference != "" && !service.limits.identifierOK(providerReference) {
		return Settlement{}, ErrLimitExceeded
	}
	return service.store.FinishSettlement(ctx, tenantID, id, expectedVersion, settled, providerReference, service.now())
}

// Settle submits a settlement through the optional adapter and then records its
// synchronous result. Hosts with asynchronous providers should use
// StartSettlement and CompleteSettlement instead.
func (service *Service) Settle(ctx context.Context, actor Actor, tenantID types.TenantID, id string, earningIDs []string) (Settlement, error) {
	if service == nil {
		return Settlement{}, ErrSettlementAdapterNotConfigured
	}
	if err := service.authorizeHost(ctx, actor, PermissionCompleteSettlement, tenantID); err != nil {
		return Settlement{}, err
	}
	if service.adapter == nil {
		return Settlement{}, ErrSettlementAdapterNotConfigured
	}
	settlement, err := service.StartSettlement(ctx, actor, tenantID, id, earningIDs)
	if err != nil {
		return Settlement{}, err
	}
	receipt, submitErr := service.adapter.Submit(ctx, cloneSettlement(settlement))
	if submitErr != nil {
		var rejection VerifiedSettlementRejection
		if errors.As(submitErr, &rejection) && rejection.SettlementRejected() {
			completed, completeErr := service.CompleteSettlement(ctx, actor, tenantID, settlement.ID, settlement.Version, false, receipt.ProviderReference)
			return completed, errors.Join(submitErr, completeErr)
		}
		return settlement, submitErr
	}
	if !validSettlementSubmissionStatus(receipt.Status) {
		return settlement, ErrInvalidSettlementReceipt
	}
	switch receipt.Status {
	case SettlementSubmissionPending:
		if !service.limits.identifierOK(receipt.ProviderReference) {
			return settlement, ErrInvalidSettlementReceipt
		}
		return settlement, nil
	case SettlementSubmissionSettled:
		if !service.limits.identifierOK(receipt.ProviderReference) {
			return settlement, ErrInvalidSettlementReceipt
		}
		return service.CompleteSettlement(ctx, actor, tenantID, settlement.ID, settlement.Version, true, receipt.ProviderReference)
	case SettlementSubmissionRejected:
		if receipt.ProviderReference != "" && !service.limits.identifierOK(receipt.ProviderReference) {
			return settlement, ErrLimitExceeded
		}
		return service.CompleteSettlement(ctx, actor, tenantID, settlement.ID, settlement.Version, false, receipt.ProviderReference)
	default:
		return settlement, ErrInvalidSettlementReceipt
	}
}

// GetEarning returns an earning and the individual actions that can be sent to
// TransitionEarning. Batch settlement and time-driven release are exposed by
// their own commands and are intentionally not advertised as per-earning
// actions.
func (service *Service) GetEarning(ctx context.Context, actor Actor, tenantID types.TenantID, id string) (Earning, []EarningAction, error) {
	if err := service.authorize(ctx, actor, PermissionRead, tenantID); err != nil {
		return Earning{}, nil, err
	}
	if service.store == nil {
		return Earning{}, nil, ErrNilStore
	}
	if !service.limits.identifierOK(id) {
		return Earning{}, nil, ErrLimitExceeded
	}
	earning, err := service.store.GetEarning(ctx, tenantID, id)
	if err != nil {
		return Earning{}, nil, err
	}
	return earning, AvailableManualEarningActions(earning.Status), nil
}

// ListEarnings returns tenant-scoped earnings under the configured read policy.
func (service *Service) ListEarnings(ctx context.Context, actor Actor, tenantID types.TenantID, filter EarningFilter) ([]Earning, error) {
	if err := service.authorize(ctx, actor, PermissionRead, tenantID); err != nil {
		return nil, err
	}
	if service.store == nil {
		return nil, ErrNilStore
	}
	normalized, err := service.limits.normalizeEarningFilter(filter)
	if err != nil {
		return nil, err
	}
	return service.store.ListEarnings(ctx, tenantID, normalized)
}

// ListJournalEntries returns immutable entries for an earning under the
// configured tenant read policy.
func (service *Service) ListJournalEntries(ctx context.Context, actor Actor, tenantID types.TenantID, earningID string) ([]JournalEntry, error) {
	if err := service.authorize(ctx, actor, PermissionRead, tenantID); err != nil {
		return nil, err
	}
	if service.store == nil {
		return nil, ErrNilStore
	}
	if !service.limits.identifierOK(earningID) {
		return nil, ErrLimitExceeded
	}
	return service.store.ListJournalEntries(ctx, tenantID, earningID)
}

// GetSettlement returns a tenant-scoped settlement batch under the configured
// read policy.
func (service *Service) GetSettlement(ctx context.Context, actor Actor, tenantID types.TenantID, id string) (Settlement, error) {
	if err := service.authorize(ctx, actor, PermissionRead, tenantID); err != nil {
		return Settlement{}, err
	}
	if service.store == nil {
		return Settlement{}, ErrNilStore
	}
	if !service.limits.identifierOK(id) {
		return Settlement{}, ErrLimitExceeded
	}
	return service.store.GetSettlement(ctx, tenantID, id)
}

// ListOutbox returns tenant-scoped outbox records for a trusted host delivery
// worker. Outbox records can include internal delivery metadata and are not a
// tenant-user read surface.
func (service *Service) ListOutbox(ctx context.Context, actor Actor, tenantID types.TenantID, filter OutboxFilter) ([]OutboxEvent, error) {
	if err := service.authorizeHost(ctx, actor, PermissionDeliverOutbox, tenantID); err != nil {
		return nil, err
	}
	if service.store == nil {
		return nil, ErrNilStore
	}
	normalized, err := service.limits.normalizeOutboxFilter(filter)
	if err != nil {
		return nil, err
	}
	return service.store.ListOutbox(ctx, tenantID, normalized)
}

// MarkOutboxPublished acknowledges a successfully delivered outbox event.
// It is host-only so tenant users cannot suppress internal event delivery.
func (service *Service) MarkOutboxPublished(ctx context.Context, actor Actor, tenantID types.TenantID, id string, publishedAt time.Time) error {
	if err := service.authorizeHost(ctx, actor, PermissionDeliverOutbox, tenantID); err != nil {
		return err
	}
	if service.store == nil {
		return ErrNilStore
	}
	if !service.limits.identifierOK(id) {
		return ErrLimitExceeded
	}
	if publishedAt.IsZero() {
		publishedAt = service.now()
	}
	return service.store.MarkOutboxPublished(ctx, tenantID, id, publishedAt)
}

func (service *Service) authorize(ctx context.Context, actor Actor, permission Permission, tenantID types.TenantID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if service == nil {
		return ErrNilStore
	}
	if tenantID == "" {
		if !actor.Host {
			return ErrUnauthorized
		}
	} else if !actor.Host && (actor.ID == "" || actor.TenantID != tenantID) {
		return ErrUnauthorized
	}
	if service.limitsErr != nil {
		return service.limitsErr
	}
	if tenantID != "" && !service.limits.identifierOK(tenantID.String()) {
		return ErrLimitExceeded
	}
	if service.authorizer == nil {
		return ErrUnauthorized
	}
	if err := service.authorizer.Authorize(ctx, actor, permission, tenantID); err != nil {
		return fmt.Errorf("commission authorize %s: %w", permission, err)
	}
	return nil
}

func (service *Service) authorizeHost(ctx context.Context, actor Actor, permission Permission, tenantID types.TenantID) error {
	if service == nil {
		return ErrNilStore
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !actor.Host || service.authorizer == nil {
		return ErrUnauthorized
	}
	return service.authorize(ctx, actor, permission, tenantID)
}

func validateProgramAgainstTemplate(program Program, template Template) error {
	if program.ID == "" || program.TenantID == "" || program.TemplateID == "" || program.TemplateVersion <= 0 || !validProgramStatus(program.Status) || len(program.Rules) == 0 {
		return ErrInvalidProgram
	}
	if template.ID != program.TemplateID || template.Version != program.TemplateVersion {
		return ErrInvalidProgram
	}
	slots := make(map[string]struct{}, len(program.Rules))
	for _, rule := range program.Rules {
		if err := validateRule(rule, true); err != nil {
			return ErrInvalidProgram
		}
		if _, exists := slots[rule.Slot]; exists {
			return ErrInvalidProgram
		}
		slots[rule.Slot] = struct{}{}
		capRule, ok := templateRule(template, rule.Slot)
		if !ok || !ruleWithinCap(rule, capRule) {
			return ErrProgramExceedsTemplate
		}
	}
	return nil
}

func validateAttribution(attribution Attribution) error {
	if attribution.TenantID == "" || attribution.ProgramID == "" || attribution.Slot == "" || !validBeneficiary(attribution.Beneficiary) {
		return ErrInvalidAttribution
	}
	return nil
}

func programHasSlot(program Program, slot string) bool {
	for _, rule := range program.Rules {
		if rule.Slot == slot {
			return true
		}
	}
	return false
}

func templateRule(template Template, slot string) (Rule, bool) {
	for _, rule := range template.Rules {
		if rule.Slot == slot {
			return rule, true
		}
	}
	return Rule{}, false
}

func ruleWithinCap(rule Rule, cap Rule) bool {
	if len(rule.Tiers) != len(cap.Tiers) {
		return false
	}
	tiers := sortedTiers(rule.Tiers)
	capTiers := sortedTiers(cap.Tiers)
	for index, tier := range tiers {
		capTier := capTiers[index]
		if tier.MinMinor != capTier.MinMinor || tier.MaxMinor != capTier.MaxMinor || tier.BasisPoints > capTier.BasisPoints || tier.FixedMinor > capTier.FixedMinor {
			return false
		}
	}
	return true
}

func sortedTiers(tiers []Tier) []Tier {
	cloned := append([]Tier(nil), tiers...)
	sort.Slice(cloned, func(i, j int) bool {
		left, right := cloned[i], cloned[j]
		if left.MinMinor != right.MinMinor {
			return left.MinMinor < right.MinMinor
		}
		if left.MaxMinor != right.MaxMinor {
			return left.MaxMinor < right.MaxMinor
		}
		if left.BasisPoints != right.BasisPoints {
			return left.BasisPoints < right.BasisPoints
		}
		return left.FixedMinor < right.FixedMinor
	})
	return cloned
}

func effectiveTemplate(template Template, program Program, attributions []Attribution) (Template, error) {
	effective := cloneTemplate(template)
	effective.Rules = cloneRules(program.Rules)
	bySlot := make(map[string]Attribution, len(attributions))
	for _, attribution := range attributions {
		if attribution.Active {
			bySlot[attribution.Slot] = attribution
		}
	}
	for index := range effective.Rules {
		if attribution, ok := bySlot[effective.Rules[index].Slot]; ok {
			effective.Rules[index].Beneficiary = attribution.Beneficiary
		}
	}
	return effective, nil
}

func attributionVersions(program Program, attributions []Attribution) map[string]int64 {
	versions := make(map[string]int64, len(program.Rules))
	for _, rule := range program.Rules {
		versions[rule.Slot] = 0
	}
	for _, attribution := range attributions {
		if _, tracked := versions[attribution.Slot]; tracked {
			versions[attribution.Slot] = attribution.Version
		}
	}
	return versions
}

func buildEventEntries(event CommissionEvent, program Program, template Template, calculations []Calculation, now time.Time) ([]Earning, []JournalEntry, []OutboxEvent) {
	availableAt := event.OccurredAt.Add(template.FreezePeriod)
	status := EarningStatusPending
	if !availableAt.After(now) {
		status = EarningStatusAvailable
	}
	earnings := make([]Earning, 0, len(calculations))
	journals := make([]JournalEntry, 0, len(calculations))
	outbox := make([]OutboxEvent, 0, len(calculations))
	for _, calculation := range calculations {
		id := earningID(event, program.ID, calculation.Slot, calculation.Beneficiary)
		earning := Earning{
			ID:              id,
			TenantID:        event.TenantID,
			ProgramID:       program.ID,
			TemplateID:      template.ID,
			TemplateVersion: template.Version,
			SourceType:      event.SourceType,
			SourceID:        event.SourceID,
			Slot:            calculation.Slot,
			Beneficiary:     calculation.Beneficiary,
			Amount:          calculation.Amount,
			Status:          status,
			AvailableAt:     availableAt,
			Version:         1,
			CreatedAt:       now,
			UpdatedAt:       now,
		}
		earnings = append(earnings, earning)
		journals = append(journals, JournalEntry{ID: journalID(earning.ID, JournalKindAccrual, earning.Version), TenantID: earning.TenantID, EarningID: earning.ID, Kind: JournalKindAccrual, Amount: earning.Amount, CreatedAt: now})
		if status == EarningStatusAvailable {
			journals = append(journals, JournalEntry{ID: journalID(earning.ID, JournalKindAvailable, earning.Version), TenantID: earning.TenantID, EarningID: earning.ID, Kind: JournalKindAvailable, Amount: earning.Amount, CreatedAt: now})
		}
		outbox = append(outbox, OutboxEvent{
			ID:          outboxID(earning.TenantID.String(), earning.ID, string(status), earning.Version),
			TenantID:    earning.TenantID,
			Type:        "commission.earning." + string(status),
			AggregateID: earning.ID,
			Payload: map[string]string{
				"earning_id": earning.ID,
				"program_id": program.ID,
				"status":     string(status),
				"currency":   earning.Amount.Currency,
			},
			CreatedAt: now,
		})
	}
	return earnings, journals, outbox
}

func cloneStrings(values []string) []string {
	if values == nil {
		return nil
	}
	cloned := make([]string, len(values))
	copy(cloned, values)
	return cloned
}

func cloneRules(rules []Rule) []Rule {
	if rules == nil {
		return nil
	}
	cloned := make([]Rule, len(rules))
	for index, rule := range rules {
		cloned[index] = cloneRule(rule)
	}
	return cloned
}

func cloneSettlement(settlement Settlement) Settlement {
	settlement.EarningIDs = cloneStrings(settlement.EarningIDs)
	return settlement
}

func sortedEarningIDs(ids []string) []string {
	cloned := cloneStrings(ids)
	sort.Strings(cloned)
	return cloned
}

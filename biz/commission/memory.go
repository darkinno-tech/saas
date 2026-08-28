package commission

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/darkinno-tech/saas/core/types"
)

var _ Store = (*MemoryStore)(nil)

// MemoryStore is a concurrency-safe reference implementation of Store. It is
// suitable for tests and local development; SQLStore supplies durable storage
// for production host applications.
type MemoryStore struct {
	mu           sync.RWMutex
	templates    map[templateKey]Template
	programs     map[programKey]Program
	attributions map[attributionKey]Attribution
	events       map[eventKey]recordedEvent
	earnings     map[string]Earning
	journals     []JournalEntry
	journalIDs   map[string]struct{}
	outbox       map[outboxKey]OutboxEvent
	settlements  map[settlementKey]Settlement
}

type templateKey struct {
	id      string
	version int64
}

type programKey struct {
	tenantID types.TenantID
	id       string
}

type attributionKey struct {
	tenantID  types.TenantID
	programID string
	slot      string
}

type eventKey struct {
	tenantID   types.TenantID
	sourceType string
	sourceID   string
}

type settlementKey struct {
	tenantID types.TenantID
	id       string
}

type outboxKey struct {
	tenantID types.TenantID
	id       string
}

type recordedEvent struct {
	fingerprint string
	decision    DecisionSnapshot
	earningIDs  []string
}

type earningTransition struct {
	earning Earning
	journal JournalEntry
	outbox  OutboxEvent
}

// NewMemoryStore creates an empty commission store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		templates:    map[templateKey]Template{},
		programs:     map[programKey]Program{},
		attributions: map[attributionKey]Attribution{},
		events:       map[eventKey]recordedEvent{},
		earnings:     map[string]Earning{},
		journalIDs:   map[string]struct{}{},
		outbox:       map[outboxKey]OutboxEvent{},
		settlements:  map[settlementKey]Settlement{},
	}
}

// CreateTemplate inserts an immutable rule version.
func (store *MemoryStore) CreateTemplate(ctx context.Context, template Template) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateNewTemplate(template); err != nil {
		return err
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	key := templateKey{id: template.ID, version: template.Version}
	if _, exists := store.templates[key]; exists {
		return ErrTemplateAlreadyExists
	}
	store.templates[key] = cloneTemplate(template)
	return nil
}

// GetTemplate returns the requested template version. A zero version selects
// the highest stored version for the identifier.
func (store *MemoryStore) GetTemplate(ctx context.Context, id string, version int64) (Template, error) {
	if err := ctx.Err(); err != nil {
		return Template{}, err
	}
	if id == "" || version < 0 {
		return Template{}, ErrInvalidTemplate
	}

	store.mu.RLock()
	defer store.mu.RUnlock()
	if version > 0 {
		template, ok := store.templates[templateKey{id: id, version: version}]
		if !ok {
			return Template{}, ErrTemplateNotFound
		}
		return cloneTemplate(template), nil
	}

	var selected Template
	for key, template := range store.templates {
		if key.id == id && (selected.Version == 0 || key.version > selected.Version) {
			selected = template
		}
	}
	if selected.Version == 0 {
		return Template{}, ErrTemplateNotFound
	}
	return cloneTemplate(selected), nil
}

// TransitionTemplate changes only lifecycle metadata; it never changes rules.
func (store *MemoryStore) TransitionTemplate(ctx context.Context, id string, version int64, action TemplateAction, now time.Time) (Template, error) {
	if err := ctx.Err(); err != nil {
		return Template{}, err
	}
	if id == "" || version <= 0 || now.IsZero() {
		return Template{}, ErrInvalidTemplate
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	key := templateKey{id: id, version: version}
	template, ok := store.templates[key]
	if !ok {
		return Template{}, ErrTemplateNotFound
	}
	next, err := nextTemplateStatus(template.Status, action)
	if err != nil {
		return Template{}, err
	}
	template.Status = next
	template.UpdatedAt = now
	store.templates[key] = cloneTemplate(template)
	return cloneTemplate(template), nil
}

// CreateProgram inserts an immutable tenant program.
func (store *MemoryStore) CreateProgram(ctx context.Context, program Program) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateNewProgram(program); err != nil {
		return err
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	key := programKey{tenantID: program.TenantID, id: program.ID}
	if _, exists := store.programs[key]; exists {
		return ErrProgramAlreadyExists
	}
	template, ok := store.templates[templateKey{id: program.TemplateID, version: program.TemplateVersion}]
	if !ok {
		return ErrTemplateNotFound
	}
	if err := validateProgramAgainstTemplate(program, template); err != nil {
		return err
	}
	store.programs[key] = cloneProgram(program)
	return nil
}

// GetProgram returns one tenant-scoped program.
func (store *MemoryStore) GetProgram(ctx context.Context, tenantID types.TenantID, id string) (Program, error) {
	if err := ctx.Err(); err != nil {
		return Program{}, err
	}
	if tenantID == "" || id == "" {
		return Program{}, ErrInvalidProgram
	}

	store.mu.RLock()
	defer store.mu.RUnlock()
	program, ok := store.programs[programKey{tenantID: tenantID, id: id}]
	if !ok {
		return Program{}, ErrProgramNotFound
	}
	return cloneProgram(program), nil
}

// TransitionProgram applies a compare-and-swap lifecycle change.
func (store *MemoryStore) TransitionProgram(ctx context.Context, tenantID types.TenantID, id string, expectedVersion int64, action ProgramAction, now time.Time) (Program, error) {
	if err := ctx.Err(); err != nil {
		return Program{}, err
	}
	if tenantID == "" || id == "" || expectedVersion <= 0 || now.IsZero() {
		return Program{}, ErrInvalidProgram
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	key := programKey{tenantID: tenantID, id: id}
	program, ok := store.programs[key]
	if !ok {
		return Program{}, ErrProgramNotFound
	}
	if program.Version != expectedVersion {
		return Program{}, ErrVersionConflict
	}
	next, err := nextProgramStatus(program.Status, action)
	if err != nil {
		return Program{}, err
	}
	if next == ProgramStatusActive {
		template, exists := store.templates[templateKey{id: program.TemplateID, version: program.TemplateVersion}]
		if !exists {
			return Program{}, ErrTemplateNotFound
		}
		if template.Status != TemplateStatusActive {
			return Program{}, ErrTemplateNotActive
		}
	}
	program.Status = next
	program.Version++
	program.UpdatedAt = now
	store.programs[key] = cloneProgram(program)
	return cloneProgram(program), nil
}

// SetAttribution creates or updates one program slot using optimistic locking.
func (store *MemoryStore) SetAttribution(ctx context.Context, attribution Attribution, expectedVersion int64) (Attribution, error) {
	if err := ctx.Err(); err != nil {
		return Attribution{}, err
	}
	if err := validateAttribution(attribution); err != nil || expectedVersion < 0 {
		return Attribution{}, ErrInvalidAttribution
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	key := attributionKey{tenantID: attribution.TenantID, programID: attribution.ProgramID, slot: attribution.Slot}
	current, exists := store.attributions[key]
	if !exists {
		if expectedVersion != 0 {
			return Attribution{}, ErrVersionConflict
		}
		attribution.Version = 1
		store.attributions[key] = cloneAttribution(attribution)
		return cloneAttribution(attribution), nil
	}
	if current.Version != expectedVersion {
		return Attribution{}, ErrVersionConflict
	}
	attribution.Version = current.Version + 1
	attribution.CreatedAt = current.CreatedAt
	store.attributions[key] = cloneAttribution(attribution)
	return cloneAttribution(attribution), nil
}

// ListAttributions returns slot-attributions ordered by slot.
func (store *MemoryStore) ListAttributions(ctx context.Context, tenantID types.TenantID, programID string) ([]Attribution, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if tenantID == "" || programID == "" {
		return nil, ErrInvalidAttribution
	}

	store.mu.RLock()
	defer store.mu.RUnlock()
	attributions := []Attribution{}
	for key, attribution := range store.attributions {
		if key.tenantID == tenantID && key.programID == programID {
			attributions = append(attributions, cloneAttribution(attribution))
		}
	}
	sort.Slice(attributions, func(i, j int) bool { return attributions[i].Slot < attributions[j].Slot })
	return attributions, nil
}

// CommitEvent atomically stores a source event, its earnings, journals, and
// outbox records. Repeating the same source facts for the same program returns
// its original earnings; changed facts or a different program report
// ErrEventConflict.
func (store *MemoryStore) CommitEvent(ctx context.Context, commit EventCommit) ([]Earning, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateEventCommit(commit); err != nil {
		return nil, err
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	key := eventKey{tenantID: commit.Event.TenantID, sourceType: commit.Event.SourceType, sourceID: commit.Event.SourceID}
	fingerprint := eventIdempotencyFingerprint(commit.Event, commit.ProgramID)
	if existing, ok := store.events[key]; ok {
		if existing.fingerprint != fingerprint {
			return nil, ErrEventConflict
		}
		return store.cloneEarningsLocked(existing.earningIDs)
	}
	if err := store.validateCommitVersionsLocked(commit); err != nil {
		return nil, err
	}

	for _, earning := range commit.Earnings {
		if _, exists := store.earnings[earning.ID]; exists {
			return nil, ErrEventConflict
		}
	}
	for _, outbox := range commit.Outbox {
		if _, exists := store.outbox[outboxKey{tenantID: outbox.TenantID, id: outbox.ID}]; exists {
			return nil, ErrEventConflict
		}
	}
	for _, journal := range commit.Journals {
		if _, exists := store.journalIDs[journal.ID]; exists {
			return nil, ErrEventConflict
		}
	}

	ids := make([]string, 0, len(commit.Earnings))
	for _, earning := range commit.Earnings {
		store.earnings[earning.ID] = cloneEarning(earning)
		ids = append(ids, earning.ID)
	}
	for _, journal := range commit.Journals {
		store.journals = append(store.journals, cloneJournal(journal))
		store.journalIDs[journal.ID] = struct{}{}
	}
	for _, outbox := range commit.Outbox {
		store.outbox[outboxKey{tenantID: outbox.TenantID, id: outbox.ID}] = cloneOutbox(outbox)
	}
	store.events[key] = recordedEvent{
		fingerprint: fingerprint,
		decision:    cloneDecisionSnapshot(decisionSnapshotFor(commit)),
		earningIDs:  cloneStrings(ids),
	}
	return store.cloneEarningsLocked(ids)
}

// GetEarning returns an earning within its tenant boundary.
func (store *MemoryStore) GetEarning(ctx context.Context, tenantID types.TenantID, id string) (Earning, error) {
	if err := ctx.Err(); err != nil {
		return Earning{}, err
	}
	if tenantID == "" || id == "" {
		return Earning{}, ErrInvalidEarning
	}

	store.mu.RLock()
	defer store.mu.RUnlock()
	earning, ok := store.earnings[id]
	if !ok || earning.TenantID != tenantID {
		return Earning{}, ErrEarningNotFound
	}
	return cloneEarning(earning), nil
}

// ListEarnings returns ordered tenant-scoped earnings.
func (store *MemoryStore) ListEarnings(ctx context.Context, tenantID types.TenantID, filter EarningFilter) ([]Earning, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if tenantID == "" || !filter.valid() {
		return nil, ErrInvalidEarningFilter
	}

	store.mu.RLock()
	defer store.mu.RUnlock()
	earnings := []Earning{}
	for _, earning := range store.earnings {
		if earning.TenantID != tenantID || !filter.matches(earning) {
			continue
		}
		earnings = append(earnings, cloneEarning(earning))
	}
	sort.Slice(earnings, func(i, j int) bool { return earnings[i].ID < earnings[j].ID })
	if filter.Cursor != "" {
		start := len(earnings)
		for index, earning := range earnings {
			if earning.ID > filter.Cursor {
				start = index
				break
			}
		}
		earnings = earnings[start:]
	}
	if filter.Limit > 0 && len(earnings) > filter.Limit {
		earnings = earnings[:filter.Limit]
	}
	return earnings, nil
}

// ListJournalEntries returns immutable entries for one tenant-scoped earning
// in creation order.
func (store *MemoryStore) ListJournalEntries(ctx context.Context, tenantID types.TenantID, earningID string) ([]JournalEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if tenantID == "" || earningID == "" {
		return nil, ErrInvalidEarning
	}

	store.mu.RLock()
	defer store.mu.RUnlock()
	entries := []JournalEntry{}
	for _, entry := range store.journals {
		if entry.TenantID == tenantID && entry.EarningID == earningID {
			entries = append(entries, cloneJournal(entry))
		}
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].CreatedAt.Before(entries[j].CreatedAt) })
	return entries, nil
}

// TransitionEarning changes an individually managed earning. Settlement
// actions are reserved for StartSettlement and FinishSettlement so a settling
// earning always has a durable settlement batch.
func (store *MemoryStore) TransitionEarning(ctx context.Context, tenantID types.TenantID, id string, expectedVersion int64, action EarningAction, now time.Time) (Earning, error) {
	if err := ctx.Err(); err != nil {
		return Earning{}, err
	}
	if tenantID == "" || id == "" || expectedVersion <= 0 || now.IsZero() || action == EarningActionMakeAvailable || action == EarningActionStartSettlement || action == EarningActionSettle || action == EarningActionRejectSettlement {
		return Earning{}, ErrInvalidEarning
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	earning, ok := store.earnings[id]
	if !ok || earning.TenantID != tenantID {
		return Earning{}, ErrEarningNotFound
	}
	if earning.Version != expectedVersion {
		return Earning{}, ErrVersionConflict
	}
	next, err := TransitionEarning(earning.Status, action)
	if err != nil {
		return Earning{}, err
	}
	earning.Status = next
	earning.Version++
	earning.UpdatedAt = now
	transition := newEarningTransition(earning, action, now)
	if err := store.validateEarningTransitionsLocked([]earningTransition{transition}); err != nil {
		return Earning{}, err
	}
	if err := store.applyEarningTransitionsLocked([]earningTransition{transition}); err != nil {
		return Earning{}, err
	}
	return cloneEarning(earning), nil
}

// MakeAvailableDue transitions at most limit due pending earnings to
// available, in available-at then ID order.
func (store *MemoryStore) MakeAvailableDue(ctx context.Context, now time.Time, limit int) ([]Earning, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if now.IsZero() || limit <= 0 {
		return nil, ErrInvalidEarning
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	due := make([]Earning, 0)
	for _, earning := range store.earnings {
		if earning.Status == EarningStatusPending && !earning.AvailableAt.After(now) {
			due = append(due, earning)
		}
	}
	sort.Slice(due, func(left, right int) bool {
		if !due[left].AvailableAt.Equal(due[right].AvailableAt) {
			return due[left].AvailableAt.Before(due[right].AvailableAt)
		}
		return due[left].ID < due[right].ID
	})
	if len(due) > limit {
		due = due[:limit]
	}
	ids := make([]string, 0, len(due))
	for _, earning := range due {
		ids = append(ids, earning.ID)
	}
	transitions := make([]earningTransition, 0, len(ids))
	for _, id := range ids {
		earning := store.earnings[id]
		next, err := TransitionEarning(earning.Status, EarningActionMakeAvailable)
		if err != nil {
			return nil, err
		}
		earning.Status = next
		earning.Version++
		earning.UpdatedAt = now
		transitions = append(transitions, newEarningTransition(earning, EarningActionMakeAvailable, now))
	}
	if err := store.validateEarningTransitionsLocked(transitions); err != nil {
		return nil, err
	}
	if err := store.applyEarningTransitionsLocked(transitions); err != nil {
		return nil, err
	}
	available := make([]Earning, 0, len(transitions))
	for _, transition := range transitions {
		available = append(available, cloneEarning(transition.earning))
	}
	return available, nil
}

// StartSettlement atomically claims a compatible set of available earnings.
func (store *MemoryStore) StartSettlement(ctx context.Context, settlement Settlement, expectedVersions map[string]int64) (Settlement, error) {
	if err := ctx.Err(); err != nil {
		return Settlement{}, err
	}
	if err := validateSettlement(settlement, expectedVersions); err != nil {
		return Settlement{}, err
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	key := settlementKey{tenantID: settlement.TenantID, id: settlement.ID}
	if _, exists := store.settlements[key]; exists {
		return Settlement{}, ErrSettlementAlreadyExists
	}
	var total int64
	for _, earningID := range sortedEarningIDs(settlement.EarningIDs) {
		earning, ok := store.earnings[earningID]
		if !ok || earning.TenantID != settlement.TenantID || earning.Status != EarningStatusAvailable || earning.Version != expectedVersions[earningID] || earning.Beneficiary != settlement.Beneficiary || earning.Amount.Currency != settlement.Amount.Currency {
			return Settlement{}, ErrEarningUnavailable
		}
		if earning.Amount.Minor > int64(^uint64(0)>>1)-total {
			return Settlement{}, ErrInvalidSettlement
		}
		total += earning.Amount.Minor
	}
	if total != settlement.Amount.Minor {
		return Settlement{}, ErrInvalidSettlement
	}
	submittedOutbox := settlementOutbox(settlement, SettlementStatusSubmitted, settlement.Version, settlement.CreatedAt)
	if err := store.validateOutboxEventsLocked([]OutboxEvent{submittedOutbox}); err != nil {
		return Settlement{}, err
	}

	for _, earningID := range settlement.EarningIDs {
		earning := store.earnings[earningID]
		next, err := TransitionEarning(earning.Status, EarningActionStartSettlement)
		if err != nil {
			return Settlement{}, err
		}
		earning.Status = next
		earning.Version++
		earning.UpdatedAt = settlement.CreatedAt
		store.earnings[earningID] = cloneEarning(earning)
	}
	store.settlements[key] = cloneSettlement(settlement)
	if err := store.appendOutboxLocked(submittedOutbox); err != nil {
		return Settlement{}, err
	}
	return cloneSettlement(settlement), nil
}

// GetSettlement returns a tenant-scoped settlement batch.
func (store *MemoryStore) GetSettlement(ctx context.Context, tenantID types.TenantID, id string) (Settlement, error) {
	if err := ctx.Err(); err != nil {
		return Settlement{}, err
	}
	if tenantID == "" || id == "" {
		return Settlement{}, ErrInvalidSettlement
	}

	store.mu.RLock()
	defer store.mu.RUnlock()
	settlement, ok := store.settlements[settlementKey{tenantID: tenantID, id: id}]
	if !ok {
		return Settlement{}, ErrSettlementNotFound
	}
	return cloneSettlement(settlement), nil
}

// FinishSettlement stores the host-verified outcome and releases or settles
// all batch earnings in the same critical section.
func (store *MemoryStore) FinishSettlement(ctx context.Context, tenantID types.TenantID, id string, expectedVersion int64, settled bool, providerReference string, now time.Time) (Settlement, error) {
	if err := ctx.Err(); err != nil {
		return Settlement{}, err
	}
	if tenantID == "" || id == "" || expectedVersion <= 0 || now.IsZero() {
		return Settlement{}, ErrInvalidSettlement
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	key := settlementKey{tenantID: tenantID, id: id}
	batch, ok := store.settlements[key]
	if !ok {
		return Settlement{}, ErrSettlementNotFound
	}
	desiredStatus := SettlementStatusRejected
	if settled {
		desiredStatus = SettlementStatusSettled
	}
	if batch.Status != SettlementStatusSubmitted {
		if batch.Status == desiredStatus && batch.ProviderReference == providerReference {
			return cloneSettlement(batch), nil
		}
		return Settlement{}, ErrSettlementOutcomeConflict
	}
	if batch.Version != expectedVersion {
		return Settlement{}, ErrVersionConflict
	}

	transitions := make([]earningTransition, 0, len(batch.EarningIDs))
	for _, earningID := range batch.EarningIDs {
		earning, ok := store.earnings[earningID]
		if !ok || earning.TenantID != tenantID || earning.Status != EarningStatusSettling {
			return Settlement{}, ErrEarningUnavailable
		}
		action := EarningActionRejectSettlement
		if settled {
			action = EarningActionSettle
		}
		next, err := TransitionEarning(earning.Status, action)
		if err != nil {
			return Settlement{}, err
		}
		earning.Status = next
		earning.Version++
		earning.UpdatedAt = now
		transitions = append(transitions, newEarningTransition(earning, action, now))
	}
	if err := store.validateEarningTransitionsLocked(transitions); err != nil {
		return Settlement{}, err
	}
	completed := cloneSettlement(batch)
	completed.Status = desiredStatus
	completed.ProviderReference = providerReference
	completed.Version++
	completed.UpdatedAt = now
	completedOutbox := settlementOutbox(completed, completed.Status, completed.Version, now)
	if err := store.validateOutboxEventsLocked([]OutboxEvent{completedOutbox}); err != nil {
		return Settlement{}, err
	}
	if err := store.applyEarningTransitionsLocked(transitions); err != nil {
		return Settlement{}, err
	}
	store.settlements[key] = completed
	if err := store.appendOutboxLocked(completedOutbox); err != nil {
		return Settlement{}, err
	}
	return cloneSettlement(completed), nil
}

// ListOutbox returns ordered host-deliverable events for one tenant.
func (store *MemoryStore) ListOutbox(ctx context.Context, tenantID types.TenantID, filter OutboxFilter) ([]OutboxEvent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if tenantID == "" || !filter.valid() {
		return nil, ErrInvalidOutboxFilter
	}

	store.mu.RLock()
	defer store.mu.RUnlock()
	events := []OutboxEvent{}
	for _, event := range store.outbox {
		if event.TenantID != tenantID || (filter.UnpublishedOnly && event.PublishedAt != nil) {
			continue
		}
		events = append(events, cloneOutbox(event))
	}
	sort.Slice(events, func(i, j int) bool {
		if events[i].CreatedAt.Equal(events[j].CreatedAt) {
			return events[i].ID < events[j].ID
		}
		return events[i].CreatedAt.Before(events[j].CreatedAt)
	})
	if !filter.Cursor.empty() {
		start := len(events)
		for index, event := range events {
			if outboxEventAfterCursor(event, filter.Cursor) {
				start = index
				break
			}
		}
		events = events[start:]
	}
	if filter.Limit > 0 && len(events) > filter.Limit {
		events = events[:filter.Limit]
	}
	return events, nil
}

// MarkOutboxPublished marks an event published once. It is idempotent for the
// same event and does not permit a caller to change the original payload.
func (store *MemoryStore) MarkOutboxPublished(ctx context.Context, tenantID types.TenantID, id string, publishedAt time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if tenantID == "" || id == "" || publishedAt.IsZero() {
		return ErrInvalidOutbox
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	key := outboxKey{tenantID: tenantID, id: id}
	event, ok := store.outbox[key]
	if !ok {
		return ErrOutboxNotFound
	}
	if event.PublishedAt == nil {
		value := publishedAt
		event.PublishedAt = &value
		store.outbox[key] = cloneOutbox(event)
	}
	return nil
}

func (store *MemoryStore) validateCommitVersionsLocked(commit EventCommit) error {
	program, ok := store.programs[programKey{tenantID: commit.Event.TenantID, id: commit.ProgramID}]
	if !ok {
		return ErrProgramNotFound
	}
	if program.Version != commit.ProgramVersion {
		return ErrVersionConflict
	}
	if program.Status != ProgramStatusActive {
		return ErrProgramNotActive
	}
	if program.TemplateID != commit.TemplateID || program.TemplateVersion != commit.TemplateVersion {
		return ErrVersionConflict
	}
	for _, rule := range program.Rules {
		expectedVersion, tracked := commit.AttributionVersions[rule.Slot]
		if !tracked {
			return ErrVersionConflict
		}
		currentVersion := int64(0)
		if attribution, exists := store.attributions[attributionKey{tenantID: commit.Event.TenantID, programID: program.ID, slot: rule.Slot}]; exists {
			currentVersion = attribution.Version
		}
		if currentVersion != expectedVersion {
			return ErrVersionConflict
		}
	}
	template, ok := store.templates[templateKey{id: commit.TemplateID, version: commit.TemplateVersion}]
	if !ok {
		return ErrTemplateNotFound
	}
	if template.Status != TemplateStatusActive {
		return ErrTemplateNotActive
	}
	return nil
}

func (store *MemoryStore) cloneEarningsLocked(ids []string) ([]Earning, error) {
	earnings := make([]Earning, 0, len(ids))
	for _, id := range ids {
		earning, ok := store.earnings[id]
		if !ok {
			return nil, ErrInvalidEarning
		}
		earnings = append(earnings, cloneEarning(earning))
	}
	return earnings, nil
}

func newEarningTransition(earning Earning, action EarningAction, now time.Time) earningTransition {
	kind := JournalKindAvailable
	switch action {
	case EarningActionHold:
		kind = JournalKindHeld
	case EarningActionReverse:
		kind = JournalKindReversal
		if earning.Status == EarningStatusRecoveryDue {
			kind = JournalKindRecovery
		}
	case EarningActionSettle:
		kind = JournalKindSettlement
	}
	return earningTransition{
		earning: earning,
		journal: JournalEntry{
			ID:        journalID(earning.ID, kind, earning.Version),
			TenantID:  earning.TenantID,
			EarningID: earning.ID,
			Kind:      kind,
			Amount:    earning.Amount,
			CreatedAt: now,
		},
		outbox: OutboxEvent{
			ID:          outboxID(earning.TenantID.String(), earning.ID, "commission.earning."+string(earning.Status), earning.Version),
			TenantID:    earning.TenantID,
			Type:        "commission.earning." + string(earning.Status),
			AggregateID: earning.ID,
			Payload: map[string]string{
				"earning_id": earning.ID,
				"status":     string(earning.Status),
			},
			CreatedAt: now,
		},
	}
}

func (store *MemoryStore) validateEarningTransitionsLocked(transitions []earningTransition) error {
	journalIDs := make(map[string]struct{}, len(transitions))
	outboxEvents := make([]OutboxEvent, 0, len(transitions))
	for _, transition := range transitions {
		if _, exists := store.journalIDs[transition.journal.ID]; exists {
			return ErrEventConflict
		}
		if _, exists := journalIDs[transition.journal.ID]; exists {
			return ErrEventConflict
		}
		journalIDs[transition.journal.ID] = struct{}{}
		outboxEvents = append(outboxEvents, transition.outbox)
	}
	return store.validateOutboxEventsLocked(outboxEvents)
}

func (store *MemoryStore) applyEarningTransitionsLocked(transitions []earningTransition) error {
	for _, transition := range transitions {
		store.earnings[transition.earning.ID] = cloneEarning(transition.earning)
	}
	for _, transition := range transitions {
		if err := store.appendEarningTransitionLocked(transition); err != nil {
			return err
		}
	}
	return nil
}

func (store *MemoryStore) appendEarningTransitionLocked(transition earningTransition) error {
	store.journals = append(store.journals, cloneJournal(transition.journal))
	store.journalIDs[transition.journal.ID] = struct{}{}
	return store.appendOutboxLocked(transition.outbox)
}

func (store *MemoryStore) validateOutboxEventsLocked(events []OutboxEvent) error {
	keys := make(map[outboxKey]struct{}, len(events))
	for _, event := range events {
		key := outboxKey{tenantID: event.TenantID, id: event.ID}
		if _, exists := store.outbox[key]; exists {
			return ErrEventConflict
		}
		if _, exists := keys[key]; exists {
			return ErrEventConflict
		}
		keys[key] = struct{}{}
	}
	return nil
}

func (store *MemoryStore) appendOutboxLocked(event OutboxEvent) error {
	key := outboxKey{tenantID: event.TenantID, id: event.ID}
	if _, exists := store.outbox[key]; exists {
		return ErrEventConflict
	}
	store.outbox[key] = cloneOutbox(event)
	return nil
}

func settlementOutbox(settlement Settlement, status SettlementStatus, version int64, createdAt time.Time) OutboxEvent {
	eventType := "commission.settlement." + string(status)
	payload := map[string]string{"settlement_id": settlement.ID}
	if status == SettlementStatusSubmitted {
		payload["currency"] = settlement.Amount.Currency
	} else {
		payload["status"] = string(status)
	}
	return OutboxEvent{
		ID:          outboxID(settlement.TenantID.String(), settlement.ID, eventType, version),
		TenantID:    settlement.TenantID,
		Type:        eventType,
		AggregateID: settlement.ID,
		Payload:     payload,
		CreatedAt:   createdAt,
	}
}

func validateNewProgram(program Program) error {
	if program.ID == "" || program.TenantID == "" || program.TemplateID == "" || program.TemplateVersion <= 0 || program.Status != ProgramStatusDraft || program.Version != 1 || program.CreatedAt.IsZero() || program.UpdatedAt.IsZero() || len(program.Rules) == 0 {
		return ErrInvalidProgram
	}
	for _, rule := range program.Rules {
		if err := validateRule(rule, true); err != nil {
			return ErrInvalidProgram
		}
	}
	return nil
}

func validateEventCommit(commit EventCommit) error {
	if err := validateCommissionEvent(commit.Event); err != nil {
		return err
	}
	emptyFinancialEntries := len(commit.Earnings) == 0 && len(commit.Journals) == 0 && len(commit.Outbox) == 0
	if commit.ProgramID == "" || commit.TemplateID == "" || commit.ProgramVersion <= 0 || commit.TemplateVersion <= 0 || commit.AttributionVersions == nil || (!emptyFinancialEntries && (len(commit.Earnings) == 0 || len(commit.Journals) == 0 || len(commit.Outbox) == 0)) {
		return ErrInvalidEvent
	}
	for _, earning := range commit.Earnings {
		if err := validateStoredEarning(earning); err != nil || earning.TenantID != commit.Event.TenantID || earning.ProgramID != commit.ProgramID || earning.TemplateID != commit.TemplateID || earning.TemplateVersion != commit.TemplateVersion || earning.SourceType != commit.Event.SourceType || earning.SourceID != commit.Event.SourceID {
			return ErrInvalidEarning
		}
	}
	earningIDs := make(map[string]struct{}, len(commit.Earnings))
	for _, earning := range commit.Earnings {
		if _, exists := earningIDs[earning.ID]; exists {
			return ErrInvalidEarning
		}
		earningIDs[earning.ID] = struct{}{}
	}
	journalIDs := make(map[string]struct{}, len(commit.Journals))
	for _, journal := range commit.Journals {
		if journal.ID == "" || journal.TenantID != commit.Event.TenantID || !validJournalKind(journal.Kind) || !validAmount(journal.Amount) || journal.CreatedAt.IsZero() {
			return ErrInvalidEvent
		}
		if _, exists := earningIDs[journal.EarningID]; !exists {
			return ErrInvalidEvent
		}
		if _, exists := journalIDs[journal.ID]; exists {
			return ErrInvalidEvent
		}
		journalIDs[journal.ID] = struct{}{}
	}
	outboxIDs := make(map[string]struct{}, len(commit.Outbox))
	for _, outbox := range commit.Outbox {
		if outbox.ID == "" || outbox.TenantID != commit.Event.TenantID || outbox.Type == "" || outbox.AggregateID == "" || outbox.CreatedAt.IsZero() {
			return ErrInvalidEvent
		}
		if _, exists := outboxIDs[outbox.ID]; exists {
			return ErrInvalidEvent
		}
		outboxIDs[outbox.ID] = struct{}{}
	}
	return nil
}

func validateStoredEarning(earning Earning) error {
	if earning.ID == "" || earning.TenantID == "" || earning.ProgramID == "" || earning.TemplateID == "" || earning.TemplateVersion <= 0 || earning.SourceType == "" || earning.SourceID == "" || earning.Slot == "" || !validBeneficiary(earning.Beneficiary) || !validAmount(earning.Amount) || !validEarningStatus(earning.Status) || earning.AvailableAt.IsZero() || earning.Version <= 0 || earning.CreatedAt.IsZero() || earning.UpdatedAt.IsZero() {
		return ErrInvalidEarning
	}
	return nil
}

func validateSettlement(settlement Settlement, expectedVersions map[string]int64) error {
	if settlement.ID == "" || settlement.TenantID == "" || !validBeneficiary(settlement.Beneficiary) || !validAmount(settlement.Amount) || settlement.Amount.Minor <= 0 || settlement.Status != SettlementStatusSubmitted || settlement.Version <= 0 || settlement.CreatedAt.IsZero() || settlement.UpdatedAt.IsZero() || len(settlement.EarningIDs) == 0 || len(settlement.EarningIDs) != len(expectedVersions) {
		return ErrInvalidSettlement
	}
	seen := map[string]struct{}{}
	for _, id := range settlement.EarningIDs {
		if id == "" || expectedVersions[id] <= 0 {
			return ErrInvalidSettlement
		}
		if _, exists := seen[id]; exists {
			return ErrInvalidSettlement
		}
		seen[id] = struct{}{}
	}
	return nil
}

func (filter EarningFilter) valid() bool {
	if filter.Limit < 0 {
		return false
	}
	if filter.Beneficiary != nil && !validBeneficiary(*filter.Beneficiary) {
		return false
	}
	for _, status := range filter.Statuses {
		if !validEarningStatus(status) {
			return false
		}
	}
	return true
}

func (filter EarningFilter) matches(earning Earning) bool {
	if filter.ProgramID != "" && earning.ProgramID != filter.ProgramID {
		return false
	}
	if filter.Beneficiary != nil && earning.Beneficiary != *filter.Beneficiary {
		return false
	}
	if len(filter.Statuses) == 0 {
		return true
	}
	for _, status := range filter.Statuses {
		if earning.Status == status {
			return true
		}
	}
	return false
}

func (filter OutboxFilter) valid() bool {
	return filter.Limit >= 0 && (filter.Cursor.CreatedAt.IsZero() == (filter.Cursor.ID == ""))
}

func (cursor OutboxCursor) empty() bool {
	return cursor.CreatedAt.IsZero() && cursor.ID == ""
}

func outboxEventAfterCursor(event OutboxEvent, cursor OutboxCursor) bool {
	if cursor.empty() {
		return true
	}
	if event.CreatedAt.After(cursor.CreatedAt) {
		return true
	}
	return event.CreatedAt.Equal(cursor.CreatedAt) && event.ID > cursor.ID
}

func validJournalKind(kind JournalKind) bool {
	switch kind {
	case JournalKindAccrual, JournalKindAvailable, JournalKindHeld, JournalKindSettlement, JournalKindReversal, JournalKindRecovery:
		return true
	default:
		return false
	}
}

func cloneProgram(program Program) Program {
	program.Rules = cloneRules(program.Rules)
	return program
}

func cloneAttribution(attribution Attribution) Attribution {
	return attribution
}

func cloneEarning(earning Earning) Earning {
	return earning
}

func cloneJournal(journal JournalEntry) JournalEntry {
	return journal
}

func cloneOutbox(event OutboxEvent) OutboxEvent {
	if event.Payload != nil {
		payload := make(map[string]string, len(event.Payload))
		for key, value := range event.Payload {
			payload[key] = value
		}
		event.Payload = payload
	}
	if event.PublishedAt != nil {
		publishedAt := *event.PublishedAt
		event.PublishedAt = &publishedAt
	}
	return event
}

package commission

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/im10furry/saas/core/types"
	"github.com/im10furry/saas/internal/sqlutil"
)

const commissionMutationMaxAttempts = 4

// SQLDialect controls SQL placeholder rendering for SQLStore.
type SQLDialect = sqlutil.Dialect

const (
	// SQLDialectMySQL uses question-mark placeholders and is the default.
	SQLDialectMySQL = sqlutil.DialectMySQL

	// SQLDialectSQLite uses question-mark placeholders.
	SQLDialectSQLite = sqlutil.DialectSQLite

	// SQLDialectPostgres uses numbered placeholders.
	SQLDialectPostgres = sqlutil.DialectPostgres
)

var _ Store = (*SQLStore)(nil)

// SQLStore is the durable, host-schema-backed Store implementation. It uses
// database/sql only and never exposes a transaction to callers. See
// SQLTableNames for the required host-managed schema.
type SQLStore struct {
	db      *sql.DB
	tables  SQLTableNames
	dialect SQLDialect
}

// SQLStoreOption configures SQLStore.
type SQLStoreOption func(*SQLStore) error

// WithTableNames replaces every host-managed commission table name. Partial
// table sets are rejected so one accidental empty field cannot produce SQL
// against an unintended table.
func WithTableNames(names SQLTableNames) SQLStoreOption {
	return func(store *SQLStore) error {
		if err := validateSQLTableNames(names); err != nil {
			return err
		}
		store.tables = names
		return nil
	}
}

// WithSQLTableNames is an explicit alias for WithTableNames.
func WithSQLTableNames(names SQLTableNames) SQLStoreOption {
	return WithTableNames(names)
}

// WithSQLDialect configures SQL placeholder rendering.
func WithSQLDialect(dialect SQLDialect) SQLStoreOption {
	return func(store *SQLStore) error {
		normalized, ok := sqlutil.NormalizeDialect(dialect)
		if !ok {
			return fmt.Errorf("%w: %s", ErrUnsupportedSQLDialect, dialect)
		}
		store.dialect = normalized
		return nil
	}
}

// NewSQLStore creates a SQL-backed commission store. It validates only the
// configuration; table creation and migration remain the host's responsibility.
func NewSQLStore(db *sql.DB, opts ...SQLStoreOption) (*SQLStore, error) {
	if db == nil {
		return nil, ErrNilDB
	}

	store := &SQLStore{
		db:      db,
		tables:  defaultSQLTableNames(),
		dialect: SQLDialectMySQL,
	}
	if err := validateSQLTableNames(store.tables); err != nil {
		return nil, err
	}
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		if err := opt(store); err != nil {
			return nil, err
		}
	}
	return store, nil
}

type sqlQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type sqlExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

type sqlRowScanner interface {
	Scan(...any) error
}

func (store *SQLStore) placeholder(index int) string {
	return sqlutil.Placeholder(store.dialect, index)
}

func (store *SQLStore) placeholders(count int, start int) string {
	return sqlutil.Placeholders(store.dialect, count, start)
}

func (store *SQLStore) forUpdate() string {
	if store.dialect == SQLDialectSQLite {
		return ""
	}
	return " FOR UPDATE"
}

func (store *SQLStore) inSerializableTx(ctx context.Context, fn func(*sql.Tx) error) (err error) {
	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, tx.Rollback())
		}
	}()

	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func retryCommissionMutation[T any](ctx context.Context, mutate func() (T, error)) (T, error) {
	var (
		value T
		err   error
	)
	for attempt := 0; attempt < commissionMutationMaxAttempts; attempt++ {
		value, err = mutate()
		if !sqlutil.IsRetryableTransactionError(err) {
			return value, err
		}
		if attempt == commissionMutationMaxAttempts-1 {
			return value, err
		}
		if err = waitForCommissionMutationRetry(ctx, attempt); err != nil {
			var zero T
			return zero, err
		}
	}
	return value, err
}

func waitForCommissionMutationRetry(ctx context.Context, attempt int) error {
	shift := attempt
	if shift > 4 {
		shift = 4
	}
	timer := time.NewTimer(time.Millisecond << shift)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func marshalPayload(value any) (string, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

const (
	templateSelectColumns = "id, version, status, created_at, updated_at, payload"
	programSelectColumns  = "tenant_id, id, template_id, template_version, status, version, created_at, updated_at, payload"
	attributionColumns    = "tenant_id, program_id, slot, beneficiary_kind, beneficiary_id, active, version, created_at, updated_at, payload"
	earningSelectColumns  = "tenant_id, id, program_id, template_id, template_version, source_type, source_id, slot, beneficiary_kind, beneficiary_id, currency, amount_minor, status, available_at, version, created_at, updated_at, payload"
	journalSelectColumns  = "tenant_id, id, earning_id, kind, currency, amount_minor, created_at, payload"
	outboxSelectColumns   = "tenant_id, id, type, aggregate_id, created_at, published_at, payload"
	settlementColumns     = "tenant_id, id, beneficiary_kind, beneficiary_id, currency, amount_minor, status, provider_reference, version, created_at, updated_at, payload"
)

func scanTemplate(row sqlRowScanner) (Template, error) {
	var (
		id        string
		version   int64
		status    string
		createdAt time.Time
		updatedAt time.Time
		payload   []byte
	)
	if err := row.Scan(&id, &version, &status, &createdAt, &updatedAt, &payload); err != nil {
		return Template{}, err
	}
	var template Template
	if err := json.Unmarshal(payload, &template); err != nil {
		return Template{}, fmt.Errorf("%w: decode template payload: %v", ErrInvalidTemplate, err)
	}
	template.ID = id
	template.Version = version
	template.Status = TemplateStatus(status)
	template.CreatedAt = createdAt
	template.UpdatedAt = updatedAt
	if err := validateTemplate(template); err != nil {
		return Template{}, err
	}
	return template, nil
}

func scanProgram(row sqlRowScanner) (Program, error) {
	var (
		tenantID        string
		id              string
		templateID      string
		templateVersion int64
		status          string
		version         int64
		createdAt       time.Time
		updatedAt       time.Time
		payload         []byte
	)
	if err := row.Scan(&tenantID, &id, &templateID, &templateVersion, &status, &version, &createdAt, &updatedAt, &payload); err != nil {
		return Program{}, err
	}
	var program Program
	if err := json.Unmarshal(payload, &program); err != nil {
		return Program{}, fmt.Errorf("%w: decode program payload: %v", ErrInvalidProgram, err)
	}
	program.TenantID = types.TenantID(tenantID)
	program.ID = id
	program.TemplateID = templateID
	program.TemplateVersion = templateVersion
	program.Status = ProgramStatus(status)
	program.Version = version
	program.CreatedAt = createdAt
	program.UpdatedAt = updatedAt
	if err := validatePersistedProgram(program); err != nil {
		return Program{}, err
	}
	return program, nil
}

func scanAttribution(row sqlRowScanner) (Attribution, error) {
	var (
		tenantID        string
		programID       string
		slot            string
		beneficiaryKind string
		beneficiaryID   string
		active          bool
		version         int64
		createdAt       time.Time
		updatedAt       time.Time
		payload         []byte
	)
	if err := row.Scan(&tenantID, &programID, &slot, &beneficiaryKind, &beneficiaryID, &active, &version, &createdAt, &updatedAt, &payload); err != nil {
		return Attribution{}, err
	}
	var attribution Attribution
	if err := json.Unmarshal(payload, &attribution); err != nil {
		return Attribution{}, fmt.Errorf("%w: decode attribution payload: %v", ErrInvalidAttribution, err)
	}
	attribution.TenantID = types.TenantID(tenantID)
	attribution.ProgramID = programID
	attribution.Slot = slot
	attribution.Beneficiary = BeneficiaryRef{Kind: BeneficiaryKind(beneficiaryKind), ID: beneficiaryID}
	attribution.Active = active
	attribution.Version = version
	attribution.CreatedAt = createdAt
	attribution.UpdatedAt = updatedAt
	if err := validateAttribution(attribution); err != nil || attribution.Version <= 0 {
		return Attribution{}, ErrInvalidAttribution
	}
	return attribution, nil
}

func scanEarning(row sqlRowScanner) (Earning, error) {
	var (
		tenantID        string
		id              string
		programID       string
		templateID      string
		templateVersion int64
		sourceType      string
		sourceID        string
		slot            string
		beneficiaryKind string
		beneficiaryID   string
		currency        string
		amountMinor     int64
		status          string
		availableAt     time.Time
		version         int64
		createdAt       time.Time
		updatedAt       time.Time
		payload         []byte
	)
	if err := row.Scan(&tenantID, &id, &programID, &templateID, &templateVersion, &sourceType, &sourceID, &slot, &beneficiaryKind, &beneficiaryID, &currency, &amountMinor, &status, &availableAt, &version, &createdAt, &updatedAt, &payload); err != nil {
		return Earning{}, err
	}
	var earning Earning
	if err := json.Unmarshal(payload, &earning); err != nil {
		return Earning{}, fmt.Errorf("%w: decode earning payload: %v", ErrInvalidEarning, err)
	}
	earning.TenantID = types.TenantID(tenantID)
	earning.ID = id
	earning.ProgramID = programID
	earning.TemplateID = templateID
	earning.TemplateVersion = templateVersion
	earning.SourceType = sourceType
	earning.SourceID = sourceID
	earning.Slot = slot
	earning.Beneficiary = BeneficiaryRef{Kind: BeneficiaryKind(beneficiaryKind), ID: beneficiaryID}
	earning.Amount = Amount{Currency: currency, Minor: amountMinor}
	earning.Status = EarningStatus(status)
	earning.AvailableAt = availableAt
	earning.Version = version
	earning.CreatedAt = createdAt
	earning.UpdatedAt = updatedAt
	if err := validateStoredEarning(earning); err != nil {
		return Earning{}, err
	}
	return earning, nil
}

func scanJournal(row sqlRowScanner) (JournalEntry, error) {
	var (
		tenantID    string
		id          string
		earningID   string
		kind        string
		currency    string
		amountMinor int64
		createdAt   time.Time
		payload     []byte
	)
	if err := row.Scan(&tenantID, &id, &earningID, &kind, &currency, &amountMinor, &createdAt, &payload); err != nil {
		return JournalEntry{}, err
	}
	var journal JournalEntry
	if err := json.Unmarshal(payload, &journal); err != nil {
		return JournalEntry{}, fmt.Errorf("%w: decode journal payload: %v", ErrInvalidEvent, err)
	}
	journal.TenantID = types.TenantID(tenantID)
	journal.ID = id
	journal.EarningID = earningID
	journal.Kind = JournalKind(kind)
	journal.Amount = Amount{Currency: currency, Minor: amountMinor}
	journal.CreatedAt = createdAt
	if journal.ID == "" || journal.TenantID == "" || journal.EarningID == "" || !validJournalKind(journal.Kind) || !validAmount(journal.Amount) || journal.CreatedAt.IsZero() {
		return JournalEntry{}, ErrInvalidEvent
	}
	return journal, nil
}

func scanOutbox(row sqlRowScanner) (OutboxEvent, error) {
	var (
		tenantID    string
		id          string
		eventType   string
		aggregateID string
		createdAt   time.Time
		publishedAt sql.NullTime
		payload     []byte
	)
	if err := row.Scan(&tenantID, &id, &eventType, &aggregateID, &createdAt, &publishedAt, &payload); err != nil {
		return OutboxEvent{}, err
	}
	var event OutboxEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		return OutboxEvent{}, fmt.Errorf("%w: decode outbox payload: %v", ErrInvalidOutbox, err)
	}
	event.TenantID = types.TenantID(tenantID)
	event.ID = id
	event.Type = eventType
	event.AggregateID = aggregateID
	event.CreatedAt = createdAt
	if publishedAt.Valid {
		value := publishedAt.Time
		event.PublishedAt = &value
	} else {
		event.PublishedAt = nil
	}
	if event.ID == "" || event.TenantID == "" || event.Type == "" || event.AggregateID == "" || event.CreatedAt.IsZero() {
		return OutboxEvent{}, ErrInvalidOutbox
	}
	return event, nil
}

func scanSettlement(row sqlRowScanner) (Settlement, error) {
	var (
		tenantID        string
		id              string
		beneficiaryKind string
		beneficiaryID   string
		currency        string
		amountMinor     int64
		status          string
		providerRef     string
		version         int64
		createdAt       time.Time
		updatedAt       time.Time
		payload         []byte
	)
	if err := row.Scan(&tenantID, &id, &beneficiaryKind, &beneficiaryID, &currency, &amountMinor, &status, &providerRef, &version, &createdAt, &updatedAt, &payload); err != nil {
		return Settlement{}, err
	}
	var settlement Settlement
	if err := json.Unmarshal(payload, &settlement); err != nil {
		return Settlement{}, fmt.Errorf("%w: decode settlement payload: %v", ErrInvalidSettlement, err)
	}
	settlement.TenantID = types.TenantID(tenantID)
	settlement.ID = id
	settlement.Beneficiary = BeneficiaryRef{Kind: BeneficiaryKind(beneficiaryKind), ID: beneficiaryID}
	settlement.Amount = Amount{Currency: currency, Minor: amountMinor}
	settlement.Status = SettlementStatus(status)
	settlement.ProviderReference = providerRef
	settlement.Version = version
	settlement.CreatedAt = createdAt
	settlement.UpdatedAt = updatedAt
	if settlement.ID == "" || settlement.TenantID == "" || !validBeneficiary(settlement.Beneficiary) || !validAmount(settlement.Amount) || settlement.Amount.Minor <= 0 || settlement.Version <= 0 || settlement.CreatedAt.IsZero() || settlement.UpdatedAt.IsZero() || len(settlement.EarningIDs) == 0 {
		return Settlement{}, ErrInvalidSettlement
	}
	return settlement, nil
}

func (store *SQLStore) insertTemplate(ctx context.Context, execer sqlExecer, template Template) error {
	payload, err := marshalPayload(template)
	if err != nil {
		return err
	}
	query := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", store.tables.Templates, templateSelectColumns, store.placeholders(6, 1))
	_, err = execer.ExecContext(ctx, query, template.ID, template.Version, string(template.Status), template.CreatedAt, template.UpdatedAt, payload)
	return err
}

func (store *SQLStore) insertProgram(ctx context.Context, execer sqlExecer, program Program) error {
	payload, err := marshalPayload(program)
	if err != nil {
		return err
	}
	query := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", store.tables.Programs, programSelectColumns, store.placeholders(9, 1))
	_, err = execer.ExecContext(ctx, query, program.TenantID.String(), program.ID, program.TemplateID, program.TemplateVersion, string(program.Status), program.Version, program.CreatedAt, program.UpdatedAt, payload)
	return err
}

func (store *SQLStore) insertAttribution(ctx context.Context, execer sqlExecer, attribution Attribution) error {
	payload, err := marshalPayload(attribution)
	if err != nil {
		return err
	}
	query := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", store.tables.Attributions, attributionColumns, store.placeholders(10, 1))
	_, err = execer.ExecContext(ctx, query, attribution.TenantID.String(), attribution.ProgramID, attribution.Slot, string(attribution.Beneficiary.Kind), attribution.Beneficiary.ID, attribution.Active, attribution.Version, attribution.CreatedAt, attribution.UpdatedAt, payload)
	return err
}

func (store *SQLStore) insertEvent(ctx context.Context, execer sqlExecer, commit EventCommit, fingerprint string) error {
	payload, err := marshalPayload(eventRecordPayload{
		Event:    cloneCommissionEvent(commit.Event),
		Decision: decisionSnapshotFor(commit),
	})
	if err != nil {
		return err
	}
	query := fmt.Sprintf(
		"INSERT INTO %s (tenant_id, source_type, source_id, fingerprint, program_id, program_version, template_id, template_version, occurred_at, payload) VALUES (%s)",
		store.tables.Events,
		store.placeholders(10, 1),
	)
	_, err = execer.ExecContext(ctx, query, commit.Event.TenantID.String(), commit.Event.SourceType, commit.Event.SourceID, fingerprint, commit.ProgramID, commit.ProgramVersion, commit.TemplateID, commit.TemplateVersion, commit.Event.OccurredAt, payload)
	return err
}

func (store *SQLStore) insertEarning(ctx context.Context, execer sqlExecer, earning Earning) error {
	payload, err := marshalPayload(earning)
	if err != nil {
		return err
	}
	query := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", store.tables.Earnings, earningSelectColumns, store.placeholders(18, 1))
	_, err = execer.ExecContext(ctx, query,
		earning.TenantID.String(), earning.ID, earning.ProgramID, earning.TemplateID, earning.TemplateVersion,
		earning.SourceType, earning.SourceID, earning.Slot, string(earning.Beneficiary.Kind), earning.Beneficiary.ID,
		earning.Amount.Currency, earning.Amount.Minor, string(earning.Status), earning.AvailableAt, earning.Version,
		earning.CreatedAt, earning.UpdatedAt, payload,
	)
	return err
}

func (store *SQLStore) insertJournal(ctx context.Context, execer sqlExecer, journal JournalEntry) error {
	payload, err := marshalPayload(journal)
	if err != nil {
		return err
	}
	query := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", store.tables.Journals, journalSelectColumns, store.placeholders(8, 1))
	_, err = execer.ExecContext(ctx, query, journal.TenantID.String(), journal.ID, journal.EarningID, string(journal.Kind), journal.Amount.Currency, journal.Amount.Minor, journal.CreatedAt, payload)
	return err
}

func (store *SQLStore) insertOutbox(ctx context.Context, execer sqlExecer, event OutboxEvent) error {
	payload, err := marshalPayload(event)
	if err != nil {
		return err
	}
	var publishedAt any
	if event.PublishedAt != nil {
		publishedAt = *event.PublishedAt
	}
	query := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", store.tables.Outbox, outboxSelectColumns, store.placeholders(7, 1))
	_, err = execer.ExecContext(ctx, query, event.TenantID.String(), event.ID, event.Type, event.AggregateID, event.CreatedAt, publishedAt, payload)
	return err
}

func (store *SQLStore) insertSettlement(ctx context.Context, execer sqlExecer, settlement Settlement) error {
	payload, err := marshalPayload(settlement)
	if err != nil {
		return err
	}
	query := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", store.tables.Settlements, settlementColumns, store.placeholders(12, 1))
	_, err = execer.ExecContext(ctx, query,
		settlement.TenantID.String(), settlement.ID, string(settlement.Beneficiary.Kind), settlement.Beneficiary.ID,
		settlement.Amount.Currency, settlement.Amount.Minor, string(settlement.Status), settlement.ProviderReference,
		settlement.Version, settlement.CreatedAt, settlement.UpdatedAt, payload,
	)
	return err
}

func (store *SQLStore) insertSettlementItem(ctx context.Context, execer sqlExecer, tenantID types.TenantID, settlementID string, earningID string) error {
	query := fmt.Sprintf("INSERT INTO %s (tenant_id, settlement_id, earning_id) VALUES (%s)", store.tables.SettlementItems, store.placeholders(3, 1))
	_, err := execer.ExecContext(ctx, query, tenantID.String(), settlementID, earningID)
	return err
}

func (store *SQLStore) updateTemplate(ctx context.Context, execer sqlExecer, template Template, expectedStatus TemplateStatus) error {
	payload, err := marshalPayload(template)
	if err != nil {
		return err
	}
	query := fmt.Sprintf("UPDATE %s SET status = %s, updated_at = %s, payload = %s WHERE id = %s AND version = %s AND status = %s", store.tables.Templates, store.placeholder(1), store.placeholder(2), store.placeholder(3), store.placeholder(4), store.placeholder(5), store.placeholder(6))
	result, err := execer.ExecContext(ctx, query, string(template.Status), template.UpdatedAt, payload, template.ID, template.Version, string(expectedStatus))
	if err != nil {
		return err
	}
	return requireAffected(result, ErrTemplateNotFound)
}

func (store *SQLStore) updateProgram(ctx context.Context, execer sqlExecer, program Program, expectedVersion int64) error {
	payload, err := marshalPayload(program)
	if err != nil {
		return err
	}
	query := fmt.Sprintf("UPDATE %s SET status = %s, version = %s, updated_at = %s, payload = %s WHERE tenant_id = %s AND id = %s AND version = %s", store.tables.Programs, store.placeholder(1), store.placeholder(2), store.placeholder(3), store.placeholder(4), store.placeholder(5), store.placeholder(6), store.placeholder(7))
	result, err := execer.ExecContext(ctx, query, string(program.Status), program.Version, program.UpdatedAt, payload, program.TenantID.String(), program.ID, expectedVersion)
	if err != nil {
		return err
	}
	return requireAffected(result, ErrVersionConflict)
}

func (store *SQLStore) updateAttribution(ctx context.Context, execer sqlExecer, attribution Attribution, expectedVersion int64) error {
	payload, err := marshalPayload(attribution)
	if err != nil {
		return err
	}
	query := fmt.Sprintf("UPDATE %s SET beneficiary_kind = %s, beneficiary_id = %s, active = %s, version = %s, updated_at = %s, payload = %s WHERE tenant_id = %s AND program_id = %s AND slot = %s AND version = %s", store.tables.Attributions, store.placeholder(1), store.placeholder(2), store.placeholder(3), store.placeholder(4), store.placeholder(5), store.placeholder(6), store.placeholder(7), store.placeholder(8), store.placeholder(9), store.placeholder(10))
	result, err := execer.ExecContext(ctx, query,
		string(attribution.Beneficiary.Kind), attribution.Beneficiary.ID, attribution.Active, attribution.Version, attribution.UpdatedAt, payload,
		attribution.TenantID.String(), attribution.ProgramID, attribution.Slot, expectedVersion,
	)
	if err != nil {
		return err
	}
	return requireAffected(result, ErrVersionConflict)
}

func (store *SQLStore) updateEarning(ctx context.Context, execer sqlExecer, earning Earning, expectedVersion int64) error {
	payload, err := marshalPayload(earning)
	if err != nil {
		return err
	}
	query := fmt.Sprintf("UPDATE %s SET status = %s, version = %s, updated_at = %s, payload = %s WHERE tenant_id = %s AND id = %s AND version = %s", store.tables.Earnings, store.placeholder(1), store.placeholder(2), store.placeholder(3), store.placeholder(4), store.placeholder(5), store.placeholder(6), store.placeholder(7))
	result, err := execer.ExecContext(ctx, query, string(earning.Status), earning.Version, earning.UpdatedAt, payload, earning.TenantID.String(), earning.ID, expectedVersion)
	if err != nil {
		return err
	}
	return requireAffected(result, ErrVersionConflict)
}

func (store *SQLStore) updateSettlement(ctx context.Context, execer sqlExecer, settlement Settlement, expectedVersion int64) error {
	payload, err := marshalPayload(settlement)
	if err != nil {
		return err
	}
	query := fmt.Sprintf("UPDATE %s SET status = %s, provider_reference = %s, version = %s, updated_at = %s, payload = %s WHERE tenant_id = %s AND id = %s AND version = %s", store.tables.Settlements, store.placeholder(1), store.placeholder(2), store.placeholder(3), store.placeholder(4), store.placeholder(5), store.placeholder(6), store.placeholder(7), store.placeholder(8))
	result, err := execer.ExecContext(ctx, query, string(settlement.Status), settlement.ProviderReference, settlement.Version, settlement.UpdatedAt, payload, settlement.TenantID.String(), settlement.ID, expectedVersion)
	if err != nil {
		return err
	}
	return requireAffected(result, ErrVersionConflict)
}

func (store *SQLStore) updateOutboxPublished(ctx context.Context, execer sqlExecer, event OutboxEvent) error {
	payload, err := marshalPayload(event)
	if err != nil {
		return err
	}
	query := fmt.Sprintf("UPDATE %s SET published_at = %s, payload = %s WHERE tenant_id = %s AND id = %s AND published_at IS NULL", store.tables.Outbox, store.placeholder(1), store.placeholder(2), store.placeholder(3), store.placeholder(4))
	publishedAt := *event.PublishedAt
	result, err := execer.ExecContext(ctx, query, publishedAt, payload, event.TenantID.String(), event.ID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return nil
	}
	return nil
}

func requireAffected(result sql.Result, empty error) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return empty
	}
	return nil
}

func (store *SQLStore) loadTemplate(ctx context.Context, queryer sqlQueryer, id string, version int64, lock bool) (Template, error) {
	query := fmt.Sprintf("SELECT %s FROM %s WHERE id = %s AND version = %s", templateSelectColumns, store.tables.Templates, store.placeholder(1), store.placeholder(2))
	if lock {
		query += store.forUpdate()
	}
	template, err := scanTemplate(queryer.QueryRowContext(ctx, query, id, version))
	if errors.Is(err, sql.ErrNoRows) {
		return Template{}, ErrTemplateNotFound
	}
	return template, err
}

func (store *SQLStore) loadLatestTemplate(ctx context.Context, id string) (Template, error) {
	query := fmt.Sprintf("SELECT %s FROM %s WHERE id = %s ORDER BY version DESC LIMIT 1", templateSelectColumns, store.tables.Templates, store.placeholder(1))
	template, err := scanTemplate(store.db.QueryRowContext(ctx, query, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Template{}, ErrTemplateNotFound
	}
	return template, err
}

func (store *SQLStore) loadProgram(ctx context.Context, queryer sqlQueryer, tenantID types.TenantID, id string, lock bool) (Program, error) {
	query := fmt.Sprintf("SELECT %s FROM %s WHERE tenant_id = %s AND id = %s", programSelectColumns, store.tables.Programs, store.placeholder(1), store.placeholder(2))
	if lock {
		query += store.forUpdate()
	}
	program, err := scanProgram(queryer.QueryRowContext(ctx, query, tenantID.String(), id))
	if errors.Is(err, sql.ErrNoRows) {
		return Program{}, ErrProgramNotFound
	}
	return program, err
}

func (store *SQLStore) loadAttribution(ctx context.Context, queryer sqlQueryer, tenantID types.TenantID, programID string, slot string, lock bool) (Attribution, error) {
	query := fmt.Sprintf("SELECT %s FROM %s WHERE tenant_id = %s AND program_id = %s AND slot = %s", attributionColumns, store.tables.Attributions, store.placeholder(1), store.placeholder(2), store.placeholder(3))
	if lock {
		query += store.forUpdate()
	}
	attribution, err := scanAttribution(queryer.QueryRowContext(ctx, query, tenantID.String(), programID, slot))
	if errors.Is(err, sql.ErrNoRows) {
		return Attribution{}, sql.ErrNoRows
	}
	return attribution, err
}

func (store *SQLStore) loadEarning(ctx context.Context, queryer sqlQueryer, tenantID types.TenantID, id string, lock bool) (Earning, error) {
	query := fmt.Sprintf("SELECT %s FROM %s WHERE tenant_id = %s AND id = %s", earningSelectColumns, store.tables.Earnings, store.placeholder(1), store.placeholder(2))
	if lock {
		query += store.forUpdate()
	}
	earning, err := scanEarning(queryer.QueryRowContext(ctx, query, tenantID.String(), id))
	if errors.Is(err, sql.ErrNoRows) {
		return Earning{}, ErrEarningNotFound
	}
	return earning, err
}

func (store *SQLStore) loadSettlement(ctx context.Context, queryer sqlQueryer, tenantID types.TenantID, id string, lock bool) (Settlement, error) {
	query := fmt.Sprintf("SELECT %s FROM %s WHERE tenant_id = %s AND id = %s", settlementColumns, store.tables.Settlements, store.placeholder(1), store.placeholder(2))
	if lock {
		query += store.forUpdate()
	}
	settlement, err := scanSettlement(queryer.QueryRowContext(ctx, query, tenantID.String(), id))
	if errors.Is(err, sql.ErrNoRows) {
		return Settlement{}, ErrSettlementNotFound
	}
	return settlement, err
}

func (store *SQLStore) loadOutbox(ctx context.Context, queryer sqlQueryer, tenantID types.TenantID, id string, lock bool) (OutboxEvent, error) {
	query := fmt.Sprintf("SELECT %s FROM %s WHERE tenant_id = %s AND id = %s", outboxSelectColumns, store.tables.Outbox, store.placeholder(1), store.placeholder(2))
	if lock {
		query += store.forUpdate()
	}
	event, err := scanOutbox(queryer.QueryRowContext(ctx, query, tenantID.String(), id))
	if errors.Is(err, sql.ErrNoRows) {
		return OutboxEvent{}, ErrOutboxNotFound
	}
	return event, err
}

func (store *SQLStore) listEarningsForEvent(ctx context.Context, queryer sqlQueryer, tenantID types.TenantID, sourceType string, sourceID string) (earnings []Earning, err error) {
	query := fmt.Sprintf("SELECT %s FROM %s WHERE tenant_id = %s AND source_type = %s AND source_id = %s ORDER BY id", earningSelectColumns, store.tables.Earnings, store.placeholder(1), store.placeholder(2), store.placeholder(3))
	rows, err := queryer.QueryContext(ctx, query, tenantID.String(), sourceType, sourceID)
	if err != nil {
		return nil, err
	}
	defer func() {
		err = errors.Join(err, rows.Close())
	}()

	earnings = make([]Earning, 0)
	for rows.Next() {
		earning, err := scanEarning(rows)
		if err != nil {
			return nil, err
		}
		earnings = append(earnings, cloneEarning(earning))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return earnings, nil
}

func (store *SQLStore) eventFingerprint(ctx context.Context, queryer sqlQueryer, tenantID types.TenantID, sourceType string, sourceID string, lock bool) (string, bool, error) {
	query := fmt.Sprintf("SELECT fingerprint FROM %s WHERE tenant_id = %s AND source_type = %s AND source_id = %s", store.tables.Events, store.placeholder(1), store.placeholder(2), store.placeholder(3))
	if lock {
		query += store.forUpdate()
	}
	var fingerprint string
	err := queryer.QueryRowContext(ctx, query, tenantID.String(), sourceType, sourceID).Scan(&fingerprint)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return fingerprint, true, nil
}

func (store *SQLStore) journalIDExists(ctx context.Context, queryer sqlQueryer, id string, lock bool) (bool, error) {
	query := fmt.Sprintf("SELECT id FROM %s WHERE id = %s", store.tables.Journals, store.placeholder(1))
	if lock {
		query += store.forUpdate()
	}
	var existing string
	err := queryer.QueryRowContext(ctx, query, id).Scan(&existing)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (store *SQLStore) existingCommitResult(ctx context.Context, commit EventCommit) ([]Earning, bool, error) {
	fingerprint, found, err := store.eventFingerprint(ctx, store.db, commit.Event.TenantID, commit.Event.SourceType, commit.Event.SourceID, false)
	if err != nil || !found {
		return nil, found, err
	}
	if fingerprint != eventIdempotencyFingerprint(commit.Event, commit.ProgramID) {
		return nil, true, ErrEventConflict
	}
	earnings, err := store.listEarningsForEvent(ctx, store.db, commit.Event.TenantID, commit.Event.SourceType, commit.Event.SourceID)
	if err != nil {
		return nil, true, err
	}
	return earnings, true, nil
}

// validatePersistedProgram accepts every lifecycle state that can exist after
// creation. validateNewProgram intentionally validates only the immutable
// creation shape (draft/version 1), so it cannot be used when scanning a
// program that has already been approved, suspended, or resumed.
func validatePersistedProgram(program Program) error {
	if program.ID == "" || program.TenantID == "" || program.TemplateID == "" || program.TemplateVersion <= 0 || !validProgramStatus(program.Status) || program.Version <= 0 || program.CreatedAt.IsZero() || program.UpdatedAt.IsZero() || len(program.Rules) == 0 {
		return ErrInvalidProgram
	}
	for _, rule := range program.Rules {
		if err := validateRule(rule, true); err != nil {
			return ErrInvalidProgram
		}
	}
	return nil
}

// CreateTemplate inserts one immutable commission-template version.
func (store *SQLStore) CreateTemplate(ctx context.Context, template Template) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateNewTemplate(template); err != nil {
		return err
	}
	if err := store.insertTemplate(ctx, store.db, cloneTemplate(template)); err != nil {
		return sqlutil.NormalizeDuplicateKeyError(err, ErrTemplateAlreadyExists)
	}
	return nil
}

// GetTemplate returns one requested template version. Version zero returns the
// current highest version for the template identifier.
func (store *SQLStore) GetTemplate(ctx context.Context, id string, version int64) (Template, error) {
	if err := ctx.Err(); err != nil {
		return Template{}, err
	}
	if id == "" || version < 0 {
		return Template{}, ErrInvalidTemplate
	}
	if version == 0 {
		template, err := store.loadLatestTemplate(ctx, id)
		return cloneTemplate(template), err
	}
	template, err := store.loadTemplate(ctx, store.db, id, version, false)
	return cloneTemplate(template), err
}

// TransitionTemplate changes only a template lifecycle state.
func (store *SQLStore) TransitionTemplate(ctx context.Context, id string, version int64, action TemplateAction, now time.Time) (Template, error) {
	if err := ctx.Err(); err != nil {
		return Template{}, err
	}
	if id == "" || version <= 0 || now.IsZero() {
		return Template{}, ErrInvalidTemplate
	}
	return retryCommissionMutation(ctx, func() (Template, error) {
		var updated Template
		err := store.inSerializableTx(ctx, func(tx *sql.Tx) error {
			template, err := store.loadTemplate(ctx, tx, id, version, true)
			if err != nil {
				return err
			}
			expectedStatus := template.Status
			next, err := nextTemplateStatus(template.Status, action)
			if err != nil {
				return err
			}
			template.Status = next
			template.UpdatedAt = now
			if err := store.updateTemplate(ctx, tx, template, expectedStatus); err != nil {
				return err
			}
			updated = cloneTemplate(template)
			return nil
		})
		return updated, err
	})
}

// CreateProgram inserts one tenant-owned commission program.
func (store *SQLStore) CreateProgram(ctx context.Context, program Program) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateNewProgram(program); err != nil {
		return err
	}
	if program.Status != ProgramStatusDraft || program.Version != 1 {
		return ErrInvalidProgram
	}
	_, err := retryCommissionMutation(ctx, func() (struct{}, error) {
		err := store.inSerializableTx(ctx, func(tx *sql.Tx) error {
			_, err := store.loadProgram(ctx, tx, program.TenantID, program.ID, true)
			if err == nil {
				return ErrProgramAlreadyExists
			}
			if !errors.Is(err, ErrProgramNotFound) {
				return err
			}
			template, err := store.loadTemplate(ctx, tx, program.TemplateID, program.TemplateVersion, true)
			if err != nil {
				return err
			}
			if err := validateProgramAgainstTemplate(program, template); err != nil {
				return err
			}
			if err := store.insertProgram(ctx, tx, cloneProgram(program)); err != nil {
				return sqlutil.NormalizeDuplicateKeyError(err, ErrProgramAlreadyExists)
			}
			return nil
		})
		return struct{}{}, err
	})
	return err
}

// GetProgram returns a tenant-scoped commission program.
func (store *SQLStore) GetProgram(ctx context.Context, tenantID types.TenantID, id string) (Program, error) {
	if err := ctx.Err(); err != nil {
		return Program{}, err
	}
	if tenantID == "" || id == "" {
		return Program{}, ErrInvalidProgram
	}
	program, err := store.loadProgram(ctx, store.db, tenantID, id, false)
	return cloneProgram(program), err
}

// TransitionProgram applies a tenant-program lifecycle CAS.
func (store *SQLStore) TransitionProgram(ctx context.Context, tenantID types.TenantID, id string, expectedVersion int64, action ProgramAction, now time.Time) (Program, error) {
	if err := ctx.Err(); err != nil {
		return Program{}, err
	}
	if tenantID == "" || id == "" || expectedVersion <= 0 || now.IsZero() {
		return Program{}, ErrInvalidProgram
	}
	return retryCommissionMutation(ctx, func() (Program, error) {
		var updated Program
		err := store.inSerializableTx(ctx, func(tx *sql.Tx) error {
			program, err := store.loadProgram(ctx, tx, tenantID, id, true)
			if err != nil {
				return err
			}
			if program.Version != expectedVersion {
				return ErrVersionConflict
			}
			next, err := nextProgramStatus(program.Status, action)
			if err != nil {
				return err
			}
			if next == ProgramStatusActive {
				template, err := store.loadTemplate(ctx, tx, program.TemplateID, program.TemplateVersion, true)
				if err != nil {
					return err
				}
				if template.Status != TemplateStatusActive {
					return ErrTemplateNotActive
				}
			}
			program.Status = next
			program.Version++
			program.UpdatedAt = now
			if err := store.updateProgram(ctx, tx, program, expectedVersion); err != nil {
				return err
			}
			updated = cloneProgram(program)
			return nil
		})
		return updated, err
	})
}

// SetAttribution creates or changes one beneficiary attribution with an
// optimistic version precondition.
func (store *SQLStore) SetAttribution(ctx context.Context, attribution Attribution, expectedVersion int64) (Attribution, error) {
	if err := ctx.Err(); err != nil {
		return Attribution{}, err
	}
	if err := validateAttribution(attribution); err != nil || expectedVersion < 0 {
		return Attribution{}, ErrInvalidAttribution
	}
	return retryCommissionMutation(ctx, func() (Attribution, error) {
		var saved Attribution
		err := store.inSerializableTx(ctx, func(tx *sql.Tx) error {
			current, err := store.loadAttribution(ctx, tx, attribution.TenantID, attribution.ProgramID, attribution.Slot, true)
			if errors.Is(err, sql.ErrNoRows) {
				if expectedVersion != 0 {
					return ErrVersionConflict
				}
				attribution.Version = 1
				if err := store.insertAttribution(ctx, tx, cloneAttribution(attribution)); err != nil {
					return sqlutil.NormalizeDuplicateKeyError(err, ErrVersionConflict)
				}
				saved = cloneAttribution(attribution)
				return nil
			}
			if err != nil {
				return err
			}
			if current.Version != expectedVersion {
				return ErrVersionConflict
			}
			attribution.Version = current.Version + 1
			attribution.CreatedAt = current.CreatedAt
			if err := store.updateAttribution(ctx, tx, attribution, expectedVersion); err != nil {
				return err
			}
			saved = cloneAttribution(attribution)
			return nil
		})
		return saved, err
	})
}

// ListAttributions returns tenant-program attributions ordered by rule slot.
func (store *SQLStore) ListAttributions(ctx context.Context, tenantID types.TenantID, programID string) (attributions []Attribution, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if tenantID == "" || programID == "" {
		return nil, ErrInvalidAttribution
	}
	query := fmt.Sprintf("SELECT %s FROM %s WHERE tenant_id = %s AND program_id = %s ORDER BY slot", attributionColumns, store.tables.Attributions, store.placeholder(1), store.placeholder(2))
	rows, err := store.db.QueryContext(ctx, query, tenantID.String(), programID)
	if err != nil {
		return nil, err
	}
	defer func() {
		err = errors.Join(err, rows.Close())
	}()

	attributions = make([]Attribution, 0)
	for rows.Next() {
		attribution, err := scanAttribution(rows)
		if err != nil {
			return nil, err
		}
		attributions = append(attributions, cloneAttribution(attribution))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return attributions, nil
}

// CommitEvent atomically persists a source event and its immutable financial
// projections. The source key is idempotent only when its fact and program
// scoped fingerprint matches.
func (store *SQLStore) CommitEvent(ctx context.Context, commit EventCommit) ([]Earning, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateEventCommit(commit); err != nil {
		return nil, err
	}

	fingerprint := eventIdempotencyFingerprint(commit.Event, commit.ProgramID)
	earnings, err := retryCommissionMutation(ctx, func() ([]Earning, error) {
		created := make([]Earning, 0, len(commit.Earnings))
		err := store.inSerializableTx(ctx, func(tx *sql.Tx) error {
			existingFingerprint, found, err := store.eventFingerprint(ctx, tx, commit.Event.TenantID, commit.Event.SourceType, commit.Event.SourceID, true)
			if err != nil {
				return err
			}
			if found {
				if existingFingerprint != fingerprint {
					return ErrEventConflict
				}
				original, err := store.listEarningsForEvent(ctx, tx, commit.Event.TenantID, commit.Event.SourceType, commit.Event.SourceID)
				if err != nil {
					return err
				}
				created = original
				return nil
			}

			program, err := store.loadProgram(ctx, tx, commit.Event.TenantID, commit.ProgramID, true)
			if err != nil {
				return err
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
				attribution, err := store.loadAttribution(ctx, tx, commit.Event.TenantID, program.ID, rule.Slot, true)
				if errors.Is(err, sql.ErrNoRows) {
					if expectedVersion != 0 {
						return ErrVersionConflict
					}
					continue
				}
				if err != nil {
					return err
				}
				if attribution.Version != expectedVersion {
					return ErrVersionConflict
				}
			}
			template, err := store.loadTemplate(ctx, tx, commit.TemplateID, commit.TemplateVersion, true)
			if err != nil {
				return err
			}
			if template.Status != TemplateStatusActive {
				return ErrTemplateNotActive
			}

			for _, journal := range commit.Journals {
				exists, err := store.journalIDExists(ctx, tx, journal.ID, true)
				if err != nil {
					return err
				}
				if exists {
					return ErrEventConflict
				}
			}

			if err := store.insertEvent(ctx, tx, commit, fingerprint); err != nil {
				return err
			}
			for _, earning := range commit.Earnings {
				if err := store.insertEarning(ctx, tx, cloneEarning(earning)); err != nil {
					return err
				}
				created = append(created, cloneEarning(earning))
			}
			for _, journal := range commit.Journals {
				if err := store.insertJournal(ctx, tx, cloneJournal(journal)); err != nil {
					return err
				}
			}
			for _, event := range commit.Outbox {
				if err := store.insertOutbox(ctx, tx, cloneOutbox(event)); err != nil {
					return err
				}
			}
			return nil
		})
		return created, err
	})
	if err == nil || !sqlutil.IsDuplicateKeyError(err) {
		return earnings, err
	}

	// A unique event-key conflict can be a concurrent identical commit. Read
	// the committed winner and preserve idempotency; unrelated unique collisions
	// remain a domain conflict rather than leaking a driver-specific error.
	existing, found, lookupErr := store.existingCommitResult(ctx, commit)
	if lookupErr != nil {
		return nil, lookupErr
	}
	if found {
		return existing, nil
	}
	return nil, ErrEventConflict
}

// GetEarning returns one tenant-scoped earning.
func (store *SQLStore) GetEarning(ctx context.Context, tenantID types.TenantID, id string) (Earning, error) {
	if err := ctx.Err(); err != nil {
		return Earning{}, err
	}
	if tenantID == "" || id == "" {
		return Earning{}, ErrInvalidEarning
	}
	earning, err := store.loadEarning(ctx, store.db, tenantID, id, false)
	return cloneEarning(earning), err
}

// ListEarnings returns tenant-scoped earnings in ID order.
func (store *SQLStore) ListEarnings(ctx context.Context, tenantID types.TenantID, filter EarningFilter) (earnings []Earning, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if tenantID == "" || !filter.valid() {
		return nil, ErrInvalidEarningFilter
	}

	clauses := []string{fmt.Sprintf("tenant_id = %s", store.placeholder(1))}
	args := []any{tenantID.String()}
	add := func(clause string, values ...any) {
		clauses = append(clauses, clause)
		args = append(args, values...)
	}
	if filter.ProgramID != "" {
		add(fmt.Sprintf("program_id = %s", store.placeholder(len(args)+1)), filter.ProgramID)
	}
	if filter.Beneficiary != nil {
		add(fmt.Sprintf("beneficiary_kind = %s AND beneficiary_id = %s", store.placeholder(len(args)+1), store.placeholder(len(args)+2)), string(filter.Beneficiary.Kind), filter.Beneficiary.ID)
	}
	if len(filter.Statuses) > 0 {
		start := len(args) + 1
		add(fmt.Sprintf("status IN (%s)", store.placeholders(len(filter.Statuses), start)))
		for _, status := range filter.Statuses {
			args = append(args, string(status))
		}
	}
	if filter.Cursor != "" {
		add(fmt.Sprintf("id > %s", store.placeholder(len(args)+1)), filter.Cursor)
	}
	query := fmt.Sprintf("SELECT %s FROM %s WHERE %s ORDER BY id", earningSelectColumns, store.tables.Earnings, strings.Join(clauses, " AND "))
	if filter.Limit > 0 {
		query += fmt.Sprintf(" LIMIT %s", store.placeholder(len(args)+1))
		args = append(args, filter.Limit)
	}
	rows, err := store.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() {
		err = errors.Join(err, rows.Close())
	}()

	earnings = make([]Earning, 0)
	for rows.Next() {
		earning, err := scanEarning(rows)
		if err != nil {
			return nil, err
		}
		earnings = append(earnings, cloneEarning(earning))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return earnings, nil
}

// ListJournalEntries returns immutable entries for one tenant-scoped earning.
func (store *SQLStore) ListJournalEntries(ctx context.Context, tenantID types.TenantID, earningID string) (entries []JournalEntry, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if tenantID == "" || earningID == "" {
		return nil, ErrInvalidEarning
	}
	query := fmt.Sprintf("SELECT %s FROM %s WHERE tenant_id = %s AND earning_id = %s ORDER BY created_at, id", journalSelectColumns, store.tables.Journals, store.placeholder(1), store.placeholder(2))
	rows, err := store.db.QueryContext(ctx, query, tenantID.String(), earningID)
	if err != nil {
		return nil, err
	}
	defer func() {
		err = errors.Join(err, rows.Close())
	}()

	entries = make([]JournalEntry, 0)
	for rows.Next() {
		entry, err := scanJournal(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, cloneJournal(entry))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}

// TransitionEarning applies a user-directed lifecycle operation. Settlement
// state changes are reserved for settlement aggregate operations.
func (store *SQLStore) TransitionEarning(ctx context.Context, tenantID types.TenantID, id string, expectedVersion int64, action EarningAction, now time.Time) (Earning, error) {
	if err := ctx.Err(); err != nil {
		return Earning{}, err
	}
	if tenantID == "" || id == "" || expectedVersion <= 0 || now.IsZero() || action == EarningActionMakeAvailable || action == EarningActionStartSettlement || action == EarningActionSettle || action == EarningActionRejectSettlement {
		return Earning{}, ErrInvalidEarning
	}
	return retryCommissionMutation(ctx, func() (Earning, error) {
		var updated Earning
		err := store.inSerializableTx(ctx, func(tx *sql.Tx) error {
			earning, err := store.loadEarning(ctx, tx, tenantID, id, true)
			if err != nil {
				return err
			}
			if earning.Version != expectedVersion {
				return ErrVersionConflict
			}
			next, err := TransitionEarning(earning.Status, action)
			if err != nil {
				return err
			}
			earning.Status = next
			earning.Version++
			earning.UpdatedAt = now
			if err := store.updateEarning(ctx, tx, earning, expectedVersion); err != nil {
				return err
			}
			if err := store.appendEarningTransition(ctx, tx, earning, action, now); err != nil {
				return err
			}
			updated = cloneEarning(earning)
			return nil
		})
		return updated, err
	})
}

// MakeAvailableDue atomically transitions at most limit due pending earnings.
func (store *SQLStore) MakeAvailableDue(ctx context.Context, now time.Time, limit int) ([]Earning, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if now.IsZero() || limit <= 0 {
		return nil, ErrInvalidEarning
	}
	return retryCommissionMutation(ctx, func() ([]Earning, error) {
		available := make([]Earning, 0)
		err := store.inSerializableTx(ctx, func(tx *sql.Tx) error {
			query := fmt.Sprintf("SELECT %s FROM %s WHERE status = %s AND available_at <= %s ORDER BY available_at, id LIMIT %s%s", earningSelectColumns, store.tables.Earnings, store.placeholder(1), store.placeholder(2), store.placeholder(3), store.forUpdate())
			rows, err := tx.QueryContext(ctx, query, string(EarningStatusPending), now, limit)
			if err != nil {
				return err
			}
			due := make([]Earning, 0)
			for rows.Next() {
				earning, err := scanEarning(rows)
				if err != nil {
					_ = rows.Close()
					return err
				}
				due = append(due, earning)
			}
			if err := rows.Err(); err != nil {
				_ = rows.Close()
				return err
			}
			if err := rows.Close(); err != nil {
				return err
			}
			for _, earning := range due {
				next, err := TransitionEarning(earning.Status, EarningActionMakeAvailable)
				if err != nil {
					return err
				}
				expectedVersion := earning.Version
				earning.Status = next
				earning.Version++
				earning.UpdatedAt = now
				if err := store.updateEarning(ctx, tx, earning, expectedVersion); err != nil {
					return err
				}
				if err := store.appendEarningTransition(ctx, tx, earning, EarningActionMakeAvailable, now); err != nil {
					return err
				}
				available = append(available, cloneEarning(earning))
			}
			return nil
		})
		return available, err
	})
}

func (store *SQLStore) appendEarningTransition(ctx context.Context, tx *sql.Tx, earning Earning, action EarningAction, now time.Time) error {
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
	journal := JournalEntry{
		ID:        journalID(earning.ID, kind, earning.Version),
		TenantID:  earning.TenantID,
		EarningID: earning.ID,
		Kind:      kind,
		Amount:    earning.Amount,
		CreatedAt: now,
	}
	exists, err := store.journalIDExists(ctx, tx, journal.ID, true)
	if err != nil {
		return err
	}
	if exists {
		return ErrEventConflict
	}
	if err := store.insertJournal(ctx, tx, journal); err != nil {
		return sqlutil.NormalizeDuplicateKeyError(err, ErrEventConflict)
	}
	eventType := "commission.earning." + string(earning.Status)
	event := OutboxEvent{
		ID:          sqlScopedOutboxID(earning.TenantID, earning.ID, eventType, earning.Version),
		TenantID:    earning.TenantID,
		Type:        eventType,
		AggregateID: earning.ID,
		Payload: map[string]string{
			"earning_id": earning.ID,
			"status":     string(earning.Status),
		},
		CreatedAt: now,
	}
	if err := store.insertOutbox(ctx, tx, event); err != nil {
		return sqlutil.NormalizeDuplicateKeyError(err, ErrEventConflict)
	}
	return nil
}

func sqlScopedOutboxID(tenantID types.TenantID, aggregateID string, eventType string, version int64) string {
	return outboxID(tenantID.String(), aggregateID, eventType, version)
}

// StartSettlement atomically claims compatible available earnings for one
// beneficiary and creates a submitted settlement batch.
func (store *SQLStore) StartSettlement(ctx context.Context, settlement Settlement, expectedVersions map[string]int64) (Settlement, error) {
	if err := ctx.Err(); err != nil {
		return Settlement{}, err
	}
	if err := validateSettlement(settlement, expectedVersions); err != nil {
		return Settlement{}, err
	}
	return retryCommissionMutation(ctx, func() (Settlement, error) {
		var created Settlement
		err := store.inSerializableTx(ctx, func(tx *sql.Tx) error {
			_, err := store.loadSettlement(ctx, tx, settlement.TenantID, settlement.ID, true)
			if err == nil {
				return ErrSettlementAlreadyExists
			}
			if !errors.Is(err, ErrSettlementNotFound) {
				return err
			}

			locked := make(map[string]Earning, len(settlement.EarningIDs))
			var total int64
			for _, earningID := range sortedEarningIDs(settlement.EarningIDs) {
				earning, err := store.loadEarning(ctx, tx, settlement.TenantID, earningID, true)
				if errors.Is(err, ErrEarningNotFound) {
					return ErrEarningUnavailable
				}
				if err != nil {
					return err
				}
				if earning.Status != EarningStatusAvailable || earning.Version != expectedVersions[earningID] || earning.Beneficiary != settlement.Beneficiary || earning.Amount.Currency != settlement.Amount.Currency {
					return ErrEarningUnavailable
				}
				if earning.Amount.Minor > math.MaxInt64-total {
					return ErrInvalidSettlement
				}
				total += earning.Amount.Minor
				locked[earningID] = earning
			}
			if total != settlement.Amount.Minor {
				return ErrInvalidSettlement
			}

			if err := store.insertSettlement(ctx, tx, cloneSettlement(settlement)); err != nil {
				return sqlutil.NormalizeDuplicateKeyError(err, ErrSettlementAlreadyExists)
			}
			for _, earningID := range sortedEarningIDs(settlement.EarningIDs) {
				earning := locked[earningID]
				expectedVersion := earning.Version
				next, err := TransitionEarning(earning.Status, EarningActionStartSettlement)
				if err != nil {
					return err
				}
				earning.Status = next
				earning.Version++
				earning.UpdatedAt = settlement.CreatedAt
				if err := store.updateEarning(ctx, tx, earning, expectedVersion); err != nil {
					return err
				}
				if err := store.insertSettlementItem(ctx, tx, settlement.TenantID, settlement.ID, earningID); err != nil {
					return sqlutil.NormalizeDuplicateKeyError(err, ErrSettlementAlreadyExists)
				}
			}
			event := OutboxEvent{
				ID:          sqlScopedOutboxID(settlement.TenantID, settlement.ID, "commission.settlement.submitted", settlement.Version),
				TenantID:    settlement.TenantID,
				Type:        "commission.settlement.submitted",
				AggregateID: settlement.ID,
				Payload: map[string]string{
					"settlement_id": settlement.ID,
					"currency":      settlement.Amount.Currency,
				},
				CreatedAt: settlement.CreatedAt,
			}
			if err := store.insertOutbox(ctx, tx, event); err != nil {
				return sqlutil.NormalizeDuplicateKeyError(err, ErrEventConflict)
			}
			created = cloneSettlement(settlement)
			return nil
		})
		return created, err
	})
}

// GetSettlement returns one tenant-scoped settlement batch.
func (store *SQLStore) GetSettlement(ctx context.Context, tenantID types.TenantID, id string) (Settlement, error) {
	if err := ctx.Err(); err != nil {
		return Settlement{}, err
	}
	if tenantID == "" || id == "" {
		return Settlement{}, ErrInvalidSettlement
	}
	settlement, err := store.loadSettlement(ctx, store.db, tenantID, id, false)
	return cloneSettlement(settlement), err
}

// FinishSettlement persists a host-verified settlement outcome. Repeating the
// same terminal callback is idempotent; a different terminal outcome conflicts.
func (store *SQLStore) FinishSettlement(ctx context.Context, tenantID types.TenantID, id string, expectedVersion int64, settled bool, providerReference string, now time.Time) (Settlement, error) {
	if err := ctx.Err(); err != nil {
		return Settlement{}, err
	}
	if tenantID == "" || id == "" || expectedVersion <= 0 || now.IsZero() {
		return Settlement{}, ErrInvalidSettlement
	}
	desiredStatus := SettlementStatusRejected
	if settled {
		desiredStatus = SettlementStatusSettled
	}
	return retryCommissionMutation(ctx, func() (Settlement, error) {
		var finished Settlement
		err := store.inSerializableTx(ctx, func(tx *sql.Tx) error {
			batch, err := store.loadSettlement(ctx, tx, tenantID, id, true)
			if err != nil {
				return err
			}
			if batch.Status != SettlementStatusSubmitted {
				if batch.Status == desiredStatus && batch.ProviderReference == providerReference {
					finished = cloneSettlement(batch)
					return nil
				}
				return ErrSettlementOutcomeConflict
			}
			if batch.Version != expectedVersion {
				return ErrVersionConflict
			}

			type earningTransition struct {
				earning         Earning
				expectedVersion int64
				action          EarningAction
			}
			transitions := make([]earningTransition, 0, len(batch.EarningIDs))
			action := EarningActionRejectSettlement
			if settled {
				action = EarningActionSettle
			}
			for _, earningID := range sortedEarningIDs(batch.EarningIDs) {
				earning, err := store.loadEarning(ctx, tx, tenantID, earningID, true)
				if errors.Is(err, ErrEarningNotFound) {
					return ErrEarningUnavailable
				}
				if err != nil {
					return err
				}
				if earning.Status != EarningStatusSettling {
					return ErrEarningUnavailable
				}
				next, err := TransitionEarning(earning.Status, action)
				if err != nil {
					return err
				}
				expectedEarningVersion := earning.Version
				earning.Status = next
				earning.Version++
				earning.UpdatedAt = now
				transitions = append(transitions, earningTransition{earning: earning, expectedVersion: expectedEarningVersion, action: action})
			}
			for _, transition := range transitions {
				if err := store.updateEarning(ctx, tx, transition.earning, transition.expectedVersion); err != nil {
					return err
				}
				if err := store.appendEarningTransition(ctx, tx, transition.earning, transition.action, now); err != nil {
					return err
				}
			}
			batch.Status = desiredStatus
			batch.ProviderReference = providerReference
			batch.Version++
			batch.UpdatedAt = now
			if err := store.updateSettlement(ctx, tx, batch, expectedVersion); err != nil {
				return err
			}
			eventType := "commission.settlement." + string(batch.Status)
			event := OutboxEvent{
				ID:          sqlScopedOutboxID(batch.TenantID, batch.ID, eventType, batch.Version),
				TenantID:    batch.TenantID,
				Type:        eventType,
				AggregateID: batch.ID,
				Payload: map[string]string{
					"settlement_id": batch.ID,
					"status":        string(batch.Status),
				},
				CreatedAt: now,
			}
			if err := store.insertOutbox(ctx, tx, event); err != nil {
				return sqlutil.NormalizeDuplicateKeyError(err, ErrEventConflict)
			}
			finished = cloneSettlement(batch)
			return nil
		})
		return finished, err
	})
}

// ListOutbox returns tenant-scoped outbox events ordered by (created_at, id).
func (store *SQLStore) ListOutbox(ctx context.Context, tenantID types.TenantID, filter OutboxFilter) (events []OutboxEvent, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if tenantID == "" || !filter.valid() {
		return nil, ErrInvalidOutboxFilter
	}
	clauses := []string{fmt.Sprintf("tenant_id = %s", store.placeholder(1))}
	args := []any{tenantID.String()}
	if filter.UnpublishedOnly {
		clauses = append(clauses, "published_at IS NULL")
	}
	if !filter.Cursor.empty() {
		createdFirst := store.placeholder(len(args) + 1)
		createdSecond := store.placeholder(len(args) + 2)
		idPlaceholder := store.placeholder(len(args) + 3)
		clauses = append(clauses, fmt.Sprintf("(created_at > %s OR (created_at = %s AND id > %s))", createdFirst, createdSecond, idPlaceholder))
		args = append(args, filter.Cursor.CreatedAt, filter.Cursor.CreatedAt, filter.Cursor.ID)
	}
	query := fmt.Sprintf("SELECT %s FROM %s WHERE %s ORDER BY created_at, id", outboxSelectColumns, store.tables.Outbox, strings.Join(clauses, " AND "))
	if filter.Limit > 0 {
		query += fmt.Sprintf(" LIMIT %s", store.placeholder(len(args)+1))
		args = append(args, filter.Limit)
	}
	rows, err := store.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() {
		err = errors.Join(err, rows.Close())
	}()

	events = make([]OutboxEvent, 0)
	for rows.Next() {
		event, err := scanOutbox(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, cloneOutbox(event))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

// MarkOutboxPublished records one successful host delivery without changing
// the event body. It is idempotent for an already-published event.
func (store *SQLStore) MarkOutboxPublished(ctx context.Context, tenantID types.TenantID, id string, publishedAt time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if tenantID == "" || id == "" || publishedAt.IsZero() {
		return ErrInvalidOutbox
	}
	_, err := retryCommissionMutation(ctx, func() (struct{}, error) {
		err := store.inSerializableTx(ctx, func(tx *sql.Tx) error {
			event, err := store.loadOutbox(ctx, tx, tenantID, id, true)
			if err != nil {
				return err
			}
			if event.PublishedAt != nil {
				return nil
			}
			value := publishedAt
			event.PublishedAt = &value
			return store.updateOutboxPublished(ctx, tx, event)
		})
		return struct{}{}, err
	})
	return err
}

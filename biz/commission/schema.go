package commission

import (
	"fmt"

	"github.com/darkinno-tech/saas/internal/sqlutil"
)

// Default SQL table names. SQLStore deliberately does not create or migrate
// these tables: the host owns migrations, retention, partitioning, and the
// database-specific JSON type (JSON, JSONB, or TEXT).
const (
	DefaultSQLTemplateTableName       = "commission_templates"
	DefaultSQLProgramTableName        = "commission_programs"
	DefaultSQLAttributionTableName    = "commission_attributions"
	DefaultSQLEventTableName          = "commission_events"
	DefaultSQLEarningTableName        = "commission_earnings"
	DefaultSQLJournalTableName        = "commission_journals"
	DefaultSQLOutboxTableName         = "commission_outbox"
	DefaultSQLSettlementTableName     = "commission_settlements"
	DefaultSQLSettlementItemTableName = "commission_settlement_items"
)

// SQLTableNames identifies the host-managed tables used by SQLStore. Every
// value must be a safe qualified identifier such as "commission_earnings" or
// "ledger.commission_earnings".
//
// Required logical columns are:
//
//   - Templates: id, version, status, created_at, updated_at, payload.
//   - Programs: tenant_id, id, template_id, template_version, status, version,
//     created_at, updated_at, payload.
//   - Attributions: tenant_id, program_id, slot, beneficiary_kind,
//     beneficiary_id, active, version, created_at, updated_at, payload.
//   - Events: tenant_id, source_type, source_id, fingerprint, program_id,
//     program_version, template_id, template_version, occurred_at, payload.
//   - Earnings: tenant_id, id, program_id, template_id, template_version,
//     source_type, source_id, slot, beneficiary_kind, beneficiary_id,
//     currency, amount_minor, status, available_at, version, created_at,
//     updated_at, payload.
//   - Journals: tenant_id, id, earning_id, kind, currency, amount_minor,
//     created_at, payload.
//   - Outbox: tenant_id, id, type, aggregate_id, created_at, published_at,
//     payload.
//   - Settlements: tenant_id, id, beneficiary_kind, beneficiary_id, currency,
//     amount_minor, status, provider_reference, version, created_at, updated_at,
//     payload.
//   - SettlementItems: tenant_id, settlement_id, earning_id.
//
// Hosts MUST enforce uniqueness on template (id, version), program
// (tenant_id, id), attribution (tenant_id, program_id, slot), event
// (tenant_id, source_type, source_id), earning (tenant_id, id), journal (id),
// outbox (tenant_id, id), settlement (tenant_id, id), and settlement item
// (tenant_id, settlement_id, earning_id). Journal IDs are globally unique so a
// malformed cross-tenant write cannot duplicate an accounting fact. Generated
// outbox IDs include tenant_id as a second collision boundary. The event and
// outbox/earning keys are financial idempotency boundaries and MUST be unique.
// Event fingerprint stores the source-fact fingerprint scoped to the selected
// program; event payload stores both the source event and its immutable
// DecisionSnapshot for audit.
//
// RequiredSQLIndexes exposes the complete constraint and query-index contract
// for these tables. It returns metadata only; hosts remain responsible for
// migration execution, index naming policy, and database-specific DDL.
type SQLTableNames struct {
	Templates       string
	Programs        string
	Attributions    string
	Events          string
	Earnings        string
	Journals        string
	Outbox          string
	Settlements     string
	SettlementItems string
}

// SQLIndexSpec describes one host-managed SQL uniqueness constraint or query
// index required by SQLStore. Hosts MAY implement a Unique specification as a
// named UNIQUE constraint or a UNIQUE index. Predicate is empty for a regular
// index and contains a portable SQL condition only when a partial index is the
// required dialect-specific shape.
//
// RequiredSQLIndexes returns freshly allocated values, so callers may adjust
// the returned slice for migration-tool input without mutating package state.
type SQLIndexSpec struct {
	// Name is the stable recommended identifier for the constraint or index.
	Name string

	// Table is the validated, host-configured table to which the specification applies.
	Table string

	// Columns are ordered left-to-right as required by the query or constraint.
	Columns []string

	// Unique requires a uniqueness constraint or unique index.
	Unique bool

	// Predicate is a partial-index predicate when supported by the requested dialect.
	Predicate string

	// Purpose ties the requirement to a SQLStore correctness or query path.
	Purpose string
}

// DefaultSQLTableNames returns a copy of the table names SQLStore uses when no
// WithSQLTableNames option is supplied. It is provided so hosts can obtain the
// matching RequiredSQLIndexes contract before creating a store.
func DefaultSQLTableNames() SQLTableNames {
	return defaultSQLTableNames()
}

func defaultSQLTableNames() SQLTableNames {
	return SQLTableNames{
		Templates:       DefaultSQLTemplateTableName,
		Programs:        DefaultSQLProgramTableName,
		Attributions:    DefaultSQLAttributionTableName,
		Events:          DefaultSQLEventTableName,
		Earnings:        DefaultSQLEarningTableName,
		Journals:        DefaultSQLJournalTableName,
		Outbox:          DefaultSQLOutboxTableName,
		Settlements:     DefaultSQLSettlementTableName,
		SettlementItems: DefaultSQLSettlementItemTableName,
	}
}

// RequiredSQLIndexes returns the constraints and indexes a host migration MUST
// create for the supplied SQLStore tables. The function never executes DDL.
//
// The returned contract is dialect-aware only where it materially changes the
// index shape: PostgreSQL and SQLite use a partial unpublished-outbox index,
// while MySQL uses published_at as a leading key column because it has no
// portable partial-index equivalent. A zero dialect defaults to MySQL, matching
// NewSQLStore.
func RequiredSQLIndexes(names SQLTableNames, dialect SQLDialect) ([]SQLIndexSpec, error) {
	if err := validateSQLTableNames(names); err != nil {
		return nil, err
	}

	normalizedDialect, ok := sqlutil.NormalizeDialect(dialect)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedSQLDialect, dialect)
	}

	indexes := []SQLIndexSpec{
		{
			Name:    "commission_templates_id_version_uq",
			Table:   names.Templates,
			Columns: []string{"id", "version"},
			Unique:  true,
			Purpose: "template version identity and current-template lookup",
		},
		{
			Name:    "commission_programs_tenant_id_uq",
			Table:   names.Programs,
			Columns: []string{"tenant_id", "id"},
			Unique:  true,
			Purpose: "tenant-scoped program identity",
		},
		{
			Name:    "commission_attributions_tenant_program_slot_uq",
			Table:   names.Attributions,
			Columns: []string{"tenant_id", "program_id", "slot"},
			Unique:  true,
			Purpose: "attribution identity and program-slot lookup",
		},
		{
			Name:    "commission_events_source_uq",
			Table:   names.Events,
			Columns: []string{"tenant_id", "source_type", "source_id"},
			Unique:  true,
			Purpose: "source-event financial idempotency",
		},
		{
			Name:    "commission_earnings_tenant_id_uq",
			Table:   names.Earnings,
			Columns: []string{"tenant_id", "id"},
			Unique:  true,
			Purpose: "tenant-scoped earning identity and default tenant cursor listing",
		},
		{
			Name:    "commission_journals_id_uq",
			Table:   names.Journals,
			Columns: []string{"id"},
			Unique:  true,
			Purpose: "globally unique immutable accounting-entry identity",
		},
		{
			Name:    "commission_outbox_tenant_id_uq",
			Table:   names.Outbox,
			Columns: []string{"tenant_id", "id"},
			Unique:  true,
			Purpose: "tenant-scoped transactional-outbox identity",
		},
		{
			Name:    "commission_settlements_tenant_id_uq",
			Table:   names.Settlements,
			Columns: []string{"tenant_id", "id"},
			Unique:  true,
			Purpose: "tenant-scoped settlement identity",
		},
		{
			Name:    "commission_settlement_items_settlement_earning_uq",
			Table:   names.SettlementItems,
			Columns: []string{"tenant_id", "settlement_id", "earning_id"},
			Unique:  true,
			Purpose: "settlement membership idempotency",
		},
		{
			Name:    "commission_earnings_source_idx",
			Table:   names.Earnings,
			Columns: []string{"tenant_id", "source_type", "source_id", "id"},
			Purpose: "source-event duplicate result lookup",
		},
		{
			Name:    "commission_earnings_due_idx",
			Table:   names.Earnings,
			Columns: []string{"status", "available_at", "id"},
			Purpose: "global pending-earning due scan",
		},
		{
			Name:    "commission_earnings_tenant_program_idx",
			Table:   names.Earnings,
			Columns: []string{"tenant_id", "program_id", "id"},
			Purpose: "tenant earning listing filtered by program",
		},
		{
			Name:    "commission_earnings_tenant_beneficiary_idx",
			Table:   names.Earnings,
			Columns: []string{"tenant_id", "beneficiary_kind", "beneficiary_id", "id"},
			Purpose: "tenant earning listing filtered by beneficiary",
		},
		{
			Name:    "commission_earnings_tenant_status_idx",
			Table:   names.Earnings,
			Columns: []string{"tenant_id", "status", "id"},
			Purpose: "tenant earning listing filtered by status",
		},
		{
			Name:    "commission_journals_tenant_earning_history_idx",
			Table:   names.Journals,
			Columns: []string{"tenant_id", "earning_id", "created_at", "id"},
			Purpose: "immutable earning journal history",
		},
		{
			Name:    "commission_outbox_tenant_cursor_idx",
			Table:   names.Outbox,
			Columns: []string{"tenant_id", "created_at", "id"},
			Purpose: "tenant transactional-outbox cursor listing",
		},
		{
			Name:    "commission_settlements_tenant_status_idx",
			Table:   names.Settlements,
			Columns: []string{"tenant_id", "status", "updated_at", "id"},
			Purpose: "tenant settlement status reconciliation",
		},
		{
			Name:    "commission_settlement_items_earning_idx",
			Table:   names.SettlementItems,
			Columns: []string{"tenant_id", "earning_id", "settlement_id"},
			Purpose: "reverse earning-to-settlement ownership lookup",
		},
	}

	unpublishedOutbox := SQLIndexSpec{
		Name:    "commission_outbox_unpublished_idx",
		Table:   names.Outbox,
		Purpose: "unpublished transactional-outbox cursor listing",
	}
	if normalizedDialect == SQLDialectPostgres || normalizedDialect == SQLDialectSQLite {
		unpublishedOutbox.Columns = []string{"tenant_id", "created_at", "id"}
		unpublishedOutbox.Predicate = "published_at IS NULL"
	} else {
		unpublishedOutbox.Columns = []string{"tenant_id", "published_at", "created_at", "id"}
	}
	indexes = append(indexes, unpublishedOutbox)

	return indexes, nil
}

func validateSQLTableNames(names SQLTableNames) error {
	for _, value := range []string{
		names.Templates,
		names.Programs,
		names.Attributions,
		names.Events,
		names.Earnings,
		names.Journals,
		names.Outbox,
		names.Settlements,
		names.SettlementItems,
	} {
		if !sqlutil.IsSafeQualifiedIdentifier(value) {
			return fmt.Errorf("%w: %q", ErrInvalidTableName, value)
		}
	}
	return nil
}

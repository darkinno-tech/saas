package migration

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/im10furry/saas/core/types"
)

const (
	DefaultTenantField     = "tenant_id"
	DefaultTenantFieldType = "VARCHAR(64)"
	DefaultSoftDeleteField = "deleted_at"
	DefaultDeleteMarker    = "deleted_flag"
)

// Planner generates migration SQL without executing it.
type Planner struct {
	Dialect Dialect
}

// NewPlanner creates a migration planner.
func NewPlanner(dialect Dialect) Planner {
	return Planner{Dialect: dialect}
}

// AddTenantColumn returns SQL to add a tenant_id column.
func (planner Planner) AddTenantColumn(table string, tenantField string, fieldType string) (string, error) {
	if err := planner.validateDialect(); err != nil {
		return "", err
	}
	if tenantField == "" {
		tenantField = DefaultTenantField
	}
	if fieldType == "" {
		fieldType = DefaultTenantFieldType
	}
	if !isSafeQualifiedIdentifier(table) || !isSafeIdentifier(tenantField) {
		return "", ErrInvalidIdentifier
	}
	if strings.ContainsAny(fieldType, ";") {
		return "", ErrInvalidMigration
	}
	return fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, tenantField, fieldType), nil
}

// CreateSoftDeleteUniqueIndex returns SQL for a soft-delete-aware unique index.
func (planner Planner) CreateSoftDeleteUniqueIndex(table string, indexName string, tenantField string, businessFields []string, softDeleteField string) (string, error) {
	if err := planner.validateDialect(); err != nil {
		return "", err
	}
	if softDeleteField == "" {
		softDeleteField = DefaultSoftDeleteField
	}
	if planner.Dialect == DialectMySQL {
		return planner.CreateMySQLSoftDeleteUniqueIndex(table, indexName, tenantField, businessFields, DefaultDeleteMarker)
	}

	columns, err := indexColumns(tenantField, businessFields)
	if err != nil {
		return "", err
	}
	if !isSafeQualifiedIdentifier(table) || !isSafeIdentifier(indexName) || !isSafeIdentifier(softDeleteField) {
		return "", ErrInvalidIdentifier
	}
	return fmt.Sprintf("CREATE UNIQUE INDEX %s ON %s (%s) WHERE %s IS NULL", indexName, table, strings.Join(columns, ", "), softDeleteField), nil
}

// CreateHardDeleteUniqueIndex returns SQL for a hard-delete table unique index.
func (planner Planner) CreateHardDeleteUniqueIndex(table string, indexName string, tenantField string, businessFields []string) (string, error) {
	if err := planner.validateDialect(); err != nil {
		return "", err
	}
	columns, err := indexColumns(tenantField, businessFields)
	if err != nil {
		return "", err
	}
	if !isSafeQualifiedIdentifier(table) || !isSafeIdentifier(indexName) {
		return "", ErrInvalidIdentifier
	}
	return fmt.Sprintf("CREATE UNIQUE INDEX %s ON %s (%s)", indexName, table, strings.Join(columns, ", ")), nil
}

// CreateMySQLSoftDeleteUniqueIndex returns SQL using a non-nullable delete marker.
func (planner Planner) CreateMySQLSoftDeleteUniqueIndex(table string, indexName string, tenantField string, businessFields []string, markerField string) (string, error) {
	if planner.Dialect != DialectMySQL {
		return "", ErrUnsupportedDialect
	}
	if markerField == "" {
		markerField = DefaultDeleteMarker
	}
	columns, err := indexColumns(tenantField, businessFields)
	if err != nil {
		return "", err
	}
	if !isSafeQualifiedIdentifier(table) || !isSafeIdentifier(indexName) || !isSafeIdentifier(markerField) {
		return "", ErrInvalidIdentifier
	}
	columns = append(columns, markerField)
	return fmt.Sprintf("CREATE UNIQUE INDEX %s ON %s (%s)", indexName, table, strings.Join(columns, ", ")), nil
}

// SeedTenants returns parameterized inserts for tenant seed data.
func (planner Planner) SeedTenants(table string, tenants []types.Tenant) ([]Statement, error) {
	if err := planner.validateDialect(); err != nil {
		return nil, err
	}
	if !isSafeQualifiedIdentifier(table) {
		return nil, ErrInvalidIdentifier
	}

	statements := make([]Statement, 0, len(tenants))
	for _, tenant := range tenants {
		if tenant.ID == "" || tenant.Status == "" {
			return nil, ErrInvalidMigration
		}
		config := tenant.Config
		if config == nil {
			config = map[string]string{}
		}
		encodedConfig, err := json.Marshal(config)
		if err != nil {
			return nil, ErrInvalidMigration
		}
		statements = append(statements, Statement{
			SQL:  fmt.Sprintf("INSERT INTO %s (id, name, status, plan_id, config) VALUES (%s)", table, planner.placeholders(5)),
			Args: []any{tenant.ID.String(), tenant.Name, string(tenant.Status), tenant.PlanID, string(encodedConfig)},
		})
	}
	return statements, nil
}

func (planner Planner) placeholders(count int) string {
	parts := make([]string, count)
	for i := range parts {
		if planner.Dialect == DialectPostgres {
			parts[i] = fmt.Sprintf("$%d", i+1)
		} else {
			parts[i] = "?"
		}
	}
	return strings.Join(parts, ", ")
}

// DetectDeletePolicy returns "soft" when softDeleteField exists, otherwise "hard".
func DetectDeletePolicy(columns []string, softDeleteField string) string {
	if softDeleteField == "" {
		softDeleteField = DefaultSoftDeleteField
	}
	for _, column := range columns {
		if column == softDeleteField {
			return "soft"
		}
	}
	return "hard"
}

func (planner Planner) validateDialect() error {
	switch planner.Dialect {
	case DialectPostgres, DialectMySQL, DialectSQLite:
		return nil
	default:
		return ErrUnsupportedDialect
	}
}

func indexColumns(tenantField string, businessFields []string) ([]string, error) {
	if tenantField == "" {
		tenantField = DefaultTenantField
	}
	if len(businessFields) == 0 {
		return nil, ErrInvalidMigration
	}
	if !isSafeIdentifier(tenantField) {
		return nil, ErrInvalidIdentifier
	}

	columns := make([]string, 0, 1+len(businessFields))
	columns = append(columns, tenantField)
	for _, field := range businessFields {
		if !isSafeIdentifier(field) {
			return nil, ErrInvalidIdentifier
		}
		columns = append(columns, field)
	}
	return columns, nil
}

func isSafeQualifiedIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for _, part := range strings.Split(value, ".") {
		if !isSafeIdentifier(part) {
			return false
		}
	}
	return true
}

func isSafeIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for i, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_' {
			continue
		}
		if i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}

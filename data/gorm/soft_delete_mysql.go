package gormtenant

import (
	"fmt"

	"github.com/im10furry/saas/data"
)

const (
	// DefaultMySQLSoftDeleteMarkerField is a non-nullable delete marker suitable for MySQL unique indexes.
	DefaultMySQLSoftDeleteMarkerField = "deleted_flag"
)

// MySQLSoftDeleteUniqueIndex describes a MySQL-compatible unique index for soft-delete tables.
type MySQLSoftDeleteUniqueIndex struct {
	TenantField string
	Fields      []string
	MarkerField string
}

// Columns returns the ordered unique-index columns.
func (index MySQLSoftDeleteUniqueIndex) Columns() []string {
	columns := make([]string, 0, 2+len(index.Fields))
	columns = append(columns, index.TenantField)
	columns = append(columns, index.Fields...)
	columns = append(columns, index.MarkerField)
	return columns
}

// NewMySQLSoftDeleteUniqueIndex creates a MySQL-safe unique-index column plan.
//
// MySQL does not support partial indexes equivalent to
// UNIQUE (...) WHERE deleted_at IS NULL. Use a non-nullable marker column or
// generated column instead of putting nullable deleted_at directly in the
// unique index.
func NewMySQLSoftDeleteUniqueIndex(tenantField string, businessFields []string, markerField string) (MySQLSoftDeleteUniqueIndex, error) {
	if tenantField == "" {
		tenantField = data.DefaultTenantField
	}
	if markerField == "" {
		markerField = DefaultMySQLSoftDeleteMarkerField
	}
	if !isSafeFieldName(tenantField) {
		return MySQLSoftDeleteUniqueIndex{}, fmt.Errorf("%w: %q", data.ErrInvalidFieldName, tenantField)
	}
	if !isSafeFieldName(markerField) {
		return MySQLSoftDeleteUniqueIndex{}, fmt.Errorf("%w: %q", data.ErrInvalidFieldName, markerField)
	}
	if len(businessFields) == 0 {
		return MySQLSoftDeleteUniqueIndex{}, fmt.Errorf("%w: at least one business field is required", data.ErrInvalidFieldName)
	}

	fields := make([]string, len(businessFields))
	for i, field := range businessFields {
		if !isSafeFieldName(field) {
			return MySQLSoftDeleteUniqueIndex{}, fmt.Errorf("%w: %q", data.ErrInvalidFieldName, field)
		}
		fields[i] = field
	}

	return MySQLSoftDeleteUniqueIndex{
		TenantField: tenantField,
		Fields:      fields,
		MarkerField: markerField,
	}, nil
}

func isSafeFieldName(value string) bool {
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

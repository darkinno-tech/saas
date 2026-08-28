package gormtenant

import (
	"reflect"
	"strings"

	"github.com/darkinno-tech/saas/core/types"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/schema"
)

func (plugin *Plugin) guardUpdate(tx *gorm.DB) {
	if tx.Statement.SQL.Len() > 0 {
		plugin.addTenantCondition(tx)
		return
	}

	filter, err := plugin.newFilter(tx.Statement.Context)
	if err != nil {
		addDBError(tx, err)
		return
	}
	if filter.IsHost() {
		return
	}
	tenantID, ok := filter.TenantID()
	if !ok {
		addDBError(tx, ErrTenantFieldNotFound)
		return
	}
	if plugin.invalidTenantFieldUpdate(tx.Statement, tenantID) {
		addDBError(tx, ErrTenantFieldUpdate)
		return
	}
	plugin.addTenantCondition(tx)
}

func (plugin *Plugin) invalidTenantFieldUpdate(statement *gorm.Statement, tenantID types.TenantID) bool {
	if statement == nil {
		return false
	}
	tenantField := plugin.tenantSchemaField(statement.Schema)

	if setClause, ok := statement.Clauses["SET"]; ok {
		assigner, ok := setClause.Expression.(clause.Assigner)
		if !ok {
			// An opaque SET clause cannot be proven not to modify the partition key.
			return true
		}
		for _, assignment := range assigner.Assignments() {
			if plugin.isTenantFieldName(assignment.Column.Name, tenantField) && !tenantUpdateValueMatches(assignment.Value, tenantID) {
				return true
			}
		}
	}

	value := reflect.ValueOf(statement.Dest)
	for value.IsValid() && (value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface) {
		if value.IsNil() {
			return false
		}
		value = value.Elem()
	}
	if !value.IsValid() {
		return false
	}

	if value.Kind() == reflect.Map && value.Type().Key().Kind() == reflect.String {
		for _, key := range value.MapKeys() {
			name := key.String()
			if plugin.isTenantFieldName(name, tenantField) && updateColumnIncluded(statement, name, tenantField) {
				if !tenantUpdateValueMatches(value.MapIndex(key).Interface(), tenantID) {
					return true
				}
			}
		}
		return false
	}
	if value.Kind() != reflect.Struct || tenantField == nil || !tenantField.Updatable {
		return false
	}

	parsed := &gorm.Statement{DB: statement.DB}
	if err := parsed.Parse(statement.Dest); err != nil || parsed.Schema == nil {
		return false
	}
	destinationField := parsed.Schema.LookUpField(tenantField.DBName)
	if destinationField == nil {
		destinationField = parsed.Schema.LookUpField(tenantField.Name)
	}
	if destinationField == nil || !destinationField.Updatable {
		return false
	}

	selected, explicit := updateColumnSelection(statement, tenantField.DBName)
	if !selected {
		return false
	}
	fieldValue, zero := destinationField.ValueOf(statement.Context, value)
	if !explicit && zero {
		return false
	}
	return !tenantUpdateValueMatches(fieldValue, tenantID)
}

func tenantUpdateValueMatches(value any, tenantID types.TenantID) bool {
	reflected := reflect.ValueOf(value)
	for reflected.IsValid() && (reflected.Kind() == reflect.Pointer || reflected.Kind() == reflect.Interface) {
		if reflected.IsNil() {
			return false
		}
		reflected = reflected.Elem()
	}
	if !reflected.IsValid() {
		return false
	}
	stringType := reflect.TypeOf("")
	tenantIDType := reflect.TypeOf(types.TenantID(""))
	return (reflected.Type() == stringType || reflected.Type() == tenantIDType) && reflected.String() == tenantID.String()
}

func (plugin *Plugin) tenantSchemaField(model *schema.Schema) *schema.Field {
	if model == nil {
		return nil
	}
	if field := lookupField(model, plugin.config.TenantField); field != nil {
		return field
	}
	return lookupField(model, unqualifiedFieldName(plugin.config.TenantField))
}

func (plugin *Plugin) isTenantFieldName(name string, field *schema.Field) bool {
	name = unqualifiedFieldName(name)
	if strings.EqualFold(name, unqualifiedFieldName(plugin.config.TenantField)) {
		return true
	}
	return field != nil && (strings.EqualFold(name, field.DBName) || strings.EqualFold(name, field.Name))
}

func updateColumnIncluded(statement *gorm.Statement, name string, field *schema.Field) bool {
	if field != nil && !field.Updatable {
		return false
	}
	column := name
	if field != nil {
		column = field.DBName
	}
	selected, _ := updateColumnSelection(statement, column)
	return selected
}

func updateColumnSelection(statement *gorm.Statement, column string) (selected, explicit bool) {
	columns, restricted := statement.SelectAndOmitColumns(false, true)
	if selected, explicit = columns[column]; explicit {
		return selected, true
	}
	return !restricted, false
}

func unqualifiedFieldName(name string) string {
	name = strings.TrimSpace(name)
	if index := strings.LastIndexByte(name, '.'); index >= 0 {
		name = name[index+1:]
	}
	return strings.Trim(name, "`\"[]")
}

package gormtenant

import (
	"fmt"
	"reflect"

	"github.com/darkinno-tech/saas/core/types"

	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

func (plugin *Plugin) fillTenantOnCreate(tx *gorm.DB) {
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
	if err := plugin.setTenantField(tx, tenantID); err != nil {
		addDBError(tx, err)
	}
}

func (plugin *Plugin) setTenantField(tx *gorm.DB, tenantID types.TenantID) error {
	if tx.Statement.Schema == nil {
		return ErrTenantFieldNotFound
	}

	field := lookupField(tx.Statement.Schema, plugin.config.TenantField)
	if field == nil {
		return ErrTenantFieldNotFound
	}

	value := dereference(tx.Statement.ReflectValue)
	switch value.Kind() {
	case reflect.Struct:
		return setTenantFieldValue(tx, field, value, tenantID)
	case reflect.Slice, reflect.Array:
		for i := 0; i < value.Len(); i++ {
			item := dereference(value.Index(i))
			if item.Kind() != reflect.Struct {
				return ErrTenantFieldNotFound
			}
			if err := setTenantFieldValue(tx, field, item, tenantID); err != nil {
				return err
			}
		}
		return nil
	default:
		return ErrTenantFieldNotFound
	}
}

func setTenantFieldValue(tx *gorm.DB, field *schema.Field, value reflect.Value, tenantID types.TenantID) error {
	if !value.IsValid() || !value.CanAddr() {
		return ErrTenantFieldNotFound
	}

	current, zero := field.ValueOf(tx.Statement.Context, value)
	if !zero && fmt.Sprint(current) != tenantID.String() {
		return ErrTenantMismatch
	}
	if !zero {
		return nil
	}

	if err := field.Set(tx.Statement.Context, value, tenantID.String()); err == nil {
		return nil
	}
	return field.Set(tx.Statement.Context, value, tenantID)
}

func lookupField(model *schema.Schema, tenantField string) *schema.Field {
	if field := model.LookUpField(tenantField); field != nil {
		return field
	}
	for _, field := range model.Fields {
		if field.DBName == tenantField || field.Name == tenantField {
			return field
		}
	}
	return nil
}

func dereference(value reflect.Value) reflect.Value {
	for value.IsValid() && value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return value
		}
		value = value.Elem()
	}
	return value
}

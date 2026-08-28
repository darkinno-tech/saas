package gormtenant

import (
	"errors"

	"github.com/darkinno-tech/saas"

	"gorm.io/gorm"
)

var (
	// ErrTenantFieldNotFound reports that a model does not expose the configured tenant field.
	ErrTenantFieldNotFound = errors.New("saas/gorm: tenant field not found")

	// ErrTenantMismatch reports that a model already contains a different tenant ID.
	ErrTenantMismatch = saas.ErrTenantMismatch

	// ErrTenantFieldUpdate reports an attempt to update the tenant partition key from a tenant context.
	ErrTenantFieldUpdate = errors.New("saas/gorm: tenant field cannot be updated in tenant context")

	// ErrUnscopedRequiresHost reports that Unscoped is forbidden in tenant context.
	ErrUnscopedRequiresHost = errors.New("saas/gorm: unscoped requires host context")

	// ErrRawRequiresHost reports that raw SQL requires explicit host context.
	ErrRawRequiresHost = errors.New("saas/gorm: raw SQL requires host context")
)

func addDBError(db *gorm.DB, err error) {
	if err == nil {
		return
	}
	if added := db.AddError(err); added != nil {
		db.Error = added
	}
}

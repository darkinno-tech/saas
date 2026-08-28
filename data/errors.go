package data

import (
	"errors"

	"github.com/im10furry/saas"
)

var (
	// ErrNoTenant reports that a tenant-scoped filter was requested without tenant context.
	ErrNoTenant = saas.ErrNoTenant

	// ErrInvalidFieldName reports an unsafe field name in filter options.
	ErrInvalidFieldName = errors.New("saas/data: invalid field name")
)

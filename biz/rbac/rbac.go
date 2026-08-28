package rbac

import "github.com/darkinno-tech/saas/core/types"

type Permission string

type Role struct {
	TenantID    types.TenantID
	Key         string
	Permissions []Permission
}

// HasPermission reports whether the role grants permission.
func (role Role) HasPermission(permission Permission) bool {
	if permission == "" {
		return false
	}
	for _, candidate := range role.Permissions {
		if candidate == permission {
			return true
		}
	}
	return false
}

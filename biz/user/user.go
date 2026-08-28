package user

import "github.com/darkinno-tech/saas/core/types"

// User is an account that may belong to many tenants.
type User struct {
	ID    string
	Email string
	Name  string
}

// Member binds a user to a tenant.
type Member struct {
	TenantID types.TenantID
	UserID   string
	Roles    []string
}

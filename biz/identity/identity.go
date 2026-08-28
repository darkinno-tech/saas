package identity

import (
	"maps"

	"github.com/darkinno-tech/saas/core/types"
)

// Assertion is a verified external identity claim. Applications must validate
// OAuth/OIDC tokens, magic links, or SAML assertions before constructing it.
type Assertion struct {
	TenantID      types.TenantID
	Provider      ProviderKey
	Subject       string
	UserID        string
	Email         string
	Name          string
	EmailVerified bool
	Roles         []string
	Metadata      map[string]string
}

// Link binds one provider subject to one user inside one tenant.
type Link struct {
	TenantID      types.TenantID
	UserID        string
	Provider      ProviderKey
	Subject       string
	Email         string
	Name          string
	EmailVerified bool
	Metadata      map[string]string
}

// Session is the tenant membership produced after accepting an assertion.
type Session struct {
	TenantID types.TenantID
	UserID   string
	Provider ProviderKey
	Subject  string
	Email    string
	Name     string
	Roles    []string
}

func (assertion Assertion) validate(requireVerifiedEmail bool) error {
	if assertion.TenantID == "" || assertion.Provider == "" || assertion.Subject == "" || assertion.Email == "" {
		return ErrInvalidIdentity
	}
	if requireVerifiedEmail && !assertion.EmailVerified {
		return ErrUnverifiedEmail
	}
	return nil
}

func (link Link) validate() error {
	if link.TenantID == "" || link.UserID == "" || link.Provider == "" || link.Subject == "" || link.Email == "" {
		return ErrInvalidIdentity
	}
	return nil
}

func cloneLink(link Link) Link {
	link.Metadata = cloneStringMap(link.Metadata)
	return link
}

func linksEqual(a Link, b Link) bool {
	return a.TenantID == b.TenantID &&
		a.UserID == b.UserID &&
		a.Provider == b.Provider &&
		a.Subject == b.Subject &&
		a.Email == b.Email &&
		a.Name == b.Name &&
		a.EmailVerified == b.EmailVerified &&
		maps.Equal(a.Metadata, b.Metadata)
}

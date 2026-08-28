package resolver

import (
	"net/http"

	"github.com/im10furry/saas/core/types"
)

// Resolver resolves a tenant identifier from an HTTP request.
type Resolver interface {
	Resolve(r *http.Request) (types.TenantID, error)
}

// Contrib attempts to resolve a tenant identifier from one request source.
type Contrib interface {
	Resolve(r *http.Request) (types.TenantID, bool, error)
}

// Composite resolves tenants by trying contributors in order.
type Composite struct {
	contribs []Contrib
}

var _ Resolver = (*Composite)(nil)

// NewComposite creates a priority-ordered resolver.
func NewComposite(contribs ...Contrib) *Composite {
	copied := make([]Contrib, 0, len(contribs))
	for _, contrib := range contribs {
		if contrib != nil {
			copied = append(copied, contrib)
		}
	}
	return &Composite{contribs: copied}
}

// Resolve returns the first tenant identifier produced by a contributor.
func (resolver *Composite) Resolve(r *http.Request) (types.TenantID, error) {
	if r == nil {
		return "", ErrNilRequest
	}

	for _, contrib := range resolver.contribs {
		id, ok, err := contrib.Resolve(r)
		if err != nil {
			return "", err
		}
		if ok {
			return id, nil
		}
	}
	return "", ErrNoTenant
}

package resolver

import (
	"errors"
	"net/http"

	"github.com/im10furry/saas/core/types"
)

const (
	// DefaultCookieName is the default cookie used for tenant resolution.
	DefaultCookieName = "tenant_id"
)

// CookieContrib resolves tenant IDs from an HTTP cookie.
type CookieContrib struct {
	name     string
	strategy types.TenantIDStrategy
}

var _ Contrib = CookieContrib{}

// NewCookieContrib creates a cookie-based resolver contributor.
func NewCookieContrib(name string, strategy types.TenantIDStrategy) CookieContrib {
	if name == "" {
		name = DefaultCookieName
	}
	return CookieContrib{name: name, strategy: strategy}
}

// Resolve reads tenant ID from the configured cookie.
func (contrib CookieContrib) Resolve(r *http.Request) (types.TenantID, bool, error) {
	if r == nil {
		return "", false, ErrNilRequest
	}

	cookie, err := r.Cookie(contrib.name)
	if errors.Is(err, http.ErrNoCookie) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}

	id, err := parseTenantID(cookie.Value, contrib.strategy)
	if err != nil {
		return "", false, err
	}
	return id, true, nil
}

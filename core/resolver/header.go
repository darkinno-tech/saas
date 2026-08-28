package resolver

import (
	"net/http"

	"github.com/darkinno-tech/saas/core/types"
)

const (
	// DefaultHeaderName is the default request header used for tenant resolution.
	DefaultHeaderName = "x-tenant-id"
)

// HeaderContrib resolves tenant IDs from an HTTP header.
type HeaderContrib struct {
	name     string
	strategy types.TenantIDStrategy
}

var _ Contrib = HeaderContrib{}

// NewHeaderContrib creates a header-based resolver contributor.
func NewHeaderContrib(name string, strategy types.TenantIDStrategy) HeaderContrib {
	if name == "" {
		name = DefaultHeaderName
	}
	return HeaderContrib{name: name, strategy: strategy}
}

// Resolve reads tenant ID from the configured header.
func (contrib HeaderContrib) Resolve(r *http.Request) (types.TenantID, bool, error) {
	if r == nil {
		return "", false, ErrNilRequest
	}

	raw, ok := headerValue(r, contrib.name)
	if !ok {
		return "", false, nil
	}

	id, err := parseTenantID(raw, contrib.strategy)
	if err != nil {
		return "", false, err
	}
	return id, true, nil
}

func headerValue(r *http.Request, name string) (string, bool) {
	values, ok := r.Header[http.CanonicalHeaderKey(name)]
	if !ok || len(values) == 0 {
		return "", false
	}
	return values[0], true
}

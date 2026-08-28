package resolver

import (
	"net/http"

	"github.com/darkinno-tech/saas/core/types"
)

const (
	// DefaultQueryParam is the default query parameter used for tenant resolution.
	DefaultQueryParam = "tenant_id"
)

// QueryContrib resolves tenant IDs from a URL query parameter.
type QueryContrib struct {
	name     string
	strategy types.TenantIDStrategy
}

var _ Contrib = QueryContrib{}

// NewQueryContrib creates a query-based resolver contributor.
func NewQueryContrib(name string, strategy types.TenantIDStrategy) QueryContrib {
	if name == "" {
		name = DefaultQueryParam
	}
	return QueryContrib{name: name, strategy: strategy}
}

// Resolve reads tenant ID from the configured query parameter.
func (contrib QueryContrib) Resolve(r *http.Request) (types.TenantID, bool, error) {
	if r == nil {
		return "", false, ErrNilRequest
	}
	if r.URL == nil {
		return "", false, ErrNilURL
	}

	values, ok := r.URL.Query()[contrib.name]
	if !ok || len(values) == 0 {
		return "", false, nil
	}

	id, err := parseTenantID(values[0], contrib.strategy)
	if err != nil {
		return "", false, err
	}
	return id, true, nil
}

package resolver

import (
	"net/http"

	"github.com/im10furry/saas/core/types"
)

// ClaimExtractor extracts a tenant claim from a request after authentication.
type ClaimExtractor func(r *http.Request) (raw string, ok bool, err error)

// TokenClaimContrib resolves tenant IDs from a caller-provided token claim extractor.
type TokenClaimContrib struct {
	extractor ClaimExtractor
	strategy  types.TenantIDStrategy
}

var _ Contrib = TokenClaimContrib{}

// NewTokenClaimContrib creates a token-claim resolver contributor.
func NewTokenClaimContrib(extractor ClaimExtractor, strategy types.TenantIDStrategy) TokenClaimContrib {
	return TokenClaimContrib{extractor: extractor, strategy: strategy}
}

// Resolve reads tenant ID through the configured claim extractor.
func (contrib TokenClaimContrib) Resolve(r *http.Request) (types.TenantID, bool, error) {
	if r == nil {
		return "", false, ErrNilRequest
	}
	if contrib.extractor == nil {
		return "", false, ErrNilClaimExtractor
	}

	raw, ok, err := contrib.extractor(r)
	if err != nil || !ok {
		return "", false, err
	}

	id, err := parseTenantID(raw, contrib.strategy)
	if err != nil {
		return "", false, err
	}
	return id, true, nil
}

package resolver

import (
	"errors"

	"github.com/im10furry/saas"
)

var (
	// ErrNoTenant reports that no resolver contributor found a tenant identifier.
	ErrNoTenant = saas.ErrNoTenant

	// ErrNilRequest reports a nil HTTP request.
	ErrNilRequest = errors.New("saas/resolver: nil request")

	// ErrNilURL reports a request whose URL is unavailable to a query resolver.
	ErrNilURL = errors.New("saas/resolver: nil request url")

	// ErrInvalidHost reports a host value that cannot be used for domain resolution.
	ErrInvalidHost = errors.New("saas/resolver: invalid host")

	// ErrNilClaimExtractor reports a nil token claim extractor.
	ErrNilClaimExtractor = errors.New("saas/resolver: nil claim extractor")
)

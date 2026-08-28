package resolver

import (
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/darkinno-tech/saas/core/types"
)

// DomainContrib resolves tenant IDs from single-label subdomains.
type DomainContrib struct {
	baseDomain string
	strategy   types.TenantIDStrategy
}

var _ Contrib = DomainContrib{}

// NewDomainContrib creates a subdomain-based resolver contributor.
func NewDomainContrib(baseDomain string, strategy types.TenantIDStrategy) DomainContrib {
	return DomainContrib{
		baseDomain: normalizeDomain(baseDomain),
		strategy:   strategy,
	}
}

// Resolve reads tenant ID from a host like tenant.example.com.
func (contrib DomainContrib) Resolve(r *http.Request) (types.TenantID, bool, error) {
	if r == nil {
		return "", false, ErrNilRequest
	}
	if contrib.baseDomain == "" {
		return "", false, ErrInvalidHost
	}

	host := normalizeDomain(hostWithoutPort(r.Host))
	if host == "" {
		return "", false, ErrInvalidHost
	}
	if host == contrib.baseDomain {
		return "", false, nil
	}

	suffix := "." + contrib.baseDomain
	if !strings.HasSuffix(host, suffix) {
		return "", false, nil
	}

	subdomain := strings.TrimSuffix(host, suffix)
	if subdomain == "" || strings.Contains(subdomain, ".") {
		return "", false, fmt.Errorf("%w: expected one subdomain label before %q", ErrInvalidHost, contrib.baseDomain)
	}

	id, err := parseTenantID(subdomain, contrib.strategy)
	if err != nil {
		return "", false, err
	}
	return id, true, nil
}

func hostWithoutPort(host string) string {
	if value, _, err := net.SplitHostPort(host); err == nil {
		return value
	}
	return host
}

func normalizeDomain(domain string) string {
	return strings.ToLower(strings.Trim(strings.TrimSuffix(domain, "."), " \t\r\n"))
}

package resolver

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/im10furry/saas/core/types"
)

func FuzzCompositeResolverPriority(f *testing.F) {
	for _, seed := range []struct {
		query   string
		header  string
		sources byte
	}{
		{query: "query-tenant", header: "header-tenant", sources: 3},
		{query: "", header: "header-tenant", sources: 2},
		{query: "", header: "", sources: 0},
		{query: "", header: "header-tenant", sources: 3},
		{query: "", header: "", sources: 2},
	} {
		f.Add(seed.query, seed.header, seed.sources)
	}

	resolver := NewComposite(
		NewQueryContrib("", types.TenantIDStrategyString),
		NewHeaderContrib("", types.TenantIDStrategyString),
	)
	f.Fuzz(func(t *testing.T, queryValue, headerValue string, sources byte) {
		queryPresent := sources&1 != 0
		headerPresent := sources&2 != 0
		queryNormalized := strings.TrimSpace(queryValue)
		headerNormalized := strings.TrimSpace(headerValue)
		requestURL := "http://example.com"
		if queryPresent {
			requestURL += "?tenant_id=" + url.QueryEscape(queryValue)
		}
		req := httptest.NewRequest(http.MethodGet, requestURL, nil)
		if headerPresent {
			req.Header.Set(DefaultHeaderName, headerValue)
		}

		id, err := resolver.Resolve(req)
		switch {
		case queryPresent && queryNormalized == "":
			if !errors.Is(err, types.ErrEmptyTenantID) {
				t.Fatalf("Resolve(query=%q, header=%q, sources=%d) error = %v, want ErrEmptyTenantID", queryValue, headerValue, sources, err)
			}
		case queryPresent:
			if err != nil || id != types.TenantID(queryNormalized) {
				t.Fatalf("Resolve(query=%q, header=%q, sources=%d) = %q, %v; want query tenant", queryValue, headerValue, sources, id, err)
			}
		case headerPresent && headerNormalized == "":
			if !errors.Is(err, types.ErrEmptyTenantID) {
				t.Fatalf("Resolve(query=%q, header=%q, sources=%d) error = %v, want ErrEmptyTenantID", queryValue, headerValue, sources, err)
			}
		case headerPresent:
			if err != nil || id != types.TenantID(headerNormalized) {
				t.Fatalf("Resolve(query=%q, header=%q, sources=%d) = %q, %v; want header tenant", queryValue, headerValue, sources, id, err)
			}
		default:
			if !errors.Is(err, ErrNoTenant) {
				t.Fatalf("Resolve(query=%q, header=%q, sources=%d) error = %v, want ErrNoTenant", queryValue, headerValue, sources, err)
			}
		}
	})
}

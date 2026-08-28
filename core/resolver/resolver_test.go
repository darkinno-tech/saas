package resolver

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/darkinno-tech/saas/core/types"
)

func TestHeaderContrib(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://example.com", nil)
	req.Header.Set(DefaultHeaderName, " tenant-a ")

	id, ok, err := NewHeaderContrib("", types.TenantIDStrategyString).Resolve(req)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if !ok || id != "tenant-a" {
		t.Fatalf("Resolve() = %q, %v; want tenant-a, true", id, ok)
	}

	missing := httptest.NewRequest(http.MethodGet, "http://example.com", nil)
	if id, ok, err := NewHeaderContrib("", types.TenantIDStrategyString).Resolve(missing); err != nil || ok || id != "" {
		t.Fatalf("Resolve(missing) = %q, %v, %v; want empty, false, nil", id, ok, err)
	}

	invalid := httptest.NewRequest(http.MethodGet, "http://example.com", nil)
	invalid.Header.Set(DefaultHeaderName, "not-int")
	if _, _, err := NewHeaderContrib("", types.TenantIDStrategyInt).Resolve(invalid); !errors.Is(err, types.ErrInvalidTenantID) {
		t.Fatalf("Resolve(invalid) error = %v, want ErrInvalidTenantID", err)
	}
}

func TestCookieContrib(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://example.com", nil)
	req.AddCookie(&http.Cookie{Name: DefaultCookieName, Value: "tenant-a"})

	id, ok, err := NewCookieContrib("", types.TenantIDStrategyString).Resolve(req)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if !ok || id != "tenant-a" {
		t.Fatalf("Resolve() = %q, %v; want tenant-a, true", id, ok)
	}

	missing := httptest.NewRequest(http.MethodGet, "http://example.com", nil)
	if id, ok, err := NewCookieContrib("", types.TenantIDStrategyString).Resolve(missing); err != nil || ok || id != "" {
		t.Fatalf("Resolve(missing) = %q, %v, %v; want empty, false, nil", id, ok, err)
	}
}

func TestQueryContrib(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://example.com?tenant_id=tenant-a", nil)

	id, ok, err := NewQueryContrib("", types.TenantIDStrategyString).Resolve(req)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if !ok || id != "tenant-a" {
		t.Fatalf("Resolve() = %q, %v; want tenant-a, true", id, ok)
	}

	missing := httptest.NewRequest(http.MethodGet, "http://example.com", nil)
	if id, ok, err := NewQueryContrib("", types.TenantIDStrategyString).Resolve(missing); err != nil || ok || id != "" {
		t.Fatalf("Resolve(missing) = %q, %v, %v; want empty, false, nil", id, ok, err)
	}
}

func TestQueryContribRejectsRequestWithoutURL(t *testing.T) {
	request := &http.Request{Header: make(http.Header)}
	_, _, err := NewQueryContrib("", types.TenantIDStrategyString).Resolve(request)
	if !errors.Is(err, ErrNilURL) {
		t.Fatalf("Resolve(request without URL) error = %v, want ErrNilURL", err)
	}
}

func TestDomainContrib(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://tenant-a.example.com:8080/path", nil)

	id, ok, err := NewDomainContrib("example.com", types.TenantIDStrategyString).Resolve(req)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if !ok || id != "tenant-a" {
		t.Fatalf("Resolve() = %q, %v; want tenant-a, true", id, ok)
	}

	root := httptest.NewRequest(http.MethodGet, "http://example.com", nil)
	if id, ok, err := NewDomainContrib("example.com", types.TenantIDStrategyString).Resolve(root); err != nil || ok || id != "" {
		t.Fatalf("Resolve(root) = %q, %v, %v; want empty, false, nil", id, ok, err)
	}

	other := httptest.NewRequest(http.MethodGet, "http://tenant-a.other.com", nil)
	if id, ok, err := NewDomainContrib("example.com", types.TenantIDStrategyString).Resolve(other); err != nil || ok || id != "" {
		t.Fatalf("Resolve(other domain) = %q, %v, %v; want empty, false, nil", id, ok, err)
	}

	nested := httptest.NewRequest(http.MethodGet, "http://a.b.example.com", nil)
	if _, _, err := NewDomainContrib("example.com", types.TenantIDStrategyString).Resolve(nested); !errors.Is(err, ErrInvalidHost) {
		t.Fatalf("Resolve(nested) error = %v, want ErrInvalidHost", err)
	}
}

func TestTokenClaimContrib(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://example.com", nil)
	req.Header.Set("Authorization", "Bearer token")

	contrib := NewTokenClaimContrib(func(r *http.Request) (string, bool, error) {
		if r.Header.Get("Authorization") == "" {
			return "", false, nil
		}
		return "tenant-a", true, nil
	}, types.TenantIDStrategyString)

	id, ok, err := contrib.Resolve(req)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if !ok || id != "tenant-a" {
		t.Fatalf("Resolve() = %q, %v; want tenant-a, true", id, ok)
	}

	if _, _, err := NewTokenClaimContrib(nil, types.TenantIDStrategyString).Resolve(req); !errors.Is(err, ErrNilClaimExtractor) {
		t.Fatalf("Resolve(nil extractor) error = %v, want ErrNilClaimExtractor", err)
	}
}

func TestCompositeResolver(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://example.com?tenant_id=query-tenant", nil)
	req.Header.Set(DefaultHeaderName, "header-tenant")

	id, err := NewComposite(
		NewQueryContrib("", types.TenantIDStrategyString),
		NewHeaderContrib("", types.TenantIDStrategyString),
	).Resolve(req)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if id != "query-tenant" {
		t.Fatalf("Resolve() = %q, want query-tenant", id)
	}

	missing := httptest.NewRequest(http.MethodGet, "http://example.com", nil)
	if _, err := NewComposite(NewHeaderContrib("", types.TenantIDStrategyString)).Resolve(missing); !errors.Is(err, ErrNoTenant) {
		t.Fatalf("Resolve(missing) error = %v, want ErrNoTenant", err)
	}

	invalid := httptest.NewRequest(http.MethodGet, "http://example.com?tenant_id=query-tenant", nil)
	invalid.Header.Set(DefaultHeaderName, "not-int")
	if _, err := NewComposite(
		NewHeaderContrib("", types.TenantIDStrategyInt),
		NewQueryContrib("", types.TenantIDStrategyString),
	).Resolve(invalid); !errors.Is(err, types.ErrInvalidTenantID) {
		t.Fatalf("Resolve(invalid first contrib) error = %v, want ErrInvalidTenantID", err)
	}

	if _, err := NewComposite().Resolve(nil); !errors.Is(err, ErrNilRequest) {
		t.Fatalf("Resolve(nil) error = %v, want ErrNilRequest", err)
	}
}

func TestContribsRejectNilRequest(t *testing.T) {
	tests := []struct {
		name    string
		contrib Contrib
	}{
		{name: "header", contrib: NewHeaderContrib("", types.TenantIDStrategyString)},
		{name: "cookie", contrib: NewCookieContrib("", types.TenantIDStrategyString)},
		{name: "query", contrib: NewQueryContrib("", types.TenantIDStrategyString)},
		{name: "domain", contrib: NewDomainContrib("example.com", types.TenantIDStrategyString)},
		{name: "token", contrib: NewTokenClaimContrib(func(r *http.Request) (string, bool, error) {
			return "tenant-a", true, nil
		}, types.TenantIDStrategyString)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, err := tt.contrib.Resolve(nil); !errors.Is(err, ErrNilRequest) {
				t.Fatalf("Resolve(nil) error = %v, want ErrNilRequest", err)
			}
		})
	}
}

package kratossaas

import (
	"context"
	"net/http"
	"testing"

	tenantctx "github.com/im10furry/saas/core/context"
	"github.com/im10furry/saas/core/resolver"
	"github.com/im10furry/saas/core/store"
	"github.com/im10furry/saas/core/types"

	kerrors "github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/middleware"
	"github.com/go-kratos/kratos/v2/transport"
)

func TestTenantMiddlewareRejectsInactiveTenantByDefault(t *testing.T) {
	backing := store.NewMemoryStore()
	if err := backing.Create(context.Background(), types.Tenant{ID: "tenant-a", Status: types.TenantStatusActive}); err != nil {
		t.Fatalf("Create(active) error = %v", err)
	}
	if err := backing.Create(context.Background(), types.Tenant{ID: "tenant-b", Status: types.TenantStatusSuspended}); err != nil {
		t.Fatalf("Create(suspended) error = %v", err)
	}

	handler := TenantMiddleware(resolver.NewComposite(resolver.NewHeaderContrib("", types.TenantIDStrategyString)), backing)(
		func(ctx context.Context, req any) (any, error) {
			tenant, ok := tenantctx.FromContext(ctx)
			if !ok {
				t.Fatal("tenant missing from context")
			}
			return tenant.ID.String(), nil
		},
	)

	response, err := handler(kratosContext("tenant-a"), nil)
	if err != nil {
		t.Fatalf("handler(active) error = %v", err)
	}
	if response != "tenant-a" {
		t.Fatalf("response = %v, want tenant-a", response)
	}

	_, err = handler(kratosContext("tenant-b"), nil)
	if reason := kerrors.Reason(err); reason != reasonTenantInactive {
		t.Fatalf("suspended reason = %s, want %s", reason, reasonTenantInactive)
	}
}

func TestTenantMiddlewareRejectsMissingTenant(t *testing.T) {
	handler := TenantMiddleware(resolver.NewComposite(resolver.NewHeaderContrib("", types.TenantIDStrategyString)), store.NewMemoryStore())(
		func(ctx context.Context, req any) (any, error) {
			return nil, nil
		},
	)

	_, err := handler(transport.NewServerContext(context.Background(), mockTransport{header: testHeader{}}), nil)
	if reason := kerrors.Reason(err); reason != reasonTenantRequired {
		t.Fatalf("missing tenant reason = %s, want %s", reason, reasonTenantRequired)
	}
}

func TestTenantMiddlewareReturnsTimeoutBeforeCallingTenantPage(t *testing.T) {
	called := false
	handler := TenantMiddleware(
		resolver.NewComposite(resolver.NewHeaderContrib("", types.TenantIDStrategyString)),
		timeoutTenantStore{err: context.DeadlineExceeded},
	)(func(context.Context, any) (any, error) {
		called = true
		return "unexpected", nil
	})

	_, err := handler(kratosContext("tenant-a"), nil)
	if code := kerrors.Code(err); code != http.StatusRequestTimeout {
		t.Fatalf("timeout code = %d, want %d", code, http.StatusRequestTimeout)
	}
	if reason := kerrors.Reason(err); reason != reasonTenantForbidden {
		t.Fatalf("timeout reason = %s, want %s", reason, reasonTenantForbidden)
	}
	if called {
		t.Fatal("tenant page handler was called after lookup timeout")
	}
}

func TestTenantMiddlewareUsesHTTPRequestFromServerContext(t *testing.T) {
	backing := store.NewMemoryStore()
	if err := backing.Create(context.Background(), types.Tenant{ID: "tenant-a", Status: types.TenantStatusActive}); err != nil {
		t.Fatalf("Create(active) error = %v", err)
	}

	handler := TenantMiddleware(resolver.NewComposite(resolver.NewHeaderContrib("", types.TenantIDStrategyString)), backing)(
		func(ctx context.Context, req any) (any, error) {
			tenant, ok := tenantctx.FromContext(ctx)
			if !ok {
				t.Fatal("tenant missing from context")
			}
			return tenant.ID.String(), nil
		},
	)

	request, err := http.NewRequest(http.MethodGet, "https://example.test/", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	request.Header.Set(resolver.DefaultHeaderName, "tenant-a")

	response, err := handler(transport.NewServerContext(context.Background(), httpRequestTransport{request: request}), nil)
	if err != nil {
		t.Fatalf("handler(http request context) error = %v", err)
	}
	if response != "tenant-a" {
		t.Fatalf("response = %v, want tenant-a", response)
	}
}

func TestTenantStatusGuard(t *testing.T) {
	handler := TenantStatusGuard()(func(ctx context.Context, req any) (any, error) {
		return "ok", nil
	})

	_, err := handler(context.Background(), nil)
	if reason := kerrors.Reason(err); reason != reasonTenantRequired {
		t.Fatalf("missing tenant reason = %s, want %s", reason, reasonTenantRequired)
	}

	_, err = handler(tenantctx.WithTenant(context.Background(), types.Tenant{
		ID:     "tenant-a",
		Status: types.TenantStatusSuspended,
	}), nil)
	if reason := kerrors.Reason(err); reason != reasonTenantInactive {
		t.Fatalf("inactive tenant reason = %s, want %s", reason, reasonTenantInactive)
	}

	response, err := handler(tenantctx.WithTenant(context.Background(), types.Tenant{
		ID:     "tenant-a",
		Status: types.TenantStatusActive,
	}), nil)
	if err != nil {
		t.Fatalf("active tenant handler error = %v", err)
	}
	if response != "ok" {
		t.Fatalf("response = %v, want ok", response)
	}
}

func TestHostGuard(t *testing.T) {
	handler := HostGuard()(func(ctx context.Context, req any) (any, error) {
		return "ok", nil
	})

	_, err := handler(context.Background(), nil)
	if reason := kerrors.Reason(err); reason != reasonHostRequired {
		t.Fatalf("without host reason = %s, want %s", reason, reasonHostRequired)
	}

	response, err := handler(tenantctx.WithHost(context.Background()), nil)
	if err != nil {
		t.Fatalf("host handler error = %v", err)
	}
	if response != "ok" {
		t.Fatalf("response = %v, want ok", response)
	}
}

func kratosContext(tenantID string) context.Context {
	return transport.NewServerContext(context.Background(), mockTransport{
		header: testHeader{resolver.DefaultHeaderName: []string{tenantID}},
	})
}

type mockTransport struct {
	header testHeader
}

func (mock mockTransport) Kind() transport.Kind {
	return transport.KindHTTP
}

func (mock mockTransport) Endpoint() string {
	return ""
}

func (mock mockTransport) Operation() string {
	return ""
}

func (mock mockTransport) RequestHeader() transport.Header {
	return mock.header
}

func (mock mockTransport) ReplyHeader() transport.Header {
	return testHeader{}
}

type httpRequestTransport struct {
	request *http.Request
}

func (httpRequestTransport) Kind() transport.Kind {
	return transport.KindHTTP
}

func (httpRequestTransport) Endpoint() string {
	return ""
}

func (httpRequestTransport) Operation() string {
	return ""
}

func (tr httpRequestTransport) RequestHeader() transport.Header {
	return testHeader(tr.request.Header)
}

func (httpRequestTransport) ReplyHeader() transport.Header {
	return testHeader{}
}

func (tr httpRequestTransport) Request() *http.Request {
	return tr.request
}

func (httpRequestTransport) PathTemplate() string {
	return ""
}

var _ middleware.Handler = func(context.Context, any) (any, error) { return nil, nil }

type testHeader map[string][]string

func (header testHeader) Get(key string) string {
	values := header.Values(key)
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func (header testHeader) Set(key string, value string) {
	header[key] = []string{value}
}

func (header testHeader) Add(key string, value string) {
	header[key] = append(header[key], value)
}

func (header testHeader) Keys() []string {
	keys := make([]string, 0, len(header))
	for key := range header {
		keys = append(keys, key)
	}
	return keys
}

func (header testHeader) Values(key string) []string {
	return append([]string(nil), header[key]...)
}

type timeoutTenantStore struct {
	err error
}

func (store timeoutTenantStore) Get(context.Context, types.TenantID) (types.Tenant, error) {
	return types.Tenant{}, store.err
}

func (timeoutTenantStore) List(context.Context, store.ListFilter) ([]types.Tenant, error) {
	return nil, context.DeadlineExceeded
}

func (timeoutTenantStore) Create(context.Context, types.Tenant) error {
	return context.DeadlineExceeded
}

func (timeoutTenantStore) Update(context.Context, types.Tenant) error {
	return context.DeadlineExceeded
}

func (timeoutTenantStore) Delete(context.Context, types.TenantID) error {
	return context.DeadlineExceeded
}

package oidc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	identity "github.com/darkinno-tech/saas/biz/identity"
)

func TestHandleLoginCallbackRetainsPendingLoginUntilValidCallback(t *testing.T) {
	tests := []struct {
		name           string
		callback       func(AuthRequest) *http.Request
		wantExactError error
	}{
		{
			name: "malformed form body",
			callback: func(AuthRequest) *http.Request {
				request := httptest.NewRequest(http.MethodPost, "/callback", strings.NewReader("state=%ZZ"))
				request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				return request
			},
		},
		{
			name: "duplicate authorization code",
			callback: func(authRequest AuthRequest) *http.Request {
				values := url.Values{"state": {authRequest.State}, "code": {authRequest.Nonce, "injected-code"}}
				return httptest.NewRequest(http.MethodGet, "/callback?"+values.Encode(), nil)
			},
			wantExactError: ErrDuplicateParam,
		},
		{
			name: "duplicate provider error",
			callback: func(authRequest AuthRequest) *http.Request {
				values := url.Values{"state": {authRequest.State}, "error": {"access_denied", "injected-error"}}
				return httptest.NewRequest(http.MethodGet, "/callback?"+values.Encode(), nil)
			},
			wantExactError: ErrDuplicateParam,
		},
		{
			name: "missing authorization code",
			callback: func(authRequest AuthRequest) *http.Request {
				return httptest.NewRequest(http.MethodGet, "/callback?"+url.Values{"state": {authRequest.State}}.Encode(), nil)
			},
			wantExactError: ErrInvalidCallback,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			server := newOIDCTestServer(t, testServerOptions{})
			client := newTestClient(t, server)
			store := NewMemoryLoginStore(DefaultLoginTTL)

			authRequest, err := client.BeginLogin(ctx, store, LoginRequest{TenantID: "tenant-a", Roles: []string{"member"}})
			if err != nil {
				t.Fatalf("BeginLogin() error = %v", err)
			}

			_, err = client.HandleLoginCallback(ctx, tt.callback(authRequest), store)
			if tt.wantExactError != nil {
				if !errors.Is(err, tt.wantExactError) {
					t.Fatalf("HandleLoginCallback() error = %v, want %v", err, tt.wantExactError)
				}
			} else if err == nil {
				t.Fatal("HandleLoginCallback() error = nil, want malformed form error")
			}
			if server.tokenCalls != 0 {
				t.Fatalf("token calls after invalid callback = %d, want 0", server.tokenCalls)
			}

			validRequest := httptest.NewRequest(http.MethodGet, "/callback?"+callbackValues(authRequest.Nonce, authRequest.State).Encode(), nil)
			if _, err := client.HandleLoginCallback(ctx, validRequest, store); err != nil {
				t.Fatalf("HandleLoginCallback(valid) error = %v, want saved login retained", err)
			}
			if _, err := client.HandleLoginCallback(ctx, validRequest, store); !errors.Is(err, ErrLoginNotFound) {
				t.Fatalf("HandleLoginCallback(replay) error = %v, want ErrLoginNotFound", err)
			}
			if server.tokenCalls != 1 {
				t.Fatalf("token calls after valid and replayed callback = %d, want 1", server.tokenCalls)
			}
		})
	}
}

func TestHandleCallbackRejectsNilRequestBeforeProviderExchange(t *testing.T) {
	server := newOIDCTestServer(t, testServerOptions{})
	client := newTestClient(t, server)
	authRequest, err := client.Begin()
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}

	_, err = client.HandleCallback(context.Background(), nil, authRequest, "tenant-a")
	if !errors.Is(err, ErrInvalidCallback) {
		t.Fatalf("HandleCallback(nil) error = %v, want ErrInvalidCallback", err)
	}
	if server.tokenCalls != 0 {
		t.Fatalf("token calls = %d, want nil request rejected before exchange", server.tokenCalls)
	}
}

func TestCallbackRejectsMissingIDTokenAndPropagatesTokenNetworkFailure(t *testing.T) {
	tests := []struct {
		name      string
		transport func(*http.Request) (*http.Response, error, bool)
		wantError error
	}{
		{
			name: "successful token response without id token",
			transport: func(request *http.Request) (*http.Response, error, bool) {
				if request.URL.Path != "/token" {
					return nil, nil, false
				}
				return oidcJSONResponse(t, request, http.StatusOK, map[string]any{
					"access_token": "access-token",
					"token_type":   "Bearer",
				}), nil, true
			},
			wantError: ErrTokenMissing,
		},
		{
			name: "provider network failure during token exchange",
			transport: func(request *http.Request) (*http.Response, error, bool) {
				if request.URL.Path != "/token" {
					return nil, nil, false
				}
				return nil, errOIDCTestProviderNetwork, true
			},
			wantError: errOIDCTestProviderNetwork,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newOIDCTestServer(t, testServerOptions{})
			client := newOIDCClientWithHTTPClient(t, server, &http.Client{Transport: oidcRouteTransport{base: http.DefaultTransport, route: tt.transport}}, false)
			authRequest, err := client.Begin()
			if err != nil {
				t.Fatalf("Begin() error = %v", err)
			}

			_, err = client.Callback(context.Background(), callbackForOIDCTest(authRequest))
			if !errors.Is(err, tt.wantError) {
				t.Fatalf("Callback() error = %v, want %v", err, tt.wantError)
			}
			if server.tokenCalls != 0 {
				t.Fatalf("server token calls = %d, want custom transport to handle exchange", server.tokenCalls)
			}
		})
	}
}

func TestCallbackCompletesProfileFromUserInfoAndPreservesProviderAttributes(t *testing.T) {
	server := newOIDCTestServer(t, testServerOptions{})
	var userInfoCalls atomic.Int32
	client := newOIDCClientWithHTTPClient(t, server, &http.Client{Transport: oidcRouteTransport{
		base: http.DefaultTransport,
		route: func(request *http.Request) (*http.Response, error, bool) {
			switch request.URL.Path {
			case "/token":
				rawIDToken := signedOIDCTestToken(t, server, request.FormValue("code"), map[string]any{
					"preferred_username": "tenant-display-name",
					"hd":                 "customer.example",
					"org_id":             "organization-42",
				})
				return oidcJSONResponse(t, request, http.StatusOK, map[string]any{
					"access_token": "access-token",
					"token_type":   "Bearer",
					"id_token":     rawIDToken,
				}), nil, true
			case "/userinfo":
				userInfoCalls.Add(1)
				return oidcJSONResponse(t, request, http.StatusOK, map[string]any{
					"sub":            "subject-1",
					"email":          "fallback@example.com",
					"email_verified": true,
					"given_name":     "Fallback",
					"family_name":    "User",
				}), nil, true
			default:
				return nil, nil, false
			}
		},
	}}, false)

	authRequest, err := client.Begin()
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	result, err := client.Callback(context.Background(), callbackForOIDCTest(authRequest))
	if err != nil {
		t.Fatalf("Callback() error = %v", err)
	}
	if userInfoCalls.Load() != 1 {
		t.Fatalf("UserInfo calls = %d, want 1 when ID token omits email", userInfoCalls.Load())
	}
	if result.Assertion.Email != "fallback@example.com" || !result.Assertion.EmailVerified {
		t.Fatalf("Assertion email = %q/%v, want verified fallback profile", result.Assertion.Email, result.Assertion.EmailVerified)
	}
	if result.Assertion.Name != "tenant-display-name" {
		t.Fatalf("Assertion name = %q, want preferred provider name", result.Assertion.Name)
	}
	if result.Assertion.Metadata["hosted_domain"] != "customer.example" || result.Assertion.Metadata["organization_id"] != "organization-42" {
		t.Fatalf("Assertion metadata = %#v, want hosted domain and organization", result.Assertion.Metadata)
	}
}

func TestCallbackRejectsProviderProfileWithoutEmail(t *testing.T) {
	server := newOIDCTestServer(t, testServerOptions{})
	client := newOIDCClientWithHTTPClient(t, server, &http.Client{Transport: oidcRouteTransport{
		base: http.DefaultTransport,
		route: func(request *http.Request) (*http.Response, error, bool) {
			switch request.URL.Path {
			case "/token":
				rawIDToken := signedOIDCTestToken(t, server, request.FormValue("code"), nil)
				return oidcJSONResponse(t, request, http.StatusOK, map[string]any{
					"access_token": "access-token",
					"token_type":   "Bearer",
					"id_token":     rawIDToken,
				}), nil, true
			case "/userinfo":
				return oidcJSONResponse(t, request, http.StatusOK, map[string]any{"sub": "subject-1"}), nil, true
			default:
				return nil, nil, false
			}
		},
	}}, false)

	authRequest, err := client.Begin()
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	_, err = client.Callback(context.Background(), callbackForOIDCTest(authRequest))
	if !errors.Is(err, ErrEmailMissing) {
		t.Fatalf("Callback() error = %v, want ErrEmailMissing", err)
	}
}

func TestCallbackPropagatesUserInfoProviderFailures(t *testing.T) {
	tests := []struct {
		name      string
		userinfo  func(*http.Request) (*http.Response, error)
		wantError error
	}{
		{
			name: "provider rejects userinfo request",
			userinfo: func(request *http.Request) (*http.Response, error) {
				return oidcJSONResponse(t, request, http.StatusBadGateway, map[string]any{"error": "temporarily_unavailable"}), nil
			},
		},
		{
			name: "provider returns malformed userinfo JSON",
			userinfo: func(request *http.Request) (*http.Response, error) {
				return oidcRawJSONResponse(request, http.StatusOK, `{"sub":`), nil
			},
		},
		{
			name: "provider network failure during userinfo request",
			userinfo: func(*http.Request) (*http.Response, error) {
				return nil, errOIDCTestProviderNetwork
			},
			wantError: errOIDCTestProviderNetwork,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newOIDCTestServer(t, testServerOptions{})
			var userInfoCalls atomic.Int32
			client := newOIDCClientWithHTTPClient(t, server, &http.Client{Transport: oidcRouteTransport{
				base: http.DefaultTransport,
				route: func(request *http.Request) (*http.Response, error, bool) {
					switch request.URL.Path {
					case "/token":
						rawIDToken := signedOIDCTestToken(t, server, request.FormValue("code"), nil)
						return oidcJSONResponse(t, request, http.StatusOK, map[string]any{
							"access_token": "access-token",
							"token_type":   "Bearer",
							"id_token":     rawIDToken,
						}), nil, true
					case "/userinfo":
						userInfoCalls.Add(1)
						response, err := tt.userinfo(request)
						return response, err, true
					default:
						return nil, nil, false
					}
				},
			}}, false)

			authRequest, err := client.Begin()
			if err != nil {
				t.Fatalf("Begin() error = %v", err)
			}
			_, err = client.Callback(context.Background(), callbackForOIDCTest(authRequest))
			if err == nil {
				t.Fatal("Callback() error = nil, want unusable UserInfo response error")
			}
			if tt.wantError != nil && !errors.Is(err, tt.wantError) {
				t.Fatalf("Callback() error = %v, want %v", err, tt.wantError)
			}
			if userInfoCalls.Load() != 1 {
				t.Fatalf("UserInfo calls = %d, want 1", userInfoCalls.Load())
			}
		})
	}
}

func TestCallbackBuildsNameFromGivenAndFamilyClaims(t *testing.T) {
	server := newOIDCTestServer(t, testServerOptions{})
	client := newOIDCClientWithHTTPClient(t, server, &http.Client{Transport: oidcRouteTransport{
		base: http.DefaultTransport,
		route: func(request *http.Request) (*http.Response, error, bool) {
			if request.URL.Path != "/token" {
				return nil, nil, false
			}
			rawIDToken := signedOIDCTestToken(t, server, request.FormValue("code"), map[string]any{
				"email":       "ada@example.com",
				"given_name":  "Ada",
				"family_name": "Lovelace",
			})
			return oidcJSONResponse(t, request, http.StatusOK, map[string]any{
				"access_token": "access-token",
				"token_type":   "Bearer",
				"id_token":     rawIDToken,
			}), nil, true
		},
	}}, false)

	authRequest, err := client.Begin()
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	result, err := client.Callback(context.Background(), callbackForOIDCTest(authRequest))
	if err != nil {
		t.Fatalf("Callback() error = %v", err)
	}
	if result.Assertion.Name != "Ada Lovelace" {
		t.Fatalf("Assertion name = %q, want given and family name", result.Assertion.Name)
	}
}

func TestNewPropagatesConfiguredHTTPClientDiscoveryFailure(t *testing.T) {
	provider := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(provider.Close)
	var discoveryCalls atomic.Int32

	_, err := New(context.Background(), Config{
		Provider:    identity.GenericOIDC(identity.ProviderGoogle, provider.URL),
		ClientID:    "client-id",
		RedirectURL: "https://app.example.com/callback",
		HTTPClient: &http.Client{Transport: oidcRouteTransport{
			base: http.DefaultTransport,
			route: func(request *http.Request) (*http.Response, error, bool) {
				if request.URL.Path != "/.well-known/openid-configuration" {
					return nil, nil, false
				}
				discoveryCalls.Add(1)
				return nil, errOIDCTestProviderNetwork, true
			},
		}},
	})
	if !errors.Is(err, errOIDCTestProviderNetwork) {
		t.Fatalf("New() error = %v, want configured HTTP client network error", err)
	}
	if discoveryCalls.Load() != 1 {
		t.Fatalf("discovery calls = %d, want 1 through configured HTTP client", discoveryCalls.Load())
	}
}

func TestNewRejectsMalformedProviderURLsBeforeNetworkIO(t *testing.T) {
	baseProvider := identity.Provider{
		Key:              identity.ProviderGoogle,
		Kind:             identity.ProviderKindOIDC,
		Issuer:           "https://issuer.example.com",
		AuthorizationURL: "https://issuer.example.com/authorize",
		TokenURL:         "https://issuer.example.com/token",
		JWKSURL:          "https://issuer.example.com/jwks",
	}
	tests := []struct {
		name      string
		provider  identity.Provider
		wantError error
	}{
		{
			name: "provider endpoint fragment",
			provider: func() identity.Provider {
				provider := baseProvider
				provider.AuthorizationURL += "#fragment"
				return provider
			}(),
			wantError: ErrInvalidConfig,
		},
		{
			name: "unsupported provider endpoint scheme",
			provider: func() identity.Provider {
				provider := baseProvider
				provider.TokenURL = "ftp://issuer.example.com/token"
				return provider
			}(),
			wantError: ErrInvalidConfig,
		},
		{
			name:      "issuer fragment",
			provider:  func() identity.Provider { provider := baseProvider; provider.Issuer += "#fragment"; return provider }(),
			wantError: ErrInvalidConfig,
		},
		{
			name:      "identity provider lacks required identity fields",
			provider:  identity.Provider{},
			wantError: identity.ErrInvalidIdentity,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(context.Background(), Config{
				Provider:    tt.provider,
				ClientID:    "client-id",
				RedirectURL: "https://app.example.com/callback",
			})
			if !errors.Is(err, tt.wantError) {
				t.Fatalf("New() error = %v, want %v", err, tt.wantError)
			}
		})
	}
}

var errOIDCTestProviderNetwork = errors.New("oidc provider network unavailable")

type oidcRouteTransport struct {
	base  http.RoundTripper
	route func(*http.Request) (*http.Response, error, bool)
}

func (transport oidcRouteTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if response, err, handled := transport.route(request); handled {
		return response, err
	}
	return transport.base.RoundTrip(request)
}

func newOIDCClientWithHTTPClient(t *testing.T, server *oidcTestServer, httpClient *http.Client, fetchUserInfo bool) *Client {
	t.Helper()
	client, err := New(context.Background(), Config{
		Provider: identity.Provider{
			Key:              identity.ProviderGoogle,
			Kind:             identity.ProviderKindOIDC,
			Issuer:           server.issuer,
			AuthorizationURL: server.issuer + "/authorize",
			TokenURL:         server.issuer + "/token",
			UserInfoURL:      server.issuer + "/userinfo",
			JWKSURL:          server.issuer + "/jwks",
			Scopes:           []string{"openid", "email", "profile"},
		},
		ClientID:      "client-id",
		ClientSecret:  "client-secret",
		RedirectURL:   "https://app.example.com/callback",
		FetchUserInfo: fetchUserInfo,
		HTTPClient:    httpClient,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return client
}

func callbackForOIDCTest(authRequest AuthRequest) Callback {
	return Callback{
		Values:        callbackValues(authRequest.Nonce, authRequest.State),
		ExpectedState: authRequest.State,
		ExpectedNonce: authRequest.Nonce,
		PKCEVerifier:  authRequest.PKCEVerifier,
		TenantID:      "tenant-a",
	}
}

func signedOIDCTestToken(t *testing.T, server *oidcTestServer, nonce string, extraClaims map[string]any) string {
	t.Helper()
	claims := map[string]any{
		"iss":   server.issuer,
		"sub":   "subject-1",
		"aud":   "client-id",
		"exp":   time.Now().Add(time.Hour).Unix(),
		"iat":   time.Now().Add(-time.Minute).Unix(),
		"nonce": nonce,
	}
	for key, value := range extraClaims {
		claims[key] = value
	}
	rawIDToken, err := server.signIDToken(claims)
	if err != nil {
		t.Fatalf("signIDToken() error = %v", err)
	}
	return rawIDToken
}

func oidcJSONResponse(t *testing.T, request *http.Request, statusCode int, value any) *http.Response {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return &http.Response{
		StatusCode:    statusCode,
		Status:        http.StatusText(statusCode),
		Header:        http.Header{"Content-Type": []string{"application/json"}},
		Body:          io.NopCloser(strings.NewReader(string(body))),
		ContentLength: int64(len(body)),
		Request:       request,
	}
}

func oidcRawJSONResponse(request *http.Request, statusCode int, body string) *http.Response {
	return &http.Response{
		StatusCode:    statusCode,
		Status:        http.StatusText(statusCode),
		Header:        http.Header{"Content-Type": []string{"application/json"}},
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       request,
	}
}

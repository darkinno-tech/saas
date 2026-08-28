package oidc

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	identity "github.com/darkinno-tech/saas/biz/identity"
	"golang.org/x/oauth2"
)

func TestHandleLoginCallbackConsumesProviderRejectionOnce(t *testing.T) {
	ctx := context.Background()
	server := newOIDCTestServer(t, testServerOptions{})
	client := newTestClient(t, server)
	store := NewMemoryLoginStore(DefaultLoginTTL)

	authRequest, err := client.BeginLogin(ctx, store, LoginRequest{TenantID: "tenant-a", Roles: []string{"member"}})
	if err != nil {
		t.Fatalf("BeginLogin() error = %v", err)
	}

	values := url.Values{
		"state":             {authRequest.State},
		"error":             {"access_denied"},
		"error_description": {"the user declined the requested consent"},
	}
	request := httptest.NewRequest(http.MethodGet, "/callback?"+values.Encode(), nil)
	_, err = client.HandleLoginCallback(ctx, request, store)
	if !errors.Is(err, ErrProviderRejected) {
		t.Fatalf("HandleLoginCallback() error = %v, want ErrProviderRejected", err)
	}
	if !strings.Contains(err.Error(), "access_denied") {
		t.Fatalf("HandleLoginCallback() error = %q, want provider error code", err)
	}
	if server.tokenCalls != 0 {
		t.Fatalf("token calls = %d, want 0 for a provider rejection", server.tokenCalls)
	}

	_, err = client.HandleLoginCallback(ctx, request, store)
	if !errors.Is(err, ErrLoginNotFound) {
		t.Fatalf("replayed HandleLoginCallback() error = %v, want ErrLoginNotFound", err)
	}
}

func TestCallbackSurfacesUnusableTokenResponses(t *testing.T) {
	tests := []struct {
		name         string
		status       int
		body         string
		wantRetrieve bool
		wantStatus   int
	}{
		{
			name:         "provider temporarily unavailable",
			status:       http.StatusBadGateway,
			body:         `{"error":"temporarily_unavailable","error_description":"provider maintenance"}`,
			wantRetrieve: true,
			wantStatus:   http.StatusBadGateway,
		},
		{
			name:       "malformed success payload",
			status:     http.StatusOK,
			body:       `{"access_token":`,
			wantStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tokenCalls := 0
			provider := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path != "/token" {
					http.NotFound(writer, request)
					return
				}
				tokenCalls++
				if request.Method != http.MethodPost {
					t.Errorf("token request method = %s, want POST", request.Method)
				}
				if err := request.ParseForm(); err != nil {
					t.Errorf("token request ParseForm() error = %v", err)
				} else if request.Form.Get("code_verifier") == "" {
					t.Error("token request has no PKCE code_verifier")
				}
				writer.Header().Set("Content-Type", "application/json")
				writer.WriteHeader(tt.status)
				_, _ = writer.Write([]byte(tt.body))
			}))
			t.Cleanup(provider.Close)

			client := newFailureResponseClient(t, provider.URL)
			authRequest, err := client.Begin()
			if err != nil {
				t.Fatalf("Begin() error = %v", err)
			}

			_, err = client.Callback(context.Background(), Callback{
				Values:        callbackValues(authRequest.Nonce, authRequest.State),
				ExpectedState: authRequest.State,
				ExpectedNonce: authRequest.Nonce,
				PKCEVerifier:  authRequest.PKCEVerifier,
				TenantID:      "tenant-a",
			})
			if err == nil {
				t.Fatal("Callback() error = nil, want failed token response")
			}
			if tokenCalls == 0 {
				t.Fatal("token calls = 0, want the callback to reach the token endpoint")
			}

			var retrieveError *oauth2.RetrieveError
			gotRetrieve := errors.As(err, &retrieveError)
			if gotRetrieve != tt.wantRetrieve {
				t.Fatalf("Callback() retrieve error = %v, want %v; err = %v", gotRetrieve, tt.wantRetrieve, err)
			}
			if retrieveError != nil && (retrieveError.Response == nil || retrieveError.Response.StatusCode != tt.wantStatus) {
				t.Fatalf("RetrieveError response = %#v, want status %d", retrieveError.Response, tt.wantStatus)
			}
		})
	}
}

func TestClientUsesConfiguredHTTPClientAcrossDiscoveryAndLogin(t *testing.T) {
	server := newOIDCTestServer(t, testServerOptions{})
	transport := &oidcRecordingTransport{base: http.DefaultTransport}
	client, err := New(context.Background(), Config{
		Provider:      identity.GenericOIDC(identity.ProviderGoogle, server.issuer, "email", "profile"),
		ClientID:      "client-id",
		ClientSecret:  "client-secret",
		RedirectURL:   "https://app.example.com/callback",
		FetchUserInfo: true,
		HTTPClient:    &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	authRequest, err := client.Begin()
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	result, err := client.Callback(context.Background(), Callback{
		Values:        callbackValues(authRequest.Nonce, authRequest.State),
		ExpectedState: authRequest.State,
		ExpectedNonce: authRequest.Nonce,
		PKCEVerifier:  authRequest.PKCEVerifier,
		TenantID:      "tenant-a",
	})
	if err != nil {
		t.Fatalf("Callback() error = %v", err)
	}
	if result.Assertion.Email != "user@example.com" || result.Assertion.Subject != "subject-1" {
		t.Fatalf("Assertion = %+v, want provider identity", result.Assertion)
	}

	for _, path := range []string{
		"/.well-known/openid-configuration",
		"/token",
		"/jwks",
		"/userinfo",
	} {
		if !transport.sawPath(path) {
			t.Errorf("configured HTTP client did not receive request for %s; paths = %#v", path, transport.pathsSnapshot())
		}
	}
}

func TestNewRejectsMalformedDiscoveryDocument(t *testing.T) {
	var requested bool
	provider := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(writer, request)
			return
		}
		requested = true
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"issuer":`))
	}))
	t.Cleanup(provider.Close)

	_, err := New(context.Background(), Config{
		Provider:    identity.GenericOIDC(identity.ProviderGoogle, provider.URL),
		ClientID:    "client-id",
		RedirectURL: "https://app.example.com/callback",
	})
	if err == nil {
		t.Fatal("New() error = nil, want malformed discovery document error")
	}
	if !requested {
		t.Fatal("New() did not request the provider discovery document")
	}
}

func newFailureResponseClient(t *testing.T, providerURL string) *Client {
	t.Helper()
	client, err := New(context.Background(), Config{
		Provider: identity.Provider{
			Key:              identity.ProviderGoogle,
			Kind:             identity.ProviderKindOIDC,
			Issuer:           providerURL,
			AuthorizationURL: providerURL + "/authorize",
			TokenURL:         providerURL + "/token",
			JWKSURL:          providerURL + "/jwks",
		},
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		RedirectURL:  "https://app.example.com/callback",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return client
}

type oidcRecordingTransport struct {
	base  http.RoundTripper
	mu    sync.Mutex
	paths []string
}

func (transport *oidcRecordingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.mu.Lock()
	transport.paths = append(transport.paths, request.URL.Path)
	transport.mu.Unlock()

	return transport.base.RoundTrip(request)
}

func (transport *oidcRecordingTransport) sawPath(path string) bool {
	for _, recordedPath := range transport.pathsSnapshot() {
		if recordedPath == path {
			return true
		}
	}
	return false
}

func (transport *oidcRecordingTransport) pathsSnapshot() []string {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return append([]string(nil), transport.paths...)
}

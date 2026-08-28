package oidc

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	identity "github.com/darkinno-tech/saas/biz/identity"
	"golang.org/x/oauth2"
)

func TestBeginBuildsAuthorizationURLWithStateNonceAndPKCE(t *testing.T) {
	client := testClient(t, testServerOptions{})

	request, err := client.Begin()
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}

	parsed, err := url.Parse(request.URL)
	if err != nil {
		t.Fatalf("Parse auth URL error = %v", err)
	}
	values := parsed.Query()
	if values.Get("state") != request.State {
		t.Fatalf("state = %q, want %q", values.Get("state"), request.State)
	}
	if values.Get("nonce") != request.Nonce {
		t.Fatalf("nonce = %q, want %q", values.Get("nonce"), request.Nonce)
	}
	if values.Get("code_challenge") == "" || values.Get("code_challenge_method") != "S256" {
		t.Fatalf("PKCE query = %q/%q, want S256 challenge", values.Get("code_challenge"), values.Get("code_challenge_method"))
	}
}

func TestCallbackVerifiesTokenAndBuildsAssertion(t *testing.T) {
	ctx := context.Background()
	client := testClient(t, testServerOptions{})
	authRequest, err := client.Begin()
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}

	result, err := client.Callback(ctx, Callback{
		Values:        callbackValues(authRequest.Nonce, authRequest.State),
		ExpectedState: authRequest.State,
		ExpectedNonce: authRequest.Nonce,
		PKCEVerifier:  authRequest.PKCEVerifier,
		TenantID:      "tenant-a",
		Roles:         []string{"member"},
	})
	if err != nil {
		t.Fatalf("Callback() error = %v", err)
	}

	assertion := result.Assertion
	if assertion.TenantID != "tenant-a" || assertion.Provider != identity.ProviderGoogle || assertion.Subject != "subject-1" {
		t.Fatalf("Assertion identity = %+v, want tenant/provider/subject", assertion)
	}
	if assertion.Email != "user@example.com" || !assertion.EmailVerified || assertion.Name != "User Example" {
		t.Fatalf("Assertion profile = %+v, want verified profile", assertion)
	}
	if len(assertion.Roles) != 1 || assertion.Roles[0] != "member" {
		t.Fatalf("Assertion roles = %#v, want member", assertion.Roles)
	}
	if result.RawIDToken == "" || result.Token.AccessToken != "access-token" {
		t.Fatalf("Result token = %+v, rawIDToken empty=%v", result.Token, result.RawIDToken == "")
	}
}

func TestCallbackRejectsStateMismatchBeforeExchange(t *testing.T) {
	ctx := context.Background()
	server := newOIDCTestServer(t, testServerOptions{})
	client := newTestClient(t, server)

	_, err := client.Callback(ctx, Callback{
		Values:        callbackValues("code-ok", "bad-state"),
		ExpectedState: "expected-state",
		ExpectedNonce: "expected-nonce",
		PKCEVerifier:  "verifier",
		TenantID:      "tenant-a",
	})
	if err != ErrStateMismatch {
		t.Fatalf("Callback() error = %v, want ErrStateMismatch", err)
	}
	if server.tokenCalls != 0 {
		t.Fatalf("token calls = %d, want 0 before state passes", server.tokenCalls)
	}
}

func TestCallbackRejectsDuplicateStateBeforeExchange(t *testing.T) {
	ctx := context.Background()
	server := newOIDCTestServer(t, testServerOptions{})
	client := newTestClient(t, server)

	_, err := client.Callback(ctx, Callback{
		Values:        url.Values{"code": {"code-ok"}, "state": {"expected-state", "other-state"}},
		ExpectedState: "expected-state",
		ExpectedNonce: "expected-nonce",
		PKCEVerifier:  "verifier",
		TenantID:      "tenant-a",
	})
	if err != ErrDuplicateParam {
		t.Fatalf("Callback() error = %v, want ErrDuplicateParam", err)
	}
	if server.tokenCalls != 0 {
		t.Fatalf("token calls = %d, want 0 before unique state passes", server.tokenCalls)
	}
}

func TestCallbackRejectsNonceMismatch(t *testing.T) {
	ctx := context.Background()
	client := testClient(t, testServerOptions{})
	authRequest, err := client.Begin()
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}

	_, err = client.Callback(ctx, Callback{
		Values:        callbackValues(authRequest.Nonce, authRequest.State),
		ExpectedState: authRequest.State,
		ExpectedNonce: "wrong-nonce",
		PKCEVerifier:  authRequest.PKCEVerifier,
		TenantID:      "tenant-a",
	})
	if err != ErrNonceMismatch {
		t.Fatalf("Callback() error = %v, want ErrNonceMismatch", err)
	}
}

func TestCallbackRejectsUserInfoSubjectMismatch(t *testing.T) {
	ctx := context.Background()
	client := testClient(t, testServerOptions{fetchUserInfo: true, userInfoSubject: "other-subject"})
	authRequest, err := client.Begin()
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}

	_, err = client.Callback(ctx, Callback{
		Values:        callbackValues(authRequest.Nonce, authRequest.State),
		ExpectedState: authRequest.State,
		ExpectedNonce: authRequest.Nonce,
		PKCEVerifier:  authRequest.PKCEVerifier,
		TenantID:      "tenant-a",
	})
	if err != ErrSubjectMismatch {
		t.Fatalf("Callback() error = %v, want ErrSubjectMismatch", err)
	}
}

func TestMergeClaimsKeepsEmailVerificationBoundToEmail(t *testing.T) {
	tests := []struct {
		name     string
		primary  claims
		fallback claims
		wantMail string
		verified bool
	}{
		{
			name:     "different fallback email cannot verify primary",
			primary:  claims{Email: "unverified@example.com"},
			fallback: claims{Email: "verified@example.com", EmailVerified: true},
			wantMail: "unverified@example.com",
			verified: false,
		},
		{
			name:     "same fallback email can supply verification",
			primary:  claims{Email: "user@example.com"},
			fallback: claims{Email: "user@example.com", EmailVerified: true},
			wantMail: "user@example.com",
			verified: true,
		},
		{
			name:     "fallback email and verification are adopted together",
			fallback: claims{Email: "user@example.com", EmailVerified: true},
			wantMail: "user@example.com",
			verified: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mergeClaims(tt.primary, tt.fallback)
			if got.Email != tt.wantMail || got.EmailVerified != tt.verified {
				t.Fatalf("mergeClaims() email = %q, verified = %v; want %q, %v", got.Email, got.EmailVerified, tt.wantMail, tt.verified)
			}
		})
	}
}

func TestCallbackRejectsAccessTokenHashMismatch(t *testing.T) {
	ctx := context.Background()
	client := testClient(t, testServerOptions{accessTokenHash: "bad-hash"})
	authRequest, err := client.Begin()
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}

	_, err = client.Callback(ctx, Callback{
		Values:        callbackValues(authRequest.Nonce, authRequest.State),
		ExpectedState: authRequest.State,
		ExpectedNonce: authRequest.Nonce,
		PKCEVerifier:  authRequest.PKCEVerifier,
		TenantID:      "tenant-a",
	})
	if err == nil {
		t.Fatal("Callback() error = nil, want access token hash verification error")
	}
}

func TestHandleCallbackAcceptsFormPost(t *testing.T) {
	ctx := context.Background()
	client := testClient(t, testServerOptions{})
	authRequest, err := client.Begin()
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}

	form := callbackValues(authRequest.Nonce, authRequest.State)
	request := httptest.NewRequest(http.MethodPost, "/callback", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	result, err := client.HandleCallback(ctx, request, authRequest, "tenant-a", "member")
	if err != nil {
		t.Fatalf("HandleCallback() error = %v", err)
	}
	if result.Assertion.TenantID != "tenant-a" || result.Assertion.Subject != "subject-1" {
		t.Fatalf("Assertion = %+v, want tenant subject", result.Assertion)
	}
}

func TestNewRejectsNonOIDCProvider(t *testing.T) {
	_, err := New(context.Background(), Config{
		Provider:    identity.GitHubOAuth(),
		ClientID:    "client-id",
		RedirectURL: "https://app.example.com/callback",
	})
	if err != ErrInvalidConfig {
		t.Fatalf("New(non OIDC) error = %v, want ErrInvalidConfig", err)
	}
}

func TestNewRejectsRemoteHTTPProviderEndpoint(t *testing.T) {
	_, err := New(context.Background(), Config{
		Provider: identity.Provider{
			Key:              identity.ProviderGoogle,
			Kind:             identity.ProviderKindOIDC,
			Issuer:           "https://issuer.example.com",
			AuthorizationURL: "http://issuer.example.com/authorize",
			TokenURL:         "https://issuer.example.com/token",
			JWKSURL:          "https://issuer.example.com/jwks",
		},
		ClientID:    "client-id",
		RedirectURL: "https://app.example.com/callback",
	})
	if err != ErrInsecureURL {
		t.Fatalf("New(remote HTTP endpoint) error = %v, want ErrInsecureURL", err)
	}
}

func TestNewRejectsRemoteHTTPRedirectURL(t *testing.T) {
	_, err := New(context.Background(), Config{
		Provider:    identity.GoogleOIDC(),
		ClientID:    "client-id",
		RedirectURL: "http://app.example.com/callback",
	})
	if err != ErrInsecureURL {
		t.Fatalf("New(remote HTTP redirect) error = %v, want ErrInsecureURL", err)
	}
}

func TestNewRejectsIssuerWithQuery(t *testing.T) {
	provider := identity.GoogleOIDC()
	provider.Issuer = "https://accounts.google.com?tenant=a"

	_, err := New(context.Background(), Config{
		Provider:    provider,
		ClientID:    "client-id",
		RedirectURL: "https://app.example.com/callback",
	})
	if err != ErrInvalidConfig {
		t.Fatalf("New(issuer query) error = %v, want ErrInvalidConfig", err)
	}
}

func TestNewAllowsLoopbackHTTPRedirectURL(t *testing.T) {
	_, err := New(context.Background(), Config{
		Provider:    identity.GoogleOIDC(),
		ClientID:    "client-id",
		RedirectURL: "http://127.0.0.1:8080/callback",
	})
	if err != nil {
		t.Fatalf("New(loopback HTTP redirect) error = %v", err)
	}
}

func testClient(t *testing.T, options testServerOptions) *Client {
	t.Helper()
	server := newOIDCTestServer(t, options)
	return newTestClient(t, server)
}

func newTestClient(t *testing.T, server *oidcTestServer) *Client {
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
		FetchUserInfo: server.options.fetchUserInfo,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return client
}

func callbackValues(code string, state string) url.Values {
	return url.Values{"code": {code}, "state": {state}}
}

type testServerOptions struct {
	fetchUserInfo   bool
	userInfoSubject string
	accessTokenHash string
}

type oidcTestServer struct {
	issuer     string
	tokenCalls int
	options    testServerOptions
	key        *rsa.PrivateKey
	server     *httptest.Server
}

func newOIDCTestServer(t *testing.T, options testServerOptions) *oidcTestServer {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}

	testServer := &oidcTestServer{key: key, options: options}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", testServer.discovery)
	mux.HandleFunc("/authorize", testServer.notUsed)
	mux.HandleFunc("/token", testServer.token)
	mux.HandleFunc("/jwks", testServer.jwks)
	mux.HandleFunc("/userinfo", testServer.userinfo)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	testServer.server = server
	testServer.issuer = server.URL
	return testServer
}

func (server *oidcTestServer) discovery(writer http.ResponseWriter, _ *http.Request) {
	server.writeJSON(writer, map[string]any{
		"issuer":                                server.issuer,
		"authorization_endpoint":                server.issuer + "/authorize",
		"token_endpoint":                        server.issuer + "/token",
		"jwks_uri":                              server.issuer + "/jwks",
		"userinfo_endpoint":                     server.issuer + "/userinfo",
		"id_token_signing_alg_values_supported": []string{"RS256"},
	})
}

func (server *oidcTestServer) notUsed(writer http.ResponseWriter, _ *http.Request) {
	http.Error(writer, "not used", http.StatusBadRequest)
}

func (server *oidcTestServer) token(writer http.ResponseWriter, request *http.Request) {
	server.tokenCalls++
	if err := request.ParseForm(); err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}
	if request.Form.Get("code_verifier") == "" {
		http.Error(writer, "missing verifier", http.StatusBadRequest)
		return
	}

	claims := map[string]any{
		"iss":            server.issuer,
		"sub":            "subject-1",
		"aud":            "client-id",
		"exp":            time.Now().Add(time.Hour).Unix(),
		"iat":            time.Now().Add(-time.Minute).Unix(),
		"nonce":          request.Form.Get("code"),
		"email":          "user@example.com",
		"email_verified": true,
		"name":           "User Example",
	}
	if server.options.accessTokenHash != "" {
		claims["at_hash"] = server.options.accessTokenHash
	}

	token, err := server.signIDToken(claims)
	if err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		return
	}

	server.writeJSON(writer, map[string]any{
		"access_token": "access-token",
		"token_type":   "Bearer",
		"expires_in":   3600,
		"id_token":     token,
	})
}

func (server *oidcTestServer) jwks(writer http.ResponseWriter, _ *http.Request) {
	server.writeJSON(writer, map[string]any{
		"keys": []map[string]any{
			{
				"kty": "RSA",
				"use": "sig",
				"kid": "test-key",
				"alg": "RS256",
				"n":   base64.RawURLEncoding.EncodeToString(server.key.N.Bytes()),
				"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(server.key.E)).Bytes()),
			},
		},
	})
}

func (server *oidcTestServer) userinfo(writer http.ResponseWriter, _ *http.Request) {
	subject := server.options.userInfoSubject
	if subject == "" {
		subject = "subject-1"
	}
	server.writeJSON(writer, map[string]any{
		"sub":            subject,
		"email":          "user@example.com",
		"email_verified": true,
		"name":           "User Example",
	})
}

func (server *oidcTestServer) writeJSON(writer http.ResponseWriter, value any) {
	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
	}
}

func (server *oidcTestServer) signIDToken(claims map[string]any) (string, error) {
	header := map[string]any{"alg": "RS256", "kid": "test-key", "typ": "JWT"}
	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}

	encodedHeader := base64.RawURLEncoding.EncodeToString(headerJSON)
	encodedClaims := base64.RawURLEncoding.EncodeToString(claimsJSON)
	unsigned := encodedHeader + "." + encodedClaims
	digest := sha256.Sum256([]byte(unsigned))

	signature, err := rsa.SignPKCS1v15(rand.Reader, server.key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return strings.Join([]string{
		encodedHeader,
		encodedClaims,
		base64.RawURLEncoding.EncodeToString(signature),
	}, "."), nil
}

func TestAuthURLWithExplicitValues(t *testing.T) {
	client := testClient(t, testServerOptions{})

	authorizationURL, err := client.AuthURL("state", "nonce", oauth2.GenerateVerifier())
	if err != nil {
		t.Fatalf("AuthURL() error = %v", err)
	}
	if !strings.Contains(authorizationURL, "state=state") || !strings.Contains(authorizationURL, "nonce=nonce") {
		t.Fatalf("AuthURL() = %q, want state and nonce", authorizationURL)
	}
}

func TestAuthURLProtectsNonceAndPKCEFromOverrideOptions(t *testing.T) {
	client := testClient(t, testServerOptions{})
	verifier := oauth2.GenerateVerifier()

	authorizationURL, err := client.AuthURL(
		"state",
		"nonce",
		verifier,
		oauth2.SetAuthURLParam("nonce", "bad-nonce"),
		oauth2.SetAuthURLParam("code_challenge", "bad-challenge"),
		oauth2.SetAuthURLParam("code_challenge_method", "plain"),
	)
	if err != nil {
		t.Fatalf("AuthURL() error = %v", err)
	}

	parsed, err := url.Parse(authorizationURL)
	if err != nil {
		t.Fatalf("Parse auth URL error = %v", err)
	}
	challenge := sha256.Sum256([]byte(verifier))
	values := parsed.Query()
	if values.Get("nonce") != "nonce" {
		t.Fatalf("nonce = %q, want protected nonce", values.Get("nonce"))
	}
	if values.Get("code_challenge") != base64.RawURLEncoding.EncodeToString(challenge[:]) {
		t.Fatalf("code_challenge = %q, want verifier challenge", values.Get("code_challenge"))
	}
	if values.Get("code_challenge_method") != "S256" {
		t.Fatalf("code_challenge_method = %q, want S256", values.Get("code_challenge_method"))
	}
}

func TestHandleLoginCallbackConsumesStoredRequest(t *testing.T) {
	ctx := context.Background()
	server := newOIDCTestServer(t, testServerOptions{})
	client := newTestClient(t, server)
	store := NewMemoryLoginStore(time.Minute)

	authRequest, err := client.BeginLogin(ctx, store, LoginRequest{
		TenantID: "tenant-a",
		Roles:    []string{"member"},
	})
	if err != nil {
		t.Fatalf("BeginLogin() error = %v", err)
	}

	request := httptest.NewRequest(http.MethodGet, "/callback?"+callbackValues(authRequest.Nonce, authRequest.State).Encode(), nil)
	result, err := client.HandleLoginCallback(ctx, request, store)
	if err != nil {
		t.Fatalf("HandleLoginCallback() error = %v", err)
	}
	if result.Assertion.TenantID != "tenant-a" || len(result.Assertion.Roles) != 1 || result.Assertion.Roles[0] != "member" {
		t.Fatalf("Assertion = %+v, want stored tenant and roles", result.Assertion)
	}

	_, err = client.HandleLoginCallback(ctx, request, store)
	if err != ErrLoginNotFound {
		t.Fatalf("HandleLoginCallback() replay error = %v, want ErrLoginNotFound", err)
	}
	if server.tokenCalls != 1 {
		t.Fatalf("token calls = %d, want 1 after replay rejection", server.tokenCalls)
	}
}

func TestMemoryLoginStoreRejectsExpiredLogin(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	store := NewMemoryLoginStore(time.Minute)
	store.now = func() time.Time { return now }

	err := store.SaveLogin(ctx, Login{
		AuthRequest: AuthRequest{
			URL:          "https://issuer.example.com/authorize",
			State:        "state",
			Nonce:        "nonce",
			PKCEVerifier: oauth2.GenerateVerifier(),
		},
		TenantID:  "tenant-a",
		ExpiresAt: now.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("SaveLogin() error = %v", err)
	}

	now = now.Add(2 * time.Minute)
	_, err = store.ConsumeLogin(ctx, "state")
	if err != ErrLoginExpired {
		t.Fatalf("ConsumeLogin() error = %v, want ErrLoginExpired", err)
	}
	_, err = store.ConsumeLogin(ctx, "state")
	if err != ErrLoginNotFound {
		t.Fatalf("ConsumeLogin() second error = %v, want ErrLoginNotFound", err)
	}
}

func TestMemoryLoginStoreRejectsOverflow(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryLoginStore(time.Minute, WithMaxPendingLogins(1))

	for _, state := range []string{"state-1", "state-2"} {
		err := store.SaveLogin(ctx, Login{
			AuthRequest: AuthRequest{
				URL:          "https://issuer.example.com/authorize",
				State:        state,
				Nonce:        "nonce-" + state,
				PKCEVerifier: oauth2.GenerateVerifier(),
			},
			TenantID: "tenant-a",
		})
		if state == "state-1" && err != nil {
			t.Fatalf("SaveLogin(first) error = %v", err)
		}
		if state == "state-2" && err != ErrLoginStoreFull {
			t.Fatalf("SaveLogin(second) error = %v, want ErrLoginStoreFull", err)
		}
	}
}

func TestMemoryLoginStoreReclaimsExpiredLoginWhenFull(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	store := NewMemoryLoginStore(time.Minute, WithMaxPendingLogins(1))
	store.now = func() time.Time { return now }

	err := store.SaveLogin(ctx, Login{
		AuthRequest: AuthRequest{
			URL:          "https://issuer.example.com/authorize",
			State:        "expired-state",
			Nonce:        "expired-nonce",
			PKCEVerifier: oauth2.GenerateVerifier(),
		},
		TenantID:  "tenant-a",
		ExpiresAt: now.Add(time.Millisecond),
	})
	if err != nil {
		t.Fatalf("SaveLogin(expired seed) error = %v", err)
	}

	now = now.Add(2 * time.Millisecond)
	err = store.SaveLogin(ctx, Login{
		AuthRequest: AuthRequest{
			URL:          "https://issuer.example.com/authorize",
			State:        "fresh-state",
			Nonce:        "fresh-nonce",
			PKCEVerifier: oauth2.GenerateVerifier(),
		},
		TenantID: "tenant-a",
	})
	if err != nil {
		t.Fatalf("SaveLogin(fresh) error = %v, want expired login reclaimed", err)
	}
}

func TestMemoryLoginStoreZeroValue(t *testing.T) {
	ctx := context.Background()
	var store MemoryLoginStore

	err := store.SaveLogin(ctx, Login{
		AuthRequest: AuthRequest{
			URL:          "https://issuer.example.com/authorize",
			State:        "state",
			Nonce:        "nonce",
			PKCEVerifier: oauth2.GenerateVerifier(),
		},
		TenantID: "tenant-a",
	})
	if err != nil {
		t.Fatalf("SaveLogin() error = %v", err)
	}
	login, err := store.ConsumeLogin(ctx, "state")
	if err != nil {
		t.Fatalf("ConsumeLogin() error = %v", err)
	}
	if login.TenantID != "tenant-a" {
		t.Fatalf("ConsumeLogin() = %+v, want tenant-a", login)
	}
}

func TestNewSQLLoginStoreValidationAndScan(t *testing.T) {
	if _, err := NewSQLLoginStore(nil); !errors.Is(err, ErrNilDB) {
		t.Fatalf("NewSQLLoginStore(nil) error = %v, want ErrNilDB", err)
	}

	db := &sql.DB{}
	store, err := NewSQLLoginStore(db)
	if err != nil {
		t.Fatalf("NewSQLLoginStore() error = %v", err)
	}
	if store.table != DefaultSQLLoginTableName {
		t.Fatalf("default table = %q, want %q", store.table, DefaultSQLLoginTableName)
	}

	store, err = NewSQLLoginStore(db, WithLoginTableName("public.oidc_logins"), WithSQLDialect(SQLDialectPostgres), WithLoginTTL(5*time.Minute))
	if err != nil {
		t.Fatalf("NewSQLLoginStore(custom) error = %v", err)
	}
	if store.table != "public.oidc_logins" || store.dialect != SQLDialectPostgres || store.ttl != 5*time.Minute {
		t.Fatalf("SQLLoginStore = %+v, want custom table, dialect, and ttl", store)
	}
	if got := store.placeholders(3, 2); got != "$2, $3, $4" {
		t.Fatalf("postgres placeholders = %q, want $2, $3, $4", got)
	}

	if _, err := NewSQLLoginStore(db, WithLoginTableName("oidc_logins;drop")); !errors.Is(err, ErrInvalidTableName) {
		t.Fatalf("NewSQLLoginStore(unsafe table) error = %v, want ErrInvalidTableName", err)
	}
	if _, err := NewSQLLoginStore(db, WithSQLDialect("oracle")); !errors.Is(err, ErrUnsupportedSQLDialect) {
		t.Fatalf("NewSQLLoginStore(unsupported dialect) error = %v, want ErrUnsupportedSQLDialect", err)
	}

	expiresAt := time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC)
	rawRoles, err := marshalLoginRoles([]string{"member"})
	if err != nil {
		t.Fatalf("marshalLoginRoles() error = %v", err)
	}
	login, err := scanLogin(loginScannerFunc(func(dest ...any) error {
		*(dest[0].(*string)) = "state"
		*(dest[1].(*string)) = "https://issuer.example.com/authorize"
		*(dest[2].(*string)) = "nonce"
		*(dest[3].(*string)) = "verifier"
		*(dest[4].(*string)) = "tenant-a"
		*(dest[5].(*sql.NullString)) = sql.NullString{String: "u1", Valid: true}
		*(dest[6].(*string)) = rawRoles
		*(dest[7].(*time.Time)) = expiresAt
		return nil
	}))
	if err != nil {
		t.Fatalf("scanLogin() error = %v", err)
	}
	if login.State != "state" || login.Nonce != "nonce" || login.PKCEVerifier != "verifier" || login.TenantID != "tenant-a" || len(login.Roles) != 1 || login.Roles[0] != "member" {
		t.Fatalf("scanLogin() = %+v, want decoded login", login)
	}
}

func TestRetrySQLLoginConsumeReturnsReplayResultAfterTransientConflict(t *testing.T) {
	attempts := 0
	waits := []int{}
	_, err := retrySQLLoginConsume(context.Background(), func() (Login, error) {
		attempts++
		if attempts == 1 {
			return Login{}, errors.New("could not serialize access due to concurrent update")
		}
		return Login{}, ErrLoginNotFound
	}, func(_ context.Context, attempt int) error {
		waits = append(waits, attempt)
		return nil
	})
	if !errors.Is(err, ErrLoginNotFound) {
		t.Fatalf("retrySQLLoginConsume() error = %v, want ErrLoginNotFound", err)
	}
	if attempts != 2 || !reflect.DeepEqual(waits, []int{0}) {
		t.Fatalf("retrySQLLoginConsume() attempts/waits = %d/%#v, want 2/[0]", attempts, waits)
	}
}

func TestRetrySQLLoginConsumeHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	attempts := 0
	_, err := retrySQLLoginConsume(ctx, func() (Login, error) {
		attempts++
		return Login{}, errors.New("could not serialize access due to concurrent update")
	}, waitForSQLLoginConsumeRetry)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("retrySQLLoginConsume() error = %v, want context.Canceled", err)
	}
	if attempts != 1 {
		t.Fatalf("retrySQLLoginConsume() attempts = %d, want 1", attempts)
	}
}

func TestRetrySQLLoginConsumeStopsAfterMaximumAttempts(t *testing.T) {
	retryable := errors.New("could not serialize access due to concurrent update")
	attempts := 0
	waits := 0
	_, err := retrySQLLoginConsume(context.Background(), func() (Login, error) {
		attempts++
		return Login{}, retryable
	}, func(context.Context, int) error {
		waits++
		return nil
	})
	if !errors.Is(err, retryable) {
		t.Fatalf("retrySQLLoginConsume() error = %v, want final retryable error", err)
	}
	if attempts != sqlLoginConsumeMaxAttempts || waits != sqlLoginConsumeMaxAttempts-1 {
		t.Fatalf("retrySQLLoginConsume() attempts/waits = %d/%d, want %d/%d", attempts, waits, sqlLoginConsumeMaxAttempts, sqlLoginConsumeMaxAttempts-1)
	}
}

type loginScannerFunc func(dest ...any) error

func (fn loginScannerFunc) Scan(dest ...any) error {
	return fn(dest...)
}

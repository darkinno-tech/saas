package oidc

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"

	identity "github.com/im10furry/saas/biz/identity"
	"github.com/im10furry/saas/core/types"
	oidclib "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

type Config struct {
	Provider      identity.Provider
	ClientID      string
	ClientSecret  string
	RedirectURL   string
	Scopes        []string
	FetchUserInfo bool
	HTTPClient    *http.Client
}

type Client struct {
	provider      identity.Provider
	oauth2        oauth2.Config
	verifier      *oidclib.IDTokenVerifier
	oidcProvider  *oidclib.Provider
	fetchUserInfo bool
	httpClient    *http.Client
}

type AuthRequest struct {
	URL          string
	State        string
	Nonce        string
	PKCEVerifier string
}

type Callback struct {
	Values        url.Values
	ExpectedState string
	ExpectedNonce string
	PKCEVerifier  string
	TenantID      types.TenantID
	UserID        string
	Roles         []string
}

type Result struct {
	Assertion  identity.Assertion
	Token      *oauth2.Token
	RawIDToken string
}

type claims struct {
	Subject        string `json:"sub"`
	Email          string `json:"email"`
	EmailVerified  bool   `json:"email_verified"`
	Name           string `json:"name"`
	PreferredName  string `json:"preferred_username"`
	GivenName      string `json:"given_name"`
	FamilyName     string `json:"family_name"`
	HostedDomain   string `json:"hd"`
	OrganizationID string `json:"org_id"`
}

func New(ctx context.Context, config Config) (*Client, error) {
	provider := config.Provider
	if err := provider.Validate(); err != nil {
		return nil, err
	}
	if provider.Kind != identity.ProviderKindOIDC || provider.Issuer == "" || config.ClientID == "" || config.RedirectURL == "" {
		return nil, ErrInvalidConfig
	}
	if err := validateURLs(provider, config.RedirectURL); err != nil {
		return nil, err
	}

	ctx = clientContext(ctx, config.HTTPClient)
	oidcProvider, err := newProvider(ctx, provider)
	if err != nil {
		return nil, err
	}

	scopes := config.Scopes
	if len(scopes) == 0 {
		scopes = provider.Scopes
	}
	scopes = ensureOpenIDScope(scopes)

	oauthConfig := oauth2.Config{
		ClientID:     config.ClientID,
		ClientSecret: config.ClientSecret,
		RedirectURL:  config.RedirectURL,
		Endpoint:     oidcProvider.Endpoint(),
		Scopes:       scopes,
	}

	return &Client{
		provider:      provider,
		oauth2:        oauthConfig,
		verifier:      oidcProvider.VerifierContext(ctx, &oidclib.Config{ClientID: config.ClientID}),
		oidcProvider:  oidcProvider,
		fetchUserInfo: config.FetchUserInfo,
		httpClient:    config.HTTPClient,
	}, nil
}

func (client *Client) Begin(opts ...oauth2.AuthCodeOption) (AuthRequest, error) {
	state, err := randomURLValue(32)
	if err != nil {
		return AuthRequest{}, err
	}
	nonce, err := randomURLValue(32)
	if err != nil {
		return AuthRequest{}, err
	}
	verifier := oauth2.GenerateVerifier()

	authURL, err := client.AuthURL(state, nonce, verifier, opts...)
	if err != nil {
		return AuthRequest{}, err
	}
	return AuthRequest{URL: authURL, State: state, Nonce: nonce, PKCEVerifier: verifier}, nil
}

func (client *Client) AuthURL(state string, nonce string, verifier string, opts ...oauth2.AuthCodeOption) (string, error) {
	if client == nil || state == "" || nonce == "" || verifier == "" {
		return "", ErrInvalidConfig
	}

	options := append([]oauth2.AuthCodeOption{}, opts...)
	options = append(options, oidclib.Nonce(nonce), oauth2.S256ChallengeOption(verifier))
	return client.oauth2.AuthCodeURL(state, options...), nil
}

func (client *Client) HandleCallback(ctx context.Context, request *http.Request, expected AuthRequest, tenantID types.TenantID, roles ...string) (Result, error) {
	if request == nil {
		return Result{}, ErrInvalidCallback
	}
	values, err := callbackValuesFromRequest(request)
	if err != nil {
		return Result{}, err
	}
	return client.Callback(ctx, Callback{
		Values:        values,
		ExpectedState: expected.State,
		ExpectedNonce: expected.Nonce,
		PKCEVerifier:  expected.PKCEVerifier,
		TenantID:      tenantID,
		Roles:         roles,
	})
}

func (client *Client) Callback(ctx context.Context, callback Callback) (Result, error) {
	if client == nil || callback.Values == nil || callback.ExpectedState == "" || callback.ExpectedNonce == "" || callback.PKCEVerifier == "" || callback.TenantID == "" {
		return Result{}, ErrInvalidCallback
	}
	providerError, err := optionalCallbackValue(callback.Values, "error")
	if err != nil {
		return Result{}, err
	}
	if providerError != "" {
		return Result{}, fmt.Errorf("%w: %s", ErrProviderRejected, providerError)
	}
	state, err := requiredCallbackValue(callback.Values, "state")
	if err != nil {
		return Result{}, err
	}
	if !constantTimeEqual(callback.ExpectedState, state) {
		return Result{}, ErrStateMismatch
	}

	code, err := requiredCallbackValue(callback.Values, "code")
	if err != nil {
		return Result{}, err
	}

	ctx = client.context(ctx)
	token, err := client.oauth2.Exchange(ctx, code, oauth2.VerifierOption(callback.PKCEVerifier))
	if err != nil {
		return Result{}, err
	}

	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return Result{}, ErrTokenMissing
	}

	idToken, err := client.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return Result{}, err
	}
	if !constantTimeEqual(callback.ExpectedNonce, idToken.Nonce) {
		return Result{}, ErrNonceMismatch
	}
	if idToken.AccessTokenHash != "" {
		if token.AccessToken == "" {
			return Result{}, ErrTokenMissing
		}
		if err := idToken.VerifyAccessToken(token.AccessToken); err != nil {
			return Result{}, err
		}
	}

	idClaims, err := tokenClaims(idToken)
	if err != nil {
		return Result{}, err
	}
	if client.fetchUserInfo || idClaims.Email == "" {
		userInfoClaims, err := client.userInfoClaims(ctx, token)
		if err != nil {
			return Result{}, err
		}
		if userInfoClaims.Subject != "" && userInfoClaims.Subject != idToken.Subject {
			return Result{}, ErrSubjectMismatch
		}
		idClaims = mergeClaims(idClaims, userInfoClaims)
	}
	if idClaims.Email == "" {
		return Result{}, ErrEmailMissing
	}

	assertion := identity.Assertion{
		TenantID:      callback.TenantID,
		Provider:      client.provider.Key,
		Subject:       idToken.Subject,
		UserID:        strings.TrimSpace(callback.UserID),
		Email:         strings.TrimSpace(idClaims.Email),
		Name:          displayName(idClaims),
		EmailVerified: idClaims.EmailVerified,
		Roles:         cloneStrings(callback.Roles),
		Metadata: map[string]string{
			"issuer": idToken.Issuer,
		},
	}
	if idClaims.HostedDomain != "" {
		assertion.Metadata["hosted_domain"] = idClaims.HostedDomain
	}
	if idClaims.OrganizationID != "" {
		assertion.Metadata["organization_id"] = idClaims.OrganizationID
	}

	return Result{Assertion: assertion, Token: token, RawIDToken: rawIDToken}, nil
}

func newProvider(ctx context.Context, provider identity.Provider) (*oidclib.Provider, error) {
	if provider.AuthorizationURL != "" && provider.TokenURL != "" && provider.JWKSURL != "" {
		return (&oidclib.ProviderConfig{
			IssuerURL:   provider.Issuer,
			AuthURL:     provider.AuthorizationURL,
			TokenURL:    provider.TokenURL,
			UserInfoURL: provider.UserInfoURL,
			JWKSURL:     provider.JWKSURL,
		}).NewProvider(ctx), nil
	}
	return oidclib.NewProvider(ctx, provider.Issuer)
}

func tokenClaims(idToken *oidclib.IDToken) (claims, error) {
	var values claims
	if err := idToken.Claims(&values); err != nil {
		return claims{}, err
	}
	values.Subject = idToken.Subject
	return values, nil
}

func (client *Client) userInfoClaims(ctx context.Context, token *oauth2.Token) (claims, error) {
	ctx = client.context(ctx)
	userInfo, err := client.oidcProvider.UserInfo(ctx, oauth2.StaticTokenSource(token))
	if err != nil {
		return claims{}, err
	}

	var values claims
	if err := userInfo.Claims(&values); err != nil {
		return claims{}, err
	}
	if values.Subject == "" {
		values.Subject = userInfo.Subject
	}
	values = mergeClaims(values, claims{
		Email:         userInfo.Email,
		EmailVerified: userInfo.EmailVerified,
	})
	return values, nil
}

func mergeClaims(primary claims, fallback claims) claims {
	if primary.Email == "" {
		primary.Email = fallback.Email
		primary.EmailVerified = fallback.EmailVerified
	} else if !primary.EmailVerified &&
		strings.TrimSpace(primary.Email) == strings.TrimSpace(fallback.Email) &&
		strings.TrimSpace(fallback.Email) != "" {
		// email_verified only vouches for the email returned by the same
		// claims source. Do not let UserInfo verification for a different
		// address promote an unverified ID-token email.
		primary.EmailVerified = fallback.EmailVerified
	}
	if primary.Name == "" {
		primary.Name = fallback.Name
	}
	if primary.PreferredName == "" {
		primary.PreferredName = fallback.PreferredName
	}
	if primary.GivenName == "" {
		primary.GivenName = fallback.GivenName
	}
	if primary.FamilyName == "" {
		primary.FamilyName = fallback.FamilyName
	}
	if primary.HostedDomain == "" {
		primary.HostedDomain = fallback.HostedDomain
	}
	if primary.OrganizationID == "" {
		primary.OrganizationID = fallback.OrganizationID
	}
	return primary
}

func displayName(values claims) string {
	switch {
	case values.Name != "":
		return strings.TrimSpace(values.Name)
	case values.PreferredName != "":
		return strings.TrimSpace(values.PreferredName)
	default:
		return strings.TrimSpace(strings.Join([]string{values.GivenName, values.FamilyName}, " "))
	}
}

func ensureOpenIDScope(scopes []string) []string {
	normalized := cloneStrings(scopes)
	if slices.Contains(normalized, oidclib.ScopeOpenID) {
		return normalized
	}
	return append([]string{oidclib.ScopeOpenID}, normalized...)
}

func randomURLValue(size int) (string, error) {
	if size <= 0 {
		return "", ErrInvalidConfig
	}
	bytes := make([]byte, size)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

func constantTimeEqual(expected string, actual string) bool {
	if expected == "" || actual == "" || len(expected) != len(actual) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(actual)) == 1
}

func cloneStrings(values []string) []string {
	if values == nil {
		return nil
	}
	cloned := make([]string, len(values))
	copy(cloned, values)
	return cloned
}

func (client *Client) context(ctx context.Context) context.Context {
	if client == nil {
		return ctx
	}
	return clientContext(ctx, client.httpClient)
}

func clientContext(ctx context.Context, httpClient *http.Client) context.Context {
	if httpClient == nil {
		return ctx
	}
	return oidclib.ClientContext(ctx, httpClient)
}

func callbackValuesFromRequest(request *http.Request) (url.Values, error) {
	if request == nil {
		return nil, ErrInvalidCallback
	}
	if request.Method == http.MethodPost && isFormURLEncoded(request.Header.Get("Content-Type")) {
		if err := request.ParseForm(); err != nil {
			return nil, err
		}
		return request.Form, nil
	}
	return request.URL.Query(), nil
}

func isFormURLEncoded(contentType string) bool {
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	return contentType == "application/x-www-form-urlencoded" || strings.HasPrefix(contentType, "application/x-www-form-urlencoded;")
}

func optionalCallbackValue(values url.Values, key string) (string, error) {
	raw, ok := values[key]
	if !ok {
		return "", nil
	}
	if len(raw) != 1 {
		return "", ErrDuplicateParam
	}
	return raw[0], nil
}

func requiredCallbackValue(values url.Values, key string) (string, error) {
	value, err := optionalCallbackValue(values, key)
	if err != nil {
		return "", err
	}
	if value == "" {
		return "", ErrInvalidCallback
	}
	return value, nil
}

func validateURLs(provider identity.Provider, redirectURL string) error {
	if err := validateIssuerURL(provider.Issuer); err != nil {
		return err
	}
	for _, rawURL := range []string{provider.AuthorizationURL, provider.TokenURL, provider.UserInfoURL, provider.JWKSURL} {
		if rawURL == "" {
			continue
		}
		if err := validateProviderURL(rawURL); err != nil {
			return err
		}
	}
	return validateRedirectURL(redirectURL)
}

func validateIssuerURL(rawURL string) error {
	parsed, err := parseHTTPSOrLoopbackURL(rawURL)
	if err != nil {
		return err
	}
	if parsed.RawQuery != "" {
		return ErrInvalidConfig
	}
	return nil
}

func validateProviderURL(rawURL string) error {
	_, err := parseHTTPSOrLoopbackURL(rawURL)
	return err
}

func validateRedirectURL(rawURL string) error {
	_, err := parseHTTPSOrLoopbackURL(rawURL)
	return err
}

func parseHTTPSOrLoopbackURL(rawURL string) (*url.URL, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.Fragment != "" {
		return nil, ErrInvalidConfig
	}
	if parsed.Scheme == "https" || (parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname())) {
		return parsed, nil
	}
	if parsed.Scheme == "http" {
		return nil, ErrInsecureURL
	}
	return nil, ErrInvalidConfig
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

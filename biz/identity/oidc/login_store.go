package oidc

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/im10furry/saas/core/types"
	"golang.org/x/oauth2"
)

const (
	DefaultLoginTTL         = 10 * time.Minute
	DefaultMaxPendingLogins = 10000
)

type LoginRequest struct {
	TenantID  types.TenantID
	UserID    string
	Roles     []string
	ExpiresAt time.Time
}

type Login struct {
	AuthRequest
	TenantID  types.TenantID
	UserID    string
	Roles     []string
	ExpiresAt time.Time
}

type LoginStore interface {
	SaveLogin(ctx context.Context, login Login) error
	ConsumeLogin(ctx context.Context, state string) (Login, error)
}

type MemoryLoginStoreOption func(*MemoryLoginStore)

type MemoryLoginStore struct {
	mu          sync.Mutex
	logins      map[string]Login
	ttl         time.Duration
	maxPending  int
	now         func() time.Time
	lastCleanup time.Time
}

func NewMemoryLoginStore(ttl time.Duration, opts ...MemoryLoginStoreOption) *MemoryLoginStore {
	if ttl <= 0 {
		ttl = DefaultLoginTTL
	}
	store := &MemoryLoginStore{
		logins:     map[string]Login{},
		ttl:        ttl,
		maxPending: DefaultMaxPendingLogins,
		now:        time.Now,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(store)
		}
	}
	if store.maxPending <= 0 {
		store.maxPending = DefaultMaxPendingLogins
	}
	return store
}

func WithMaxPendingLogins(maxPending int) MemoryLoginStoreOption {
	return func(store *MemoryLoginStore) {
		store.maxPending = maxPending
	}
}

func (store *MemoryLoginStore) SaveLogin(ctx context.Context, login Login) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if store == nil || login.State == "" || login.Nonce == "" || login.PKCEVerifier == "" || login.TenantID == "" {
		return ErrInvalidConfig
	}

	now := store.currentTime()
	if login.ExpiresAt.IsZero() {
		login.ExpiresAt = now.Add(store.effectiveTTL())
	}
	if !login.ExpiresAt.After(now) {
		return ErrLoginExpired
	}
	login.UserID = strings.TrimSpace(login.UserID)
	login.Roles = cloneStrings(login.Roles)

	store.mu.Lock()
	defer store.mu.Unlock()

	if store.logins == nil {
		store.logins = map[string]Login{}
	}
	store.cleanupExpiredLocked(now, false)
	if _, ok := store.logins[login.State]; ok {
		return ErrDuplicateParam
	}
	if len(store.logins) >= store.effectiveMaxPending() {
		store.cleanupExpiredLocked(now, true)
		if len(store.logins) >= store.effectiveMaxPending() {
			return ErrLoginStoreFull
		}
	}
	store.logins[login.State] = login
	return nil
}

func (store *MemoryLoginStore) ConsumeLogin(ctx context.Context, state string) (Login, error) {
	if err := ctx.Err(); err != nil {
		return Login{}, err
	}
	if store == nil || state == "" {
		return Login{}, ErrInvalidCallback
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	now := store.currentTime()
	login, ok := store.logins[state]
	if !ok {
		return Login{}, ErrLoginNotFound
	}
	delete(store.logins, state)
	if !login.ExpiresAt.After(now) {
		return Login{}, ErrLoginExpired
	}
	login.Roles = cloneStrings(login.Roles)
	return login, nil
}

func (store *MemoryLoginStore) cleanupExpiredLocked(now time.Time, force bool) {
	if !force && !store.lastCleanup.IsZero() && now.Sub(store.lastCleanup) < time.Minute {
		return
	}
	for state, login := range store.logins {
		if !login.ExpiresAt.After(now) {
			delete(store.logins, state)
		}
	}
	store.lastCleanup = now
}

func (store *MemoryLoginStore) currentTime() time.Time {
	if store.now == nil {
		return time.Now()
	}
	return store.now()
}

func (store *MemoryLoginStore) effectiveTTL() time.Duration {
	if store.ttl <= 0 {
		return DefaultLoginTTL
	}
	return store.ttl
}

func (store *MemoryLoginStore) effectiveMaxPending() int {
	if store.maxPending <= 0 {
		return DefaultMaxPendingLogins
	}
	return store.maxPending
}

func (client *Client) BeginLogin(ctx context.Context, store LoginStore, request LoginRequest, opts ...oauth2.AuthCodeOption) (AuthRequest, error) {
	if store == nil || request.TenantID == "" {
		return AuthRequest{}, ErrInvalidConfig
	}
	authRequest, err := client.Begin(opts...)
	if err != nil {
		return AuthRequest{}, err
	}
	if err := store.SaveLogin(ctx, Login{
		AuthRequest: authRequest,
		TenantID:    request.TenantID,
		UserID:      request.UserID,
		Roles:       request.Roles,
		ExpiresAt:   request.ExpiresAt,
	}); err != nil {
		return AuthRequest{}, err
	}
	return authRequest, nil
}

func (client *Client) HandleLoginCallback(ctx context.Context, request *http.Request, store LoginStore) (Result, error) {
	if request == nil || store == nil {
		return Result{}, ErrInvalidCallback
	}
	values, err := callbackValuesFromRequest(request)
	if err != nil {
		return Result{}, err
	}
	state, err := requiredCallbackValue(values, "state")
	if err != nil {
		return Result{}, err
	}
	providerError, err := optionalCallbackValue(values, "error")
	if err != nil {
		return Result{}, err
	}
	if providerError == "" {
		if _, err := requiredCallbackValue(values, "code"); err != nil {
			return Result{}, err
		}
	}
	login, err := store.ConsumeLogin(ctx, state)
	if err != nil {
		return Result{}, err
	}
	return client.Callback(ctx, Callback{
		Values:        values,
		ExpectedState: login.State,
		ExpectedNonce: login.Nonce,
		PKCEVerifier:  login.PKCEVerifier,
		TenantID:      login.TenantID,
		UserID:        login.UserID,
		Roles:         login.Roles,
	})
}

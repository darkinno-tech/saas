package identity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/darkinno-tech/saas/biz/user"
)

type Service struct {
	users                user.Service
	store                Store
	providers            map[ProviderKey]Provider
	defaultRoles         []string
	requireVerifiedEmail bool
}

type Option func(*Service)

func WithStore(store Store) Option {
	return func(service *Service) {
		if store != nil {
			service.store = store
		}
	}
}

func WithProviders(providers ...Provider) Option {
	return func(service *Service) {
		for _, provider := range providers {
			if provider.Key == "" {
				continue
			}
			service.providers[provider.Key] = cloneProvider(provider)
		}
	}
}

func WithDefaultRoles(roles ...string) Option {
	return func(service *Service) {
		service.defaultRoles = cloneStrings(roles)
	}
}

func WithEmailVerificationRequired(required bool) Option {
	return func(service *Service) {
		service.requireVerifiedEmail = required
	}
}

func NewService(users user.Service, opts ...Option) *Service {
	service := &Service{
		users:                users,
		store:                NewMemoryStore(),
		providers:            map[ProviderKey]Provider{},
		requireVerifiedEmail: true,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(service)
		}
	}
	return service
}

func (service *Service) Authenticate(ctx context.Context, assertion Assertion) (Session, error) {
	if err := ctx.Err(); err != nil {
		return Session{}, err
	}
	if service == nil || service.users == nil || service.store == nil {
		return Session{}, ErrInvalidIdentity
	}
	assertion = normalizeAssertion(assertion)
	if err := assertion.validate(service.requireVerifiedEmail); err != nil {
		return Session{}, err
	}
	if err := service.validateProvider(assertion.Provider); err != nil {
		return Session{}, err
	}

	userID, err := service.userID(ctx, assertion)
	if err != nil {
		return Session{}, err
	}
	if err := service.ensureUser(ctx, userID, assertion); err != nil {
		return Session{}, err
	}

	roles := assertion.Roles
	if len(roles) == 0 {
		roles = cloneStrings(service.defaultRoles)
	}
	if err := service.users.AddMember(ctx, user.Member{TenantID: assertion.TenantID, UserID: userID, Roles: roles}); err != nil {
		if !errors.Is(err, user.ErrMemberExists) {
			return Session{}, err
		}
		member, err := service.users.GetMember(ctx, assertion.TenantID, userID)
		if err != nil {
			return Session{}, err
		}
		roles = member.Roles
	}

	if err := service.store.Link(ctx, Link{
		TenantID:      assertion.TenantID,
		UserID:        userID,
		Provider:      assertion.Provider,
		Subject:       assertion.Subject,
		Email:         assertion.Email,
		Name:          assertion.Name,
		EmailVerified: assertion.EmailVerified,
		Metadata:      assertion.Metadata,
	}); err != nil {
		return Session{}, err
	}

	return Session{
		TenantID: assertion.TenantID,
		UserID:   userID,
		Provider: assertion.Provider,
		Subject:  assertion.Subject,
		Email:    assertion.Email,
		Name:     assertion.Name,
		Roles:    cloneStrings(roles),
	}, nil
}

func (service *Service) validateProvider(key ProviderKey) error {
	provider, ok := service.providers[key]
	if !ok {
		return ErrProviderNotAllowed
	}
	if err := provider.Validate(); err != nil {
		return err
	}
	return nil
}

func (service *Service) userID(ctx context.Context, assertion Assertion) (string, error) {
	userID := assertion.UserID
	if userID == "" {
		if link, err := service.store.GetByExternal(ctx, assertion.TenantID, assertion.Provider, assertion.Subject); err == nil {
			userID = link.UserID
		} else if !errors.Is(err, ErrIdentityNotFound) {
			return "", err
		}
	}
	if userID == "" {
		userID = DefaultUserID(assertion.Provider, assertion.Subject)
	}

	if link, err := service.store.GetByExternal(ctx, assertion.TenantID, assertion.Provider, assertion.Subject); err == nil && link.UserID != userID {
		return "", ErrIdentityConflict
	} else if err != nil && !errors.Is(err, ErrIdentityNotFound) {
		return "", err
	}
	return userID, nil
}

func (service *Service) ensureUser(ctx context.Context, userID string, assertion Assertion) error {
	if current, err := service.users.GetUser(ctx, userID); err == nil {
		return validateExistingUser(current, assertion)
	} else if !errors.Is(err, user.ErrUserNotFound) {
		return err
	}

	err := service.users.CreateUser(ctx, user.User{
		ID:    userID,
		Email: assertion.Email,
		Name:  assertion.Name,
	})
	if errors.Is(err, user.ErrUserExists) {
		current, getErr := service.users.GetUser(ctx, userID)
		if getErr != nil {
			return getErr
		}
		return validateExistingUser(current, assertion)
	}
	return err
}

func validateExistingUser(current user.User, assertion Assertion) error {
	if current.Email != assertion.Email {
		return ErrIdentityConflict
	}
	return nil
}

func DefaultUserID(provider ProviderKey, subject string) string {
	sum := sha256.Sum256([]byte(string(provider) + "\x00" + subject))
	return "idp_" + hex.EncodeToString(sum[:16])
}

func normalizeAssertion(assertion Assertion) Assertion {
	assertion.Provider = ProviderKey(strings.TrimSpace(string(assertion.Provider)))
	assertion.Subject = strings.TrimSpace(assertion.Subject)
	assertion.UserID = strings.TrimSpace(assertion.UserID)
	assertion.Email = strings.TrimSpace(assertion.Email)
	assertion.Name = strings.TrimSpace(assertion.Name)
	assertion.Roles = cloneStrings(assertion.Roles)
	assertion.Metadata = cloneStringMap(assertion.Metadata)
	return assertion
}

func cloneStrings(values []string) []string {
	if values == nil {
		return nil
	}
	cloned := make([]string, len(values))
	copy(cloned, values)
	return cloned
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

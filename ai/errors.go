package ai

import "errors"

var (
	// ErrNilProvider reports an empty provider registration.
	ErrNilProvider = errors.New("saas/ai: nil provider")

	// ErrProviderNotFound reports an unregistered provider lookup.
	ErrProviderNotFound = errors.New("saas/ai: provider not found")

	// ErrNoTenant reports that a metering context carries no tenant.
	ErrNoTenant = errors.New("saas/ai: no tenant in context")

	// ErrInvalidUsage reports invalid usage or a malformed token limit.
	ErrInvalidUsage = errors.New("saas/ai: invalid usage or limit")

	// ErrNilStore reports a meter created with a nil quota store.
	ErrNilStore = errors.New("saas/ai: nil store")
)

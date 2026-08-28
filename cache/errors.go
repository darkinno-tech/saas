package cache

import (
	"errors"

	"github.com/im10furry/saas"
)

var (
	// ErrNoTenant reports cache access without tenant or allowed host context.
	ErrNoTenant = saas.ErrNoTenant

	// ErrUnsafeKey reports a key that attempts to bypass tenant scoping.
	ErrUnsafeKey = errors.New("saas/cache: unsafe key")

	// ErrHostGlobalKeyNotAllowed reports host global key access without explicit opt-in.
	ErrHostGlobalKeyNotAllowed = errors.New("saas/cache: host global key not allowed")

	// ErrInvalidCacheSize reports an invalid bounded memory cache size.
	ErrInvalidCacheSize = errors.New("saas/cache: invalid cache size")

	// ErrInvalidRedisConfig reports an invalid Redis cache adapter configuration.
	ErrInvalidRedisConfig = errors.New("saas/cache: invalid redis config")
)

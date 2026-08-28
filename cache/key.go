package cache

import (
	"context"
	"encoding/base64"
	"strings"

	tenantctx "github.com/im10furry/saas/core/context"
)

const (
	tenantPrefix       = "t2:"
	legacyTenantPrefix = "t:"
	globalPrefix       = "g:"
)

// KeyBuilder creates scoped cache keys.
type KeyBuilder struct {
	AllowHostGlobal bool
}

// Build returns a scoped key for ctx.
func (builder KeyBuilder) Build(ctx context.Context, key string) (string, error) {
	if key == "" ||
		strings.HasPrefix(key, tenantPrefix) ||
		strings.HasPrefix(key, legacyTenantPrefix) ||
		strings.HasPrefix(key, globalPrefix) {
		return "", ErrUnsafeKey
	}

	if tenant, ok := tenantctx.FromContext(ctx); ok {
		encodedTenantID := base64.RawURLEncoding.EncodeToString([]byte(tenant.ID.String()))
		return tenantPrefix + encodedTenantID + ":" + key, nil
	}
	if tenantctx.IsHost(ctx) {
		if !builder.AllowHostGlobal {
			return "", ErrHostGlobalKeyNotAllowed
		}
		return globalPrefix + key, nil
	}
	return "", ErrNoTenant
}

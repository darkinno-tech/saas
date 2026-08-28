package rpc

import (
	"context"

	tenantctx "github.com/im10furry/saas/core/context"
	"github.com/im10furry/saas/core/types"
)

const DefaultTenantMetadataKey = "x-tenant-id"

// Carrier stores RPC metadata.
type Carrier interface {
	Get(key string) (string, bool)
	Set(key string, value string)
}

// InjectTenant writes tenant metadata from ctx into carrier.
func InjectTenant(ctx context.Context, carrier Carrier, key string) error {
	if carrier == nil {
		return ErrInvalidCarrier
	}
	if key == "" {
		key = DefaultTenantMetadataKey
	}

	tenant, ok := tenantctx.FromContext(ctx)
	if !ok || tenant.ID == "" {
		return ErrNoTenantMetadata
	}
	carrier.Set(key, tenant.ID.String())
	return nil
}

// ExtractTenant reads tenant metadata from carrier.
func ExtractTenant(carrier Carrier, key string, strategy types.TenantIDStrategy) (types.TenantID, error) {
	if carrier == nil {
		return "", ErrInvalidCarrier
	}
	if key == "" {
		key = DefaultTenantMetadataKey
	}

	raw, ok := carrier.Get(key)
	if !ok {
		return "", ErrNoTenantMetadata
	}
	return types.ParseTenantID(raw, strategy)
}

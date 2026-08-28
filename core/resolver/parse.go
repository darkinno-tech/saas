package resolver

import "github.com/im10furry/saas/core/types"

func parseTenantID(raw string, strategy types.TenantIDStrategy) (types.TenantID, error) {
	return types.ParseTenantID(raw, strategy)
}

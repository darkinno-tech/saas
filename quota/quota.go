package quota

import "github.com/im10furry/saas/core/types"

// Limit describes a tenant resource limit.
type Limit struct {
	TenantID types.TenantID
	Resource string
	Limit    int64
	Period   Period
}

// Usage describes current resource usage.
type Usage struct {
	TenantID types.TenantID
	Resource string
	Period   Period
	Used     int64
	Limit    int64
}

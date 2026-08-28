package store

import (
	"maps"

	"github.com/darkinno-tech/saas/core/types"
)

func cloneTenant(tenant types.Tenant) types.Tenant {
	if tenant.Config == nil {
		return tenant
	}

	cloned := make(map[string]string, len(tenant.Config))
	for key, value := range tenant.Config {
		cloned[key] = value
	}
	tenant.Config = cloned
	return tenant
}

func tenantsEqual(a types.Tenant, b types.Tenant) bool {
	return a.ID == b.ID &&
		a.Name == b.Name &&
		a.Status == b.Status &&
		a.PlanID == b.PlanID &&
		maps.Equal(a.Config, b.Config)
}

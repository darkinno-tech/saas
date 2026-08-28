package migration

import "github.com/im10furry/saas/core/types"

// Migrator defines tenant-aware table migration planning operations.
type Migrator interface {
	AddTenantColumn(table string, tenantField string, fieldType string) (string, error)
	CreateSoftDeleteUniqueIndex(table string, indexName string, tenantField string, businessFields []string, softDeleteField string) (string, error)
	CreateHardDeleteUniqueIndex(table string, indexName string, tenantField string, businessFields []string) (string, error)
	SeedTenants(table string, tenants []types.Tenant) ([]Statement, error)
}

var _ Migrator = Planner{}

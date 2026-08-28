package gormtenant

import "github.com/im10furry/saas/data"

// Config controls the GORM tenant plugin.
type Config struct {
	TenantField        string
	SoftDeleteField    string
	IncludeSoftDeleted bool
}

func (config Config) normalize() Config {
	if config.TenantField == "" {
		config.TenantField = data.DefaultTenantField
	}
	return config
}

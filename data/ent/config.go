package enttenant

import "github.com/im10furry/saas/data"

// Config controls Ent tenant predicate generation.
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

func (config Config) filterOptions() []data.FilterOption {
	config = config.normalize()

	opts := []data.FilterOption{
		data.WithTenantField(config.TenantField),
		data.WithIncludeSoftDeleted(config.IncludeSoftDeleted),
	}
	if config.SoftDeleteField != "" {
		opts = append(opts, data.WithSoftDeleteField(config.SoftDeleteField))
	}
	return opts
}

package ai

// Registry selects a Provider by name.
type Registry struct {
	providers map[string]Provider
}

// NewRegistry returns an empty provider registry.
func NewRegistry() *Registry {
	return &Registry{providers: map[string]Provider{}}
}

// Register adds a provider under its Name.
func (registry *Registry) Register(provider Provider) error {
	if provider == nil || provider.Name() == "" {
		return ErrNilProvider
	}
	registry.providers[provider.Name()] = provider
	return nil
}

// Get returns the provider registered under name.
func (registry *Registry) Get(name string) (Provider, error) {
	provider, ok := registry.providers[name]
	if !ok {
		return nil, ErrProviderNotFound
	}
	return provider, nil
}

package quota

import (
	"context"

	"github.com/im10furry/saas/core/types"
)

// Service checks and consumes tenant quotas.
type Service struct {
	store Store
}

// NewService creates a quota service.
func NewService(store Store) *Service {
	return &Service{store: store}
}

// Check returns current usage and whether amount can be consumed.
func (service *Service) Check(ctx context.Context, limit Limit, amount int64) (Usage, error) {
	if service == nil || service.store == nil {
		return Usage{}, ErrNilStore
	}
	if err := validateLimit(limit); err != nil {
		return Usage{}, err
	}
	if amount < 0 {
		return Usage{}, ErrInvalidQuota
	}

	used, err := service.store.Get(ctx, limit.TenantID, limit.Resource, limit.Period)
	if err != nil {
		return Usage{}, err
	}
	usage := Usage{
		TenantID: limit.TenantID,
		Resource: limit.Resource,
		Period:   limit.Period,
		Used:     used,
		Limit:    limit.Limit,
	}
	if amount > limit.Limit-used {
		return usage, ErrQuotaExceeded
	}
	return usage, nil
}

// Consume increments usage when the limit allows it.
func (service *Service) Consume(ctx context.Context, limit Limit, amount int64) (Usage, error) {
	if service == nil || service.store == nil {
		return Usage{}, ErrNilStore
	}
	if err := validateLimit(limit); err != nil {
		return Usage{}, err
	}
	if amount < 0 {
		return Usage{}, ErrInvalidQuota
	}

	used, err := service.store.Consume(ctx, limit, amount)
	if err != nil {
		return Usage{}, err
	}
	return Usage{
		TenantID: limit.TenantID,
		Resource: limit.Resource,
		Period:   limit.Period,
		Used:     used,
		Limit:    limit.Limit,
	}, nil
}

// Reset clears usage for a tenant resource.
func (service *Service) Reset(ctx context.Context, tenantID types.TenantID, resource string, period Period) error {
	if service == nil || service.store == nil {
		return ErrNilStore
	}
	return service.store.Reset(ctx, tenantID, resource, period)
}

func validateLimit(limit Limit) error {
	if limit.TenantID == "" || limit.Resource == "" || limit.Period == "" || limit.Limit < 0 {
		return ErrInvalidQuota
	}
	return nil
}

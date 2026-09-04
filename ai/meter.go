package ai

import (
	"context"

	tenantctx "github.com/darkinno-tech/saas/core/context"
	"github.com/darkinno-tech/saas/core/types"
	"github.com/darkinno-tech/saas/quota"
)

// ResourceKey maps a model and usage dimension to a tenant quota resource key.
func ResourceKey(model, dimension string) string {
	if model == "" {
		return "ai//" + dimension
	}
	return "ai//" + model + "//" + dimension
}

// TokenLimit caps a tenant's per-model usage over a period. A zero limit means
// the corresponding dimension is unchecked.
type TokenLimit struct {
	TenantID    types.TenantID
	Model       string
	TurnsLimit  int64
	InputLimit  int64
	OutputLimit int64
	TotalLimit  int64
	Period      quota.Period
}

// Budget shows a tenant's current per-model usage against a TokenLimit.
type Budget struct {
	TenantID    types.TenantID
	Model       string
	Period      quota.Period
	UsedTurns   int64
	UsedInput   int64
	UsedOutput  int64
	UsedTotal   int64
	LimitTurns  int64
	LimitInput  int64
	LimitOutput int64
	LimitTotal  int64
}

// Meter records AI usage against a re-usable, ORM-independent quota store.
type Meter struct {
	store quota.Store
}

// NewMeter creates an AI usage meter backed by a quota store.
func NewMeter(store quota.Store) *Meter {
	return &Meter{store: store}
}

// RecordUsage records token usage without enforcing a limit. The tenant is
// taken from the context.
func (meter *Meter) RecordUsage(ctx context.Context, usage Usage, period quota.Period) error {
	if meter == nil || meter.store == nil {
		return ErrNilStore
	}
	if err := validateUsage(usage); err != nil {
		return err
	}
	tenantID, err := meter.tenantID(ctx)
	if err != nil {
		return err
	}
	if err := recordDimension(ctx, meter.store, tenantID, ResourceKey(usage.Model, DimTurns), period, usage.Turns); err != nil {
		return err
	}
	if err := recordDimension(ctx, meter.store, tenantID, ResourceKey(usage.Model, DimInputTokens), period, usage.InputTokens); err != nil {
		return err
	}
	if err := recordDimension(ctx, meter.store, tenantID, ResourceKey(usage.Model, DimOutputTokens), period, usage.OutputTokens); err != nil {
		return err
	}
	return nil
}

// CheckUsage reports current usage and whether the request stays within the
// tenant token limit.
func (meter *Meter) CheckUsage(ctx context.Context, limit TokenLimit, usage Usage) (Budget, error) {
	if meter == nil || meter.store == nil {
		return Budget{}, ErrNilStore
	}
	if err := validateLimit(limit); err != nil {
		return Budget{}, err
	}
	if err := validateUsage(usage); err != nil {
		return Budget{}, err
	}

	budget, err := meter.snapshot(ctx, limit)
	if err != nil {
		return Budget{}, err
	}
	if limit.TurnsLimit > 0 && budget.UsedTurns+usage.Turns > limit.TurnsLimit {
		return budget, quota.ErrQuotaExceeded
	}
	if limit.InputLimit > 0 && budget.UsedInput+usage.InputTokens > limit.InputLimit {
		return budget, quota.ErrQuotaExceeded
	}
	if limit.OutputLimit > 0 && budget.UsedOutput+usage.OutputTokens > limit.OutputLimit {
		return budget, quota.ErrQuotaExceeded
	}
	if limit.TotalLimit > 0 && budget.UsedTotal+usage.TotalTokens() > limit.TotalLimit {
		return budget, quota.ErrQuotaExceeded
	}
	return budget, nil
}

// ConsumeUsage consumes usage against the tenant token limit. Each dimension
// is enforced atomically by the underlying quota store.
func (meter *Meter) ConsumeUsage(ctx context.Context, limit TokenLimit, usage Usage) (Budget, error) {
	if meter == nil || meter.store == nil {
		return Budget{}, ErrNilStore
	}
	if err := validateLimit(limit); err != nil {
		return Budget{}, err
	}
	if err := validateUsage(usage); err != nil {
		return Budget{}, err
	}

	if usage.Turns > 0 && limit.TurnsLimit > 0 {
		if _, err := meter.store.Consume(ctx, dimLimit(limit, DimTurns, limit.TurnsLimit), usage.Turns); err != nil {
			return Budget{}, err
		}
	}
	if usage.InputTokens > 0 && limit.InputLimit > 0 {
		if _, err := meter.store.Consume(ctx, dimLimit(limit, DimInputTokens, limit.InputLimit), usage.InputTokens); err != nil {
			return Budget{}, err
		}
	}
	if usage.OutputTokens > 0 && limit.OutputLimit > 0 {
		if _, err := meter.store.Consume(ctx, dimLimit(limit, DimOutputTokens, limit.OutputLimit), usage.OutputTokens); err != nil {
			return Budget{}, err
		}
	}
	if usage.TotalTokens() > 0 && limit.TotalLimit > 0 {
		if _, err := meter.store.Consume(ctx, dimLimit(limit, DimTotalTokens, limit.TotalLimit), usage.TotalTokens()); err != nil {
			return Budget{}, err
		}
	}
	return meter.snapshot(ctx, limit)
}

// snapshot reads current usage and returns a Budget without mutating state.
func (meter *Meter) snapshot(ctx context.Context, limit TokenLimit) (Budget, error) {
	turns, err := meter.store.Get(ctx, limit.TenantID, ResourceKey(limit.Model, DimTurns), limit.Period)
	if err != nil {
		return Budget{}, err
	}
	input, err := meter.store.Get(ctx, limit.TenantID, ResourceKey(limit.Model, DimInputTokens), limit.Period)
	if err != nil {
		return Budget{}, err
	}
	output, err := meter.store.Get(ctx, limit.TenantID, ResourceKey(limit.Model, DimOutputTokens), limit.Period)
	if err != nil {
		return Budget{}, err
	}
	return Budget{
		TenantID:    limit.TenantID,
		Model:       limit.Model,
		Period:      limit.Period,
		UsedTurns:   turns,
		UsedInput:   input,
		UsedOutput:  output,
		UsedTotal:   input + output,
		LimitTurns:  limit.TurnsLimit,
		LimitInput:  limit.InputLimit,
		LimitOutput: limit.OutputLimit,
		LimitTotal:  limit.TotalLimit,
	}, nil
}

func (meter *Meter) tenantID(ctx context.Context) (types.TenantID, error) {
	tenant, ok := tenantctx.FromContext(ctx)
	if !ok {
		return "", ErrNoTenant
	}
	return tenant.ID, nil
}

func recordDimension(ctx context.Context, store quota.Store, tenantID types.TenantID, resource string, period quota.Period, amount int64) error {
	if amount <= 0 {
		return nil
	}
	_, err := store.Add(ctx, tenantID, resource, period, amount)
	return err
}

func dimLimit(limit TokenLimit, dimension string, capacity int64) quota.Limit {
	return quota.Limit{
		TenantID: limit.TenantID,
		Resource: ResourceKey(limit.Model, dimension),
		Limit:    capacity,
		Period:   limit.Period,
	}
}

func validateLimit(limit TokenLimit) error {
	if limit.TenantID == "" || limit.Period == "" {
		return ErrInvalidUsage
	}
	return nil
}

func validateUsage(usage Usage) error {
	if usage.Turns < 0 || usage.InputTokens < 0 || usage.OutputTokens < 0 {
		return ErrInvalidUsage
	}
	return nil
}

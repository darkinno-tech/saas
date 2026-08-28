package onboarding

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/darkinno-tech/saas/biz/audit"
	"github.com/darkinno-tech/saas/biz/notification"
	"github.com/darkinno-tech/saas/core/store"
	"github.com/darkinno-tech/saas/core/types"
	"github.com/darkinno-tech/saas/deployment"
	saasfeature "github.com/darkinno-tech/saas/feature"
	saasplan "github.com/darkinno-tech/saas/plan"
	saasquota "github.com/darkinno-tech/saas/quota"
	saassubscription "github.com/darkinno-tech/saas/subscription"
	saastenant "github.com/darkinno-tech/saas/tenant"
)

func TestServiceOnboardInitializesSaaSModules(t *testing.T) {
	ctx := context.Background()
	plans := saasplan.NewMemoryService()
	plan := saasplan.Plan{
		ID:   "starter",
		Name: "Starter",
		Features: []saasplan.Feature{{
			Key:     "exports",
			Enabled: false,
			Config:  map[string]string{"limit": "10"},
		}},
		Quotas: []saasplan.Quota{{
			Resource: "api_calls",
			Limit:    100,
			Period:   saasplan.QuotaPeriodMonth,
		}},
	}
	if err := plans.Create(ctx, plan); err != nil {
		t.Fatalf("plans.Create() error = %v", err)
	}

	features := saasfeature.NewMemoryStore()
	quotas := saasquota.NewMemoryStore()
	audits := audit.NewMemoryStore()
	notifier := notification.NewMemoryNotifier()
	subscriptions := saassubscription.NewMemoryService()
	service := New(
		saastenant.New(store.NewMemoryStore()),
		plans,
		subscriptions,
		WithFeatureStore(features),
		WithQuotaStore(quotas),
		WithAuditStore(audits),
		WithNotifier(notifier),
	)
	periodEnd := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	result, err := service.Onboard(ctx, Input{
		Tenant: saastenant.CreateInput{
			ID:     "tenant-a",
			Name:   "Tenant A",
			PlanID: "starter",
			Config: map[string]string{"region": "us"},
		},
		SubscriptionPeriodEnd: &periodEnd,
		FeatureOverrides: []saasfeature.Flag{{
			Key:     "exports",
			Enabled: true,
			Config:  map[string]string{"limit": "20"},
		}},
		Welcome: &notification.Message{
			TenantID: "tenant-b",
			Channel:  "email",
			To:       "owner@example.com",
			Subject:  "Welcome",
		},
		AuditMetadata: map[string]string{"source": "signup"},
	})
	if err != nil {
		t.Fatalf("Onboard() error = %v", err)
	}

	if result.Tenant.ID != "tenant-a" || result.Tenant.Status != "active" || result.Tenant.PlanID != "starter" {
		t.Fatalf("Tenant = %+v, want active starter tenant", result.Tenant)
	}
	if result.Subscription.Status != saassubscription.StatusActive || result.Subscription.CurrentPeriodEnd == nil || !result.Subscription.CurrentPeriodEnd.Equal(periodEnd) {
		t.Fatalf("Subscription = %+v, want active subscription with period end", result.Subscription)
	}
	if len(result.Features) != 1 || !result.Features[0].Enabled || result.Features[0].Config["limit"] != "20" {
		t.Fatalf("Features = %+v, want tenant override", result.Features)
	}
	if len(result.QuotaLimits) != 1 || result.QuotaLimits[0].Resource != "api_calls" || result.QuotaLimits[0].Period != saasquota.PeriodMonth {
		t.Fatalf("QuotaLimits = %+v, want converted monthly API quota", result.QuotaLimits)
	}

	used, err := quotas.Get(ctx, "tenant-a", "api_calls", saasquota.PeriodMonth)
	if err != nil {
		t.Fatalf("quotas.Get() error = %v", err)
	}
	if used != 0 {
		t.Fatalf("quota usage = %d, want reset to zero", used)
	}

	events, err := audits.List(ctx, "tenant-a")
	if err != nil {
		t.Fatalf("audits.List() error = %v", err)
	}
	if len(events) != 1 || events[0].Action != "tenant.onboard" || events[0].Metadata["plan_id"] != "starter" || events[0].Metadata["source"] != "signup" {
		t.Fatalf("audit events = %+v, want onboarding audit metadata", events)
	}

	messages := notifier.Messages()
	if len(messages) != 1 || messages[0].TenantID != "tenant-a" || messages[0].To != "owner@example.com" {
		t.Fatalf("messages = %+v, want tenant welcome message", messages)
	}
}

func TestServiceOnboardAssignsDeploymentBeforeActivation(t *testing.T) {
	ctx := context.Background()
	plans := saasplan.NewMemoryService()
	if err := plans.Create(ctx, saasplan.Plan{ID: "starter", Name: "Starter"}); err != nil {
		t.Fatalf("plans.Create() error = %v", err)
	}

	deployments := deployment.New(deployment.NewMemoryStore())
	if err := deployments.CreateUnit(ctx, types.DeploymentUnit{
		ID:            "cn-shanghai-1",
		Status:        types.DeploymentUnitStatusActive,
		Region:        "cn-shanghai",
		ResidencyTags: []string{"cn", "data-local"},
	}); err != nil {
		t.Fatalf("CreateUnit() error = %v", err)
	}

	service := New(
		saastenant.New(store.NewMemoryStore()),
		plans,
		saassubscription.NewMemoryService(),
		WithDeploymentService(deployments),
	)
	input := Input{
		Tenant:           saastenant.CreateInput{ID: "tenant-a", Name: "Tenant A", PlanID: "starter"},
		DeploymentUnitID: "cn-shanghai-1",
	}

	result, err := service.Onboard(ctx, input)
	if err != nil {
		t.Fatalf("Onboard() error = %v", err)
	}
	if result.Tenant.Status != types.TenantStatusActive || result.DeploymentUnit.ID != "cn-shanghai-1" {
		t.Fatalf("Onboard() result = %#v, want active tenant assigned to cn-shanghai-1", result)
	}

	resolved, err := deployments.Resolve(ctx, result.Tenant)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolved.ID != result.DeploymentUnit.ID {
		t.Fatalf("Resolve() = %q, want %q", resolved.ID, result.DeploymentUnit.ID)
	}

	retry, err := service.Onboard(ctx, input)
	if err != nil {
		t.Fatalf("Onboard(retry) error = %v", err)
	}
	if retry.DeploymentUnit.ID != "cn-shanghai-1" {
		t.Fatalf("Onboard(retry) deployment = %q, want cn-shanghai-1", retry.DeploymentUnit.ID)
	}
}

func TestServiceOnboardRejectsDeploymentInputWithoutMatchingService(t *testing.T) {
	ctx := context.Background()
	plans := saasplan.NewMemoryService()
	if err := plans.Create(ctx, saasplan.Plan{ID: "starter", Name: "Starter"}); err != nil {
		t.Fatalf("plans.Create() error = %v", err)
	}

	withoutService := New(saastenant.New(store.NewMemoryStore()), plans, saassubscription.NewMemoryService())
	if _, err := withoutService.Onboard(ctx, Input{
		Tenant:           saastenant.CreateInput{ID: "tenant-a", Name: "Tenant A", PlanID: "starter"},
		DeploymentUnitID: "cn-shanghai-1",
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Onboard(without deployment service) error = %v, want ErrInvalidInput", err)
	}

	withService := New(
		saastenant.New(store.NewMemoryStore()),
		plans,
		saassubscription.NewMemoryService(),
		WithDeploymentService(deployment.New(deployment.NewMemoryStore())),
	)
	if _, err := withService.Onboard(ctx, Input{
		Tenant: saastenant.CreateInput{ID: "tenant-a", Name: "Tenant A", PlanID: "starter"},
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Onboard(with deployment service but no unit) error = %v, want ErrInvalidInput", err)
	}
}

func TestServiceOnboardValidation(t *testing.T) {
	ctx := context.Background()
	plans := saasplan.NewMemoryService()
	subscriptions := saassubscription.NewMemoryService()
	service := New(saastenant.New(store.NewMemoryStore()), plans, subscriptions)

	if _, err := service.Onboard(ctx, Input{}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Onboard(empty) error = %v, want ErrInvalidInput", err)
	}
	if _, err := service.Onboard(ctx, Input{Tenant: saastenant.CreateInput{Name: "Tenant", PlanID: "missing"}}); !errors.Is(err, saasplan.ErrPlanNotFound) {
		t.Fatalf("Onboard(missing plan) error = %v, want ErrPlanNotFound", err)
	}

	backing := store.NewMemoryStore()
	periodEnd := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	service = New(saastenant.New(backing), plans, basicSubscriptionService{})
	_, err := service.Onboard(ctx, Input{
		Tenant: saastenant.CreateInput{
			ID:     "tenant-a",
			Name:   "Tenant A",
			PlanID: "starter",
		},
		SubscriptionPeriodEnd: &periodEnd,
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Onboard(period unsupported) error = %v, want ErrInvalidInput", err)
	}
	if _, err := backing.Get(ctx, "tenant-a"); !errors.Is(err, store.ErrTenantNotFound) {
		t.Fatalf("tenant created before validation, Get() error = %v, want ErrTenantNotFound", err)
	}
}

func TestServiceOnboardResumesAfterPostActivationFailure(t *testing.T) {
	ctx := context.Background()
	plans := saasplan.NewMemoryService()
	if err := plans.Create(ctx, saasplan.Plan{
		ID: "starter", Name: "Starter",
		Quotas: []saasplan.Quota{{Resource: "api_calls", Limit: 100, Period: saasplan.QuotaPeriodMonth}},
	}); err != nil {
		t.Fatalf("plans.Create() error = %v", err)
	}

	backing := store.NewMemoryStore()
	quotas := saasquota.NewMemoryStore()
	audits := audit.NewMemoryStore()
	notifications := []notification.Message{}
	wantErr := errors.New("notifier unavailable")
	fail := true
	notifier := notification.NotifierFunc(func(_ context.Context, message notification.Message) error {
		if fail {
			return wantErr
		}
		notifications = append(notifications, message.Clone())
		return nil
	})
	service := New(
		saastenant.New(backing),
		plans,
		saassubscription.NewMemoryService(),
		WithQuotaStore(quotas),
		WithAuditStore(audits),
		WithNotifier(notifier),
	)
	input := Input{
		Tenant:  saastenant.CreateInput{ID: "tenant-a", Name: "Tenant A", PlanID: "starter"},
		Welcome: &notification.Message{Channel: "email", To: "owner@example.com", Subject: "Welcome"},
	}

	partial, err := service.Onboard(ctx, input)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Onboard() error = %v, want notifier error", err)
	}
	if partial.Tenant.Status != types.TenantStatusActive || partial.Subscription.Status != saassubscription.StatusActive {
		t.Fatalf("Onboard() partial result = %+v, want committed active tenant and subscription", partial)
	}
	if _, err := quotas.Add(ctx, "tenant-a", "api_calls", saasquota.PeriodMonth, 7); err != nil {
		t.Fatalf("quotas.Add() error = %v", err)
	}

	fail = false
	result, err := service.Onboard(ctx, input)
	if err != nil {
		t.Fatalf("Onboard(retry) error = %v", err)
	}
	if result.Tenant.Status != types.TenantStatusActive || result.Subscription.Status != saassubscription.StatusActive {
		t.Fatalf("Onboard(retry) result = %+v, want active state", result)
	}
	used, err := quotas.Get(ctx, "tenant-a", "api_calls", saasquota.PeriodMonth)
	if err != nil {
		t.Fatalf("quotas.Get() error = %v", err)
	}
	if used != 7 {
		t.Fatalf("quota usage after resume = %d, want 7 without reset", used)
	}
	events, err := audits.List(ctx, "tenant-a")
	if err != nil {
		t.Fatalf("audits.List() error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("audit events after resume = %d, want one", len(events))
	}
	if len(notifications) != 1 || notifications[0].ID == "" {
		t.Fatalf("notifications = %+v, want one idempotent welcome", notifications)
	}

	if _, err := service.Onboard(ctx, input); err != nil {
		t.Fatalf("Onboard(idempotent retry) error = %v", err)
	}
	if len(notifications) != 1 {
		t.Fatalf("notifications after repeated retry = %d, want one", len(notifications))
	}
}

func TestServiceOnboardSkipActivationRetryDoesNotResetQuota(t *testing.T) {
	ctx := context.Background()
	plans := saasplan.NewMemoryService()
	if err := plans.Create(ctx, saasplan.Plan{
		ID: "starter", Name: "Starter",
		Quotas: []saasplan.Quota{{Resource: "api_calls", Limit: 100, Period: saasplan.QuotaPeriodMonth}},
	}); err != nil {
		t.Fatalf("plans.Create() error = %v", err)
	}

	quotas := saasquota.NewMemoryStore()
	fail := true
	wantErr := errors.New("notifier unavailable")
	notifier := notification.NotifierFunc(func(context.Context, notification.Message) error {
		if fail {
			return wantErr
		}
		return nil
	})
	service := New(
		saastenant.New(store.NewMemoryStore()),
		plans,
		saassubscription.NewMemoryService(),
		WithQuotaStore(quotas),
		WithNotifier(notifier),
	)
	input := Input{
		Tenant:         saastenant.CreateInput{ID: "tenant-a", Name: "Tenant A", PlanID: "starter"},
		SkipActivation: true,
		Welcome:        &notification.Message{Channel: "email", To: "owner@example.com", Subject: "Welcome"},
	}

	partial, err := service.Onboard(ctx, input)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Onboard() error = %v, want notifier error", err)
	}
	if partial.Tenant.Status != types.TenantStatusPending || partial.Subscription.Status != saassubscription.StatusActive {
		t.Fatalf("Onboard() partial = %+v, want pending tenant with active subscription", partial)
	}
	if _, err := quotas.Add(ctx, "tenant-a", "api_calls", saasquota.PeriodMonth, 7); err != nil {
		t.Fatalf("quotas.Add() error = %v", err)
	}

	fail = false
	result, err := service.Onboard(ctx, input)
	if err != nil {
		t.Fatalf("Onboard(retry) error = %v", err)
	}
	if result.Tenant.Status != types.TenantStatusPending {
		t.Fatalf("Onboard(retry) status = %q, want pending", result.Tenant.Status)
	}
	used, err := quotas.Get(ctx, "tenant-a", "api_calls", saasquota.PeriodMonth)
	if err != nil {
		t.Fatalf("quotas.Get() error = %v", err)
	}
	if used != 7 {
		t.Fatalf("quota usage after SkipActivation retry = %d, want 7", used)
	}
}

func TestServiceWelcomeDedupeKeyIncludesTenant(t *testing.T) {
	ctx := context.Background()
	plans := saasplan.NewMemoryService()
	if err := plans.Create(ctx, saasplan.Plan{ID: "starter", Name: "Starter"}); err != nil {
		t.Fatalf("plans.Create() error = %v", err)
	}
	notifier := notification.NewMemoryNotifier()
	service := New(
		saastenant.New(store.NewMemoryStore()),
		plans,
		saassubscription.NewMemoryService(),
		WithNotifier(notifier),
	)
	for _, tenantID := range []types.TenantID{"tenant-a", "tenant-b"} {
		_, err := service.Onboard(ctx, Input{
			Tenant:  saastenant.CreateInput{ID: tenantID, Name: tenantID.String(), PlanID: "starter"},
			Welcome: &notification.Message{ID: "shared-request-id", Channel: "email", To: "owner@example.com", Subject: "Welcome"},
		})
		if err != nil {
			t.Fatalf("Onboard(%s) error = %v", tenantID, err)
		}
	}
	messages := notifier.Messages()
	if len(messages) != 2 || messages[0].TenantID == messages[1].TenantID {
		t.Fatalf("welcome messages = %+v, want one delivery for each tenant", messages)
	}
}

func TestServiceOnboardReturnsGeneratedIDAfterTenantAuditFailure(t *testing.T) {
	ctx := context.Background()
	plans := saasplan.NewMemoryService()
	if err := plans.Create(ctx, saasplan.Plan{
		ID: "starter", Name: "Starter",
		Quotas: []saasplan.Quota{{Resource: "api_calls", Limit: 100, Period: saasplan.QuotaPeriodMonth}},
	}); err != nil {
		t.Fatalf("plans.Create() error = %v", err)
	}
	wantErr := errors.New("tenant audit unavailable")
	tenants := saastenant.New(
		store.NewMemoryStore(),
		saastenant.WithIDGenerator(func(context.Context) (types.TenantID, error) { return "generated-id", nil }),
		saastenant.WithAuditor(func(context.Context, saastenant.Event) error { return wantErr }),
	)
	service := New(tenants, plans, saassubscription.NewMemoryService())
	input := Input{
		Tenant:         saastenant.CreateInput{Name: "Tenant A", PlanID: "starter"},
		SkipActivation: true,
	}

	partial, err := service.Onboard(ctx, input)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Onboard() error = %v, want tenant audit error", err)
	}
	if partial.Tenant.ID != "generated-id" || partial.Plan.ID != "starter" || len(partial.QuotaLimits) != 1 {
		t.Fatalf("Onboard() partial = %+v, want generated tenant ID and selected plan state", partial)
	}

	input.Tenant.ID = partial.Tenant.ID
	result, err := service.Onboard(ctx, input)
	if err != nil {
		t.Fatalf("Onboard(retry with partial ID) error = %v", err)
	}
	if result.Tenant.ID != partial.Tenant.ID || result.Subscription.Status != saassubscription.StatusActive {
		t.Fatalf("Onboard(retry) result = %+v, want resumed generated tenant", result)
	}
}

func TestServiceRejectsSubscriptionPeriodConflictBeforeInitialization(t *testing.T) {
	ctx := context.Background()
	plans := saasplan.NewMemoryService()
	if err := plans.Create(ctx, saasplan.Plan{
		ID: "starter", Name: "Starter",
		Features: []saasplan.Feature{{Key: "exports", Enabled: true}},
		Quotas:   []saasplan.Quota{{Resource: "api_calls", Limit: 100, Period: saasplan.QuotaPeriodMonth}},
	}); err != nil {
		t.Fatalf("plans.Create() error = %v", err)
	}
	tenants := saastenant.New(store.NewMemoryStore())
	if _, err := tenants.Create(ctx, saastenant.CreateInput{ID: "tenant-a", Name: "Tenant A", PlanID: "starter"}); err != nil {
		t.Fatalf("tenants.Create() error = %v", err)
	}
	periodEnd := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	subscriptions := saassubscription.NewMemoryService()
	if _, err := subscriptions.SubscribeWithPeriod(ctx, "tenant-a", "starter", periodEnd); err != nil {
		t.Fatalf("subscriptions.SubscribeWithPeriod() error = %v", err)
	}
	features := saasfeature.NewMemoryStore()
	if err := features.SetPlanDefaults(ctx, "starter", []saasfeature.Flag{{Key: "exports", Enabled: false}}); err != nil {
		t.Fatalf("features.SetPlanDefaults() error = %v", err)
	}
	quotas := saasquota.NewMemoryStore()
	if _, err := quotas.Add(ctx, "tenant-a", "api_calls", saasquota.PeriodMonth, 7); err != nil {
		t.Fatalf("quotas.Add() error = %v", err)
	}
	service := New(tenants, plans, subscriptions, WithFeatureStore(features), WithQuotaStore(quotas))

	_, err := service.Onboard(ctx, Input{
		Tenant: saastenant.CreateInput{ID: "tenant-a", Name: "Tenant A", PlanID: "starter"},
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Onboard(period mismatch) error = %v, want ErrInvalidInput", err)
	}
	flag, err := features.Resolve(ctx, "tenant-a", "starter", "exports")
	if err != nil {
		t.Fatalf("features.Resolve() error = %v", err)
	}
	if flag.Enabled {
		t.Fatal("subscription conflict changed feature defaults before failing")
	}
	used, err := quotas.Get(ctx, "tenant-a", "api_calls", saasquota.PeriodMonth)
	if err != nil {
		t.Fatalf("quotas.Get() error = %v", err)
	}
	if used != 7 {
		t.Fatalf("subscription conflict reset quota usage to %d, want 7", used)
	}
}

type basicSubscriptionService struct{}

func (basicSubscriptionService) Subscribe(context.Context, types.TenantID, string) (saassubscription.Subscription, error) {
	return saassubscription.Subscription{}, nil
}

func (basicSubscriptionService) Unsubscribe(context.Context, types.TenantID) (saassubscription.Subscription, error) {
	return saassubscription.Subscription{}, nil
}

func (basicSubscriptionService) Upgrade(context.Context, types.TenantID, string) (saassubscription.Subscription, error) {
	return saassubscription.Subscription{}, nil
}

func (basicSubscriptionService) Downgrade(context.Context, types.TenantID, string) (saassubscription.Subscription, error) {
	return saassubscription.Subscription{}, nil
}

func (basicSubscriptionService) Get(context.Context, types.TenantID) (saassubscription.Subscription, error) {
	return saassubscription.Subscription{}, saassubscription.ErrSubscriptionNotFound
}

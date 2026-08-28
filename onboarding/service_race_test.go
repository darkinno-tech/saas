package onboarding

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/im10furry/saas/core/store"
	"github.com/im10furry/saas/core/types"
	saasplan "github.com/im10furry/saas/plan"
	saassubscription "github.com/im10furry/saas/subscription"
	saastenant "github.com/im10furry/saas/tenant"
)

func TestServiceOnboardResumesSubscriptionCreatedByConcurrentRequest(t *testing.T) {
	ctx := context.Background()
	plans := saasplan.NewMemoryService()
	if err := plans.Create(ctx, saasplan.Plan{ID: "starter", Name: "Starter"}); err != nil {
		t.Fatalf("plans.Create() error = %v", err)
	}

	raced := &subscriptionCreateRace{
		subscription: saassubscription.Subscription{
			TenantID:  "tenant-a",
			PlanID:    "starter",
			Status:    saassubscription.StatusActive,
			StartDate: time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC),
		},
	}
	service := New(saastenant.New(store.NewMemoryStore()), plans, raced)

	result, err := service.Onboard(ctx, Input{
		Tenant: saastenant.CreateInput{ID: "tenant-a", Name: "Tenant A", PlanID: "starter"},
	})
	if err != nil {
		t.Fatalf("Onboard() error = %v", err)
	}
	if raced.getCalls != 2 || raced.subscribeCalls != 1 {
		t.Fatalf("subscription calls = Get:%d Subscribe:%d, want Get twice and Subscribe once", raced.getCalls, raced.subscribeCalls)
	}
	if result.Subscription != raced.subscription {
		t.Fatalf("Subscription = %+v, want concurrent subscription %+v", result.Subscription, raced.subscription)
	}
	if result.Tenant.Status != types.TenantStatusActive {
		t.Fatalf("Tenant status = %q, want active after resumed subscription", result.Tenant.Status)
	}
}

func TestServiceOnboardRejectsChangedRetryBeforeSubscriptionMutation(t *testing.T) {
	ctx := context.Background()
	plans := saasplan.NewMemoryService()
	if err := plans.Create(ctx, saasplan.Plan{ID: "starter", Name: "Starter"}); err != nil {
		t.Fatalf("plans.Create() error = %v", err)
	}

	tenants := saastenant.New(store.NewMemoryStore())
	if _, err := tenants.Create(ctx, saastenant.CreateInput{
		ID:     "tenant-a",
		Name:   "Tenant A",
		PlanID: "starter",
		Config: map[string]string{"region": "cn"},
	}); err != nil {
		t.Fatalf("tenants.Create() error = %v", err)
	}
	subscriptions := &subscriptionCreateRace{}
	service := New(tenants, plans, subscriptions)

	_, err := service.Onboard(ctx, Input{
		Tenant: saastenant.CreateInput{
			ID:     "tenant-a",
			Name:   "Changed name",
			PlanID: "starter",
			Config: map[string]string{"region": "cn"},
		},
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Onboard(changed retry) error = %v, want ErrInvalidInput", err)
	}
	if subscriptions.getCalls != 0 || subscriptions.subscribeCalls != 0 {
		t.Fatalf("subscription calls = Get:%d Subscribe:%d, want no mutation after tenant retry mismatch", subscriptions.getCalls, subscriptions.subscribeCalls)
	}
}

func TestServiceSubscribeOrResumeSurfacesIncompleteConcurrentWrites(t *testing.T) {
	ctx := context.Background()
	backendErr := errors.New("subscription backend unavailable")
	tests := []struct {
		name         string
		traced       *subscriptionCreateRace
		wantErr      error
		wantGetCalls int
	}{
		{
			name:         "winner disappeared before readback",
			traced:       &subscriptionCreateRace{alwaysMissing: true},
			wantErr:      saassubscription.ErrSubscriptionNotFound,
			wantGetCalls: 1,
		},
		{
			name: "winner has incompatible plan",
			traced: &subscriptionCreateRace{visibleOnFirstGet: true, subscription: saassubscription.Subscription{
				TenantID: "tenant-a", PlanID: "enterprise", Status: saassubscription.StatusActive,
			}},
			wantErr:      ErrInvalidInput,
			wantGetCalls: 1,
		},
		{
			name:         "subscription backend failure",
			traced:       &subscriptionCreateRace{subscribeErr: backendErr},
			wantErr:      backendErr,
			wantGetCalls: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &Service{subscriptions: tt.traced}
			_, err := service.subscribeOrResume(ctx, "tenant-a", "starter", nil)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("subscribeOrResume() error = %v, want %v", err, tt.wantErr)
			}
			if tt.traced.getCalls != tt.wantGetCalls || tt.traced.subscribeCalls != 1 {
				t.Fatalf("subscription calls = Get:%d Subscribe:%d, want Get:%d Subscribe:1", tt.traced.getCalls, tt.traced.subscribeCalls, tt.wantGetCalls)
			}
		})
	}
}

// subscriptionCreateRace models the legitimate race where another worker
// creates the same subscription between Onboard's first lookup and create.
type subscriptionCreateRace struct {
	subscription      saassubscription.Subscription
	subscribeErr      error
	alwaysMissing     bool
	visibleOnFirstGet bool
	getCalls          int
	subscribeCalls    int
}

func (service *subscriptionCreateRace) Subscribe(context.Context, types.TenantID, string) (saassubscription.Subscription, error) {
	service.subscribeCalls++
	if service.subscribeErr != nil {
		return saassubscription.Subscription{}, service.subscribeErr
	}
	return saassubscription.Subscription{}, saassubscription.ErrSubscriptionAlreadyExists
}

func (service *subscriptionCreateRace) Unsubscribe(context.Context, types.TenantID) (saassubscription.Subscription, error) {
	return saassubscription.Subscription{}, saassubscription.ErrSubscriptionNotFound
}

func (service *subscriptionCreateRace) Upgrade(context.Context, types.TenantID, string) (saassubscription.Subscription, error) {
	return saassubscription.Subscription{}, saassubscription.ErrSubscriptionNotFound
}

func (service *subscriptionCreateRace) Downgrade(context.Context, types.TenantID, string) (saassubscription.Subscription, error) {
	return saassubscription.Subscription{}, saassubscription.ErrSubscriptionNotFound
}

func (service *subscriptionCreateRace) Get(context.Context, types.TenantID) (saassubscription.Subscription, error) {
	service.getCalls++
	if service.alwaysMissing || (service.getCalls == 1 && !service.visibleOnFirstGet) {
		return saassubscription.Subscription{}, saassubscription.ErrSubscriptionNotFound
	}
	return service.subscription, nil
}

package subscription

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/im10furry/saas/core/types"
)

func TestMemoryServiceExpirePersistsLifecycleAndEmitsBillingEvent(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 17, 9, 0, 0, 0, time.UTC)
	periodEnd := now.Add(30 * 24 * time.Hour)
	events := []BillingEvent{}
	service := NewMemoryService(
		WithClock(func() time.Time { return now }),
		WithGracePeriod(72*time.Hour),
		WithBillingHook(func(_ context.Context, event BillingEvent) error {
			events = append(events, event)
			return nil
		}),
	)

	if _, err := service.SubscribeWithPeriod(ctx, "tenant-a", "pro", periodEnd); err != nil {
		t.Fatalf("SubscribeWithPeriod() error = %v", err)
	}
	now = now.Add(90 * time.Minute)

	expired, err := service.Expire(ctx, "tenant-a")
	if err != nil {
		t.Fatalf("Expire() error = %v", err)
	}
	if expired.Status != StatusExpired || expired.EndDate == nil || !expired.EndDate.Equal(now) {
		t.Fatalf("Expire() = %+v, want expired subscription ending at %v", expired, now)
	}
	if expired.CurrentPeriodEnd == nil || !expired.CurrentPeriodEnd.Equal(periodEnd) {
		t.Fatalf("Expire() CurrentPeriodEnd = %v, want %v", expired.CurrentPeriodEnd, periodEnd)
	}
	if expired.GracePeriodEnd == nil || !expired.GracePeriodEnd.Equal(periodEnd.Add(72*time.Hour)) {
		t.Fatalf("Expire() GracePeriodEnd = %v, want %v", expired.GracePeriodEnd, periodEnd.Add(72*time.Hour))
	}

	persisted, err := service.Get(ctx, "tenant-a")
	if err != nil {
		t.Fatalf("Get() after Expire error = %v", err)
	}
	if !subscriptionsEqual(persisted, expired) {
		t.Fatalf("Get() after Expire = %+v, want %+v", persisted, expired)
	}
	if len(events) != 2 {
		t.Fatalf("billing events = %+v, want subscribe and expire", events)
	}
	expireEvent := events[1]
	if expireEvent.Action != "expire" || expireEvent.TenantID != "tenant-a" || expireEvent.FromPlan != "pro" || expireEvent.Status != StatusExpired || expireEvent.CurrentPeriodEnd == nil || !expireEvent.CurrentPeriodEnd.Equal(periodEnd) {
		t.Fatalf("expire billing event = %+v, want tenant-a/pro/expired with period end", expireEvent)
	}

	if _, err := service.Expire(ctx, "tenant-a"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Expire(expired) error = %v, want ErrInvalidTransition", err)
	}
	if _, err := service.Expire(ctx, "missing"); !errors.Is(err, ErrSubscriptionNotFound) {
		t.Fatalf("Expire(missing) error = %v, want ErrSubscriptionNotFound", err)
	}
	if _, err := service.Expire(ctx, ""); !errors.Is(err, ErrInvalidSubscription) {
		t.Fatalf("Expire(empty tenant) error = %v, want ErrInvalidSubscription", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := service.Expire(cancelled, "tenant-a"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Expire(cancelled context) error = %v, want context.Canceled", err)
	}
}

func TestMemoryServiceRenewReactivatesExpiredSubscriptionAndRejectsCancelled(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
	initialPeriodEnd := now.Add(24 * time.Hour)
	renewalPeriodEnd := now.Add(31 * 24 * time.Hour)
	events := []BillingEvent{}
	service := NewMemoryService(
		WithClock(func() time.Time { return now }),
		WithBillingHook(func(_ context.Context, event BillingEvent) error {
			events = append(events, event)
			return nil
		}),
	)

	if _, err := service.SubscribeWithPeriod(ctx, "tenant-expired", "starter", initialPeriodEnd); err != nil {
		t.Fatalf("SubscribeWithPeriod() error = %v", err)
	}
	now = now.Add(time.Hour)
	if _, err := service.Expire(ctx, "tenant-expired"); err != nil {
		t.Fatalf("Expire() error = %v", err)
	}

	renewed, err := service.Renew(ctx, "tenant-expired", renewalPeriodEnd)
	if err != nil {
		t.Fatalf("Renew(expired) error = %v", err)
	}
	if renewed.Status != StatusActive || renewed.EndDate != nil || renewed.CurrentPeriodEnd == nil || !renewed.CurrentPeriodEnd.Equal(renewalPeriodEnd) || renewed.GracePeriodEnd != nil {
		t.Fatalf("Renew(expired) = %+v, want active subscription with only the new period end", renewed)
	}
	persisted, err := service.Get(ctx, "tenant-expired")
	if err != nil {
		t.Fatalf("Get() after Renew error = %v", err)
	}
	if !subscriptionsEqual(persisted, renewed) {
		t.Fatalf("Get() after Renew = %+v, want %+v", persisted, renewed)
	}
	if len(events) != 3 || events[2].Action != "renew" || events[2].ToPlan != "starter" || events[2].Status != StatusActive || events[2].CurrentPeriodEnd == nil || !events[2].CurrentPeriodEnd.Equal(renewalPeriodEnd) {
		t.Fatalf("renew billing events = %+v, want subscribe/expire/renew with active renewal", events)
	}

	if _, err := service.Subscribe(ctx, "tenant-cancelled", "starter"); err != nil {
		t.Fatalf("Subscribe(cancelled tenant) error = %v", err)
	}
	if _, err := service.Unsubscribe(ctx, "tenant-cancelled"); err != nil {
		t.Fatalf("Unsubscribe() error = %v", err)
	}
	eventsBeforeRejectedRenewal := len(events)
	if _, err := service.Renew(ctx, "tenant-cancelled", renewalPeriodEnd); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Renew(cancelled) error = %v, want ErrInvalidTransition", err)
	}
	if len(events) != eventsBeforeRejectedRenewal {
		t.Fatalf("Renew(cancelled) emitted billing event: %+v", events[eventsBeforeRejectedRenewal:])
	}
	cancelledSubscription, err := service.Get(ctx, "tenant-cancelled")
	if err != nil {
		t.Fatalf("Get(cancelled tenant) error = %v", err)
	}
	if cancelledSubscription.Status != StatusCancelled {
		t.Fatalf("Renew(cancelled) changed status to %q, want %q", cancelledSubscription.Status, StatusCancelled)
	}

	if _, err := service.Renew(ctx, "", renewalPeriodEnd); !errors.Is(err, ErrInvalidSubscription) {
		t.Fatalf("Renew(empty tenant) error = %v, want ErrInvalidSubscription", err)
	}
	if _, err := service.Renew(ctx, "tenant-expired", time.Time{}); !errors.Is(err, ErrInvalidSubscription) {
		t.Fatalf("Renew(zero period end) error = %v, want ErrInvalidSubscription", err)
	}
	if _, err := service.Renew(ctx, "missing", renewalPeriodEnd); !errors.Is(err, ErrSubscriptionNotFound) {
		t.Fatalf("Renew(missing) error = %v, want ErrSubscriptionNotFound", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := service.Renew(cancelled, "tenant-expired", renewalPeriodEnd); !errors.Is(err, context.Canceled) {
		t.Fatalf("Renew(cancelled context) error = %v, want context.Canceled", err)
	}
}

func TestMemoryServiceSubscribeWithPeriodValidatesInputAndAppliesGracePeriod(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 17, 11, 0, 0, 0, time.UTC)
	periodEnd := now.Add(14 * 24 * time.Hour)
	events := []BillingEvent{}
	withoutGrace := NewMemoryService(
		WithClock(func() time.Time { return now }),
		WithBillingHook(func(_ context.Context, event BillingEvent) error {
			events = append(events, event)
			return nil
		}),
	)

	if _, err := withoutGrace.SubscribeWithPeriod(ctx, "tenant-zero", "starter", time.Time{}); !errors.Is(err, ErrInvalidSubscription) {
		t.Fatalf("SubscribeWithPeriod(zero period end) error = %v, want ErrInvalidSubscription", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := withoutGrace.SubscribeWithPeriod(cancelled, "tenant-cancelled", "starter", periodEnd); !errors.Is(err, context.Canceled) {
		t.Fatalf("SubscribeWithPeriod(cancelled context) error = %v, want context.Canceled", err)
	}
	if len(events) != 0 {
		t.Fatalf("invalid SubscribeWithPeriod calls emitted billing events: %+v", events)
	}
	if _, err := withoutGrace.Get(ctx, "tenant-zero"); !errors.Is(err, ErrSubscriptionNotFound) {
		t.Fatalf("Get(tenant-zero) after rejected subscription error = %v, want ErrSubscriptionNotFound", err)
	}
	if _, err := withoutGrace.Get(ctx, "tenant-cancelled"); !errors.Is(err, ErrSubscriptionNotFound) {
		t.Fatalf("Get(tenant-cancelled) after rejected subscription error = %v, want ErrSubscriptionNotFound", err)
	}

	subscribed, err := withoutGrace.SubscribeWithPeriod(ctx, "tenant-no-grace", "starter", periodEnd)
	if err != nil {
		t.Fatalf("SubscribeWithPeriod() error = %v", err)
	}
	if subscribed.Status != StatusActive || !subscribed.StartDate.Equal(now) || subscribed.CurrentPeriodEnd == nil || !subscribed.CurrentPeriodEnd.Equal(periodEnd) || subscribed.GracePeriodEnd != nil {
		t.Fatalf("SubscribeWithPeriod() = %+v, want active subscription with no grace period", subscribed)
	}
	if len(events) != 1 || events[0].Action != "subscribe" || events[0].CurrentPeriodEnd == nil || !events[0].CurrentPeriodEnd.Equal(periodEnd) {
		t.Fatalf("SubscribeWithPeriod() billing event = %+v, want subscribe with period end", events)
	}

	withGrace := NewMemoryService(
		WithClock(func() time.Time { return now }),
		WithGracePeriod(48*time.Hour),
	)
	graceSubscription, err := withGrace.SubscribeWithPeriod(ctx, "tenant-with-grace", "pro", periodEnd)
	if err != nil {
		t.Fatalf("SubscribeWithPeriod(with grace) error = %v", err)
	}
	if graceSubscription.GracePeriodEnd == nil || !graceSubscription.GracePeriodEnd.Equal(periodEnd.Add(48*time.Hour)) {
		t.Fatalf("SubscribeWithPeriod(with grace) GracePeriodEnd = %v, want %v", graceSubscription.GracePeriodEnd, periodEnd.Add(48*time.Hour))
	}
}

func TestMemoryStoreListAndPageApplyBusinessFiltersBeforeCursor(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	for _, subscription := range []Subscription{
		{TenantID: "tenant-a", PlanID: "starter", Status: StatusActive, StartDate: start},
		{TenantID: "tenant-b", PlanID: "starter", Status: StatusExpired, StartDate: start},
		{TenantID: "tenant-c", PlanID: "pro", Status: StatusActive, StartDate: start},
		{TenantID: "tenant-d", PlanID: "pro", Status: StatusCancelled, StartDate: start},
	} {
		if err := store.Create(ctx, subscription); err != nil {
			t.Fatalf("Create(%s) error = %v", subscription.TenantID, err)
		}
	}

	activePro, err := store.List(ctx, ListFilter{PlanIDs: []string{"pro"}, Statuses: []Status{StatusActive}})
	if err != nil {
		t.Fatalf("List(active pro) error = %v", err)
	}
	assertSubscriptionTenantIDs(t, activePro, "tenant-c")

	page, err := store.ListPage(ctx, PageFilter{
		TenantIDs: []types.TenantID{"tenant-b", "tenant-c", "tenant-d"},
		Cursor:    "tenant-b",
		Limit:     2,
	})
	if err != nil {
		t.Fatalf("ListPage(filtered cursor) error = %v", err)
	}
	assertSubscriptionTenantIDs(t, page, "tenant-c", "tenant-d")

	page, err = store.ListPage(ctx, PageFilter{PlanIDs: []string{"starter"}, Cursor: "tenant-a", Limit: 1})
	if err != nil {
		t.Fatalf("ListPage(starter next page) error = %v", err)
	}
	assertSubscriptionTenantIDs(t, page, "tenant-b")

	page, err = store.ListPage(ctx, PageFilter{Cursor: "tenant-z", Limit: 10})
	if err != nil {
		t.Fatalf("ListPage(past end) error = %v", err)
	}
	assertSubscriptionTenantIDs(t, page)

	for name, filter := range map[string]ListFilter{
		"empty tenant filter":  {TenantIDs: []types.TenantID{"tenant-a", ""}},
		"empty plan filter":    {PlanIDs: []string{"starter", ""}},
		"negative limit":       {Limit: -1},
		"offset without limit": {Offset: 1},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := store.List(ctx, filter); !errors.Is(err, ErrInvalidListFilter) {
				t.Fatalf("List(%+v) error = %v, want ErrInvalidListFilter", filter, err)
			}
		})
	}
	if _, err := store.ListPage(ctx, PageFilter{Cursor: "tenant-a", Offset: 1, Limit: 1}); !errors.Is(err, ErrInvalidListFilter) {
		t.Fatalf("ListPage(cursor with offset) error = %v, want ErrInvalidListFilter", err)
	}
}

func TestMemoryStoreMutationFailuresPreserveSubscriptionState(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 7, 17, 13, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	original := Subscription{TenantID: "tenant-a", PlanID: "starter", Status: StatusActive, StartDate: start}
	if err := store.Create(ctx, original); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := store.Create(cancelled, Subscription{TenantID: "tenant-cancelled", PlanID: "starter", Status: StatusActive, StartDate: start}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Create(cancelled context) error = %v, want context.Canceled", err)
	}

	replacement := original
	replacement.PlanID = "enterprise"
	if err := store.Update(cancelled, replacement); !errors.Is(err, context.Canceled) {
		t.Fatalf("Update(cancelled context) error = %v, want context.Canceled", err)
	}
	persisted, err := store.Get(ctx, original.TenantID)
	if err != nil {
		t.Fatalf("Get() after cancelled Update error = %v", err)
	}
	if !subscriptionsEqual(persisted, original) {
		t.Fatalf("cancelled Update changed subscription to %+v, want %+v", persisted, original)
	}

	if err := store.Update(ctx, Subscription{TenantID: "missing", PlanID: "starter", Status: StatusActive, StartDate: start}); !errors.Is(err, ErrSubscriptionNotFound) {
		t.Fatalf("Update(missing) error = %v, want ErrSubscriptionNotFound", err)
	}
	if err := store.Delete(cancelled, original.TenantID); !errors.Is(err, context.Canceled) {
		t.Fatalf("Delete(cancelled context) error = %v, want context.Canceled", err)
	}
	persisted, err = store.Get(ctx, original.TenantID)
	if err != nil {
		t.Fatalf("Get() after cancelled Delete error = %v", err)
	}
	if !subscriptionsEqual(persisted, original) {
		t.Fatalf("cancelled Delete changed subscription to %+v, want %+v", persisted, original)
	}
	if err := store.Delete(ctx, "missing"); !errors.Is(err, ErrSubscriptionNotFound) {
		t.Fatalf("Delete(missing) error = %v, want ErrSubscriptionNotFound", err)
	}
	if err := store.Delete(ctx, ""); !errors.Is(err, ErrInvalidSubscription) {
		t.Fatalf("Delete(empty tenant) error = %v, want ErrInvalidSubscription", err)
	}
	if err := store.Delete(ctx, original.TenantID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := store.Get(ctx, original.TenantID); !errors.Is(err, ErrSubscriptionNotFound) {
		t.Fatalf("Get(deleted) error = %v, want ErrSubscriptionNotFound", err)
	}
}

func assertSubscriptionTenantIDs(t *testing.T, subscriptions []Subscription, want ...types.TenantID) {
	t.Helper()
	if len(subscriptions) != len(want) {
		t.Fatalf("subscription count = %d, want %d; subscriptions = %+v", len(subscriptions), len(want), subscriptions)
	}
	for index, subscription := range subscriptions {
		if subscription.TenantID != want[index] {
			t.Fatalf("subscription[%d].TenantID = %q, want %q; subscriptions = %+v", index, subscription.TenantID, want[index], subscriptions)
		}
	}
}

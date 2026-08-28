package subscription

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/darkinno-tech/saas/core/types"
)

var _ LifecycleService = (*MemoryService)(nil)
var _ Store = (*MemoryService)(nil)
var _ PagedStore = (*MemoryService)(nil)

// MemoryStore is kept as a Store-oriented name for MemoryService.
type MemoryStore = MemoryService

// Option configures MemoryService.
type Option func(*MemoryService)

// WithClock sets the clock used for subscription dates.
func WithClock(clock func() time.Time) Option {
	return func(service *MemoryService) {
		if clock != nil {
			service.now = clock
		}
	}
}

// WithBillingHook sets the billing hook.
func WithBillingHook(hook BillingHook) Option {
	return func(service *MemoryService) {
		service.billing = hook
	}
}

// WithGracePeriod sets the post-period grace window before ExpireDue marks a subscription expired.
func WithGracePeriod(period time.Duration) Option {
	return func(service *MemoryService) {
		if period > 0 {
			service.gracePeriod = period
		}
	}
}

// MemoryService is a thread-safe in-memory subscription service. Billing hooks
// receive a context that can read the staged state, while other readers keep
// seeing the last committed state. Same-tenant writers wait for the hook to
// finish; a hook attempting that write itself receives
// ErrBillingHookReentrantMutation instead of deadlocking. Get observes the
// hook overlay; List and ListPage intentionally return committed state only.
type MemoryService struct {
	mu            sync.RWMutex
	now           func() time.Time
	gracePeriod   time.Duration
	billing       BillingHook
	subscriptions map[types.TenantID]Subscription
	pending       map[types.TenantID]*pendingMutation
}

type pendingMutation struct {
	staged Subscription
	done   chan struct{}
}

type billingHookContextKey struct{}

type billingHookContext struct {
	service  *MemoryService
	tenantID types.TenantID
	pending  *pendingMutation
	parent   *billingHookContext
}

// NewMemoryService creates an empty subscription service.
func NewMemoryService(opts ...Option) *MemoryService {
	service := &MemoryService{
		now:           time.Now,
		subscriptions: map[types.TenantID]Subscription{},
		pending:       map[types.TenantID]*pendingMutation{},
	}
	for _, opt := range opts {
		if opt != nil {
			opt(service)
		}
	}
	return service
}

// NewMemoryStore creates an empty subscription store.
func NewMemoryStore(opts ...Option) *MemoryStore {
	return NewMemoryService(opts...)
}

// Create inserts a subscription.
func (service *MemoryService) Create(ctx context.Context, subscription Subscription) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateSubscription(subscription); err != nil {
		return err
	}
	if err := service.lockMutation(ctx, subscription.TenantID); err != nil {
		return err
	}
	defer service.mu.Unlock()

	if _, ok := service.subscriptions[subscription.TenantID]; ok {
		return ErrSubscriptionAlreadyExists
	}
	service.subscriptions[subscription.TenantID] = cloneSubscription(subscription)
	return nil
}

// Subscribe creates an active subscription for a tenant.
func (service *MemoryService) Subscribe(ctx context.Context, tenantID types.TenantID, planID string) (Subscription, error) {
	return service.subscribe(ctx, tenantID, planID, nil)
}

// SubscribeWithPeriod creates an active subscription with a current billing period end.
func (service *MemoryService) SubscribeWithPeriod(ctx context.Context, tenantID types.TenantID, planID string, currentPeriodEnd time.Time) (Subscription, error) {
	if currentPeriodEnd.IsZero() {
		return Subscription{}, ErrInvalidSubscription
	}
	return service.subscribe(ctx, tenantID, planID, &currentPeriodEnd)
}

func (service *MemoryService) subscribe(ctx context.Context, tenantID types.TenantID, planID string, currentPeriodEnd *time.Time) (Subscription, error) {
	if err := ctx.Err(); err != nil {
		return Subscription{}, err
	}
	if tenantID == "" || planID == "" {
		return Subscription{}, ErrInvalidSubscription
	}
	if err := service.lockMutation(ctx, tenantID); err != nil {
		return Subscription{}, err
	}
	if _, ok := service.subscriptions[tenantID]; ok {
		service.mu.Unlock()
		return Subscription{}, ErrSubscriptionAlreadyExists
	}

	subscription := Subscription{
		TenantID:  tenantID,
		PlanID:    planID,
		Status:    StatusActive,
		StartDate: service.now(),
	}
	if currentPeriodEnd != nil {
		service.setPeriod(&subscription, *currentPeriodEnd)
	}
	pending := service.stageLocked(tenantID, subscription)
	service.mu.Unlock()

	if err := service.runBillingHook(ctx, BillingEvent{TenantID: tenantID, Action: "subscribe", ToPlan: planID, Status: subscription.Status, CurrentPeriodEnd: cloneTimePtr(subscription.CurrentPeriodEnd)}, tenantID, pending); err != nil {
		return Subscription{}, err
	}
	return subscription, nil
}

// Unsubscribe cancels an active subscription.
func (service *MemoryService) Unsubscribe(ctx context.Context, tenantID types.TenantID) (Subscription, error) {
	if err := ctx.Err(); err != nil {
		return Subscription{}, err
	}
	if err := service.lockMutation(ctx, tenantID); err != nil {
		return Subscription{}, err
	}
	current, ok := service.subscriptions[tenantID]
	if !ok {
		service.mu.Unlock()
		return Subscription{}, ErrSubscriptionNotFound
	}
	if current.Status != StatusActive {
		service.mu.Unlock()
		return Subscription{}, ErrInvalidTransition
	}
	now := service.now()
	current.Status = StatusCancelled
	current.EndDate = &now
	pending := service.stageLocked(tenantID, current)
	service.mu.Unlock()

	if err := service.runBillingHook(ctx, BillingEvent{TenantID: tenantID, Action: "unsubscribe", FromPlan: current.PlanID, Status: current.Status}, tenantID, pending); err != nil {
		return Subscription{}, err
	}
	return cloneSubscription(current), nil
}

// Renew reactivates an active or expired subscription with a new current period end.
func (service *MemoryService) Renew(ctx context.Context, tenantID types.TenantID, currentPeriodEnd time.Time) (Subscription, error) {
	if err := ctx.Err(); err != nil {
		return Subscription{}, err
	}
	if tenantID == "" || currentPeriodEnd.IsZero() {
		return Subscription{}, ErrInvalidSubscription
	}
	if err := service.lockMutation(ctx, tenantID); err != nil {
		return Subscription{}, err
	}
	current, ok := service.subscriptions[tenantID]
	if !ok {
		service.mu.Unlock()
		return Subscription{}, ErrSubscriptionNotFound
	}
	if current.Status == StatusCancelled {
		service.mu.Unlock()
		return Subscription{}, ErrInvalidTransition
	}
	current.Status = StatusActive
	current.EndDate = nil
	service.setPeriod(&current, currentPeriodEnd)
	pending := service.stageLocked(tenantID, current)
	service.mu.Unlock()

	if err := service.runBillingHook(ctx, BillingEvent{TenantID: tenantID, Action: "renew", ToPlan: current.PlanID, Status: current.Status, CurrentPeriodEnd: cloneTimePtr(current.CurrentPeriodEnd)}, tenantID, pending); err != nil {
		return Subscription{}, err
	}
	return cloneSubscription(current), nil
}

// Expire marks an active subscription expired immediately.
func (service *MemoryService) Expire(ctx context.Context, tenantID types.TenantID) (Subscription, error) {
	if err := ctx.Err(); err != nil {
		return Subscription{}, err
	}
	if tenantID == "" {
		return Subscription{}, ErrInvalidSubscription
	}
	if err := service.lockMutation(ctx, tenantID); err != nil {
		return Subscription{}, err
	}
	current, ok := service.subscriptions[tenantID]
	if !ok {
		service.mu.Unlock()
		return Subscription{}, ErrSubscriptionNotFound
	}
	if current.Status != StatusActive {
		service.mu.Unlock()
		return Subscription{}, ErrInvalidTransition
	}
	now := service.now()
	current.Status = StatusExpired
	current.EndDate = &now
	pending := service.stageLocked(tenantID, current)
	service.mu.Unlock()

	if err := service.runBillingHook(ctx, BillingEvent{TenantID: tenantID, Action: "expire", FromPlan: current.PlanID, Status: current.Status, CurrentPeriodEnd: cloneTimePtr(current.CurrentPeriodEnd)}, tenantID, pending); err != nil {
		return Subscription{}, err
	}
	return cloneSubscription(current), nil
}

// ExpireDue marks active subscriptions expired after their current period and grace window end.
func (service *MemoryService) ExpireDue(ctx context.Context) ([]Subscription, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	now := service.now()
	service.mu.RLock()
	due := make([]types.TenantID, 0, len(service.subscriptions))
	for tenantID, current := range service.subscriptions {
		if subscriptionDue(current, now) {
			due = append(due, tenantID)
		}
	}
	service.mu.RUnlock()
	sort.Slice(due, func(i, j int) bool { return due[i] < due[j] })

	expired := []Subscription{}
	for _, tenantID := range due {
		if err := service.lockMutation(ctx, tenantID); err != nil {
			return expired, err
		}
		current, ok := service.subscriptions[tenantID]
		if !ok {
			service.mu.Unlock()
			continue
		}
		if !subscriptionDue(current, now) {
			service.mu.Unlock()
			continue
		}

		next := current
		next.Status = StatusExpired
		endDate := expirationDate(next)
		next.EndDate = &endDate
		pending := service.stageLocked(tenantID, next)
		service.mu.Unlock()

		event := BillingEvent{TenantID: tenantID, Action: "expire", FromPlan: next.PlanID, Status: next.Status, CurrentPeriodEnd: cloneTimePtr(next.CurrentPeriodEnd)}
		if err := service.runBillingHook(ctx, event, tenantID, pending); err != nil {
			return expired, err
		}
		expired = append(expired, cloneSubscription(next))
	}
	return expired, nil
}

// Upgrade changes an active subscription to a higher plan.
func (service *MemoryService) Upgrade(ctx context.Context, tenantID types.TenantID, planID string) (Subscription, error) {
	return service.changePlan(ctx, tenantID, planID, "upgrade")
}

// Downgrade changes an active subscription to a lower plan.
func (service *MemoryService) Downgrade(ctx context.Context, tenantID types.TenantID, planID string) (Subscription, error) {
	return service.changePlan(ctx, tenantID, planID, "downgrade")
}

// Get returns a subscription by tenant ID.
func (service *MemoryService) Get(ctx context.Context, tenantID types.TenantID) (Subscription, error) {
	if err := ctx.Err(); err != nil {
		return Subscription{}, err
	}
	if tenantID == "" {
		return Subscription{}, ErrInvalidSubscription
	}

	service.mu.RLock()
	defer service.mu.RUnlock()
	if pending := service.pending[tenantID]; pending != nil {
		if service.hookCanRead(ctx, tenantID, pending) {
			return cloneSubscription(pending.staged), nil
		}
	}

	subscription, ok := service.subscriptions[tenantID]
	if !ok {
		return Subscription{}, ErrSubscriptionNotFound
	}
	return cloneSubscription(subscription), nil
}

// List returns subscriptions matching filter.
func (service *MemoryService) List(ctx context.Context, filter ListFilter) ([]Subscription, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := filter.validate(); err != nil {
		return nil, err
	}
	return service.list(filter, "")
}

// ListPage returns subscriptions after the cursor while preserving List filtering semantics.
func (service *MemoryService) ListPage(ctx context.Context, filter PageFilter) ([]Subscription, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := filter.validate(); err != nil {
		return nil, err
	}
	return service.list(filter.listFilter(), filter.Cursor)
}

func (service *MemoryService) list(filter ListFilter, cursor types.TenantID) ([]Subscription, error) {
	service.mu.RLock()
	defer service.mu.RUnlock()

	subscriptions := make([]Subscription, 0, len(service.subscriptions))
	for _, subscription := range service.subscriptions {
		if filter.matches(subscription) {
			subscriptions = append(subscriptions, cloneSubscription(subscription))
		}
	}
	sort.Slice(subscriptions, func(i, j int) bool {
		return subscriptions[i].TenantID < subscriptions[j].TenantID
	})
	subscriptions = seekSubscriptions(subscriptions, cursor)
	return pageSubscriptions(subscriptions, filter), nil
}

// Update replaces a subscription.
func (service *MemoryService) Update(ctx context.Context, subscription Subscription) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateSubscription(subscription); err != nil {
		return err
	}
	if err := service.lockMutation(ctx, subscription.TenantID); err != nil {
		return err
	}
	defer service.mu.Unlock()

	if _, ok := service.subscriptions[subscription.TenantID]; !ok {
		return ErrSubscriptionNotFound
	}
	service.subscriptions[subscription.TenantID] = cloneSubscription(subscription)
	return nil
}

// Delete removes a subscription by tenant ID.
func (service *MemoryService) Delete(ctx context.Context, tenantID types.TenantID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if tenantID == "" {
		return ErrInvalidSubscription
	}
	if err := service.lockMutation(ctx, tenantID); err != nil {
		return err
	}
	defer service.mu.Unlock()

	if _, ok := service.subscriptions[tenantID]; !ok {
		return ErrSubscriptionNotFound
	}
	delete(service.subscriptions, tenantID)
	return nil
}

func (service *MemoryService) changePlan(ctx context.Context, tenantID types.TenantID, planID string, action string) (Subscription, error) {
	if err := ctx.Err(); err != nil {
		return Subscription{}, err
	}
	if tenantID == "" || planID == "" {
		return Subscription{}, ErrInvalidSubscription
	}
	if err := service.lockMutation(ctx, tenantID); err != nil {
		return Subscription{}, err
	}
	current, ok := service.subscriptions[tenantID]
	if !ok {
		service.mu.Unlock()
		return Subscription{}, ErrSubscriptionNotFound
	}
	if current.Status != StatusActive {
		service.mu.Unlock()
		return Subscription{}, ErrInvalidTransition
	}
	fromPlan := current.PlanID
	current.PlanID = planID
	pending := service.stageLocked(tenantID, current)
	service.mu.Unlock()

	if err := service.runBillingHook(ctx, BillingEvent{TenantID: tenantID, Action: action, FromPlan: fromPlan, ToPlan: planID, Status: current.Status}, tenantID, pending); err != nil {
		return Subscription{}, err
	}
	return cloneSubscription(current), nil
}

func (service *MemoryService) emit(ctx context.Context, event BillingEvent, pending *pendingMutation) error {
	if service.billing == nil {
		return nil
	}
	parent, _ := ctx.Value(billingHookContextKey{}).(*billingHookContext)
	hook := &billingHookContext{service: service, tenantID: event.TenantID, pending: pending, parent: parent}
	hookCtx := context.WithValue(ctx, billingHookContextKey{}, hook)
	return service.billing(hookCtx, event)
}

// runBillingHook commits the staged mutation only after the hook succeeds. Its
// deferred rollback deliberately does not recover panics: it releases waiters
// and discards the overlay, then lets the original panic continue naturally.
func (service *MemoryService) runBillingHook(ctx context.Context, event BillingEvent, tenantID types.TenantID, pending *pendingMutation) (err error) {
	committed := false
	defer func() {
		if !committed {
			service.finishPending(tenantID, pending, false)
		}
	}()
	if err := service.emit(ctx, event, pending); err != nil {
		return err
	}
	service.finishPending(tenantID, pending, true)
	committed = true
	return nil
}

func (service *MemoryService) lockMutation(ctx context.Context, tenantID types.TenantID) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		service.mu.Lock()
		pending := service.pending[tenantID]
		if pending == nil {
			return nil
		}
		if hasBillingHookMarker(ctx) {
			service.mu.Unlock()
			return ErrBillingHookReentrantMutation
		}
		done := pending.done
		service.mu.Unlock()

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
		}
	}
}

func (service *MemoryService) stageLocked(tenantID types.TenantID, staged Subscription) *pendingMutation {
	if service.pending == nil {
		service.pending = map[types.TenantID]*pendingMutation{}
	}
	pending := &pendingMutation{staged: cloneSubscription(staged), done: make(chan struct{})}
	service.pending[tenantID] = pending
	return pending
}

func (service *MemoryService) finishPending(tenantID types.TenantID, pending *pendingMutation, commit bool) {
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.pending[tenantID] != pending {
		return
	}
	if commit {
		service.subscriptions[tenantID] = cloneSubscription(pending.staged)
	}
	delete(service.pending, tenantID)
	close(pending.done)
}

func (service *MemoryService) hookCanRead(ctx context.Context, tenantID types.TenantID, pending *pendingMutation) bool {
	hook, _ := ctx.Value(billingHookContextKey{}).(*billingHookContext)
	for hook != nil {
		if hook.service == service && hook.tenantID == tenantID && hook.pending == pending {
			return true
		}
		hook = hook.parent
	}
	return false
}

func hasBillingHookMarker(ctx context.Context) bool {
	hook, _ := ctx.Value(billingHookContextKey{}).(*billingHookContext)
	return hook != nil
}

func (service *MemoryService) setPeriod(subscription *Subscription, currentPeriodEnd time.Time) {
	end := currentPeriodEnd
	subscription.CurrentPeriodEnd = &end
	if service.gracePeriod > 0 {
		graceEnd := end.Add(service.gracePeriod)
		subscription.GracePeriodEnd = &graceEnd
		return
	}
	subscription.GracePeriodEnd = nil
}

func subscriptionDue(subscription Subscription, now time.Time) bool {
	if subscription.Status != StatusActive || subscription.CurrentPeriodEnd == nil {
		return false
	}
	dueAt := expirationDate(subscription)
	return !now.Before(dueAt)
}

func expirationDate(subscription Subscription) time.Time {
	if subscription.GracePeriodEnd != nil {
		return *subscription.GracePeriodEnd
	}
	return *subscription.CurrentPeriodEnd
}

func cloneSubscription(subscription Subscription) Subscription {
	subscription.CurrentPeriodEnd = cloneTimePtr(subscription.CurrentPeriodEnd)
	subscription.GracePeriodEnd = cloneTimePtr(subscription.GracePeriodEnd)
	subscription.EndDate = cloneTimePtr(subscription.EndDate)
	return subscription
}

func cloneTimePtr(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func validateSubscription(subscription Subscription) error {
	if subscription.TenantID == "" || subscription.PlanID == "" || !validStatus(subscription.Status) || subscription.StartDate.IsZero() {
		return ErrInvalidSubscription
	}
	return nil
}

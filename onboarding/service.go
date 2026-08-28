package onboarding

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"maps"
	"sync"
	"time"

	"github.com/im10furry/saas/biz/audit"
	"github.com/im10furry/saas/biz/notification"
	"github.com/im10furry/saas/core/store"
	"github.com/im10furry/saas/core/types"
	"github.com/im10furry/saas/deployment"
	saasfeature "github.com/im10furry/saas/feature"
	saasplan "github.com/im10furry/saas/plan"
	saasquota "github.com/im10furry/saas/quota"
	saassubscription "github.com/im10furry/saas/subscription"
	saastenant "github.com/im10furry/saas/tenant"
)

// Service coordinates the default tenant onboarding flow.
type Service struct {
	mu            sync.Mutex
	tenants       saastenant.Service
	plans         saasplan.Service
	subscriptions saassubscription.Service
	features      saasfeature.Store
	quotas        saasquota.Store
	audit         audit.Store
	notifier      notification.Notifier
	deployments   DeploymentService
	sentWelcome   map[welcomeDeliveryKey]struct{}
}

type welcomeDeliveryKey struct {
	tenantID  types.TenantID
	messageID string
}

// Option configures onboarding integrations.
type Option func(*Service)

// WithFeatureStore initializes plan defaults and tenant feature overrides.
func WithFeatureStore(store saasfeature.Store) Option {
	return func(service *Service) {
		service.features = store
	}
}

// WithQuotaStore initializes tenant quota usage buckets from the selected plan.
func WithQuotaStore(store saasquota.Store) Option {
	return func(service *Service) {
		service.quotas = store
	}
}

// WithAuditStore records a tenant.onboard event after the flow succeeds.
func WithAuditStore(store audit.Store) Option {
	return func(service *Service) {
		service.audit = store
	}
}

// WithNotifier sends an optional welcome message after the flow succeeds.
func WithNotifier(notifier notification.Notifier) Option {
	return func(service *Service) {
		service.notifier = notifier
	}
}

// DeploymentService binds a newly onboarded tenant to, and resolves, its
// host-managed logical deployment unit.
type DeploymentService interface {
	Assign(context.Context, types.Tenant, types.DeploymentUnitID) (deployment.Assignment, error)
	Resolve(context.Context, types.Tenant) (types.DeploymentUnit, error)
}

// WithDeploymentService requires onboarding requests to select a deployment
// unit before subscription initialization and tenant activation.
func WithDeploymentService(deployments DeploymentService) Option {
	return func(service *Service) {
		service.deployments = deployments
	}
}

// New creates an onboarding service.
func New(tenants saastenant.Service, plans saasplan.Service, subscriptions saassubscription.Service, opts ...Option) *Service {
	service := &Service{
		tenants:       tenants,
		plans:         plans,
		subscriptions: subscriptions,
		sentWelcome:   make(map[welcomeDeliveryKey]struct{}),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(service)
		}
	}
	return service
}

// Input describes a tenant onboarding request.
type Input struct {
	Tenant                saastenant.CreateInput
	DeploymentUnitID      types.DeploymentUnitID
	SubscriptionPeriodEnd *time.Time
	FeatureOverrides      []saasfeature.Flag
	Welcome               *notification.Message
	SkipActivation        bool
	AuditMetadata         map[string]string
}

// Result describes the completed onboarding state.
type Result struct {
	Tenant         types.Tenant
	DeploymentUnit types.DeploymentUnit
	Plan           saasplan.Plan
	Subscription   saassubscription.Subscription
	Features       []saasfeature.Flag
	QuotaLimits    []saasquota.Limit
}

// Onboard creates the tenant, attaches the selected plan subscription, initializes
// feature and quota state, records audit metadata, optionally sends a welcome
// message, and activates the tenant unless SkipActivation is set. Errors after
// tenant creation return the completed portion of Result. Callers that omit a
// tenant ID should reuse Result.Tenant.ID when retrying a partially completed
// flow. Welcome Message.ID is stable and the in-process service suppresses
// repeats; restart and multi-process idempotency require the Notifier to honor
// Message.ID as its delivery idempotency key.
func (service *Service) Onboard(ctx context.Context, input Input) (Result, error) {
	if err := service.validate(input); err != nil {
		return Result{}, err
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	selectedPlan, err := service.plans.Get(ctx, input.Tenant.PlanID)
	if err != nil {
		return Result{}, err
	}

	created, _, err := service.createOrResumeTenant(ctx, input.Tenant)
	result := Result{
		Tenant:      created,
		Plan:        selectedPlan,
		QuotaLimits: quotaLimits(created.ID, selectedPlan.Quotas),
	}
	if err != nil {
		if created.ID == "" {
			return Result{}, err
		}
		return result, err
	}
	if service.deployments != nil {
		unit, err := service.assignOrResolveDeployment(ctx, created, input.DeploymentUnitID)
		result.DeploymentUnit = unit
		if err != nil {
			return result, err
		}
	}

	subscription, subscribed, err := service.existingSubscription(ctx, created.ID, selectedPlan.ID, input.SubscriptionPeriodEnd)
	if err != nil {
		return result, err
	}

	// The active subscription is the durable initialization checkpoint. Until it
	// exists, repeating feature/quota initialization is safe; once it exists,
	// retries must not reset usage even when the tenant intentionally remains
	// pending through SkipActivation or a prior activation failed.
	initialize := !subscribed
	if service.features != nil {
		if initialize {
			if err := service.features.SetPlanDefaults(ctx, selectedPlan.ID, planFeatureFlags(selectedPlan.Features)); err != nil {
				return result, err
			}
			if len(input.FeatureOverrides) > 0 {
				if err := service.features.SetTenantOverrides(ctx, created.ID, input.FeatureOverrides); err != nil {
					return result, err
				}
			}
		}
		flags, err := service.features.List(ctx, created.ID, selectedPlan.ID)
		if err != nil {
			return result, err
		}
		result.Features = flags
	}

	if service.quotas != nil && initialize {
		for _, limit := range result.QuotaLimits {
			if err := service.quotas.Reset(ctx, limit.TenantID, limit.Resource, limit.Period); err != nil {
				return result, err
			}
		}
	}

	if !subscribed {
		subscription, err = service.subscribeOrResume(ctx, created.ID, selectedPlan.ID, input.SubscriptionPeriodEnd)
		if err != nil {
			return result, err
		}
	}
	result.Subscription = subscription

	if !input.SkipActivation && created.Status != types.TenantStatusActive {
		activated, err := service.tenants.Activate(ctx, created.ID)
		if err != nil {
			return result, err
		}
		result.Tenant = activated
	}

	if service.audit != nil {
		event := audit.Event{
			ID:       onboardingOperationID("audit", created.ID),
			TenantID: created.ID,
			Action:   "tenant.onboard",
			Resource: "tenant:" + created.ID.String(),
			Metadata: auditMetadata(selectedPlan.ID, result.Tenant.Status, result.DeploymentUnit.ID, input.AuditMetadata),
		}
		recorded, err := service.hasOnboardingAudit(ctx, event)
		if err != nil {
			return result, err
		}
		if !recorded {
			if err := service.audit.Record(ctx, event); err != nil {
				return result, err
			}
		}
	}

	if input.Welcome != nil {
		if service.notifier == nil {
			return Result{}, ErrInvalidInput
		}
		message := cloneWelcome(created.ID, *input.Welcome)
		if message.ID == "" {
			message.ID = onboardingOperationID("welcome", created.ID)
		}
		if service.sentWelcome == nil {
			service.sentWelcome = make(map[welcomeDeliveryKey]struct{})
		}
		deliveryKey := welcomeDeliveryKey{tenantID: created.ID, messageID: message.ID}
		if _, sent := service.sentWelcome[deliveryKey]; !sent {
			if err := service.notifier.Send(ctx, message); err != nil {
				return result, err
			}
			service.sentWelcome[deliveryKey] = struct{}{}
		}
	}

	return result, nil
}

func (service *Service) createOrResumeTenant(ctx context.Context, input saastenant.CreateInput) (types.Tenant, bool, error) {
	created, err := service.tenants.Create(ctx, input)
	if err == nil {
		return created, true, nil
	}
	if created.ID != "" {
		return created, true, err
	}
	if input.ID == "" || !errors.Is(err, store.ErrTenantAlreadyExists) {
		return types.Tenant{}, false, err
	}

	current, getErr := service.tenants.Get(ctx, input.ID)
	if getErr != nil {
		return types.Tenant{}, false, getErr
	}
	if current.Name != input.Name || current.PlanID != input.PlanID || !maps.Equal(current.Config, input.Config) {
		return types.Tenant{}, false, ErrInvalidInput
	}
	if current.Status != types.TenantStatusPending && current.Status != types.TenantStatusActive {
		return types.Tenant{}, false, ErrInvalidInput
	}
	return current, false, nil
}

func (service *Service) subscribeOrResume(ctx context.Context, tenantID types.TenantID, planID string, periodEnd *time.Time) (saassubscription.Subscription, error) {
	subscription, err := service.subscribe(ctx, tenantID, planID, periodEnd)
	if err == nil {
		return subscription, nil
	}
	if !errors.Is(err, saassubscription.ErrSubscriptionAlreadyExists) {
		return saassubscription.Subscription{}, err
	}

	current, found, getErr := service.existingSubscription(ctx, tenantID, planID, periodEnd)
	if getErr != nil {
		return saassubscription.Subscription{}, getErr
	}
	if !found {
		return saassubscription.Subscription{}, saassubscription.ErrSubscriptionNotFound
	}
	return current, nil
}

func (service *Service) existingSubscription(ctx context.Context, tenantID types.TenantID, planID string, periodEnd *time.Time) (saassubscription.Subscription, bool, error) {
	current, err := service.subscriptions.Get(ctx, tenantID)
	if errors.Is(err, saassubscription.ErrSubscriptionNotFound) {
		return saassubscription.Subscription{}, false, nil
	}
	if err != nil {
		return saassubscription.Subscription{}, false, err
	}
	if current.PlanID != planID || current.Status != saassubscription.StatusActive {
		return saassubscription.Subscription{}, false, ErrInvalidInput
	}
	if (periodEnd == nil) != (current.CurrentPeriodEnd == nil) {
		return saassubscription.Subscription{}, false, ErrInvalidInput
	}
	if periodEnd != nil && !current.CurrentPeriodEnd.Equal(*periodEnd) {
		return saassubscription.Subscription{}, false, ErrInvalidInput
	}
	return current, true, nil
}

func (service *Service) assignOrResolveDeployment(ctx context.Context, tenant types.Tenant, unitID types.DeploymentUnitID) (types.DeploymentUnit, error) {
	_, err := service.deployments.Assign(ctx, tenant, unitID)
	if err != nil && !errors.Is(err, deployment.ErrAssignmentAlreadyExists) {
		unit, resolveErr := service.deployments.Resolve(ctx, tenant)
		if resolveErr == nil && unit.ID == unitID {
			return unit, err
		}
		return types.DeploymentUnit{}, err
	}

	unit, err := service.deployments.Resolve(ctx, tenant)
	if err != nil {
		return types.DeploymentUnit{}, err
	}
	if unit.ID != unitID {
		return types.DeploymentUnit{}, ErrInvalidInput
	}
	return unit, nil
}

func (service *Service) hasOnboardingAudit(ctx context.Context, target audit.Event) (bool, error) {
	events, err := service.audit.List(ctx, target.TenantID)
	if err != nil {
		return false, err
	}
	for _, event := range events {
		if event.ID == target.ID || (event.Action == target.Action && event.Resource == target.Resource) {
			return true, nil
		}
	}
	return false, nil
}

func onboardingOperationID(kind string, tenantID types.TenantID) string {
	sum := sha256.Sum256([]byte(kind + "\x00" + tenantID.String()))
	return "onboard_" + kind + "_" + hex.EncodeToString(sum[:16])
}

func (service *Service) validate(input Input) error {
	if service == nil || service.tenants == nil || service.plans == nil || service.subscriptions == nil {
		return ErrInvalidInput
	}
	if input.Tenant.PlanID == "" {
		return ErrInvalidInput
	}
	if service.deployments != nil && input.DeploymentUnitID == "" {
		return ErrInvalidInput
	}
	if service.deployments == nil && input.DeploymentUnitID != "" {
		return ErrInvalidInput
	}
	if input.SubscriptionPeriodEnd != nil && input.SubscriptionPeriodEnd.IsZero() {
		return ErrInvalidInput
	}
	if input.SubscriptionPeriodEnd != nil {
		if _, ok := service.subscriptions.(saassubscription.LifecycleService); !ok {
			return ErrInvalidInput
		}
	}
	if input.Welcome != nil && service.notifier == nil {
		return ErrInvalidInput
	}
	return nil
}

func (service *Service) subscribe(ctx context.Context, tenantID types.TenantID, planID string, periodEnd *time.Time) (saassubscription.Subscription, error) {
	if periodEnd == nil {
		return service.subscriptions.Subscribe(ctx, tenantID, planID)
	}
	lifecycle, ok := service.subscriptions.(saassubscription.LifecycleService)
	if !ok {
		return saassubscription.Subscription{}, ErrInvalidInput
	}
	return lifecycle.SubscribeWithPeriod(ctx, tenantID, planID, *periodEnd)
}

func planFeatureFlags(features []saasplan.Feature) []saasfeature.Flag {
	flags := make([]saasfeature.Flag, len(features))
	for i, feature := range features {
		flags[i] = saasfeature.Flag{
			Key:     feature.Key,
			Enabled: feature.Enabled,
			Config:  cloneStringMap(feature.Config),
		}
	}
	return flags
}

func quotaLimits(tenantID types.TenantID, quotas []saasplan.Quota) []saasquota.Limit {
	limits := make([]saasquota.Limit, len(quotas))
	for i, quota := range quotas {
		limits[i] = saasquota.Limit{
			TenantID: tenantID,
			Resource: quota.Resource,
			Limit:    quota.Limit,
			Period:   quotaPeriod(quota.Period),
		}
	}
	return limits
}

func quotaPeriod(period saasplan.QuotaPeriod) saasquota.Period {
	switch period {
	case saasplan.QuotaPeriodNone:
		return saasquota.PeriodNone
	case saasplan.QuotaPeriodDay:
		return saasquota.PeriodDay
	case saasplan.QuotaPeriodMonth:
		return saasquota.PeriodMonth
	default:
		return saasquota.Period(period)
	}
}

func auditMetadata(planID string, status types.TenantStatus, deploymentUnitID types.DeploymentUnitID, extra map[string]string) map[string]string {
	metadata := cloneStringMap(extra)
	if metadata == nil {
		metadata = map[string]string{}
	}
	metadata["plan_id"] = planID
	metadata["tenant_status"] = string(status)
	if deploymentUnitID != "" {
		metadata["deployment_unit_id"] = deploymentUnitID.String()
	}
	return metadata
}

func cloneWelcome(tenantID types.TenantID, message notification.Message) notification.Message {
	message.TenantID = tenantID
	message.Metadata = cloneStringMap(message.Metadata)
	return message
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

package commission

import (
	"strings"
	"time"

	"github.com/im10furry/saas/core/types"
)

// CommissionEvent is a tenant-scoped source event eligible for a commission
// calculation. SourceType and SourceID form the host application's business
// identity for the source event.
type CommissionEvent struct {
	TenantID       types.TenantID
	SourceType     string
	SourceID       string
	OccurredAt     time.Time
	Commissionable Amount
	Attributes     map[string]string
}

// Clone returns a deep copy of the event's mutable attributes.
func (event CommissionEvent) Clone() CommissionEvent {
	return cloneCommissionEvent(event)
}

func cloneCommissionEvent(event CommissionEvent) CommissionEvent {
	event.Attributes = cloneStringMap(event.Attributes)
	return event
}

func validateCommissionEvent(event CommissionEvent) error {
	if event.TenantID == "" || strings.TrimSpace(event.SourceType) == "" || strings.TrimSpace(event.SourceID) == "" || event.OccurredAt.IsZero() || !validAmount(event.Commissionable) {
		return ErrInvalidCommissionEvent
	}
	return nil
}

// BeneficiaryKind identifies the kind of account receiving a commission.
type BeneficiaryKind string

const (
	BeneficiaryKindTenant   BeneficiaryKind = "tenant"
	BeneficiaryKindUser     BeneficiaryKind = "user"
	BeneficiaryKindExternal BeneficiaryKind = "external"
)

// BeneficiaryRef identifies a commission recipient.
type BeneficiaryRef struct {
	Kind BeneficiaryKind
	ID   string
}

func validBeneficiary(beneficiary BeneficiaryRef) bool {
	if strings.TrimSpace(beneficiary.ID) == "" {
		return false
	}
	switch beneficiary.Kind {
	case BeneficiaryKindTenant, BeneficiaryKindUser, BeneficiaryKindExternal:
		return true
	default:
		return false
	}
}

// FormulaKind describes the supported formula components that tiers can use.
type FormulaKind string

const (
	FormulaKindPercentageBPS FormulaKind = "percentage_bps"
	FormulaKindFixedMinor    FormulaKind = "fixed_minor"
)

// Tier applies a percentage of its intersecting minor-unit range and, when the
// event amount lies within the tier, one fixed minor-unit amount. A MaxMinor of
// zero makes the range unbounded.
type Tier struct {
	MinMinor    int64
	MaxMinor    int64
	BasisPoints int64
	FixedMinor  int64
}

// Rule assigns tier-derived commission to a named beneficiary slot.
type Rule struct {
	Slot        string
	Beneficiary BeneficiaryRef
	Tiers       []Tier
}

// Template is a versioned commission formula. A rule may omit Beneficiary
// while it acts only as a platform cap; Calculate requires beneficiaries on
// effective rules.
type Template struct {
	ID                string
	Version           int64
	Status            TemplateStatus
	CreatedAt         time.Time
	UpdatedAt         time.Time
	AllowedEventTypes []string
	MaxCommission     Amount
	FreezePeriod      time.Duration
	Rules             []Rule
}

func cloneTemplate(template Template) Template {
	template.AllowedEventTypes = append([]string(nil), template.AllowedEventTypes...)
	rules := template.Rules
	template.Rules = make([]Rule, len(rules))
	for i, rule := range rules {
		template.Rules[i] = cloneRule(rule)
	}
	return template
}

func cloneRule(rule Rule) Rule {
	rule.Tiers = append([]Tier(nil), rule.Tiers...)
	return rule
}

func validateTemplate(template Template) error {
	if strings.TrimSpace(template.ID) == "" || template.Version <= 0 || !validTemplateStatus(template.Status) || template.CreatedAt.IsZero() || template.UpdatedAt.IsZero() || template.UpdatedAt.Before(template.CreatedAt) || template.FreezePeriod < 0 || !optionalAmountValid(template.MaxCommission) {
		return ErrInvalidTemplate
	}
	for _, eventType := range template.AllowedEventTypes {
		if strings.TrimSpace(eventType) == "" {
			return ErrInvalidTemplate
		}
	}
	slots := make(map[string]struct{}, len(template.Rules))
	for _, rule := range template.Rules {
		if err := validateRule(rule, false); err != nil {
			return err
		}
		if _, exists := slots[rule.Slot]; exists {
			return ErrInvalidTemplate
		}
		slots[rule.Slot] = struct{}{}
	}
	return nil
}

// validateNewTemplate applies the creation-only lifecycle constraint without
// rejecting active or retired template versions that are already persisted and
// need to be read back by a Store.
func validateNewTemplate(template Template) error {
	if err := validateTemplate(template); err != nil {
		return err
	}
	if template.Status != TemplateStatusDraft {
		return ErrInvalidTemplate
	}
	return nil
}

func validateRule(rule Rule, requireBeneficiary bool) error {
	if strings.TrimSpace(rule.Slot) == "" || len(rule.Tiers) == 0 {
		return ErrInvalidRule
	}
	if requireBeneficiary || rule.Beneficiary.Kind != "" || rule.Beneficiary.ID != "" {
		if !validBeneficiary(rule.Beneficiary) {
			return ErrInvalidBeneficiary
		}
	}
	for _, tier := range rule.Tiers {
		if err := validateTier(tier); err != nil {
			return err
		}
	}
	return nil
}

func validateTier(tier Tier) error {
	if tier.MinMinor < 0 || tier.MaxMinor < 0 || tier.MaxMinor != 0 && tier.MaxMinor <= tier.MinMinor || tier.BasisPoints < 0 || tier.BasisPoints > basisPointDenominator || tier.FixedMinor < 0 || tier.BasisPoints == 0 && tier.FixedMinor == 0 {
		return ErrInvalidTier
	}
	return nil
}

// Calculation is the commission derived for one beneficiary rule.
type Calculation struct {
	Slot        string
	Beneficiary BeneficiaryRef
	Amount      Amount
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

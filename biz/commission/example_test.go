package commission_test

import (
	"context"
	"fmt"
	"time"

	"github.com/im10furry/saas/biz/commission"
	"github.com/im10furry/saas/core/types"
)

func ExampleService_RecordEvent() {
	ctx := context.Background()
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	service := commission.NewService(
		commission.NewMemoryStore(),
		commission.WithClock(func() time.Time { return now }),
		commission.WithAuthorizer(exampleAuthorizer{}),
	)
	host := commission.Actor{ID: "host-job", Host: true}

	template := commission.Template{
		ID:                "partner-template",
		Version:           1,
		Status:            commission.TemplateStatusDraft,
		AllowedEventTypes: []string{"payment.succeeded"},
		MaxCommission:     commission.Amount{Currency: "USD", Minor: 1_000},
		Rules: []commission.Rule{{
			Slot: "referral",
			Tiers: []commission.Tier{{
				MinMinor:    0,
				BasisPoints: 1_000, // platform maximum: 10%
			}},
		}},
	}
	must(service.CreateTemplate(ctx, host, template))
	mustTransitionTemplate(service, ctx, host, template.ID, template.Version)

	program := commission.Program{
		ID:              "tenant-program",
		TenantID:        "tenant-a",
		TemplateID:      template.ID,
		TemplateVersion: template.Version,
		Status:          commission.ProgramStatusDraft,
		Rules: []commission.Rule{{
			Slot:        "referral",
			Beneficiary: commission.BeneficiaryRef{Kind: commission.BeneficiaryKindExternal, ID: "partner-42"},
			Tiers:       []commission.Tier{{MinMinor: 0, BasisPoints: 500}}, // tenant choice: 5%
		}},
	}
	must(service.CreateProgram(ctx, host, program))
	pending, err := service.TransitionProgram(ctx, host, program.TenantID, program.ID, 1, commission.ProgramActionSubmit)
	must(err)
	_, err = service.TransitionProgram(ctx, host, program.TenantID, program.ID, pending.Version, commission.ProgramActionApprove)
	must(err)

	earnings, err := service.RecordEvent(ctx, host, program.ID, commission.CommissionEvent{
		TenantID:       program.TenantID,
		SourceType:     "payment.succeeded",
		SourceID:       "payment-1001",
		OccurredAt:     now,
		Commissionable: commission.Amount{Currency: "USD", Minor: 10_000},
	})
	must(err)
	fmt.Printf("%s %d %s\n", earnings[0].Status, earnings[0].Amount.Minor, earnings[0].Amount.Currency)

	// Output:
	// available 500 USD
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func mustTransitionTemplate(service *commission.Service, ctx context.Context, actor commission.Actor, id string, version int64) {
	_, err := service.TransitionTemplate(ctx, actor, id, version, commission.TemplateActionActivate)
	must(err)
}

// exampleAuthorizer stands in for the application's trusted identity and
// permission adapter. Production code should verify actor identity here.
type exampleAuthorizer struct{}

func (exampleAuthorizer) Authorize(context.Context, commission.Actor, commission.Permission, types.TenantID) error {
	return nil
}

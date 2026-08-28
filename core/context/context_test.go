package tenantctx

import (
	"context"
	"testing"

	"github.com/darkinno-tech/saas/core/types"
)

func TestWithTenantAndFromContext(t *testing.T) {
	tenant := types.Tenant{
		ID:     "tenant-a",
		Name:   "Tenant A",
		Status: types.TenantStatusActive,
		PlanID: "starter",
		Config: map[string]string{"region": "us"},
	}

	ctx := WithTenant(context.Background(), tenant)

	tenant.Config["region"] = "eu"

	got, ok := FromContext(ctx)
	if !ok {
		t.Fatal("FromContext() ok = false, want true")
	}
	if got.ID != tenant.ID || got.Name != tenant.Name || got.Status != tenant.Status || got.PlanID != tenant.PlanID {
		t.Fatalf("FromContext() = %+v, want tenant metadata", got)
	}
	if got.Config["region"] != "us" {
		t.Fatalf("stored tenant config was mutated, got region %q", got.Config["region"])
	}

	got.Config["region"] = "ap"
	again, ok := FromContext(ctx)
	if !ok {
		t.Fatal("FromContext() after mutation ok = false, want true")
	}
	if again.Config["region"] != "us" {
		t.Fatalf("returned tenant config mutated stored state, got region %q", again.Config["region"])
	}
}

func TestWithTenantDeploymentAndDeploymentFromContext(t *testing.T) {
	tenant := types.Tenant{
		ID:     "tenant-a",
		Config: map[string]string{"tier": "standard"},
	}
	deployment := types.DeploymentUnit{
		ID:            "eu-central-1",
		Status:        types.DeploymentUnitStatusActive,
		Region:        "eu-central-1",
		ResidencyTags: []string{"gdpr", "eu"},
		Metadata:      map[string]string{"provider": "example"},
	}

	ctx := WithTenantDeployment(context.Background(), tenant, deployment)
	tenant.Config["tier"] = "enterprise"
	deployment.ResidencyTags[0] = "changed"
	deployment.Metadata["provider"] = "changed"

	gotTenant, ok := FromContext(ctx)
	if !ok {
		t.Fatal("FromContext() ok = false, want true")
	}
	if gotTenant.Config["tier"] != "standard" {
		t.Fatalf("stored tenant config was mutated, got tier %q", gotTenant.Config["tier"])
	}

	got, ok := DeploymentFromContext(ctx)
	if !ok {
		t.Fatal("DeploymentFromContext() ok = false, want true")
	}
	if got.ID != "eu-central-1" || got.Status != types.DeploymentUnitStatusActive || got.Region != "eu-central-1" {
		t.Fatalf("DeploymentFromContext() = %+v, want deployment metadata", got)
	}
	if got.ResidencyTags[0] != "gdpr" {
		t.Fatalf("stored deployment residency tags were mutated, got %q", got.ResidencyTags[0])
	}
	if got.Metadata["provider"] != "example" {
		t.Fatalf("stored deployment metadata was mutated, got %q", got.Metadata["provider"])
	}

	got.ResidencyTags[0] = "returned-change"
	got.Metadata["provider"] = "returned-change"
	again, ok := DeploymentFromContext(ctx)
	if !ok {
		t.Fatal("DeploymentFromContext() after mutation ok = false, want true")
	}
	if again.ResidencyTags[0] != "gdpr" || again.Metadata["provider"] != "example" {
		t.Fatalf("returned deployment mutated stored state, got %+v", again)
	}
}

func TestHostContext(t *testing.T) {
	tenant := types.Tenant{ID: "tenant-a"}

	hostCtx := WithHost(context.Background())
	if !IsHost(hostCtx) {
		t.Fatal("IsHost() = false, want true")
	}
	if _, ok := FromContext(hostCtx); ok {
		t.Fatal("FromContext() ok = true for host context, want false")
	}

	tenantCtx := WithTenant(hostCtx, tenant)
	if IsHost(tenantCtx) {
		t.Fatal("IsHost() = true after WithTenant(), want false")
	}
	if got, ok := FromContext(tenantCtx); !ok || got.ID != tenant.ID {
		t.Fatalf("FromContext() = %+v, %v; want tenant", got, ok)
	}

	hostAgain := WithHost(tenantCtx)
	if !IsHost(hostAgain) {
		t.Fatal("IsHost() = false after WithHost(), want true")
	}
	if _, ok := FromContext(hostAgain); ok {
		t.Fatal("FromContext() ok = true after WithHost(), want false")
	}
}

func TestContextTransitionsClearDeployment(t *testing.T) {
	withDeployment := WithTenantDeployment(context.Background(), types.Tenant{ID: "tenant-a"}, types.DeploymentUnit{
		ID:     "eu-central-1",
		Status: types.DeploymentUnitStatusActive,
	})

	tests := []struct {
		name string
		ctx  context.Context
	}{
		{name: "with tenant", ctx: WithTenant(withDeployment, types.Tenant{ID: "tenant-b"})},
		{name: "with host", ctx: WithHost(withDeployment)},
		{name: "detach", ctx: Detach(withDeployment)},
		{name: "switch", ctx: Switch(withDeployment, types.Tenant{ID: "tenant-b"})},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if deployment, ok := DeploymentFromContext(tt.ctx); ok {
				t.Fatalf("DeploymentFromContext() = %+v, true; want zero deployment, false", deployment)
			}
		})
	}

	if deployment, ok := DeploymentFromContext(withDeployment); !ok || deployment.ID != "eu-central-1" {
		t.Fatalf("DeploymentFromContext(original) = %+v, %v; want original deployment", deployment, ok)
	}
}

func TestDetachRemovesTenantAndCancellation(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	ctx := WithTenantDeployment(parent, types.Tenant{ID: "tenant-a"}, types.DeploymentUnit{
		ID:     "eu-central-1",
		Status: types.DeploymentUnitStatusActive,
	})
	cancel()

	detached := Detach(ctx)
	if _, ok := FromContext(detached); ok {
		t.Fatal("FromContext() ok = true after Detach(), want false")
	}
	if IsHost(detached) {
		t.Fatal("IsHost() = true after Detach(), want false")
	}
	if _, ok := DeploymentFromContext(detached); ok {
		t.Fatal("DeploymentFromContext() ok = true after Detach(), want false")
	}
	if err := detached.Err(); err != nil {
		t.Fatalf("detached.Err() = %v, want nil", err)
	}
}

func TestSwitchReturnsNewTenantContext(t *testing.T) {
	original := WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})
	switched := Switch(original, types.Tenant{ID: "tenant-b"})

	oldTenant, ok := FromContext(original)
	if !ok {
		t.Fatal("FromContext(original) ok = false, want true")
	}
	if oldTenant.ID != "tenant-a" {
		t.Fatalf("original tenant = %q, want tenant-a", oldTenant.ID)
	}

	newTenant, ok := FromContext(switched)
	if !ok {
		t.Fatal("FromContext(switched) ok = false, want true")
	}
	if newTenant.ID != "tenant-b" {
		t.Fatalf("switched tenant = %q, want tenant-b", newTenant.ID)
	}
}

func TestFromContextWithoutTenant(t *testing.T) {
	if tenant, ok := FromContext(context.Background()); ok {
		t.Fatalf("FromContext() = %+v, true; want zero tenant, false", tenant)
	}
	if IsHost(context.Background()) {
		t.Fatal("IsHost() = true for background context, want false")
	}
	if deployment, ok := DeploymentFromContext(context.Background()); ok {
		t.Fatalf("DeploymentFromContext() = %+v, true; want zero deployment, false", deployment)
	}
}

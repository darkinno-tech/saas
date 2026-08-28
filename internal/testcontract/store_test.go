package testcontract

import (
	"testing"

	"github.com/darkinno-tech/saas/core/store"
	"github.com/darkinno-tech/saas/core/types"
)

func TestRunStoreContractWithMemoryStore(t *testing.T) {
	RunStoreContract(t, func() store.Store {
		return store.NewMemoryStore()
	})
}

func TestContractTenantProvidesIndependentValidMetadata(t *testing.T) {
	tenant := ContractTenant("tenant-a")
	if tenant.ID != "tenant-a" || tenant.Status != types.TenantStatusActive || tenant.PlanID != "starter" || tenant.Config["feature"] != "on" {
		t.Fatalf("ContractTenant() = %+v, want valid active starter tenant", tenant)
	}

	tenant.Config["feature"] = "off"
	again := ContractTenant("tenant-a")
	if again.Config["feature"] != "on" {
		t.Fatalf("ContractTenant() reused mutable config map: %+v", again.Config)
	}
}

package store_test

import (
	"testing"

	"github.com/darkinno-tech/saas/core/store"
	"github.com/darkinno-tech/saas/internal/testcontract"
)

func TestMemoryStoreContract(t *testing.T) {
	testcontract.RunStoreContract(t, func() store.Store {
		return store.NewMemoryStore()
	})
}

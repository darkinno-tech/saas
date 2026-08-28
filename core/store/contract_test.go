package store_test

import (
	"testing"

	"github.com/im10furry/saas/core/store"
	"github.com/im10furry/saas/internal/testcontract"
)

func TestMemoryStoreContract(t *testing.T) {
	testcontract.RunStoreContract(t, func() store.Store {
		return store.NewMemoryStore()
	})
}

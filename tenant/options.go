package tenant

import (
	"context"
	"crypto/rand"
	"encoding/hex"

	"github.com/im10furry/saas/core/types"
)

// IDGenerator creates tenant identifiers when CreateInput omits ID.
type IDGenerator func(context.Context) (types.TenantID, error)

// Seeder initializes tenant-scoped data after a tenant is created.
type Seeder func(context.Context, types.Tenant) error

// Auditor records lifecycle events.
type Auditor func(context.Context, Event) error

// Event describes a tenant lifecycle event.
type Event struct {
	TenantID types.TenantID
	Action   string
	From     types.TenantStatus
	To       types.TenantStatus
}

// Option configures Manager.
type Option func(*Manager)

// WithIDGenerator sets the ID generator.
func WithIDGenerator(generator IDGenerator) Option {
	return func(manager *Manager) {
		if generator != nil {
			manager.generateID = generator
		}
	}
}

// WithSeeder sets the tenant seeder.
func WithSeeder(seeder Seeder) Option {
	return func(manager *Manager) {
		manager.seed = seeder
	}
}

// WithAuditor sets the lifecycle auditor.
func WithAuditor(auditor Auditor) Option {
	return func(manager *Manager) {
		manager.audit = auditor
	}
}

func defaultIDGenerator(context.Context) (types.TenantID, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return types.TenantID(hex.EncodeToString(bytes[:])), nil
}

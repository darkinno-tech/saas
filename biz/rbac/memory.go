package rbac

import (
	"context"
	"sync"

	"github.com/im10furry/saas/core/types"
)

var _ Service = (*MemoryService)(nil)
var _ Enforcer = (*MemoryService)(nil)

// MemoryEnforcer is kept as an Enforcer-oriented name for MemoryService.
type MemoryEnforcer = MemoryService

type MemoryService struct {
	mu    sync.RWMutex
	roles map[roleKey]Role
}

type roleKey struct {
	tenantID types.TenantID
	key      string
}

func NewMemoryService() *MemoryService {
	return &MemoryService{roles: map[roleKey]Role{}}
}

func NewMemoryEnforcer() *MemoryEnforcer {
	return NewMemoryService()
}

func (service *MemoryService) CreateRole(ctx context.Context, role Role) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateRole(role); err != nil {
		return ErrInvalidRole
	}

	service.mu.Lock()
	defer service.mu.Unlock()

	key := roleKey{tenantID: role.TenantID, key: role.Key}
	if _, ok := service.roles[key]; ok {
		return ErrRoleExists
	}
	service.roles[key] = cloneRole(role)
	return nil
}

func (service *MemoryService) GetRole(ctx context.Context, tenantID types.TenantID, key string) (Role, error) {
	if err := ctx.Err(); err != nil {
		return Role{}, err
	}
	if tenantID == "" || key == "" {
		return Role{}, ErrInvalidRole
	}

	service.mu.RLock()
	defer service.mu.RUnlock()

	role, ok := service.roles[roleKey{tenantID: tenantID, key: key}]
	if !ok {
		return Role{}, ErrRoleNotFound
	}
	return cloneRole(role), nil
}

func (service *MemoryService) DeleteRole(ctx context.Context, tenantID types.TenantID, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if tenantID == "" || key == "" {
		return ErrInvalidRole
	}

	service.mu.Lock()
	defer service.mu.Unlock()

	roleKey := roleKey{tenantID: tenantID, key: key}
	if _, ok := service.roles[roleKey]; !ok {
		return ErrRoleNotFound
	}
	delete(service.roles, roleKey)
	return nil
}

func (service *MemoryService) Authorize(ctx context.Context, tenantID types.TenantID, roles []string, permission Permission) error {
	return service.Enforce(ctx, tenantID, roles, permission)
}

func (service *MemoryService) Enforce(ctx context.Context, tenantID types.TenantID, roles []string, permission Permission) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if tenantID == "" || permission == "" {
		return ErrInvalidRole
	}

	service.mu.RLock()
	defer service.mu.RUnlock()

	for _, roleKeyValue := range roles {
		role, ok := service.roles[roleKey{tenantID: tenantID, key: roleKeyValue}]
		if !ok {
			continue
		}
		if role.HasPermission(permission) {
			return nil
		}
	}
	return ErrPermissionDeny
}

func cloneRole(role Role) Role {
	permissions := make([]Permission, len(role.Permissions))
	copy(permissions, role.Permissions)
	role.Permissions = permissions
	return role
}

func validateRole(role Role) error {
	if role.TenantID == "" || role.Key == "" {
		return ErrInvalidRole
	}
	for _, permission := range role.Permissions {
		if permission == "" {
			return ErrInvalidRole
		}
	}
	return nil
}

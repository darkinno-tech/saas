package user

import (
	"context"
	"sort"
	"sync"

	"github.com/im10furry/saas/core/types"
)

var _ Service = (*MemoryService)(nil)
var _ PagedService = (*MemoryService)(nil)

type MemoryService struct {
	mu      sync.RWMutex
	users   map[string]User
	members map[memberKey]Member
}

type memberKey struct {
	tenantID types.TenantID
	userID   string
}

func NewMemoryService() *MemoryService {
	return &MemoryService{
		users:   map[string]User{},
		members: map[memberKey]Member{},
	}
}

func (service *MemoryService) CreateUser(ctx context.Context, user User) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if user.ID == "" || user.Email == "" {
		return ErrInvalidUser
	}

	service.mu.Lock()
	defer service.mu.Unlock()

	if _, ok := service.users[user.ID]; ok {
		return ErrUserExists
	}
	service.users[user.ID] = user
	return nil
}

func (service *MemoryService) GetUser(ctx context.Context, id string) (User, error) {
	if err := ctx.Err(); err != nil {
		return User{}, err
	}
	if id == "" {
		return User{}, ErrInvalidUser
	}

	service.mu.RLock()
	defer service.mu.RUnlock()

	user, ok := service.users[id]
	if !ok {
		return User{}, ErrUserNotFound
	}
	return user, nil
}

func (service *MemoryService) AddMember(ctx context.Context, member Member) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if member.TenantID == "" || member.UserID == "" {
		return ErrInvalidUser
	}

	service.mu.Lock()
	defer service.mu.Unlock()

	if _, ok := service.users[member.UserID]; !ok {
		return ErrUserNotFound
	}
	key := memberKey{tenantID: member.TenantID, userID: member.UserID}
	if _, ok := service.members[key]; ok {
		return ErrMemberExists
	}
	service.members[key] = cloneMember(member)
	return nil
}

func (service *MemoryService) GetMember(ctx context.Context, tenantID types.TenantID, userID string) (Member, error) {
	if err := ctx.Err(); err != nil {
		return Member{}, err
	}
	if tenantID == "" || userID == "" {
		return Member{}, ErrInvalidUser
	}

	service.mu.RLock()
	defer service.mu.RUnlock()

	member, ok := service.members[memberKey{tenantID: tenantID, userID: userID}]
	if !ok {
		return Member{}, ErrMemberNotFound
	}
	return cloneMember(member), nil
}

func (service *MemoryService) ListMembers(ctx context.Context, tenantID types.TenantID) ([]Member, error) {
	return service.ListMembersPage(ctx, tenantID, MemberListFilter{})
}

func (service *MemoryService) ListMembersPage(ctx context.Context, tenantID types.TenantID, filter MemberListFilter) ([]Member, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if tenantID == "" {
		return nil, ErrInvalidUser
	}
	if err := filter.validate(); err != nil {
		return nil, err
	}

	service.mu.RLock()
	defer service.mu.RUnlock()

	members := []Member{}
	for key, member := range service.members {
		if key.tenantID == tenantID {
			members = append(members, cloneMember(member))
		}
	}
	sort.Slice(members, func(i, j int) bool {
		return members[i].UserID < members[j].UserID
	})
	return pageMembers(members, filter), nil
}

func (service *MemoryService) RemoveMember(ctx context.Context, tenantID types.TenantID, userID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if tenantID == "" || userID == "" {
		return ErrInvalidUser
	}

	service.mu.Lock()
	defer service.mu.Unlock()

	key := memberKey{tenantID: tenantID, userID: userID}
	if _, ok := service.members[key]; !ok {
		return ErrMemberNotFound
	}
	delete(service.members, key)
	return nil
}

func cloneMember(member Member) Member {
	roles := make([]string, len(member.Roles))
	copy(roles, member.Roles)
	member.Roles = roles
	return member
}

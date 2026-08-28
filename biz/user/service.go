package user

import (
	"context"

	"github.com/darkinno-tech/saas/core/types"
)

type Service interface {
	CreateUser(ctx context.Context, user User) error
	GetUser(ctx context.Context, id string) (User, error)
	AddMember(ctx context.Context, member Member) error
	GetMember(ctx context.Context, tenantID types.TenantID, userID string) (Member, error)
	ListMembers(ctx context.Context, tenantID types.TenantID) ([]Member, error)
	RemoveMember(ctx context.Context, tenantID types.TenantID, userID string) error
}

// PagedService extends Service with cursor-based tenant membership listing.
type PagedService interface {
	Service
	ListMembersPage(ctx context.Context, tenantID types.TenantID, filter MemberListFilter) ([]Member, error)
}

// MemberListFilter restricts paged tenant membership list queries.
type MemberListFilter struct {
	// Cursor returns rows ordered after the user ID cursor.
	Cursor string
	Limit  int
}

func (filter MemberListFilter) validate() error {
	if filter.Limit < 0 {
		return ErrInvalidListFilter
	}
	return nil
}

func pageMembers(members []Member, filter MemberListFilter) []Member {
	if filter.Cursor != "" {
		start := len(members)
		for i, member := range members {
			if member.UserID > filter.Cursor {
				start = i
				break
			}
		}
		members = members[start:]
	}
	if filter.Limit > 0 && len(members) > filter.Limit {
		return members[:filter.Limit]
	}
	return members
}

package identity

import (
	"context"
	"errors"
	"testing"

	"github.com/im10furry/saas/biz/user"
)

func TestServiceAuthenticateCreatesUserMemberAndLink(t *testing.T) {
	ctx := context.Background()
	users := user.NewMemoryService()
	links := NewMemoryStore()
	service := NewService(users, WithStore(links), WithProviders(GoogleOIDC()), WithDefaultRoles("member"))

	assertion := Assertion{
		TenantID:      "tenant-a",
		Provider:      ProviderGoogle,
		Subject:       "google-subject",
		Email:         "user@example.com",
		Name:          "User Example",
		EmailVerified: true,
		Metadata:      map[string]string{"hd": "example.com"},
	}
	session, err := service.Authenticate(ctx, assertion)
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if session.UserID != DefaultUserID(ProviderGoogle, "google-subject") || session.Roles[0] != "member" {
		t.Fatalf("Authenticate() session = %+v, want generated user and default role", session)
	}

	gotUser, err := users.GetUser(ctx, session.UserID)
	if err != nil {
		t.Fatalf("GetUser() error = %v", err)
	}
	if gotUser.Email != "user@example.com" || gotUser.Name != "User Example" {
		t.Fatalf("GetUser() = %+v, want assertion profile", gotUser)
	}

	member, err := users.GetMember(ctx, "tenant-a", session.UserID)
	if err != nil {
		t.Fatalf("GetMember() error = %v", err)
	}
	if len(member.Roles) != 1 || member.Roles[0] != "member" {
		t.Fatalf("GetMember() roles = %#v, want default role", member.Roles)
	}

	link, err := links.GetByExternal(ctx, "tenant-a", ProviderGoogle, "google-subject")
	if err != nil {
		t.Fatalf("GetByExternal() error = %v", err)
	}
	if link.UserID != session.UserID || link.Metadata["hd"] != "example.com" {
		t.Fatalf("GetByExternal() = %+v, want linked identity", link)
	}

	if _, err := service.Authenticate(ctx, assertion); err != nil {
		t.Fatalf("Authenticate(idempotent) error = %v", err)
	}
}

func TestServiceAuthenticateRequiresAllowedProviderAndVerifiedEmail(t *testing.T) {
	ctx := context.Background()

	withoutProviders := NewService(user.NewMemoryService())
	_, err := withoutProviders.Authenticate(ctx, Assertion{
		TenantID:      "tenant-a",
		Provider:      ProviderGoogle,
		Subject:       "sub",
		Email:         "user@example.com",
		EmailVerified: true,
	})
	if !errors.Is(err, ErrProviderNotAllowed) {
		t.Fatalf("Authenticate(provider not allowed) error = %v, want ErrProviderNotAllowed", err)
	}

	service := NewService(user.NewMemoryService(), WithProviders(GoogleOIDC()))
	_, err = service.Authenticate(ctx, Assertion{
		TenantID: "tenant-a",
		Provider: ProviderGoogle,
		Subject:  "sub",
		Email:    "user@example.com",
	})
	if !errors.Is(err, ErrUnverifiedEmail) {
		t.Fatalf("Authenticate(unverified email) error = %v, want ErrUnverifiedEmail", err)
	}
}

func TestServiceAuthenticateDetectsIdentityConflict(t *testing.T) {
	ctx := context.Background()
	service := NewService(user.NewMemoryService(), WithProviders(GitHubOAuth()), WithEmailVerificationRequired(false))

	first := Assertion{
		TenantID: "tenant-a",
		Provider: ProviderGitHub,
		Subject:  "github-subject",
		UserID:   "u1",
		Email:    "user@example.com",
	}
	if _, err := service.Authenticate(ctx, first); err != nil {
		t.Fatalf("Authenticate(first) error = %v", err)
	}

	second := first
	second.UserID = "u2"
	if _, err := service.Authenticate(ctx, second); !errors.Is(err, ErrIdentityConflict) {
		t.Fatalf("Authenticate(conflict) error = %v, want ErrIdentityConflict", err)
	}
}

func TestServiceAuthenticateRejectsExistingUserEmailMismatch(t *testing.T) {
	ctx := context.Background()
	users := user.NewMemoryService()
	if err := users.CreateUser(ctx, user.User{ID: "u1", Email: "owner@example.com"}); err != nil {
		t.Fatalf("CreateUser() error = %v", err)
	}

	service := NewService(users, WithProviders(GoogleOIDC()))
	_, err := service.Authenticate(ctx, Assertion{
		TenantID:      "tenant-a",
		Provider:      ProviderGoogle,
		Subject:       "google-subject",
		UserID:        "u1",
		Email:         "attacker@example.com",
		EmailVerified: true,
	})
	if !errors.Is(err, ErrIdentityConflict) {
		t.Fatalf("Authenticate(email mismatch) error = %v, want ErrIdentityConflict", err)
	}
}

func TestServiceAuthenticateUsesStoredRolesForExistingMember(t *testing.T) {
	ctx := context.Background()
	users := user.NewMemoryService()
	if err := users.CreateUser(ctx, user.User{ID: "u1", Email: "user@example.com"}); err != nil {
		t.Fatalf("CreateUser() error = %v", err)
	}
	if err := users.AddMember(ctx, user.Member{TenantID: "tenant-a", UserID: "u1", Roles: []string{"owner"}}); err != nil {
		t.Fatalf("AddMember() error = %v", err)
	}

	service := NewService(users, WithProviders(GoogleOIDC()))
	session, err := service.Authenticate(ctx, Assertion{
		TenantID:      "tenant-a",
		Provider:      ProviderGoogle,
		Subject:       "google-subject",
		UserID:        "u1",
		Email:         "user@example.com",
		EmailVerified: true,
		Roles:         []string{"member"},
	})
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if len(session.Roles) != 1 || session.Roles[0] != "owner" {
		t.Fatalf("Authenticate() roles = %#v, want stored owner role", session.Roles)
	}
}

func TestServiceAuthenticateKeepsSharedIdentityRolesTenantScoped(t *testing.T) {
	ctx := context.Background()
	users := user.NewMemoryService()
	links := NewMemoryStore()
	service := NewService(users, WithStore(links), WithProviders(GoogleOIDC()))

	rolesA := []string{"owner"}
	metadataA := map[string]string{"tenant": "a"}
	sessionA, err := service.Authenticate(ctx, Assertion{
		TenantID:      "tenant-a",
		Provider:      ProviderGoogle,
		Subject:       "shared-subject",
		Email:         "user@example.com",
		EmailVerified: true,
		Roles:         rolesA,
		Metadata:      metadataA,
	})
	if err != nil {
		t.Fatalf("Authenticate(tenant A) error = %v", err)
	}
	rolesA[0] = "mutated"
	metadataA["tenant"] = "mutated"
	linkA, err := links.GetByExternal(ctx, "tenant-a", ProviderGoogle, "shared-subject")
	if err != nil {
		t.Fatalf("GetByExternal(tenant A after source mutation) error = %v", err)
	}
	if linkA.Metadata["tenant"] != "a" {
		t.Fatalf("tenant-A metadata = %#v, want preserved input clone", linkA.Metadata)
	}

	sessionB, err := service.Authenticate(ctx, Assertion{
		TenantID:      "tenant-b",
		Provider:      ProviderGoogle,
		Subject:       "shared-subject",
		Email:         "user@example.com",
		EmailVerified: true,
		Roles:         []string{"viewer"},
		Metadata:      map[string]string{"tenant": "b"},
	})
	if err != nil {
		t.Fatalf("Authenticate(tenant B) error = %v", err)
	}
	if sessionA.UserID != sessionB.UserID {
		t.Fatalf("shared subject user IDs = %q and %q, want one global user", sessionA.UserID, sessionB.UserID)
	}
	if len(sessionA.Roles) != 1 || sessionA.Roles[0] != "owner" || len(sessionB.Roles) != 1 || sessionB.Roles[0] != "viewer" {
		t.Fatalf("initial sessions = %+v / %+v, want tenant-scoped owner/viewer roles", sessionA, sessionB)
	}

	for _, assertion := range []Assertion{
		{TenantID: "tenant-a", Provider: ProviderGoogle, Subject: "shared-subject", Email: "user@example.com", EmailVerified: true, Roles: []string{"administrator"}},
		{TenantID: "tenant-b", Provider: ProviderGoogle, Subject: "shared-subject", Email: "user@example.com", EmailVerified: true, Roles: []string{"owner"}},
	} {
		session, err := service.Authenticate(ctx, assertion)
		if err != nil {
			t.Fatalf("Authenticate(existing %s) error = %v", assertion.TenantID, err)
		}
		if assertion.TenantID == "tenant-a" && (len(session.Roles) != 1 || session.Roles[0] != "owner") {
			t.Fatalf("tenant-A reauthentication roles = %#v, want stored owner", session.Roles)
		}
		if assertion.TenantID == "tenant-b" && (len(session.Roles) != 1 || session.Roles[0] != "viewer") {
			t.Fatalf("tenant-B reauthentication roles = %#v, want stored viewer", session.Roles)
		}
	}

	memberA, err := users.GetMember(ctx, "tenant-a", sessionA.UserID)
	if err != nil {
		t.Fatalf("GetMember(tenant A) error = %v", err)
	}
	memberB, err := users.GetMember(ctx, "tenant-b", sessionA.UserID)
	if err != nil {
		t.Fatalf("GetMember(tenant B) error = %v", err)
	}
	if len(memberA.Roles) != 1 || memberA.Roles[0] != "owner" || len(memberB.Roles) != 1 || memberB.Roles[0] != "viewer" {
		t.Fatalf("stored members = %+v / %+v, want tenant-scoped roles", memberA, memberB)
	}
}

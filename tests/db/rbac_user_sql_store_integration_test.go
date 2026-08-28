package db_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/darkinno-tech/saas/biz/rbac"
	"github.com/darkinno-tech/saas/biz/user"
	"github.com/darkinno-tech/saas/core/types"
)

func TestRBACAndUserSQLStoreMySQLIntegration(t *testing.T) {
	runRBACAndUserSQLStoreIntegration(t, "mysql", os.Getenv("SAAS_MYSQL_DSN"), resetMySQLRBACAndUserTables, func(db *sql.DB) (rbac.Service, user.PagedService, error) {
		rbacStore, err := rbac.NewSQLStore(db)
		if err != nil {
			return nil, nil, err
		}
		userStore, err := user.NewSQLStore(db)
		return rbacStore, userStore, err
	})
}

func TestRBACAndUserSQLStorePostgresIntegration(t *testing.T) {
	runRBACAndUserSQLStoreIntegration(t, "postgres", os.Getenv("SAAS_POSTGRES_DSN"), resetPostgresRBACAndUserTables, func(db *sql.DB) (rbac.Service, user.PagedService, error) {
		rbacStore, err := rbac.NewSQLStore(db, rbac.WithSQLDialect(rbac.SQLDialectPostgres))
		if err != nil {
			return nil, nil, err
		}
		userStore, err := user.NewSQLStore(db, user.WithSQLDialect(user.SQLDialectPostgres))
		return rbacStore, userStore, err
	})
}

func runRBACAndUserSQLStoreIntegration(t *testing.T, driver, dsn string, reset func(*testing.T, context.Context, *sql.DB), newStores func(*sql.DB) (rbac.Service, user.PagedService, error)) {
	t.Helper()
	if dsn == "" {
		t.Skipf("set the %s DSN to run RBAC and user SQL integration tests", driver)
	}

	db, err := sql.Open(driver, dsn)
	if err != nil {
		t.Fatalf("sql.Open(%s) error = %v", driver, err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("db.Close() error = %v", err)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := pingUntilReady(ctx, db); err != nil {
		t.Fatalf("%s not ready: %v", driver, err)
	}
	reset(t, ctx, db)

	rbacStore, userStore, err := newStores(db)
	if err != nil {
		t.Fatalf("NewSQLStore() error = %v", err)
	}
	runRBACAndUserSQLStoreContract(t, ctx, rbacStore, userStore)
}

func runRBACAndUserSQLStoreContract(t *testing.T, ctx context.Context, rbacStore rbac.Service, userStore user.PagedService) {
	t.Helper()
	tenantA := types.TenantID("tenant-a")
	tenantB := types.TenantID("tenant-b")

	if err := rbacStore.CreateRole(ctx, rbac.Role{
		TenantID:    tenantA,
		Key:         "admin",
		Permissions: []rbac.Permission{"orders.read"},
	}); err != nil {
		t.Fatalf("CreateRole(tenant A admin) error = %v", err)
	}
	if err := rbacStore.CreateRole(ctx, rbac.Role{
		TenantID:    tenantB,
		Key:         "admin",
		Permissions: []rbac.Permission{"orders.write"},
	}); err != nil {
		t.Fatalf("CreateRole(tenant B admin) error = %v", err)
	}
	if err := rbacStore.Authorize(ctx, tenantA, []string{"admin"}, "orders.read"); err != nil {
		t.Fatalf("Authorize(tenant A read) error = %v", err)
	}
	if err := rbacStore.Authorize(ctx, tenantA, []string{"admin"}, "orders.write"); !errors.Is(err, rbac.ErrPermissionDeny) {
		t.Fatalf("Authorize(tenant A write) error = %v, want ErrPermissionDeny", err)
	}
	if err := rbacStore.Authorize(ctx, tenantB, []string{"admin"}, "orders.write"); err != nil {
		t.Fatalf("Authorize(tenant B write) error = %v", err)
	}
	if err := rbacStore.Authorize(ctx, tenantB, []string{"admin"}, "orders.read"); !errors.Is(err, rbac.ErrPermissionDeny) {
		t.Fatalf("Authorize(tenant B read) error = %v, want ErrPermissionDeny", err)
	}
	role, err := rbacStore.GetRole(ctx, tenantB, "admin")
	if err != nil {
		t.Fatalf("GetRole(tenant B admin) error = %v", err)
	}
	if role.TenantID != tenantB || role.Key != "admin" || !reflect.DeepEqual(role.Permissions, []rbac.Permission{"orders.write"}) {
		t.Fatalf("GetRole(tenant B admin) = %+v, want tenant B write-only role", role)
	}
	if err := rbacStore.DeleteRole(ctx, tenantA, "admin"); err != nil {
		t.Fatalf("DeleteRole(tenant A admin) error = %v", err)
	}
	if _, err := rbacStore.GetRole(ctx, tenantA, "admin"); !errors.Is(err, rbac.ErrRoleNotFound) {
		t.Fatalf("GetRole(deleted tenant A admin) error = %v, want ErrRoleNotFound", err)
	}
	if err := rbacStore.Authorize(ctx, tenantA, []string{"admin"}, "orders.read"); !errors.Is(err, rbac.ErrPermissionDeny) {
		t.Fatalf("Authorize(deleted tenant A role) error = %v, want ErrPermissionDeny", err)
	}
	if err := rbacStore.Authorize(ctx, tenantB, []string{"admin"}, "orders.write"); err != nil {
		t.Fatalf("Authorize(tenant B write after tenant A delete) error = %v", err)
	}

	for _, account := range []user.User{
		{ID: "a-only", Email: "a-only@example.com", Name: "A Only"},
		{ID: "shared", Email: "shared@example.com", Name: "Shared User"},
		{ID: "b-only", Email: "b-only@example.com", Name: "B Only"},
	} {
		if err := userStore.CreateUser(ctx, account); err != nil {
			t.Fatalf("CreateUser(%s) error = %v", account.ID, err)
		}
	}
	for _, member := range []user.Member{
		{TenantID: tenantA, UserID: "a-only", Roles: []string{"viewer"}},
		{TenantID: tenantA, UserID: "shared", Roles: []string{"admin"}},
		{TenantID: tenantB, UserID: "b-only", Roles: []string{"viewer"}},
		{TenantID: tenantB, UserID: "shared", Roles: []string{"owner"}},
	} {
		if err := userStore.AddMember(ctx, member); err != nil {
			t.Fatalf("AddMember(%s, %s) error = %v", member.TenantID, member.UserID, err)
		}
	}
	member, err := userStore.GetMember(ctx, tenantA, "shared")
	if err != nil {
		t.Fatalf("GetMember(tenant A shared) error = %v", err)
	}
	if member.TenantID != tenantA || member.UserID != "shared" || !reflect.DeepEqual(member.Roles, []string{"admin"}) {
		t.Fatalf("GetMember(tenant A shared) = %+v, want tenant A admin membership", member)
	}
	member, err = userStore.GetMember(ctx, tenantB, "shared")
	if err != nil {
		t.Fatalf("GetMember(tenant B shared) error = %v", err)
	}
	if member.TenantID != tenantB || member.UserID != "shared" || !reflect.DeepEqual(member.Roles, []string{"owner"}) {
		t.Fatalf("GetMember(tenant B shared) = %+v, want tenant B owner membership", member)
	}
	members, err := userStore.ListMembersPage(ctx, tenantA, user.MemberListFilter{Cursor: "a-only", Limit: 1})
	if err != nil {
		t.Fatalf("ListMembersPage(tenant A cursor) error = %v", err)
	}
	if len(members) != 1 || members[0].TenantID != tenantA || members[0].UserID != "shared" || !reflect.DeepEqual(members[0].Roles, []string{"admin"}) {
		t.Fatalf("ListMembersPage(tenant A cursor) = %+v, want shared tenant A admin membership", members)
	}
	if err := userStore.RemoveMember(ctx, tenantA, "shared"); err != nil {
		t.Fatalf("RemoveMember(tenant A) error = %v", err)
	}
	if _, err := userStore.GetMember(ctx, tenantA, "shared"); !errors.Is(err, user.ErrMemberNotFound) {
		t.Fatalf("GetMember(removed tenant A) error = %v, want ErrMemberNotFound", err)
	}
	member, err = userStore.GetMember(ctx, tenantB, "shared")
	if err != nil {
		t.Fatalf("GetMember(tenant B shared) error = %v", err)
	}
	if member.TenantID != tenantB || member.UserID != "shared" || !reflect.DeepEqual(member.Roles, []string{"owner"}) {
		t.Fatalf("GetMember(tenant B shared) = %+v, want tenant B owner membership", member)
	}

	members, err = userStore.ListMembers(ctx, tenantA)
	if err != nil {
		t.Fatalf("ListMembers(tenant A) error = %v", err)
	}
	if len(members) != 1 || members[0].TenantID != tenantA || members[0].UserID != "a-only" || !reflect.DeepEqual(members[0].Roles, []string{"viewer"}) {
		t.Fatalf("ListMembers(tenant A) = %+v, want only tenant A viewer membership", members)
	}
	members, err = userStore.ListMembers(ctx, tenantB)
	if err != nil {
		t.Fatalf("ListMembers(tenant B) error = %v", err)
	}
	if len(members) != 2 || members[0].TenantID != tenantB || members[0].UserID != "b-only" || !reflect.DeepEqual(members[0].Roles, []string{"viewer"}) || members[1].TenantID != tenantB || members[1].UserID != "shared" || !reflect.DeepEqual(members[1].Roles, []string{"owner"}) {
		t.Fatalf("ListMembers(tenant B) = %+v, want tenant B memberships only", members)
	}
}

func resetMySQLRBACAndUserTables(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	resetRBACAndUserTables(t, ctx, db, []string{
		"DROP TABLE IF EXISTS biz_members",
		"DROP TABLE IF EXISTS biz_users",
		"DROP TABLE IF EXISTS rbac_roles",
		`CREATE TABLE rbac_roles (
			tenant_id VARCHAR(191) NOT NULL,
			role_key VARCHAR(191) NOT NULL,
			permissions JSON NOT NULL,
			PRIMARY KEY (tenant_id, role_key)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE biz_users (
			id VARCHAR(191) NOT NULL PRIMARY KEY,
			email VARCHAR(255) NOT NULL,
			name VARCHAR(255) NOT NULL
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE biz_members (
			tenant_id VARCHAR(191) NOT NULL,
			user_id VARCHAR(191) NOT NULL,
			roles JSON NOT NULL,
			PRIMARY KEY (tenant_id, user_id)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	})
}

func resetPostgresRBACAndUserTables(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	resetRBACAndUserTables(t, ctx, db, []string{
		"DROP TABLE IF EXISTS biz_members",
		"DROP TABLE IF EXISTS biz_users",
		"DROP TABLE IF EXISTS rbac_roles",
		`CREATE TABLE rbac_roles (
			tenant_id VARCHAR(191) NOT NULL,
			role_key VARCHAR(191) NOT NULL,
			permissions JSONB NOT NULL,
			PRIMARY KEY (tenant_id, role_key)
		)`,
		`CREATE TABLE biz_users (
			id VARCHAR(191) PRIMARY KEY,
			email VARCHAR(255) NOT NULL,
			name VARCHAR(255) NOT NULL
		)`,
		`CREATE TABLE biz_members (
			tenant_id VARCHAR(191) NOT NULL,
			user_id VARCHAR(191) NOT NULL,
			roles JSONB NOT NULL,
			PRIMARY KEY (tenant_id, user_id)
		)`,
	})
}

func resetRBACAndUserTables(t *testing.T, ctx context.Context, db *sql.DB, statements []string) {
	t.Helper()
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("exec %q error = %v", statement, err)
		}
	}
}

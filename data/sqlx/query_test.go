package sqlxtenant

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	tenantctx "github.com/darkinno-tech/saas/core/context"
	"github.com/darkinno-tech/saas/core/types"
	"github.com/darkinno-tech/saas/data"
)

func TestQueryAddsTenantFilter(t *testing.T) {
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})
	query, args, err := Query(ctx, "SELECT * FROM orders")
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if query != "SELECT * FROM orders WHERE tenant_id = ?" {
		t.Fatalf("Query() sql = %q", query)
	}
	if len(args) != 1 || args[0] != "tenant-a" {
		t.Fatalf("Query() args = %#v, want tenant-a", args)
	}

	query, args, err = Query(ctx, "SELECT * FROM orders WHERE status = ?", data.WithTenantField("orders.tenant_id"))
	if err != nil {
		t.Fatalf("Query(where) error = %v", err)
	}
	if query != "SELECT * FROM orders WHERE (status = ?) AND orders.tenant_id = ?" {
		t.Fatalf("Query(where) sql = %q", query)
	}
	if len(args) != 1 || args[0] != "tenant-a" {
		t.Fatalf("Query(where) args = %#v, want tenant-a", args)
	}
}

func TestTenantConditionForComplexSQL(t *testing.T) {
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})

	condition, err := TenantCondition(ctx, data.WithTenantField("orders.tenant_id"))
	if err != nil {
		t.Fatalf("TenantCondition() error = %v", err)
	}
	if condition.Expression != "orders.tenant_id = ?" {
		t.Fatalf("TenantCondition().Expression = %q, want orders.tenant_id = ?", condition.Expression)
	}
	if len(condition.Args) != 1 || condition.Args[0] != "tenant-a" {
		t.Fatalf("TenantCondition().Args = %#v, want tenant-a", condition.Args)
	}

	hostCondition, err := TenantCondition(tenantctx.WithHost(context.Background()))
	if err != nil {
		t.Fatalf("TenantCondition(host) error = %v", err)
	}
	if !hostCondition.Empty() {
		t.Fatalf("TenantCondition(host) = %+v, want empty", hostCondition)
	}
}

func TestQueryHostContext(t *testing.T) {
	query, args, err := QueryWithArgs(tenantctx.WithHost(context.Background()), "SELECT * FROM orders WHERE status = ?", []any{"open"})
	if err != nil {
		t.Fatalf("Query(host) error = %v", err)
	}
	if query != "SELECT * FROM orders WHERE status = ?" || len(args) != 1 || args[0] != "open" {
		t.Fatalf("Query(host) = %q, %#v; want original sql and args", query, args)
	}
}

func TestQueryValidation(t *testing.T) {
	if _, _, err := Query(context.Background(), "SELECT * FROM orders"); !errors.Is(err, data.ErrNoTenant) {
		t.Fatalf("Query(no tenant) error = %v, want ErrNoTenant", err)
	}
	if _, _, err := Query(tenantctx.WithHost(context.Background()), " "); !errors.Is(err, ErrUnsafeSQL) {
		t.Fatalf("Query(empty sql) error = %v, want ErrUnsafeSQL", err)
	}
}

func TestQueryRejectsUnsafeTenantRewriteSQL(t *testing.T) {
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})
	tests := []string{
		"INSERT INTO orders (name) VALUES (?)",
		"SELECT * FROM orders ORDER BY created_at DESC",
		"SELECT * FROM orders LIMIT 10",
		"SELECT * FROM orders JOIN order_items ON order_items.order_id = orders.id",
		"SELECT * FROM orders o, secrets s",
		"SELECT (SELECT MAX(secret) FROM secrets) FROM orders",
		"SELECT * FROM orders WHERE EXISTS (SELECT 1 FROM secrets)",
		"UPDATE orders SET name = secrets.name FROM secrets WHERE orders.id = secrets.id",
		"UPDATE orders SET name = ? WHERE id = ? RETURNING id",
		"UPDATE orders SET name = ? OUTPUT inserted.id WHERE id = ?",
		"SELECT * FROM orders WHERE id = ? LOCK IN SHARE MODE",
		"SELECT * FROM orders WHERE id = ? OPTION (RECOMPILE)",
		"SELECT * FROM orders; DELETE FROM orders",
		"SELECT * FROM orders -- WHERE tenant_id = ?",
		`SELECT "safe\"; DELETE FROM secrets --" FROM orders`,
	}

	for _, sql := range tests {
		if _, _, err := Query(ctx, sql); !errors.Is(err, ErrUnsafeSQL) {
			t.Fatalf("Query(%q) error = %v, want ErrUnsafeSQL", sql, err)
		}
	}
}

func TestQueryWrapsExistingDeletePredicate(t *testing.T) {
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})

	query, args, err := QueryWithArgs(ctx, "DELETE FROM orders WHERE id = ? OR status = ?", []any{1, "stale"})
	if err != nil {
		t.Fatalf("QueryWithArgs() error = %v", err)
	}
	if query != "DELETE FROM orders WHERE (id = ? OR status = ?) AND tenant_id = ?" {
		t.Fatalf("QueryWithArgs() sql = %q", query)
	}
	if len(args) != 3 || args[0] != 1 || args[1] != "stale" || args[2] != "tenant-a" {
		t.Fatalf("QueryWithArgs() args = %#v", args)
	}
}

func TestQueryScannerIgnoresQuotedContent(t *testing.T) {
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})
	baseSQL := `SELECT "SELECT,OR", ` + "`JOIN`" + ` FROM "orders,archive" WHERE note = 'SELECT, OR; -- /* JOIN'`

	query, _, err := Query(ctx, baseSQL)
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	want := `SELECT "SELECT,OR", ` + "`JOIN`" + ` FROM "orders,archive" WHERE (note = 'SELECT, OR; -- /* JOIN') AND tenant_id = ?`
	if query != want {
		t.Fatalf("Query() sql = %q, want %q", query, want)
	}

	dollarQuoted := "SELECT $tag$SELECT; -- OR, JOIN$tag$ AS note FROM orders WHERE name = 'it''s OR, SELECT'"
	query, _, err = Query(ctx, dollarQuoted)
	if err != nil {
		t.Fatalf("Query(tagged dollar quote) error = %v", err)
	}
	want = "SELECT $tag$SELECT; -- OR, JOIN$tag$ AS note FROM orders WHERE (name = 'it''s OR, SELECT') AND tenant_id = ?"
	if query != want {
		t.Fatalf("Query(tagged dollar quote) sql = %q, want %q", query, want)
	}
}

func TestQueryAllowsCommasInsideExpressions(t *testing.T) {
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})
	baseSQL := "UPDATE orders SET name = CONCAT(?, ?), status = ? WHERE id IN (?, ?)"

	query, _, err := Query(ctx, baseSQL)
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if query != "UPDATE orders SET name = CONCAT(?, ?), status = ? WHERE (id IN (?, ?)) AND tenant_id = ?" {
		t.Fatalf("Query() sql = %q", query)
	}
}

func TestQueryRejectsTenantFieldUpdate(t *testing.T) {
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})

	if _, _, err := Query(ctx, "UPDATE orders SET tenant_id = ? WHERE id = ?"); !errors.Is(err, ErrTenantFieldUpdate) {
		t.Fatalf("Query(tenant field update) error = %v, want ErrTenantFieldUpdate", err)
	}
	for _, baseSQL := range []string{
		"UPDATE orders SET account_id = ? WHERE id = ?",
		"UPDATE orders SET orders.account_id = ? WHERE id = ?",
		`UPDATE orders SET "account_id" = ? WHERE id = ?`,
	} {
		if _, _, err := Query(ctx, baseSQL, data.WithTenantField("orders.account_id")); !errors.Is(err, ErrTenantFieldUpdate) {
			t.Fatalf("Query(%q, qualified tenant field) error = %v, want ErrTenantFieldUpdate", baseSQL, err)
		}
	}

	host := tenantctx.WithHost(context.Background())
	query, args, err := QueryWithArgs(host, "UPDATE orders SET tenant_id = ? WHERE id = ?", []any{"tenant-b", 1})
	if err != nil {
		t.Fatalf("QueryWithArgs(host tenant field update) error = %v", err)
	}
	if query != "UPDATE orders SET tenant_id = ? WHERE id = ?" || len(args) != 2 {
		t.Fatalf("QueryWithArgs(host tenant field update) = %q, %#v", query, args)
	}
}

func TestExecWrappers(t *testing.T) {
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})
	db := &fakeDB{}

	if err := SelectContext(ctx, db, &[]string{}, "SELECT * FROM orders WHERE status = ?", []any{"open"}); err != nil {
		t.Fatalf("SelectContext() error = %v", err)
	}
	if db.query != "SELECT * FROM orders WHERE (status = ?) AND tenant_id = ?" || db.args[0] != "open" || db.args[1] != "tenant-a" {
		t.Fatalf("SelectContext captured %q %#v", db.query, db.args)
	}

	if _, err := ExecContext(ctx, db, "UPDATE orders SET name = ? WHERE id = ?", []any{"updated", 1}); err != nil {
		t.Fatalf("ExecContext() error = %v", err)
	}
	if db.query != "UPDATE orders SET name = ? WHERE (id = ?) AND tenant_id = ?" || db.args[0] != "updated" || db.args[1] != 1 || db.args[2] != "tenant-a" {
		t.Fatalf("ExecContext captured %q %#v", db.query, db.args)
	}
}

type fakeDB struct {
	query string
	args  []interface{}
}

func (db *fakeDB) SelectContext(_ context.Context, _ interface{}, query string, args ...interface{}) error {
	db.query = query
	db.args = args
	return nil
}

func (db *fakeDB) GetContext(_ context.Context, _ interface{}, query string, args ...interface{}) error {
	db.query = query
	db.args = args
	return nil
}

func (db *fakeDB) ExecContext(_ context.Context, query string, args ...interface{}) (sql.Result, error) {
	db.query = query
	db.args = args
	return fakeResult(1), nil
}

type fakeResult int64

func (result fakeResult) LastInsertId() (int64, error) {
	return 0, nil
}

func (result fakeResult) RowsAffected() (int64, error) {
	return int64(result), nil
}

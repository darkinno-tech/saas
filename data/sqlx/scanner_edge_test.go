package sqlxtenant

import (
	"context"
	"errors"
	"testing"

	tenantctx "github.com/darkinno-tech/saas/core/context"
	"github.com/darkinno-tech/saas/core/types"
	"github.com/darkinno-tech/saas/data"
)

func TestQuerySupportsQuotedRelationsWithoutLosingTenantIsolation(t *testing.T) {
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})
	tests := []struct {
		name string
		sql  string
		want string
	}{
		{
			name: "bracket quoted select",
			sql:  "SELECT * FROM [orders] WHERE [status] = ?",
			want: "SELECT * FROM [orders] WHERE ([status] = ?) AND tenant_id = ?",
		},
		{
			name: "backtick qualified update",
			sql:  "UPDATE `billing`.`orders` SET `status` = ? WHERE `id` = ?",
			want: "UPDATE `billing`.`orders` SET `status` = ? WHERE (`id` = ?) AND tenant_id = ?",
		},
		{
			name: "quoted alias delete",
			sql:  "DELETE FROM \"orders\" AS \"o\" WHERE \"o\".\"id\" = ?",
			want: "DELETE FROM \"orders\" AS \"o\" WHERE (\"o\".\"id\" = ?) AND tenant_id = ?",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			query, args, err := QueryWithArgs(ctx, tt.sql, []any{"open"})
			if err != nil {
				t.Fatalf("QueryWithArgs() error = %v", err)
			}
			if query != tt.want {
				t.Fatalf("QueryWithArgs() sql = %q, want %q", query, tt.want)
			}
			if len(args) != 2 || args[0] != "open" || args[1] != "tenant-a" {
				t.Fatalf("QueryWithArgs() args = %#v, want original argument followed by tenant", args)
			}
		})
	}
}

func TestQueryRejectsTenantFieldUpdatesForEverySupportedIdentifierStyle(t *testing.T) {
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})
	for _, sql := range []string{
		"UPDATE orders SET [tenant_id] = ? WHERE id = ?",
		"UPDATE orders SET `tenant_id` = ? WHERE id = ?",
		"UPDATE orders SET \"tenant_id\" = ? WHERE id = ?",
		"UPDATE orders SET orders.[tenant_id] = ? WHERE id = ?",
	} {
		if _, _, err := Query(ctx, sql); !errors.Is(err, ErrTenantFieldUpdate) {
			t.Fatalf("Query(%q) error = %v, want ErrTenantFieldUpdate", sql, err)
		}
	}
}

func TestQueryRejectsMalformedOrAmbiguousTenantRewriteSQL(t *testing.T) {
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})
	for _, sql := range []string{
		"SELECT id FROM",
		"SELECT * FROM orders WHERE",
		"SELECT * FROM orders FROM archived_orders",
		"UPDATE orders SET",
		"UPDATE orders SET status =",
		"UPDATE orders SET status = ?, WHERE id = ?",
		"UPDATE orders SET orders. = ? WHERE id = ?",
		"DELETE FROM WHERE id = ?",
		"DELETE FROM orders WHERE",
		"SELECT * FROM [orders WHERE id = ?",
		"SELECT * FROM \"orders WHERE id = ?",
		"SELECT * FROM `orders WHERE id = ?",
		"SELECT $tag$unterminated FROM orders",
		"SELECT 'backslash\\' quote' FROM orders",
		"SELECT * FROM orders WHERE (id = ?",
		"SELECT * FROM orders WHERE id = ?) ",
	} {
		if _, _, err := Query(ctx, sql); !errors.Is(err, ErrUnsafeSQL) {
			t.Fatalf("Query(%q) error = %v, want ErrUnsafeSQL", sql, err)
		}
	}
}

func TestGetContextRewritesQueryBeforeDelegating(t *testing.T) {
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})
	db := &fakeDB{}
	dest := new(string)

	if err := GetContext(ctx, db, dest, "SELECT name FROM orders WHERE id = ?", []any{42}, data.WithTenantField("orders.tenant_id")); err != nil {
		t.Fatalf("GetContext() error = %v", err)
	}
	if db.query != "SELECT name FROM orders WHERE (id = ?) AND orders.tenant_id = ?" {
		t.Fatalf("GetContext() query = %q", db.query)
	}
	if len(db.args) != 2 || db.args[0] != 42 || db.args[1] != "tenant-a" {
		t.Fatalf("GetContext() args = %#v", db.args)
	}
}

func TestSQLScannerIdentifierAndDollarQuoteRules(t *testing.T) {
	for _, tt := range []struct {
		identifier string
		want       string
	}{
		{identifier: "[tenant]]id]", want: "tenant]id"},
		{identifier: "`tenant``id`", want: "tenant`id"},
		{identifier: "\"tenant\"\"id\"", want: "tenant\"id"},
	} {
		tokens, err := scanSQL(tt.identifier)
		if err != nil || len(tokens) != 1 {
			t.Fatalf("scanSQL(%q) = %#v, %v", tt.identifier, tokens, err)
		}
		if got := identifierValue(tokens[0]); got != tt.want {
			t.Fatalf("identifierValue(%q) = %q, want %q", tt.identifier, got, tt.want)
		}
	}

	for _, tt := range []struct {
		input string
		want  string
		ok    bool
	}{
		{input: "$$body$$", want: "$$", ok: true},
		{input: "$tenant_1$body$tenant_1$", want: "$tenant_1$", ok: true},
		{input: "$1$", ok: false},
		{input: "$tenant", ok: false},
	} {
		got, ok := dollarQuoteDelimiter(tt.input, 0)
		if got != tt.want || ok != tt.ok {
			t.Fatalf("dollarQuoteDelimiter(%q) = %q, %t; want %q, %t", tt.input, got, ok, tt.want, tt.ok)
		}
	}
}

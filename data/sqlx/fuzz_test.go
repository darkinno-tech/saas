package sqlxtenant

import (
	"context"
	"strings"
	"testing"

	tenantctx "github.com/im10furry/saas/core/context"
	"github.com/im10furry/saas/core/types"
)

func FuzzQueryWithArgs(f *testing.F) {
	for _, seed := range []string{
		"SELECT * FROM orders",
		"DELETE FROM orders WHERE id = ?",
		"SELECT * FROM orders; DELETE FROM orders",
	} {
		f.Add(seed)
	}

	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-fuzz"})
	f.Fuzz(func(t *testing.T, baseSQL string) {
		rewritten, args, err := QueryWithArgs(ctx, baseSQL, []any{"existing"})
		if err != nil {
			return
		}

		if !strings.HasSuffix(rewritten, "tenant_id = ?") {
			t.Fatalf("QueryWithArgs(%q) = %q without an appended tenant condition", baseSQL, rewritten)
		}
		if len(args) != 2 || args[0] != "existing" || args[1] != "tenant-fuzz" {
			t.Fatalf("QueryWithArgs(%q) args = %#v, want existing argument plus tenant-fuzz", baseSQL, args)
		}
		if _, err := scanSQL(rewritten); err != nil {
			t.Fatalf("QueryWithArgs(%q) returned unscannable SQL %q: %v", baseSQL, rewritten, err)
		}
	})
}

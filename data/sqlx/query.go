package sqlxtenant

import (
	"context"
	"strings"

	"github.com/im10furry/saas/data"
)

// Query adds the tenant filter to a base SQL statement.
func Query(ctx context.Context, baseSQL string, opts ...data.FilterOption) (string, []any, error) {
	return QueryWithArgs(ctx, baseSQL, nil, opts...)
}

// TenantCondition returns a parameterized tenant condition for explicit placement in complex SQL.
//
// Use this helper for JOIN, GROUP BY, ORDER BY, LIMIT, or locking queries where Query cannot
// safely rewrite SQL. The returned condition is empty for host contexts.
func TenantCondition(ctx context.Context, opts ...data.FilterOption) (data.Condition, error) {
	filter, err := data.NewFilter(ctx, opts...)
	if err != nil {
		return data.Condition{}, err
	}
	condition := filter.Condition()
	condition.Args = append([]any(nil), condition.Args...)
	return condition, nil
}

// QueryWithArgs adds the tenant filter to a base SQL statement and preserves existing args.
func QueryWithArgs(ctx context.Context, baseSQL string, baseArgs []any, opts ...data.FilterOption) (string, []any, error) {
	baseSQL = strings.TrimSpace(baseSQL)
	if baseSQL == "" {
		return "", nil, ErrUnsafeSQL
	}

	filter, err := data.NewFilter(ctx, opts...)
	if err != nil {
		return "", nil, err
	}
	if filter.IsHost() {
		return baseSQL, append([]any(nil), baseArgs...), nil
	}

	condition := filter.Condition()
	if condition.Empty() {
		return baseSQL, append([]any(nil), baseArgs...), nil
	}
	analysis, err := analyzeTenantRewriteSQL(baseSQL, tenantFieldFromCondition(condition.Expression))
	if err != nil {
		return "", nil, err
	}

	args := append([]any(nil), baseArgs...)
	args = append(args, condition.Args...)
	return addTenantCondition(baseSQL, condition.Expression, analysis), args, nil
}

func tenantFieldFromCondition(expression string) string {
	if index := strings.Index(expression, " = ?"); index >= 0 {
		return strings.TrimSpace(expression[:index])
	}
	return ""
}

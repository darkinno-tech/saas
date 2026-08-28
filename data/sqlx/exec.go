package sqlxtenant

import (
	"context"
	"database/sql"

	"github.com/im10furry/saas/data"
)

// Queryer is implemented by *sqlx.DB and *sqlx.Tx.
type Queryer interface {
	SelectContext(ctx context.Context, dest interface{}, query string, args ...interface{}) error
	GetContext(ctx context.Context, dest interface{}, query string, args ...interface{}) error
}

// Execer is implemented by *sqlx.DB and *sqlx.Tx.
type Execer interface {
	ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error)
}

// SelectContext runs a tenant-filtered SELECT.
func SelectContext(ctx context.Context, db Queryer, dest interface{}, baseSQL string, baseArgs []any, opts ...data.FilterOption) error {
	query, args, err := QueryWithArgs(ctx, baseSQL, baseArgs, opts...)
	if err != nil {
		return err
	}
	return db.SelectContext(ctx, dest, query, args...)
}

// GetContext runs a tenant-filtered single-row SELECT.
func GetContext(ctx context.Context, db Queryer, dest interface{}, baseSQL string, baseArgs []any, opts ...data.FilterOption) error {
	query, args, err := QueryWithArgs(ctx, baseSQL, baseArgs, opts...)
	if err != nil {
		return err
	}
	return db.GetContext(ctx, dest, query, args...)
}

// ExecContext runs a tenant-filtered write statement.
func ExecContext(ctx context.Context, db Execer, baseSQL string, baseArgs []any, opts ...data.FilterOption) (sql.Result, error) {
	query, args, err := QueryWithArgs(ctx, baseSQL, baseArgs, opts...)
	if err != nil {
		return nil, err
	}
	return db.ExecContext(ctx, query, args...)
}

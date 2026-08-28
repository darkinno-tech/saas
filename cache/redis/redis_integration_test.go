package redis

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	corecache "github.com/darkinno-tech/saas/cache"
	tenantctx "github.com/darkinno-tech/saas/core/context"
	"github.com/darkinno-tech/saas/core/types"
	goredis "github.com/redis/go-redis/v9"
)

func TestRedisCacheIntegration(t *testing.T) {
	addr := os.Getenv("SAAS_REDIS_ADDR")
	if addr == "" {
		t.Skip("set SAAS_REDIS_ADDR to run Redis cache integration tests")
	}

	db := 0
	if raw := os.Getenv("SAAS_REDIS_DB"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			t.Fatalf("invalid SAAS_REDIS_DB: %v", err)
		}
		db = parsed
	}

	ctx := context.Background()
	client := goredis.NewClient(&goredis.Options{
		Addr:     addr,
		Password: os.Getenv("SAAS_REDIS_PASSWORD"),
		DB:       db,
	})
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatalf("Ping() error = %v", err)
	}

	base, err := New(client)
	if err != nil {
		t.Fatalf("NewRedis() error = %v", err)
	}
	if err := base.Ping(ctx); err != nil {
		t.Fatalf("cache Ping() error = %v", err)
	}
	t.Cleanup(func() {
		if err := base.Close(); err != nil {
			t.Logf("Close() error = %v", err)
		}
	})

	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	scoped := corecache.NewTenantCache(base)
	ctxA := tenantctx.WithTenant(ctx, types.Tenant{ID: types.TenantID("redis-a-" + suffix)})
	ctxB := tenantctx.WithTenant(ctx, types.Tenant{ID: types.TenantID("redis-b-" + suffix)})
	t.Cleanup(func() {
		_ = scoped.Delete(ctxA, "profile")
		_ = scoped.Delete(ctxB, "profile")
		_ = scoped.Delete(ctxA, "ephemeral")
	})

	if err := scoped.Set(ctxA, "profile", []byte("a"), time.Minute); err != nil {
		t.Fatalf("Set(ctxA) error = %v", err)
	}
	if err := scoped.Set(ctxB, "profile", []byte("b"), time.Minute); err != nil {
		t.Fatalf("Set(ctxB) error = %v", err)
	}

	got, ok, err := scoped.Get(ctxA, "profile")
	if err != nil {
		t.Fatalf("Get(ctxA) error = %v", err)
	}
	if !ok || string(got) != "a" {
		t.Fatalf("Get(ctxA) = %q, %v; want a, true", got, ok)
	}

	got, ok, err = scoped.Get(ctxB, "profile")
	if err != nil {
		t.Fatalf("Get(ctxB) error = %v", err)
	}
	if !ok || string(got) != "b" {
		t.Fatalf("Get(ctxB) = %q, %v; want b, true", got, ok)
	}

	if err := scoped.Set(ctxA, "ephemeral", []byte("gone"), 10*time.Millisecond); err != nil {
		t.Fatalf("Set(ephemeral) error = %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if got, ok, err := scoped.Get(ctxA, "ephemeral"); err != nil || ok || got != nil {
		t.Fatalf("Get(expired) = %q, %v, %v; want miss", got, ok, err)
	}
}

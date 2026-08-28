//go:build chaos

package redis

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	corecache "github.com/darkinno-tech/saas/cache"
	tenantctx "github.com/darkinno-tech/saas/core/context"
	"github.com/darkinno-tech/saas/core/types"
	"github.com/darkinno-tech/saas/internal/testtoxiproxy"
	goredis "github.com/redis/go-redis/v9"
)

const (
	chaosToxiproxyURL = "http://127.0.0.1:58474"
	chaosRedisAddress = "127.0.0.1:58668"
)

func TestRedisChaosEnvironmentValidation(t *testing.T) {
	tests := []struct {
		name     string
		variable string
		value    string
		expected string
		wantErr  bool
	}{
		{name: "expected toxiproxy URL", variable: "SAAS_TOXIPROXY_URL", value: chaosToxiproxyURL, expected: chaosToxiproxyURL},
		{name: "expected redis address", variable: "SAAS_CHAOS_REDIS_ADDR", value: chaosRedisAddress, expected: chaosRedisAddress},
		{name: "missing toxiproxy URL", variable: "SAAS_TOXIPROXY_URL", expected: chaosToxiproxyURL, wantErr: true},
		{name: "external redis address", variable: "SAAS_CHAOS_REDIS_ADDR", value: "redis.example.com:6379", expected: chaosRedisAddress, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateRedisChaosValue(tt.variable, tt.value, tt.expected)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateRedisChaosValue(%q) error = %v, wantErr %v", tt.value, err, tt.wantErr)
			}
		})
	}
}

func TestRedisChaos(t *testing.T) {
	if os.Getenv("SAAS_CHAOS") != "1" {
		t.Skip("set SAAS_CHAOS=1 to run Redis chaos test")
	}

	toxiproxyURL := requireChaosEnvironment(t, "SAAS_TOXIPROXY_URL", chaosToxiproxyURL)
	redisAddress := requireChaosEnvironment(t, "SAAS_CHAOS_REDIS_ADDR", chaosRedisAddress)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	toxiproxy := testtoxiproxy.New(toxiproxyURL)
	if err := toxiproxy.Wait(ctx); err != nil {
		t.Fatalf("wait for Toxiproxy: %v", err)
	}

	const proxyName = "saas_redis"
	if _, err := toxiproxy.CreateProxy(ctx, proxyName, "0.0.0.0:8668", "redis:6379"); err != nil {
		t.Fatalf("CreateProxy() error = %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if err := toxiproxy.DeleteProxy(cleanupCtx, proxyName); err != nil {
			t.Errorf("DeleteProxy() error = %v", err)
		}
	})

	redisClient := goredis.NewClient(&goredis.Options{
		Addr:         redisAddress,
		DialTimeout:  750 * time.Millisecond,
		ReadTimeout:  750 * time.Millisecond,
		WriteTimeout: 750 * time.Millisecond,
		MaxRetries:   -1,
	})
	base, err := New(redisClient)
	if err != nil {
		t.Fatalf("NewRedis() error = %v", err)
	}
	t.Cleanup(func() {
		if err := base.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	if err := waitForRedisPing(ctx, base); err != nil {
		t.Fatalf("wait for Redis Ping: %v", err)
	}

	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	tenantA := types.Tenant{ID: types.TenantID("redis-chaos-a-" + suffix)}
	tenantB := types.Tenant{ID: types.TenantID("redis-chaos-b-" + suffix)}
	ctxA := tenantctx.WithTenant(ctx, tenantA)
	ctxB := tenantctx.WithTenant(ctx, tenantB)
	scoped := corecache.NewTenantCache(base)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		cleanupCtxA := tenantctx.WithTenant(cleanupCtx, tenantA)
		cleanupCtxB := tenantctx.WithTenant(cleanupCtx, tenantB)
		for _, key := range []string{"profile", "recovered"} {
			_ = scoped.Delete(cleanupCtxA, key)
			_ = scoped.Delete(cleanupCtxB, key)
		}
	})

	assertRedisTenantValue(t, scoped, ctxA, ctxB, "profile", "a", "b")

	const toxicName = "blocked"
	toxicAdded := false
	t.Cleanup(func() {
		if !toxicAdded {
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if err := toxiproxy.RemoveToxic(cleanupCtx, proxyName, toxicName); err != nil {
			t.Logf("RemoveToxic() cleanup error = %v", err)
		}
	})
	if err := toxiproxy.AddTimeout(ctx, proxyName, toxicName); err != nil {
		t.Fatalf("AddTimeout() error = %v", err)
	}
	toxicAdded = true

	faultCtx, faultCancel := context.WithTimeout(context.Background(), 5*time.Second)
	err = base.Ping(faultCtx)
	faultCancel()
	if err == nil {
		t.Fatal("Ping() succeeded while Toxiproxy timeout toxic was active")
	}

	if err := toxiproxy.RemoveToxic(ctx, proxyName, toxicName); err != nil {
		t.Fatalf("RemoveToxic() error = %v", err)
	}
	toxicAdded = false

	recoveryCtx, recoveryCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer recoveryCancel()
	if err := waitForRedisPing(recoveryCtx, base); err != nil {
		t.Fatalf("wait for Redis recovery: %v", err)
	}
	assertRedisTenantValue(t, corecache.NewTenantCache(base), ctxA, ctxB, "recovered", "recovered-a", "recovered-b")
}

func requireChaosEnvironment(t *testing.T, name, expected string) string {
	t.Helper()
	value := os.Getenv(name)
	if err := validateRedisChaosValue(name, value, expected); err != nil {
		t.Fatal(err)
	}
	return value
}

func validateRedisChaosValue(name, value, expected string) error {
	if value == "" {
		return fmt.Errorf("%s must be set when SAAS_CHAOS=1", name)
	}
	if value != expected {
		return fmt.Errorf("%s must target the local disposable Compose address", name)
	}
	return nil
}

func waitForRedisPing(ctx context.Context, cache *Redis) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		if err := cache.Ping(ctx); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func assertRedisTenantValue(t *testing.T, cache *corecache.TenantCache, ctxA, ctxB context.Context, key, valueA, valueB string) {
	t.Helper()
	if valueA == valueB {
		t.Fatalf("test setup for %q does not distinguish tenant values", key)
	}
	if err := cache.Set(ctxA, key, []byte(valueA), time.Minute); err != nil {
		t.Fatalf("Set(tenant A, %q) error = %v", key, err)
	}
	if err := cache.Set(ctxB, key, []byte(valueB), time.Minute); err != nil {
		t.Fatalf("Set(tenant B, %q) error = %v", key, err)
	}
	for _, assertion := range []struct {
		name string
		ctx  context.Context
		want string
	}{
		{name: "tenant A", ctx: ctxA, want: valueA},
		{name: "tenant B", ctx: ctxB, want: valueB},
	} {
		got, ok, err := cache.Get(assertion.ctx, key)
		if err != nil {
			t.Fatalf("Get(%s, %q) error = %v", assertion.name, key, err)
		}
		if !ok || string(got) != assertion.want {
			t.Fatalf("Get(%s, %q) = %q, %v; want %q, true", assertion.name, key, got, ok, assertion.want)
		}
	}
}

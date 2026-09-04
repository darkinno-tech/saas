package ai

import (
	"context"
	"errors"
	"testing"

	tenantctx "github.com/darkinno-tech/saas/core/context"
	"github.com/darkinno-tech/saas/core/types"
	"github.com/darkinno-tech/saas/quota"
)

func TestMockProviderChat(t *testing.T) {
	provider := &MockProvider{NameID: "mock", Reply: "hello", Usage: Usage{InputTokens: 3, OutputTokens: 5}}
	resp, err := provider.Chat(context.Background(), Request{Model: "m1"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "hello" {
		t.Fatalf("Content = %q", resp.Content)
	}
	if resp.Usage.TotalTokens() != 8 {
		t.Fatalf("TotalTokens = %d", resp.Usage.TotalTokens())
	}
	if provider.Calls != 1 {
		t.Fatalf("Calls = %d", provider.Calls)
	}

	provider.ChatErr = errors.New("boom")
	if _, err := provider.Chat(context.Background(), Request{}); err == nil {
		t.Fatal("expected chat error")
	}
}

func TestRegistry(t *testing.T) {
	registry := NewRegistry()
	provider := &MockProvider{NameID: "mock"}
	if err := registry.Register(provider); err != nil {
		t.Fatal(err)
	}
	got, err := registry.Get("mock")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name() != "mock" {
		t.Fatalf("got %q", got.Name())
	}
	if _, err := registry.Get("missing"); !errors.Is(err, ErrProviderNotFound) {
		t.Fatalf("err = %v", err)
	}
	if err := registry.Register(nil); !errors.Is(err, ErrNilProvider) {
		t.Fatalf("register nil err = %v", err)
	}
}

func tenantContext(t *testing.T, id string) context.Context {
	t.Helper()
	return tenantctx.WithTenant(context.Background(), types.Tenant{ID: types.TenantID(id)})
}

func mustMeter(t *testing.T) *Meter {
	t.Helper()
	return NewMeter(quota.NewMemoryStore())
}

func TestRecordUsageWithoutTenant(t *testing.T) {
	meter := mustMeter(t)
	err := meter.RecordUsage(context.Background(), Usage{Model: "gpt", InputTokens: 1}, quota.PeriodDay)
	if !errors.Is(err, ErrNoTenant) {
		t.Fatalf("err = %v", err)
	}
}

func TestRecordUsage(t *testing.T) {
	meter := mustMeter(t)
	ctx := tenantContext(t, "tenant-a")
	usage := Usage{Model: "gpt-4o", Turns: 1, InputTokens: 100, OutputTokens: 50}

	if err := meter.RecordUsage(ctx, usage, quota.PeriodDay); err != nil {
		t.Fatal(err)
	}
	budget, err := meter.CheckUsage(ctx, TokenLimit{
		TenantID: types.TenantID("tenant-a"), Model: "gpt-4o", Period: quota.PeriodDay,
	}, Usage{})
	if err != nil {
		t.Fatal(err)
	}
	if budget.UsedInput != 100 || budget.UsedOutput != 50 || budget.UsedTotal != 150 {
		t.Fatalf("budget = %+v", budget)
	}
	if ResourceKey("gpt-4o", DimInputTokens) != "ai//gpt-4o//tokens_input" {
		t.Fatalf("ResourceKey = %q", ResourceKey("gpt-4o", DimInputTokens))
	}
}

func TestCheckUsageExceeded(t *testing.T) {
	meter := mustMeter(t)
	ctx := tenantContext(t, "tenant-a")
	limit := TokenLimit{
		TenantID: types.TenantID("tenant-a"), Model: "gpt-4o",
		InputLimit: 100, OutputLimit: 50, TotalLimit: 150, Period: quota.PeriodDay,
	}

	if err := meter.RecordUsage(ctx, Usage{Model: "gpt-4o", InputTokens: 80, OutputTokens: 30}, quota.PeriodDay); err != nil {
		t.Fatal(err)
	}
	// within limit: 80+20 in, 30+20 out, 110+40 total < 150
	if _, err := meter.CheckUsage(ctx, limit, Usage{InputTokens: 20, OutputTokens: 20}); err != nil {
		t.Fatalf("expected within limit, got %v", err)
	}
	// input over: 80+30 > 100
	if _, err := meter.CheckUsage(ctx, limit, Usage{InputTokens: 30}); !errors.Is(err, quota.ErrQuotaExceeded) {
		t.Fatalf("err = %v", err)
	}
	// total over: 110+50 > 150
	if _, err := meter.CheckUsage(ctx, limit, Usage{InputTokens: 30, OutputTokens: 20}); !errors.Is(err, quota.ErrQuotaExceeded) {
		t.Fatalf("total err = %v", err)
	}
}

func TestConsumeUsage(t *testing.T) {
	meter := mustMeter(t)
	ctx := tenantContext(t, "tenant-a")
	limit := TokenLimit{
		TenantID: types.TenantID("tenant-a"), Model: "gpt-4o",
		InputLimit: 100, OutputLimit: 100, TotalLimit: 200, Period: quota.PeriodDay,
	}

	budget, err := meter.ConsumeUsage(ctx, limit, Usage{Model: "gpt-4o", InputTokens: 30, OutputTokens: 20})
	if err != nil {
		t.Fatal(err)
	}
	if budget.UsedInput != 30 || budget.UsedOutput != 20 {
		t.Fatalf("budget = %+v", budget)
	}

	if _, err := meter.ConsumeUsage(ctx, limit, Usage{InputTokens: 80}); !errors.Is(err, quota.ErrQuotaExceeded) {
		t.Fatalf("exceed err = %v", err)
	}
}

func TestEmbeddingProvider(t *testing.T) {
	provider := &MockEmbedding{NameID: "emb", Vectors: [][]float32{{0.1}, {0.2}}}
	res, err := provider.Embed(context.Background(), EmbeddingInput{Model: "m", Texts: []string{"a", "b"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Embeddings) != 2 {
		t.Fatalf("embeddings = %d", len(res.Embeddings))
	}
	if provider.EmbedCalls != 1 || provider.ChatCalls != 0 {
		t.Fatalf("calls chat=%d embed=%d", provider.ChatCalls, provider.EmbedCalls)
	}
	var _ EmbeddingProvider = provider
}

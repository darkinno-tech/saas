package obs

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	tenantctx "github.com/im10furry/saas/core/context"
	"github.com/im10furry/saas/core/types"
)

func TestFields(t *testing.T) {
	tenantFields := Fields(tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"}))
	if tenantFields[TenantIDField] != "tenant-a" || tenantFields[TenantSideField] != tenantSide {
		t.Fatalf("tenant Fields() = %#v, want tenant-a tenant side", tenantFields)
	}

	hostFields := Fields(tenantctx.WithHost(context.Background()))
	if _, ok := hostFields[TenantIDField]; ok {
		t.Fatalf("host Fields() tenant id = %q, want absent", hostFields[TenantIDField])
	}
	if hostFields[TenantSideField] != hostSide {
		t.Fatalf("host Fields() = %#v, want host side", hostFields)
	}

	deploymentFields := Fields(tenantctx.WithTenantDeployment(context.Background(), types.Tenant{ID: "tenant-a"}, types.DeploymentUnit{
		ID:     "cn-shanghai-1",
		Status: types.DeploymentUnitStatusActive,
	}))
	if deploymentFields[DeploymentUnitIDField] != "cn-shanghai-1" {
		t.Fatalf("deployment Fields() unit = %q, want cn-shanghai-1", deploymentFields[DeploymentUnitIDField])
	}

	empty := Fields(context.Background())
	if len(empty) != 0 {
		t.Fatalf("background Fields() = %#v, want empty", empty)
	}
}

func TestRedact(t *testing.T) {
	input := map[string]string{"tenant_id": "tenant-a", "api-key": "secret", "Password": "pw"}
	got := Redact(input)
	if got["tenant_id"] != "tenant-a" {
		t.Fatalf("Redact() tenant_id = %q, want tenant-a", got["tenant_id"])
	}
	if got["api-key"] != redactedValue || got["Password"] != redactedValue {
		t.Fatalf("Redact() = %#v, want sensitive fields redacted", got)
	}

	got["tenant_id"] = "changed"
	if input["tenant_id"] != "tenant-a" {
		t.Fatal("Redact() mutated input")
	}
}

func TestSlogAttrs(t *testing.T) {
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})
	attrs := SlogAttrs(ctx)
	if len(attrs) != 2 {
		t.Fatalf("SlogAttrs() len = %d, want 2", len(attrs))
	}
	if attrs[0].Key != TenantIDField || attrs[0].Value.String() != "tenant-a" {
		t.Fatalf("SlogAttrs()[0] = %#v, want tenant_id tenant-a", attrs[0])
	}
	if attrs[1].Key != TenantSideField || attrs[1].Value.String() != tenantSide {
		t.Fatalf("SlogAttrs()[1] = %#v, want tenant side", attrs[1])
	}
}

func TestSlogAttrsIncludesDeploymentUnitID(t *testing.T) {
	ctx := tenantctx.WithTenantDeployment(context.Background(), types.Tenant{ID: "tenant-a"}, types.DeploymentUnit{
		ID:     "eu-central-1",
		Status: types.DeploymentUnitStatusActive,
	})
	attrs := SlogAttrs(ctx)
	if len(attrs) != 3 {
		t.Fatalf("SlogAttrs() len = %d, want 3", len(attrs))
	}
	if attrs[2].Key != DeploymentUnitIDField || attrs[2].Value.String() != "eu-central-1" {
		t.Fatalf("SlogAttrs()[2] = %#v, want deployment unit", attrs[2])
	}
}

func TestRedactSlogAttrs(t *testing.T) {
	attrs := RedactSlogAttrs(
		slog.String("authorization", "Bearer token"),
		slog.Group("nested", slog.String("refresh-token", "secret"), slog.String("safe", "value")),
		slog.Any("lazy", sensitiveLogValue{}),
	)
	if attrs[0].Value.String() != redactedValue {
		t.Fatalf("RedactSlogAttrs()[0] = %#v, want redacted", attrs[0])
	}

	group := attrs[1].Value.Group()
	if group[0].Value.String() != redactedValue || group[1].Value.String() != "value" {
		t.Fatalf("RedactSlogAttrs() group = %#v, want nested sensitive redacted", group)
	}

	lazyGroup := attrs[2].Value.Group()
	if lazyGroup[0].Value.String() != redactedValue || lazyGroup[1].Value.String() != "value" {
		t.Fatalf("RedactSlogAttrs() log valuer group = %#v, want sensitive redacted", lazyGroup)
	}
}

func TestLogAttrsAddsTenantAndRedacts(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})

	LogAttrs(ctx, logger, slog.LevelInfo, "created", slog.String("api_key", "secret"), slog.String("safe", "value"))

	var record map[string]any
	if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if record[TenantIDField] != "tenant-a" || record[TenantSideField] != tenantSide {
		t.Fatalf("LogAttrs() tenant fields = %#v", record)
	}
	if record["api_key"] != redactedValue || record["safe"] != "value" {
		t.Fatalf("LogAttrs() fields = %#v, want redacted api_key and safe value", record)
	}
}

func TestLoggerWithTenant(t *testing.T) {
	var buf bytes.Buffer
	base := slog.New(slog.NewJSONHandler(&buf, nil))
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})

	LoggerWithTenant(ctx, base).InfoContext(context.Background(), "ready")

	var record map[string]any
	if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if record[TenantIDField] != "tenant-a" || record[TenantSideField] != tenantSide {
		t.Fatalf("LoggerWithTenant() record = %#v", record)
	}
}

type sensitiveLogValue struct{}

func (sensitiveLogValue) LogValue() slog.Value {
	return slog.GroupValue(slog.String("api_key", "secret"), slog.String("safe", "value"))
}

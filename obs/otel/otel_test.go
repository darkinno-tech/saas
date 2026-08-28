package otel

import (
	"context"
	"testing"

	tenantctx "github.com/im10furry/saas/core/context"
	"github.com/im10furry/saas/core/types"
	obs "github.com/im10furry/saas/obs"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

func TestNewTracer(t *testing.T) {
	t.Run("uses process provider when nil", func(t *testing.T) {
		tracer := NewTracer(nil)
		if tracer == nil {
			t.Fatal("NewTracer(nil) = nil")
		}

		_, span := tracer.Start(context.Background(), "tenant.operation")
		if span == nil {
			t.Fatal("NewTracer(nil).Start() = nil span")
		}
		span.End()
	})

	t.Run("uses supplied provider and SaaS scope", func(t *testing.T) {
		want := &recordingTracer{}
		provider := &recordingTracerProvider{tracer: want}

		got := NewTracer(provider)

		if got != want {
			t.Fatalf("NewTracer(provider) = %T, want supplied tracer %T", got, want)
		}
		if provider.name != InstrumentationName {
			t.Fatalf("provider scope = %q, want %q", provider.name, InstrumentationName)
		}
	})
}

func TestSpanAttributes(t *testing.T) {
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})
	attrs := SpanAttributes(ctx)
	assertAttribute(t, attrs, obs.TenantIDField, "tenant-a")
	assertAttribute(t, attrs, obs.TenantSideField, "tenant")
}

func TestSpanAttributesWithoutTenantAndForHost(t *testing.T) {
	t.Run("no tenant", func(t *testing.T) {
		if attrs := SpanAttributes(context.Background()); attrs != nil {
			t.Fatalf("SpanAttributes() = %#v, want nil", attrs)
		}
	})

	t.Run("host", func(t *testing.T) {
		attrs := SpanAttributes(tenantctx.WithHost(context.Background()))
		if len(attrs) != 1 {
			t.Fatalf("SpanAttributes(host) count = %d, want 1", len(attrs))
		}
		assertAttribute(t, attrs, obs.TenantSideField, "host")
	})
}

func TestSpanAttributesIncludesDeploymentUnitID(t *testing.T) {
	ctx := tenantctx.WithTenantDeployment(context.Background(), types.Tenant{ID: "tenant-a"}, types.DeploymentUnit{
		ID:     "eu-central-1",
		Status: types.DeploymentUnitStatusActive,
	})
	attrs := SpanAttributes(ctx)
	assertAttribute(t, attrs, obs.DeploymentUnitIDField, "eu-central-1")
}

func TestAddSpanAttributes(t *testing.T) {
	span := &recordingSpan{recording: true}
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})
	ctx = trace.ContextWithSpan(ctx, span)

	AddSpanAttributes(ctx)

	assertAttribute(t, span.attrs, obs.TenantIDField, "tenant-a")
	assertAttribute(t, span.attrs, obs.TenantSideField, "tenant")
}

func TestAddSpanAttributesNoopPaths(t *testing.T) {
	t.Run("context has no tenant fields", func(t *testing.T) {
		span := &recordingSpan{recording: true}
		ctx := trace.ContextWithSpan(context.Background(), span)

		AddSpanAttributes(ctx)

		if len(span.attrs) != 0 {
			t.Fatalf("AddSpanAttributes() set %#v for unscoped context", span.attrs)
		}
	})

	t.Run("span is not recording", func(t *testing.T) {
		span := &recordingSpan{}
		ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})
		ctx = trace.ContextWithSpan(ctx, span)

		AddSpanAttributes(ctx)

		if len(span.attrs) != 0 {
			t.Fatalf("AddSpanAttributes() set %#v on non-recording span", span.attrs)
		}
	})
}

func TestStartSpanAddsTenantAttributes(t *testing.T) {
	tracer := &recordingTracer{}
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})

	_, span := StartSpan(ctx, tracer, "tenant.operation")
	span.End()

	if tracer.name != "tenant.operation" {
		t.Fatalf("StartSpan() name = %q, want tenant.operation", tracer.name)
	}
	config := trace.NewSpanStartConfig(tracer.opts...)
	assertAttribute(t, config.Attributes(), obs.TenantIDField, "tenant-a")
	assertAttribute(t, config.Attributes(), obs.TenantSideField, "tenant")
}

func TestStartSpanWithoutTenantAndWithNilTracer(t *testing.T) {
	t.Run("does not add attributes to unscoped start", func(t *testing.T) {
		tracer := &recordingTracer{}
		option := trace.WithSpanKind(trace.SpanKindServer)

		_, span := StartSpan(context.Background(), tracer, "unscoped.operation", option)
		span.End()

		if len(tracer.opts) != 1 {
			t.Fatalf("StartSpan() options = %d, want only caller option", len(tracer.opts))
		}
	})

	t.Run("uses process tracer when tracer is nil", func(t *testing.T) {
		ctx, span := StartSpan(context.Background(), nil, "unscoped.operation")
		if span == nil {
			t.Fatal("StartSpan(nil) = nil span")
		}
		if trace.SpanFromContext(ctx) == nil {
			t.Fatal("StartSpan(nil) did not return a span context")
		}
		span.End()
	})
}

func TestRecordSpanError(t *testing.T) {
	span := &recordingSpan{recording: true}
	ctx := trace.ContextWithSpan(context.Background(), span)
	err := sensitiveTestError{}

	RecordSpanError(ctx, err, "")

	if span.err == nil || span.err.Error() != defaultErrorDescription {
		t.Fatalf("RecordSpanError() err = %v, want sanitized default description", span.err)
	}
	if span.err.Error() == err.Error() {
		t.Fatalf("RecordSpanError() recorded raw error text")
	}
	if span.status != codes.Error || span.statusDescription != defaultErrorDescription {
		t.Fatalf("RecordSpanError() status = %v %q, want error default description", span.status, span.statusDescription)
	}
	assertAttribute(t, span.eventAttrs, errorTypeAttribute, "otel.sensitiveTestError")
}

func TestRecordSpanErrorNoopPathsAndExplicitDescription(t *testing.T) {
	t.Run("nil error", func(t *testing.T) {
		span := &recordingSpan{recording: true}
		ctx := trace.ContextWithSpan(context.Background(), span)

		RecordSpanError(ctx, nil, "unused")

		if span.err != nil || span.status != codes.Unset {
			t.Fatalf("RecordSpanError(nil) mutated span: err=%v status=%v", span.err, span.status)
		}
	})

	t.Run("non-recording span", func(t *testing.T) {
		span := &recordingSpan{}
		ctx := trace.ContextWithSpan(context.Background(), span)

		RecordSpanError(ctx, sensitiveTestError{}, "upstream unavailable")

		if span.err != nil || span.status != codes.Unset {
			t.Fatalf("RecordSpanError() mutated non-recording span: err=%v status=%v", span.err, span.status)
		}
	})

	t.Run("explicit description remains sanitized", func(t *testing.T) {
		span := &recordingSpan{recording: true}
		ctx := trace.ContextWithSpan(context.Background(), span)
		description := "upstream unavailable"

		RecordSpanError(ctx, sensitiveTestError{}, description)

		if span.err == nil || span.err.Error() != description {
			t.Fatalf("RecordSpanError() err = %v, want %q", span.err, description)
		}
		if span.status != codes.Error || span.statusDescription != description {
			t.Fatalf("RecordSpanError() status = %v %q, want error %q", span.status, span.statusDescription, description)
		}
		assertAttribute(t, span.eventAttrs, errorTypeAttribute, "otel.sensitiveTestError")
	})
}

func TestErrorTypeNil(t *testing.T) {
	if got := errorType(nil); got != "" {
		t.Fatalf("errorType(nil) = %q, want empty", got)
	}
}

func assertAttribute(t *testing.T, attrs []attribute.KeyValue, key, value string) {
	t.Helper()
	for _, attr := range attrs {
		if string(attr.Key) == key {
			if attr.Value.AsString() != value {
				t.Fatalf("attribute %s = %q, want %q", key, attr.Value.AsString(), value)
			}
			return
		}
	}
	t.Fatalf("attribute %s missing in %#v", key, attrs)
}

type recordingTracer struct {
	noop.Tracer

	name string
	opts []trace.SpanStartOption
	span *recordingSpan
}

func (tracer *recordingTracer) Start(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	tracer.name = name
	tracer.opts = append([]trace.SpanStartOption(nil), opts...)
	tracer.span = &recordingSpan{recording: true}
	return trace.ContextWithSpan(ctx, tracer.span), tracer.span
}

type recordingTracerProvider struct {
	noop.TracerProvider

	name   string
	tracer trace.Tracer
}

func (provider *recordingTracerProvider) Tracer(name string, _ ...trace.TracerOption) trace.Tracer {
	provider.name = name
	return provider.tracer
}

type recordingSpan struct {
	noop.Span

	recording         bool
	attrs             []attribute.KeyValue
	eventAttrs        []attribute.KeyValue
	err               error
	status            codes.Code
	statusDescription string
}

func (span *recordingSpan) IsRecording() bool {
	return span.recording
}

func (span *recordingSpan) SetAttributes(attrs ...attribute.KeyValue) {
	span.attrs = append(span.attrs, attrs...)
}

func (span *recordingSpan) RecordError(err error, opts ...trace.EventOption) {
	span.err = err
	config := trace.NewEventConfig(opts...)
	span.eventAttrs = append(span.eventAttrs, config.Attributes()...)
}

func (span *recordingSpan) SetStatus(code codes.Code, description string) {
	span.status = code
	span.statusDescription = description
}

type sensitiveTestError struct{}

func (sensitiveTestError) Error() string {
	return "password=secret"
}

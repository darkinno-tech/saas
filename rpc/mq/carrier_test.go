package mq

import (
	"context"
	"errors"
	"reflect"
	"testing"

	tenantctx "github.com/im10furry/saas/core/context"
	"github.com/im10furry/saas/core/types"
	"github.com/im10furry/saas/rpc"
)

var (
	_ rpc.Carrier = NATSCarrier{}
	_ rpc.Carrier = RabbitMQCarrier{}
	_ rpc.Carrier = KafkaCarrier{}
)

func TestNATSCarrierRoundTrip(t *testing.T) {
	for _, test := range []struct {
		name string
		key  string
		want string
	}{
		{name: "default key", want: rpc.DefaultTenantMetadataKey},
		{name: "custom key", key: "tenant-id", want: "tenant-id"},
	} {
		t.Run(test.name, func(t *testing.T) {
			headers := fakeNATSHeaders{}
			carrier, err := NewNATSCarrier(headers)
			if err != nil {
				t.Fatalf("NewNATSCarrier() error = %v", err)
			}

			injectAndExtractTenant(t, carrier, test.key)
			if got := headers[test.want]; got != "tenant-a" {
				t.Fatalf("header %q = %q, want tenant-a", test.want, got)
			}
		})
	}
}

func TestRabbitMQCarrierRoundTrip(t *testing.T) {
	for _, test := range []struct {
		name string
		key  string
		want string
	}{
		{name: "default key", want: rpc.DefaultTenantMetadataKey},
		{name: "custom key", key: "tenant-id", want: "tenant-id"},
	} {
		t.Run(test.name, func(t *testing.T) {
			headers := fakeRabbitMQHeaders{}
			carrier, err := NewRabbitMQCarrier(headers)
			if err != nil {
				t.Fatalf("NewRabbitMQCarrier() error = %v", err)
			}

			injectAndExtractTenant(t, carrier, test.key)
			if got := headers[test.want]; got != "tenant-a" {
				t.Fatalf("header %q = %q, want tenant-a", test.want, got)
			}
		})
	}
}

func TestKafkaCarrierRoundTrip(t *testing.T) {
	for _, test := range []struct {
		name string
		key  string
		want string
	}{
		{name: "default key", want: rpc.DefaultTenantMetadataKey},
		{name: "custom key", key: "tenant-id", want: "tenant-id"},
	} {
		t.Run(test.name, func(t *testing.T) {
			headers := fakeKafkaHeaders{}
			carrier, err := NewKafkaCarrier(headers)
			if err != nil {
				t.Fatalf("NewKafkaCarrier() error = %v", err)
			}

			injectAndExtractTenant(t, carrier, test.key)
			if got := string(headers[test.want]); got != "tenant-a" {
				t.Fatalf("header %q = %q, want tenant-a", test.want, got)
			}
		})
	}
}

func TestCarriersRejectMissingTenantContext(t *testing.T) {
	for _, test := range carrierCases(t) {
		t.Run(test.name, func(t *testing.T) {
			if err := rpc.InjectTenant(context.Background(), test.carrier, ""); !errors.Is(err, rpc.ErrNoTenantMetadata) {
				t.Fatalf("InjectTenant() error = %v, want ErrNoTenantMetadata", err)
			}
		})
	}
}

func TestCarriersRejectMissingHeaders(t *testing.T) {
	for _, test := range carrierCases(t) {
		t.Run(test.name, func(t *testing.T) {
			if _, err := rpc.ExtractTenant(test.carrier, "", types.TenantIDStrategyString); !errors.Is(err, rpc.ErrNoTenantMetadata) {
				t.Fatalf("ExtractTenant() error = %v, want ErrNoTenantMetadata", err)
			}
		})
	}
}

func TestNATSAndKafkaTreatEmptyHeadersAsAbsent(t *testing.T) {
	nats, err := NewNATSCarrier(fakeNATSHeaders{rpc.DefaultTenantMetadataKey: ""})
	if err != nil {
		t.Fatalf("NewNATSCarrier() error = %v", err)
	}
	if _, err := rpc.ExtractTenant(nats, "", types.TenantIDStrategyString); !errors.Is(err, rpc.ErrNoTenantMetadata) {
		t.Fatalf("ExtractTenant(NATS empty) error = %v, want ErrNoTenantMetadata", err)
	}

	kafka, err := NewKafkaCarrier(fakeKafkaHeaders{rpc.DefaultTenantMetadataKey: {}})
	if err != nil {
		t.Fatalf("NewKafkaCarrier() error = %v", err)
	}
	if _, err := rpc.ExtractTenant(kafka, "", types.TenantIDStrategyString); !errors.Is(err, rpc.ErrNoTenantMetadata) {
		t.Fatalf("ExtractTenant(Kafka empty) error = %v, want ErrNoTenantMetadata", err)
	}
}

func TestRabbitMQCarrierPreservesStringPresenceContract(t *testing.T) {
	carrier, err := NewRabbitMQCarrier(fakeRabbitMQHeaders{rpc.DefaultTenantMetadataKey: ""})
	if err != nil {
		t.Fatalf("NewRabbitMQCarrier() error = %v", err)
	}
	if _, err := rpc.ExtractTenant(carrier, "", types.TenantIDStrategyString); !errors.Is(err, types.ErrEmptyTenantID) {
		t.Fatalf("ExtractTenant(RabbitMQ empty) error = %v, want ErrEmptyTenantID", err)
	}
}

func TestCarriersRejectInvalidTenantMetadata(t *testing.T) {
	for _, test := range []struct {
		name    string
		carrier rpc.Carrier
	}{
		{name: "NATS", carrier: mustNATSCarrier(t, fakeNATSHeaders{rpc.DefaultTenantMetadataKey: "not-an-int"})},
		{name: "RabbitMQ", carrier: mustRabbitMQCarrier(t, fakeRabbitMQHeaders{rpc.DefaultTenantMetadataKey: "not-an-int"})},
		{name: "Kafka", carrier: mustKafkaCarrier(t, fakeKafkaHeaders{rpc.DefaultTenantMetadataKey: []byte("not-an-int")})},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := rpc.ExtractTenant(test.carrier, "", types.TenantIDStrategyInt); !errors.Is(err, types.ErrInvalidTenantID) {
				t.Fatalf("ExtractTenant() error = %v, want ErrInvalidTenantID", err)
			}
			if _, err := rpc.ExtractTenant(test.carrier, "", types.TenantIDStrategy("unsupported")); !errors.Is(err, types.ErrInvalidTenantID) {
				t.Fatalf("ExtractTenant(unsupported strategy) error = %v, want ErrInvalidTenantID", err)
			}
		})
	}
}

func TestConstructorsValidateAndDoNotMutateHeaders(t *testing.T) {
	if _, err := NewNATSCarrier(nil); !errors.Is(err, ErrInvalidHeaders) {
		t.Fatalf("NewNATSCarrier(nil) error = %v, want ErrInvalidHeaders", err)
	}
	if _, err := NewRabbitMQCarrier(nil); !errors.Is(err, ErrInvalidHeaders) {
		t.Fatalf("NewRabbitMQCarrier(nil) error = %v, want ErrInvalidHeaders", err)
	}
	if _, err := NewKafkaCarrier(nil); !errors.Is(err, ErrInvalidHeaders) {
		t.Fatalf("NewKafkaCarrier(nil) error = %v, want ErrInvalidHeaders", err)
	}

	var nilNATSHeaders fakeNATSHeaders
	if _, err := NewNATSCarrier(nilNATSHeaders); !errors.Is(err, ErrInvalidHeaders) {
		t.Fatalf("NewNATSCarrier(typed nil) error = %v, want ErrInvalidHeaders", err)
	}
	var nilRabbitHeaders fakeRabbitMQHeaders
	if _, err := NewRabbitMQCarrier(nilRabbitHeaders); !errors.Is(err, ErrInvalidHeaders) {
		t.Fatalf("NewRabbitMQCarrier(typed nil) error = %v, want ErrInvalidHeaders", err)
	}
	var nilKafkaHeaders fakeKafkaHeaders
	if _, err := NewKafkaCarrier(nilKafkaHeaders); !errors.Is(err, ErrInvalidHeaders) {
		t.Fatalf("NewKafkaCarrier(typed nil) error = %v, want ErrInvalidHeaders", err)
	}

	natsHeaders := fakeNATSHeaders{"existing": "nats"}
	rabbitHeaders := fakeRabbitMQHeaders{"existing": "rabbit"}
	kafkaHeaders := fakeKafkaHeaders{"existing": []byte("kafka")}

	natsBefore := mapsClone(natsHeaders)
	rabbitBefore := mapsClone(rabbitHeaders)
	kafkaBefore := bytesMapClone(kafkaHeaders)

	if _, err := NewNATSCarrier(natsHeaders); err != nil {
		t.Fatalf("NewNATSCarrier() error = %v", err)
	}
	if _, err := NewRabbitMQCarrier(rabbitHeaders); err != nil {
		t.Fatalf("NewRabbitMQCarrier() error = %v", err)
	}
	if _, err := NewKafkaCarrier(kafkaHeaders); err != nil {
		t.Fatalf("NewKafkaCarrier() error = %v", err)
	}

	if !reflect.DeepEqual(natsHeaders, natsBefore) {
		t.Fatalf("NewNATSCarrier() mutated headers: got %#v, want %#v", natsHeaders, natsBefore)
	}
	if !reflect.DeepEqual(rabbitHeaders, rabbitBefore) {
		t.Fatalf("NewRabbitMQCarrier() mutated headers: got %#v, want %#v", rabbitHeaders, rabbitBefore)
	}
	if !reflect.DeepEqual(kafkaHeaders, kafkaBefore) {
		t.Fatalf("NewKafkaCarrier() mutated headers: got %#v, want %#v", kafkaHeaders, kafkaBefore)
	}
}

func injectAndExtractTenant(t *testing.T, carrier rpc.Carrier, key string) {
	t.Helper()
	ctx := tenantctx.WithTenant(context.Background(), types.Tenant{ID: "tenant-a"})
	if err := rpc.InjectTenant(ctx, carrier, key); err != nil {
		t.Fatalf("InjectTenant() error = %v", err)
	}

	id, err := rpc.ExtractTenant(carrier, key, types.TenantIDStrategyString)
	if err != nil {
		t.Fatalf("ExtractTenant() error = %v", err)
	}
	if id != "tenant-a" {
		t.Fatalf("ExtractTenant() = %q, want tenant-a", id)
	}
}

type carrierCase struct {
	name    string
	carrier rpc.Carrier
}

func carrierCases(t *testing.T) []carrierCase {
	t.Helper()
	return []carrierCase{
		{name: "NATS", carrier: mustNATSCarrier(t, fakeNATSHeaders{})},
		{name: "RabbitMQ", carrier: mustRabbitMQCarrier(t, fakeRabbitMQHeaders{})},
		{name: "Kafka", carrier: mustKafkaCarrier(t, fakeKafkaHeaders{})},
	}
}

func mustNATSCarrier(t *testing.T, headers NATSHeaders) NATSCarrier {
	t.Helper()
	carrier, err := NewNATSCarrier(headers)
	if err != nil {
		t.Fatalf("NewNATSCarrier() error = %v", err)
	}
	return carrier
}

func mustRabbitMQCarrier(t *testing.T, headers RabbitMQHeaders) RabbitMQCarrier {
	t.Helper()
	carrier, err := NewRabbitMQCarrier(headers)
	if err != nil {
		t.Fatalf("NewRabbitMQCarrier() error = %v", err)
	}
	return carrier
}

func mustKafkaCarrier(t *testing.T, headers KafkaHeaders) KafkaCarrier {
	t.Helper()
	carrier, err := NewKafkaCarrier(headers)
	if err != nil {
		t.Fatalf("NewKafkaCarrier() error = %v", err)
	}
	return carrier
}

type fakeNATSHeaders map[string]string

func (headers fakeNATSHeaders) Get(key string) string {
	return headers[key]
}

func (headers fakeNATSHeaders) Set(key, value string) {
	headers[key] = value
}

type fakeRabbitMQHeaders map[string]string

func (headers fakeRabbitMQHeaders) GetString(key string) (string, bool) {
	value, ok := headers[key]
	return value, ok
}

func (headers fakeRabbitMQHeaders) SetString(key, value string) {
	headers[key] = value
}

type fakeKafkaHeaders map[string][]byte

func (headers fakeKafkaHeaders) GetBytes(key string) ([]byte, bool) {
	value, ok := headers[key]
	return value, ok
}

func (headers fakeKafkaHeaders) SetBytes(key string, value []byte) {
	headers[key] = append([]byte(nil), value...)
}

func mapsClone[T ~map[string]string](headers T) T {
	clone := make(T, len(headers))
	for key, value := range headers {
		clone[key] = value
	}
	return clone
}

func bytesMapClone(headers fakeKafkaHeaders) fakeKafkaHeaders {
	clone := make(fakeKafkaHeaders, len(headers))
	for key, value := range headers {
		clone[key] = append([]byte(nil), value...)
	}
	return clone
}

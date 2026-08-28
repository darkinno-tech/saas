package mq

import (
	"reflect"

	"github.com/im10furry/saas/rpc"
)

// NATSHeaders is the host-owned NATS message-header wrapper used by
// NATSCarrier. It intentionally mirrors only the string operations required
// for tenant metadata propagation.
type NATSHeaders interface {
	Get(key string) string
	Set(key, value string)
}

// RabbitMQHeaders is the host-owned RabbitMQ message-header wrapper used by
// RabbitMQCarrier. It keeps the host's explicit string-presence contract.
type RabbitMQHeaders interface {
	GetString(key string) (string, bool)
	SetString(key, value string)
}

// KafkaHeaders is the host-owned Kafka record-header wrapper used by
// KafkaCarrier. It intentionally exposes byte values because Kafka headers
// are binary metadata.
type KafkaHeaders interface {
	GetBytes(key string) ([]byte, bool)
	SetBytes(key string, value []byte)
}

// NATSCarrier adapts NATSHeaders to rpc.Carrier.
type NATSCarrier struct {
	headers NATSHeaders
}

// NewNATSCarrier creates an rpc.Carrier backed by NATS message headers.
func NewNATSCarrier(headers NATSHeaders) (NATSCarrier, error) {
	if isNil(headers) {
		return NATSCarrier{}, ErrInvalidHeaders
	}
	return NATSCarrier{headers: headers}, nil
}

// Get returns a NATS header value. NATS cannot distinguish an empty value
// from an absent value through this minimal interface, so empty values are
// treated as absent.
func (carrier NATSCarrier) Get(key string) (string, bool) {
	value := carrier.headers.Get(key)
	return value, value != ""
}

// Set stores a tenant metadata value in the NATS headers.
func (carrier NATSCarrier) Set(key string, value string) {
	carrier.headers.Set(key, value)
}

// RabbitMQCarrier adapts RabbitMQHeaders to rpc.Carrier.
type RabbitMQCarrier struct {
	headers RabbitMQHeaders
}

// NewRabbitMQCarrier creates an rpc.Carrier backed by RabbitMQ message
// headers.
func NewRabbitMQCarrier(headers RabbitMQHeaders) (RabbitMQCarrier, error) {
	if isNil(headers) {
		return RabbitMQCarrier{}, ErrInvalidHeaders
	}
	return RabbitMQCarrier{headers: headers}, nil
}

// Get returns a RabbitMQ string header using the host wrapper's presence
// contract.
func (carrier RabbitMQCarrier) Get(key string) (string, bool) {
	return carrier.headers.GetString(key)
}

// Set stores a tenant metadata value in the RabbitMQ headers.
func (carrier RabbitMQCarrier) Set(key string, value string) {
	carrier.headers.SetString(key, value)
}

// KafkaCarrier adapts KafkaHeaders to rpc.Carrier.
type KafkaCarrier struct {
	headers KafkaHeaders
}

// NewKafkaCarrier creates an rpc.Carrier backed by Kafka record headers.
func NewKafkaCarrier(headers KafkaHeaders) (KafkaCarrier, error) {
	if isNil(headers) {
		return KafkaCarrier{}, ErrInvalidHeaders
	}
	return KafkaCarrier{headers: headers}, nil
}

// Get converts a Kafka byte header to a string. Missing and empty byte
// headers are treated as absent tenant metadata.
func (carrier KafkaCarrier) Get(key string) (string, bool) {
	value, ok := carrier.headers.GetBytes(key)
	if !ok || len(value) == 0 {
		return "", false
	}
	return string(value), true
}

// Set converts a tenant metadata string to a Kafka byte header.
func (carrier KafkaCarrier) Set(key string, value string) {
	carrier.headers.SetBytes(key, []byte(value))
}

func isNil(value any) bool {
	if value == nil {
		return true
	}

	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

var (
	_ rpc.Carrier = NATSCarrier{}
	_ rpc.Carrier = RabbitMQCarrier{}
	_ rpc.Carrier = KafkaCarrier{}
)

package grpcsaas

import (
	"context"

	tenantctx "github.com/im10furry/saas/core/context"
	"github.com/im10furry/saas/core/store"
	"github.com/im10furry/saas/core/types"
	"github.com/im10furry/saas/deployment"
	baserpc "github.com/im10furry/saas/rpc"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// Config controls gRPC tenant metadata extraction.
type Config struct {
	MetadataKey        string
	Strategy           types.TenantIDStrategy
	DeploymentResolver deployment.Resolver
}

// Option configures gRPC interceptors.
type Option func(*Config)

// WithMetadataKey overrides the incoming metadata key.
func WithMetadataKey(key string) Option {
	return func(config *Config) {
		config.MetadataKey = key
	}
}

// WithTenantIDStrategy overrides tenant ID parsing strategy.
func WithTenantIDStrategy(strategy types.TenantIDStrategy) Option {
	return func(config *Config) {
		config.Strategy = strategy
	}
}

// WithDeploymentResolver resolves a tenant's current deployment unit after
// tenant lookup succeeds. Passing nil leaves deployment resolution disabled.
func WithDeploymentResolver(resolver deployment.Resolver) Option {
	return func(config *Config) {
		config.DeploymentResolver = resolver
	}
}

// TenantUnaryServerInterceptor resolves tenant metadata for unary RPCs.
func TenantUnaryServerInterceptor(store store.Store, opts ...Option) grpc.UnaryServerInterceptor {
	config := newConfig(opts...)
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		tenantCtx, err := tenantContext(ctx, store, config)
		if err != nil {
			return nil, err
		}
		return handler(tenantCtx, req)
	}
}

// TenantStreamServerInterceptor resolves tenant metadata for streaming RPCs.
func TenantStreamServerInterceptor(store store.Store, opts ...Option) grpc.StreamServerInterceptor {
	config := newConfig(opts...)
	return func(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		tenantCtx, err := tenantContext(stream.Context(), store, config)
		if err != nil {
			return err
		}
		return handler(srv, &contextServerStream{ServerStream: stream, ctx: tenantCtx})
	}
}

// TenantStatusUnaryServerInterceptor allows only active tenants for unary RPCs.
func TenantStatusUnaryServerInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if err := checkTenantStatus(ctx); err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

// TenantStatusStreamServerInterceptor allows only active tenants for streaming RPCs.
func TenantStatusStreamServerInterceptor() grpc.StreamServerInterceptor {
	return func(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if err := checkTenantStatus(stream.Context()); err != nil {
			return err
		}
		return handler(srv, stream)
	}
}

// HostUnaryServerInterceptor allows only explicit host context for unary RPCs.
func HostUnaryServerInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if !tenantctx.IsHost(ctx) {
			return nil, status.Error(codes.PermissionDenied, "host_required")
		}
		return handler(ctx, req)
	}
}

// HostStreamServerInterceptor allows only explicit host context for streaming RPCs.
func HostStreamServerInterceptor() grpc.StreamServerInterceptor {
	return func(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if !tenantctx.IsHost(stream.Context()) {
			return status.Error(codes.PermissionDenied, "host_required")
		}
		return handler(srv, stream)
	}
}

func newConfig(opts ...Option) Config {
	config := Config{
		MetadataKey: baserpc.DefaultTenantMetadataKey,
		Strategy:    types.TenantIDStrategyString,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&config)
		}
	}
	return config
}

func tenantContext(ctx context.Context, store store.Store, config Config) (context.Context, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "tenant_required")
	}

	tenantID, err := baserpc.ExtractTenant(metadataCarrier{md: md}, config.MetadataKey, config.Strategy)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "tenant_required")
	}

	tenant, err := store.Get(ctx, tenantID)
	if err != nil {
		return nil, status.Error(codes.PermissionDenied, "tenant_forbidden")
	}
	if tenant.Status != types.TenantStatusActive {
		return nil, status.Error(codes.PermissionDenied, "tenant_inactive")
	}
	if config.DeploymentResolver != nil {
		unit, err := config.DeploymentResolver.Resolve(ctx, tenant)
		if err != nil || unit.ID == "" || unit.Status != types.DeploymentUnitStatusActive {
			return nil, status.Error(codes.PermissionDenied, "deployment_unavailable")
		}
		return tenantctx.WithTenantDeployment(ctx, tenant, unit), nil
	}
	return tenantctx.WithTenant(ctx, tenant), nil
}

func checkTenantStatus(ctx context.Context) error {
	tenant, ok := tenantctx.FromContext(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "tenant_required")
	}
	if tenant.Status != types.TenantStatusActive {
		return status.Error(codes.PermissionDenied, "tenant_inactive")
	}
	return nil
}

type metadataCarrier struct {
	md metadata.MD
}

func (carrier metadataCarrier) Get(key string) (string, bool) {
	values := carrier.md.Get(key)
	if len(values) == 0 {
		return "", false
	}
	return values[0], true
}

func (carrier metadataCarrier) Set(key string, value string) {
	carrier.md.Set(key, value)
}

type contextServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (stream *contextServerStream) Context() context.Context {
	return stream.ctx
}

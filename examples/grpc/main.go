package main

import (
	"context"
	"fmt"
	"log"

	tenantctx "github.com/darkinno-tech/saas/core/context"
	"github.com/darkinno-tech/saas/core/store"
	"github.com/darkinno-tech/saas/core/types"
	baserpc "github.com/darkinno-tech/saas/rpc"
	grpcsaas "github.com/darkinno-tech/saas/rpc/grpc"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

func main() {
	ctx := context.Background()
	tenants := store.NewMemoryStore()
	if err := tenants.Create(ctx, types.Tenant{
		ID:     "tenant-a",
		Name:   "Tenant A",
		Status: types.TenantStatusActive,
	}); err != nil {
		log.Fatal(err)
	}

	incoming := metadata.NewIncomingContext(ctx, metadata.Pairs(
		baserpc.DefaultTenantMetadataKey,
		"tenant-a",
	))

	interceptor := grpcsaas.TenantUnaryServerInterceptor(tenants)
	response, err := interceptor(
		incoming,
		"list orders request",
		&grpc.UnaryServerInfo{FullMethod: "/orders.OrderService/List"},
		func(ctx context.Context, req any) (any, error) {
			tenant, ok := tenantctx.FromContext(ctx)
			if !ok {
				return nil, fmt.Errorf("tenant missing from context")
			}
			return tenant.ID.String(), nil
		},
	)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println(response)
}

# SaaS

[EN](README.md) | [中文](README.zh-CN.md)

[![Go Reference](https://pkg.go.dev/badge/github.com/im10furry/saas.svg)](https://pkg.go.dev/github.com/im10furry/saas)
[![CI](https://github.com/im10furry/saas/actions/workflows/ci.yml/badge.svg)](https://github.com/im10furry/saas/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

SaaS is a production-oriented, ORM-independent Go toolkit for shared-database, shared-schema multi-tenant products. It makes the required `tenant_id` isolation boundary explicit and pairs it with the SaaS lifecycle components needed to operate tenants.

It provides tenant context and resolution, data guards, web/RPC middleware, tenant metadata storage, plans, subscriptions, quotas, feature flags, onboarding, identity links, RBAC, audit, and notifications. Every tenant-owned row carries `tenant_id`, and adapters derive the active tenant from `context.Context`.

## Scope

- **Only** shared-database, shared-schema isolation: tenant-owned rows use the same tables and require a `tenant_id` boundary.
- Host-wide access only through explicit host context.
- Logical tenant-to-deployment-unit mapping with host-defined placement policy and controlled moves.
- Built-in sqlx helpers plus optional GORM and Ent adapters for tenant-aware data access.
- Built-in HTTP middleware plus optional Gin, Echo, Fiber, Kratos, and gRPC integrations.
- Tenant lifecycle, plans, subscriptions, quotas, features, post-auth identity links, RBAC, audit, users, and notifications.

SaaS does not implement database-per-tenant, schema-per-tenant, or hybrid isolation. It does not provision tenant databases or schemas, route tenant connections, or switch schemas at runtime. Applications that need those models must provide that layer separately or use a different isolation solution.
Deployment mapping is a control-plane directory only; the host still owns physical
routing, connection selection, and data movement.
Optional integrations are separate Go modules, so the core toolkit does not pull
in framework, ORM, Redis, OIDC, OpenTelemetry, or provider-SDK dependencies.

## Architecture

See [docs/architecture.md](docs/architecture.md) for the host-integration,
tenant-boundary, data-isolation, and external-adapter diagram.
For logical placement and regional-residency integration, see
[docs/deployment.md](docs/deployment.md).

## Migration from GoTenancy

SaaS v0.2.0 is a breaking rename. The module path is now
`github.com/im10furry/saas`, and lifecycle packages are top-level paths such as
`github.com/im10furry/saas/tenant`. See the [v0.2 migration guide](docs/migration-v0.2.md)
before upgrading an existing application.

## Requirements

- Go `1.22+` for the root module.
- Optional integrations have their own minimum Go versions; Redis and OIDC
  require Go `1.24+`.

See [docs/modules.md](docs/modules.md) for the complete compatibility matrix.

## Install

```bash
# Core toolkit only (Go 1.22+).
go mod init your-app
go get github.com/im10furry/saas@v0.3.0

# Add only the integration your application uses; this example adds GORM.
go get github.com/im10furry/saas/data/gorm@v0.3.0
```

The root module does not download optional adapters. See
[docs/modules.md](docs/modules.md) for every installable module and its Go
version requirement.

## Complete Example

This copy-paste example uses the optional GORM module. It creates an in-memory
tenant, installs the GORM plugin, runs a tenant-scoped query in GORM DryRun
mode, and prints the generated SQL. It does not require a live database.

```go
package main

import (
	"context"
	"fmt"
	"log"

	tenantctx "github.com/im10furry/saas/core/context"
	"github.com/im10furry/saas/core/store"
	"github.com/im10furry/saas/core/types"
	gormtenant "github.com/im10furry/saas/data/gorm"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

type Order struct {
	ID       uint
	TenantID string `gorm:"column:tenant_id"`
	Number   string `gorm:"column:number"`
}

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

	tenant, err := tenants.Get(ctx, "tenant-a")
	if err != nil {
		log.Fatal(err)
	}
	ctx = tenantctx.WithTenant(ctx, tenant)

	db, err := gorm.Open(mysql.New(mysql.Config{
		DSN:                       "user:pass@tcp(localhost:3306)/app?parseTime=true",
		SkipInitializeWithVersion: true,
	}), &gorm.Config{
		DryRun:                 true,
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
	})
	if err != nil {
		log.Fatal(err)
	}
	if err := db.Use(gormtenant.New(gormtenant.Config{})); err != nil {
		log.Fatal(err)
	}

	var orders []Order
	result := db.WithContext(ctx).Find(&orders)
	if result.Error != nil {
		log.Fatal(result.Error)
	}

	fmt.Println(result.Statement.SQL.String())
	fmt.Println(result.Statement.Vars)
}
```

## Adoption Examples

Run the examples from the repository root. Each example owns a small Go module:

```bash
go -C examples/quickstart run .
go -C examples/gin-gorm run .
go -C examples/grpc run .
go -C examples/ent run .
go -C examples/http-servemux run .
```

- [examples/quickstart](examples/quickstart): minimal GORM create flow.
- [examples/gin-gorm](examples/gin-gorm): Gin header resolver, tenant store validation, request context injection, and GORM query guard.
- [examples/grpc](examples/grpc): unary gRPC interceptor that resolves tenant metadata and injects tenant context.
- [examples/ent](examples/ent): Ent query and mutation filters using the storage-level interfaces generated builders expose.
- [examples/http-servemux](examples/http-servemux): standard-library `http.ServeMux` route with header resolution, active-tenant validation, request-context injection, and GORM DryRun isolation.

## Standard-library `http.ServeMux`

The complete [ServeMux example](examples/http-servemux) uses `net/http` for
routing without a third-party HTTP framework. It registers `GET /orders` on
`http.NewServeMux`, wraps that mux with
`web/http.TenantMiddleware`, and runs an in-process request rather than opening
a listener or database connection.

For a request with the default `x-tenant-id` header, its integration path is:

1. `resolver.NewHeaderContrib` resolves the tenant ID from the request header.
2. `web/http.TenantMiddleware` loads the tenant through `core/store.Store` and
   accepts only an active tenant.
3. The middleware attaches that tenant with `tenantctx.WithTenant` to the
   request's `context.Context` before it calls the route handler.
4. The handler passes the request context to GORM; the tenant plugin adds the
   `tenant_id` predicate to the generated query.

The example prints the success response with the tenant ID, SQL, and variables.
It also demonstrates the standard failure responses for a missing header
(`401 tenant_required`), an unknown tenant (`403 tenant_forbidden`), and a
suspended tenant (`403 tenant_inactive`).

## Message-queue tenant propagation

`rpc/mq` adapts host-provided message headers to the existing framework-neutral
`rpc.Carrier` interface without importing a broker SDK.

| Broker | Header interface | `rpc.Carrier` adapter |
|---|---|---|
| NATS | `mq.NATSHeaders` | `mq.NATSCarrier` via `mq.NewNATSCarrier` |
| RabbitMQ | `mq.RabbitMQHeaders` | `mq.RabbitMQCarrier` via `mq.NewRabbitMQCarrier` |
| Kafka | `mq.KafkaHeaders` | `mq.KafkaCarrier` via `mq.NewKafkaCarrier` |

On the producer, adapt the message headers and inject the tenant that is
already present in the application context. This NATS-shaped call applies
equally to the RabbitMQ and Kafka constructors; an empty key selects the
default `x-tenant-id` metadata key.

```go
func injectNATSTenant(ctx context.Context, headers mq.NATSHeaders) error {
	carrier, err := mq.NewNATSCarrier(headers)
	if err != nil {
		return err
	}
	return rpc.InjectTenant(ctx, carrier, "")
}
```

On the consumer, header extraction is only the first step. The host must load
and validate the tenant before it invokes tenant-scoped work:

```go
func tenantMessageContext(ctx context.Context, headers mq.NATSHeaders, tenants store.Store) (context.Context, bool) {
	carrier, err := mq.NewNATSCarrier(headers)
	if err != nil {
		return nil, false
	}
	tenantID, err := rpc.ExtractTenant(carrier, "", types.TenantIDStrategyString)
	if err != nil {
		return nil, false
	}
	tenant, err := tenants.Get(ctx, tenantID)
	if err != nil || tenant.Status != types.TenantStatusActive {
		return nil, false
	}
	return tenantctx.WithTenant(ctx, tenant), true
}
```

The adapters transport metadata only. They do not establish NATS, RabbitMQ, or
Kafka client connections; publish or consume messages; acknowledge messages;
apply retry or dead-letter policy; or validate that a tenant exists and is
active. Those responsibilities remain with the host application.

## Common Patterns

Register the GORM plugin once on startup:

```go
if err := db.Use(gormtenant.New(gormtenant.Config{})); err != nil {
	log.Fatal(err)
}
```

Resolve tenants at the edge, then pass `context.Context` through application and data layers:

```go
tenantResolver := resolver.NewComposite(
	resolver.NewHeaderContrib("", types.TenantIDStrategyString),
)
router.Use(ginsaas.TenantMiddleware(tenantResolver, tenants))
```

Filter Ent queries before execution:

```go
query := client.Order.Query()
if err := enttenant.FilterQuery(ctx, query, enttenant.Config{}); err != nil {
	return err
}
orders, err := query.All(ctx)
```

Register the Ent mutation hook with generated clients:

```go
client.Use(enttenant.Hook(enttenant.Config{}))
```

Protect gRPC handlers with tenant metadata:

```go
server := grpc.NewServer(
	grpc.UnaryInterceptor(grpcsaas.TenantUnaryServerInterceptor(tenants)),
)
```

Use explicit host context for host-wide operations:

```go
ctx := tenantctx.WithHost(context.Background())
```

## Packages

- `core/types`: tenant and deployment-unit IDs, metadata, status, and side types.
- `core/context`: tenant, deployment-unit, and host context; detach and switch.
- `core/resolver`: header, cookie, query, domain, token-claim, and composite resolvers.
- `core/store`: memory store, paginated list filters, memory cache, cached store decorator, and `database/sql` store.
- `data`: ORM-independent tenant filter condition.
- `data/gorm`: optional Go 1.22 GORM plugin, guard suite, host-only `SafeRaw`/`SafeExec`, `BulkCreate`, and delete APIs.
- `data/ent`: optional Go 1.23 Ent selector predicate, query filter, mutation filter, and hook APIs.
- `data/sqlx`: tenant-filtered APIs for simple single-table SELECT/UPDATE/DELETE statements.
- `tenant`: tenant lifecycle state machine.
- `plan`: plan Store, memory implementation, and `database/sql` SQLStore.
- `subscription`: subscription lifecycle, renewal, expiration, grace-period handling, billing hook, Store, memory implementation, and `database/sql` SQLStore.
- `quota`: quota checking, atomic consumption, reset, memory implementation, and `database/sql` SQLStore.
- `feature`: plan defaults, tenant-level feature overrides, memory implementation, and `database/sql` SQLStore.
- `deployment`: logical tenant placement directory, memory and `database/sql` stores, host policy/audit hooks, and controlled moves; physical routing remains host-owned.
- `onboarding`: tenant onboarding flow across tenant, plan, subscription, feature, quota, audit, notification, and optional deployment-assignment services.
- `biz/identity`: post-auth tenant user mapping for verified external identity assertions, with memory and `database/sql` stores.
- `biz/identity/oidc`: optional Go 1.24 OIDC authorization-code bridge with PKCE, state, nonce, ID-token verification, one-time login state storage, SQL-backed login state storage, and assertion output.
- `web/http`: core tenant middleware and guards for `net/http`, with optional deployment resolution; Gin, Echo, Fiber, and Kratos are optional modules.
- `rpc/grpc`: optional Go 1.23 gRPC unary and stream tenant interceptors with optional deployment resolution.
- `migration`: tenant column and index planning.
- `cache`: core tenant-scoped cache wrapper and memory adapters; `cache/redis` is an optional Go 1.24 adapter.
- `obs`: core tenant observability fields, deployment-unit IDs, redaction, and `slog` helpers; `obs/otel` is an optional Go 1.23 module.
- `biz/*`: identity, user, RBAC, audit, and notification modules with memory stores, SQL stores where persistence is part of the module contract, SMTP/Resend/webhook delivery, channel routing, fanout, retry, and timeout helpers. SES is optional.

## Post-Auth Identity Mapping

`biz/identity` maps verified external identities into tenant users and memberships. The optional Go 1.24 `biz/identity/oidc` module adds a standard OIDC authorization-code bridge for callback processing, one-time login state, and ID-token verification. It is still not a full IAM platform: application sessions, account screens, Magic Link delivery, SAML validation, MFA, and WebAuthn remain application or IdP responsibilities.

```go
oidcClient, err := oidc.New(ctx, oidc.Config{
	Provider:    identity.GoogleOIDC(),
	ClientID:    "client-id",
	RedirectURL: "https://app.example.com/auth/callback",
})

loginStore := oidc.NewMemoryLoginStore(10 * time.Minute)
login, err := oidcClient.BeginLogin(ctx, loginStore, oidc.LoginRequest{
	TenantID: "tenant-a",
	Roles:    []string{"member"},
})
result, err := oidcClient.HandleLoginCallback(ctx, request, loginStore)

users := user.NewMemoryService()
identityService := identity.NewService(users, identity.WithProviders(identity.GoogleOIDC()))
session, err := identityService.Authenticate(ctx, result.Assertion)
```

Redirect the browser to `login.URL`. `MemoryLoginStore` is process-local and consumes states once; multi-instance deployments should implement `LoginStore` on top of their secure session or shared cache layer.

## Verification

```bash
# Core module (Go 1.22+).
go test ./...
go vet ./...
go test -race ./...

# Run an optional module only when your application uses it.
go -C cache/redis test ./...
```

The CI workflow runs every published optional module with its minimum supported
Go version; see [docs/modules.md](docs/modules.md) for the complete matrix.

On Windows, `go test -race` requires cgo and a C compiler. Without local cgo, run race tests in Docker:

```bash
docker run --rm -v "${PWD}:/workspace" -w /workspace -e CGO_ENABLED=1 -e GOFLAGS=-mod=readonly golang:1.24 go test -race ./...
```

### Disposable integration, coverage, and chaos

The PowerShell runners start a local disposable Docker Compose environment for MySQL, PostgreSQL, Redis, and (for chaos) Toxiproxy. They create and drop test tables and volumes, so never point their DSNs at shared or production services.

```powershell
# Windows PowerShell; PowerShell 7 users may substitute pwsh.
# SQL-contract coverage from the disposable MySQL/PostgreSQL tests.
$sqlProfile = Join-Path $env:TEMP 'saas-sql-contract-coverage.out'
powershell.exe -NoProfile -ExecutionPolicy Bypass -File tests/run-integration.ps1 -CoverageProfile $sqlProfile

$profile = Join-Path $env:TEMP 'saas-coverage.out' # merged core + optional + database profiles
powershell.exe -NoProfile -ExecutionPolicy Bypass -File tests/run-coverage.ps1 -Output $profile -IntegrationProfile $sqlProfile
powershell.exe -NoProfile -ExecutionPolicy Bypass -File tests/check-coverage.ps1 -Profile $profile -Minimum 85
Remove-Item -LiteralPath $sqlProfile, $profile, "$profile.txt" -Force -ErrorAction SilentlyContinue

powershell.exe -NoProfile -ExecutionPolicy Bypass -File tests/run-chaos.ps1
```

For production Redis, configure TLS, timeouts, retry limits, and OpenTelemetry on the `go-redis` client before passing it to `redis.New` from the optional `cache/redis` module.

## Project Layout

```text
core/          Tenant context, resolver, store, and types
deployment/    Logical tenant-to-deployment-unit directory and moves
data/          Data filtering contracts and adapters
tenant/        Tenant lifecycle module
plan/          Plan metadata and storage
subscription/  Subscription lifecycle and billing hooks
quota/         Quota checking and consumption
feature/       Feature defaults and tenant overrides
onboarding/    Cross-module tenant onboarding
web/           Web framework and net/http integration
migration/     Tenant schema migration planning
cache/         Tenant-aware cache abstractions
rpc/           RPC metadata propagation
obs/           Observability fields, redaction, and slog; OpenTelemetry is optional
biz/           Identity, user, RBAC, audit, and notification modules
examples/      Runnable examples
tests/         Security, cache, concurrency, and local-only DB integration tests
docs/          API, security, and compatibility notes
```

## Compatibility

See [docs/modules.md](docs/modules.md) for module installation, Go-version
requirements, release tags, and migration steps. See
[docs/compatibility.md](docs/compatibility.md) for broader compatibility notes.

## License

[Apache License 2.0](LICENSE)

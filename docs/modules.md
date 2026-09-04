# Core and Optional Modules

[EN](modules.md) | [中文](modules.zh-CN.md)

SaaS v0.3.0 separates the runtime-dependency-free tenant toolkit from integrations
that are only useful to some applications. Importing the root module no longer
downloads ORM, web-framework, Redis, OIDC, OpenTelemetry, or AWS SDK
dependencies. Add an integration module only when application code imports it.

## Core module

The root module is `github.com/darkinno-tech/saas` and supports Go `1.22+`.
It contains the tenant boundary, lifecycle services, `database/sql` and sqlx
helpers, `net/http` middleware, in-memory cache, `slog` helpers, and business
modules that do not need an external provider SDK.

```bash
go get github.com/darkinno-tech/saas@v0.3.3
```

### Host-integrated domain packages

[`biz/commission`](../biz/commission) is a root-module, host-integrated domain
package that applications opt into by composition. It has no payment SDK and
does not execute payouts or migrations; the host supplies normalized business
facts (such as paid/refund events), chooses when to record or reverse earnings,
and optionally supplies a settlement adapter. Every `Service` command requires
a host `Authorizer` adapter; callers must not treat the supplied `Actor` or
`Actor.Host` fields as credentials. Settlement adapters return an explicit
`pending`, `settled`, or `rejected` outcome, so only a verified terminal result
changes a submitted batch.

Install the root module first, then add each optional module at a published
tag. Nested modules may have independent patch versions; their `go.mod` files
pin the compatible root release. For example, the currently published GORM
adapter is compatible with root `v0.3.3`:

```bash
go get github.com/darkinno-tech/saas@v0.3.3
go get github.com/darkinno-tech/saas/data/gorm@v0.3.1
```

## Optional integration modules

Each path below is an independently versioned Go module. The Go version is the
minimum supported toolchain for that module; importing a higher-tier module
raises the requirement for the consuming application accordingly.

| Module path | Minimum Go | Purpose |
|---|---:|---|
| `github.com/darkinno-tech/saas/data/gorm` | 1.22 | GORM v2 tenant plugin and guards |
| `github.com/darkinno-tech/saas/web/gin` | 1.22 | Gin middleware and guards |
| `github.com/darkinno-tech/saas/web/fiber` | 1.22 | Fiber middleware and guards |
| `github.com/darkinno-tech/saas/web/kratos` | 1.22 | Kratos middleware and guards |
| `github.com/darkinno-tech/saas/data/ent` | 1.23 | Ent predicates, filters, and hooks |
| `github.com/darkinno-tech/saas/web/echo` | 1.23 | Echo middleware and guards |
| `github.com/darkinno-tech/saas/rpc/grpc` | 1.23 | gRPC unary and stream interceptors |
| `github.com/darkinno-tech/saas/obs/otel` | 1.23 | OpenTelemetry tracing helpers |
| `github.com/darkinno-tech/saas/biz/notification/ses` | 1.23 | Amazon SES v2 notifier |
| `github.com/darkinno-tech/saas/cache/redis` | 1.24 | `go-redis/v9` cache adapter |
| `github.com/darkinno-tech/saas/biz/identity/oidc` | 1.24 | OIDC authorization-code bridge |

Install the root module before adding an integration, and use a published
version for that integration. For example:

```bash
go get github.com/darkinno-tech/saas@v0.3.3
go get github.com/darkinno-tech/saas/cache/redis@v0.3.1
go get github.com/darkinno-tech/saas/biz/identity/oidc@v0.3.1
```

This is intentional compatibility isolation: Go 1.22 and 1.23 applications
can adopt the root toolkit without being forced to download or compile the Go
1.24 Redis or OIDC dependency chains.

## Example modules

Runnable examples are also separate modules, so they do not contribute to an
application's dependency graph. Run them from the repository root with `go -C`:

| Example module | Minimum Go | Command |
|---|---:|---|
| `github.com/darkinno-tech/saas/examples/quickstart` | 1.22 | `go -C examples/quickstart run .` |
| `github.com/darkinno-tech/saas/examples/gin-gorm` | 1.22 | `go -C examples/gin-gorm run .` |
| `github.com/darkinno-tech/saas/examples/grpc` | 1.23 | `go -C examples/grpc run .` |
| `github.com/darkinno-tech/saas/examples/ent` | 1.23 | `go -C examples/ent run .` |

## Release and tag rules

Go requires a directory prefix in a tag for every nested module. A nested
module must require a root version that is already published; do not assume all
modules use the same patch version:

| Module | Current published tag |
|---|---|
| Root `github.com/darkinno-tech/saas` | `v0.3.3` |
| `github.com/darkinno-tech/saas/data/gorm` | `data/gorm/v0.3.1` |
| `github.com/darkinno-tech/saas/cache/redis` | `cache/redis/v0.3.1` |
| Any new nested module at `<path>` | `<path>/vX.Y.Z` |

The same rule applies to example modules. Before publishing an adapter tag,
verify it from an isolated consumer module with `go get <module-path>@<tag>`;
local `replace` directives do not participate in downstream resolution.

## Migrating from the pre-split API

The root APIs remain in the root module. Only optional surfaces move to their
own module paths or require an explicit module installation.

| Before v0.3.0 | After v0.3.0 |
|---|---|
| `cache.NewRedis`, `cache.NewRedisFromOptions`, `cache.NewRedisFromClusterOptions`, `cache.NewRedisFromURL` from `github.com/darkinno-tech/saas/cache` | Import `github.com/darkinno-tech/saas/cache/redis`; use `redis.New`, `redis.NewFromOptions`, `redis.NewFromClusterOptions`, and `redis.NewFromURL`. |
| `obs.NewTracer`, `obs.SpanAttributes`, `obs.AddSpanAttributes`, `obs.StartSpan`, `obs.RecordSpanError` from `github.com/darkinno-tech/saas/obs` | Import `github.com/darkinno-tech/saas/obs/otel`; use the same helper names through the `otel` package. |
| `notification.NewSESNotifier` and the `notification.SES*` types | Import `github.com/darkinno-tech/saas/biz/notification/ses`; use `ses.NewSESNotifier` and `ses.SES*` types. |
| Root-owned OIDC integration | Add `github.com/darkinno-tech/saas/biz/identity/oidc@v0.3.1` explicitly. The package import path and name remain `oidc`; `biz/identity` remains the core post-auth identity mapping package. |

For example, migrate a Redis adapter import as follows:

```go
// Before
import "github.com/darkinno-tech/saas/cache"

client, err := cache.NewRedis(redisClient)

// After
import cacheredis "github.com/darkinno-tech/saas/cache/redis"

client, err := cacheredis.New(redisClient)
```

Run `go mod tidy` and the tests for your application after changing imports.

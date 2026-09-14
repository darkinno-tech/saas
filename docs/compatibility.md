# Compatibility

[EN](compatibility.md) | [中文](compatibility.zh-CN.md)

## Go

- Root module language version: Go `1.22`. `go.mod` records this as `go 1.22.0`.
- Optional adapters declare their own minimum, so importing one raises the
  consumer's requirement: Go `1.22` for `web/gin`, `web/fiber`, and
  `web/kratos`; Go `1.23` for `data/ent`, `web/echo`, `obs/otel`, and
  `biz/notification/ses`; Go `1.24` for `cache/redis` and
  `biz/identity/oidc`; Go `1.25` for `data/gorm` and `rpc/grpc`.
- CI test jobs cover Go `1.22.x` through Go `1.26.x` across those tiers. Lint
  and vulnerability scans run on a patched Go `1.26.5+` toolchain, and the
  `coverage` and `integration` gates run on Go `1.25.x` because they build the
  Go 1.25 adapters.

Go `1.23` support was dropped for the OIDC path because the patched
`github.com/go-jose/go-jose/v4` release requires Go `1.24.0`.

The module should not require Go `1.25+` dependencies without an explicit
compatibility decision. `data/gorm` and `rpc/grpc` are the recorded exception:

- GO-2026-5970 is only fixed by `golang.org/x/text` v0.39.0, which requires
  `gorm.io/gorm` consumers to move to that release.
- GO-2026-6061 is only fixed by `google.golang.org/grpc` v1.82.1; the newest
  Go 1.24-compatible gRPC release (`v1.80.0`) is still affected.

Both patched releases declare `go 1.25.0` and have no earlier fix, so keeping
those two adapters on Go 1.22 or 1.23 would mean publishing a
known-vulnerable dependency graph. Applications that must stay on Go 1.22 -
1.24 can keep using the root toolkit and the lower-tier adapters; adopting
`data/gorm` or `rpc/grpc` now raises the consumer's minimum to Go 1.25.

## Isolation Model

SaaS supports only shared-database, shared-schema isolation with a required `tenant_id` boundary on tenant-owned rows.

Database-per-tenant, schema-per-tenant, and hybrid isolation models are not part of the current API. The module does not provision tenant databases or schemas, route tenant connections, or switch schemas at runtime.

The `deployment` package provides a logical tenant-to-unit directory, not a
physical isolation topology. It does not change the shared-database contract or
select connections; hosts own regional routing and data movement. See
[Deployment Units](deployment.md).

## Adapters

| Adapter | Dependency |
|---|---|
| GORM v2 | `gorm.io/gorm` v1.31.2 |
| Ent | `entgo.io/ent` v0.14.1 |
| Gin | `github.com/gin-gonic/gin` v1.9.1 |
| Echo | `github.com/labstack/echo/v4` v4.13.4 |
| Fiber | `github.com/gofiber/fiber/v2` v2.52.13 |
| Kratos | `github.com/go-kratos/kratos/v2` v2.9.2 |
| gRPC | `google.golang.org/grpc` v1.82.1 |
| OIDC | `github.com/coreos/go-oidc/v3` v3.15.0 and `golang.org/x/oauth2` v0.30.0 |
| Redis cache | `github.com/redis/go-redis/v9` v9.21.0 |

`core/` remains free of GORM, Ent, sqlx, Redis, and web-framework imports.

## SQLStore

`core/store.SQLStore` supports:

- MySQL/SQLite placeholders: `?`
- PostgreSQL placeholders: `$1`, `$2`, ...

Use `WithSQLDialect(SQLDialectPostgres)` for PostgreSQL.

The disposable integration runner owns the local MySQL, PostgreSQL, and Redis endpoints used by SQLStore, GORM, and Redis cache tests. It must never be configured with shared or production DSNs because the database tests create and drop tables.

## CI and resilience

Pull requests run the existing Go version matrix, lint, vulnerability scan, and example smoke tests, plus two dedicated gates:

- `coverage` creates an atomic root-module profile, enforces at least 85.0% statements, and uploads the profile and summary artifact.
- `integration (mysql, postgres, redis)` runs the disposable Compose-backed MySQL, PostgreSQL, GORM, and Redis contracts.

The separate `resilience` workflow runs every Wednesday at 03:17 UTC and on manual dispatch. It executes bounded native fuzz targets and the deterministic Toxiproxy fault/recovery contracts; these longer-running checks are intentionally outside the pull-request path.

Repository branch protection must independently mark `coverage` and `integration (mysql, postgres, redis)` as required checks in GitHub settings.

## Verification

```bash
go test ./...
go vet ./...
go test -race ./...
go list -m -f '{{.Path}} {{.GoVersion}}' all
go run golang.org/x/vuln/cmd/govulncheck@v1.5.0 ./...
```

On Windows without local cgo, run race tests in Docker:

```bash
docker run --rm -v "${PWD}:/workspace" -w /workspace -e CGO_ENABLED=1 -e GOFLAGS=-mod=readonly golang:1.24 go test -race ./...
```

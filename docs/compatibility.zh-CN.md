# 兼容性

[EN](compatibility.md) | [中文](compatibility.zh-CN.md)

## Go

- 根模块语言版本：Go `1.22`。`go.mod` 将其记录为 `go 1.22.0`。
- 可选适配器各自声明最低版本，导入后会把使用方的最低要求一并抬高：`web/gin`、`web/fiber`、`web/kratos` 为 Go `1.22`；`data/ent`、`web/echo`、`obs/otel`、`biz/notification/ses` 为 Go `1.23`；`cache/redis`、`biz/identity/oidc` 为 Go `1.24`；`data/gorm`、`rpc/grpc` 为 Go `1.25`。
- CI 测试任务覆盖 Go `1.22.x` 至 Go `1.26.x` 的各层；lint 和漏洞扫描在已修复的 Go `1.26.5+` 工具链上运行；`coverage` 与 `integration` 门禁运行在 Go `1.25.x`，因为它们会编译 Go 1.25 适配器。

由于 OIDC 路径所需的已修复 `github.com/go-jose/go-jose/v4` 版本要求 Go `1.24.0`，因此不再支持 Go `1.23`。

除非作出明确的兼容性决策，模块不应引入要求 Go `1.25+` 的依赖。`data/gorm` 与 `rpc/grpc` 就是已记录的例外：

- GO-2026-5970 只有 `golang.org/x/text` v0.39.0 修复，这要求 `gorm.io/gorm` 的使用方升级到该版本。
- GO-2026-6061 只有 `google.golang.org/grpc` v1.82.1 修复；兼容 Go 1.24 的最新 gRPC 版本（`v1.80.0`）仍然受影响。

这两个补丁版本都声明 `go 1.25.0`，且没有更早的修复版本，因此把这两个适配器留在 Go 1.22 或 1.23 就等于继续发布含已知漏洞的依赖图。必须停留在 Go 1.22–1.24 的应用可以继续使用根模块和低层适配器；一旦采用 `data/gorm` 或 `rpc/grpc`，使用方的最低版本即提升到 Go 1.25。

## 隔离模型

SaaS 仅支持共享数据库、共享 Schema 隔离；租户数据必须具有 `tenant_id` 边界。

按租户独立数据库、独立 Schema 和混合隔离模型不属于当前 API 的范围。该模块不会创建租户数据库或 Schema、路由租户连接，或在运行时切换 Schema。

`deployment` 包提供逻辑的租户到单元目录，而不是物理隔离拓扑。它不会改变共享数据库契约或选择连接；区域路由和数据迁移由宿主负责。参阅[部署单元](deployment.zh-CN.md)。

## 适配器

| 适配器 | 依赖 |
|---|---|
| GORM v2 | `gorm.io/gorm` v1.31.2 |
| Ent | `entgo.io/ent` v0.14.1 |
| Gin | `github.com/gin-gonic/gin` v1.9.1 |
| Echo | `github.com/labstack/echo/v4` v4.13.4 |
| Fiber | `github.com/gofiber/fiber/v2` v2.52.13 |
| Kratos | `github.com/go-kratos/kratos/v2` v2.9.2 |
| gRPC | `google.golang.org/grpc` v1.82.1 |
| OIDC | `github.com/coreos/go-oidc/v3` v3.15.0 和 `golang.org/x/oauth2` v0.30.0 |
| Redis 缓存 | `github.com/redis/go-redis/v9` v9.21.0 |

`core/` 保持不导入 GORM、Ent、sqlx、Redis 和 web 框架。

## SQLStore

`core/store.SQLStore` 支持：

- MySQL/SQLite 占位符：`?`
- PostgreSQL 占位符：`$1`、`$2`、...

对于 PostgreSQL，请使用 `WithSQLDialect(SQLDialectPostgres)`。

一次性集成运行脚本负责 SQLStore、GORM 和 Redis 缓存测试所使用的本地 MySQL、PostgreSQL 与 Redis 端点。数据库测试会创建和删除表，因此绝不能将脚本配置为使用共享或生产 DSN。

## CI 与韧性测试

Pull Request 会运行既有的 Go 版本矩阵、lint、漏洞扫描和示例 smoke 测试，并额外包含两个独立门禁：

- `coverage` 生成根模块的原子覆盖率 profile，强制至少 85.0% statements，并上传 profile 与摘要 artifact。
- `integration (mysql, postgres, redis)` 运行基于一次性 Compose 的 MySQL、PostgreSQL、GORM 与 Redis 契约测试。

独立的 `resilience` 工作流会在每周三 03:17 UTC 运行，也可手动触发。它执行有时间上限的原生 fuzz target 和确定性的 Toxiproxy 故障/恢复契约；这些耗时更长的检查有意不放在 Pull Request 路径中。

仓库分支保护还必须在 GitHub 设置中独立将 `coverage` 与 `integration (mysql, postgres, redis)` 设为 required checks。

## 验证

```bash
go test ./...
go vet ./...
go test -race ./...
go list -m -f '{{.Path}} {{.GoVersion}}' all
go run golang.org/x/vuln/cmd/govulncheck@v1.5.0 ./...
```

在 Windows 上如果本地没有 cgo，请在 Docker 中运行 race 测试：

```bash
docker run --rm -v "${PWD}:/workspace" -w /workspace -e CGO_ENABLED=1 -e GOFLAGS=-mod=readonly golang:1.24 go test -race ./...
```

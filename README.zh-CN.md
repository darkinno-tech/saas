# SaaS

[EN](README.md) | [中文](README.zh-CN.md)

[![Go Reference](https://pkg.go.dev/badge/github.com/darkinno-tech/saas.svg)](https://pkg.go.dev/github.com/darkinno-tech/saas)
[![CI](https://github.com/darkinno-tech/saas/actions/workflows/ci.yml/badge.svg)](https://github.com/darkinno-tech/saas/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

SaaS 是一个面向生产环境、与 ORM 无关的 Go 多租户应用工具包，适用于构建共享数据库、共享 Schema 隔离的 SaaS 产品。它以强制 `tenant_id` 隔离边界为核心，并提供运营租户所需的 SaaS 生命周期能力。

它提供租户上下文与解析、数据防护、Web/RPC 中间件、租户元数据存储、套餐、订阅、配额、功能开关、入驻流程、身份关联、RBAC、审计和通知。每一行租户数据都带有 `tenant_id`，适配器从 `context.Context` 派生当前活跃租户。

## 范围

- **仅支持**共享数据库、共享 Schema 隔离：租户数据使用同一组表，并且必须具有 `tenant_id` 边界。
- 只能通过显式主机上下文进行全局主机访问。
- 提供逻辑的租户到部署单元映射、宿主定义的放置策略和受控迁移。
- 内置 sqlx 辅助能力，以及可选的 GORM、Ent 租户感知数据访问适配器。
- 内置 HTTP 中间件，以及可选的 Gin、Echo、Fiber、Kratos 和 gRPC 集成。
- 租户生命周期、套餐、订阅、配额、功能、认证后身份关联、RBAC、审计、用户和通知。

SaaS 不实现按租户独立数据库、独立 Schema 或混合隔离；它不会创建租户数据库或 Schema、路由租户连接，或在运行时切换 Schema。需要这些模型的应用必须自行提供该层能力，或采用其他隔离方案。
部署映射仅是控制面目录；物理路由、连接选择和数据迁移仍由宿主负责。
可选集成采用独立 Go 模块，因此核心工具包不会引入框架、ORM、Redis、OIDC、OpenTelemetry 或提供商 SDK 依赖。

## 架构

有关宿主集成、租户边界、数据隔离和外部适配器图，请参阅 [docs/architecture.zh-CN.md](docs/architecture.zh-CN.md)。
有关逻辑放置和区域驻留集成，请参阅 [docs/deployment.zh-CN.md](docs/deployment.zh-CN.md)。

## 从 GoTenancy 迁移

SaaS v0.2.0 是一次破坏性改名。Go 模块路径现在是
`github.com/darkinno-tech/saas`，生命周期包也提升为顶级路径，例如
`github.com/darkinno-tech/saas/tenant`。已有应用升级前请阅读
[v0.2 迁移指南](docs/migration-v0.2.zh-CN.md)。

## 要求

- 根模块需要 Go `1.22+`。
- 可选集成有各自的最低 Go 版本；Redis 和 OIDC 需要 Go `1.24+`。

完整兼容矩阵请参阅 [docs/modules.zh-CN.md](docs/modules.zh-CN.md)。

## 安装

```bash
# 仅核心工具包（Go 1.22+）。
go mod init your-app
go get github.com/darkinno-tech/saas@v0.3.3

# 仅添加应用实际使用的集成；这里以 GORM 为例。
go get github.com/darkinno-tech/saas/data/gorm@v0.3.1
```

根模块不会下载可选适配器。全部可安装模块及其 Go 版本要求请参阅
[docs/modules.zh-CN.md](docs/modules.zh-CN.md)。

## 完整示例

此可复制粘贴的示例使用可选 GORM 模块：它会创建内存中的租户、安装 GORM 插件、在 GORM DryRun 模式下执行租户作用域查询，并打印生成的 SQL。它不需要真实数据库。

```go
package main

import (
	"context"
	"fmt"
	"log"

	tenantctx "github.com/darkinno-tech/saas/core/context"
	"github.com/darkinno-tech/saas/core/store"
	"github.com/darkinno-tech/saas/core/types"
	gormtenant "github.com/darkinno-tech/saas/data/gorm"

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

## 接入示例

请从仓库根目录运行示例。每个示例均拥有独立的 Go 模块：

```bash
go -C examples/quickstart run .
go -C examples/gin-gorm run .
go -C examples/grpc run .
go -C examples/ent run .
go -C examples/http-servemux run .
```

- [examples/quickstart](examples/quickstart)：最小化 GORM 创建流程。
- [examples/gin-gorm](examples/gin-gorm)：Gin header 解析器、租户存储校验、请求上下文注入和 GORM 查询防护。
- [examples/grpc](examples/grpc)：解析租户元数据并注入租户上下文的 unary gRPC 拦截器。
- [examples/ent](examples/ent)：使用存储层接口（由生成的 builder 暴露）的 Ent 查询和变更过滤器。
- [examples/http-servemux](examples/http-servemux)：使用标准库 `http.ServeMux` 的路由，展示 header 解析、活跃租户校验、请求上下文注入和 GORM DryRun 隔离。

## 标准库 `http.ServeMux`

完整的 [ServeMux 示例](examples/http-servemux) 在路由层只使用 `net/http`，不依赖
第三方 HTTP 框架。它在 `http.NewServeMux` 上注册 `GET /orders`，以 `web/http.TenantMiddleware` 包装
该 mux，并在进程内运行请求，不会启动监听器或连接真实数据库。

请求携带默认 `x-tenant-id` header 时，完整集成路径如下：

1. `resolver.NewHeaderContrib` 从请求 header 解析租户 ID。
2. `web/http.TenantMiddleware` 通过 `core/store.Store` 加载租户，并且只允许活跃租户。
3. 中间件在调用路由处理器之前，使用 `tenantctx.WithTenant` 将租户放入请求的 `context.Context`。
4. 处理器将请求上下文传给 GORM；租户插件会将 `tenant_id` 条件加入生成的查询。

示例会输出包含租户 ID、SQL 和变量的成功响应；同时展示缺少 header
（`401 tenant_required`）、未知租户（`403 tenant_forbidden`）和已暂停租户
（`403 tenant_inactive`）的标准失败响应。

## 消息队列租户上下文传播

`rpc/mq` 在不引入消息队列 SDK 的前提下，将宿主提供的消息 header 适配为现有的、
与框架无关的 `rpc.Carrier` 接口。

| 消息队列 | Header 接口 | `rpc.Carrier` 适配器 |
|---|---|---|
| NATS | `mq.NATSHeaders` | 通过 `mq.NewNATSCarrier` 创建的 `mq.NATSCarrier` |
| RabbitMQ | `mq.RabbitMQHeaders` | 通过 `mq.NewRabbitMQCarrier` 创建的 `mq.RabbitMQCarrier` |
| Kafka | `mq.KafkaHeaders` | 通过 `mq.NewKafkaCarrier` 创建的 `mq.KafkaCarrier` |

生产端需要先适配消息 header，再注入已经存在于应用上下文中的租户。下面以
NATS 形式说明；RabbitMQ 和 Kafka 仅需替换为各自的构造函数。传入空键会使用默认
的 `x-tenant-id` 元数据键：

```go
func injectNATSTenant(ctx context.Context, headers mq.NATSHeaders) error {
	carrier, err := mq.NewNATSCarrier(headers)
	if err != nil {
		return err
	}
	return rpc.InjectTenant(ctx, carrier, "")
}
```

消费端中，提取 header 只是第一步。宿主必须在执行租户作用域工作前加载并校验租户：

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

这些适配器只传递元数据：不会建立 NATS、RabbitMQ 或 Kafka 客户端连接，不会发布或
消费消息、确认消息、执行重试或死信策略，也不会校验租户是否存在及处于活跃状态；这些
职责仍由宿主应用承担。

## 常见模式

在启动时只注册一次 GORM 插件：

```go
if err := db.Use(gormtenant.New(gormtenant.Config{})); err != nil {
	log.Fatal(err)
}
```

在入口处解析租户，然后让 `context.Context` 贯穿应用层和数据层：

```go
tenantResolver := resolver.NewComposite(
	resolver.NewHeaderContrib("", types.TenantIDStrategyString),
)
router.Use(ginsaas.TenantMiddleware(tenantResolver, tenants))
```

在执行前过滤 Ent 查询：

```go
query := client.Order.Query()
if err := enttenant.FilterQuery(ctx, query, enttenant.Config{}); err != nil {
	return err
}
orders, err := query.All(ctx)
```

将 Ent 变更 hook 注册到生成的客户端：

```go
client.Use(enttenant.Hook(enttenant.Config{}))
```

使用租户元数据保护 gRPC handler：

```go
server := grpc.NewServer(
	grpc.UnaryInterceptor(grpcsaas.TenantUnaryServerInterceptor(tenants)),
)
```

为全局主机操作使用显式主机上下文：

```go
ctx := tenantctx.WithHost(context.Background())
```

## 包

- `core/types`：租户与部署单元 ID、元数据、状态和侧类型。
- `core/context`：租户、部署单元和主机上下文，以及 detach 和 switch。
- `core/resolver`：header、cookie、query、domain、token-claim 和组合式解析器。
- `core/store`：内存存储、分页列表过滤器、内存缓存、缓存存储装饰器以及 `database/sql` 存储。
- `data`：与 ORM 无关的租户过滤条件。
- `data/gorm`：可选的 Go 1.22 GORM 插件、防护套件、仅限主机的 `SafeRaw`/`SafeExec`、`BulkCreate` 和删除 API。
- `data/ent`：可选的 Go 1.23 Ent selector 谓词、查询过滤器、变更过滤器和 hook API。
- `data/sqlx`：用于简单单表 SELECT/UPDATE/DELETE 语句的租户过滤 API。
- `tenant`：租户生命周期状态机。
- `plan`：套餐 Store、内存实现和 `database/sql` SQLStore。
- `subscription`：订阅生命周期、续订、过期、宽限期处理、计费 hook、Store、内存实现和 `database/sql` SQLStore。
- `quota`：配额检查、原子消耗、重置、内存实现和 `database/sql` SQLStore。
- `feature`：套餐默认功能、租户级功能覆盖、内存实现和 `database/sql` SQLStore。
- `deployment`：逻辑租户放置目录、内存和 `database/sql` 存储、宿主策略/审计 hook 与受控迁移；物理路由仍由宿主负责。
- `onboarding`：涵盖租户、套餐、订阅、功能、配额、审计、通知和可选部署分配服务的租户开通流程。
- `biz/identity`：针对已验证外部身份断言的认证后租户用户映射，提供内存和 `database/sql` 存储。
- `biz/identity/oidc`：可选的 Go 1.24 OIDC 授权码桥接，支持 PKCE、state、nonce、ID Token 验证、一次性登录状态存储、SQL 支持的登录状态存储和断言输出。
- `web/http`：核心的 `net/http` 租户中间件和防护，支持可选部署解析；Gin、Echo、Fiber、Kratos 为可选模块。
- `rpc/grpc`：可选的 Go 1.23 gRPC unary 和 stream 租户拦截器，支持可选部署解析。
- `migration`：租户列和索引规划。
- `cache`：核心租户作用域缓存包装器和内存适配器；`cache/redis` 是可选的 Go 1.24 适配器。
- `obs`：核心租户可观测性字段、部署单元 ID、脱敏和 `slog` 辅助函数；`obs/otel` 是可选的 Go 1.23 模块。
- `biz/*`：身份、用户、RBAC、审计和通知模块，提供内存存储、当持久化属于模块契约时提供 SQL 存储、SMTP/Resend/webhook 投递、渠道路由、扇出、重试和超时辅助函数；SES 为可选模块。

## 认证后身份映射

`biz/identity` 将已验证的外部身份映射为租户用户和成员资格。可选的 Go 1.24 `biz/identity/oidc` 模块为回调处理、一次性登录状态和 ID Token 验证增加标准 OIDC 授权码桥接。它仍不是完整的 IAM 平台：应用会话、账户页面、Magic Link 投递、SAML 验证、MFA 和 WebAuthn 仍是应用或 IdP 的职责。

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

将浏览器重定向到 `login.URL`。`MemoryLoginStore` 是进程本地的，并且每个 state 只消费一次；多实例部署应在其安全会话或共享缓存层之上实现 `LoginStore`。

## 验证

```bash
# 核心模块（Go 1.22+）。
go test ./...
go vet ./...
go test -race ./...

# 仅在应用实际使用该集成时运行对应可选模块。
go -C cache/redis test ./...
```

CI 会使用每个已发布可选模块的最低支持 Go 版本执行测试；完整矩阵见
[docs/modules.zh-CN.md](docs/modules.zh-CN.md)。

在 Windows 上，`go test -race` 需要 cgo 和 C 编译器。如果本地没有 cgo，请在 Docker 中运行 race 测试：

```bash
docker run --rm -v "${PWD}:/workspace" -w /workspace -e CGO_ENABLED=1 -e GOFLAGS=-mod=readonly golang:1.24 go test -race ./...
```

### 一次性集成、覆盖率与混沌测试

PowerShell 运行脚本会为 MySQL、PostgreSQL、Redis 以及（混沌测试时）Toxiproxy 启动本地一次性 Docker Compose 环境。脚本会创建和删除测试表及卷，因此绝不能将其 DSN 指向共享或生产服务。

```powershell
# Windows PowerShell；PowerShell 7 用户可将其替换为 pwsh。
# 由一次性 MySQL/PostgreSQL 测试生成 SQL 契约覆盖率。
$sqlProfile = Join-Path $env:TEMP 'saas-sql-contract-coverage.out'
powershell.exe -NoProfile -ExecutionPolicy Bypass -File tests/run-integration.ps1 -CoverageProfile $sqlProfile

$profile = Join-Path $env:TEMP 'saas-coverage.out' # 合并核心、可选模块与数据库 profile
powershell.exe -NoProfile -ExecutionPolicy Bypass -File tests/run-coverage.ps1 -Output $profile -IntegrationProfile $sqlProfile
powershell.exe -NoProfile -ExecutionPolicy Bypass -File tests/check-coverage.ps1 -Profile $profile -Minimum 85
Remove-Item -LiteralPath $sqlProfile, $profile, "$profile.txt" -Force -ErrorAction SilentlyContinue

powershell.exe -NoProfile -ExecutionPolicy Bypass -File tests/run-chaos.ps1
```

对于生产 Redis，请在将 `go-redis` 客户端传递给可选 `cache/redis` 模块中的 `redis.New` 前，配置 TLS、超时、重试限制和 OpenTelemetry。

## 项目布局

```text
core/          Tenant context, resolver, store, and types
deployment/    Logical tenant-to-deployment-unit directory and moves
data/          Data filtering contracts and adapters
tenant/        租户生命周期模块
plan/          套餐元数据和存储
subscription/  订阅生命周期和计费 hook
quota/         配额检查和消耗
feature/       功能默认值和租户覆盖
onboarding/    跨模块租户开通
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

## 兼容性

模块安装、Go 版本要求、发布 tag 与迁移步骤请参阅
[docs/modules.zh-CN.md](docs/modules.zh-CN.md)。更广泛的兼容性说明请参阅
[docs/compatibility.zh-CN.md](docs/compatibility.zh-CN.md)。

## 许可证

[Apache License 2.0](LICENSE)

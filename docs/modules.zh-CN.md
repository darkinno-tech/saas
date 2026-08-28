# 核心模块与可选模块

[EN](modules.md) | [中文](modules.zh-CN.md)

SaaS v0.3.0 将无外部集成依赖的租户工具包，与仅部分应用需要的集成能力拆分开来。仅引入根模块时，不会下载 ORM、Web 框架、Redis、OIDC、OpenTelemetry 或 AWS SDK 依赖；应用代码实际使用某项集成时，再按需引入对应模块。

## 核心模块

根模块为 `github.com/darkinno-tech/saas`，支持 Go `1.22+`。它包含租户边界、生命周期服务、`database/sql` 与 sqlx 辅助能力、`net/http` 中间件、内存缓存、`slog` 辅助函数，以及不依赖外部提供商 SDK 的业务模块。

```bash
go get github.com/darkinno-tech/saas@v0.3.0
```

### 宿主集成的领域包

[`biz/commission`](../biz/commission) 是根模块中的宿主集成型领域包，应用通过组合方式按需接入。它不依赖支付 SDK，也不执行打款或迁移；宿主应用负责提供标准化业务事实（例如已支付/退款事件），决定何时记账或冲正收益，并可选地提供结算适配器。所有 `Service` 命令都必须由宿主 `Authorizer` 适配器授权，不能把传入的 `Actor` 或 `Actor.Host` 字段当作凭据。结算适配器必须返回明确的 `pending`、`settled` 或 `rejected` 结果，只有经验证的终态才会改变 submitted 批次。

根模块和实际选用的可选模块应保持在同一个 SaaS 发布版本。例如，GORM 应用只需安装根模块和 GORM 适配器：

```bash
go get github.com/darkinno-tech/saas@v0.3.0
go get github.com/darkinno-tech/saas/data/gorm@v0.3.0
```

## 可选集成模块

下列路径均为独立版本管理的 Go 模块。Go 版本是该模块支持的最低工具链版本；一旦应用导入更高层级的模块，应用自身也需要满足相应的版本要求。

| 模块路径 | 最低 Go | 用途 |
|---|---:|---|
| `github.com/darkinno-tech/saas/data/gorm` | 1.22 | GORM v2 租户插件与防护 |
| `github.com/darkinno-tech/saas/web/gin` | 1.22 | Gin 中间件与防护 |
| `github.com/darkinno-tech/saas/web/fiber` | 1.22 | Fiber 中间件与防护 |
| `github.com/darkinno-tech/saas/web/kratos` | 1.22 | Kratos 中间件与防护 |
| `github.com/darkinno-tech/saas/data/ent` | 1.23 | Ent 谓词、过滤器与 Hook |
| `github.com/darkinno-tech/saas/web/echo` | 1.23 | Echo 中间件与防护 |
| `github.com/darkinno-tech/saas/rpc/grpc` | 1.23 | gRPC unary 与 stream 拦截器 |
| `github.com/darkinno-tech/saas/obs/otel` | 1.23 | OpenTelemetry 链路追踪辅助函数 |
| `github.com/darkinno-tech/saas/biz/notification/ses` | 1.23 | Amazon SES v2 通知器 |
| `github.com/darkinno-tech/saas/cache/redis` | 1.24 | `go-redis/v9` 缓存适配器 |
| `github.com/darkinno-tech/saas/biz/identity/oidc` | 1.24 | OIDC 授权码流程桥接 |

可通过 `go get <模块路径>@v0.3.0` 安装任一可选模块，例如：

```bash
go get github.com/darkinno-tech/saas/cache/redis@v0.3.0
go get github.com/darkinno-tech/saas/biz/identity/oidc@v0.3.0
```

这是有意设计的兼容性隔离：使用 Go 1.22 或 1.23 的应用可以接入核心工具包，而不会被迫下载或编译需要 Go 1.24 的 Redis、OIDC 依赖链。

## 示例模块

可运行示例也是独立模块，因此不会进入应用的依赖图。可在仓库根目录使用 `go -C` 运行：

| 示例模块 | 最低 Go | 命令 |
|---|---:|---|
| `github.com/darkinno-tech/saas/examples/quickstart` | 1.22 | `go -C examples/quickstart run .` |
| `github.com/darkinno-tech/saas/examples/gin-gorm` | 1.22 | `go -C examples/gin-gorm run .` |
| `github.com/darkinno-tech/saas/examples/grpc` | 1.23 | `go -C examples/grpc run .` |
| `github.com/darkinno-tech/saas/examples/ent` | 1.23 | `go -C examples/ent run .` |

## 发布与 tag 规则

同一个 SaaS 发布中的模块使用相同的语义版本；但 Go 要求每个嵌套模块的 tag 带上目录前缀：

| 模块 | v0.3.0 对应发布 tag |
|---|---|
| 根模块 `github.com/darkinno-tech/saas` | `v0.3.0` |
| `github.com/darkinno-tech/saas/data/gorm` | `data/gorm/v0.3.0` |
| `github.com/darkinno-tech/saas/cache/redis` | `cache/redis/v0.3.0` |
| 位于 `<path>` 的任意其他嵌套模块 | `<path>/v0.3.0` |

同一规则也适用于示例模块，例如 `examples/quickstart/v0.3.0`。应从同一个发布提交创建根模块 tag 和所有已变更嵌套模块的 tag，这样 `go get` 才能解析出一致的模块集合。

## 从拆分前 API 迁移

根模块 API 仍保留在根模块中。只有可选能力移动到了独立模块路径，或需要显式安装对应模块。

| v0.3.0 前 | v0.3.0 后 |
|---|---|
| 从 `github.com/darkinno-tech/saas/cache` 使用 `cache.NewRedis`、`cache.NewRedisFromOptions`、`cache.NewRedisFromClusterOptions`、`cache.NewRedisFromURL` | 导入 `github.com/darkinno-tech/saas/cache/redis`；使用 `redis.New`、`redis.NewFromOptions`、`redis.NewFromClusterOptions`、`redis.NewFromURL`。 |
| 从 `github.com/darkinno-tech/saas/obs` 使用 `obs.NewTracer`、`obs.SpanAttributes`、`obs.AddSpanAttributes`、`obs.StartSpan`、`obs.RecordSpanError` | 导入 `github.com/darkinno-tech/saas/obs/otel`；通过 `otel` 包使用同名辅助函数。 |
| `notification.NewSESNotifier` 与 `notification.SES*` 类型 | 导入 `github.com/darkinno-tech/saas/biz/notification/ses`；使用 `ses.NewSESNotifier` 和 `ses.SES*` 类型。 |
| 由根模块携带的 OIDC 集成 | 显式添加 `github.com/darkinno-tech/saas/biz/identity/oidc@v0.3.0`。包导入路径和包名仍为 `oidc`；`biz/identity` 仍是核心的认证后身份映射包。 |

例如，Redis 适配器的导入方式可按以下方式迁移：

```go
// 迁移前
import "github.com/darkinno-tech/saas/cache"

client, err := cache.NewRedis(redisClient)

// 迁移后
import cacheredis "github.com/darkinno-tech/saas/cache/redis"

client, err := cacheredis.New(redisClient)
```

完成 import 变更后，运行 `go mod tidy` 及应用自身的测试。

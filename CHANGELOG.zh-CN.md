# 更新日志

[EN](CHANGELOG.md) | [中文](CHANGELOG.zh-CN.md)

本文件记录 SaaS 的所有重要变更。

## v0.2.1 - 2026-07-17

- 补充项目、根包和 Release 的对外简介，使 GitHub 与 pkg.go.dev 能清晰展示 SaaS 的定位和支持的生命周期能力。

## v0.2.0 - 2026-07-17

- 将 GoTenancy 更名为 SaaS，并将公开 Go 模块路径迁移到 `github.com/im10furry/saas`。
- 将生命周期模块从 `saas/*` 提升为顶级 `tenant`、`plan`、`subscription`、`quota`、`feature` 和 `onboarding` 包。
- 将框架适配器包标识、Webhook 默认请求头、集成/混沌测试环境变量、默认出站 User-Agent、OpenTelemetry instrumentation scope 以及 CI 覆盖率构件改为 SaaS 命名。
- 保持 Go `1.24.0` 最低版本和缓存键协议不变。完整升级映射请参阅 [v0.2 迁移指南](docs/migration-v0.2.zh-CN.md)。

## v0.1.7 - 2026-07-09

- 新增由 `github.com/redis/go-redis/v9` 支持的 Redis 缓存适配器，提供单机、集群和 URL 构造函数、`PING` 健康检查以及可选的 Redis 集成测试。
- 将最低 Go 版本提升至 `1.24.0`，以支持已修复的安全依赖，包括 `github.com/go-jose/go-jose/v4` v4.1.4。

## v0.1.6 - 2026-07-09

- 新增用于套餐、订阅、功能标志和配额使用量的 SaaS SQL 存储，实现了安全的表名校验以及 MySQL/SQLite/PostgreSQL 占位符渲染。
- 新增套餐和订阅的 Store/List API 以及面向 Store 的内存构造函数，同时保留既有的服务构造函数。
- 新增 `Role.HasPermission`、RBAC `Enforcer`、`MemoryEnforcer`，并为配额服务添加 nil-store 防护。
- 为 `biz/audit` 和 `biz/rbac` 新增 `database/sql` SQLStore 实现。
- 为 `biz/user`、`biz/identity` 和 OIDC 登录状态新增 `database/sql` SQLStore 实现。
- 为 `biz/notification` 新增 SMTP 邮件投递。
- 为租户、套餐、订阅、审计和成员资格存储新增基于游标的列表分页；为复杂 SQL 新增显式 sqlx 租户条件辅助函数；并将租户上下文中的 GORM `Unscoped` 访问从 panic 改为返回错误。
- 新增用于 `slog` 结构化日志、OpenTelemetry API span 属性、span 错误记录和可观测性基准测试的 `obs` 辅助函数。
- 新增通知渠道路由、顺序扇出、重试、超时、`NotifierFunc`、Amazon SES API v2 投递、Resend 邮件 API 投递、支持可选 HMAC 签名的 HTTP webhook 投递，以及共享消息校验辅助函数。

## v0.1.5 - 2026-07-09

- 新增订阅续订、直接过期、基于宽限期的 `ExpireDue` 扫描以及当前周期跟踪。
- 新增 `saas/onboarding`，用于协调租户创建、套餐校验、订阅创建、功能/配额初始化、审计记录、欢迎通知和激活。
- 新增 `biz/identity` 作为认证后的身份映射层，提供供应商元数据预设，以及已验证身份到租户用户/成员的关联。
- 新增 `biz/identity/oidc`，支持标准 OIDC 授权码回调处理、PKCE、ID Token 验证、可选的 userinfo 加载、一次性登录状态存储和断言输出。
- 修复 OIDC 授权 URL 生成：调用方选项不能再覆盖 nonce 或 PKCE 安全参数。
- 修复 OIDC 内存登录存储的容量处理：在返回 `ErrLoginStoreFull` 前会回收已过期的登录记录。

## v0.1.4 - 2026-07-09

- 将 ORM、框架、gRPC 和示例包恢复到主 Go 模块中，使 `go get github.com/im10furry/gotenancy` 提供完整的包接口。
- 将 CI 和 lint 验证恢复为根模块检查，同时继续将 SQLStore 数据库集成测试排除在默认门禁之外。
- 明确未来扩展能力（而不是核心接入适配器）才是合适的拆分边界。

## v0.1.3 - 2026-07-09

- 将 ORM、框架、gRPC 和示例包拆分到独立 Go 模块中，使根模块不再拉取适配器依赖。
- 将数据库集成检查保留在默认 CI 门禁之外。
- 为 quickstart、Gin + GORM、gRPC 和 Ent 示例新增 CI smoke 覆盖。
- 为 HTTP、Echo、Fiber、Kratos 和 gRPC 租户防护及流/请求上下文行为新增回归测试。
- 新增本更新日志以便追溯发布变更。

## v0.1.2 - 2026-07-09

- 为 quickstart、Gin + GORM、gRPC 和 Ent 新增可运行的接入示例。
- 扩充 README 指引，涵盖安装、包概览、验证和集成示例。
- 为公共示例、web、SaaS、data、RPC、可观测性和业务模块新增包文档注释。

## v0.1.1 - 2026-07-08

- 将许可证文件替换为标准 Apache License 2.0 文本。

## v0.1.0 - 2026-07-08

- 发布初始 GoTenancy 模块。
- 新增共享数据库租户上下文、租户解析、存储、GORM/Ent/sqlx 数据防护、web 和 gRPC 中间件、SaaS 模块、缓存隔离、迁移规划、可观测性辅助函数和安全测试。
- 为最低支持的 Go 版本和最新 Go 工具链系列新增 CI 覆盖。

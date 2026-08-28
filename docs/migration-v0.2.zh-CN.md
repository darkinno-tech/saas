# 迁移到 SaaS v0.2.0

SaaS v0.2.0 将原 GoTenancy 模块改名。这是一次破坏性发布：仓库、Go 模块
路径、根包、生命周期包路径以及少量对外标识均已调整。最低支持的 Go 版本仍为
`1.24.0`。

## 更新 Go import

| 旧路径 | 新路径 |
|---|---|
| `github.com/darkinno-tech/gotenancy` | `github.com/darkinno-tech/saas` |
| `github.com/darkinno-tech/gotenancy/saas/tenant` | `github.com/darkinno-tech/saas/tenant` |
| `github.com/darkinno-tech/gotenancy/saas/plan` | `github.com/darkinno-tech/saas/plan` |
| `github.com/darkinno-tech/gotenancy/saas/subscription` | `github.com/darkinno-tech/saas/subscription` |
| `github.com/darkinno-tech/gotenancy/saas/quota` | `github.com/darkinno-tech/saas/quota` |
| `github.com/darkinno-tech/gotenancy/saas/feature` | `github.com/darkinno-tech/saas/feature` |
| `github.com/darkinno-tech/gotenancy/saas/onboarding` | `github.com/darkinno-tech/saas/onboarding` |

根包标识从 `gotenancy` 改为 `saas`。框架适配器的包标识同样改名：
`gingotenancy`、`echogotenancy`、`fibergotenancy`、`httpgotenancy`、
`kratosgotenancy`、`grpcgotenancy` 分别变为 `ginsaas`、`echosaas`、
`fibersaas`、`httpsaas`、`kratossaas`、`grpcsaas`。

```bash
go get github.com/darkinno-tech/saas@v0.2.0
go mod tidy
```

v0.2.0 之前的已发布版本仍保留旧模块路径。请原子性地更新所有 import；新模块
不会提供透明的旧路径兼容层。

## 更新运行时集成

| 项目 | 旧值 | 新值 |
|---|---|---|
| Webhook 签名头 | `X-GoTenancy-Signature` | `X-SaaS-Signature` |
| Webhook 时间戳头 | `X-GoTenancy-Timestamp` | `X-SaaS-Timestamp` |
| Webhook 租户头 | `X-GoTenancy-Tenant-ID` | `X-SaaS-Tenant-ID` |
| Webhook 渠道头 | `X-GoTenancy-Channel` | `X-SaaS-Channel` |
| 默认出站 User-Agent | `gotenancy` | `saas` |
| OpenTelemetry scope | `github.com/darkinno-tech/gotenancy` | `github.com/darkinno-tech/saas` |
| GORM 插件与回调命名空间 | `gotenancy` / `gotenancy:*` | `saas` / `saas:*` |
| 错误文本前缀 | `gotenancy/...` | `saas/...` |
| 集成与混沌测试环境变量 | `GOTENANCY_*` | `SAAS_*` |

`WebhookConfig.SignatureHeader` 和 `WebhookConfig.TimestampHeader` 仍然可配置，
因此可以在协调迁移期保留旧的签名与时间戳头名称。租户和渠道头的消费者需要与
生产者同时升级。

缓存键协议保持不变：租户键为 `t2:`，旧兼容键为 `t:`，显式主机全局键为 `g:`。
品牌改名不应破坏或削弱这一隔离边界。

## 验证升级

```bash
go test ./...
go vet ./...
go test -race ./...
```

如应用消费 SaaS Webhook，或在监控面板中使用其 OpenTelemetry scope，请在部署
v0.2.0 前同步更新这些集成。

# Migrating to SaaS v0.2.0

SaaS v0.2.0 renames the former GoTenancy module. It is a breaking release:
the repository, Go module path, root package, lifecycle package paths, and a
small set of externally visible identifiers have changed. The minimum supported
Go version remains `1.24.0`.

## Update Go imports

| Before | After |
|---|---|
| `github.com/im10furry/gotenancy` | `github.com/im10furry/saas` |
| `github.com/im10furry/gotenancy/saas/tenant` | `github.com/im10furry/saas/tenant` |
| `github.com/im10furry/gotenancy/saas/plan` | `github.com/im10furry/saas/plan` |
| `github.com/im10furry/gotenancy/saas/subscription` | `github.com/im10furry/saas/subscription` |
| `github.com/im10furry/gotenancy/saas/quota` | `github.com/im10furry/saas/quota` |
| `github.com/im10furry/gotenancy/saas/feature` | `github.com/im10furry/saas/feature` |
| `github.com/im10furry/gotenancy/saas/onboarding` | `github.com/im10furry/saas/onboarding` |

The root package identifier changes from `gotenancy` to `saas`. Framework
adapter package identifiers follow the same rename: `gingotenancy`,
`echogotenancy`, `fibergotenancy`, `httpgotenancy`, `kratosgotenancy`, and
`grpcgotenancy` become `ginsaas`, `echosaas`, `fibersaas`, `httpsaas`,
`kratossaas`, and `grpcsaas`.

```bash
go get github.com/im10furry/saas@v0.2.0
go mod tidy
```

Released versions before v0.2.0 retain the former module path. Update all
imports atomically; the renamed module deliberately does not provide a
transparent import-path shim.

## Update runtime integrations

| Surface | Before | After |
|---|---|---|
| Webhook signature header | `X-GoTenancy-Signature` | `X-SaaS-Signature` |
| Webhook timestamp header | `X-GoTenancy-Timestamp` | `X-SaaS-Timestamp` |
| Webhook tenant header | `X-GoTenancy-Tenant-ID` | `X-SaaS-Tenant-ID` |
| Webhook channel header | `X-GoTenancy-Channel` | `X-SaaS-Channel` |
| Default outbound User-Agent | `gotenancy` | `saas` |
| OpenTelemetry scope | `github.com/im10furry/gotenancy` | `github.com/im10furry/saas` |
| GORM plugin and callback namespace | `gotenancy` / `gotenancy:*` | `saas` / `saas:*` |
| Error text prefixes | `gotenancy/...` | `saas/...` |
| Integration and chaos test variables | `GOTENANCY_*` | `SAAS_*` |

`WebhookConfig.SignatureHeader` and `WebhookConfig.TimestampHeader` remain
configurable, so a consumer can use legacy signature and timestamp header names
during a coordinated migration. Update consumers of the tenant and channel
headers at the same time as the producer.

The cache key protocol remains unchanged: tenant keys use `t2:`, legacy
compatibility keys use `t:`, and explicit host-global keys use `g:`. A brand
rename must not invalidate or weaken that isolation boundary.

## Verify the upgrade

```bash
go test ./...
go vet ./...
go test -race ./...
```

If your application consumes SaaS webhooks or uses its OpenTelemetry scope in
dashboards, update those integrations before deploying v0.2.0.

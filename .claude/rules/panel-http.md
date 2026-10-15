---
paths:
  - "panel/internal/module/*/handler/**"
  - "panel/internal/pkg/httpx/**"
  - "panel/internal/pkg/ratelimit/**"
  - "panel/internal/pkg/route/**"
  - "panel/internal/pkg/authn/**"
  - "panel/internal/pkg/reqmeta/**"
  - "panel/internal/module/idempotency/**"
  - "panel/internal/module/realtime/**"
---

# HTTP：路由、中间件、幂等、限流

## 中间件链
- 两个引擎（public、admin）都由 `panel/internal/app/serve.go` 的 `newEngine` 建，顺序：`RequestID` → `AccessLog` → `Recovery` → `Errors` → `SecurityHeaders` → `Concurrency` → `Timeout` → 引擎专属（admin 先 `AdminWritesGate`，再 `Authenticate` → `DomainGuard`）。SSE `/v1/events` 不占并发名额、不设请求超时（`isStream`）。
- 服务器限额：请求头 64 KiB（`serve.go` 的 `maxHeaderBytes`）；JSON 请求体 1 MiB（`httpx.MaxBodyBytes`，回调与 webhook 各自 LimitReader）；超时、并发、连接池在 config 缺省值里（`internal/pkg/config/load.go`），改 `database.max_conns` 缺省要同步 `deploy/postgresql-pandora.conf` 的 `max_connections` 预算（serve 30 + worker 10）与 RUNBOOK 的连接预算一节。节点 gRPC 每条连接最多 16 条流（`node/grpc/server.go`）。
- 安全响应头只在 `httpx.SecurityHeaders`（nosniff、X-Frame-Options、Referrer-Policy、COOP/CORP same-origin、Permissions-Policy、no-store；生产加 HSTS 一年、不带 includeSubDomains）。入口页的 CSP 在 `internal/pkg/webapp`（`indexCSP`）；webapp 不读设置，门户入口页经 `webapp.WithExtraSources` 由 app 装配注入额外来源（`internal/app/portal_csp.go`：只在开了人机验证时追加所选那家的固定来源；只读服务商一键、不解封 secret，进程内缓存：成功 30 秒、读失败 5 秒，本进程保存注册设置后（`SignupGeneration` 变了）立即重读，同一时刻只有一趟读库（singleflight，不持锁、上限 2 秒）；CSP 变了 ETag 跟着变）。HSTS 不放 nginx：location 一旦写了自己的 `add_header` 就不继承 server 级的。
- 路由级中间件经 `route.Kit` 传给每个模块（`panel/internal/pkg/route/route.go`）：`RequireAuth`、`RecentReauth`、`Idempotency(scope)`、`Switch(code)`。模块之间不互相 import handler。
- 顺序固定：`RecentReauth` 排在 `Idempotency` 之前，403 `reauth_required` 时幂等键还没被占，前端弹框后用原键重放。降级开关门同样排在幂等之前。
- 后台不分权限：`RequireAuth` 在 admin 上就是「已登录的管理员」，没有权限码、角色。
- 哪些后台写不要求重认证是产品决定，改之前先问用户：后台查单 `POST /v1/orders/:id/query` 只要幂等（只记渠道已确认的那笔，造不出钱）；节点 `activate` 不要重认证，`retire` 要（`panel/internal/module/node/handler/routes_inventory.go`）。

## 来源地址
- 引擎只信 nginx 覆写的 `X-Real-IP`（`TrustedPlatform`，不解析 X-Forwarded-For），所以两个 HTTP 监听在生产必须绑回环（`panel/internal/pkg/config/validate.go` 强制）。
- IP、UA 由 `RequestID` 中间件写进 ctx（`reqmeta.With`），审计、限流、登录记录用 `reqmeta.From(ctx)` 取，不沿调用链传参。

## handler 与 DTO
- handler 只做绑定、校验、调 service、写响应。每个接口在 handler 包里单独定义请求与响应 DTO，不把 model 或 service 结构直接序列化，不把请求体直接解码进 service 输入。JSON 键名就是前端契约。
- 请求体经 `httpx.BindJSON`（1 MiB、`DisallowUnknownFields`），校验失败 422 `validation_failed`，`fields` 写中文；业务校验在 service。
- 成功响应不包信封：直接回 DTO，状态码表达结果，经 `httpx.OK` / `Created` / `Accepted` / `NoContent` 写；要录进幂等表的经 `idemsvc.JSON`。handler 里不直接调 `c.JSON`。
- 错误交给 `httpx.Fail`（或 `c.Error`）统一写信封，不要自己拼 JSON。面向用户的文案是中文，页面原样显示。

## 幂等
- `panel/internal/module/idempotency/handler/middleware.go` 的 `Middleware(scope)`：按 `Idempotency-Key` 认领；业务在同一事务里已调 `service.Complete*` 时中间件不再写库，否则按录下的响应完成或释放（非 2xx 释放，同键重试会重新执行）。
- 需要与业务同事务完成的写（下单、续费、换套餐、充值、流量包、工单等）在 service 事务里完成认领；不要在事务外补写。
- 每条幂等路由用自己的 scope 名，只有同一动作的别名路由才共享，否则同一个键在两边会被当成重放。

## 限流
- `panel/internal/pkg/ratelimit`：缺省 GCRA（`PerMinute` / `PerWindow`）；产品写死的严格上限（如「每天 5 次」）用 `FixedWindow` 或 `Cooldown`。Redis 故障时普通规则放行，`StrictMode()` 的规则拒绝（注册、找回密码这类用它）。
- 中间件 `Limiter.Middleware(dims...)` 按声明顺序判，首个超限即停：「间隔」要声明在「总次数」前面。按 IP 的键用 `ByIP` / `ByIPPrefix` / `ByRoute`，不要拿原始路径做键。

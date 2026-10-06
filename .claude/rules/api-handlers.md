---
paths:
  - "panel/internal/api/**"
---

# 三个网关的处理器约定

- admin、public、node 各一个包，域间令牌不可交叉，所以没有共享的 handler 基类；共享的只有 `platform/httpx` 与 middleware 链
- 处理器不跑 SQL：不开事务、不调 Query/Exec/QueryRow/Begin/Acquire、不 import pgx。守卫：`panel/internal/api/handler_sql_guard_test.go` 的 `TestHandlersRunNoSQL`
- 响应只经 httpx 写出。CSV 导出、SSE、订阅输出、pdnd 安装、支付与 Telegram 回执、UniProxy 配置这类确需直写的，按「包目录/文件 声明名」登记进 `directWriteAllowed` 并写理由；登记项失效同样变红。守卫：`panel/internal/api/response_writes_guard_test.go` 的 `TestAPIHandlersWriteResponsesOnlyThroughHttpx`
- 拿了幂等认领、交给 domain 在事务里完成的处理器，成功路径只能用 `httpx.WritePrepared` 写出 domain 备好的那份响应，不能再自己 `httpx.OK`，否则首次与重放的状态码不一致。守卫：`panel/internal/api/admin/idempotent_response_contract_test.go` 的 `TestIdempotentHandlersWritePreparedResponse`
- 成功响应用具名结构体（多为 `<处理器名>Response`），不回 `map`；JSON 键名就是前端契约，有时缺省的键用指针或 omitempty 表达
- 改变节点交付集合或路由的写，在事务提交之后再通知节点（`NotifyUsersChanged`、`notifyRoutingNodes`）。守卫：`panel/internal/api/admin/node_routing_notify_test.go` 的 `TestNodeSetRoutingNotifiesNodeAfterCommit`
- 来源地址只经 `httpx.ClientIP` 取（只信 nginx 覆写的 X-Real-IP，不解析 X-Forwarded-For），限流、审计、拉取日志同一口径。守卫：`panel/internal/platform/httpx/context_test.go` 的 `TestClientIPTrustsOnlyXRealIP`、`panel/internal/api/public/public_security_contract_test.go` 的 `TestSubscribeTakesClientIPFromHTTPX`
- 根路由：admin、public 都经 `platform/webapp` 在根 `/` 与 `/assets/*` 下发前端；public 的字面量段（`/assets`、`/pdnd`、`/v1`）必须优先于订阅通配 `/{prefix}/{token}`。守卫：两个包 `router_contract_test.go` 的 `TestAdminRouterServesEmbeddedApp`、`TestPublicRouterServesEmbeddedAppBesideSubscriptions`
- 面向用户的 httpx 文案必须是中文（页面原样显示）；只给节点、支付渠道看的函数要豁免，就登记进 `message_zh_contract_test.go`，且不能被后台与门户调用。守卫：`TestUserFacingErrorMessagesAreChinese`、`TestNodeOnlyExemptionsStayOffUserGateways`

## node 包

- 调用方是机器：pdnd 走 `/nodes/*`，用每个请求的 Ed25519 签名证明身份；UniProxy 兼容端走 `/api/v1/server/UniProxy/*`，用节点令牌
  - 令牌的取法只在 `uniProxyToken`：有 Authorization 头时它说了算（只认单个 Bearer，畸形即拒，不降级到查询参数），没有才用 `?token=`。`authNode` 不自己读请求头。守卫：`panel/internal/api/node/handlers_auth_test.go` 的 `TestUniProxyTokenPrefersBearerAndKeepsLegacyFallback`、`TestAuthNodeDelegatesCredentialSelection`
- 节点 SSE 只是快车道，失败即放弃；节点端有轮询兜底，不要把它当唯一通路去补可靠性
- 整包文案只给节点看，不受中文文案契约约束

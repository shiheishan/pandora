---
paths:
  - "panel/frontend/src/core/**"
  - "panel/frontend/src/shell/**"
  - "panel/frontend/src/admin/**"
  - "panel/frontend/src/portal/**"
---

# 面板前端：core 与 shell 底座

- 依赖方向是 core → ui → shell → admin / portal，不许反向
  - core 不 import 组件
  - ui 里只有 `QueryView` 依赖 `core/api` 的 `isApiError`
  - 没有守卫测试，靠约定维持
- core 不持有单例：`TokenStore`、`ApiClient`、`QueryClient` 由各入口经 `shell/runtime.tsx` 的 `createAppRuntime` 各建一份
  - 两个网关的令牌互不通用；后台还要另外注入 reauth 对话框
- 两个入口同源（后台只是一个反代前缀），所以令牌的 localStorage 键必须按入口区分（`core/token.ts` 的 `tokenStorageKey`）
  - 守卫：`token.test.ts` 的 "keeps admin and portal tokens apart on the shared origin"
  - 只存 access_token，后端没有 refresh 接口
- 幂等键约定（契约 1.5）只有 `core/intent.ts` 这一份，后台的 `admin/actions.ts` 与门户的 `screens/common/intent.ts` 都是原样转出
  - 一次用户意图一把键，按意图的 JSON 指纹决定：双击、断网或 5xx 后重试、reauth 重放都复用同一把键
  - 意图变了就换新键，因为同键换请求体后端回 409 `idempotency_key_reuse`；成功后调 `reset`
  - 后端只重放 2xx。所以 4xx 结束意图（`endsIntent`）；`reauth_required`、断网、5xx、回包解析失败都保留键
- 重试只在 `api.ts` 一处做，同一次调用里才能复用幂等键
  - 网络失败只重发 GET 和带键的请求
  - react-query 层 mutation 不重试，query 只对 5xx 补一次
  - 不要在页面或 query 层另加写请求的重试
- reauth（契约 1.6，只有后台有）
  - 403 `reauth_required` 在幂等中间件之前就被拦下，键没有消耗，所以 `api.ts` 不论有没有键都只重放一次
  - 并发被拦的请求共用一次 `requestReauth`
  - 用户取消时请求以 `reauth_required` 失败，调用方应静默
- 401 会清掉发请求时用的那枚令牌并登出。口令校验接口（后台改密、reauth、门户改密）必须传 `passwordCheck: true`，否则口令输错会把人踢回登录页
- 非 JSON 响应（CSV 导出等）走 `api.requestRaw`，不要裸 `fetch`，否则会丢掉 Bearer、幂等键和 reauth 重放
  - 幂等重放的响应不带 `Content-Disposition`，文件名用 `core/download.ts` 的 `filenameFromDisposition` 回退
- SSE 只能经 `api.openStream` 用 fetch 流读，不能用 `EventSource`：认证只有 Bearer 头，`EventSource` 带不上
- 每个入口只开一条 SSE（后台在 `admin/EventsCapsule.tsx`，门户在 `portal/Shell.tsx`，都用 `useRealtime`）
  - 页面要随事件刷新，就在查询的 `meta.topics` 里声明，由 `createRealtimeInvalidator` 失效，不另开连接
- `core/query.ts` 的 `REALTIME_TOPICS` 要与 Go 对齐：`platform/realtime/listener.go` 的 `topicFor`，加上 admin / public 的 `tickets.go` 发的 `ticket.updated`，以及 admin `handlers.go` 发的 `switches.changed`。Go 新增 topic 时这里同步加
- 令牌清空（退出、任一请求 401、其它标签页退出）时，`createAppRuntime` 会清空查询缓存，下一个登录的人看不到上一个人的数据。不要绕开 runtime 自建 `QueryClient`

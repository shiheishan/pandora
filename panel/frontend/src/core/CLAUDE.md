# panel/frontend/src/core/
> L2 | 父级: /panel/frontend/CLAUDE.md

与界面无关的前端底层，两个入口共用。这里的模块不 import 组件，组件与页面 import 它们；它们也不持有单例——api 客户端、令牌存储、QueryClient 由各入口（第 ⑥ 步）各建一份，因为两个网关的令牌互不通用，后台还要注入 reauth 对话框。
数据流：页面 useQuery / useMutation → api.ts（唯一出口，zod 校验响应）→ fetch；sse.ts 经 api.openStream 建流 → query.ts 的失效器按查询 meta.topics 失效。契约以 panel/docs/redesign/api-contract.md 第 1 节与第 9 节修订为准。

成员清单
theme-boot.js: 首帧前的主题引导，经典脚本（非 module），读 localStorage 的 pandora-theme 写 <html data-theme>；由 vite.config.ts 的 themeBoot 插件按内容哈希输出到 assets/ 并注入到 <meta charset> 之后，因为 CSP 禁止内联脚本、而 module 脚本延迟执行会让深色用户闪白
theme.ts: 主题状态，真相在 <html data-theme> 上不另存 React 状态；setTheme 写存储并改属性、toggleTheme、subscribeTheme 同步其它标签页（storage 事件）、useTheme 经 useSyncExternalStore 供组件订阅；只有 light / dark，非 dark 一律按 light
theme.test.ts: theme.ts 的单元测试，伪造 document / window 覆盖解析、持久化、存储不可用、订阅与跨标签页同步
api.ts: 唯一 HTTP 出口。路径只收 v1/ 开头、相对 document.baseURI 解析且不许逃出入口目录（后台在 nginx 前缀后）；Bearer 取自 TokenStore；错误信封解析成 ApiError（封闭码列表 + network_error / invalid_response，无信封时按状态归类）；响应一律经 zod schema；idempotencyKey 一次调用内跨网络重试与 reauth 重放复用，网络失败只重发 GET 与带键请求；403 reauth_required 时并发请求共用一次 requestReauth，换新令牌后以原键重放一次；非口令接口的 401 清掉发请求用的那枚令牌并回调 onUnauthorized，passwordCheck 接口的 401 留给表单；requestRaw 走同一条链路但 2xx 不解析、把 Response 交给调用方（CSV 导出等非 JSON 响应）；UUID v4 用 getRandomValues（明文 HTTP 下没有 randomUUID）；模块加载即设 zod jitless
token.ts: 访问令牌存储，localStorage 键按入口区分（pandora-admin-token / pandora-portal-token，两网关同源），存储不可用退回内存，storage 事件同步其它标签页；只存 access_token，不存 refresh_token（后端无 refresh 接口）
sse.ts: 实时事件流。createSseParser 按 WHATWG 规范解析（任意块边界、CRLF/CR/LF、注释心跳、retry）；openEventStream 经 connect（一般是 api.openStream）用 fetch 流读，流结束、断网、5xx、429 等 retry（首帧 5000）加随机抖动重连并以 reconnected 标记，4xx 调 onStop 停止（404 = 无 ops.notification.read）；不用 EventSource，因为认证只有 Bearer 头
query.ts: react-query 接入。createQueryClient 默认查询只对 5xx 补一次重试、写不在这层重试（重发要复用幂等键，归 api.ts），staleTime 30 秒；Register 把默认错误登记为 ApiError、queryMeta 登记 topics；createRealtimeInvalidator 按 meta.topics 精确失效、同 topic 2 秒节流合并（节点上报会刷屏 subscriptions.changed）、重连时失效全部；REALTIME_TOPICS 对齐 platform/realtime/listener.go
download.ts: saveFile（Blob 经 <a download> 存文件，对象 URL 用后回收）、filenameFromDisposition（取 Content-Disposition 文件名，缺失时回退，幂等重放就不带这个头）、toCsv（BOM + RFC 4180 转义 + 防公式注入）
format.ts: formatMoney（最小单位 → ¥1,280.00，CNY / USD 符号，其它币种写代码）、formatCount（整数千分位）、formatBytes（1024 进制三位有效数字，BigInt 安全，吃 DASH-01 的十进制字符串）、relativeTime（刚刚 / N 分钟 / N 小时 / MM-DD，设计稿口径）与 formatDateTime（本地时区 YYYY-MM-DD HH:mm，解析不了原样返回）
router.ts: hash 路由原语，parseHash / href / navigate（push 或 replace）/ matchPath（:param，已解码）/ useHashLocation（useSyncExternalStore 订阅 hashchange）；真相在 location.hash，不含路由表；为什么是 hash：网关只下发 / 与 /assets/*，public 根下还有两段式订阅通配
*.test.ts: api / token / sse / query / router / format / download 的单元测试，node 环境下伪造 fetch、流、location 与计时器，不引入 DOM 测试库；覆盖第 ⑤ 步验收的幂等键跨重试复用、reauth 后重放、SSE 断线重连、前缀下相对路径解析

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

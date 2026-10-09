---
paths:
  - "panel/internal/middleware/**"
---

# 横切中间件

- 只依赖 platform，不 import domain
- 路由上 `RequireRecentReauth` 总在 `Idempotency` 之前：reauth 失败不能消耗幂等键（前端以原键重放）
- `RequirePermission` 缺权限回 404，不回 403，免得暴露接口存在
- 中间件不写 SQL（2026-10-07）：认证那一次往返在 `platform/sessionauth`，开关读取在 `platform/featureswitch`，中间件只调它们。`idempotency.go` 的认领 SQL 还在本包，待迁
- `sessions.last_seen_at` 只在 `platform/sessionauth` 的 `touchSQL` 一处写（5 分钟节流），会话有效性、节流刷新与实时权限在一条语句里取齐（`authSQL`，经 `QueryRowScoped` 一次往返）；别处不要再写这一列。不做进程内会话缓存：吊销必须下一个请求立即生效
- 限流（`ratelimit.go`）每层一个 Lua 脚本、一次 EVALSHA 判定全部维度，按声明顺序计数、遇第一个超限维度即停；`RateLimit` 后端故障放行，`RateLimitStrict` 回 503
  - 超限时脚本回 `{序号, 计数, 剩余毫秒}`。维度 `.AsCooldown()` 的计数键不带时间窗编号、从第一次计数起过期（Max=1 即「两次至少隔 Window」）；`.WithRetryHint()` 让 429 写「操作太频繁，请 X 分钟后再试」。被前面维度拦下的请求不占后面维度的额度，所以「间隔」要声明在「总次数」前面
- 访问日志 `AccessLog`（`accesslog.go`）挂在三个路由器的 `RequestID` 之后：每请求一行「access」，字段 route（「方法 路由模板」，不记原始路径——订阅令牌、回调签名都在路径里）、status、dur_us、db_rt、kv_rt、request_id，db_ping / db_prepare / db_reset 非零才写。它给每个请求挂往返计数器（`platform/roundtrip`），每请求只多 1 次分配，守卫 `accesslog_alloc_test.go`（函数标了 `//go:noinline`，去掉会让请求副本逃逸到堆上）
  - 节点网关挂 `QuietSuccess(20ms)`：成功（200/204/304）且快的请求降到 debug，生产不落盘（上千节点每秒几百个例行请求，逐条记一天几个 GB）；失败的、慢的照常 info
  - 方法名是客户端可控的：统计表的键与 access 行的 route 只用归一后的方法（标准九种，其余 `OTHER`），表另有硬上限 `maxRouteEntries`，超出并进溢出条目。按请求里的任何字段分桶（方法、路径、头、UA）都要先归一、设上限，否则未认证就能撑爆内存和日志盘。守卫 `accesslog_bounds_test.go`
  - 每个路由的累计统计每分钟打一行「access_summary」（`accesslog_summary.go`：请求数、状态分类、耗时直方图 le_ms/hist、往返总和与最大值），复测取窗口首尾两行相减出分布；逐请求行降级的路由只能看它
- 门户请求不展开权限（门户没有任何路由按权限放行）；后台才展开
- 降级开关读取有 3 秒进程内缓存（`switches.go`，`platform/cache` 的 Cache：同一开关的并发未命中只读一次库，上限 256 条）：admin 切开关经 `AdminWritesGate` 当场失效（`Clear` 同时作废正在读库的那一趟，失效之后不会写回旧值），public 收 `switches.changed` 失效，广播丢了退回 TTL。守卫 `switches_cache_test.go`
- 降级开关缺行视为开启（`platform/featureswitch.Enabled`，经 `switches.go` 的 `FeatureSwitch`、`AdminWritesGate`），因为它们是急停开关，新租户没有行也要能下单
  - 新增开关要同时补进建租户触发器 `app.seed_tenant_defaults`。守卫：`switch_seed_test.go` 的 `TestTenantSeedSwitchesMatchCode`
- 单租户假设：`Tenant` 恒定注入 `DefaultTenantID`。守卫：`tenant_guard_test.go` 的 `TestSingleTenantAssumptionGuard`
- 幂等重放只保存并回写白名单里的响应头（`idempotency_replay.go` 的 `validateIdempotencyReplayHeaders`：Content-Type、Location、ETag、Cache-Control、Content-Language）；业务要靠别的头随重放返回，得先扩白名单
- 幂等的业务完成要和业务写入同一事务（`CompleteSuccessJSONInTx`），不要在事务外补写认领
- 幂等单测按被测文件分（认领与 SQL 契约、录制器、capture、重放判定）；`idempotency_pg18_test.go` 是行数豁免文件（见根 CLAUDE.md，豁免表在 `panel/tools/refactorcheck/linelimit_test.go`），只有一个子测试共享连接的超长门禁函数；夹具与锁等待观测放 `idempotency_pg18_fixture_test.go`，不要再往门禁函数里加行

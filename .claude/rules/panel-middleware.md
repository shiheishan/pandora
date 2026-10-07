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
- 门户请求不展开权限（门户没有任何路由按权限放行）；后台才展开
- 降级开关读取有 3 秒进程内缓存：admin 切开关经 `AdminWritesGate` 当场失效，public 收 `switches.changed` 失效，广播丢了退回 TTL
- 降级开关缺行视为开启（`platform/featureswitch.Enabled`，经 `switches.go` 的 `FeatureSwitch`、`AdminWritesGate`），因为它们是急停开关，新租户没有行也要能下单
  - 新增开关要同时补进建租户触发器 `app.seed_tenant_defaults`。守卫：`switch_seed_test.go` 的 `TestTenantSeedSwitchesMatchCode`
- 单租户假设：`Tenant` 恒定注入 `DefaultTenantID`。守卫：`tenant_guard_test.go` 的 `TestSingleTenantAssumptionGuard`
- 幂等重放只保存并回写白名单里的响应头（`idempotency_replay.go` 的 `validateIdempotencyReplayHeaders`：Content-Type、Location、ETag、Cache-Control、Content-Language）；业务要靠别的头随重放返回，得先扩白名单
- 幂等的业务完成要和业务写入同一事务（`CompleteSuccessJSONInTx`），不要在事务外补写认领
- 幂等单测按被测文件分（认领与 SQL 契约、录制器、capture、重放判定）；`idempotency_pg18_test.go` 是行数豁免文件（见根 CLAUDE.md，豁免表在 `panel/tools/refactorcheck/linelimit_test.go`），只有一个子测试共享连接的超长门禁函数；夹具与锁等待观测放 `idempotency_pg18_fixture_test.go`，不要再往门禁函数里加行

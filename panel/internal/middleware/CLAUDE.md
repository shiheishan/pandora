# panel/internal/middleware/
> L2 | 父级: /panel/internal/CLAUDE.md

挂在 api 路由之前的横切中间件，只依赖 platform，不 import domain。三条公共链：认证（令牌 → 会话 → 实时权限）、门槛（权限缺失回 404 不暴露接口、高危写要 15 分钟内重认证、降级开关）、幂等（数据库持有的执行认领，业务写入与认领完成同事务）。reauth 失败不消耗幂等键，所以路由上 RequireRecentReauth 总在 Idempotency 之前。租户由 Tenant 恒定注入默认租户——产品只有一个租户，这个假设由守卫单测钉住。

成员清单
auth.go: Authenticate 解析 Bearer 装配 Principal：会话有效性、节流刷新 sessions.last_seen_at（5 分钟一次，R62 唯一写入点）与实时权限在同一事务里取齐
middleware.go: 请求 ID、Recovery、安全头、租户（Tenant 恒定注入 DefaultTenantID）、DomainGuard、RequireAuth、RequirePermission（缺权限回 404）、RequireRecentReauth（拒绝回 403 reauth_required，前端弹框后以原幂等键重放）、Valkey 限流（按 IP / 前缀 / 账号 / 路由 / 租户 / JSON 字段哈希）、超时与公共链装配
switches.go: 降级开关门：FeatureSwitch 按开关回 503、AdminWritesGate 管理端只读模式，缺行视为开启
idempotency.go: 幂等键主体：Idempotency 中间件（INSERT 新占认领，SELECT FOR UPDATE + 条件 UPDATE 接管可重试的旧认领）、IdempotencyClaim 交给业务处理器、CompleteSuccessJSONInTx 在业务事务里完成认领，作用域按主体隔离，键与请求目标、正文哈希绑定
idempotency_replay.go: 重放判定（按记录状态与存储格式，含两种旧格式）、存储响应编解码与重放出口；只保存并重放五个白名单业务响应头，写回前校验
idempotency_recorder.go: 响应录制器：透传的同时留存状态码、白名单响应头与正文，不合规即作废留存
*_test.go: 认证、限流（严格模式）、重认证错误码、超时与流式、降级开关、幂等（认领、业务完成、PG18 并发与接管）测试；幂等单测按被测文件分：idempotency_test.go 管认领、键、作用域与 SQL 契约，idempotency_recorder_test.go 管录制器提交语义，idempotency_capture_test.go 管与真实 net/http 对齐和捕获边界，idempotency_replay_test.go 管重放判定；idempotency_pg18_test.go 只有一个 983 行的门禁函数（子测试共享连接与上下文，纯挪动拆不开，行数守卫单独豁免），夹具与锁等待观测在 idempotency_pg18_fixture_test.go；switch_seed_test 钉住建租户触发器种的开关与代码读取的开关一一对应；tenant_guard_test 在产品代码出现建租户或 Tenant 不再恒定注入默认租户时失败，提示先扩 app.seed_tenant_defaults；idempotency_pg18_test.go 由 run-pg18-gates.sh 跑

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

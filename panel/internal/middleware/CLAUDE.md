# panel/internal/middleware/
> L2 | 父级: /panel/internal/CLAUDE.md

四域网关共用的横切中间件，挂在 api 路由之前，只依赖 platform、不 import domain。认证、幂等、降级开关、限流 / 超时 / 权限 / 重认证各一处实现，网关只做装配。租户由 Tenant 恒定注入默认租户——产品只有一个租户，这个假设由守卫单测钉住。

成员清单
auth.go: Authenticate 四域独立 HMAC 令牌校验与 Principal 装配；会话有效性检查后节流刷新 sessions.last_seen_at（5 分钟一次，R62 唯一写入点），实时权限同事务取齐
idempotency.go: 数据库持有的幂等键声明、业务完成绑定与资源绑定；只重放 2xx
switches.go: 降级开关门：FeatureSwitch 按开关回 503，AdminWritesGate 管理端只读模式；缺行视为开启
middleware.go: RequestID、Recovery、DomainGuard、Tenant（恒定注入 DefaultTenantID）、RequirePermission、RequireRecentReauth（拒绝回 403 reauth_required，前端弹框后以原幂等键重放）、RateLimit 与 Limit、超时与公共链装配
*_test.go: PG18 幂等（idempotency_pg18_test，run-pg18-gates.sh 的 idempotency 域）、严格限流、重认证错误码、流式超时；switch_seed_test 钉住建租户触发器种的开关与代码读取的开关一一对应；tenant_guard_test 在产品代码出现建租户或 Tenant 不再恒定注入默认租户时失败，提示先扩 app.seed_tenant_defaults

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

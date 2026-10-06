---
paths:
  - "panel/internal/**"
  - "panel/cmd/**"
---

# 面板分层与边界

- 依赖方向：api → domain → platform；middleware 挂在 api 之前
  - platform 不 import api、domain、middleware，唯一例外是 `platform/idempotencybind` 引用 middleware 的 `IdempotencyClaim`
  - middleware 不 import domain 与 api；domain 不 import api（domain 可以 import middleware，用的是幂等认领）
  - 没有守卫测试，靠自觉；新增 import 前先对照这条
- api 只做路由、鉴权链与 DTO；用例与状态机在 domain；platform 不带业务语义
  - 处理器不跑 SQL，读写都在拥有那张表的 domain 服务里。守卫：`panel/internal/api/handler_sql_guard_test.go` 的 `TestHandlersRunNoSQL`
- 安全与财务不变量落在 PostgreSQL：RLS、追加写触发器、DEFERRABLE 配平、回调唯一约束。Go 网关负责执行，策略本身由库定义
  - 运行时只以 `aegis_app`（NOSUPERUSER NOBYPASSRLS，见 `panel/deploy/configure-app-role.sql`）连库，不要为了绕开触发器或 RLS 换成特权连接
- 产品只有一个租户：`middleware.Tenant` 恒定注入 `DefaultTenantID`。产品代码里出现建租户就会让 `panel/internal/middleware/tenant_guard_test.go` 的 `TestSingleTenantAssumptionGuard` 失败；要做多租户，先扩 `app.seed_tenant_defaults`
- 客户端登录（CLIENT-AUTH）不在主线：实现代码、冻结设计稿和两份从未应用的冻结迁移都只在 tag `archive/client-auth`，不要在主线里补它的代码或迁移
- 前后端接口以代码为准：改接口形状时同步改后端处理器与前端对应的 zod schema，没有单独的契约文档

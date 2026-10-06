---
paths:
  - "panel/internal/domain/**"
---

# 业务域之间的依赖与通用约定

- 跨域只用对方导出的口径（SQL 片段、函数、接口）。现有方向：adminops → billing / nodefabric / subscription，subscription → nodefabric，support → subscription，notify → appearance，billing / giftcard / identity / notify / support → plugin
- 反向需要时用回调或接口注入，不要反向 import 造环：billing 用 `Service.SetUsersChangedNotifier` 回调（不持有 realtime），support 用 `ReplyNotifier` 接口（不 import notify），identity 用 `VerificationMailer` 接口
- nodefabric 不能 import subscription（subscription 已依赖它），所以后台节点列表的「是否下发」判定 `subscription.DeliveryState` 由 handler 调
- notify 不能 import identity：identity 的同包 PG18 测试 import 了 notify，再反引会成环；邮箱验证缺行回退值由调用方传入 `identity.EmailVerificationDefault`
- payment 渠道适配层不依赖 httpx 和数据库：渠道记录经 `payment.Loader` 注入；停用回 `payment.ErrProviderDisabled`、不支持的操作回 `payment.ErrNotSupported`，两个哨兵都由 billing 翻成中文 httpx 错误
- 数据库原句（`db.Message`）只进日志，不进 httpx 错误：按约束名译成中文，认不出就写通用中文（参照 `nodefabric.NodeStatusRefusal`、adminops 的 `switchRefusal`）。守卫 `nodefabric/node_refusal_test.go:TestNoRawDatabaseMessageInHTTPErrors`，扫整个 panel/internal
- 节点通知、`notify.Kick` 这类副作用在事务提交之后才发，不能在事务里发；写路径把「受影响节点 / 名单是否变化」返回给 handler，由 handler 在提交后通知
- 本目录的契约测试大量按声明名加 SQL 字面量检查锁序与分支：改函数名、拆函数、改被检查的 SQL 字面量时，同步改测试里的声明名和字面量

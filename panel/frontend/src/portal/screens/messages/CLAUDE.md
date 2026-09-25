# panel/frontend/src/portal/screens/messages/
> L2 | 父级: /panel/frontend/src/portal/screens/CLAUDE.md

消息（用户门户-08-消息.dc.html；契约门户-08，修订 R71）。「通知 / 公告」两个标签，#/messages?tab=announcements 直达公告（概览公告卡入口）。通知一次取 100 条（接口上限、无游标），点开先标已读、再按 code 跳页（order.paid → 订单、ticket.replied → 工单、subscription.expiring / quota.warning → 我的订阅，其余只标已读）；「全部标为已读」只在通知页签，没有未读时置灰。
新站内信没有 SSE：外框铃铛 60 秒轮询 limit=1（queries.ts 的 ['portal','notifications','unread']），这里用 ['portal','notifications','list']，写操作失效整个 ['portal','notifications'] 前缀，角标同步。公告复用 common/announcements 的查询与级别文案：标题前色点（info 不加，notice / warning / critical 取 --info / --warn / --danger）、置顶标签，正文直接展开。

成员清单
index.tsx: 页面组件——标签（通知带未读数）、全部已读、通知列表、公告列表
api.ts: 数据层——站内信 schema 与查询，单条已读、全部已读
model.ts: 纯映射——messageTab、notificationTarget
Messages.module.css: 页面样式，取自设计稿门户-08
messages.test.ts: 第 ⑤ 步消息的单元测试（schema、标签页、跳转目标）

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

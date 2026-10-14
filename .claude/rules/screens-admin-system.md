---
paths:
  - "panel/frontend/src/admin/screens/system/**"
---

# 后台 · 通知与插件

- 邮件设置的 SMTP 六字段每次整体覆盖：「注册与验证」卡保存时必须带「已保存」的 SMTP 字段（不是 SMTP 卡里没保存的输入），密码不带。
- Webhook 钩子按 code upsert、省略的字段写成零值：新建时生成避开现有 code 的 code（撞上会静默覆盖），编辑与启停都从现有行回填全字段（`logic.ts` 的请求体构建）。
- 钩子超时（500–30000 毫秒）与重试次数（1–10）后端只有 DB CHECK，越界变 500，前端先拦。
- 三个测试发送用的都是已保存的配置：渠道卡有未保存修改时禁用测试按钮。
- Telegram 扩展设置（`TelegramExtrasCard.tsx`，`/v1/settings/telegram/extras`）与 Telegram 主设置分开保存：主设置的保存会改启用开关与 webhook，扩展卡整份提交自己的 7 个键；管理员群组 chat id 是测试消息的默认目标，也是收款、工单提醒的去处。
- 产品取舍：设计稿里的「重置密码」「礼品卡兑换成功」模板后端没有，按后端现有模板显示；钩子事件只用后端返回的目录，不提供 `ticket.replied` / `node.offline` / `node.online`。

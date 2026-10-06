---
paths:
  - "panel/frontend/src/admin/screens/tickets/**"
---

# 后台 · 工单

- 状态全在地址上：`#/tickets/<工单 id>?f=<筛选>&q=<搜索>`，列表只拼链接、不持有选中态。
- 队列与详情是同一个 Go 结构：队列的 `related_order` 恒为 null，详情按关联订单联表回 `{ id, order_no }`；详情的 `message_count` / `last_reply_at` 与队列同口径，直接用，不从 messages 推算。
- 「回复并解决」是先 reply 再 status 两把幂等键：第二步失败重试时不重发回复（`Composer.tsx` 记住本次意图的回复已送达）。
- 升级到 L2 只改状态与优先级、不发通知（D-B-6 已决），文案不承诺「通知二线值班」。
- 没有 `ops.ticket.write` 时整页只读：回复框、状态 / 指派、升级、SLA 扫描与快捷回复管理都不出现。

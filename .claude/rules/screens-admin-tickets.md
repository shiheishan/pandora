---
paths:
  - "panel/frontend/src/admin/screens/tickets/**"
---

# 后台 · 工单

- 布局（10-16 设计稿）：搜索框与「立即检查 SLA 超时」「工单设置」在双栏外的顶部 `Toolbar`；左栏只有筛选（`FilterTabs`，不带计数）和列表。窄于 640 时双栏变单栏：没选工单只显示列表，选中只显示详情，上方「← 返回工单列表」（保留筛选与搜索）。
- 快捷回复条只露前 4 个（`Composer.tsx` 的 `MACRO_CHIPS`），多出的进「全部 N」浮层（带搜索，点外面或 Esc 收起，Esc 不冒泡）；「管理」弹窗是「列表 + 底部新增两个输入」，修改在单独的小弹窗里，没有排序输入（`sort_order` 新建取最大 +10、修改沿用原值）。
- 状态全在地址上：`#/tickets/<工单 id>?f=<筛选>&q=<搜索>`，列表只拼链接、不持有选中态。
- 队列的「未读」（行加粗）= `last_message_author_kind === 'user'` 且状态不是已解决/已关闭；`last_message_author_kind` 取最后一条**非内部备注、非系统消息**的作者（`repo/queue.go`），升级、改状态、撤回、自动关闭插的系统消息不会盖掉用户的追问（用户 2026-10-14 定）。mock 同口径。
- 队列与详情是同一个 Go 结构：队列的 `related_order` 恒为 null，详情按关联订单联表回 `{ id, order_no }`；详情的 `message_count` / `last_reply_at` 与队列同口径，直接用，不从 messages 推算。
- 「回复并解决」是先 reply 再 status 两把幂等键：第二步失败重试时不重发回复（`Composer.tsx` 记住本次意图的回复已送达）。
- 升级到 L2 只改状态与优先级、不发通知（D-B-6 已决），文案不承诺「通知二线值班」。
- 「工单设置」对话框（`TicketSettingsDialog.tsx`）读写 `/v1/settings/support`，三个键整份提交；关闭原因多了 `auto_closed`（「自动关闭」）。

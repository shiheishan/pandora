---
paths:
  - "panel/frontend/src/admin/screens/tickets/**"
---

# 后台 · 工单

- 布局（样稿 admin-v4 `#p-tickets`，外框 `fullBleed` 全幅模式：页面拿到 100vh 的纵向容器，没有外框页头）：三栏铺满「列表 340 | 对话 自适应 | 属性 280」。页面网格只有两列（`index.tsx`：`Queue` | `Detail`），`Detail` 内部再分「对话 | 属性栏（`Props.tsx`）」。
  - 左栏（`Queue.tsx`）：栏头「工单」+ 计数 +「⋯」菜单（ui `Menu`：立即检查 SLA 超时、工单设置）+「?」（`PageHelpTip align="start"` + `pageHelp('tickets', null)`，栏头靠左，浮层向右展开）；下面搜索、筛选页签（ui `Tabs`）、列表。列表行三行：优先级圆点 + 标题 + 等待时长 / 邮箱 · 分类（· 指派人）/ 状态 + 已升级 + 编号后 6 位 +「SLA 超时」。
  - 对话栏（`Detail.tsx`）：头部标题 + 编号、邮箱、创建时间；对话按天插日期分隔，消息带首字头像（kit `Avatar`），用户靠左、客服靠右、内部备注浅橙虚线框；底部 `Composer`。
  - 属性栏（`Props.tsx`）：处理（状态、指派、优先级只显示、分类；关闭原因写在「状态」一行下面）、时限（SLA 两行）、用户（邮箱、套餐、查看用户）、关联订单、升级按钮（ui `Button` 的 `icon` 带上箭头，对话头那颗同样）。
  - ≤ 1279：不渲染属性栏，状态 / 指派 / 升级挪进对话头，头部说明多显示套餐、分类、关联订单、关闭原因。状态、指派、升级与两个对话框的逻辑在 `Controls.tsx` 的 `useTicketControls`，同一时间只渲染一处。
  - ≤ 639：单栏，没选工单只显示列表，选中只显示对话；对话头最上面「← 返回工单列表」（保留筛选与搜索）。左栏头的标题、计数、「?」隐藏（外框顶栏已有标题），「⋯」挪到搜索框右侧。高度由外框 `.bleedCol` 给，页面不自己算。
- 快捷回复条只露前 4 个（`Composer.tsx` 的 `MACRO_CHIPS`，手机 2 个），多出的进「全部 N」浮层（带搜索，点外面或 Esc 收起，Esc 不冒泡）；「管理」弹窗是「列表 + 底部新增两个输入」，修改在单独的小弹窗里，没有排序输入（`sort_order` 新建取最大 +10、修改沿用原值）。Composer、MacroManager、工单设置对话框的样式在 `Composer.module.css`。
- 状态全在地址上：`#/tickets/<工单 id>?f=<筛选>&q=<搜索>`，列表只拼链接、不持有选中态。
- 侧栏工单提醒卡、仪表盘「待处理工单」格、导航徽标数的都是待办接口的 `tickets_open`，点进来一律带 `f=todo`（10-21：测试员从侧栏「7」点进来看到「全部 11」对不上）。栏头计数悬停写「「<筛选名>」N 张」；当前筛选不是待处理且待办数 > 0 时，计数旁多一个红底小按钮「待处理 N」（`.todoJump`），点了切到待处理。
- 队列的「未读」（行加粗）= `last_message_author_kind === 'user'` 且状态不是已解决/已关闭；`last_message_author_kind` 取最后一条**非内部备注、非系统消息**的作者（`repo/queue.go`），升级、改状态、撤回、自动关闭插的系统消息不会盖掉用户的追问（用户 2026-10-14 定）。mock 同口径。
- 队列与详情是同一个 Go 结构：`user_id`、`user_email`、`sla_first_response_due`、`sla_resolution_due` 没有 omitempty，zod 必填；队列的 `related_order` 恒为 null，详情按关联订单联表回 `{ id, order_no }`；详情的 `message_count` / `last_reply_at` 与队列同口径，直接用，不从 messages 推算。
- 列表的等待时长 = now − `last_reply_at`（「等 1 小时 30 分」），等待用户的写「已回复」，已结束的写「已结束」；「SLA 超时」看 `sla_breached` 且工单未结束（`model.ts` 的 `waitLabel`）。
- 「回复并解决」是先 reply 再 status 两把幂等键：第二步失败重试时不重发回复（`Composer.tsx` 记住本次意图的回复已送达）。
- 升级到 L2 只改状态与优先级、不发通知（D-B-6 已决），确认框保留，文案不承诺「通知二线值班」。关闭工单先弹对话框填说明（选填）。
- 「工单设置」对话框（`TicketSettingsDialog.tsx`）读写 `/v1/settings/support`，四个键整份提交（等待回复、自动关闭开关与时长、每人同时打开的工单上限 `max_open_tickets` 1–50）；关闭原因多了 `auto_closed`（「自动关闭」）。

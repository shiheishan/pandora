---
paths:
  - "panel/internal/module/support/**"
---

# 工单（support）

- 内部备注对用户不可见：门户的读取路径一律在 SQL 里带 `internal_note = false`，不在 Go 里过滤；门户里客服的作者名一律为 null，只给用户自己的显示名。
- 内部备注不改工单：不算首次响应、不改状态、不通知提单人，否则写条备注就能停掉 SLA 计时（`AgentReply`）。
- 状态六种（`StatusOpen`、`StatusPendingUser`、`StatusPendingAgent`、`StatusEscalated`、`StatusResolved`、`StatusClosed`）。转换走 `panel/internal/module/support/service/rules.go` 的转换表 `CanTransition`，写库带 `WHERE status = 旧状态`。
  - 客服能直接设的状态见 `agentSettable`；`pending_user` 只由客服公开回复驱动，手工设成「等待用户」却没有回复，用户会一直等。
  - 撤回不是新状态，是 closed 加原因。关闭原因四种（`ClosedByUser`、`ClosedWithdrawn`、`ClosedByAgent`、`ClosedAuto`），门户展示与解决率统计都靠它；转出 closed 时清空原因与说明。改状态时各列怎么写统一在 `agentStatusCols`，新增改状态的路径照它写，不要各写一份。
- 优先级由分类推导（`priorityFor`），用户不能自选；分类是封闭枚举。SLA 截止时间建单时按优先级算好（`slaDue`，首次响应时限必须小于解决时限）。
- SLA 升级是 asynq 定时任务 `support:escalate_sla`（每 5 分钟），只做「首次响应已超时且未响应 → escalated」这一件幂等的事，审计记 system；后台手动触发走同一个 `escalateOverdue`。
- 客服公开回复经 `ReplyNotifier`（notify 实现）在同一事务里给提单人排一条通知，去重键 `ticket-replied:<消息 id>`；通知写失败回复一起回滚。
- 写接口的幂等在业务同一事务里完成（service 里的 `completeIdem`，handler 传 `ReplyFunc`）；`Withdraw` 没有幂等。
- 工单正文可能含用户隐私：审计只记 id、状态、计数，不记正文、标题、撤回说明、改状态原因。
- 文本校验统一在 `rules.go`（`normalizeBody` 等）：去空白、按字符数限长、拒 NUL（PG 的 text 不收 NUL，放进库会变 500）。
- 后台队列的在用套餐名经 `PlanNames` 端口从 subscription 取，不直接读订阅表。
- 工单规则在 settings（`SupportRules`，经 `Settings` 端口读，缺省都关）：
  - 须等客服回复（`support.wait_for_reply`，Xboard 的 ticket_must_wait_reply）：最后一条公开消息（不算内部备注与系统消息，`LastPublicAuthorKind`）是用户自己的时，`UserReply` 回 `ErrWaitForAgent`（409），门户详情 `reply_blocked` 为真。
  - 每人同时打开的工单上限（`support.max_open_tickets`，缺省 `DefaultSupportMaxOpenTickets` = 5、1–50；替换了原来的 `MaxOpenTickets` 常量）：门户建单在 `LockUserTickets` 之后现读规则再数，到了回 409「您有 N 个进行中的工单…」（`errTooManyOpen` 按实际张数拼文案，不写死 5）；规则读出小于 1 时按缺省。
  - 自动关闭（`support.auto_close_enabled` + `support.auto_close_hours`，缺省 24、1–720）：asynq 定时任务 `support:auto_close`（每 10 分钟，`2-59/10`）把 `pending_user` 且 `updated_at` 早于 N 小时的工单关成 `auto_closed`，一轮最多 `AutoCloseBatch` 张（`FOR UPDATE SKIP LOCKED`，走 `tickets_auto_close_idx`），每张加一条系统消息、审计 `ticket.auto_closed` 记 system。
- 建单与用户回复在同一事务里经 `AdminTicketNotifier`（notify 实现）往管理员 Telegram 群组排提醒，写失败一起回滚；notify 那边没开开关时什么都不写。在 Telegram 里回复的客服回复与后台同走 `AgentReply`，`AgentReplyInput.Source = SourceTelegram`，审计 data 带 `via`。

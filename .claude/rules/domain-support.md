---
paths:
  - "panel/internal/domain/support/**"
---

# 工单

- 写操作都有 `*Atomic` 版本，把业务写入与幂等键的完成（`middleware.CompleteSuccessJSONInTx`）放在同一个事务里；新增写入口照此提供（守卫 `atomic_contract_test.go:TestSupportAtomicMutationContracts`）
- 工单审计不得记录回复正文，也不得记录可关联的正文哈希，只记字数（同一测试守住）
- 关闭有三种来源，由 `closed_reason` 区分：user_closed（用户关闭）、withdrawn（用户撤回）、agent_closed（客服关闭）。门户展示和解决率统计都靠它，所以每条关闭路径都要写对原因，任何把工单从 closed 改走的路径都要把它清空（`setStatus` 已这样做）。注意 `replyAsAgent` 的非内部回复会把 closed 工单改成 pending_user，目前没有清 `closed_reason`
- 撤回不是新状态，只是 closed 加一个原因（迁移 00046 的取舍：状态机被十几处引用）
- 内部备注不算对用户的响应：不计首次响应、不改工单状态、不通知提单人，用户侧在 SQL 层排除
- 客服非内部回复经 `ReplyNotifier` 在同一事务里给提单人排 ticket.replied（去重键 `ticket-replied:<消息 id>`），提交后再 Kick。`ReplyNotifier` 由 notify.Service 实现，承载后台工单回复的网关必须调 `SetReplyNotifier`
- SLA 超时升级由定时任务以 system 身份幂等扫描，不依赖 HTTP 幂等认领（守卫 `TestSchedulerEntryDoesNotRequireHTTPClaim`）
- 队列与详情里的 `user_active_plan` 取 `subscription.ActivePlanNameSQL`，与后台用户列表的 active_plan 同一口径

# panel/internal/domain/support/
> L2 | 父级: /panel/internal/domain/CLAUDE.md

工单域（OPS-001）：用户提单、双方回复、客服指派与改状态、超时升级。写操作都有 *Atomic 版本，把业务写入与幂等键的完成放在同一个事务里。关闭有三种来源，由 closed_reason 区分（user_closed / withdrawn / agent_closed），门户与解决率统计都靠它，每条关闭路径都必须写对、重新打开时清空。

成员清单
service.go: Service 骨架：NewService 与 SetReplyNotifier 注入（ReplyNotifier 由 notify.Service 实现，接口不反向依赖 notify）、按优先级算 SLA 截止、两侧共用的视图 Ticket / Message / TicketOrderRef、*Atomic 共用的预制响应、工单号生成与 TicketOwner
user_tickets.go: 用户侧：提单（标题可由正文推出）、列表与详情（内部备注在 SQL 层排除，详情回 related_order 与 closed_reason）、回复、关闭（closed_reason=user_closed）
agent_tickets.go: 客服侧：负责人目录（查询时校验权限绑定，不复用普通用户目录）、队列（status 可逗号多值，回 last_message_author_kind 并跳过内部备注）与详情（回 user_active_plan，message_count / last_reply_at 与队列同口径 R75）、回复与内部备注（非内部回复经 ReplyNotifier 在同一事务里给提单人排 ticket.replied，变量 subject、去重键 ticket-replied:<消息 id>，service 类别按偏好过滤，提交后 Kick，R115）、指派、改状态（closed_reason=agent_closed，重新打开时清空）
escalation.go: SLA 超时升级：定时任务以 system 身份幂等扫描，不依赖 HTTP 认领；后台人工升级走 *Atomic 并把优先级提到至少 high
withdraw.go: 用户撤回工单（closed_reason=withdrawn，可带说明），与用户关闭只差原因一列
macros.go: 客服快捷回复 ticket_macros（00078）的列表与增改删，租户共享、按 sort_order 排，长度校验与库内 CHECK 同值，写操作审计 ticket_macro.saved / deleted
*_test.go: 原子性与契约单元测试；support_pg18_test.go、closed_reason_pg18_test.go、macros_pg18_test.go、agent_queue_pg18_test.go 与 reply_notify_pg18_test.go（回复通知：回滚不排、同键重放不多排、内部备注不排、关掉 service 不排）为 PG18 集成测试（run-pg18-gates.sh 的 support 域）

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

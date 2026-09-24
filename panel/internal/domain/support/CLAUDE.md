# panel/internal/domain/support/
> L2 | 父级: /panel/internal/domain/CLAUDE.md

工单域（OPS-001）：用户提单、双方回复、客服指派与改状态、超时升级。写操作都有 *Atomic 版本，把业务写入与幂等键的完成放在同一个事务里。关闭有三种来源，由 closed_reason 区分（user_closed / withdrawn / agent_closed），门户与解决率统计都靠它，每条关闭路径都必须写对、重新打开时清空。

成员清单
service.go: Service 与全部工单用例：创建、用户读写与关闭、客服队列 / 详情 / 回复 / 指派 / 改状态、超时升级
withdraw.go: 用户撤回工单（closed_reason=withdrawn，可带说明），与用户关闭只差原因一列
macros.go: 客服快捷回复 ticket_macros（00078）的列表与增改删，租户共享、按 sort_order 排，长度校验与库内 CHECK 同值，写操作审计 ticket_macro.saved / deleted
*_test.go: 原子性与契约单元测试；support_pg18_test.go、closed_reason_pg18_test.go 与 macros_pg18_test.go 为 PG18 集成测试（run-pg18-gates.sh 的 support 域）

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

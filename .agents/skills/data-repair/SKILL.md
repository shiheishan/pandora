---
name: data-repair
description: pandora 数据巡检报警后怎么修脏数据：先确认是真问题还是巡检误报，再定位属主模块与根因，只用后台现成操作（属主 service 的入口，自带审计）、RUNBOOK 第 16 章给出的带旧值条件的 SQL 或补偿分录来修，不直接 UPDATE / DELETE 只追加的表；修复留审计或运维记录、可回查，修完重跑确认 SQL 并等下一轮巡检转绿，根因是代码缺陷时另开修复。生产库与测试机上的红线（只读先行、先备份、整段事务、口令只经 stdin 或 0600 文件、动手前问用户）都在这里。用户或告警说「巡检红了」「数据巡检发现异常」「integrity_checks 有违例」「仪表盘要处理里数据巡检标红」「余额对不上」「账本不平」「账本漂移」「冻结对不上」「付了钱没到账」「礼品卡兑换对不上」「修数据」「手工改库」时使用，即使只说「某个用户的钱不对」也先用它。
---

# 数据巡检告警后修数据

`pandora-worker` 每小时第 19、49 分跑一轮 `report:integrity`，九项跨表一致性检查的最新结果在 `integrity_checks`，变坏时发 Telegram 管理员群组、后台「仪表盘 › 要处理」出红格。实现口径见 `.claude/rules/module-report-audit.md`「数据巡检」。

**每项检查是什么、怎么确认、怎么处理、修好之后看什么，`panel/deploy/RUNBOOK.md` 第 16 章逐节写了，本 skill 不重抄。** 这里给的是 agent 的做事顺序、红线，以及 RUNBOOK 之外要补的两步：判断是不是误报、根因是代码缺陷时怎么另开修复。

## 红线（生产库与测试机）

先读 `~/ai/servers/README.md` 找到那台，再读它目录下的 `AGENTS.md`，按那里的流程登录；不凭 ssh 别名直接上去。

- **只读先行。** 第一步、第二步只跑 `SELECT`。RUNBOOK 的确认 SQL 用 `pg <<'SQL' … SQL` 一次跑完没问题（`pg` 的定义见 RUNBOOK「约定」）。
- **改库前停下问用户。** 改生产或测试机上的库是「修改本仓库以外的东西」，按根 CLAUDE.md 先把要执行的 SQL、影响的行、备份位置给用户看，等明确同意。后台操作（调余额、加时长、开单）同样先问。
- **先备份**：照 RUNBOOK「约定」`sudo systemctl start pandora-backup.service`，看到 `backup complete: <路径>` 再动手，路径记进运维记录。
- **一个事务，带旧值条件，核对行数。** `BEGIN;` → `SET LOCAL lock_timeout = '5s';` → `SELECT … FOR UPDATE` 看清当前值 → 每条 `UPDATE` 的 `WHERE` 带上刚查到的旧值 → 回的行数不对就 `ROLLBACK`。改库的块不要整段灌给 psql：进交互会话（不带参数的 `pg`）逐条粘贴，看清每条输出再往下。
- **只照 RUNBOOK 给的 SQL 改。** RUNBOOK 那一节写「升级处理」或没给 SQL 的，不自己写改库 SQL：带上确认结果交给用户，按第五步开修复。
- **只追加的表不改不删**（账本交易与分录、审计、兑换流水、预留流水、订阅事件等）。哪张表只追加以库里的触发器为准：
  `pg -c "SELECT tgrelid::regclass AS tbl, p.proname FROM pg_trigger t JOIN pg_proc p ON p.oid = t.tgfoid WHERE p.proname IN ('deny_mutation', 'deny_delete') ORDER BY 1;"`
  （`deny_mutation` 不许改也不许删，`deny_delete` 只许改状态不许删）。
  - **不许绕守卫**：不 `SET session_replication_role = replica`、不 `ALTER TABLE … DISABLE TRIGGER`、不为此换连接身份。那是测试夹具（`testkit`）和保留期清理函数的专用手段，生产上用了账就不可信。
- **口令只经 stdin 或 0600 文件。** `psql.sh` 自己从 `deploy.env` 读口令交给 psql，不 `cat` `secrets.env` / `deploy.env`，不把口令写进命令参数或输出。不读 `ops-local/**/secrets/`。
- **记录里不写现场值。** 样例 id、用户 id、订单号是现场数据：可以写进机器目录下的运维记录（`ops-local/<机器目录>/`，被 git 忽略），**不进仓库**、不进提交信息和 PR。仓库里只写检查代号与做法。

## 第一步：确认是真问题还是巡检误报

1. 看结果行与巡检本身在不在跑（RUNBOOK 16「怎么确认」那两条）：`count`、`sample`、`checked_at`、`failing_since`、`alerted_count`。`checked_at` 落后一小时以上，先查 worker（RUNBOOK 第 1 章），不是数据问题。
2. 拿 `sample` 里的 id，跑 RUNBOOK 对应小节的「确认」SQL。要按检查原样复核，读属主 `repo/integrity.go` 里那条检查 SQL（下表），只读地在库里跑一遍。
3. 按下面判断：

| 现象 | 结论 | 怎么办 |
|---|---|---|
| 现跑确认 SQL，样例已经一致 | 已自愈或瞬时（例如巡检那一刻任务正在处理） | 不动数据；等下一轮看是否回 0。同一项反复出现再按真问题查 |
| 确认 SQL 证实对不上 | 真问题 | 进第二步 |
| `subscription_expire_overdue` / `subscription_close_overdue` | 真问题，但是任务卡了，不是脏数据 | 不改库，按 RUNBOOK 那两节让到期扫描恢复 |
| `payment_unsettled` / `commission_unpaid_order` 补偿后计数不降 | 7 天回看窗口（`IntegrityWindow`），不是没修好 | 看 `sample` 有没有**新**的 id；旧的等滑出窗口 |
| 检查 SQL 本身的口径和 service 注释、规则写的不一样，或这一项刚加或刚改过（`git log -- panel/internal/module/<属主>/repo/integrity.go`） | 巡检误报（检查有 bug） | 不修数据。按第五步修检查：PG 用例造「正常数据」证明它被误判 |

拿不准就当真问题处理，但第三步之前先把判断依据给用户看。

## 第二步：定位属主模块与根因

| 代号 | 属主（检查 SQL） | RUNBOOK 小节 | 规则 |
|---|---|---|---|
| `ledger_drift`、`ledger_hold_mismatch` | ledger（`ledger/repo/integrity.go`） | 16 · ledger_drift、ledger_hold_mismatch | `module-order.md`（记账锁序）、`panel-migrations.md`（只追加） |
| `payment_unsettled` | payment 给收款（`payment/repo/integrity.go`）→ order 核状态（`order/repo/integrity.go` 的 `UnsettledAmong`） | 16 · payment_unsettled | `module-payment.md` |
| `order_paid_unfulfilled`、`order_reservation_mismatch` | order（`order/repo/integrity.go`） | 16 · 对应两节 | `module-order.md` |
| `commission_unpaid_order` | commission 给条目（`commission/repo/integrity.go`）→ order 核状态 | 16 · commission_unpaid_order | `module-order.md`（佣金部分） |
| `giftcard_redemption_mismatch` | giftcard（`giftcard/repo/integrity.go`） | 16 · giftcard_redemption_mismatch | `module-giftcard.md` |
| `subscription_expire_overdue`、`subscription_close_overdue` | subscription（`subscription/repo/integrity.go`） | 16 · 对应两节 | `module-subscription.md` |

找根因（都是只读）：

- **时间点**：`failing_since` 前后发生了什么。样例记录的创建、更新时间；同一来源的账本交易（`ledger_transactions` 按 `source_type`、`source_id`）；`audit_events` 里同一资源的动作。
- **请求**：审计或日志里的 `req_id`，`sudo grep -h '<req_id>' /var/log/pandora/serve.log /var/log/pandora/worker.log`。
- **有没有人手改过库**：问用户；看有没有不经面板的改动痕迹（`updated_at` 变了却没有对应的审计或事件）。
- **代码**：这段时间部署了哪个版本，属主模块那条写路径最近改过什么（`git log -- panel/internal/module/<属主>/service/`）。照规则文件里写的锁序与「同一事务」约定，找哪一步没在同一事务里、或被绕过了。

结论写成一句话：「<代号> 的 N 条，是 <哪条写路径> 在 <什么情况> 下只写了一半 / 被手改」，带上依据。

## 第三步：修

按这个先后，能用前一种就不用后一种：

1. **用户受影响的先补上**，用后台现成操作。它们走属主 service，自带校验、事务与审计：
   - 用户详情「调整余额」（ledger 的 `AdjustBalance`，记一笔 `balance_adjusted` 交易，原因 5 到 500 字）；「加时长」；「订单与收款 › 订单」的「人工开单」「取消订单」；「营销 › 佣金与提现 › 待复核」的「作废」。
   - 原因一律写成「数据巡检 <代号> <样例 id 或订单号>」，以后能按原因搜到。
   - 哪种情况用哪个、哪些操作**不能**用（例如别对已收款的单「标记已支付」，会重复记收款），照 RUNBOOK 对应小节。
2. **RUNBOOK 给了 SQL 的**（账本余额缓存校准、佣金转待复核、礼品卡码按流水改回、码放回未使用），照抄，按红线执行。只改可变的状态或缓存，从不改只追加的表。
3. **钱算错了要纠正**：只追加的账不改旧行，**新记一笔补偿**。现成入口只有后台调余额（用户余额科目）与营收报表调整的登记 / 冲正（`report/service/adjustments.go`，冲正 = 新插一行、金额取反）。别的科目要补分录、或要「重新履约」「释放冻结」，没有现成入口：不手记账，升级（第五步），由代码修复走属主 service 的记账路径。
4. **临时补偿要能对冲**：补了多少余额、几天、开了哪张单、审计原因，写进运维记录和升级说明。修复代码时要么先冲回这笔补偿，要么不再给用户发放，否则用户拿两份（RUNBOOK 各节都强调了这句）。

## 第四步：修完要能回查、要转绿

- **可回查**：后台操作已有审计。直接执行的 SQL（RUNBOOK 那几段）不留审计，执行前后在运维记录里写清：时间、备份路径、代号与样例 id、改前值与改后值、`UPDATE` 回的行数、谁同意的。
- **当场核对**：再跑一遍「确认」SQL，结果符合 RUNBOOK 那一节的「修好之后」。
- **等下一轮巡检**（下一个第 19 或 49 分）：`integrity_checks` 这一项 `count = 0`、`failing_since` 为空，后台「要处理」的格子不再红。巡检不能手动触发，就等这一轮；回看窗口类的两项按第一步的表看新样例。
- 过几轮又出现：不是修好了，按第五步升级。

## 第五步：根因是代码缺陷时另开修复

修数据只是止血。只要根因不是「有人手改了库」，就要有一个代码修复，交主 Agent 或用户排期：

- **复现**：在属主模块写 PG 用例，用产品路径造出这种半截状态，断言对应的检查方法计数 +1、样例里有它；正常路径计数不变（`module-report-audit.md`「加或改一项检查」那条的要求同样适用于修复）。
- **修写路径**：让那一步回到同一事务里，或补上漏掉的状态转移；改动碰钱、订单、账本，合入前走 pre-merge-review。
- **存量数据**：所有部署都可能有同样的脏数据时，用迁移修，走 new-migration skill 的「只修数据」样板。迁移只改可变的状态列，**不改只追加的表**；要补账的，在 Go 里经属主 service 记账（例如一次性任务或启动时的修复入口），不写在迁移 SQL 里。修复要把已经做过的临时补偿算进去（不重复发放，或先冲回）。
- **巡检本身误报**：修 `repo/integrity.go` 的 SQL 和 service 注释，PG 用例覆盖被误判的正常数据；代号不改（改了等于换一项新检查）。RUNBOOK 第 16 章那一节的口径同步改。
- 新发现的「怎么确认 / 怎么处理」写回 RUNBOOK 第 16 章对应小节（只写占位符，不写现场值）。

## 交回或报告里写什么

一张表：

| 代号 | 违例数 | 真问题 / 误报 | 根因（一句） | 做了什么（后台操作 / SQL / 无） | 审计原因或运维记录位置 | 下一轮结果 | 后续修复 |
|---|---|---|---|---|---|---|---|

样例 id 只写在运维记录里，这张表里用「样例 1」「样例 2」代指。

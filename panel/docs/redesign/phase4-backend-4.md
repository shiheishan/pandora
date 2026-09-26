# 面板重构 · 第 4 阶段 · 后端四：交付语义

> 协调会话写于 2026-09-25。worktree `../pandora-be4`，分支 `feat/panel-redesign-be4`，迁移号段 **00093–00098**。
> 先读 `phase3-common.md`（第 1–9 节与第 11 节），再读本文件。

## 负责范围

用户 2026-09-25 定案里会改变「哪些用户能连哪些节点」的三件事：无池节点不服务任何人、节点池限定用户组（D-B-3，R104）、设备识别窗口可选（D-B-4，R103）。这几件都直接改下发给节点的用户集合，出错就是一批用户断网或越权连上专属线路，所以单独一个会话做。

- 主要代码：`nodefabric/uniproxy.go`（`ListNodeUsers`、`PurgeStaleAlive`）、`subscription/service.go`（`listEligibleNodesTx` 与两个调用方）、`api/admin/pools.go`、`usergroup.go`、`devices.go`、`api/admin/handlers.go` 里节点列表的在线统计、相关迁移。
- 不归你：套餐目录、计费、节点 PATCH（后端三）。pdnd 不改：R103 已核实生产 NativeCore 不依赖 5 分钟窗口。

## 步骤（每步一个提交，做完停下报告）

| 步 | 内容 | 依据 |
|---|---|---|
| ① 无池节点与通知 | `ListNodeUsers` 去掉「节点 `pool_id` 为空视为公共节点」这一支，无池节点不下发任何用户；订阅下载、门户预览本来就不含无池节点，补测试钉住三处口径一致。`POST v1/plans/{id}/pools` 提交后发租户级 `node.users.changed`（现在不发）。检查仓库里依赖「无池节点对所有人开放」的测试、e2e 脚本（`tests/uniproxy_e2e.sh`、`node_e2e.sh`）并改掉，在报告里列出 | R104「无池节点」 |
| ② 节点池限定用户组 | 迁移建池与用户组的关联表（复合外键、租户 RLS、表登记簿与权限字典）；下发三处按 R104 规则过滤（`ListNodeUsers` 要联 `users`，`listEligibleNodesTx` 要带用户）；接口按 R104：`GET v1/node-pools` 的 `allowed_user_groups`、两个写接口的 `allowed_user_group_ids`（带了就要 reauth、写审计）、`GET v1/user-groups` 的 `exclusive_pools`、删用户组被引用时 409；池名单变化与用户换组后发 `node.users.changed` | R104 |
| ③ 设备识别窗口 | 设置键 `device_limit.window_minutes`（5 / 10 / 30 / 60，缺行按 5）；视图 `subscription_online_devices` 按租户读这个键；节点列表在线统计里写死的 5 分钟改用同一口径；`PurgeStaleAlive` 的截止不小于最大窗口；`GET v1/devices` 与 `POST v1/settings/device-limit` 按 R103 加字段 | R103 |

## 要求

- ② 的 reauth 按字段条件挂：若现有中间件做不干净（第 2 节约定不改 `middleware.RequireRecentReauth`），可以把两个池写路由整体挂 reauth，报告里说明，协调会话改契约。
- 每步都要有 PG18 测试覆盖下发结果本身，不只测接口：①无池节点的用户列表为空；②限定池只下发给名单内组的用户、默认组用户拿不到、未限定的池照旧、订阅下载与门户预览同口径、删组被拒；③窗口 5 与 30 分钟下同一批在线记录的在线数不同，strict 判定跟着变。
- 默认行为不变的地方要有测试证明：没配任何限定、窗口缺行时，下发结果与改动前一致（① 的无池节点除外，那是有意改变）。

## 进度与补充事项（协调会话维护，接力的新会话从这里接上）

**进度**：①–⑦ 已验收合入（最后 ⑦ 合并 837462e）。**第 5 阶段追加 ⑧–⑫（2026-09-26）**，见补充事项最前面「第 5 阶段」一条；原会话已归档，新会话从这里接上。迁移号段 00095–00098。

补充事项（与上文冲突时以这里为准）：
- 契约修订已到 R116，本轮从 R117 起。
- **第 5 阶段（2026-09-26，新会话接力）**：先读 `phase3-common.md` 第 12 节（与重构会话的文件分界）。用户定了修下面五件，都是协调会话调查过、根因已明的问题。⑧ 单独一个提交、单独报告（牵涉钱）；⑨–⑫ 每步一个提交，全部做完一起报告。每个提交推送后看三组 CI，PG18 必须 0 SKIP；本机全量用 `go test -p 1 -count=1 -timeout 15m ./...`。
  - **⑧ 订阅终态时的续费 / 变更结算（R117，迁移 00095）**。事实：`subscription_transitions`（00003:340-357）里 `expired` 没有出边、`cancelled` 只能到 `expired`；建续费单与变更单时校验了状态（`renewal.go:108`、`plan_change.go:132`），但**结算时不再校验**，`fulfillRenewalLocked` / 变更履约无条件写 `active`，订阅若在 30 分钟支付窗口内被改成终态，触发器报 23514，整个结算事务回滚，连 `payment_events` 都不落库；后台「标记已付」得到 500。产品里没有写这两个状态的路径，只有手工 SQL 能触发，但牵涉钱，要做完整。做法：在 `settlePaymentTx` 锁住订阅后（`checkout.go` 约 1277 行）按单据类型重新校验订阅状态，不合格**不回滚**，把钱隔离进挂账：迁移 00095 给 `late_payment_cases.case_kind` 加 `ineligible_subscription`（与 00040 的守卫同风格补一支守卫分支，Down 在有该类行时拒绝回滚），`unexpected_payment.go` 的白名单同步；回调回执 success（钱已有记录），后台「标记已付」回中文 409 说明「订阅已结束，款项已转入挂账」（或你按代码定更合适的语义，写进报告）；顺手删掉 `renewal.go` 里 `CASE ... status IN ('expired',...)` 永远走不到的 expired 分支。PG18：建续费单 → SQL 把订阅改成 expired → 回调 → 断言进挂账、订单与订阅不变、回执成功；cancelled 同样一条；变更套餐单一条；后台标记已付一条。
  - **⑨ 通知收件人哈希专用盐**。事实：admin 的 `notify.New` 直接用 `cfg.MasterKey` 当 HMAC key（`cmd/aegis-admin/main.go:144`，根密钥复用），public 借用 `crypto.SubscriptionAuditSalt`（`cmd/aegis-public/main.go:104/142`）。做法：照 `SubscriptionAuditSalt` 在 `platform/crypto` 加 `NotifyRecipientSalt(masterKey)`（域分隔串 `aegis/notify/recipient-salt/v1`），两个网关都改用；`cmd/aegis-admin/main_contract_test.go` 与 public 同类测试钉住装配。存量行不回填（这一列只写不读）。审计与访问日志里 IP 哈希直接用主密钥的 8 处**不动**（被 IP 聚类比对，换 key 要数据迁移，另议）。
  - **⑩ 回退值与种子一致 + 租户守卫测试**。事实：`auth.email_verification` 缺行时注册流程按 true（`identity/service.go:113`），后台邮件页按 false（`api/admin/mail.go:54`），种子是 false（00030 / 00042）；佣金缺行时代码回退冻结 3 天、最低提现 10000（`billing/commission.go:79-81`），种子是 7 与 1000（00028）。做法：回退值改成与种子一致，并收成一处常量（两个读取点共用）；加单测钉住「代码回退值 = 迁移种子值」。另加一条守卫单测：非测试源码（`internal`、`cmd`、`deploy/*.sh` 不含 `test-*`）出现 `INSERT INTO tenants`，或 `middleware.Tenant` 不再恒定注入 `DefaultTenantID` 时失败，失败信息写明「先扩 `app.seed_tenant_defaults`，至少补系统角色与权限」（新租户缺角色会建不出管理员）。多租户本身不做。
  - **⑪ 节点状态报错可能露英文（R116 遗留的 UNKNOWN）**。事实：admin `handlers.go` 改节点状态（约 1081 行）、`nodefabric/node_activate.go`（约 166 行）、`node_retire.go`（约 104 行）三处用 `db.Message(err)` 透传数据库错误；状态机触发器（00005）的报错是中文，但 nodes 表的 CHECK 约束（例如 `nodes_desired_effective_pair_check`）是英文原句。做法：列出这三条 UPDATE 能撞到的全部 CHECK / 唯一约束，逐个判定能否真撞到（写进报告）；照 R116 ⑥ `switchRefusal` 的写法按约束名给中文原因，未知约束写通用中文，原句 `WithInternal` 进日志；触发器的中文 RAISE 仍原样透传。能用 PG18 造出来的约束各补一条用例。**`handlers.go` 这次只改这一处调用**（重构会话在你合入后才拆它）。
  - **⑫ pdnd 兼容构建里 juicity 的默认工作目录**。事实：`pdnd/core/external/juicity.go:64` 缺省目录是 `/etc/aegis-nodeagent/juicity`，而 pandora-native 以 `pandora` 用户、`ProtectSystem=strict` 运行，只能写 `/var/lib/pandora-native`（`pdnd/release/pandora-native.service`），配置不写 `work_dir` 时 juicity 起不来。只影响 compat 构建（`core/multi` 调用）。做法：缺省改为 `/var/lib/pandora-native/juicity`，同文件与 `pdnd/core/CLAUDE.md` 的旧名注释顺带改；`go test -tags compat` 相关包跑一遍。`nodeagent/` 目录与其它 aegis-nodeagent 字样**不动**（等测试机定性）。
- ⑦ 的验收结论：删源码四个文件与 node_e2e.sh、三个部署脚本出列、runner 只编 payctl 且 SCRIPTS 剩五个、nodefabric 两处注释与 uniproxy_e2e.sh 的说明改成现状、全仓只剩 tests/CLAUDE.md 一句退役说明，都认可。Makefile 第 6 行泛称「Node Agent 分发」不是这个二进制，不动。
- **第 ⑦ 步：退役 aegis-agent（用户 2026-09-25 定）**。事实：服务端只收两阶段接入，`cmd/aegis-agent` 的 bootstrap 已写死成报错，角色由 pandora-native 接替，但它仍在发布包与安装脚本里；唯一专测它的 `tests/node_e2e.sh` 因此过不了（冒烟 ⑥ 查出）。接入、上线、心跳已由联调冒烟用真实 Ed25519 两阶段流程覆盖。一个提交做完：
  1. 删 `panel/cmd/aegis-agent/` 与 `panel/tests/node_e2e.sh`。
  2. 从 `deploy/build-release.sh`、`install-linux-binaries.sh`、`migrate-to-new-host.sh` 的二进制列表去掉它；`install-native.sh` 的那行注释、`run-smoke-e2e.sh` 里编译 aegis-agent 与 `/opt/aegispanel/bin` 准备（只为 node_e2e 存在的部分）一并清掉——这个文件归冒烟，这次授权你改，只删 aegis-agent 相关行。**还有第 106 行 `SCRIPTS=(…)` 列表里的 `node_e2e.sh` 也要删**（按「aegis-agent」grep 找不到它，但删了脚本不删这一项，runner 会报文件不存在）。
  3. 文档：根 `README.md` 二进制表、`panel/CLAUDE.md` 的 cmd 行（可执行入口数减一）、`deploy/CLAUDE.md`、`tests/CLAUDE.md`；`nodefabric/uniproxy.go:39`、`service.go:114` 两处注释里的 aegis-agent 改成现状（pandora-native / 两阶段接入）。
  4. grep 全仓（不含 docs/redesign 与 ops-local）确认不再有 aegis-agent；有 Go 测试或发布契约测试钉着二进制列表的同步改。`nodeagent/` 目录（aegis-nodeagent）**不在本次范围**，别动。
  5. 本机全量 + 推送看三组 CI（panel-smoke 里 e2e 表应剩五个脚本、全过），报告。
- ⑥ 的验收结论：AST 扫描 + 追调用方区分用户文案与机器文案、31 处改中文且沿用仓库已有措辞、降级开关按约束名翻译（约束名已在 00009 核对）且 PG 原句只进日志、两条契约测试（扫描器失效下限 300、豁免清单过期即红、反查豁免函数不被后台 / 门户调用）并做过变异验证、⑥ 放单独本地分支避免被冒烟带走，都认可。
- 已知未查（UNKNOWN，本阶段不查）：节点状态三处 `db.Message` 透传（admin `handlers.go` 改状态、`node_activate.go`、`node_retire.go`）理论上可能撞到 nodes 表的英文 CHECK 约束，页面会看到英文原句；契约测试管不到运行时文案。测试机人工点时留意。
- **第 ⑥ 步（R116，2026-09-25 指派）**：前端已删掉英文→中文文案映射，后端 4xx 响应里给用户看的 `message` 必须是中文。后端三已收工，**越界授权你做这一件**（全仓 `panel/internal` 的 httpx 错误文案，只改 message 字符串）。做法：
  1. grep 所有 `httpx.New` / `httpx.Invalid` 等构造与 `fields` 里的英文文案（协调会话粗扫 billing 与 api 下约 37 处），列成表：位置、原文、新文案、哪个前端页面会显示。首例是 `billing/checkout.go:1106` 的「unknown payment provider」→「支付渠道不存在」。
  2. 只改 message 字符串，`code`、HTTP 状态、响应形状一律不动；日志与 `httpx.Internal` 包装的内部错误不改；节点端（uniproxy、agent、node 网关）给机器看的文案不改；断言文案的测试同步更新。
  3. 加一条源码契约测试，防止面向用户的 4xx 文案再出现纯英文（白名单放不可避免的专有名词）。
  **时序**：你的 ⑤ 还没合入主线（在等冒烟翻断言），⑥ 先在本地提交、**不要推送**，等协调会话告诉你 ⑤ 已合入主线后再 `git merge feat/panel-redesign`、推送、看三组 CI、报告。
- ⑤ 的验收结论：窄接口 `ReplyNotifier{Enqueue, Kick}` 经 `SetReplyNotifier` 注入（support 不依赖 notify）、锁工单时顺带取提单人与标题、同事务排队且去重键带消息 id、提交后且确实排进才 Kick、内部备注不排、幂等与普通两条路径都覆盖、`main_contract_test.go` 钉住装配（根因就是没接线）、没在 admin 起派发循环（会吞掉 telegram / email），都认可。admin 里 Kick 是空操作、站内信最慢约 5 分钟可见：协调会话定为可接受，写进 R115 补，不另做跨进程唤醒。
- 你记的小事：admin 的 notify 用 `cfg.MasterKey` 做 salt、public 用 `subSalt`，同一用户的 `recipient_hash` 两边不同。这一列只写不读，**不改**，记在这里；以后谁要读这一列，先统一 salt。
- **第 ⑤ 步（R115，2026-09-25 指派）**：联调冒烟 ④ 实测「后台回复工单后门户收不到通知」，协调会话核实是缺陷。这本属后端三的范围（工单、通知），后端三已全部完成收工，**越界授权给你做这一件**，只动 `domain/support`、它与 `notify` 的接线（照 billing 的 setter 注入写法）和 `cmd/aegis-admin` 的装配。要求按契约 R115：非内部回复在同一事务里给提单人排 `ticket.replied`（变量 `subject`，去重键 `ticket-replied:<消息 id>`，类别 service 按偏好过滤），内部备注不发，提交后 Kick 一次派发；`ReplyAsAgentAtomic`（幂等包装）与普通路径都覆盖。PG18 测试：回复一条 → 提单人 inapp 队列多一条且变量正确；同键重放不多；内部备注不排；用户关掉 service 类别后不排。PG18 夹具前缀先 grep 全仓确认没人用。一个提交，推送看三组 CI，报告。
- **接力（2026-09-25）**：原会话上下文将满，用户在同一 worktree 开新会话**待命**。目前没有指派的活；联调冒烟 ④ 查出属于本范围（下发、节点池、用户组、设备、节点上线）的问题时，协调会话追加在这里再开工。待命期间不写代码，先 `git merge feat/panel-redesign` 跟上主线，读完本文件与 `phase3-common.md` 第 11 节就停。
- 用户定案（2026-09-25）：**节点池状态（draining / disabled）维持现状，不影响下发**，只是后台上的标签。不要改下发三处口径。
- ④ 的验收结论：与退役对称的单事务、逐边过触发器（临时删边能证明）、投影提到 nodefabric 只留一份、已 active 先于版本号幂等、服务器进 ready 不要求控制节点（与 `server_admin.go` 规则一致）、服务器新进 ready 时补发租户级通知、两种 warnings，都认可，已写成 R113。
- ③ 的验收结论：窗口唯一来源是库函数（STABLE，缺行与非法值按 5，不放宽不归零）、视图列不变让所有消费方自动跟随、节点列表改调同一函数、清理截止 70 分钟、`DeviceWindowMinutes` 只做写入校验与清理常量、单测钉住与迁移一致且源码不再写死窗口、Down 恢复写死 5 分钟并保留设置行，都认可，已写成 R111。`GET v1/devices` 读模式与宽容值吞错的既有写法不动。
- **④ 请尽快**：前端收尾 ③ 的上线按钮和设备窗口已写好，只等你的 ④ 合入主线就能一起合；冒烟也要在 ④ 之后改用新接口。
- ④ 的响应形状已更正（R110）：`AdminNode`，与 `POST v1/nodes/{id}/retire`、`PATCH v1/nodes/{id}` 同一个形状，另带 `warnings: string[]`。前端收尾 ③ 的上线按钮和设备窗口已写好在等你的 ③、④，请按顺序做完。
- ② 的验收结论：独立关联表而不用 policy 或数组列、`PoolAdmitsUserSQL` 只收两种写死参数组合（其余 panic）、`listEligibleNodesTx` 带用户并有契约测试钉住调用方、处理器里按字段判 reauth（与中间件同一个 `ReauthedRecently` 标志、先于一切校验与写入）、名单没变不审计不通知、删组被名单引用回 409 且外键兜底、换组成功即通知、顺手把两处非法路径 id 从 500 改成 404，都认可。名单上限 100 认可。已写成 R109。
- 你提的「节点池 status（draining / disabled）不影响下发」已报用户，用户定维持现状（见上）。
- **新增第 ④ 步：`POST v1/nodes/{id}/activate`（R108）**。前端收尾核对代码发现：接入流程只把节点推到 attesting，之后只有旧 `POST v1/nodes/{id}/status` 能往前推，而 `status:batch` 启用要求服务器 ready、服务器 ready 又要求名下有 active 节点，新服务器 + 新节点在新前端里上不了线。做法照 R57 退役：一个事务里按 00005 的合法边逐条推进到 active（每步过触发器）、`serving_status` 用 `projectNodeLifecycle` 同一套投影、服务器同事务进 ready、审计、提交后通知节点；已 active 幂等回 200；前置条件的具体判据按代码定，写进报告。PG18 测试要覆盖：新服务器 + 新接入节点调一次就能被下发用户（节点划进池、池绑到套餐）；每种不满足前置条件的情况回 409；非法状态不绕过触发器。
- ① 的验收结论：`ListNodeUsers` 去掉无池公共节点一支并加上 `pnp.tenant_id` 条件、`setPlanPools` 提交后通知、`NotifyUsersChanged` 放在 `nodestream.go`、后台节点列表 `DeliveryState` 加「是否在池」参数、新建 `delivery` 门禁域且过滤写精确，都认可。② 的池名单变化与用户换组一律用 `NotifyUsersChanged`；`delivery` 域的测试名单加新用例时同步更新过滤正则。
- `cmd/aegis-admin`、`cmd/aegis-public` 里履约通知手写的发布代码改用 `NotifyUsersChanged`，这件**交给后端三**（它第 ⑤ 步本来就要给赠送单加通知），你不要动 `cmd/`。
- 上线提醒（给协调会话与用户）：① 合入后，测试机上没划进节点池的节点会停止服务，部署前先把它们划进池。

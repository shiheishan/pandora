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

**进度**：①（合并 b3eea00，R105）、②（合并 4b21102，R109）、③（合并 2125afa，R111）、④ `f6ce152`（合并 b8f8520；PG18 run 36140269989：228 PASS / 0 SKIP / 0 FAIL，NativeCore 36140270056、panel-smoke 36140269900 全绿；契约 R113）已验收合入。**后端四四步全部完成，本会话不再开工。** 迁移剩 00095–00098 未用。联调冒烟查出属于本范围的问题时，协调会话在同一 worktree 另开会话处理。

补充事项（与上文冲突时以这里为准）：
- 契约修订已到 R113。
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

# 面板重构 · 第 3 阶段 · 后端二：平台、运营与安全

> 协调会话写于 2026-09-23。worktree `../pandora-be2`，分支 `feat/panel-redesign-be2`，迁移号段 **00074–00085**。
> 先读 `phase3-common.md`（公共规则、推送授权、共享文件约定），再读本文件。

## 负责范围

契约里这些模块的「待补·后端」和相关缺陷归你：

- admin：后台外壳、后台-01 仪表盘、后台-02 工单、后台-03 用户、后台-07 节点与服务器、后台-08 内容与外观、后台-09 通知与安全。
- public：门户外壳与认证、门户-01 概览、门户-02 我的订阅（按日流量接口除外，归后端一）、门户-07 工单、门户-08 消息、门户-09 帮助、门户-10 账号安全。
- 主要代码：`domain/identity`、`notify`、`support`、`content`、`appearance`、`nodefabric`（流量上报与配额扣减路径除外）、`adminops` 的用户 / 批量 / 仪表盘部分、审计与安全，以及对应处理器；还有 CI。

不归你：订单、支付、礼品卡、佣金、套餐、流量包、折算归后端一。D-D-1 只影响前端，后端不改。

## 步骤（每步一个提交，做完停下报告）

| 步 | 内容 | 依据 |
|---|---|---|
| ⓪ 数据库测试接进 CI | CI 里目前没有数据库，14 组 `*_pg18_test.go` 本机和 CI 都跳过。把 `panel/deploy/run-pg18-gates.sh` 接进 `.github/workflows/pandora-native.yml`：新 job 在 ubuntu 上用自带 Docker，按 go.mod 装 Go、装 goose，跑全部域。推送本分支验证。**现有套件在 CI 上如果红了，先报告失败原文，不要顺手大改**，协调会话判断是修还是暂时排除 | 公共规则 §4 |
| ① 安全 | D-B-1：`POST v1/subscriptions/{id}/rotate` 响应去掉 `token`；缺陷 1：注册验证码投递（迁移 M11 模板种子 + 入队发送）；缺陷 6：门户令牌不得列出或吊销后台会话（按 audience 过滤）；第 7.3 节里属于本范围的保护：批量导出 / 生成 / 群发与用户状态、设备上限设置挂 reauth 与正确权限，`server-token` 与设备上限写审计并拒绝已退役 / 已销毁节点、订阅不存在回 404，`reset-password` / `rotate` 改为先权限后 reauth，两条批量改节点状态路由统一幂等 scope，通知测试发送改用正确权限码 | 5.A、7.1、7.3 |
| ② 主题 | 迁移：删除 00051 默认主题与 00055 stellar 两个内置主题，新建内置主题「默认 · 纸白」并激活，tokens 用设计稿变量名、分 light / dark 两组，branding 放站点名称（Pandora）；custom_css 本期停用（保存时拒绝或忽略，写进契约报告）；缺陷 20：`POST v1/themes` 字段校验，违反 CHECK 不再回 500 | 5.A D-D-4 / D-E-4 |
| ③ 功能缺陷 | 缺陷 7（通知偏好 ON CONFLICT 必然 500）、8（用户列表用户组为空）、9（recent_orders 字段恒为零值）、14（访问日志分类筛选失效）、15（流量上报放大推送）、17（用户关闭工单不写 closed_reason）、18（单节点路由保存不通知节点、出站引用不认全局 tag）、21（节点列表硬上限 200 且 total 错误） | 7.2 |
| ④ 新表与新接口 | 迁移 M1 风控复核、M2 工单快捷回复、M5 帮助「有帮助」反馈、M6 审计 `auth_context`、M7 webhook 投递耗时、M8 节点国家与 server token 签发记录，及对应接口 | 第 6.1 节、各模块条目 |
| ⑤ 种子与其余扩展 | 迁移 M9 权限 `ops.dashboard.read`、M10 降级开关种子；本范围内其余「待补·后端」（仪表盘扩展字段、系统状态 components、工单队列与详情字段、升级为 escalated 时提升优先级、用户列表 / 详情 / 批量字段、节点列表字段与排序、事件 topic `switches.changed`、门户 me / 概览 / 消息 / 账号安全扩展等） | 各模块条目 |

## 验收

- 第 ⓪ 步：CI 上新 job 真正执行了 PG18 测试（日志里能看到各域 PASS，不是 skip），运行号写进报告。
- 每个安全修复都有反向测试：无权限 / 未 reauth / 错 audience 的请求被拒。
- 涉及 SQL 的都有 PG18 测试并登记进 `run-pg18-gates.sh`，推送后在 CI 上通过。
- 主题迁移的 Down 能恢复原内置主题；门户 `GET v1/appearance` 返回的 tokens 只含设计稿变量名。

## 进度与补充事项（协调会话维护，接力的新会话从这里接上）

**进度**：⓪–⑤ 全部验收并合入 `feat/panel-redesign`（⑤ 共 18 个提交，最后 `408d2e5`；PG18 run 36001623508：209 PASS / 0 SKIP / 0 FAIL）。迁移用到 00086（00083 站点时区、00084 M9、00085 M10、00086 审计链 v2；00086 超出原号段一位，与后端一的 00073 不冲突）。**后端二已结束，本会话不再开工。**

补充事项（与上文冲突时以这里为准）：
- 每步做完：推送本分支，看两个 workflow；做事前先 `git merge feat/panel-redesign` 同步。契约改动写进报告，编号由协调会话统一分配。
- 第 ⑤ 步：`GET v1/dashboard/tasks` 实现时，`withdrawals_pending` 挂 `marketing.commission.read`（后端一已把分销读权限统一到这个码）。
- `platform/httpx` 错误码与 `middleware.RequireRecentReauth` 仍归第 2 阶段第 ⑤ 步，不要碰。
- 新建租户拿不到通知模板（所有模板都有）：暂不处理。SanitizeCSS 保留不删。
- node_preview 的「从没心跳过的节点不下发」是有意规则，不是 bug。
- 契约修订已到 R80（R40–R46 是第 ④ 步）。新 PG18 测试一律用 `platform/pg18test` 辅助包；在已有域的包里加 PG18 用例时，把该域的测试名单写精确（默认过滤 `PG18` 会把别的域的用例拉进来、跳过、被判失败）。
- 本会话改过、还没有 L2 的目录（api/admin、api/public、platform/realtime）：按 GEB 逆向流，下次改到时再补，不用专门补。
- **第 ⑤ 步追加（后端一建议）**：`notify/scan.go` 的流量预警把用户的流量包剩余（`traffic_pack_grants` 的 remaining）算进可用量，有流量包余量的用户不该收到「流量即将用尽」；`deploy/configure-app-role.sql` 末尾的 REVOKE UPDATE/DELETE 名单补上 `traffic_pack_grants`、`gift_card_batches`（两表已靠触发器守住，这是纵深防御）。
- **第 ⑤ 步第一个提交：机械拆分 `api/admin/router.go`**（现 799 行，⑤ 一加路由就超 800）。按模块把路由段移到同包的 `router_<模块>.go`（函数接收同一个路由器与依赖），不改任何路径、中间件、顺序与行为，路由契约测试与权限字典测试原样通过；这个提交单独推送、在报告里单列，后端一第 ⑥ 步会在它合入后再往后台路由里加流量包管理。
- **第 ⑤ 步顺手修 `run-pg18-gates.sh` 的就绪检查**（第 ④ 步 CI 偶发失败的原因）：`docker exec … pg_isready` 走 unix socket，会连上 postgres 镜像初始化时的临时实例；改为 `pg_isready -h 127.0.0.1`（临时实例不监听 TCP），或等到连续两次成功。
- **第 ⑤ 步追加：修 `audit.VerifyChain`（2026-09-24 用户授权）**。现状：复算哈希用的是从 jsonb 读回的摘要，jsonb 会重排键、改空白，与写入时 `json.Marshal` 的字节不同，任何带摘要的审计行都验不过。修法由你定，但必须满足：
  - 写入与校验对同一条记录得到同一串字节，不依赖 jsonb 的存储形态（例如：哈希前把摘要规范化成确定的形式，或把参与哈希的原始字节另存一列）；
  - `audit_events` 是追加写 + 哈希链，**不得改写已有行**；迁移前的存量行怎么处理（如按版本区分口径、从某个切换点起按新口径校验）要明确，并写进 `platform/audit` 的 L3 与报告；
  - 与第 ④ 步的 `auth_context` 入链规则兼容；
  - PG18 测试覆盖：带摘要的多行链校验通过；改动任一行的摘要、auth_context 或删掉中间一行，校验能指出断点；存量口径的行按你定的规则处理；
  - 单独一个提交，报告里写清修法、存量行的处理和迁移号。
- 后台按文章统计「有帮助 / 没帮助」的接口契约未定，⑤ 不做。
- 第 ④ 步新建了 `api/admin`、`api/public`、`domain/plugin`、`domain/content` 四个 L2；`api/public/CLAUDE.md` 合并时协调会话补上了后端一的 `traffic_packs.go`、`plan_change.go` 两行。`nodefabric/node_admin.go` 1007 行、`adminops/service.go` 959 行，既有超限，不重构。
- 推送前对**最后一个提交**跑本机全量（见公共规则第 5 节新增条），CI 不跑 panel 单元测试。
- **第 ⑤ 步追加：站点时区（2026-09-24 用户定案，契约 R49、R50）**。现状：`users.timezone` 与 `tenants.timezone` 默认都是 `'UTC'`，Go 里没有任何地方能改，所以门户按日用量（后端一 00072）和后台收入趋势（`adminops/revenue.go`）都按 UTC 切日，国内用户差 8 小时。要做：
  - 迁移：`tenants.timezone` 默认值改为 `'Asia/Shanghai'`，存量仍为 `'UTC'` 的租户改成 `'Asia/Shanghai'`（迁移前没有任何写入口，存量 `'UTC'` 都是默认值）；Down 恢复默认 `'UTC'` 并把 `'Asia/Shanghai'` 改回 `'UTC'`。`users.timezone` 不动。
  - 接口：`GET / POST v1/settings/site`，形状、权限、reauth、审计见契约后台-08 同名条目；时区名用 `time.LoadLocation` 校验，拒绝空串与 `Local`。
  - 切日口径：改 `nodefabric/usage_daily.go` 的 `UsageLocation`（后端一的文件，后端一第 ⑥ 步不会碰它），用户时区为 `'UTC'` 时视同未设、走租户时区；补单元测试，并在 `TestUsageDailyWritePG18` 或新用例里覆盖「用户 UTC + 租户 Asia/Shanghai 按上海切日」。
  - **迁移号**：⑤ 现在要 M9、M10、站点时区三个迁移，正好用完 00083–00085；审计链修复如果也要迁移，**追加分配 00086**，不用再问。
- 第 ⑤ 步实现 `GET v1/system/status` 的 components 时按契约修订 R52：`metrics` 对象总是返回，down / unknown 时字段可缺失；`GET v1/dashboard/tasks` 的 withdrawals_pending 挂 `marketing.commission.read`（R51）。
- ⑤ 的结论（留档）：`api/admin/router.go` 已拆为 `router_<模块>.go`（85f9b0b）；审计链 v2（00086）：新增 `chain_seq`，新行按规范化摘要 + 长度前缀计哈希，存量行不改写、按旧口径校验，带摘要且对不上的旧行只核链接（`ChainReport.LegacyLinkOnly`）；00086 的 Down 删除 `chain_seq`，回滚后新口径行无法再校验。`configure-app-role.sql` 对 `traffic_pack_grants`、`gift_card_batches` 只收回 DELETE（扣量与批次导出要 UPDATE）。契约修订 R53–R62。
- 留给后续的：`GET v1/me/sessions` 的 `last_seen_at` 无写入点（R62）；admin `handlers.go`、`nodefabric/node_admin.go` 仍超 800 行。

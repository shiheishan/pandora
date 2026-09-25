# 面板重构 · 第 4 阶段 · 后端三：数据修复与商品

> 协调会话写于 2026-09-25。worktree `../pandora-be3`，分支 `feat/panel-redesign-be3`，迁移号段 **00087–00092**。
> 先读 `phase3-common.md`（第 1–9 节与第 11 节），再读本文件。

## 负责范围

- 三条会悄悄坏数据的遗留：节点 PATCH 清空密钥（R78）、套餐编辑向导三处（R92）、新建租户缺种子（R94、R97 与 offline 渠道、通知模板）。
- 用户 2026-09-25 的定案里属于商品与身份的：D-C-5 限速（R99）、D-E-3 卖点与推荐（R100）、D-B-2 重置密码不要原因（R101）、D-A-3 删三个未接入开关（R102）。
- 其余后端遗留（第 ⑤ 步列表）。
- 主要代码：`adminops`（套餐目录、向导、订单）、`api/public` 的套餐目录、`billing`、`notify`、`support`、`identity`、`nodefabric/node_admin.go` 的节点 PATCH、`api/admin` 对应处理器。

不归你：`nodefabric/uniproxy.go`、`subscription/service.go`、节点池、用户组、设备窗口，这些归后端四（第 11 节分界）。

## 步骤（每步一个提交，做完停下报告）

| 步 | 内容 | 依据 |
|---|---|---|
| ① 小缺陷 | 节点 PATCH 的 `protocol_config`：请求里没给的敏感键（读接口会抹掉的那些：password、private_key、psk 等）保留库里原值，显式给了才覆盖，补单元与 PG18 测试；钩子超时 / 重试次数越界回 422 而不是 500；门户单条标已读的 id 不是 UUID 时回 404 而不是 500 | R78、R93、R84 |
| ② 套餐向导与限速 | R92 三处：`PUT v1/plans/{id}/complete` 的 `max_devices` 与 `throttle_kbps` 改三态（缺省不动、显式 null 清为不限、正整数设置；Go 里要能区分「缺省」和「null」）；额度或线路变化开新版本时，新版本从当前版本继承宽限期、续费语义、权益等全部高级设置；资料整体写入时保留 `visible_from` / `visible_until`。D-C-5：迁移删掉 `plan_versions_throttle_exact`，`validateVersionSemantics` 去掉限速与策略耦合，新写入的 `overage_policy` 只收 `suspend`；向导的 `throttle_kbps` 正常生效；门户 `GET v1/plans` 加 `throttle_kbps` | R92、R99 |
| ③ 卖点与推荐 | 迁移给 `plans` 加 `highlights`、`recommended`；四个写接口与后台两个读接口、门户套餐目录按 R100 补字段与校验 | R100 |
| ④ 新租户种子与定案小项 | **先查清新租户由谁创建**（Go 里没有 `INSERT INTO tenants`；看安装脚本、迁移、adminctl），在报告里写明。然后让新租户和老租户一样能用：`offline` 支付渠道（缺它 mark-paid 与线下已收款会 404）、通知模板、`POST v1/settings/mail` 写 SMTP 密码改 upsert、降级开关 `POST v1/switches/{code}` 缺行时能切（upsert 或建租户时补种，选一种说明理由）。D-A-3：迁移删三个未接入开关，`tests/invariants.sql` 换用别的非核心开关。D-B-2：重置密码的 `reason` 改可选 | R94、R97、R102、R101 |
| ⑤ 其余遗留 | 后台工单详情补 `related_order` / `last_reply_at` / `message_count`（R75）；用户详情 `stats.paid_total` 按币种拆（R80）；取消订单、凭证号重复两处英文文案改中文（R95、R74）；订单列表行加人工单标识（R95）；赠送单履约后通知节点可服务用户变化（发租户级 `node.users.changed`，照 `billing/checkout.go` 的做法）；`GET v1/me/sessions` 的 `last_seen_at` 找个写入点（例如刷新令牌时节流更新，R62）。可选：change-plan/preview 回 coupon（R76）、门户佣金概况回 scope（R81）。另：第 3 阶段后端一记过一条推断——续费或变更单待支付的 30 分钟内订阅恰好过期，状态机不允许 expired → active，履约会失败。先写 PG18 测试复现，复现了再修，没复现就在报告里写明 | 各修订 |

## 验收

- 每条修复都有测试；①② 的数据保护要有反向测试（只改普通字段后密钥还在；编辑向导后权益、上架时间窗还在；null 能清回不限）。
- 带 SQL 的改动有 PG18 测试并登记进 `run-pg18-gates.sh`，域测试名单写精确。
- 契约与实现不一致、或 R99–R102 写得不对，写进报告，不要自己偏离。

## 进度与补充事项（协调会话维护，接力的新会话从这里接上）

**进度**：① `f4ad6e3` + `43ac5e3`（合并 24c2174，R106）、② `698617d` + `a49875a`（合并 cb861f6；PG18 run 36129026975：222 PASS / 0 SKIP / 0 FAIL，NativeCore 36129026978、panel-smoke 36129026983 全绿；契约 R107）已验收合入。**下一步 ③** 卖点与推荐（迁移 00088）。

补充事项（与上文冲突时以这里为准）：
- 契约修订已到 R109。
- ② 的验收结论：`mask_password` 抹敏与名单一致性测试（删键时能报出 trojan、vless 两处）、`mask` 开关门控补回（关掉掩码或切出 mKCP 不补口令，否则会 422）、00087 的 Down 在存量不符旧约束时拒绝回滚、三态传 0 回 422、新版本以当前版本为底稿继承全部设置与权益、流量没改原样照抄、保留上架时间窗、不限流量再提交 0 不白滚版本，都认可，已写成 R107。
- **③ 请尽快**：前端收尾 ② 的后台套餐 schema 已按 R100 把 `highlights`、`recommended` 写成必填，要等你的 ③ 合入主线才能一起合，免得真后端上套餐页读崩。
- ① 的验收结论：`PreserveRedactedProtocolSecrets` 与抹敏严格对称（缺席的补、显式给的以请求为准、数组同长才补、换协议类型不补、有重复键原样交给校验）、R93 越界 422、R84 非 UUID 404、PG18 用例改用 `8b…` 前缀、gitleaks 命中改假值而不加放行，都认可。R78 的事实更正已写进 R106。
- **② 之前（② 的第一个提交）**：`mask_password` 补进 `nodefabric/protocol_schema.go` 的 `sensitiveProtocolKey`。同时加一条单元测试：遍历 `ProtocolSchemas()` 里每个协议的 `SensitiveProperties`，断言都在抹敏键名表里——两份名单以后不会再悄悄分叉。PG18 测试的租户 id 前缀先 grep 确认没人用。
- 后端四已在 `nodefabric/nodestream.go` 加了 `Service.NotifyUsersChanged(ctx, tenantID)`（租户级 `node.users.changed`，合并 b3eea00）。第 ⑤ 步给赠送单加通知时用它；同一步顺带把 `cmd/aegis-admin`、`cmd/aegis-public` 里履约通知手写的发布代码改用它（行为不变）。做事前先 `git merge feat/panel-redesign`。

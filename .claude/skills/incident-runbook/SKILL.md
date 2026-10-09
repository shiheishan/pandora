---
name: incident-runbook
description: pandora 已装面板出故障时的分诊入口：从症状或告警（healthcheck.sh 巡检、aegis-tls-renew 失败、Telegram 告警）找到 panel/deploy/RUNBOOK.md 的那一章，跑一份只读快照和对应的只读查询把原因查清；需要写操作时转 panel-ops，并先问用户。用户或总协调说「面板 5xx / 502 / 打不开」「网关起不来」「管理员进不来」「节点离线、不上报心跳」「证书续期失败、证书过期、自签」「节点证书签不出来」「支付回调没到账、订单卡住」「通知发不出去、队列积压」「数据库连接满、库慢」「磁盘满」「备份没跑」「迁移失败、卡在预检、无效索引」时使用。CI 红（wait-status / wait-github 退出非 0、GitHub job 失败）用 ci-triage，不是本 skill；已经知道要做哪件运维操作用 panel-ops；只是查数据用 db-query。
---

# 面板故障分诊

目标：从一句症状走到 RUNBOOK 的一章、一份快照、一两条只读查询，拿到原因再决定要不要动手。原因表和处理步骤都在 `panel/deploy/RUNBOOK.md`，这里不抄。

## 先定三件事

1. **是不是 CI 红**：是就去 ci-triage。本 skill 只管已装好的面板（测试机、面板机）。
2. **哪台机器**：按根 CLAUDE.md，先读 `~/ai/servers/README.md` 和那台机器的 `AGENTS.md`，不凭 `~/.ssh/config` 里的别名直接上去。子 agent 沙箱连不上 1Password agent，ssh 要关沙箱，或者交给主会话。
3. **哪种布局**：`test -f /opt/aegispanel/deploy/.env && echo docker || echo native`。两种布局的差异见 RUNBOOK「约定」与 panel-ops「先认布局」。直装布局没有 `psql.sh` 和备份脚本。

## 第一步：快照（只读）

```bash
ssh <别名> 'bash -s' < .claude/skills/incident-runbook/scripts/snapshot.sh
```

一次拿到这些：

- failed 单元、三个网关和 nginx 的状态、续期 / 备份 / 巡检 timer 是否启用；
- 9000、9001、9003 的 healthz，以及 9000、9001 的 readyz；
- 容器或数据库服务的状态；
- `edge-tls.sh status` 与 `/var/lib/aegispanel/tls/status`；
- 磁盘、最新备份的年龄；
- 三份网关日志里关键报错的计数、节点验签失败按原因的分布、最后一条「启动失败」；
- nginx 状态码分布。

日志只看最后 5000 行，要看更多加参数：`'bash -s -- 20000'`。

快照只打计数，不打日志原文：网关日志里请求的 `path` 可能带订阅令牌。要看某条报错的上下文，按 `request_id` 在机器上单独 grep，贴给用户前先脱敏。

## 第二步：按症状对号

查询都经 db-query 的 `q.sh` 跑（`-t <别名>`，docker 与直装两种布局它都认），连接、身份、坑见 db-query：

```bash
q=.claude/skills/db-query/scripts/q.sh
bash $q -t <别名> .claude/skills/incident-runbook/queries/<文件>.sql
bash $q -t <别名> .claude/skills/db-query/queries/<文件>.sql
```

| 症状 / 告警 | RUNBOOK | 快照里先看 | 查询 | 要写时（先问用户） |
|---|---|---|---|---|
| 5xx、打不开、`服务 … 状态为`、`端口 … 探活返回` | 第 1 章 | 服务状态、探活、`启动失败`、`请求失败` 计数、nginx 5xx | 依赖不通时转第 8 章的查询 | 重启网关、`docker compose up -d`：RUNBOOK 第 1 章「处理」 |
| 管理员进不来 | 第 2 章 | 证书来源是不是 `selfsigned`；9001 探活 | `incident-runbook/queries/admin-login-failures.sql` | 改密、建号、授权：panel-ops 命令表「可逆」 |
| 节点离线、`过去 30 分钟没有任何节点上报心跳` | 第 3 章 | 9003 探活、证书、`节点请求验签失败` 的原因分布、`节点请求验签暂不可用` | `db-query/queries/nodes-online.sql` | 重启 pdnd、修时钟：RUNBOOK 第 3 章「处理」；重新注册走后台节点详情「签发一键安装令牌」；装到一半卡住用 enrollment status / abort（panel-ops）；单元漂移按 `pdnd/release/README.md` |
| 证书续期失败、过期、`HTTPS 证书续期出错`、`timer 超过 36 小时没跑`、aegis-tls-renew failed | 第 4 章 | 证书段整段；`aegis-tls-renew.timer` 是否 enabled | 无 | `edge-tls.sh renew / issue / setup`：panel-ops 命令表「可逆」 |
| 节点证书签不出来 | 第 5 章 | admin.log 的证书相关计数 | `incident-runbook/queries/node-certs.sql` | 只在后台「网络 › 证书」操作，没有命令行 |
| 支付回调没到账、订单卡住 | 第 6 章 | `支付回调…` 与 `主动查单巡检失败` 计数 | `incident-runbook/queries/payment-callbacks.sql`（`-v no=<订单号>` 看单张）；`db-query/queries/orders-status.sql`；账不平看 `ledger-reconcile.sql` | 后台「向渠道查单」「转入余额」「手工标记已支付」；商户密钥在后台改，不用 `aegis-payctl --key` |
| 通知积压、`通知排队超过 30 分钟`、`通知发送失败` | 第 7 章 | public 探活、`通知派发失败` / `通知投递失败` 计数 | `incident-runbook/queries/notify-backlog.sql` | 后台改渠道配置、降级开关；failed 的不重发，不改库 |
| `数据库连不上`、库慢、连接满 | 第 8 章 | readyz、容器状态 | `db-query/queries/connections.sql`、`slow-queries.sql`（要 pg_stat_statements）、`incident-runbook/queries/table-sizes.sql` | `pg_cancel_backend` / `pg_terminate_backend`、重启库、开 pg_stat_statements：都先问 |
| 磁盘满、`/ 已用`、`/tmp 已用` | 第 9 章 | 磁盘段、备份目录大小、未加密升级前备份的个数 | `incident-runbook/queries/table-sizes.sql` | 删备份、`journalctl --vacuum-size`、`docker image prune`：先问；永远不跑 `docker system prune --volumes` |
| 备份没跑、`没有任何备份`、`最新备份 … 小时前` | 第 10 章 | `aegis-backup.timer` 是否 enabled、最新备份年龄 | 无 | `enable --now aegis-backup.timer`、`backup-postgres.sh`、`verify-backup.sh`：panel-ops「备份与恢复」 |
| 迁移失败、卡在预检、`INVALID indexes found` | 第 11 章（细节在 `MIGRATION-RUNBOOK.md`） | 无（看安装器或发布控制器的输出） | `migrate.sh version`、`check-indexes`（panel-ops 命令表「只读」） | `DROP INDEX CONCURRENTLY`、`rollback-to`、恢复：panel-ops「破坏性」，按 MIGRATION-RUNBOOK |

## 写操作的规矩

- 本 skill 里只跑只读命令。查清原因后，要做的写操作在 panel-ops 里找命令、环境和成功判据。
- 动手前向用户说清楚：命令是什么、影响面（谁会断线、哪些数据回到哪一刻）、能不能撤回。等用户明确同意，一次同意只管这一次。
- 破坏性操作的顺序（问 → 备份 → 停写入者 → 执行 → 核对）按 panel-ops「破坏性操作的顺序」。
- 不手改订单、支付、账本、审计表；不清 Valkey 键，只有用户同意后删单个键（panel-ops）。

## 坑

- **巡检从 w10rel 起随发布包装并启用。** `healthcheck.sh` 与 `aegis-health.timer` 由安装器首装与升级时装上、`enable --now`（首跑在启用后 10 分钟）。之前的版本装的机器上没有它，快照里 `aegis-health.timer` 显示 inactive 时先看面板版本：旧版是常态，新版就是装失败（安装日志里有「没能启用健康巡检」）。
- **网关日志不在 journal 里。** `journalctl -u aegis-public` 只有启停记录，输出在 `/var/log/aegis/*.log`。证书续期和备份才看 journal（`aegis-tls-renew`、`aegis-backup`）。
- **nginx 的 503 不一定是网关坏了。** `limit_req` 超限默认就回 503；节点路径按「来源 IP + 节点」限速，压测或一台机器上跑很多节点时常见。
- **节点证书签发失败不会让节点离线。** 集中签发的证书还没下发到节点（`docs/node-certificates.md` 开头的说明）。节点全掉，先查面板 HTTPS 证书（第 4 章）和 aegis-node。
- **`audit-node-panel-link.md` 里的问题大多已修。** 推送 panic、节点无本地缓存、Valkey 挂了 aegis-node 起不来、库抖动回 401，现在代码里都已处理。引用前先 grep 当前代码核实。
- **输出里有面板地址、邮箱、订单号、来源 IP**：汇报时脱敏或只给计数，不进仓库和公开报告。

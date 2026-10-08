---
name: db-query
description: pandora 查库与查 Valkey：怎么连（本地数据基座、测试机/面板机、对照机 bench-pg）、以什么身份查（超级用户 vs aegis_app + 租户，RLS 下查不到不等于不存在）、常用只读查询文件（订阅分布与即将到期、某用户的订阅/配额/流量包/余额、订单状态、账本对平、节点在线、审计链、慢查询、连接数、限流键与剩余冷却）。看哪条 SQL 慢用本 skill，改写前后对比用 bench-eval。用户或总协调说「查数据」「看库里现在什么状态」「核对订阅/订单/余额/节点」「慢查询」「连接数」「限流键」「为什么这个用户查不到」时使用。只读；要写库、清键、开 pg_stat_statements 先问用户。
---

# 查库、核对状态

目标：不用现编 SQL 与连接命令，几分钟内知道「库里现在什么样」。写库一律不在本 skill 里做。

## 一条命令

```bash
bash .claude/skills/db-query/scripts/q.sh [-t 目标] [-v 名=值]... .claude/skills/db-query/queries/<文件>.sql
```

| `-t` | 连到哪 | 前提 |
|---|---|---|
| `local`（缺省） | 本机 `panel/deploy/psql.sh`，即 docker 容器 `aegis-postgres`，读 `panel/deploy/.env` | 本机装了 Docker 并 `make up`（在 `panel/` 下）。2026-10-07 实测这台 Mac 的 PATH 里没有 docker、psql、valkey-cli |
| `<ssh 别名>` | 面板机/测试机：远端先找 `/opt/aegispanel/deploy`，再找 `/opt/pandora/deploy`；docker 布局走 `psql.sh`，直装布局走 `runuser -u postgres psql` | 别名在 `~/.ssh/config`；ssh 需要 1Password agent，子 agent 沙箱里连不上，要关沙箱或交主会话 |
| `bench` | 对照机容器 `bench-pg`，超级用户 `postgres`，`-d` 选库（缺省 `aegis`，即 5k 基线库） | 只读用；做 EXPLAIN 对照改走 bench-eval，不要改 `aegis`、`aegis_train*`、`aegis_holdout*` |

脚本做的事：去掉注释和字符串后，文件里有写类关键字（insert/update/delete/create/drop/alter/grant/copy/lock/do 等）就拒绝；发送前先 `SET default_transaction_read_only = on`；`-v` 的值只许字母数字和 `. _ : @ + -`；口令不经本机（远端 `psql.sh` 自己读 `.env`）。这道闸只是兜底，不是授权。

## 身份与 RLS

业务表全部 `FORCE ROW LEVEL SECURITY`，策略是 `tenant_id = app.current_tenant_id()`，后者读会话变量 `app.tenant_id`，没设就是 NULL，**默认一行都看不到**。

| 身份 | 怎么得到 | 用在哪 |
|---|---|---|
| 超级用户（docker 里是 `.env` 的 `POSTGRES_USER`，缺省 `aegis`；直装是 `postgres`） | `psql.sh` 缺省 | **不受 RLS 约束**，看得到所有租户。用于排查：存在性、跨租户、`pg_stat_*`、`pg_stat_statements`。不能用来证明「产品也看得到」 |
| `aegis_app` + 租户 | 事务里 `SET LOCAL ROLE aegis_app` 再 `SELECT set_config('app.tenant_id', '<租户 id>', true)` | 复现产品看到的数据、核对 API 返回值、判断某条业务查询会不会漏行。对账与状态类查询都用它 |

要点：

- 默认租户 id 是 `00000000-0000-7000-8000-000000000001`（迁移 00010 种下，Go 里是 `middleware.DefaultTenantID`）。查询文件都以 `tenant` 变量覆盖它。
- `SET ROLE` 不带角色级配置。运行角色在本库上有 `search_path`、`jit = off`、`statement_timeout = 15s`（`deploy/configure-app-role.sql`），查询文件开头都手动 `SET LOCAL` 补上；`timeout` 变量可调大。`aegis_app` 被收回了 TEMPORARY 权限，别建临时表。
- 运行角色下 `users.email = '…'`（citext）走不了索引，要写 `email_lower = lower('…')`（00119）。超级用户查邮箱随便写。
- 「查不到」先跑 `identity-check.sql`：超级用户视角有行、`aegis_app` 无租户 0 行、设租户后有行，就是会话没设租户或租户填错。
- 超级用户查得到而产品里看不到，用 `aegis_app` 重查一遍再下结论。

## 查询文件

文件开头都写了用途、身份、变量。身份一栏：`app` = aegis_app + 租户，`超` = 超级用户。

| 文件 | 身份 | 变量 | 回答什么 |
|---|---|---|---|
| `identity-check.sql` | 超→app | tenant | 现在是谁、RLS 挡没挡 |
| `sub-status.sql` | app | tenant | 订阅按状态分布；已到期没被扫成 expired 的；expired 里还能原地续费的 |
| `sub-expiring.sql` | app | days=7 | 未来 N 天到期的订阅清单与按日汇总 |
| `user-overview.sql` | app | uid 或 email | 一个用户的账号、订阅、配额、流量包、余额与冻结额、最近订单、在线设备 |
| `orders-status.sql` | app | days=7 | 订单状态分布；过期没清、processing 卡住、预留到期没释放 |
| `ledger-reconcile.sql` | app | timeout | 账本漂移、余额方向、已付订单缺分录、收入与订单总额、渠道实收与应付 |
| `nodes-online.sql` | app | tenant | 节点在线数（心跳 90 秒内）、应在线却失联、degraded |
| `audit-chain-links.sql` | app | timeout | 审计链的行数、序号连续、prev_hash 接得上 |
| `slow-queries.sql` | 超 | n=20 | pg_stat_statements 总耗时/平均/调用数前 N |
| `connections.sql` | 超 | 无 | 连接数与预算、按用户和状态、长事务、锁等待 |

例：

```bash
q=.claude/skills/db-query
bash $q/scripts/q.sh -t <面板机别名> $q/queries/sub-status.sql
bash $q/scripts/q.sh -t <面板机别名> -v email=someone@example.test $q/queries/user-overview.sql
bash $q/scripts/q.sh -t bench -v n=10 $q/queries/slow-queries.sql
```

读结果的几条口径（都对着代码核过）：

- **配额**：`quota_balances.remaining` 是生成列 `limit_value + granted_addon + adjusted - consumed`，`limit_value` 为空是不限量。`granted_addon` 自 00070 起恒为 0（CHECK 钉死），加量包在 `traffic_pack_grants`，挂用户不挂订阅、永不过期、剩余 = `granted_bytes - consumed_bytes`；节点下发看的是配额余量加流量包余量。
- **余额**：用户科目是贷方科目，余额 = `-ledger_accounts.balance_signed`（`user_balance`）；下单时冻结的额度在 `user_balance_hold`。
- **到期**：订阅到点由 aegis-admin 的过期扫描翻成 expired（`billing.ScanExpiredSubscriptions`），expired 满 30 天写 `renewal_closed_at` 并吊销凭据；`sub-status.sql` 第 2 段的谓词与扫描逐字相同。
- **对账**：`ledger-reconcile.sql` 前两段应为空；后三段是候选异常，种子数据、迁移造的订单会命中，先看 `kind` 与 `paid_at`。借贷配平靠延迟约束触发器，不用 SQL 复核。
- **审计链**：SQL 只能核序号和链接。重算 `entry_hash` 的完整校验只有 Go 的 `audit.VerifyChain(ctx, tx, tenantID)`，没有命令行入口，只在 PG18 测试里被调用（`platform/audit/chain_pg18_test.go`）。
- **在线节点**：`last_heartbeat_at` 在 90 秒内（`nodefabric.NodeStaleAfter`），且只有 `serving_status = 'active'` 才算「应在线」。
- **连接预算**：算式与各网关缺省上限见 `panel/deploy/.env.example` 的连接池注释。网关经 unix socket 连库时 `client_addr` 为空。

## 慢查询（pg_stat_statements）

- 扩展没开时 `slow-queries.sql` 第 1 段就报错，不是脚本坏了。开关要改 `shared_preload_libraries` 并**重启 PostgreSQL**（网关断几秒）：面板机上用 `panel/tools/loadtest/scripts/pgstat.sh enable --yes`（root；`reset` 清零、`export DIR` 导 CSV），生产或测试机上的这一步先问用户。
- 计数是自上次 reset 起累计的，改前改后对比要先 reset 再跑负载。
- 对一条慢语句做改前改后 EXPLAIN，用 bench-eval，不在这里做。

## Valkey

连接信息在面板机的 `deploy/.env`（`VALKEY_PASSWORD`，缺省端口 6380，本地 compose 另有 unix socket `deploy/run/valkey/valkey.sock`，权限 700，属主之外只有 root 连得上）。口令只经环境变量 `REDISCLI_AUTH`，不进命令行。从本机用 `scripts/valkey.sh`（在面板机上以 root 跑，docker 与直装两种布局都认）：

```bash
v=.claude/skills/db-query/scripts/valkey.sh
ssh <别名> 'bash -s -- rl'                  < $v   # 列出全部 rl:* 键：计数、PTTL、剩余秒
ssh <别名> 'bash -s -- rl sub_rotate'       < $v   # 只列某个维度
ssh <别名> 'bash -s -- cli PTTL rl:sub_rotate_gap:<用户 id>' < $v
ssh <别名> 'bash -s -- cli INFO memory'     < $v
```

### 限流键

- 键形是 `rl:<name>:<后缀>[:<窗口号>]`（实现在 `panel/internal/middleware/ratelimit.go`、`middleware.go`），另有 `aegis:node-nonce:…`（节点 nonce 防重放）、`rt:sse:conns:<实例>`（SSE 连接数，`rt:*` 另有同名 pub/sub 频道，不是键）。
- 后缀里有冒号和 `|`（IP 前缀、`METHOD:/path|IP`），别按冒号切。键名和维度的对应用 `grep -rn 'middleware.By.*("<name>"' panel/internal` 找。
- 冷却式键（`AsCooldown`，不带窗口号，如 `sub_rotate_gap`）：`PTTL` 毫秒数减 1000 就是面板提示「请 X 分钟后再试」用的值（`rejectMessage`）；`valkey.sh rl` 的 `retry_after_s` 就是这个数，对固定窗口键只是上界参考。
- 固定窗口键：键名末段是窗口号 N，窗口秒数 W 取自源码里该维度的声明，解封时刻 = `(N+1)*W`（Unix 秒），计数超过该维度的 Max 才会 429。
- 限流只有 Valkey 可用时才生效：不可达时 `RateLimit` 放行，`RateLimitStrict`（匿名写入口）回 503。Valkey 配了 `allkeys-lru`、96MB，键可能被淘汰，没有键不等于没限过。

清键只有开发基座用 `panel/deploy/clear-ratelimit.sh`（删光 `rl:*`）；`FLUSHALL`、`FLUSHDB` 在 compose 里被改名禁用。测试机和生产上不要清，要清先问用户。

## 坑

- **多语句 SQL 用文件，经标准输入送**：`psql.sh < 文件.sql`。docker 布局下 `psql -f` 和 `\i` 指的是容器里的路径，宿主文件读不到。
- **口令**：`.env` 是 0600，只由远端脚本读；不要 `cat .env`，不要把口令写进命令行（`PGPASSWORD=… psql` 会进进程表）。要从本机直连的现场口令只经 0600 文件或标准输入，来源在 `ops-local/` 与 `~/ai/servers/`，不读 `ops-local/**/secrets/`。
- **输出含邮箱、订阅 id、订单号**：不贴进仓库和公开报告，汇报时脱敏或只给计数。
- **生产/测试机上不跑写语句**：包括 `pg_stat_statements_reset()`、`pg_terminate_backend`、`DEL`。排障需要写，先向用户说明语句和影响面。
- **对照机 `aegis` 库的结构停在 10-06 的快照**（goose 97），新迁移加的列它没有：`sub-status.sql`、`user-overview.sql` 用到的 `renewal_closed_at`（00124）、`nodes-online.sql` 用到的 `runtime_status`（00122）在那里报「column does not exist」。这是库旧，不是查询错；查这几项改连跑着当前版本的测试机或本地库。10-07 实测：10 个查询在对照机跑通 7 个，剩下 3 个在当前版本的测试机上全部跑通。

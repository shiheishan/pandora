# perfseed

P8 性能测试造数工具：在按 `panel/deploy/install.sh` 装好的**空面板**上批量创建服务器、节点、套餐、用户、订阅与历史业务数据。

推荐顺序三步：**infra → 启动 nodesim 并全部 connected → activate → users**。缺省规模：200 台服务器（每台 1 个节点）、5000 用户、30 天按天用量历史；`--expired-percent` 缺省 10% 用户订阅已过期。

---

## 1. 做什么

| 步骤 | 子命令 / 动作 | 作用 |
|------|----------------|------|
| 1 | `perfseed infra` | 经后台 API 建用户组、节点池、服务器与节点，签发接入令牌，写出 `tokens`、`nodesim.env` 等 |
| 2 | `nodesim` | 用令牌接入面板 gRPC，保持**全部**服务器 `connected`（activate 前必须完成） |
| 3 | `perfseed activate` | 节点上线、建并发布套餐，写出 `catalog.json` |
| 4 | `perfseed users` | 用属主角色直连 PostgreSQL，一个事务写入用户与历史数据，写出 `users.csv`，并推进节点名单纪元 |

---

## 2. 前置条件

- 面板已按 **`panel/deploy/install.sh`** 安装，库中**尚无订单**（空业务数据）。
- 已用 **`pandora admin create`**（或安装器引导）创建至少一名管理员。
- **在面板机上执行**（后台默认只监听 `127.0.0.1:9001`，infra / activate 走本机后台地址）。
- 构建 perfseed（在仓库 `panel` 目录下，工具链与发布一致）：

```bash
cd panel && GOTOOLCHAIN=go1.26.10 go build -o perfseed ./tools/perfseed
```

nodesim 在 `pdnd/release/nodesim` 目录构建（与 P8 计划一致），例如：

```bash
cd pdnd/release/nodesim && GOTOOLCHAIN=go1.26.10 go build -o nodesim .
```

---

## 3. 口令与密钥文件

### 口令文件（权限 `0600`）

不要用 `echo` 把口令留在 shell 历史里。推荐：

```bash
install -m 600 /dev/null /root/perf/admin.pass
install -m 600 /dev/null /root/perf/user.pass
# 用编辑器写入单行口令后保存
```

- **admin.pass**：管理员口令，供 `infra`、`activate` 的 `--admin-password-file`。
- **user.pass**：所有 perf 用户共用口令，供 `users` 的 `--user-password-file`。

工具会检查文件权限：组或其他用户有任何读/写/执行位则拒绝。

### 数据库与机密

| 参数 | 说明 |
|------|------|
| `--dsn-file` | 属主角色 **`pandora_owner`** 的连接串。可直接指向 **`/etc/pandora/deploy.env`**（其中 `PANDORA_DATABASE_MIGRATION_URL=...` 会被解析）；或单独一个只含一行 DSN 的 `0600` 文件。 |
| `--secrets-env` | 缺省 **`/etc/pandora/secrets.env`**。`users` 从中读取 **`PANDORA_SECRETS_MASTER_KEY`**（封存订阅链接令牌）和 **`PANDORA_REDIS_URL`**（推进名单纪元）。 |

`deploy.env`、`secrets.env` 通常仅 root 可读，因此 **`users` 子命令用 `sudo` 跑**（仍建议口令文件属主与 perfseed 运行用户一致且为 0600）。

---

## 4. 三步命令示例

下面目录示例为 **`/root/perf`**，后台地址 **`http://127.0.0.1:9001`**。先准备：

```bash
mkdir -m 700 /root/perf
export PERF=/root/perf
export ADMIN=http://127.0.0.1:9001
```

### 4.1 infra

```bash
./perfseed infra \
  --admin-url "$ADMIN" \
  --admin-email 'admin@example.com' \
  --admin-password-file /root/perf/admin.pass \
  --out-dir "$PERF" \
  --servers 200
```

| 参数 | 说明 | 缺省 |
|------|------|------|
| `--admin-url` | 后台基地址，不带 `/v1`，末尾 `/` 可写可不写 | （必填） |
| `--admin-email` | 管理员邮箱 | （必填） |
| `--admin-password-file` | 管理员口令文件 | （必填） |
| `--out-dir` | 输出目录（会 `chmod 700`） | （必填） |
| `--servers` | 服务器台数，每台 1 个节点 | **200**（范围 1–1000） |

完成后按终端提示启动 nodesim（见下一节）。

### 4.2 启动 nodesim（infra 与 activate 之间）

从 **`$PERF/nodesim.env`** 读取变量（与 `tokens` 配套生成）：

```bash
set -a
source /root/perf/nodesim.env
set +a

mkdir -m 700 /root/perf/nodesim-state

./nodesim \
  --panel "$PANEL" \
  --ca-sha256 "$CA_SHA256" \
  --tokens-file /root/perf/tokens \
  --state-dir /root/perf/nodesim-state \
  --status-file /root/perf/nodesim-status.json \
  --report-interval 10s \
  --traffic-users 5 \
  --log-level info
```

| nodesim 参数 | 说明 | 缺省 |
|--------------|------|------|
| `--panel` | 面板节点通道 `host:port` | （必填，来自 nodesim.env） |
| `--ca-sha256` | 接入 CA 指纹 `sha256:…` | 无身份时必填（来自 nodesim.env） |
| `--tokens-file` | perfseed 写的 `tokens` 文件 | （必填） |
| `--state-dir` | 每台服务器身份目录根 `D/<名字>` | （必填） |
| `--status-file` | 状态 JSON 路径 | 空则不写文件 |
| `--report-interval` | 流量与在线上报间隔 | **10s** |
| `--traffic-users` | 每节点每轮上报用户数 | **5** |
| `--duration` | 运行时长，`0` 表示直到 SIGINT/SIGTERM | **0** |
| `--log-level` | `debug` \| `info` \| `warn` \| `error` | **info** |

**在跑 `activate` 之前**，确认状态里**全部**服务器已 `enrolled` 且 `connected`（可参考 `node_smoke_e2e.sh` 里对 status JSON 的等待逻辑）。

### 4.3 activate

```bash
./perfseed activate \
  --admin-url "$ADMIN" \
  --admin-email 'admin@example.com' \
  --admin-password-file /root/perf/admin.pass \
  --out-dir "$PERF"
```

参数与 infra 相同，但没有 `--servers`。

### 4.4 users（整点到期突发示例）

```bash
sudo ./perfseed users \
  --dsn-file /etc/pandora/deploy.env \
  --secrets-env /etc/pandora/secrets.env \
  --out-dir "$PERF" \
  --user-password-file /root/perf/user.pass \
  --users 5000 \
  --history-days 30 \
  --expired-percent 10 \
  --seed 1 \
  --expire-at 2026-10-20T12:00:00Z \
  --expire-count 1000
```

| 参数 | 说明 | 缺省 |
|------|------|------|
| `--dsn-file` | 属主 DSN 或 deploy.env | （必填） |
| `--secrets-env` | secrets.env 路径 | **`/etc/pandora/secrets.env`** |
| `--out-dir` | 读 `catalog.json`、`infra.json`，写 `users.csv` | （必填） |
| `--user-password-file` | 用户共用口令文件 | （必填） |
| `--users` | 用户数 | **5000**（1–100000） |
| `--history-days` | 按天用量往回造多少天 | **30**（0–400） |
| `--expired-percent` | 已过期用户占比（末尾用户） | **10**（0–50） |
| `--seed` | 随机种子（时间线与用量可复现；uuid 与令牌仍随机） | **1** |
| `--expire-at` | RFC3339，与 `--expire-count` **成对**出现 | 省略 |
| `--expire-count` | 把前 N 个**生效中**用户的订阅到期时间改为 `--expire-at` | **0**（与 `--expire-at` 同用） |

`--expire-at` 须晚于当前时间至少 10 分钟，且不能超过当前时间 365 天；`--expire-count` 不能超过「生效中」用户数（`users - users*expired_percent/100`）。

---

## 5. 重跑与幂等

| 子命令 | 行为 |
|--------|------|
| **infra** | 按 `infra.json` 续建未完成的服务器/节点；`tokens_done=true` 且 `tokens`、`nodesim.env` 都在时，再跑会提示**已经做过**并跳过。若 `infra.json` 里的服务器已多于本次 `--servers` 则报错（令牌还没签完时少于则按新台数补建；签完后再跑一律报「已经做过」，不看 `--servers`）。 |
| **activate** | 跳过 `infra.json` 里已 `activated` 的节点；`catalog.json` 已存在则**不再建套餐**。 |
| **users** | 整库写入在**一个事务**里，失败整笔回滚，可直接重跑。若库里已有 `perf-*@example.com` 用户且 **`users.csv` 仍在**，只推进名单纪元并退出。若库里有 perfseed 用户但 **`users.csv` 丢失**，报错（订阅令牌只在造数时写入 CSV），需**换空面板**重来。 |

中途失败且 `infra.json` 丢失、但面板上已有 `code=perf` 的用户组或池时，infra 会提示换空面板重来（409 与本地状态不一致）。

---

## 6. 输出文件

均在 **`--out-dir`** 下，写入时权限 **0600**（目录 0700）。

| 文件 | 格式 / 用途 |
|------|-------------|
| **tokens** | 每行：`名字 令牌`（名字 `s0001`…，与 nodesim 身份目录名一致）。供 **nodesim** `--tokens-file`。 |
| **nodesim.env** | `PANEL=…`、`CA_SHA256=…`（可 `source`）。供 **nodesim** 与运维脚本。 |
| **admin.env** | `ADMIN_EMAIL=…`。供 **k6** 等后台脚本。 |
| **infra.json** | 用户组、池、每台服务器/节点 id、cipher、`tokens_done` 等进度。infra / activate 续跑用。 |
| **catalog.json** | 已发布套餐 code、plan_id、价格 id 等。users 读套餐定义用。 |
| **users.csv** | CSV 表头 `user_id,email,password,sub_token`。供 **k6** 订阅拉取等。 |

### users.csv 行顺序（与生成器一致）

1. 用户下标 **1 … expire_count**：订阅**生效中**，且 `current_period_end` 被设为 **`--expire-at`**（整点突发用）。
2. 中间用户：生效中，正常到期时间。
3. 末尾 **`users × expired_percent / 100`** 个用户：订阅 **`expired`**，拉订阅应得到过期提示。

邮箱形如 **`perf-00001@example.com`** … **`perf-05000@example.com`**。

### 订阅 URL 形状

```
<public_base_url>/<订阅路径前缀>/<sub_token>
```

- **`public_base_url`**：站点对外地址（安装时写入配置，示例占位 `https://panel.example.com`）。
- **订阅路径前缀**：站点设置 **`subscription.path_prefix`**（12 位小写十六进制，首次使用时由面板生成）。

查询前缀（在面板机上，走超级用户 `psql.sh`）：

```bash
sudo /opt/pandora/deploy/psql.sh -tAc \
  "SELECT value #>> '{}' FROM settings WHERE key = 'subscription.path_prefix'"
```

若查不到，可在门户打开一次「添加到 App」相关页面，让面板生成该设置后再查。

---

## 7. 造出来的数据

### 7.1 表与内容概览

| 表 | 写入内容概要 |
|----|----------------|
| **users** | `perf-%@example.com`，`active`，指定 perf 用户组，带 `email_verified_at` |
| **user_passwords** | 所有用户同一 Argon2id PHC（与面板 identity 参数一致） |
| **subscriptions** | 每用户 1 份，关联 catalog 套餐；`active` 或 `expired`；含周期、快照金额、 `proxy_uuid` |
| **subscription_credentials** | 每订阅 1 条活跃凭据；`token_hash` / 封存 `token_encrypted` |
| **quota_balances** | 按套餐配额定义：`cycle` / `total`，消耗与按天用量对齐 |
| **orders** | 每订阅 1–3 张（0–2 次提前续费 + 首购），**`fulfilled`** |
| **order_items** | 订单行快照（套餐名、版本、权益/配额 JSON 等） |
| **order_reservations** | 预留 **`captured`** |
| **order_reservation_events** | 每个预留 `reserve` + `capture` 两条事件 |
| **payments** | **`offline`** 渠道、`succeeded` |
| **payment_events** | `payment.succeeded`，已验签、已处理 |
| **ledger_accounts** | 按需确保 `channel_cash(offline)`、`platform_revenue(main)` |
| **ledger_transactions** | 每单一笔 **`order_paid`** |
| **ledger_entries** | 每笔交易：借 channel_cash、贷 platform_revenue（金额与订单一致） |
| **subscription_events** | `activated`、可选 `renewed`、`expired` |
| **traffic_reset_logs** | cycle 配额在续费时 **`renewal`** 重置记录 |
| **subscription_usage_daily** | 仅 **`traffic.bytes`**；每天 ≤ 2 GiB，且不超过对应限额的一半 |
| **audit_events** | 注册、下单、支付成功、订阅过期等 |

历史订单在业务语义上等价于：**用户下单 + 后台线下标记已支付**（`offline` 渠道）。账本每笔 **`order_paid`**：借 **`channel_cash(offline)`**、贷 **`platform_revenue(main)`**，并在同事务更新科目余额缓存。

订阅：**0–2 次提前续费**；`cycle` 配额随期滚动，并写 **`renewal`** 流量重置日志；用量按站点时区逐日累加。

### 7.2 为何 SQL 直写仍能保证一致

- 金额恒等式、订单状态形状、复合外键、只追加表、账本配平等约束在 **PostgreSQL** 里（含延迟触发器检查借贷配平）。
- **`users` 全部 INSERT/UPDATE 在一个事务**中完成，任一行违反约束则**整笔回滚**。
- 不经业务 service 的副作用按「历史已发生」处理：前置条件要求**空面板**（`orders` 计数为 0），付款通知扫描游标只回看约 **2 小时**，不会给 perfseed 历史单补发通知；perf 用户无邀请关系，**不产生佣金**。

---

## 8. 名单生效

`users` 提交成功后，对 Redis 执行（与面板 `node/service/epoch.go` 相同）：

- `INCR node:users_epoch`
- `PUBLISH node:changed users`

节点会话管理器收到通知后会尽快重算名单。若推进失败，工具会报错并说明库与 users.csv 已写好；这时节点要等最多 15 秒的全量比对才看到新用户，也可以 **`systemctl restart pandora-serve`**，或直接重跑 `users`（已经做过时只推进名单纪元）。

---

## 9. 跑完怎么核对

以下在面板机上执行；`psql.sh` 生产布局为 **`/opt/pandora/deploy/psql.sh`**（亦可用仓库内 `panel/deploy/psql.sh` 并设置 `PANDORA_DEPLOY_ENV`）。

### 9.1 行数

```bash
sudo /opt/pandora/deploy/psql.sh -tAc \
  "SELECT (SELECT count(*) FROM users WHERE email LIKE 'perf-%'), \
          (SELECT count(*) FROM subscriptions), \
          (SELECT count(*) FROM subscriptions WHERE status='expired'), \
          (SELECT count(*) FROM orders), \
          (SELECT count(*) FROM subscription_usage_daily)"
```

期望（缺省参数）：perf 用户 **5000**、订阅 **5000**、过期约 **500**（10%）、订单与订阅续费次数相关（多于 5000）、按天用量行数 > 0（`history-days=30` 时）。

### 9.2 库不变量

```bash
sudo /opt/pandora/deploy/psql.sh -X -v ON_ERROR_STOP=1 \
  -f /opt/pandora/tests/invariants.sql
```

发布包若无 `tests` 目录，从仓库拷贝：

```bash
sudo /opt/pandora/deploy/psql.sh -X -v ON_ERROR_STOP=1 \
  -f /path/to/panel/tests/invariants.sql
```

（`invariants.sql` 全程在一个事务里试插虚构用例、末尾 ROLLBACK，不留数据；它核对的是约束都在且生效。perf 数据能在这些约束下整笔提交，本身就说明满足它们。）

### 9.3 账本漂移（结果必须为 0）

与 `panel/tests/e2e.sh` 第 6 节口径一致：

```bash
sudo /opt/pandora/deploy/psql.sh -tAc "
SELECT count(*) FROM ledger_accounts a
  LEFT JOIN (
        SELECT account_id,
               coalesce(sum(amount) FILTER (WHERE direction = 'debit'), 0) AS d,
               coalesce(sum(amount) FILTER (WHERE direction = 'credit'), 0) AS c
          FROM ledger_entries GROUP BY account_id) s ON s.account_id = a.id
 WHERE a.balance <> CASE WHEN a.account_type IN ('channel_cash','platform_fee_expense','platform_promo_expense','suspense')
                         THEN coalesce(s.d,0) - coalesce(s.c,0)
                         ELSE coalesce(s.c,0) - coalesce(s.d,0) END"
```

### 9.4 后台与 nodesim

- 管理后台 **概览**：**`ledger_drift_accounts`**（或同等指标）应为 **0**。
- **nodesim** 状态 JSON（`--status-file`）：各节点 **`user_count`** 应等于当前名单中的生效订阅数（perf 套餐均挂在 perf 池上，以各节点 `users` 列表或计数为准）。

---

## 相关文档

- 文件名常量与各表的固定列：`types.go`
- P8 测试计划：`docs/perf-test-plan.md`
- 冒烟里的 nodesim / activate 顺序：`panel/tests/node_smoke_e2e.sh`

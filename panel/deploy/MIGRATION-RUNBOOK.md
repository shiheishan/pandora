# 迁移失败与回滚 runbook

面向自己部署 Pandora Panel 的运维。三种情形：升级时迁移失败、回到指定版本、从升级前备份恢复。
命令里的路径按 install.sh 的布局（`/opt/aegispanel`）写；install-native.sh 装的机器换成 `/opt/pandora`。
这份手册随发布包分发（`deploy/MIGRATION-RUNBOOK.md`），两个安装器都会把它装到 `/opt/aegispanel/deploy/`（或 `/opt/pandora/deploy/`）下。

## 0. 先弄清三件事

- **迁移是逐个事务执行的。** goose 每个迁移一个事务（标了 `-- +goose NO TRANSACTION` 的除外）。一次 `up` 里第 N 个失败时，第 N 个整体回滚，前面 N-1 个已经提交。失败后库停在「最后一个成功的迁移」上，不是升级前的版本。
- **看当前版本**（只读，随时可跑）：

  ```bash
  cd /opt/aegispanel/deploy
  PANDORA_LOCAL_MIGRATION_APPROVED=yes GOOSE_BIN=/opt/aegispanel/bin/goose ./migrate.sh version
  PANDORA_LOCAL_MIGRATION_APPROVED=yes GOOSE_BIN=/opt/aegispanel/bin/goose ./migrate.sh status
  ```

  `.env` 里有 `AEGIS_MIGRATION_DATABASE_URL` 时不需要 `PANDORA_LOCAL_MIGRATION_APPROVED`。`GOOSE_BIN` 指向发布包自带的 goose；没有就去掉这一项，用机器上的 goose。

- **升级前一定有备份。**
  - 发布控制器 `release-stop-the-world.sh` 在停服之后、迁移之前跑 `backup-postgres.sh`，产物是加密的 `aegis-postgres-<时间>.dump.age`，输出里那行 `backup complete: <路径>` 就是它。
  - `install.sh` 升级时先做 `pg_dump -Fc`，放在 `/var/backups/aegispanel/pre-upgrade-<时间>.dump`；`install-native.sh` 放在 `/var/backups/pandora/pre-upgrade-<时间>.dump`。

### 发布控制器的输出怎么读

控制器失败时打印 `rollback=<结论>`，对照下表决定走哪一节：

| 输出 | 发生在哪 | 库的状态 | 下一步 |
|---|---|---|---|
| `rollback=not_required` | 停服之前（含一次性库预检失败） | 没碰过 | 服务一直在跑。看报错修好再发 |
| `rollback=writers_and_ingress_restored` | 停服之后、迁移之前（含停服后核对不过） | 没碰过 | 旧服务已自动拉回。看报错修好再发 |
| `rollback=restored_before_migration` | 换完目录、迁移之前 | 没碰过 | 旧目录与旧服务已自动恢复 |
| `rollback=manual_required` | 正式迁移已经开始 | 可能已提交了一部分 | 写入者保持停止，按第 1 节处理 |
| `rollback=prohibited_after_exposure` | 新版本已经接过流量 | 新结构上已有新数据 | 不要自动回滚，按第 2 节评估，优先前滚修复 |

### 预检在什么时候跑

- 控制器在**停服之前**跑一次性库预检（`install.sh` 与 `install-native.sh` 的升级也是同一个顺序，见下一条）：把正式库整库克隆到同一个 PostgreSQL 里的临时库，在克隆上按「写入者已停」的口径演练待执行的迁移。通过后在只有 root 能读的发布暂存目录里留一张预检凭据。
  - 好处：停服时间里不再包含克隆和演练。5k 规模实测，这一步约 15 秒，原来占停服的三分之二，库越大越长。
  - 代价：克隆发生在业务时段，会给正式库带来一次整库读。大库请挑低峰发版。
- **停服之后**只做一次只读核对，亚秒级，结果与凭据逐项比对：
  - 迁移目录摘要：演练过的就是要跑的；
  - 源库的 goose 水位：中间没人迁移过；
  - 续费闸门：全部写入者停止之后，未关联的在途续费仍是 0；
  - 停写演练与正式迁移一致；
  - 凭据在六小时内。

  任何一项对不上就拒绝，控制器自动拉回旧服务（`rollback=writers_and_ingress_restored`），重新发版即可。
- `install.sh` / `install-native.sh` 升级时同样是「停服前完整预检 → 停服 → 迁移只核凭据」（共用 `install-lib.sh` 的 `pandora_run_migrations`）：
  - 预检失败：报「停服前的迁移预检没通过：服务没停，数据库没动」，服务一直在跑，按预检输出处理后重跑安装脚本；
  - 停服后核对不过或迁移失败：报「迁移失败，服务已拉回原来的版本」。新程序在迁移成功之后才装，拉回来的是原来的版本。
  - 全新库（还没有 goose 记录）没有要保护的数据，跳过克隆预检。
  - 两种布局都能跑：docker 布局在 `aegis-postgres` 容器里克隆；直装布局（`install-native.sh`）用本机的 `psql` / `pg_dump`，经 `127.0.0.1:POSTGRES_PORT` 以 `postgres` 超级用户（`.env` 的 `POSTGRES_SUPER_PASSWORD`）在同一个 PG18 集群里克隆，不需要 Docker。
- 预检凭据只对这次发布有效，不要手工复制或改写它。
- 直接调用 `migrate.sh up` 而不带凭据时，它照旧在调用当下做完整预检。

## 1. 情形一：升级时迁移失败

现象：

- 控制器输出 `rollback=manual_required` 和 `FAIL-CLOSED after migration attempt`；
- 或者 `install.sh` / `install-native.sh` 报「迁移失败，服务已拉回原来的版本」并贴出完整输出。

1. **保持写入者停止，不要重启服务。**
   - 控制器已经停了写入者，入口（nginx）也没恢复。
   - 安装脚本会把原来版本的服务拉回来（新程序还没装）。失败的迁移之前已经提交了几个时，旧程序跑在更新过一部分的结构上：立刻 `systemctl stop aegis-public aegis-admin aegis-node`，按下面处理完再启动。
2. **记下现场。**
   - 用第 0 节的 `migrate.sh version` 看当前版本；
   - 从输出里找出失败的迁移文件名和报错，例如 `ERROR 00135_xxx.sql: ... (SQLSTATE 55P03)`。
3. **判断报错类型：**
   - **锁等待超时**（`lock_timeout`，SQLSTATE 55P03）或**语句超时**（`statement_timeout`，57014）：说明还有连接占着表锁，或者表比预想的大。
     - 先查 `pg_stat_activity`，确认没有残留连接；
     - 原样重跑 `migrate.sh up`：失败的那个迁移已经整体回滚，重跑是安全的。
   - **数据不满足新约束**（23xxx）：存量数据和迁移的假设不一致。
     - 不要手改迁移文件；
     - 按报错修数据（走正常业务流程或经过评审的修复 SQL），再重跑 `up`。
   - **迁移里的守卫主动拒绝**（P0001，报错里写明原因）：照报错里给的处置做。
4. **三条出路，按优先级选：**
   1. **前滚**（首选）：修好原因后重跑 `up`，到最新版本，再启动新服务。
   2. **回到旧版本**：数据不能前滚、又必须马上恢复服务时，用第 2 节的 `rollback-to` 回到旧发布的版本号，再装回旧二进制。
   3. **从备份恢复**：上面两条都走不通，或者涉及 irreversible 迁移时，按第 3 节恢复升级前备份。
5. `-- +goose NO TRANSACTION` 的迁移（例如 `CREATE INDEX CONCURRENTLY`）失败时可能留下半成品，例如 `INVALID` 状态的索引：
   - 先 `DROP INDEX CONCURRENTLY IF EXISTS <名字>`，再重跑；
   - 这类迁移的 Up 必须能重入，DDL lint 也按这个要求检查。
   - **不清理就重跑是错的，而且不会报错。** 这类 Up 写的是 `CREATE ... CONCURRENTLY IF NOT EXISTS`：INVALID 的索引也算「已存在」，重跑直接跳过，goose 照样把版本记成已执行，留下一个永远不生效的索引。唯一索引不生效就等于没有唯一约束。在 5k 副本上实测过（00136，见第 4 节）。
   - 所以 CONCURRENTLY 的迁移失败后，无论之后是重跑 `up` 还是重跑安装脚本，都先查一遍：

     ```bash
     cd /opt/aegispanel/deploy
     ./psql.sh -c "SELECT indexrelid::regclass, indisvalid, indisready FROM pg_index WHERE NOT indisvalid OR NOT indisready;"
     ```

     有行就逐个 `DROP INDEX CONCURRENTLY IF EXISTS public.<名字>;`（不能放在事务里，`psql -c` 单条执行即可），确认这条查询返回空，再重跑。
   - 重跑之后再跑一次同一条查询，应为空。

## 2. 情形二：回到指定版本（rollback-to）

用于：新版本上线后有问题，要把库退回旧版本的结构，再装回旧二进制。
`migrate.sh` 的公开入口一直拒绝 `down` 和 `redo`，回滚只能走 `rollback-to`。

**用新发布的 `migrate.sh` 和 `migrations/` 执行。** 要撤销的迁移和它们的 Down 段都在新发布里，旧发布的目录里没有这些文件。

前提：

- 入口和全部写入者已经停止：`systemctl stop nginx aegis-public aegis-admin aegis-node`。
  - 脚本要求设 `PANDORA_ROLLBACK_WRITERS_STOPPED=yes`；
  - 有 systemd 时它还会自己核实三个写入单元确实没在运行。
- 刚做了一份备份，用 `PANDORA_ROLLBACK_BACKUP` 指向它。Down 会删列、删表，新版本写进去的数据随之消失，这份备份是退路：

  ```bash
  cd /opt/aegispanel/deploy && ./backup-postgres.sh   # 记下输出的 backup complete: <路径>
  ```

- 目标版本 = 旧发布 `migrations/` 里最大的迁移号，例如旧版本到 `00121`，就回到 `121`。目标必须是 0 或一个真实存在的迁移号（空号不行）。

执行：

```bash
cd /opt/aegispanel/deploy
PANDORA_ROLLBACK_WRITERS_STOPPED=yes \
PANDORA_ROLLBACK_BACKUP=/var/backups/aegispanel/aegis-postgres-<时间>.dump.age \
PANDORA_LOCAL_MIGRATION_APPROVED=yes GOOSE_BIN=/opt/aegispanel/bin/goose \
  ./migrate.sh rollback-to 121
```

它会依次做这些事，任何一步不满足就以退出码 78 拒绝，一个 Down 都不执行：

1. 读出当前版本，列出要撤销的迁移，从高到低。
2. **预扫描 irreversible。** 范围里有文件头标了 `-- irreversible:` 的迁移就整体拒绝，同时告诉你最多能回到哪个版本。要回到更早，只能走第 3 节。
3. **要求确认。** 确认短语和当前、目标版本绑定，形如 `rollback 133 to 121`。
   - 终端里运行时会提示你原样输入；
   - 非交互时用 `PANDORA_ROLLBACK_CONFIRM='rollback 133 to 121'`。
   - 库的版本变了，旧的确认短语自动失效。
4. 逐个 `goose down`，每撤一个都核对版本。

**某个 Down 失败或拒绝时立刻停下**：

- 那个迁移的事务回滚，库停在它自己的版本上；
- 比它更早的迁移不再尝试；
- 输出会写明停在哪个版本。

常见原因：

- Down 里的数据守卫。例如 00129：已经发出去的后台流量包是用户余额，有这种行就拒绝。按报错处理那些行，或者改走第 3 节。
- 00037–00040 的 Down 要逐个版本显式批准（幂等与订单释放的结构切换）。`rollback-to` 不替人批准，到这里一定会被拒绝；回到这么早的版本只能走第 3 节。
- 00012 的 Down：已有节点写的审计记录（审计只追加、不删）时拒绝，这种库回不到 00011 之前。
- 00134、00137、00138 的 Down（购买模型统一）：有后台代开的换套餐单、套餐卡换套餐，或者有用户、后台、系统转移流量包的流水时拒绝。上线接过流量的库基本都会命中，细节见第 4 节。
- irreversible 的迁移（种子数据、修数据）：00010、00029、00030、00042、00067、00102。`rollback-to` 越不过它们，例如最多回到 102，要回到 101 之前只能走第 3 节。
- 历史迁移的 Down 缺陷已清零，CI 上每个迁移都过「up → down → up」往返（`run-migration-roundtrip.sh` 的 `KNOWN` 为空）。

收尾：

1. 回滚完成后，装回旧发布的二进制与 `migrations/`。用发布控制器时，旧目录在 `/opt/aegispanel/.release-backups/<发布号>/`。
2. 启动写入者，确认 `/readyz` 正常，再开入口。
3. 新版本已经接过流量时，新数据随 Down 一起丢失。先评估能不能前滚修复，确实要回滚再执行，并保留好回滚前的那份备份。

## 3. 情形三：从升级前备份恢复

用于：迁移中途失败且无法前滚；范围里有 irreversible 迁移；Down 被拒绝；或者库已经处于说不清的状态。
恢复会让库回到备份那一刻：之后写入的一切（订单、流量、工单……）都会丢失。

1. 停入口与全部写入者：`systemctl stop nginx aegis-public aegis-admin aegis-node`。
2. 恢复前先给当前库再做一份备份，留作事后核对：`./backup-postgres.sh`。
3. 装回旧发布的二进制与 `migrations/`（发布控制器留在 `.release-backups/<发布号>/`），让程序版本与备份里的结构一致。
4. 恢复数据库：
   - **加密备份**（`*.dump.age`，来自 `backup-postgres.sh` 和发布控制器）用 `restore-postgres.sh`。它先在临时库里完整恢复一遍做校验，再动目标库；覆盖正式库要三道确认：

     ```bash
     cd /opt/aegispanel/deploy
     AEGIS_RESTORE_CONFIRM=RESTORE:<库名> \
     AEGIS_RESTORE_EXISTING_CONFIRM=OVERWRITE_EXISTING:<库名> \
     AEGIS_RESTORE_PRODUCTION_CONFIRM=OVERWRITE_CONFIGURED_DATABASE:<库名> \
       ./restore-postgres.sh --archive /var/backups/aegispanel/aegis-postgres-<时间>.dump.age --target-db <库名>
     ```

     `<库名>` 是 `.env` 里的 `POSTGRES_DB`。
   - **install.sh 的升级前备份**（`pre-upgrade-<时间>.dump`，未加密的 `pg_dump -Fc`）：
     1. 先恢复到一个新库核对：

        ```bash
        docker cp <备份> aegis-postgres:/tmp/restore.dump
        docker exec aegis-postgres createdb -U <POSTGRES_USER> <新库名>
        docker exec aegis-postgres pg_restore -U <POSTGRES_USER> -d <新库名> --exit-on-error /tmp/restore.dump
        ```

     2. 确认无误后，把 `.env` 的 `POSTGRES_DB` 与两条连接串指向新库；或者删掉旧库后改名。改名前务必再确认一次写入者全部停止。
   - **install-native.sh 的升级前备份**（`/var/backups/pandora/pre-upgrade-<时间>.dump`，同样未加密）：同上先恢复到新库核对，客户端是本机的，以 postgres 系统用户经本地 socket：

     ```bash
     runuser -u postgres -- createdb -p <POSTGRES_PORT> <新库名>
     runuser -u postgres -- pg_restore -p <POSTGRES_PORT> -d <新库名> --exit-on-error < <备份>
     ```
5. 恢复后：
   - 用 `migrate.sh version` 确认版本就是旧发布的最大迁移号；
   - 跑 `bootstrap.sh` 重新收窄运行角色；
   - 启动写入者，检查 `/readyz`，再开入口。
6. 事后在新版本里修好问题，重新发版。被恢复掉的那段时间里有支付回调的，按订单核对补记。

## 4. 购买模型统一（00134–00139）：回滚注意与实测耗时

这一批让一个人可以有多份订阅、流量包挂到具体的一份上。升级前按人共用一池的流量包，由 00138 挂到这个人「生效中、到期最晚」的那一份上。

### 4.1 每个迁移的 Down 会拒绝什么、会丢什么

| 迁移 | 内容 | Down 在什么情况下拒绝 | Down 成功时丢掉什么 |
|---|---|---|---|
| 00134 | 换套餐的三个入口（门户、后台开单、套餐卡）共用的两处守卫函数 | 有后台代开的换套餐单（`orders.kind='upgrade'` 且 `created_by` 非空）、套餐卡换套餐留下的无订单 `plan_changed` 事件，或来源是卡密的 `plan_change_refund` 分录 | 不删数据，只还原函数 |
| 00135 | `subscriptions.label`、`orders.subscription_label`；订阅表的纪元触发器拆成两条 | 不拒绝 | 用户给每份起的名字，以及未付款新购单上的名字。只影响显示和 App 里的配置名，不涉及钱 |
| 00136 | 同一用户名下备注名不重名的唯一索引（`CONCURRENTLY`） | 不拒绝 | 只删索引 |
| 00137 | `traffic_pack_grants.subscription_id`、转移流水表 `traffic_pack_transfers`、改挂守卫与提交时检查 | 转移流水里有 `actor_kind <> 'migration'` 的行：用户挪过包、后台转过，或者开第一份订阅时系统自动挂上未分配的包，都算 | 每笔流量包挂在哪一份（回到按人共用一池），以及全部转移流水 |
| 00138 | 回填：存量流量包挂到一份订阅上，每挂一笔写一条 `migration` 流水 | 同 00137 | 回填的结果 |
| 00139 | 按订阅取有余量流量包的索引（`CONCURRENTLY`） | 不拒绝 | 只删索引 |

要点：

- **新买的流量包不写转移流水，不挡回滚，但回滚会丢掉它挂在哪一份。** 升级后门户买的流量包直接写 `subscription_id`。Down 删掉这一列以后，这些包回到按人共用。
- `rollback-to` 从 00139 往下逐个撤，第一个被拒的停下。新版本上线后只要有一个用户挪过一次旧包，就会停在 138：00139 已撤、00138 被拒。这在 5k 副本上实测过。
- 停在 138 时不要装回旧程序。库已经不是旧版本的结构，旧程序也不认识按份挂的流量包。二选一：
  - 重跑 `migrate.sh up` 回到 139，继续用新程序，前滚修复；
  - 或者走第 3 节，从升级前备份恢复。
- **不要绕过守卫。** 转移流水是只追加的证据表，不要用 `session_replication_role` 或手工 SQL 删它。
- 守卫拒绝时首选前滚：保留 00134–00139，修好代码再发一版。

### 4.2 回滚前先导出什么

第 2 节要求的整库备份照做。另外，Down 会删掉下面几样东西，先单独导出成 CSV。回滚或从备份恢复之后，按它们逐个通知用户、人工核对：

```bash
cd /opt/aegispanel/deploy
d=/var/backups/aegispanel/rollback-$(date +%Y%m%d-%H%M%S); mkdir -m 700 "$d"
x() { ./psql.sh -X -q -c "\\copy ($2) TO STDOUT WITH CSV HEADER" > "$d/$1.csv"; }
# 00135：每份订阅的备注名、未付款新购单上的名字
x sub_labels   "SELECT tenant_id, id, user_id, label FROM subscriptions WHERE label IS NOT NULL"
x order_labels "SELECT tenant_id, id, order_no, user_id, status, subscription_label FROM orders WHERE subscription_label IS NOT NULL"
# 00137 / 00138：每笔流量包挂在哪一份、全部转移流水
x pack_grants    "SELECT tenant_id, id, user_id, subscription_id, source, granted_bytes, consumed_bytes, created_at FROM traffic_pack_grants"
x pack_transfers "SELECT * FROM traffic_pack_transfers ORDER BY tenant_id, created_at"
# 00134：后台代开的换套餐单、套餐卡换套餐（这些在就回不去，先留证据）
x admin_plan_changes "SELECT tenant_id, id, order_no, user_id, subscription_id, created_by, total_amount, proration_credit_amount, status FROM orders WHERE kind = 'upgrade' AND created_by IS NOT NULL"
x gift_plan_changes  "SELECT tenant_id, id, subscription_id, payload, occurred_at FROM subscription_events WHERE event_type = 'plan_changed' AND order_id IS NULL"
chmod 600 "$d"/*.csv
```

导出里有用户 id 和订单号，只放在本机的备份目录，不要外传。

回滚前先看会不会被拒（只读）：

```bash
./psql.sh -X -c "SELECT actor_kind, count(*) FROM traffic_pack_transfers GROUP BY 1;"
```

结果里出现 `migration` 以外的行，00137 和 00138 的 Down 就会拒绝。

### 4.3 00136、00139 的 CONCURRENTLY 失败了怎么清理

两个都是 `-- +goose NO TRANSACTION` 加 `CREATE ... INDEX CONCURRENTLY IF NOT EXISTS`。失败会留下 INVALID 索引，处理按第 1 节第 5 步。这两个的具体情况：

| 迁移 | 索引名 | 常见失败原因 |
|---|---|---|
| 00136 | `subscriptions_user_label_unique`（唯一） | 同一用户的两份备注名只差大小写；锁等待超过 5 秒；语句超时 |
| 00139 | `idx_traffic_pack_grants_open_sub` | 锁等待超过 5 秒；语句超时 |

00136 正常不会有重名：门户改名时就挡住了，只有手改数据才会出现。失败时先找出重名，改掉其中一份再清理、重跑：

```bash
./psql.sh -X -c "SELECT tenant_id, user_id, lower(label), count(*) FROM subscriptions WHERE label IS NOT NULL GROUP BY 1, 2, 3 HAVING count(*) > 1;"
```

清理与重跑：

1. 先确认版本。看 `migrate.sh version`：
   - 还停在 135（或 138）：失败的迁移没记版本，往下做第 2 步；
   - 已经是 136（或 139）：说明之前有人没清理就重跑过，跳到第 3 步。
2. 版本没记上时：
   - `./psql.sh -X -c "DROP INDEX CONCURRENTLY IF EXISTS public.<索引名>;"`；
   - 重跑 `migrate.sh up`（或重跑安装脚本）。
3. 版本已记上、索引却是 INVALID 时，不要来回 Down / Up：
   - 先 `DROP INDEX CONCURRENTLY IF EXISTS public.<索引名>;`；
   - 再把迁移文件 `-- +goose Up` 段里那条 `CREATE ... CONCURRENTLY` 原样执行一次，`SET lock_timeout` 与 `statement_timeout` 也照抄。
4. 最后用第 1 节第 5 步的查询确认没有 INVALID 索引。

5k 副本上的实测：

- 造一组 `dup` / `DUP` 的备注名，00136 失败，版本停在 135，留下 `valid=false ready=false` 的索引；
- 把数据修好、但不清理就重跑：goose 报成功、版本记成 136，索引仍是 `valid=false`，唯一约束实际不存在；
- 回到 135，`DROP INDEX CONCURRENTLY`（164 ms），再重跑：索引 `valid=true`。

### 4.4 实测耗时（5k 规模，2c4g 一次性测试面板机）

库 157 MB：用户约 1 万、订阅约 1 万、节点 528、流量包余额 4 笔。用 `install.sh` 原地升级，迁移从 133 到 139。

整次升级：

| 阶段 | 耗时 | 说明 |
|---|---|---|
| install.sh 全程 | 43.9 s | 退出码 0 |
| 升级前 `pg_dump` | 6.7 s | 停服之前 |
| 一次性克隆库预检 | 21 s | 停服之前，服务照常在跑 |
| 停服（日志「停止服务后迁移」到「启动服务」） | 9.3 s | 外部每 0.5 秒探一次 `/healthz`，连续失败 10.5–11.7 s |
| 六个迁移合计 | 约 0.25 s | 停服的 9.3 s 里：核对预检凭据加迁移约 2.9 s，收窄数据库角色 0.9 s，装程序与 systemd 单元 5.5 s；之后起服务约 1.9 s 后探针恢复 200 |

单个迁移的耗时，取 goose 自己报的数：

| 迁移 | 正式升级 | 副本重放 3 轮 | 说明 |
|---|---|---|---|
| 00134 | 约 27 ms（由 `goose_db_version` 时间戳推算） | 13 / 13 / 19 ms | 只换函数 |
| 00135 | 00135 加 00136 共约 70 ms（同上） | 46 / 30 / 31 ms | 加可空列，两个 CHECK 先 NOT VALID 再 VALIDATE |
| 00136 | （见上一行） | 27 / 28 / 23 ms | 新列全为空，部分索引是空的 |
| 00137 | 75.8 ms | 97 / 64 / 74 ms | 加列、建表、换函数与触发器 |
| 00138 | 51.2 ms，改挂 4 笔 | 52 / 50 / 48 ms | 耗时随存量流量包笔数线性增长：造 1 万笔时约 7.2 s（见文件头） |
| 00139 | 10.0 ms | 20 / 15 / 14 ms | |

- `install.sh` 只打印迁移输出的最后 4 行，所以正式升级里看不到 00134–00136 各自的耗时。
- 要单个迁移的数，用 `SELECT version_id, tstamp FROM goose_db_version ORDER BY id DESC LIMIT 10` 的时间差推算；或者先把升级前备份恢复成一个临时库，用 `goose up-by-one` 逐个重放。上表「副本重放」一列就是这样量的。
- 流量包多的库，00138 是这一批里唯一随数据量变长的迁移。预检会在克隆库上先跑一遍，预检日志里的总耗时可以用来估算停服时间。

## 5. 旧版本集群里的 aegis 库（直装）

`install-native.sh` 只用 PostgreSQL 18 的 `main` 集群，机器上别的版本的集群（16、17，以后的 19）一律不停、不升级、不删。
它在两种情况下停下，报「旧集群里的 aegis 库要由人搬到 PG18」，这时什么都还没改：

- 首装（或 `--from-docker`），PG18 里还没有 `aegis` 库，而另一个在线的旧集群里有（或查不清有没有）；
- 升级，`.env` 的 `POSTGRES_PORT` 指着旧集群，或者既不是旧集群也不是 PG18 的端口。

（更早的安装器在这里会对旧集群跑 `pg_upgrade`，失败了也照样 `pg_dropcluster`，等于删库；已经删掉的只能从备份恢复，见第 3 节。）

搬法：导出 → 恢复到 PG18 → 核对 → 改端口 → 重跑安装器。旧集群留着，确认无误之后由人删。

```bash
pg_lsclusters                                  # 记下旧集群版本 <旧>、端口 <旧端口>，以及 18/main 的端口 <新端口>
systemctl stop aegis-public aegis-admin aegis-node
install -d -m 0700 /var/backups/pandora
B=/var/backups/pandora/aegis-pg<旧>-$(date +%Y%m%d-%H%M%S).dump
runuser -u postgres -- pg_dump -p <旧端口> -Fc -d aegis > "$B" && chmod 0600 "$B"
runuser -u postgres -- pg_restore --list < "$B" > /dev/null && echo 备份可读
# 角色是集群级的：先把旧集群的角色（不带口令）建到 PG18，已存在的会报错跳过
runuser -u postgres -- pg_dumpall -p <旧端口> --roles-only --no-role-passwords \
  | runuser -u postgres -- psql -X -p <新端口> -d postgres
runuser -u postgres -- createdb -p <新端口> -O aegis aegis
runuser -u postgres -- pg_restore -p <新端口> -d aegis --exit-on-error < "$B"
```

核对：两边逐表行数一致（把 `-p` 分别换成旧端口和新端口，两份输出 `diff` 为空）：

```bash
runuser -u postgres -- psql -X -At -p <端口> -d aegis -c "
  SELECT n.nspname || '.' || c.relname || ' ' ||
         (xpath('/row/c/text()', query_to_xml(format('SELECT count(*) AS c FROM %I.%I', n.nspname, c.relname), false, true, '')))[1]::text
    FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
   WHERE c.relkind IN ('r', 'p') AND n.nspname NOT IN ('pg_catalog', 'information_schema') AND n.nspname NOT LIKE 'pg_toast%'
   ORDER BY 1"
```

然后把 `/opt/pandora/deploy/.env` 里 `POSTGRES_PORT`、`AEGIS_DATABASE_URL`、`AEGIS_MIGRATION_DATABASE_URL` 的端口改成 `<新端口>`，重跑 `install-native.sh`（按升级走：备份、预检、迁移、重设运行角色口令、起服务）。
面板在 PG18 上跑稳之后，旧集群由人决定是否删除（`pg_dropcluster --stop <旧> main`，删前再做一份 `pg_dump`）。

## 附：写迁移时的约定（摘要）

完整规则见仓库 `.claude/rules/panel-migrations.md`，CI 守卫是 `panel/tools/migrationlint` 与 `run-migration-roundtrip.sh`：

- 每个迁移都有 `-- +goose Down`。确实不能逆的（种子数据、修数据）：
  - 文件头写 `-- irreversible: <原因>` 和 `-- forward-fix: <前滚补救办法>`；
  - Down 里用 `RAISE EXCEPTION` 拒绝。
- Up 和 Down 开头都要先设锁等待和语句超时，再写任何 DDL。
- 大表上：
  - 建索引用 `CREATE INDEX CONCURRENTLY`，文件带 `-- +goose NO TRANSACTION`；
  - 不做整表重写，必须做时在文件头写行数和预估耗时；
  - 回填要分批、可重跑。
- 改名、删列分两个发布走完：扩展 → 迁移数据 → 收缩。
- 每个可逆迁移在 CI 上都要过「up → down → up」往返，结构逐项一致。

# 迁移失败与回滚 runbook

面向自己部署 Pandora Panel 的运维。三种情形：升级时迁移失败、回到指定版本、从升级前备份恢复。
命令里的路径按 install.sh 的布局（`/opt/aegispanel`）写；install-native.sh 装的机器换成 `/opt/pandora`。

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
  - `install.sh` 升级时先做 `pg_dump -Fc`，放在 `/var/backups/aegispanel/pre-upgrade-<时间>.dump`。

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

- 控制器在**停服之前**跑一次性库预检：把正式库整库克隆到同一个 PostgreSQL 里的临时库，在克隆上按「写入者已停」的口径演练待执行的迁移。通过后在只有 root 能读的发布暂存目录里留一张预检凭据。
  - 好处：停服时间里不再包含克隆和演练。5k 规模实测，这一步约 15 秒，原来占停服的三分之二，库越大越长。
  - 代价：克隆发生在业务时段，会给正式库带来一次整库读。大库请挑低峰发版。
- **停服之后**只做一次只读核对，亚秒级，结果与凭据逐项比对：
  - 迁移目录摘要：演练过的就是要跑的；
  - 源库的 goose 水位：中间没人迁移过；
  - 续费闸门：全部写入者停止之后，未关联的在途续费仍是 0；
  - 停写演练与正式迁移一致；
  - 凭据在六小时内。

  任何一项对不上就拒绝，控制器自动拉回旧服务（`rollback=writers_and_ingress_restored`），重新发版即可。
- 预检凭据只对这次发布有效，不要手工复制或改写它。
- 直接调用 `migrate.sh up` 而不带凭据时，它照旧在调用当下做完整预检。

## 1. 情形一：升级时迁移失败

现象：

- 控制器输出 `rollback=manual_required` 和 `FAIL-CLOSED after migration attempt`；
- 或者 `install.sh` 报「迁移失败（退出码 N）」并贴出完整输出。

1. **保持写入者停止，不要重启服务。**
   - 控制器已经停了写入者，入口（nginx）也没恢复。
   - install.sh 会尝试把旧服务拉回来；如果新二进制已经装上，立刻 `systemctl stop aegis-public aegis-admin aegis-node`。
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
- 历史迁移里还有少数 Down 本身有缺陷，清单见 `run-migration-roundtrip.sh` 的 `KNOWN`。

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
5. 恢复后：
   - 用 `migrate.sh version` 确认版本就是旧发布的最大迁移号；
   - 跑 `bootstrap.sh` 重新收窄运行角色；
   - 启动写入者，检查 `/readyz`，再开入口。
6. 事后在新版本里修好问题，重新发版。被恢复掉的那段时间里有支付回调的，按订单核对补记。

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

---
name: new-migration
description: pandora 新写或修改一个 goose 迁移（panel/migrations）时按项目规范一次写对并自证：取号、选样板（可逆、CREATE OR REPLACE 别人的函数、irreversible、大表并发建索引）、RLS / 授权 / 追加写 / 保留表 / DDL lint 检查表、本地与 GitHub 往返自证、回填耗时，以及只改 Down 修历史迁移缺陷（往返 KNOWN 清单）。要「加表」「加列」「写迁移」「改 seed 函数」「补 Down」「修 KNOWN」「往返红了」「迁移号用哪个」时使用。
---

# 写一个迁移

规则全文在 `.claude/rules/panel-migrations.md`（改 `panel/migrations/**` 时自动加载），回滚与失败处置在 `panel/deploy/MIGRATION-RUNBOOK.md`。这里是按顺序做的步骤：取号 → 套样板 → 过检查表 → 自证。

脚本都只读，在仓库根目录跑：

| 脚本 | 做什么 |
|---|---|
| `bash .claude/skills/new-migration/scripts/next-number.sh` | 主线最近 5 个迁移、当前工作区最大号、各任务分支上主线还没有的迁移、历史空号，以及下一个可用号 |
| `python3 .claude/skills/new-migration/scripts/find-def.py <对象名> [--body] [--before <号>]` | 哪些迁移的 Up 段对这个函数、视图、表做过 DDL；`--body` 打印最后一次 CREATE 的原文，用来写 Down |
| `python3 .claude/skills/new-migration/scripts/upsegment-sha.py <迁移.sql>` / `--check` | 按 migrationlint 的口径算 Up 段 SHA-256，输出 `upsegments.txt` 的登记行；`--check` 核对整张冻结表 |

## 1. 取号

- 跑 `next-number.sh`，用它给的号。编号只要求严格递增、不重复，允许空号。历史空号（00073、00091、00092 等）不回填：已装的库记着更大的版本号。
- 文件名 `NNNNN_snake_name.sql`，`migrate.sh` 与 `check-migrations.sh` 用同一个正则 `^[0-9]{5}_[A-Za-z0-9._-]+\.sql$` 检查。
- 几路并行：号段由总协调按 dispatch-task skill 预分，只用自己那段，用不到就空着。合并按号从小到大（goose 不接受「库里已到 00106，又冒出没跑过的 00104」）。
- 新迁移的号一定大于 133：`ratchet.txt` 和往返 `KNOWN` 都只收 00133 及以前的条目，新迁移没有豁免，必须一次合规。

## 2. 套样板

样板在 `templates/`，复制成新文件后替换所有 `<…>`：

| 场景 | 样板 | 参考的真迁移 |
|---|---|---|
| 建表、加列、加索引（小表）等能逆的结构变更 | `reversible.sql` | 00130（建表 + RLS + 收 DELETE）、00129（换 CHECK，Down 带数据守卫） |
| CREATE OR REPLACE 已有的函数（守卫函数、`app.seed_tenant_defaults`） | `replace-function.sql` | 00134（两个函数，Down 带守卫再逐字还原）、00128（seed 函数在 00126 版上加一行） |
| 修数据、纯种子，Down 无法还原 | `irreversible.sql` | 00029、00030 |
| 大表上建索引 | `concurrent-index.sql` | 主线还没有 NO TRANSACTION 的迁移，样板按 lint 规则写 |

- **StatementBegin / StatementEnd**：goose 按分号切语句。函数体、DO 块这类内部有分号的语句必须包在 `-- +goose StatementBegin` 与 `-- +goose StatementEnd` 之间；普通 DDL 不用包，包了也无害（00130 把 CREATE TABLE 包了）。
- **超时**：Up 和 Down 的第一批语句就是 `SET LOCAL lock_timeout` 与 `SET LOCAL statement_timeout`，在任何 DDL / DML 之前。lint 只认段首连续的 SET。
- **Down 无法安全执行时**，二选一：
  - 永远不能逆（原值没留存）：文件头（`-- +goose Up` 之前）写 `-- irreversible: <原因>` 与 `-- forward-fix: <前滚办法>`，Down 里 `RAISE EXCEPTION`。`migrate.sh rollback-to` 预扫描范围内的文件头，碰到它就在执行任何 Down 之前整体拒绝，并说明最多能回到哪；往返门禁要求它的 down 失败且版本、结构不变。
  - 只在有某类数据时不能逆：Down 开头用 DO 块查，有就 `RAISE EXCEPTION`，没有就照常回退（00129、00134）。这不算 irreversible，**不要**标。
  - 不许写空 Down 或只有 SET 的 Down（lint `down-empty`）：goose 对它执行 down 只删版本行，回滚静默「成功」。

## 3. 检查表

写完逐条对一遍。「守卫」一列是会变红的测试或脚本；写「评审」的没有自动守卫。

| 项 | 要做的 | 守卫 |
|---|---|---|
| Down | 能逆就逆，Down 之后的结构与上一版逐项一致：列序、函数体原文、触发器、策略、授权、注释、集群级角色。种子行在 Down 里保留（Up 插入带 NOT EXISTS / ON CONFLICT DO NOTHING，重跑不重复） | lint `down-missing` / `down-empty`；GitHub 往返 |
| 租户隔离 | 新表带 `tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE`，并 `SELECT app.enable_tenant_rls('<表>')`：ENABLE + FORCE RLS + `tenant_isolation` 策略（00001 定义）。不带 FORCE，表属主会绕过策略 | 评审（`check-migrations.sh`、`install.sh` 只打印 FORCE RLS 的表数）；PG18 用例以 `aegis_app` 跑 |
| 追加写 | 证据流水表 `SELECT app.make_append_only('<表>')`：语句级 BEFORE UPDATE OR DELETE 触发器，加 `REVOKE UPDATE, DELETE, TRUNCATE … FROM PUBLIC`。同时把表名加进 `configure-app-role.sql` 的 append-only 数组（在 `required append-only table is missing` 那个循环里） | 评审 |
| 授权 | 新表对 `aegis_app` 的默认权限只有 SELECT（configure-app-role.sql 的 ALTER DEFAULT PRIVILEGES），用到的 INSERT / UPDATE 在迁移里显式 GRANT | PG18 门禁（准备动作 `app_role` 先跑 configure-app-role.sql，再以 `aegis_app` 测） |
| 不许删的表 | 迁移里 `REVOKE DELETE, TRUNCATE`；configure-app-role.sql 重跑时 `GRANT … ON ALL TABLES` 和末尾「列级提升回表级」会把它冲掉，所以在文件末尾、重授块之后再写一行 REVOKE，并把这行加进契约测试的列表 | `TestConfigureAppRoleRevokesDeleteOnGuardedTablesLast`（`panel/internal/platform/db/configure_role_contract_test.go`，只核列表里的行） |
| 新表要有 Go 引用 | 重放全部 Up 后还在的表，要么被非测试 Go 源码按名字引用，要么登记 `RESERVED-TABLES.md`；删登记过的表先读它的「保留原因」。Up 段里行首是 `CREATE TABLE` / `DROP TABLE` 的任何文本（含块注释）都算建表、删表 | `TestSchemaTablesAreReferencedOrRegistered` |
| 注释 | 迁移注释不写 RESERVED-TABLES.md 里的保留表名：迁移 SQL 常被原样抄进 Go，注释跟进去就算「引用」 | 同上 |
| CREATE OR REPLACE 别人的函数 | 用 `find-def.py <函数> --body` 拿当前生效版，在它上面改，其余逐字不变。几路同改一个函数：后合的那份包含先合那份的改动，Down 还原到先合那份。改签名就不是 REPLACE，要 DROP 旧签名并重做授权（configure-app-role.sql 有按签名 REVOKE 的函数） | 往返比函数体文本；seed 类契约（第 6 节） |
| 已发布的 Up | 一个字节都不改，要改行为就写新迁移。允许的只有：追加 Down 段、在文件头加标记行 | `TestPublishedUpSegmentsAreFrozen`（`upsegments.txt`） |
| 大表 | 清单在 `panel/tools/migrationlint/bigtables.go`，新表会随用户数、节点数或时间增长就登记。大表上：建索引用 CONCURRENTLY + NO TRANSACTION + IF NOT EXISTS；不整表重写，必须做就写 `-- rewrite: <表> rows=<行数> est=<耗时>`；回填写 `-- backfill: batched-by=…; rerunnable=…`。同一迁移里刚建的表不受约束 | lint `index-not-concurrent`、`concurrently-in-transaction`、`table-rewrite`、`backfill-unmarked` |
| 删列、改名 | 扩展 → 迁移数据 → 收缩，收缩单独一个发布，文件头 `-- contract-of: <扩展迁移号>` | lint `expand-contract` |
| 集群级 DDL | 不建角色、不建库。预检（`check-migrations.sh`）在**同一个 PostgreSQL 集群**里克隆库重放 Up，集群级 DDL 会落到生产集群上；确实要（00038）就幂等创建并核对已有角色的属性 | 评审 |
| 运行角色调用的函数 | `SECURITY DEFINER` + 固定 `search_path`，限本租户、分批、有上限：`aegis_app` 按库设了 `statement_timeout = 15s`（迁移本身以超级用户跑，不受限）。要绕追加写触发器，在函数定义上 `SET session_replication_role = replica`（00100） | `TestConfigureAppRolePinsQueryGuards` 钉 15s |
| 影响节点下发 | 新表或新列会改变节点下发结果，给 00101 那组延迟约束触发器补一条 | 评审 |
| 动态 SQL | lint 看不见 `EXECUTE format(...)` 拼的语句 | 评审 |

## 4. 自证

### 本地（秒级）

在仓库根目录：

```bash
bash panel/deploy/check-migrations_mock_test.sh        # 文件名、编号、Up 标记、Down 或 irreversible，拿真实 migrations/ 跑
cd panel && go test ./tools/migrationlint/ ./internal/platform/db/   # DDL lint 与棘轮、Up 冻结、表登记簿、configure-app-role 契约
```

再跑读这个迁移或这个对象的源码契约所在的包：

```bash
grep -rln --include='*_test.go' -e '<迁移号>_' -e '<函数或表名>' panel/internal panel/tools
```

改了 `app.seed_tenant_defaults` 至少加跑 `go test ./internal/domain/notify/ ./internal/middleware/`（`TestTenantSeedTemplatesMatchDefaults`、`TestTenantSeedSwitchesMatchCode`）。本机没有 Docker，PG18 用例与往返都跑不了，跳过不等于通过。

### 推送后（GitHub）

- 迁移改动触发 `panel-pg18.yml`。往返是 `panel-pg18` job 里的一步「Migration round trip (up, down, up per migration)」，跑 `bash panel/deploy/run-migration-roundtrip.sh panel`，约 3 分钟；之后同一 job 跑 PG18 门禁。检查机跳过整个 panel-pg18，所以必须等 `wait-github.sh`（verify skill）。
- 往返从空库逐个迁移：up → dump；down → 必须等于上一版的 dump；再 up → 必须等于第一次的 dump。irreversible 的 down 必须失败且版本、结构不变。dump 是 `pg_dump --schema-only` 加 `pg_dumpall --roles-only`，按对象条目排序后比。
- 只看往返的结论（PASS 行已去掉）：

  ```bash
  gh run view <run-id> --log | grep -F 'Migration round trip' | cut -f3- \
    | sed -E 's/^[0-9TZ:.-]+ //' | grep -v -e '--- PASS: roundtrip/'
  ```

  失败行是 `--- FAIL: roundtrip/<文件>: <原因>`，下面缩进的是 diff（`-` 上一版或第一次 up，`+` down 或重新 up 之后），只打印前 80 行。

### 回填耗时

有回填、修数据或大表 DDL 的，上线前在 5k 库副本上量：按 bench-eval skill「快速诊断」第 1 步用 `prep-copy.sh` 建副本并只套新迁移的 Up 段，它打印每个迁移的耗时，按两倍估（预检在克隆库跑一遍、正式库再跑一遍）。注意：

- 副本的结构可能落后于主线（评测模板停在 00097，见 bench-eval 2.2）。新迁移依赖更晚的对象时，把中间的迁移按号一起传进去。
- prep-copy 把每个 Up 段包进 `BEGIN; … COMMIT;`，NO TRANSACTION 的文件（CONCURRENTLY）在里面会报错，要另跑。

数字写进文件头的 `rewrite:` / 注释，以及任务报告。

### 合进主线之后

Up 段从此视为已发布。冻结表 `upsegments.txt` 只追加不改，登记行用 `upsegment-sha.py <文件>` 生成（目前登记到 00133，00134 还没登记）。

## 5. 修历史迁移的 Down（往返 KNOWN）

`run-migration-roundtrip.sh` 的 `KNOWN` 原登记 12 个历史 Down 缺陷，w7migr（10-07，合入 0fd6c5d）已全部修完，现在是空数组，只许保持为空；往返再红就按下面的做法修，不往 KNOWN 里加。登记了却已通过的条目会判「过期登记」变红。做法：

1. **只动 Down 段和文件头**，Up 段一个字节不改（`TestPublishedUpSegmentsAreFrozen` 兜底）。
2. **先看差异再修**。KNOWN 里的条目失败时，往返只打一行「KNOWN roundtrip/…: <原因>」，**不打 diff**。要看完整差异：先只从 KNOWN 删掉这一条推一次，或者修好一版推上去看还差什么。改函数体前用 `find-def.py <函数> --before <本迁移号> --body` 拿上一版原文逐字抄（00035 就是只差 `END $$;` 与 `END;` 换行 `$$;`）。
3. **修一个，就在同一个提交里从 KNOWN 删一条**。
4. **同步登记**：
   - 给 Down 段开头补了超时，或给文件头加了 irreversible（irreversible 的 Down 不查超时），`ratchet.txt` 里这个文件的 `lock-timeout-down` / `statement-timeout-down` 会变成过期登记，测试要求删掉。给 Up 补不了超时，`*-up` 那几行留着。
   - 先 `grep` 哪些测试读这个迁移文件：PG18 用例会原样执行历史迁移的 Up 或 Down（`traffic_retention_pg18_test.go` 跑 00131–00133 的 Down 并断言自引用外键还在；`subscription_period_pg18_test.go` 执行 00102 的 Up），改了 Down 要连断言一起改。

按缺陷类型的修法方向（以往返 diff 为准）：

| 类型 | 历史例子（都已修好，修法见各文件 Down 段） | 方向 |
|---|---|---|
| Down 对追加写表 DELETE（直接或经外键级联），语句级触发器空表也拒绝 | 00010、00012、00042、00050 | 首选不删：种子行按规则在 Down 里保留（00042、00050 的 Up 插入都带 ON CONFLICT DO NOTHING，重跑不重复）；要删的证据行改成数据守卫（有行就 RAISE）。确实要删，迁移以超级用户跑，`SET LOCAL session_replication_role = replica` 能跳过触发器，但外键级联与校验也一并跳过，会削弱追加写不变量，不用。实际修法：00010、00042 纯种子标 irreversible 加 forward-fix（00010 的 Down 要删默认租户，等于删全部业务数据）；00012 改成有节点审计行就拒绝回滚；00050 只删两张表、保留设置行与模板 |
| 函数体文本与上一版不同 | 00035、00037 | `find-def.py --before` 取上一版逐字粘贴 |
| 授权没退干净 | 00036（列级 INSERT/UPDATE 授权残留） | Down 里逐条 REVOKE Up 加的列级授权 |
| 集群级对象残留 | 00038（角色）、00001（扩展） | Down 里 DROP。角色是集群级的，同集群其他库（预检克隆库）可能还有依赖，先读 00038 的 Up 再定。实际修法：00038 只在整个集群都不再引用该角色时才删，否则保留并提示；00001 删扩展不带 CASCADE |
| Down 无条件 RAISE 却没标 | 00067、00102 | 文件头加 `-- irreversible:` 与 `-- forward-fix:`；之后 rollback-to 不能越过它 |
| 约束恢复得不一样 | 00131（外键） | 先以 NOT VALID 加回再试 VALIDATE：没有悬空引用就成为已校验外键（结构一致），有就保持 NOT VALID（保留期清理可能删了原报文），回滚照常完成 |

## 6. 坑

- **两路同改 seed 函数**：w5account 的 00128 起初在 00090 版上加模板，而先合的 00126 已经加了到期与召回模板；合并时改成在 00126 版上加、Down 还原 00126 版，否则新租户丢 6 个模板。动手前跑 `find-def.py app.seed_tenant_defaults`，看当前生效的是哪一版。
- **seed 契约按「最后一个定义它的迁移」核对**：notify 与 middleware 的契约按文件名排序，取最后一个包含 `FUNCTION app.seed_tenant_defaults(` 字样的迁移（整个文件，含注释）。只在注释里提到这串字、或只对它 `REVOKE … ON FUNCTION app.seed_tenant_defaults(uuid)` 的迁移，也会被当成最新定义。模板数写死在 `TestTenantSeedTemplatesMatchDefaults`（当前 19），加模板同一提交改它。
- **契约与计数随迁移一起改**：seed 模板数；后台路由新用的权限码必须由某个迁移插进 `permissions`（`TestRoutePermissionsExistInCatalog` 扫全部迁移）；加了 PG18 域时 `run-pg18-gates.sh` 的 DOMAINS。几路同时加 DOMAINS 会在相邻行冲突，两行都留。
- **PG18 夹具租户 id 撞号**：同一个域库里各用例共用一个库，新夹具的 id 前缀先 grep 主线有没有人在用（accept-task 坑里记了三次）。
- **configure-app-role.sql 冲掉迁移里的 REVOKE**：00130 收了 DELETE，脚本末尾的表级重授又把它给回去，直到总协调把 REVOKE 挪到重授之后（691bb77）。新的「不许删」表照第 3 节在两处写。
- **迁移脚本找 migrations 目录**：`migrate.sh`、`check-migrations.sh` 取与 `deploy/` 并排的 `migrations/`（源码树 `panel/`、install.sh 的 `/opt/aegispanel`、install-native.sh 的 `/opt/pandora` 都是这个布局），`AEGIS_MIGRATIONS_DIR` 可覆盖。曾写死 `/opt/pandora`，install.sh 装的机器上找不到或读到另一套安装的旧迁移（64c0b21 修）。写新脚本或测试找迁移时沿用这条，桩测试 `migrate-layout_mock_test.sh`。往返和 PG18 门禁收的参数是 `panel` 源码目录，不是 migrations 目录。
- **irreversible 标记必须在文件头**：`migrate.sh` 与往返脚本都用 awk 只扫 `-- +goose Up` 之前的行；写在 Up 段里的不算。
- **NO TRANSACTION 文件里的 SET**：goose v3.26.0 对这种文件逐条用连接池执行、不开事务，`SET LOCAL` 不生效，lint 要求会话级 `SET`；样板在段尾 RESET，免得留给同一条连接上的下一个迁移。中途失败会留下 INVALID 索引，Up 要能重入。
- **00037–00040 的闸门**：这几个迁移的 Up / Down 要逐版本用 PGOPTIONS 批准，往返脚本的 `MIGRATE_OPTS` 原样带着批准跑；`rollback-to` 不代签，回到这么早只能从备份恢复。修它们的 Down 时，往返里看到的是「已批准」路径。

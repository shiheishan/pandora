---
name: new-migration
description: pandora 新写或修改一个 goose 迁移（panel/migrations）时按项目规范一次写对并自证：取号、选样板（可逆、CREATE OR REPLACE 别人的函数、irreversible、大表并发建索引）、RLS / 授权 / 追加写 / 保留表 / DDL lint 检查表、本地与 GitHub 往返自证、回填耗时，以及往返红了只改 Down 修历史迁移缺陷。要「加表」「加列」「写迁移」「改 seed 函数」「补 Down」「往返红了」「迁移号用哪个」时使用。
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

- 跑 `next-number.sh`，用它给的号（编号规则、历史空号不回填见 `rules/panel-migrations.md`）。
- 文件名 `NNNNN_snake_name.sql`。
- 几路并行：号段由总协调按 dispatch-task skill 预分，只用自己那段，用不到就空着；合并顺序也在那里。
- 叠在集成分支上的路（如 S）不取主线号，用设计给的相对编号，见第 7 节。
- `ratchet.txt` 和往返 `KNOWN` 不给新迁移豁免，必须一次合规。

## 2. 套样板

样板在 `templates/`，复制成新文件后替换所有 `<…>`：

| 场景 | 样板 | 参考的真迁移 |
|---|---|---|
| 建表、加列、加索引（小表）等能逆的结构变更 | `reversible.sql` | 00130（建表 + RLS + 收 DELETE）、00129（换 CHECK，Down 带数据守卫） |
| CREATE OR REPLACE 已有的函数（守卫函数、`app.seed_tenant_defaults`） | `replace-function.sql` | 00134（两个函数，Down 带守卫再逐字还原）、00128（seed 函数在 00126 版上加一行） |
| 修数据、纯种子，Down 无法还原 | `irreversible.sql` | 00029、00030 |
| 大表上建索引 | `concurrent-index.sql` | 00136、00139（都是 `NO TRANSACTION`） |

- **超时**：Up 和 Down 的第一批语句就是 `SET LOCAL lock_timeout` 与 `SET LOCAL statement_timeout`，在任何 DDL / DML 之前。lint 只认段首连续的 SET。
- **Down 无法安全执行时**，二选一（细则见规则「Down、DDL 守卫与往返」）：永远不能逆用 `irreversible.sql`，文件头（`-- +goose Up` 之前）写 `-- irreversible:` 与 `-- forward-fix:`，Down 里 `RAISE EXCEPTION`；只在有某类数据时不能逆，Down 开头用 DO 块查、有就 RAISE，这不算 irreversible，**不要**标。不许写空 Down 或只有 SET 的 Down（lint `down-empty`）。

## 3. 检查表：项 → 守卫

要做什么见规则文件；这里列每一项会在哪里变红。「评审」表示没有自动守卫，写完自己对一遍。

| 项 | 守卫 |
|---|---|
| Down 存在且结构与上一版逐项一致 | lint `down-missing` / `down-empty`；GitHub 往返 |
| 新表租户隔离：`tenant_id` 列加 `SELECT app.enable_tenant_rls('<表>')`（ENABLE + FORCE） | 评审；PG18 用例以 `aegis_app` 跑 |
| 证据流水表追加写：`SELECT app.make_append_only('<表>')`，并把表名加进 `configure-app-role.sql` 的 append-only 数组 | 评审 |
| 授权：新表对 `aegis_app` 默认只有 SELECT，用到的 INSERT / UPDATE 在迁移里显式 GRANT | PG18 门禁（先跑 configure-app-role.sql，再以 `aegis_app` 测） |
| 不许删的表：迁移里 REVOKE，再在 configure-app-role.sql 末尾重授块之后补一行并加进契约列表 | `TestConfigureAppRoleRevokesDeleteOnGuardedTablesLast`（`panel/internal/platform/db/configure_role_contract_test.go`，只核列表里的行） |
| 新表有 Go 引用或登记 `RESERVED-TABLES.md`；删登记过的表先读「保留原因」；迁移注释同样不写保留表名（见根 CLAUDE.md「环境与工具坑」） | `TestSchemaTablesAreReferencedOrRegistered` |
| CREATE OR REPLACE 别人的函数：`find-def.py <函数> --body` 取当前生效版再改；改签名要 DROP 旧签名并重做授权 | 往返比函数体文本；seed 类契约（第 6 节） |
| 已发布 Up 不改（允许追加 Down、文件头标记） | `TestPublishedUpSegmentsAreFrozen`（`upsegments.txt`） |
| 大表（清单 `panel/tools/migrationlint/bigtables.go`）：并发建索引、不整表重写、回填标注 | lint `index-not-concurrent`、`concurrently-in-transaction`、`table-rewrite`、`backfill-unmarked` |
| 删列、改名走扩展 → 收缩，文件头 `-- contract-of:` | lint `expand-contract` |
| 集群级 DDL：不建角色不建库（预检在同一集群克隆库重放）；确实要（00038）就幂等创建并核对已有角色属性 | 评审 |
| 运行角色调用的函数：`SECURITY DEFINER` + 固定 `search_path`、本租户、分批；要绕追加写触发器用函数定义上的 `SET session_replication_role = replica`（00100） | `TestConfigureAppRolePinsQueryGuards` 钉 15s |
| 影响节点下发的新表或新列：给 00101 那组延迟约束触发器补一条 | 评审 |
| 动态 SQL（`EXECUTE format(...)`） | 评审（lint 看不见） |

## 4. 自证

### 本地（秒级）

在仓库根目录：

```bash
bash panel/deploy/check-migrations_mock_test.sh        # 文件名、编号、Up 标记、Down 或 irreversible，拿真实 migrations/ 跑
python3 .claude/skills/new-migration/scripts/upsegment-sha.py --check  # 动了已发布迁移再跑：Up 段应 0 条不一致
cd panel && GOTOOLCHAIN=go<go.mod 版本> go test ./tools/migrationlint/ ./internal/platform/db/   # DDL lint 与棘轮、Up 冻结、表登记簿、configure-app-role 契约
```

再跑读这个迁移或这个对象的源码契约所在的包：

```bash
grep -rln --include='*_test.go' -e '<迁移号>_' -e '<函数或表名>' panel/internal panel/tools
```

改了 `app.seed_tenant_defaults` 至少加跑 `GOTOOLCHAIN=go<go.mod 版本> go test ./internal/domain/notify/ ./internal/middleware/`（`TestTenantSeedTemplatesMatchDefaults`、`TestTenantSeedSwitchesMatchCode`）。PG18 用例与往返本机跑不了（见根 CLAUDE.md「环境与工具坑」），跳过不等于通过。

### 推送后（GitHub）

- 迁移改动触发 `panel-pg18.yml`。往返只在 GitHub 的 panel-pg18 job 里跑（口径与脚本见规则），检查机跳过它，所以必须等 `wait-github.sh`（verify skill）。
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

Up 段从此视为已发布。冻结表 `upsegments.txt` 只追加不改：合并时由总协调（accept-task「合并」）用 `upsegment-sha.py <文件>` 追加登记，`--check` 看登记到哪。合进集成分支不算发布，不冻结（第 7 节）。

## 5. 往返红了：只改 Down

`run-migration-roundtrip.sh` 的 `KNOWN` 已是空数组，只许保持为空，不往里加（历史缺陷的修法见各文件 Down 段）。往返红了：
1. **只动 Down 段和文件头**，Up 段一个字节不改（`TestPublishedUpSegmentsAreFrozen` 兜底）。
2. **先看 diff 再修**；改函数体前用 `find-def.py <函数> --before <本迁移号> --body` 拿上一版原文逐字抄。
3. **同步登记与测试**：给 Down 补了超时或加了 irreversible，`ratchet.txt` 对应的 `*-down` 行变过期登记，要删；先 `grep` 哪些 PG18 用例会原样执行这个历史迁移的 Up 或 Down，改了 Down 连断言一起改。

## 6. 坑

- **seed 函数多路并行**：见根 CLAUDE.md「环境与工具坑」。动手前跑 `find-def.py app.seed_tenant_defaults`，看当前生效的是哪一版。
- **seed 契约按「最后一个定义它的迁移」核对**：notify 与 middleware 的契约按文件名排序，取最后一个包含 `FUNCTION app.seed_tenant_defaults(` 字样的迁移（整个文件，含注释）。只在注释里提到这串字、或只对它 `REVOKE … ON FUNCTION app.seed_tenant_defaults(uuid)` 的迁移，也会被当成最新定义。模板数写死在 `TestTenantSeedTemplatesMatchDefaults`，加模板同一提交改它。
- **契约与计数随迁移一起改**：seed 模板数；后台路由新用的权限码必须由某个迁移插进 `permissions`（`TestRoutePermissionsExistInCatalog` 扫全部迁移）；加了 PG18 域时 `run-pg18-gates.sh` 的 DOMAINS（相邻行冲突与夹具 id 撞号见 `rules/platform-pg18.md`）。
- **NO TRANSACTION 文件里的 SET**：会话级 `SET` 的写法见规则；样板在段尾 RESET，免得留给同一条连接上的下一个迁移。中途失败会留下 INVALID 索引，Up 要能重入。
- **00037–00040 的闸门**：这几个迁移的 Up / Down 要逐版本用 PGOPTIONS 批准，往返脚本的 `MIGRATE_OPTS` 原样带着批准跑；`rollback-to` 不代签，回到这么早只能从备份恢复。修它们的 Down 时，往返里看到的是「已批准」路径。

## 7. 集成分支的相对编号与重编号

几路先叠在一个集成分支上、全部合完再一次并主线时（S，见 dispatch-task「叠在集成分支上的路」），迁移不取主线号：主线在此期间还会前进（S6、C1a、approle B 都带迁移），先取的主线号会和后来者撞号，或排到本该在它之后跑的主线迁移前面。

**写的时候**
- 用设计给的相对编号，文件名是号段 09000 起的临时号：S-NN 写成 `090NN_<名>.sql`（S-01 → `09001_…`）。临时号排序永远在主线号之后，集成分支合进主线新迁移后，跑的顺序仍是「主线全部 → S 按相对顺序」，和最后并主线时一致。
- `next-number.sh` 把 09000 起的号单列为临时号，不计入下一个主线号。`check-migrations` 与 migrationlint 都认这个号段（只要求严格递增、不重复）。
- 迁移注释、Go 与测试里引用别的临时号迁移，写全文件名（`09003_…sql`），重编号时一条 grep 就能找全。
- 不登记 `upsegments.txt`：还没发布。`ratchet.txt`、往返 `KNOWN` 照样不豁免。
- 测试机上跑过临时号的库，装不了重编号之后的包（库里版本 09011 大于包里的最大号，`check-migrations.sh` 拒绝）。这种库只用于验证集成分支；并主线后要验证，用新库首装（panel-install）。

**重编号**（集成分支的最后一个提交；设计 §11.3 第 7 条）
1. 先在集成分支上合最新主线，跑 `next-number.sh` 取主线下一个号 N。按临时号顺序连续映射：09001 → N，09002 → N+1……不留空，相对顺序不变。
2. `git mv` 改文件名。
3. 改全部引用，改完下面两条 grep 都应为 0：

   ```bash
   git grep -nE '090(0[1-9]|[1-9][0-9])_' -- panel pdnd .claude/rules
   git grep -nwE '090(0[1-9]|[1-9][0-9])' -- panel pdnd .claude/rules
   ```

   要改的地方：Go 与测试里按文件名读迁移的（`filepath.Join(…, "migrations", "<文件>")`、`migrationSection`）、迁移文件头的 `-- contract-of:`、迁移和 Go 注释里的号、`run-pg18-gates.sh` 的 DOMAINS 注释、`RESERVED-TABLES.md`、规则文件。
4. `upsegments.txt` 这时不加，并主线时由 accept-task「并主线」一次加。
5. 自证：`bash panel/deploy/check-migrations_mock_test.sh`；`cd panel && GOTOOLCHAIN=go<go.mod 版本> go test ./tools/migrationlint/ ./internal/platform/db/` 加上第 3 步改到的包；推送后等 GitHub 的 panel-pg18：往返全过，DOMAINS 里各域 0 SKIP（往返只在 GitHub 跑，见第 4 节）。

**和 `rules/panel-migrations.md`「不要重编号」不冲突**：那条管的是进了主线、可能已经装进库的历史号（`goose_db_version` 记着）。临时号从没进过主线，重编号正是为了让它们第一次发布时就拿到主线号。

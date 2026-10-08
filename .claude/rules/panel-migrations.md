---
paths:
  - "panel/migrations/**"
  - "panel/deploy/migrate.sh"
  - "panel/deploy/check-migrations.sh"
  - "panel/deploy/configure-app-role.sql"
  - "panel/deploy/run-migration-roundtrip.sh"
  - "panel/deploy/MIGRATION-RUNBOOK.md"
  - "panel/tools/migrationlint/**"
  - "panel/internal/platform/db/**"
---

# 迁移与运行角色

- 编号只要求严格递增、不重复，允许空号。主序列的 00073、00091、00092 是历史空号，不要重编号去填（已装的库 `goose_db_version` 记着其后的版本）
  - `panel/deploy/migrate.sh` 与 `panel/deploy/check-migrations.sh` 用同一条规则，改一处要改两处；桩测试 `check-migrations_mock_test.sh`、`migrate_fail_closed_mock_test.sh`
- 已发布迁移的 Up 段一个字节都不改，要改行为就写新的前向迁移。守卫：`panel/tools/migrationlint/upsegments.txt` 冻结每个已发布 Up 段的 SHA-256（`TestPublishedUpSegmentsAreFrozen`）
  - 唯一例外是用户授权过的：给历史迁移追加 Down 段、在文件头（`-- +goose Up` 之前）加标记行。Up 段的定义去掉末尾空行，所以追加不影响冻结值
- 公开入口 `migrate.sh` 永远拒绝 down/redo。回滚只走带确认的 `migrate.sh rollback-to <版本>`，三种情形的操作见 `panel/deploy/MIGRATION-RUNBOOK.md`
  - rollback-to 的前置条件：
    - 声明写入者已停（`PANDORA_ROLLBACK_WRITERS_STOPPED=yes`，有 systemd 时再核实）；
    - 指向一份刚做的备份；
    - 范围内有 irreversible 就整体拒绝，并说明最多能回到哪；
    - 确认短语与当前、目标版本绑定（`rollback <当前> to <目标>`）。
  - 执行时逐个 down，第一个失败或被拒就停下。
  - 不下发任何 PGOPTIONS，00037–00040 的逐版本 down 批准不会被它代签。
  - 桩测试：`migrate-rollback_mock_test.sh`
- 建表、删表都要和 `panel/migrations/RESERVED-TABLES.md` 对上：重放全部 Up 段后仍存在的表，要么被非测试 Go 源码按名字引用，要么登记在册；登记了却被引用、或已被删掉，同样失败。守卫：`panel/internal/platform/db/schema_registry_test.go` 的 `TestSchemaTablesAreReferencedOrRegistered`
  - 坑：这条测试对 Go 源码做整词匹配，注释里写到登记簿里的表名也算「引用」，会让测试变红
  - 解开登记簿里某张表的锁（改 invariants.sql、configure-app-role.sql 或外键）属于需要用户授权的独立变更
- `panel/deploy/configure-app-role.sql` 把运行角色收窄成 `aegis_app`。末尾「列级提升回表级」的整表重授之后，必须再收回证据流水与守卫表的写权限（`gift_card_redemptions`、`traffic_reset_logs` 收 UPDATE/DELETE；`traffic_pack_grants`、`gift_card_batches` 业务要 UPDATE，只收 DELETE）。新增同类表，把它的 REVOKE 加在重授之后。守卫：`panel/internal/platform/db/configure_role_contract_test.go` 的 `TestConfigureAppRoleRevokesDeleteOnGuardedTablesLast`
- 迁移 DSN（`AEGIS_MIGRATION_DATABASE_URL`，超级用户）只给 `migrate.sh` 与 `check-migrations.sh` 用；`platform/config` 不读它，运行时代码也不要读

## Down、DDL 守卫与往返（用户 2026-10-07 定：迁移必须能回滚、DDL 出错要有预案）

- 每个迁移必须有 `-- +goose Down`。goose 对没有 Down 段的迁移执行 down 时只删版本行，回滚会静默「成功」
  - 能逆就写真正的逆操作：Down 之后的结构要和上一版逐项一致，包括列序、函数体原文、触发器、策略、授权、注释、集群级角色
    - 恢复被 Up 改掉的视图、函数、注释、索引时，从上一版的迁移里逐字抄（照 00022、00026、00027 的 Down）
    - 种子行在 Down 里保留：可能已被管理员改过、被别的行引用；旧版本不读这些行。Up 的插入带 NOT EXISTS，重跑不会重复（照 00023、00024、00028）
  - 确实不能逆的（纯种子、修数据，原值没留）：
    - 文件头写 `-- irreversible: <原因>` 和 `-- forward-fix: <前滚补救办法>`；
    - Down 里 `RAISE EXCEPTION` 拒绝（照 00029、00030）；
    - rollback-to 遇到它会在执行任何 Down 之前就拒绝，并指向备份恢复。
  - 带数据守卫的 Down（有不能丢的行时 RAISE，没有时照常回退，照 00057、00129、00012）不算 irreversible，不要标
  - Down 不对追加写表 DELETE（直接或经外键级联都会被语句级触发器拒绝，空表也拒）：种子行保留（00050），证据行用数据守卫（00012）；纯种子、删了会级联进证据表的标 irreversible（00010、00042）
  - 集群级对象：Up 装的扩展在 Down 里删（00001）；Up 建的角色只在整个集群都不再引用时才删（00038 查 `pg_shdepend`、成员关系与按库设置），否则保留并 NOTICE
  - 恢复约束时，存量数据允许就恢复成原样：00131 的外键先 NOT VALID 加回，再试 VALIDATE，有悬空引用才保持 NOT VALID
  - 发布物兜底：`check-migrations.sh` 拒绝既没有 Down 段、也没有 irreversible 文件头的迁移
- DDL lint：`panel/tools/migrationlint`，随 `go test ./...` 跑。新迁移必须全部满足：
  - Up 和 Down 开头、任何 DDL/DML 之前，`SET LOCAL lock_timeout` 与 `SET LOCAL statement_timeout`
    - 带 `-- +goose NO TRANSACTION` 的文件里 SET LOCAL 不生效，改用会话级 `SET`
    - irreversible 的 Down 只 RAISE，不要求设超时
  - 大表（清单在 `bigtables.go`，按用户数、节点数或时间增长的表；新表会增长就登记）上的三条：
    - 建索引用 `CREATE INDEX CONCURRENTLY`，文件带 `-- +goose NO TRANSACTION`，Up 要能重入（`IF NOT EXISTS`）。CONCURRENTLY 出现在事务内同样报错
    - 不做整表重写的 ALTER：改列类型、加带易变默认值的列（`gen_random_uuid()`、`clock_timestamp()`、`nextval` 等；`now()` 是稳定函数，不重写）、加存储型生成列或标识列、改存储属性。确有必要就在文件头写 `-- rewrite: <表> rows=<行数> est=<预估耗时>`，数字按 new-migration skill「回填耗时」一节在 5k 副本上量
    - 回填、批量 UPDATE/DELETE、INSERT…SELECT 写进大表时，文件头写 `-- backfill: batched-by=<分批方式>; rerunnable=<为什么重跑安全>`，做法见下一节
  - 删列、改列名、改表名走「扩展 → 迁移数据 → 收缩」，收缩单独一个发布。收缩那个迁移的文件头写 `-- contract-of: <扩展迁移编号>`
  - 同一个迁移里刚建的表是空的，不受大表规则约束；注释、字符串、函数体里的字不算语句，DO 块体算
  - lint 看不见 `EXECUTE format(...)` 拼出来的动态 SQL，那部分靠评审
  - 历史违例登记在 `ratchet.txt`（建守卫时 390 条，只登记 00133 及以前）。只许删不许加：新违例直接红，登记了却已不违例的条目也红
- 迁移往返：`panel/deploy/run-migration-roundtrip.sh`，GitHub 上作为 panel-pg18 job 的一步跑。检查机跳过整个 panel-pg18，所以不能另起 job
  - 空库开始逐个迁移：up 一步 → dump；down 一步 → 必须等于上一版的 dump；再 up → 必须等于第一次 up 的 dump
  - irreversible 的 down 必须失败，且版本与结构都不变
  - dump 是 `pg_dump --schema-only` 加 `pg_dumpall --roles-only`，按对象条目切开排序后比较
  - 全量约 3 分钟
  - 历史缺陷登记在脚本的 `KNOWN`，同样只许删不许加。建门禁时的 12 条已于 2026-10-07 全部修好（只改 Down 与文件头），现在为空：任何迁移往返不过即红
  - 改 Down 时先在本地读懂上一版的完整定义，再推到 GitHub 看往返结果；本机没有 Docker 跑不了它
- 一次性库预检（`check-migrations.sh`）由发布控制器、install.sh 与 install-native.sh 的升级在**停服之前**跑，整库克隆在停服窗口之外（安装器经 `install-lib.sh` 的 `pandora_run_migrations`，桩测试 `install-migrate-order_mock_test.sh`）
  - 通过后写预检凭据，内容是水位、迁移目录摘要、是否按停写口径演练
  - 停服后 `--verify-attestation` 只读核对：文件、水位、续费闸门，再与凭据比对，对不上就自动拉回旧服务
  - `migrate.sh up` 带 `PANDORA_PRECHECK_ATTESTATION` 时只做这次核对，不带时照旧完整预检，两样都没有就不跑 up
  - 克隆上的停写演练用 `PANDORA_PRECHECK_REHEARSE_STOPPED_WRITER=yes`。`PANDORA_STOPPED_WRITER_UPGRADE_APPROVED` 仍只表示「线上写入者已停」，不要在停服前传它
  - 桩测试：`check-migrations_mock_test.sh`；`release-stop-the-world_mock_test.sh` 用事件日志证明顺序，要 root，在 panel-deploy 的 deploy-root-mock-tests job 里用 sudo 跑

## 回填与数据修复（2026-10 第一、二波的做法）

- 回填按「租户 × UTC 自然日」分批，`ON CONFLICT DO UPDATE` 写重算值：单批事务短，失败重跑不会把数翻倍
- 回填要打耗时：goose 不显示 NOTICE。每批用 `RAISE NOTICE` 打行数与毫秒（psql 手跑时看），总计再用 `RAISE LOG` 写进库日志，发布后能从日志查到
- 多步解析写成 LATERAL 链时，每一步单独一层子查询加 `OFFSET 0`：SQL 函数被内联后，规划器会把各层拉平，把上一步整棵表达式（含正则）抄进每个引用处。00099 因此从 190s 降到 4.8s（5k 副本），源码契约钉着 `OFFSET 0` 的道数
- 回填分档：便宜的原始合计取现成列、不解析报文，覆盖长窗口；昂贵的严格口径只回填界面真会读的短窗口（00099 是 31 天 / 48 小时）。两档都按 UTC 整点对齐，每个桶要么整桶严格、要么整桶只有原始合计
- 外键放到回填之后，用 `ADD CONSTRAINT` 一次校验：批量写入时逐行外键触发器（约 46µs/行）比回填本身还贵
- 读路径、定时任务、回填共用一个 SQL 函数作口径的唯一出处（如 `app.node_traffic_payload_entries`）：口径只有一处可改；PG18 用例拿旧 SQL 逐项对照，并在计划里确认函数被内联（没有对它的 Function Scan）
- 回填耗时按两倍估：迁移预检先在克隆库上跑一遍，正式库再跑一遍。上线前按 new-migration skill「回填耗时」一节在 5k 副本上实测
- 数据修复迁移只往前推、不缩短，如只把落后的凭据到期、额度周期末拉齐到订阅周期末。注释写清哪些行可以安全改、哪些不碰（已吊销、空值、已用量）
- 不可逆的 Down 用 `RAISE EXCEPTION` 拒绝并写明原因（照 00102：修复前的值没留存，恢复就等于让用户重新 404），文件头写 `-- irreversible:` 与 `-- forward-fix:`
- 修复和回填要在 PG18 里实跑 Up：
  - 数据修复：以迁移角色执行这段 Up，断言三件事：旧写法留下的分叉被修好、已一致的行不变、重跑一次零改动
  - 回填：在回滚的事务里重跑迁移原文，与旧 SQL 逐项对照；再把行改脏重跑两遍，结果相同

## 序列、触发器与维护函数

- 「输入变没变」这类信号用序列，不用计数行：序列不加锁、不进事务，几百个并发写事务不会抢同一行，也不会和业务行锁成环（00101 的下发纪元）
- 把序列当版本号读时取 `last_value + is_called::int`：第一次 `nextval` 时 `last_value` 不变，只有 `is_called` 翻成 true，只读 `last_value` 会漏掉第一次前进（w1node 在 PG18 上踩到）
- 要在提交时才推进的信号，用约束触发器 `DEFERRABLE INITIALLY DEFERRED FOR EACH ROW`：信号与数据可见之间只隔提交本身
  - 高频表加 `WHEN`，只在「能不能用」翻转时触发，每次扣量、每次心跳都不推进（00101 的配额、流量包）
  - 新增会影响节点下发结果的表或列，要给 00101 那组触发器补同类的一条，否则 aegis-node 的缓存只能靠几秒的 TTL 兜底
- 只给某一张表改通用变更通知，照 00076 / 00022 的写法：
  - 不动 `app.notify_change()`，也不动别的表，只 DROP、重建这张表上的 `zz_notify_<表>`；
  - 文件头写清这张表为什么例外；
  - Down 原样恢复。
  - 要加 `WHEN` 时注意：引用 OLD 的触发器不能同时挂 INSERT / DELETE，要拆成「INSERT OR DELETE」和「UPDATE + WHEN」两个
- 运行角色要调的维护函数（清理、按租户回填）写成 `SECURITY DEFINER`，加固定 `search_path`，并限定本租户、分批、有上限
  - 函数体里不能 `ALTER TABLE … DISABLE TRIGGER`，那要求表属主；要绕开追加写触发器，就在函数定义上 `SET session_replication_role = replica`，只在函数执行期间生效（照 00100）
- 迁移开头 `SET LOCAL lock_timeout = '5s'` 和 `SET LOCAL statement_timeout`（DDL lint 强制，Up 与 Down 都要）：建触发器、加约束要拿表锁，拿不到就失败重来，不排在业务事务后面堵住整张表

## 编号、注释与并行

- 迁移注释和 Go 里的 SQL 注释都不写 RESERVED-TABLES.md 登记的保留表名，用描述代替
  - 表登记簿测试把 Go 非测试源码里的整词（含注释、SQL 字符串）都算作「引用」。迁移 `.sql` 本身不参与这项匹配，但迁移里的 SQL 常被原样抄进 Go（口径函数、对照 SQL），注释会跟着进去
  - 迁移 Up 段里行首是 `CREATE TABLE` / `DROP TABLE` 的任何文本（含块注释）会被当成建表、删表
- 几路并行时按号段分配：每路一段连续号，用不到就空着（允许空号）
- 合并按号从小到大：goose 不接受「库里已到 00106，又冒出一个没跑过的 00104」
- configure-app-role.sql 给 `aegis_app` 的参数一律按库写（`ALTER ROLE aegis_app IN DATABASE %I SET …`），不用集群级 `ALTER ROLE`。契约 `TestConfigureAppRolePinsQueryGuards` 钉着 `jit = off` 和 `statement_timeout = 15s`
  - 迁移以超级用户跑，不受这 15 秒限制；但迁移里建的、由运行角色调用的函数受它限制，所以要分批

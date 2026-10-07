---
paths:
  - "panel/migrations/**"
  - "panel/deploy/migrate.sh"
  - "panel/deploy/check-migrations.sh"
  - "panel/deploy/configure-app-role.sql"
  - "panel/internal/platform/db/**"
---

# 迁移与运行角色

- 编号只要求严格递增、不重复，允许空号。主序列的 00073、00091、00092 是历史空号，不要重编号去填（已装的库 `goose_db_version` 记着其后的版本）
  - `panel/deploy/migrate.sh` 与 `panel/deploy/check-migrations.sh` 用同一条规则，改一处要改两处；桩测试 `check-migrations_mock_test.sh`、`migrate_fail_closed_mock_test.sh`
- 已发布的迁移不改内容，只写新的前向迁移；`migrate.sh` 拒绝 down/redo，00067 的 Down 直接报错
- 建表、删表都要和 `panel/migrations/RESERVED-TABLES.md` 对上：重放全部 Up 段后仍存在的表，要么被非测试 Go 源码按名字引用，要么登记在册；登记了却被引用、或已被删掉，同样失败。守卫：`panel/internal/platform/db/schema_registry_test.go` 的 `TestSchemaTablesAreReferencedOrRegistered`
  - 坑：这条测试对 Go 源码做整词匹配，注释里写到登记簿里的表名也算「引用」，会让测试变红
  - 解开登记簿里某张表的锁（改 invariants.sql、configure-app-role.sql 或外键）属于需要用户授权的独立变更
- `panel/deploy/configure-app-role.sql` 把运行角色收窄成 `aegis_app`。末尾「列级提升回表级」的整表重授之后，必须再收回证据流水与守卫表的写权限（`gift_card_redemptions`、`traffic_reset_logs` 收 UPDATE/DELETE；`traffic_pack_grants`、`gift_card_batches` 业务要 UPDATE，只收 DELETE）。新增同类表，把它的 REVOKE 加在重授之后。守卫：`panel/internal/platform/db/configure_role_contract_test.go` 的 `TestConfigureAppRoleRevokesDeleteOnGuardedTablesLast`
- 迁移 DSN（`AEGIS_MIGRATION_DATABASE_URL`，超级用户）只给 `migrate.sh` 与 `check-migrations.sh` 用；`platform/config` 不读它，运行时代码也不要读

## 回填与数据修复（2026-10 第一、二波的做法）

- 回填按「租户 × UTC 自然日」分批，`ON CONFLICT DO UPDATE` 写重算值：单批事务短，失败重跑不会把数翻倍
- 回填要打耗时：goose 不显示 NOTICE。每批用 `RAISE NOTICE` 打行数与毫秒（psql 手跑时看），总计再用 `RAISE LOG` 写进库日志，发布后能从日志查到
- 多步解析写成 LATERAL 链时，每一步单独一层子查询加 `OFFSET 0`：SQL 函数被内联后，规划器会把各层拉平，把上一步整棵表达式（含正则）抄进每个引用处。00099 因此从 190s 降到 4.8s（5k 副本），源码契约钉着 `OFFSET 0` 的道数
- 回填分档：便宜的原始合计取现成列、不解析报文，覆盖长窗口；昂贵的严格口径只回填界面真会读的短窗口（00099 是 31 天 / 48 小时）。两档都按 UTC 整点对齐，每个桶要么整桶严格、要么整桶只有原始合计
- 外键放到回填之后，用 `ADD CONSTRAINT` 一次校验：批量写入时逐行外键触发器（约 46µs/行）比回填本身还贵
- 读路径、定时任务、回填共用一个 SQL 函数作口径的唯一出处（如 `app.node_traffic_payload_entries`）：口径只有一处可改；PG18 用例拿旧 SQL 逐项对照，并在计划里确认函数被内联（没有对它的 Function Scan）
- 回填耗时按两倍估：迁移预检先在克隆库上跑一遍，正式库再跑一遍。上线前用 bench-eval skill 在 5k 副本上实测
- 数据修复迁移只往前推、不缩短，如只把落后的凭据到期、额度周期末拉齐到订阅周期末。注释写清哪些行可以安全改、哪些不碰（已吊销、空值、已用量）
- 不可逆的 Down 用 `RAISE EXCEPTION` 拒绝并写明原因（照 00102：修复前的值没留存，恢复就等于让用户重新 404）
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
- 迁移开头 `SET LOCAL lock_timeout = '5s'`：建触发器、加约束要拿表锁，拿不到就失败重来，不排在业务事务后面堵住整张表

## 编号、注释与并行

- 迁移注释和 Go 里的 SQL 注释都不写 RESERVED-TABLES.md 登记的保留表名，用描述代替
  - 表登记簿测试把 Go 非测试源码里的整词（含注释、SQL 字符串）都算作「引用」。迁移 `.sql` 本身不参与这项匹配，但迁移里的 SQL 常被原样抄进 Go（口径函数、对照 SQL），注释会跟着进去
  - 迁移 Up 段里行首是 `CREATE TABLE` / `DROP TABLE` 的任何文本（含块注释）会被当成建表、删表
- 几路并行时按号段分配：每路一段连续号，用不到就空着（允许空号）
- 合并按号从小到大：goose 不接受「库里已到 00106，又冒出一个没跑过的 00104」
- configure-app-role.sql 给 `aegis_app` 的参数一律按库写（`ALTER ROLE aegis_app IN DATABASE %I SET …`），不用集群级 `ALTER ROLE`。契约 `TestConfigureAppRolePinsQueryGuards` 钉着 `jit = off` 和 `statement_timeout = 15s`
  - 迁移以超级用户跑，不受这 15 秒限制；但迁移里建的、由运行角色调用的函数受它限制，所以要分批

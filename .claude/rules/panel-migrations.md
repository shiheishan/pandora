---
paths:
  - "panel/db/migrations/**"
  - "panel/deploy/configure-app-role.sql"
  - "panel/deploy/bootstrap.sh"
  - "panel/internal/app/migrate.go"
---

# 迁移与运行角色

## goose 迁移

- 迁移在 `panel/db/migrations/*.sql`，`//go:embed *.sql` 嵌进二进制（`panel/db/migrations/migrations.go`），由 `pandora migrate up|down|status` 执行（`panel/internal/app/migrate.go`）。不用 GORM AutoMigrate。
- 迁移连接只读 `PANDORA_DATABASE_MIGRATION_URL`（属主角色 `pandora_owner`）；`serve` / `worker` 用运行角色 `pandora_app`，不读迁移 DSN。
- 编号严格递增；几路并行时合并按号从小到大。goose 的 Provider 缺省不接受「库里已到更高版本、又冒出一个没跑过的低号」。
- 每个迁移都要有 `-- +goose Down`，能逆就写真正的逆操作；确实不可逆的在 Down 里写明原因（照 `00001_base.sql` 留 citext 的写法）。
- 往返两处验证：
  - `panel/db/migrations/migrations_test.go` 的 `TestRoundTrip`：Up → `DownAll` → Up，并断言全部版本 Applied（要 `PANDORA_TEST_DATABASE_URL`）。
  - CI `panel-newstack.yml` 的 Migration round trip 步骤：用编好的二进制跑 `migrate up` → `status` → `down` → `up`。
- 函数体、DO 块用 `-- +goose StatementBegin` / `StatementEnd` 包住，否则 goose 按分号切错。

## 锁与 DDL 预案（会锁大表、失败代价高的 DDL 写清预检、超时与回滚）

- 改已有表的迁移开头写 `SET LOCAL lock_timeout = '5s'`（拿不到锁就失败重来，不排在业务事务后面堵整张表），耗时可能长的再加 `SET LOCAL statement_timeout`。
- 文件头写三段：锁与耗时（拿什么锁、挡住什么、和哪张表的行数成正比）、预检（上线前查什么行数）、失败回滚（失败后库是什么状态、怎么重试）。范本：`00016_commission_review.sql`、`00024_node_alive_by_subscription.sql`。
- goose 缺省把每个迁移放在一个事务里，事务内不能 `CREATE INDEX CONCURRENTLY`。表到百万行级时拆成带 `-- +goose NO TRANSACTION` 的迁移（加约束用 `NOT VALID` 再 `VALIDATE`）；这种文件里 `SET LOCAL` 不生效，改用会话级 `SET`。现有迁移都还没用到。

## 只追加表与禁删表

- 证据类表（账本、审计、流水）挂 `deny_mutation()`（BEFORE UPDATE OR DELETE OR TRUNCATE，FOR EACH STATEMENT）；允许改状态、不许删的表挂 `deny_delete()`。两个函数在 `00001_base.sql`，用例 `TestDenyMutation`。
- 语句级触发器对空表也拒：Down 里不要对这类表 DELETE，直接 DROP 表或保留行。
- 保留期清理走 `SECURITY DEFINER` 函数，定义上带 `SET search_path = pg_catalog, public, pg_temp` 与 `SET session_replication_role = replica`，分批、每批有上限，并 `REVOKE ALL … FROM PUBLIC`（照 `00023_node_retention.sql` 的 `purge_node_metrics`）。新加一个这类函数，要同时在 `panel/deploy/configure-app-role.sql` 的白名单里登记，否则 grants 失败。

## 运行角色收敛（configure-app-role.sql）

- 安装顺序：`bootstrap.sh roles` 建库与两个角色 → `pandora migrate up` → `bootstrap.sh grants` 以 postgres 超级用户跑 `panel/deploy/configure-app-role.sql`（`panel/deploy/install-lib.sh` 的 `pandora_bootstrap_db` / `pandora_migrate`）。每次迁移后都要重跑 grants。
- 这份 SQL 是幂等的收敛脚本，不是迁移。它做的事：
  - 按库给 `pandora_app` 钉住 `search_path`、`jit = off`、`statement_timeout = '15s'`（`ALTER ROLE … IN DATABASE`，不用集群级设置）；
  - `pandora_app` 不许是任何表、函数、schema、类型的属主，否则 RAISE；
  - 收回 `goose_db_version` 的全部权限；
  - 按 `deny_mutation` / `deny_delete` 触发器自动收回对应表的 UPDATE / DELETE；
  - SECURITY DEFINER 函数按白名单授 EXECUTE，发现没登记的直接失败。
- 新表靠这份脚本自动收权，迁移里不要写针对 `pandora_app` 的 GRANT / REVOKE。
- 迁移由属主角色跑，不受 15 秒限制；但迁移里建的、由运行角色调用的函数受这个限制，要分批。
- `jit = off` 在 `panel/deploy/postgresql-pandora.conf` 与开发数据基座 `panel/dev/docker-compose.yml` 也各关一次，是有意的双保险，不要在会话里再打开。

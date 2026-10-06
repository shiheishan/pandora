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

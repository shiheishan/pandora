-- <一句话：这个迁移做什么>（<谁在什么时候定的，如 用户 2026-10-07 定>；<任务路名>）。
--
-- <为什么要它；读写它的是哪段 Go（包.函数）；锁与耗时：拿什么锁、多大的表、预计多久>。
-- 注释里不写 RESERVED-TABLES.md 登记的保留表名（会被抄进 Go，触发表登记簿测试）。
--
-- 文件头标记（按需，写在 `-- +goose Up` 之前，格式由 migrationlint 解析）：
--   -- backfill: batched-by=<分批方式>; rerunnable=<为什么重跑安全>   大表回填 / 批量改写
--   -- rewrite: <表> rows=<行数> est=<预估耗时>                      大表整表重写（数字用 bench-eval 量）
--   -- contract-of: <扩展迁移编号>                                    删列、改名的收缩阶段

-- +goose Up
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

-- 只含普通 DDL 的语句不用包 StatementBegin/End；函数体、DO 块里有分号，必须包。
CREATE TABLE <表> (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id   uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  -- <业务列>
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now()
);

-- <索引用途：哪条查询走它>
CREATE INDEX idx_<表>_recent ON <表> (tenant_id, created_at DESC);

-- 租户隔离：ENABLE + FORCE RLS + tenant_isolation 策略（00001 的 app.enable_tenant_rls）
SELECT app.enable_tenant_rls('<表>');

-- 运行角色只拿要用的权限。新表的默认权限只有 SELECT，INSERT / UPDATE 要在这里显式给。
-- 不许删的表在这里收 DELETE，并在 configure-app-role.sql 的表级重授之后再收一次（见 SKILL.md）。
GRANT SELECT, INSERT, UPDATE ON <表> TO aegis_app;
REVOKE DELETE, TRUNCATE ON <表> FROM aegis_app;

COMMENT ON TABLE <表> IS '<一句话说明>';

-- +goose Down
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

-- 逆操作要把结构退回上一版的逐项原样（列序、函数体原文、触发器、策略、授权、注释）。
-- 有不能丢的数据时加数据守卫（有行就 RAISE、没有就照常退，照 00129 / 00134），这不算 irreversible。
DROP TABLE IF EXISTS <表>;

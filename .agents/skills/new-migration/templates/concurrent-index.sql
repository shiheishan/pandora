-- +goose NO TRANSACTION
-- <一句话：给大表 <表> 加哪条索引、哪条查询走它>（<谁定的>；<任务路名>）。
--
-- 大表（panel/tools/migrationlint/bigtables.go）上建索引必须 CONCURRENTLY，而 CONCURRENTLY 不能在
-- 事务里跑，所以整个文件 NO TRANSACTION：
--   - SET LOCAL 不生效，用会话级 SET，文件末尾 RESET（不留给同一连接上的下一个迁移）；
--   - 每条语句单独提交，中途失败会留下 INVALID 索引：Up 必须可重入（IF NOT EXISTS），
--     runbook 第 1 节第 5 步写了先 DROP INDEX CONCURRENTLY IF EXISTS 再重跑；
--   - 这种文件里只放建 / 删索引，别的 DDL 放进另一个普通迁移。

-- +goose Up
SET lock_timeout = '5s';
SET statement_timeout = '30min';

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_<表>_<列>
  ON public.<表> (tenant_id, <列>);

RESET statement_timeout;
RESET lock_timeout;

-- +goose Down
SET lock_timeout = '5s';
SET statement_timeout = '30min';

DROP INDEX CONCURRENTLY IF EXISTS public.idx_<表>_<列>;

RESET statement_timeout;
RESET lock_timeout;

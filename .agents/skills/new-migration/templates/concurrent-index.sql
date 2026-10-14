-- +goose NO TRANSACTION
-- +goose Up
-- <一句话：给 <表> 加哪条索引、哪条查询走它>（<谁定的>）。属 <模块> 模块。
--
-- 表已经大到普通 CREATE INDEX 会长时间挡写（预检：SELECT count(*) FROM <表> 的结果 <行数>），所以 CONCURRENTLY。
-- CONCURRENTLY 不能在事务里跑，所以整个文件 NO TRANSACTION：
--   - SET LOCAL 不生效，用会话级 SET，段尾 RESET（不留给同一连接上的下一个迁移）；
--   - 每条语句单独提交，中途失败会留下 INVALID 索引：Up 必须可重入（IF NOT EXISTS）；
--     失败后先 DROP INDEX CONCURRENTLY IF EXISTS <索引> 再重跑 migrate up；
--   - 这种文件里只放建 / 删索引，别的 DDL 放进另一个普通迁移。
-- 小表不用这个样板：普通 CREATE INDEX 放进事务里的迁移即可（照 00024_node_alive_by_subscription.sql）。
SET lock_timeout = '5s';
SET statement_timeout = '30min';

CREATE INDEX CONCURRENTLY IF NOT EXISTS <表>_<列>_idx ON <表> (<列>);

RESET statement_timeout;
RESET lock_timeout;

-- +goose Down
SET lock_timeout = '5s';
SET statement_timeout = '30min';

DROP INDEX CONCURRENTLY IF EXISTS <表>_<列>_idx;

RESET statement_timeout;
RESET lock_timeout;

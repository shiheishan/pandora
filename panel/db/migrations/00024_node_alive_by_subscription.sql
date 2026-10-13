-- +goose Up
-- node 模块：在线来源按订阅取设备数（门户订阅详情、后台设备总览按订阅 id 查窗口内的设备）。
-- 依赖 00022_node_traffic.sql。
-- 不用 CONCURRENTLY：goose 默认在事务里跑迁移，事务里不能 CREATE INDEX CONCURRENTLY；
-- 这张表只存在线来源、保留 31 天，行数小，普通建索引只短暂挡住在线上报的写（读不受影响）。
--   预检：SELECT count(*) FROM node_alive_ips。到百万级时改成 NO TRANSACTION 迁移用 CONCURRENTLY。
--   失败回滚：任一步失败（含 lock_timeout 超时）整笔回滚，库保持原样，重试即可；在线上报丢的一分钟下一轮就补上。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

CREATE INDEX node_alive_ips_subscription_idx ON node_alive_ips (subscription_id, last_seen_at);

-- +goose Down
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

DROP INDEX IF EXISTS node_alive_ips_subscription_idx;

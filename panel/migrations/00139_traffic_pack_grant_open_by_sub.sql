-- +goose NO TRANSACTION
-- 按订阅取还有余量的流量包（购买模型统一 Q5；w7buya）。
--
-- 节点下发的耗尽判断、扣量加锁、门户与拉取订阅的余量、到期提醒扫描，都从「按用户」改成「按订阅」
-- （B 路：uniproxy.go、uniproxy_traffic.go、pull.go、my_subscriptions.go、notify/scan.go），走这条
-- 部分索引，形状与 00070 的 idx_traffic_pack_grants_open（按 user_id）一致。放在 00138 回填之后建，
-- 回填期间不用维护它。旧的按用户索引在 B 合入后没人用，删它单独一个迁移（contract-of: 00139），
-- 跟下一次发布走。
--
-- traffic_pack_grants 是大表：CONCURRENTLY + NO TRANSACTION，会话级 SET、段尾 RESET；中途失败会
-- 留下 INVALID 索引，Up 用 IF NOT EXISTS 可重入（MIGRATION-RUNBOOK 第 1 节第 5 步）。
-- 5k 副本上造 1 万笔余额后建索引约 30ms（对照机空闲）。

-- +goose Up
SET lock_timeout = '5s';
SET statement_timeout = '30min';

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_traffic_pack_grants_open_sub
  ON public.traffic_pack_grants (tenant_id, subscription_id, created_at, id)
  WHERE consumed_bytes < granted_bytes;

RESET statement_timeout;
RESET lock_timeout;

-- +goose Down
SET lock_timeout = '5s';
SET statement_timeout = '30min';

DROP INDEX CONCURRENTLY IF EXISTS public.idx_traffic_pack_grants_open_sub;

RESET statement_timeout;
RESET lock_timeout;

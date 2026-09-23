-- +goose Up
-- 给同源账号视图加时间窗。
--
-- 原来的视图对 audit_events 做全表 GROUP BY。审计表是只增不删的，
-- 现在几百行没感觉，一年后几千万行时风控页会直接打不开 ——
-- 而那正是最需要它的时候。
--
-- 窗口取 90 天：批量注册的号往往在几小时内集中出现，
-- 90 天足够覆盖「先养号后使用」这类慢速手法，
-- 又能让扫描量随时间保持有界而不是随历史无限增长。
--
-- 需要看更久以前的关联时，直接查 audit_events 本表 —— 那是低频的
-- 事后取证，慢一点可以接受；风控页是高频的日常巡查，不能慢。

CREATE OR REPLACE VIEW audit_ip_clusters AS
SELECT tenant_id,
       source_ip_hash,
       count(DISTINCT actor_id)           AS account_count,
       count(*)                           AS event_count,
       min(occurred_at)                   AS first_seen,
       max(occurred_at)                   AS last_seen,
       array_agg(DISTINCT actor_id::text) AS accounts
  FROM audit_events
 WHERE source_ip_hash IS NOT NULL
   AND actor_kind = 'user'
   AND actor_id IS NOT NULL
   AND occurred_at > now() - interval '90 days'
 GROUP BY tenant_id, source_ip_hash
HAVING count(DISTINCT actor_id) > 1;

COMMENT ON VIEW audit_ip_clusters IS
  '近 90 天内同一来源 IP 关联到的多个账号。仅为线索：共用出口 IP 在学校、
   公司、家庭网络下是正常现象，需结合注册时间、行为模式一起判断。
   需要更长的历史请直接查 audit_events。';

-- 让时间窗真正能裁剪。
--
-- 已有的 audit_events_ip_idx 是 (tenant_id, source_ip_hash, occurred_at)，
-- 前导列是 IP 哈希，按时间过滤时用不上。这条把 occurred_at 提到前面，
-- 并且只索引带来源信息的用户事件 —— 系统事件占了审计表的大头，
-- 它们不参与聚类，放进索引只是浪费。
CREATE INDEX IF NOT EXISTS audit_events_cluster_idx
  ON audit_events (tenant_id, occurred_at DESC, source_ip_hash)
  WHERE source_ip_hash IS NOT NULL AND actor_kind = 'user' AND actor_id IS NOT NULL;

-- +goose Up
-- 审计里的可还原来源信息
--
-- 现状是只存 IP 的哈希。那对「同一个 IP 注册了多少账号」这类关联分析够用
-- （哈希相同即同一 IP），但管理员看不到 IP 本身 —— 也就判断不了
-- 这是家宽还是机房、来自哪个国家、是不是某个已知的滥用段。
-- 而这几件事恰恰是识别批量注册和薅号的主要依据。
--
-- 解法是两份都存，各司其职：
--   source_ip_hash  HMAC，不可逆。用于 GROUP BY、去重计数、关联分析
--   source_ip_enc   信封加密。管理员在后台查看时解密
--
-- 拖库的人拿到的是一列不可逆哈希加一列没有密钥打不开的密文；
-- 管理员在后台拿到的是完整明文。两个需求不必二选一。
--
-- 主密钥与数据库分开保管这一条依然成立（见 00019 的说明）。

ALTER TABLE audit_events
  ADD COLUMN IF NOT EXISTS source_ip_enc bytea;

COMMENT ON COLUMN audit_events.source_ip_enc IS
  '信封加密后的来源 IP，供管理员在后台查看。与 source_ip_hash 配合：
   哈希用于关联分析，密文用于展示。';

ALTER TABLE subscription_fetch_log
  ADD COLUMN IF NOT EXISTS ip_enc bytea,
  ADD COLUMN IF NOT EXISTS ua_enc bytea;

COMMENT ON COLUMN subscription_fetch_log.ip_enc IS
  '信封加密后的来源 IP。订阅链接被异常多来源拉取时，管理员需要看到
   究竟是哪些地址，才能判断是分享泄露还是扫描探测。';

-- 画像查询要按用户和时间翻，没有索引会随着审计量增长越来越慢
CREATE INDEX IF NOT EXISTS audit_events_actor_time_idx
  ON audit_events (tenant_id, actor_id, occurred_at DESC);

-- 关联分析的核心索引：给定一个 IP 哈希，反查所有用过它的账号
CREATE INDEX IF NOT EXISTS audit_events_ip_idx
  ON audit_events (tenant_id, source_ip_hash, occurred_at DESC)
  WHERE source_ip_hash IS NOT NULL;

-- 同一 IP 关联出的账号视图。
--
-- 这是抓「一人多号」最直接的信号：正常用户不会和陌生人共用出口 IP，
-- 而批量注册的号往往来自同一台机器或同一个代理池。
--
-- 注意它只是线索不是判决 —— 学校、公司、家庭共用出口 IP 是常态，
-- 所以视图里保留了账号数与时间跨度，让人去判断，而不是直接给结论。
CREATE OR REPLACE VIEW audit_ip_clusters AS
SELECT tenant_id,
       source_ip_hash,
       count(DISTINCT actor_id)                          AS account_count,
       count(*)                                          AS event_count,
       min(occurred_at)                                  AS first_seen,
       max(occurred_at)                                  AS last_seen,
       array_agg(DISTINCT actor_id::text)                AS accounts
  FROM audit_events
 WHERE source_ip_hash IS NOT NULL
   AND actor_kind = 'user'
   AND actor_id IS NOT NULL
 GROUP BY tenant_id, source_ip_hash
HAVING count(DISTINCT actor_id) > 1;

COMMENT ON VIEW audit_ip_clusters IS
  '同一来源 IP 关联到的多个账号。仅为线索：共用出口 IP 在学校、公司、
   家庭网络下是正常现象，需结合注册时间、行为模式一起判断。';

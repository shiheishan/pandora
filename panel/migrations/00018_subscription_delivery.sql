-- +goose Up
-- +goose StatementBegin
-- 订阅分发与访问审计（XBD-002 延伸）
--
-- subscription_credentials 已经有 fetch_count / last_fetched_at / last_fetch_ip_hash，
-- 但那只是「最后一次」的快照，回答不了真正要紧的问题：
--   这条链接是不是被分享出去了？（同时有多少个不同来源在拉）
--   泄露是从哪天开始的？（需要时间序列）
--   哪些是扫描器的试探？（失败的拉取同样要留痕）
--
-- 因此单独建一张时间序列表。它是纯审计数据，不参与任何业务判断，
-- 保留 30 天足够定位问题，再久就只是负担。

CREATE TABLE IF NOT EXISTS subscription_fetch_log (
  id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  tenant_id      uuid NOT NULL,
  -- 凭据可能已被吊销删除，这里不加外键，保留审计痕迹
  credential_id  uuid,
  subscription_id uuid,

  fetched_at     timestamptz NOT NULL DEFAULT now(),

  -- IP 与 UA 只存带盐哈希。
  --
  -- 判断「是不是多个地方在用同一条链接」只需要区分「是不是同一个来源」，
  -- 不需要知道来源具体是谁。存明文 IP 等于凭空攒了一份用户行踪档案，
  -- 一旦库被拖走，泄露的是真实用户的地理位置轨迹 —— 那比订阅本身敏感得多。
  ip_hash        bytea,
  ua_hash        bytea,
  -- 客户端家族（clash / sing-box / shadowrocket / v2rayn / unknown），
  -- 用于分发格式统计，不含任何可识别信息
  ua_family      text,

  -- 结果：ok / not_found / revoked / expired / rate_limited
  --
  -- 失败的也记：扫描器探测留下的正是一串 not_found，
  -- 只记成功的话，被扫这件事在数据里完全看不见。
  result         text NOT NULL,
  format         text,
  node_count     int,
  bytes_sent     int
);

CREATE INDEX IF NOT EXISTS subscription_fetch_log_cred_idx
  ON subscription_fetch_log (tenant_id, credential_id, fetched_at DESC);
CREATE INDEX IF NOT EXISTS subscription_fetch_log_time_idx
  ON subscription_fetch_log (tenant_id, fetched_at DESC);

ALTER TABLE subscription_fetch_log ENABLE ROW LEVEL SECURITY;
ALTER TABLE subscription_fetch_log FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS subscription_fetch_log_tenant ON subscription_fetch_log;
CREATE POLICY subscription_fetch_log_tenant ON subscription_fetch_log
  USING (tenant_id = app.current_tenant_id())
  WITH CHECK (tenant_id = app.current_tenant_id());
GRANT SELECT, INSERT ON subscription_fetch_log TO aegis_app;

-- 审计数据只增不改
SELECT app.make_append_only('subscription_fetch_log');

-- 清理过期审计。与 node_metrics 的做法一致，由定时任务调用。
CREATE OR REPLACE FUNCTION app.purge_subscription_fetch_log(retain interval DEFAULT '30 days')
RETURNS bigint LANGUAGE plpgsql SECURITY DEFINER AS $$
DECLARE
  removed bigint;
BEGIN
  DELETE FROM subscription_fetch_log WHERE fetched_at < now() - retain;
  GET DIAGNOSTICS removed = ROW_COUNT;
  RETURN removed;
END;
$$;

-- 凭据轮换次数：让用户能看到自己换过几次，也便于排查
ALTER TABLE subscription_credentials
  ADD COLUMN IF NOT EXISTS rotated_count int NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS rotated_at timestamptz;

-- 分发路径前缀。
--
-- 不做成常量而是每个租户一个随机值：所有部署都用同一个路径的话，
-- 扫全网只要试那一个路径就能把用这套面板的站点一次性捞干净。
-- 前缀随机意味着扫描器得先猜中前缀才谈得上猜 token。
ALTER TABLE tenants
  ADD COLUMN IF NOT EXISTS sub_path_prefix text;

UPDATE tenants
   SET sub_path_prefix = encode(gen_random_bytes(6), 'hex')
 WHERE sub_path_prefix IS NULL;

COMMENT ON TABLE subscription_fetch_log IS
  '订阅拉取审计。IP/UA 仅存带盐哈希，保留 30 天。失败记录同样保存，用于识别扫描探测。';
COMMENT ON COLUMN tenants.sub_path_prefix IS
  '订阅分发路径前缀，随机生成，避免全网按固定路径批量识别。';
-- +goose StatementEnd

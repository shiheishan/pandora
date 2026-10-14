-- +goose Up
-- subscription 模块（P4）：订阅拉取日志、按天用量，以及订阅事件新增 link_rotated（换链接）。
-- 依赖 00011_subscription.sql 的 subscriptions、subscription_credentials、subscription_events。
--
-- 与旧表相比：去掉租户列与 RLS；拉取日志主键改 uuidv7，结果与格式加 CHECK，未认证失败（not_found）不带凭据、
-- 其余结果必须带凭据与订阅；拉取限流改 Redis，不再按日志计数，所以凭据表不加回旧的每小时上限列。
-- 拉取日志只追加（deny_mutation），保留 31 天（与旧库相同），清理只走下面的 SECURITY DEFINER 函数。

-- 订阅拉取日志（只追加）：每次拉取一行，未认证失败按来源采样落库（不是每次都写）。
-- IP 与 UA 两份：*_hash 是 HMAC（子密钥 pandora/hmac/sub-audit/v1），用来按来源关联；*_enc 是封存的原文，后台查看时解开。
CREATE TABLE subscription_fetch_log (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  credential_id   uuid REFERENCES subscription_credentials(id) ON DELETE RESTRICT,
  subscription_id uuid REFERENCES subscriptions(id) ON DELETE RESTRICT,
  fetched_at      timestamptz NOT NULL DEFAULT now(),
  ip_hash         bytea CHECK (ip_hash IS NULL OR octet_length(ip_hash) = 32),
  ua_hash         bytea CHECK (ua_hash IS NULL OR octet_length(ua_hash) = 32),
  ip_enc          bytea,
  ua_enc          bytea,
  -- 客户端家族（clash / sing-box / shadowrocket …），不含可识别信息
  ua_family       text NOT NULL CHECK (char_length(ua_family) BETWEEN 1 AND 32),
  -- ok 成功；expired 令牌有效、订阅过期（回提示节点）；rate_limited 超限；error 认证通过后读库或取线路失败；
  -- not_found 前缀或令牌不对、凭据失效、主人停用（扫描器的主要流量）
  result          text NOT NULL CHECK (result IN ('ok', 'expired', 'rate_limited', 'error', 'not_found')),
  format          text CHECK (format IS NULL OR format IN ('clash', 'clash-premium', 'singbox', 'uri')),
  node_count      int NOT NULL DEFAULT 0 CHECK (node_count >= 0),
  bytes_sent      int NOT NULL DEFAULT 0 CHECK (bytes_sent >= 0),
  -- 未认证失败不知道是哪条凭据；其余结果都认出了凭据与订阅
  CONSTRAINT subscription_fetch_log_cred_shape
    CHECK ((result = 'not_found') = (credential_id IS NULL) AND (credential_id IS NULL) = (subscription_id IS NULL))
);

-- 门户链接列表的「近 24 小时不同来源数」按凭据与时间取
CREATE INDEX subscription_fetch_log_cred_idx ON subscription_fetch_log (credential_id, fetched_at DESC)
  WHERE credential_id IS NOT NULL;
-- 保留期清理按时间从最早的一端删
CREATE INDEX subscription_fetch_log_time_idx ON subscription_fetch_log (fetched_at);

CREATE TRIGGER subscription_fetch_log_append_only
  BEFORE UPDATE OR DELETE OR TRUNCATE ON subscription_fetch_log
  FOR EACH STATEMENT EXECUTE FUNCTION deny_mutation();

-- 保留期清理：删 p_before 之前的拉取日志，每次最多 p_batch_limit 行，由定时任务循环调用。
-- 截止时间不晚于 31 天前（与旧库相同）：调用方传更晚的时间也只删 31 天以前的，不能借它删近期证据。
-- SECURITY DEFINER 以迁移角色执行；session_replication_role = replica 只在本函数执行期间让追加写触发器不触发。
-- +goose StatementBegin
CREATE FUNCTION purge_subscription_fetch_log(p_before timestamptz, p_batch_limit int)
RETURNS int
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public, pg_temp
SET session_replication_role = replica
AS $$
DECLARE
  v_cutoff  timestamptz;
  v_deleted int;
BEGIN
  IF p_before IS NULL THEN
    RAISE EXCEPTION '截止时间不能为空' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_batch_limit IS NULL OR p_batch_limit < 1 OR p_batch_limit > 10000 THEN
    RAISE EXCEPTION '每批删除行数必须在 1 到 10000 之间' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  v_cutoff := least(p_before, now() - interval '31 days');

  DELETE FROM public.subscription_fetch_log f
   USING (SELECT id
            FROM public.subscription_fetch_log
           WHERE fetched_at < v_cutoff
           ORDER BY fetched_at
           LIMIT p_batch_limit) d
   WHERE f.id = d.id;
  GET DIAGNOSTICS v_deleted = ROW_COUNT;
  RETURN v_deleted;
END $$;
-- +goose StatementEnd

-- 运行角色的 EXECUTE 由部署时的角色配置授予（运行角色不是本函数的属主）
REVOKE ALL ON FUNCTION purge_subscription_fetch_log(timestamptz, int) FROM PUBLIC;

-- 按天用量：每份订阅每个自然日（站点时区）一行计费后字节数，门户「本期用量」柱状图、今天与日均读它。
-- 写入方是 P5 的节点流量入账（与扣量同一事务 upsert）；P4 只读。它是扣量的派生读数，不是账目证据，
-- 所以不挂追加写守卫（入账要累加）。订阅不删，行随订阅一直留着。
CREATE TABLE subscription_usage_daily (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  subscription_id uuid NOT NULL REFERENCES subscriptions(id) ON DELETE RESTRICT,
  day             date NOT NULL,
  bytes           bigint NOT NULL DEFAULT 0 CHECK (bytes >= 0),
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT subscription_usage_daily_sub_day_key UNIQUE (subscription_id, day)
);

-- 报表按天汇总全站用量
CREATE INDEX subscription_usage_daily_day_idx ON subscription_usage_daily (day);

-- 订阅事件加 link_rotated：换链接（吊销旧凭据、签新凭据、换节点身份）也是「为什么我的订阅变了」的一种。
-- 00011 里这条 CHECK 是列约束，名字由 PG 按「表_列_check」生成。
ALTER TABLE subscription_events DROP CONSTRAINT subscription_events_event_type_check;
ALTER TABLE subscription_events ADD CONSTRAINT subscription_events_event_type_check
  CHECK (event_type IN ('activated', 'renewed', 'extended', 'plan_changed', 'expired', 'renewal_closed', 'link_rotated'));

-- +goose Down
-- 事件表只追加，已有的 link_rotated 行删不掉：加回旧 CHECK 时用 NOT VALID，只约束之后的写入。
-- 空库（CI 的 up → down → up）上与原约束等价。
ALTER TABLE subscription_events DROP CONSTRAINT subscription_events_event_type_check;
ALTER TABLE subscription_events ADD CONSTRAINT subscription_events_event_type_check
  CHECK (event_type IN ('activated', 'renewed', 'extended', 'plan_changed', 'expired', 'renewal_closed')) NOT VALID;
DROP TABLE IF EXISTS subscription_usage_daily;
DROP FUNCTION IF EXISTS purge_subscription_fetch_log(timestamptz, int);
-- 触发器与索引随表删除。
DROP TABLE IF EXISTS subscription_fetch_log;

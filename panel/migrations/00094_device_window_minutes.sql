-- 设备识别窗口可选（D-B-4 方案 B，契约 R103）。
--
-- 「在线设备」= 最近一个窗口内上报过的去重 IP。窗口原先写死 5 分钟，现在按租户
-- 读设置键 device_limit.window_minutes，只认 5 / 10 / 30 / 60，缺行或存了别的值
-- 一律按 5——读不懂的配置不该把窗口放宽，也不该让在线数归零。
--
-- 窗口只有一个出处：app.device_limit_window_minutes。视图 subscription_online_devices
-- 用它，于是 uniproxy 的 strict 判定、后台设备概览、用户列表与详情、门户订阅的
-- 在线数自动跟着变；后台节点列表的在线人数与 IP 统计也改为调它，不再另写字面量。
--
-- 事实更正（00024 的注释）：窗口「与节点端 TTL 对齐」只对 compat 构建成立。生产
-- NativeCore 按连接进出跟踪设备、没有 5 分钟 TTL，节点每 60 秒上报一次在线 IP，
-- 所以这是纯面板口径，窗口不低于 5 分钟即可。代价：窗口越长，换了网络的旧 IP
-- 被多算得越久；strict 模式下超限的订阅要等大约一个窗口才恢复下发。

-- +goose Up

-- +goose StatementBegin
CREATE FUNCTION app.device_limit_window_minutes(p_tenant uuid) RETURNS int
LANGUAGE sql STABLE PARALLEL SAFE AS $$
  SELECT COALESCE((
    SELECT CASE s.value #>> '{}'
             WHEN '5' THEN 5 WHEN '10' THEN 10 WHEN '30' THEN 30 WHEN '60' THEN 60
           END
      FROM public.system_settings s
     WHERE s.tenant_id = p_tenant AND s.key = 'device_limit.window_minutes'), 5)
$$;

COMMENT ON FUNCTION app.device_limit_window_minutes(uuid) IS
  'R103：租户的设备识别窗口（分钟），只认 5/10/30/60，缺行或非法值按 5。在线设备数的唯一窗口来源。';

GRANT EXECUTE ON FUNCTION app.device_limit_window_minutes(uuid) TO aegis_app;

-- 列与 00024 一致，只换窗口
CREATE OR REPLACE VIEW subscription_online_devices AS
SELECT tenant_id,
       subscription_id,
       count(DISTINCT ip_hash)                    AS device_count,
       count(DISTINCT node_id)                    AS node_count,
       max(last_seen_at)                          AS last_seen_at
  FROM node_alive_ips
 WHERE last_seen_at > now() - make_interval(mins => app.device_limit_window_minutes(tenant_id))
 GROUP BY tenant_id, subscription_id;

COMMENT ON VIEW subscription_online_devices IS
  '每条订阅当前在线的设备数（按 IP 去重，跨节点汇总）。窗口按租户读 device_limit.window_minutes（5/10/30/60，缺省 5，R103）。';
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
CREATE OR REPLACE VIEW subscription_online_devices AS
SELECT tenant_id,
       subscription_id,
       count(DISTINCT ip_hash)                    AS device_count,
       count(DISTINCT node_id)                    AS node_count,
       max(last_seen_at)                          AS last_seen_at
  FROM node_alive_ips
 WHERE last_seen_at > now() - interval '5 minutes'
 GROUP BY tenant_id, subscription_id;

COMMENT ON VIEW subscription_online_devices IS
  '每条订阅当前在线的设备数（按 IP 去重，跨节点汇总）。窗口 5 分钟，与节点端 TTL 一致。';

DROP FUNCTION IF EXISTS app.device_limit_window_minutes(uuid);
-- device_limit.window_minutes 的设置行不删：回滚后的代码不读它，留着无害，
-- 重新 Up 时管理员选过的窗口原样生效
-- +goose StatementEnd

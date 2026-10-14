-- 压测前后对 settings 表的改动（只在测试机执行），用 action 选一段：
--   sudo /opt/pandora/deploy/psql.sh -v action=show -f perf_settings.sql   读订阅前缀（k6 的 SUB_PREFIX）与当前拉取上限
--   sudo /opt/pandora/deploy/psql.sh -v action=up   -f perf_settings.sql   每条订阅凭据每小时拉取上限放到 10000
--   sudo /opt/pandora/deploy/psql.sh -v action=down -f perf_settings.sql   恢复缺省（60）
-- 缺省 60/小时：k6 用 5000 个令牌按 50/s 拉一小时，每个令牌约 36 次，接近上限，所以压测时放宽。
-- 面板每次拉取都读库，改完不用重启。
\set ON_ERROR_STOP on
\if :{?action}
\else
  \echo '用法: psql -v action=show|up|down -f perf_settings.sql'
  \quit
\endif

SELECT :'action' = 'show' AS act_show,
       :'action' = 'up'   AS act_up,
       :'action' = 'down' AS act_down \gset

\if :act_show
SELECT
  (SELECT value #>> '{}' FROM settings WHERE key = 'subscription.path_prefix') AS sub_prefix,
  coalesce((SELECT value #>> '{}' FROM settings WHERE key = 'subscription.fetch_limit_per_hour'), '60（缺省）') AS fetch_limit_per_hour;
\elif :act_up
INSERT INTO settings (key, value)
VALUES ('subscription.fetch_limit_per_hour', '10000'::jsonb)
ON CONFLICT (key) DO UPDATE
  SET value = EXCLUDED.value,
      updated_at = now();
\elif :act_down
DELETE FROM settings
WHERE key = 'subscription.fetch_limit_per_hour';
\else
  \echo 'action 只能是 show、up、down'
\endif

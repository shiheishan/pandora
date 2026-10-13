-- +goose Up
-- node 模块：节点流量留档、指标、在线 IP 与小时汇总的保留期清理索引与库函数。
-- 依赖 00022_node_traffic.sql。

-- 保留期清理按时间从最早的一端扫；node_user_traffic_hourly 主键以 hour_start 开头，不再单独建索引。
CREATE INDEX node_traffic_reports_received_at_idx ON node_traffic_reports (received_at);
CREATE INDEX node_metrics_recorded_at_idx ON node_metrics (recorded_at);
CREATE INDEX node_alive_ips_last_seen_at_idx ON node_alive_ips (last_seen_at);
CREATE INDEX node_traffic_hourly_hour_start_idx ON node_traffic_hourly (hour_start);

-- node_traffic_reports、node_metrics 挂 deny_mutation，应用层不能直接 DELETE，清理走 SECURITY DEFINER 函数。
-- node_alive_ips 与两张小时表是 upsert 表，无追加写触发器，由 Go 直接分批 DELETE。

-- 保留期清理：删 p_before 之前的流量留档，每次最多 p_batch_limit 行，由定时任务循环调用。
-- 截止时间不晚于 31 天前：调用方传更晚的时间也只删 31 天以前的。
-- SECURITY DEFINER 以迁移角色执行；session_replication_role = replica 只在本函数执行期间让追加写触发器不触发。
-- +goose StatementBegin
CREATE FUNCTION purge_node_traffic_reports(p_before timestamptz, p_batch_limit int)
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

  DELETE FROM public.node_traffic_reports f
   USING (SELECT id
            FROM public.node_traffic_reports
           WHERE received_at < v_cutoff
           ORDER BY received_at
           LIMIT p_batch_limit) d
   WHERE f.id = d.id;
  GET DIAGNOSTICS v_deleted = ROW_COUNT;
  RETURN v_deleted;
END $$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION purge_node_traffic_reports(timestamptz, int) FROM PUBLIC;

-- 保留期清理：删 p_before 之前的节点指标，每次最多 p_batch_limit 行。
-- 主键为 (server_id, recorded_at)，按 recorded_at 排序分批删。
-- +goose StatementBegin
CREATE FUNCTION purge_node_metrics(p_before timestamptz, p_batch_limit int)
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

  DELETE FROM public.node_metrics f
   USING (SELECT server_id, recorded_at
            FROM public.node_metrics
           WHERE recorded_at < v_cutoff
           ORDER BY recorded_at
           LIMIT p_batch_limit) d
   WHERE f.server_id = d.server_id AND f.recorded_at = d.recorded_at;
  GET DIAGNOSTICS v_deleted = ROW_COUNT;
  RETURN v_deleted;
END $$;
-- +goose StatementEnd

-- 运行角色的 EXECUTE 由部署时的角色配置授予（运行角色不是本函数的属主）
REVOKE ALL ON FUNCTION purge_node_metrics(timestamptz, int) FROM PUBLIC;

-- +goose Down
DROP FUNCTION IF EXISTS purge_node_metrics(timestamptz, int);
DROP FUNCTION IF EXISTS purge_node_traffic_reports(timestamptz, int);
DROP INDEX IF EXISTS node_traffic_hourly_hour_start_idx;
DROP INDEX IF EXISTS node_alive_ips_last_seen_at_idx;
DROP INDEX IF EXISTS node_metrics_recorded_at_idx;
DROP INDEX IF EXISTS node_traffic_reports_received_at_idx;

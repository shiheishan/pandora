-- 节点探针指标（时序）。
--
-- 保留策略：只留 48 小时的原始点。探针数据的价值随时间衰减极快 ——
-- 一周前某分钟的 CPU 是 37% 还是 41% 对任何决策都没有影响，
-- 而按分钟保留会让这张表在几十个节点下迅速涨到千万行级别。
-- 需要长期趋势时应当另做小时聚合，而不是保留原始点。

-- +goose Up

-- +goose StatementBegin
CREATE TABLE node_metrics (
  tenant_id   uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  node_id     uuid NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
  recorded_at timestamptz NOT NULL DEFAULT now(),

  -- 百分比一律存 0–10000 的整数（万分比），避免浮点在聚合时累积误差
  cpu_bp      int CHECK (cpu_bp IS NULL OR cpu_bp BETWEEN 0 AND 10000),
  mem_used_mb int,
  mem_total_mb int,
  disk_used_gb int,
  disk_total_gb int,

  -- 系统负载，同样放大 100 倍存整数
  load1_cbp   int,
  load5_cbp   int,
  load15_cbp  int,

  -- 网络累计字节。速率由相邻两点算差值得出，不在这里存 ——
  -- 存速率的话，Agent 重启导致的计数器归零会变成一个虚假的巨大尖峰，
  -- 而存累计值时同样的情况只会让某一段差值为负，容易识别并丢弃。
  net_rx_bytes bigint,
  net_tx_bytes bigint,

  tcp_conns   int,
  uptime_sec  bigint,

  PRIMARY KEY (node_id, recorded_at)
);

-- 查最近 N 分钟是唯一的高频查询模式
CREATE INDEX idx_node_metrics_recent ON node_metrics (node_id, recorded_at DESC);
-- 清理任务按时间扫全表
CREATE INDEX idx_node_metrics_purge ON node_metrics (recorded_at);

SELECT app.enable_tenant_rls('node_metrics');
SELECT app.make_append_only('node_metrics');

COMMENT ON TABLE node_metrics IS
  '探针原始点，保留 48 小时。长期趋势需另做聚合，不要靠延长这里的保留期。';
-- +goose StatementEnd

-- +goose StatementBegin
-- 清理函数。幂等，可由定时任务反复调用。
CREATE OR REPLACE FUNCTION app.purge_node_metrics(p_keep_hours int DEFAULT 48)
RETURNS bigint LANGUAGE plpgsql AS $$
DECLARE
  v_deleted bigint;
BEGIN
  -- 追加写表禁了 DELETE，这里以定义者权限绕过 ——
  -- 保留期清理是设计的一部分，与「防止篡改历史」不冲突。
  ALTER TABLE node_metrics DISABLE TRIGGER trg_node_metrics_append_only;
  DELETE FROM node_metrics WHERE recorded_at < now() - make_interval(hours => p_keep_hours);
  GET DIAGNOSTICS v_deleted = ROW_COUNT;
  ALTER TABLE node_metrics ENABLE TRIGGER trg_node_metrics_append_only;
  RETURN v_deleted;
END $$;
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
DROP FUNCTION IF EXISTS app.purge_node_metrics(int);
DROP TABLE IF EXISTS node_metrics;
-- +goose StatementEnd

-- 用户流量按小时汇总（去掉节点维度），看板用户排行只读它。
--
-- 00099 之后看板用户排行读 node_user_traffic_hourly（节点 × uid × 小时），再按小时行逐行
-- 连订阅、按用户 count(DISTINCT 订阅) 分组：窗口内的行数 = 在线用户 × 小时 × 每人用过的节点数，
-- 每一行都要连一次订阅，最后还要为 DISTINCT 整体排序（5k 库副本 145–175ms）。
--
-- 现在入库时多写一张去掉节点维度的表：
--
--   uid_traffic_hourly   租户 × 小时 × 节点端 uid：该 uid 在这个小时里所有节点上报的严格口径
--                        有效上下行、正流量项数与最晚上报时刻。
--
-- 写法与 00099 相同：nodefabric 的 ReportTraffic 在写入上报的同一事务里、同一条语句里，把同一份
-- per_uid 结果同时累加进 node_user_traffic_hourly 与这张表，所以恒有
--   uid_traffic_hourly(t, h, uid) = node_user_traffic_hourly 按 (t, h, uid) 求和
-- （上下行与项数求和、最晚上报取最大）。口径、重复上报与小时切法全部继承 00099，不另起一套。
--
-- 归属同样在读时定：按 subscriptions.node_uid 连订阅。node_uid 全局唯一（00013 的唯一索引），
-- 一个 uid 至多对应一条订阅，所以按 uid 先聚合、再连订阅与按小时行逐行连订阅得到的用户排行完全一样。
--
-- 不挂 nodes 外键：节点删不掉（它的上报留档是追加写、外键 ON DELETE CASCADE，删节点会被留档的
-- 追加写保护拒绝），所以不会出现「节点表的行随节点删了、这张表还留着」的分叉。
--
-- 回填：从 node_user_traffic_hourly 按 (租户, 小时, uid) GROUP BY，保留期内（70 天）全量。
--   - 分批：按租户 × UTC 自然日一批（走主键 (tenant_id, hour_start, …) 的范围扫描），每批一条
--     INSERT … ON CONFLICT；每批与总计的行数、耗时用 RAISE NOTICE 打印（psql 可见），总计另用
--     RAISE LOG 写进数据库日志（goose 不转发 NOTICE）。
--   - 可重入：冲突时用重算值覆盖而不是累加，重跑任意次结果相同；整段在迁移事务里，失败即全部回滚。
--   - 发布是停写窗口（install.sh 先停三个网关再跑迁移），回填与入库不会交错。若在写入期间手工
--     重跑这一段，覆盖可能吞掉并发累加的增量——只在停写时跑。
--
-- 派生读数，不是证据：证据仍是 node_traffic_reports；保留期与 00099 同为 70 天
-- （nodefabric.TrafficRollupRetentionDays），由 aegis-admin 的保留期任务分批删除。
-- 不挂 zz_notify 触发器：写入频率就是上报频率（同 00072、00099 的理由）。

-- +goose Up

SET LOCAL lock_timeout = '5s';

-- +goose StatementBegin
-- 外键（tenants，ON DELETE CASCADE）在回填之后再加，见文件末尾
CREATE TABLE uid_traffic_hourly (
  tenant_id      uuid NOT NULL,
  hour_start     timestamptz NOT NULL
                   CHECK (extract(epoch FROM hour_start) % 3600 = 0),
  -- 节点端上报的 uid（subscriptions.node_uid），读时再连订阅
  node_uid       bigint NOT NULL,
  upload_bytes   numeric NOT NULL CHECK (upload_bytes >= 0),
  download_bytes numeric NOT NULL CHECK (download_bytes >= 0),
  -- 贡献了正流量的有效项数（各节点之和）
  entry_count    bigint NOT NULL CHECK (entry_count > 0),
  -- 各节点里最晚的一次上报
  last_report_at timestamptz NOT NULL,
  PRIMARY KEY (tenant_id, hour_start, node_uid)
);

SELECT app.enable_tenant_rls('uid_traffic_hourly');

-- 应用累加、读，并由 aegis-admin 的保留期任务按批删 70 天以前的桶；不清表
GRANT SELECT, INSERT, UPDATE, DELETE ON uid_traffic_hourly TO aegis_app;
REVOKE TRUNCATE ON uid_traffic_hourly FROM aegis_app;

COMMENT ON TABLE uid_traffic_hourly IS
  '用户流量按小时汇总（uid 级，去掉节点维度）。入库时与上报同事务累加，看板用户排行只读它。派生读数，证据在 node_traffic_reports。';
-- +goose StatementEnd

-- 回填（PG18 测试按下面两行标记截取这一段，在回滚的事务里重跑，与按节点的小时表逐行比对）。
-- uid-backfill:begin
-- +goose StatementBegin
DO $$
DECLARE
  v_from    timestamptz := date_trunc('hour', now() - interval '70 days', 'UTC');
  v_until   timestamptz := date_trunc('hour', now(), 'UTC') + interval '1 hour';
  v_start   timestamptz := clock_timestamp();
  v_tenant  uuid;
  v_day     timestamptz;
  v_batch   timestamptz;
  v_rows    bigint;
  v_total   bigint := 0;
  v_batches int := 0;
BEGIN
  FOR v_tenant IN SELECT id FROM tenants ORDER BY id LOOP
    v_day := date_trunc('day', v_from, 'UTC');
    WHILE v_day < v_until LOOP
      v_batch := clock_timestamp();
      INSERT INTO uid_traffic_hourly AS g
        (tenant_id, hour_start, node_uid, upload_bytes, download_bytes, entry_count, last_report_at)
      SELECT t.tenant_id, t.hour_start, t.node_uid,
             sum(t.upload_bytes), sum(t.download_bytes), sum(t.entry_count), max(t.last_report_at)
        FROM node_user_traffic_hourly t
       WHERE t.tenant_id = v_tenant
         AND t.hour_start >= greatest(v_day, v_from)
         AND t.hour_start < v_day + interval '1 day'
       GROUP BY t.tenant_id, t.hour_start, t.node_uid
      ON CONFLICT (tenant_id, hour_start, node_uid) DO UPDATE SET
        upload_bytes   = EXCLUDED.upload_bytes,
        download_bytes = EXCLUDED.download_bytes,
        entry_count    = EXCLUDED.entry_count,
        last_report_at = EXCLUDED.last_report_at;
      GET DIAGNOSTICS v_rows = ROW_COUNT;
      v_total := v_total + v_rows;
      v_batches := v_batches + 1;
      IF v_rows > 0 THEN
        RAISE NOTICE 'uid_traffic_hourly 回填 租户 % 日 %：% 行，% ms', v_tenant,
          to_char(v_day AT TIME ZONE 'UTC', 'YYYY-MM-DD'), v_rows,
          round(extract(epoch FROM clock_timestamp() - v_batch) * 1000, 1);
      END IF;
      v_day := v_day + interval '1 day';
    END LOOP;
  END LOOP;
  RAISE NOTICE 'uid_traffic_hourly 回填完成：% 批，% 行，% ms', v_batches, v_total,
    round(extract(epoch FROM clock_timestamp() - v_start) * 1000, 1);
  RAISE LOG 'uid_traffic_hourly 回填完成：% 批，% 行，% ms', v_batches, v_total,
    round(extract(epoch FROM clock_timestamp() - v_start) * 1000, 1);
END $$;
-- +goose StatementEnd
-- uid-backfill:end

-- +goose StatementBegin
ALTER TABLE uid_traffic_hourly
  ADD CONSTRAINT uid_traffic_hourly_tenant_id_fkey
    FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
-- 派生表，删掉不丢证据；本迁移之前的代码不读也不写它
DROP TABLE IF EXISTS uid_traffic_hourly;
-- +goose StatementEnd

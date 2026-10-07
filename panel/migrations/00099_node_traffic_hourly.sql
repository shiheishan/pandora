-- 节点流量按小时汇总（看板流量排行、后台节点列表的 24h / 30 天流量）。
--
-- 原来这两个读路径每次请求都回到上报留档 node_traffic_reports：看板对窗口内每条
-- 上报的 raw_payload 做 jsonb_each + 正则校验归一（5k 用户、约 25 分钟的上报就要
-- 6.9s / 3.2s，生产一天约 28.8 万条上报，同一查询再放大约 60 倍），节点列表对
-- 30 天的上报做 sum。读的代价随留档线性增长，而留档是追加写、永不删除的证据。
--
-- 现在改成入库时汇总：nodefabric 的 ReportTraffic 在写入上报的同一个事务里，
-- 按与看板完全相同的校验口径把这一份上报累加进两张小时表，读路径只读小时表。
--
--   node_traffic_hourly        节点 × 小时：上报数、重复上报数、非法上报数、非法项数、
--                              严格口径的有效上下行与正流量项数 / 上报数，以及节点列表
--                              用的 Go 口径原始合计（total_upload + total_download）。
--   node_user_traffic_hourly   节点 × 节点端 uid × 小时：只存严格口径下上下行合计 > 0
--                              的有效项（零流量项对任何读数都没有贡献）。
--
-- 归属不在入库时定：小时表存的是节点端上报的 uid，读的时候再按 subscriptions.node_uid
-- 连订阅，与原来「读时 LEFT JOIN 订阅」的口径一致（订阅后来被删，它的流量就变成未归属）。
--
-- 校验口径只有一个出处：app.node_traffic_payload_entries，逐字搬自原看板的
-- strict_entries CTE（键必须是 [+-]?数字、去前导零后不超过 19 位且落在 int64 内；
-- 值必须是恰好两个元素的数组，两个元素都是 JSON 数字、是整数、落在 [0, int64 最大值]）。
-- 入库与下面的回填都只经它分类，PG18 测试拿原看板 SQL 对同一批上报逐项比对。
--
-- 幂等与时间口径：
--   - 小时桶按服务器收到的时刻 received_at（UTC 整点）切，不看节点端时间；迟到的上报
--     落在它被收到的那个小时，与原来按 received_at 开窗一致。
--   - 被判为重试的重复上报（duplicate_of 非空）只计入 duplicate_report_count，
--     不贡献流量与质量计数，与原来 duplicate_of IS NULL 才展开一致。
--   - 汇总与上报行在同一个事务里写：事务回滚两边都不留，提交两边都在；没有别的路径
--     会再把同一行上报加一次，节点重放同一报文得到的是一条新的上报行（10 秒内则是重复）。
--
-- 回填分两档，迁移之后的上报全部由入库路径汇总：
--   - 节点列表要的原始合计（report_count、raw_bytes，取上报行上的 total_* 列，不解析报文）
--     回填近 31 天（按整点对齐）：节点列表读近 30 天。
--   - 看板要的严格口径（解析报文：重复 / 非法计数、有效上下行、正流量项与节点×uid 表）
--     只回填近 48 小时（按整点对齐）：看板界面固定读近 24 小时（整点对齐后至多 25 个桶），
--     留一倍余量。解析是回填里唯一贵的部分（生产 200 节点每分钟一报约 28.8 万份 / 天，
--     按 62 天全量解析在 2 核机器上是小时级），停写窗口里不值得为界面不读的数据付这个代价。
--     代价写明：API 允许的 7d / 30d 区间（界面不发）在上线后的头 7 / 30 天里，上线 48 小时
--     以前的那部分不计；过了这段时间就完全由入库路径覆盖。
-- 发布是停写窗口（install.sh 先停三个网关再跑迁移），回填与入库不会交错。
-- 外键在回填之后再加：逐行的外键触发器在批量写入时比回填本身还贵，ADD CONSTRAINT 一次校验。
--
-- 两张表是派生读数，不是证据：证据仍是 node_traffic_reports，汇总丢了可以从它重算。
-- 保留 70 天（nodefabric.TrafficRollupRetentionDays，比读路径最远的 61 天多留余量），
-- 由保留期任务分批删除；要更长的趋势另做按日汇总，不要靠延长这里的保留期。
-- 不挂 zz_notify 触发器：写入频率就是上报频率（同 00072 的理由）。

-- +goose Up

SET LOCAL lock_timeout = '5s';

-- +goose StatementBegin
-- 一份上报报文按原看板口径拆成逐项分类结果。
-- 不是 STRICT、不带 SET、IMMUTABLE 的单条 SELECT：规划器会把它内联进调用方的查询。
-- 输出：entry_valid 为真时 entry_uid / entry_upload / entry_download 有值（上下行已 trunc），
-- 否则三者为 NULL。根不是对象时没有任何项（调用方另按 jsonb_typeof 判非法上报）。
--
-- 表达式与原 strict_entries 逐条相同，只是每一步单独一层子查询并加 OFFSET 0：不加的话
-- 规划器会把各层拉平，每引用一次上一步的列就把它的整棵表达式（含正则）抄一遍，
-- 一项要算上百次正则（5k 库回填 1.9 万份上报要 83s + 109s）；分层后每项每步只算一次
-- （同样的数据量级比旧看板的解析还快）。改这里时保持「一步一层、OFFSET 0」。
CREATE FUNCTION app.node_traffic_payload_entries(p_payload jsonb)
RETURNS TABLE (entry_uid bigint, entry_upload numeric, entry_download numeric, entry_valid boolean)
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
SELECT CASE WHEN v.is_valid THEN v.parsed_uid_numeric::bigint END,
       CASE WHEN v.is_valid THEN trunc(v.upload_numeric) END,
       CASE WHEN v.is_valid THEN trunc(v.download_numeric) END,
       v.is_valid
  FROM (
    -- 判定（numbers 之后的 is_valid）
    SELECT p.parsed_uid_numeric, p.upload_numeric, p.download_numeric,
           coalesce(
             p.parsed_uid_numeric IS NOT NULL AND p.shape_ok IS TRUE
             AND p.upload_numeric=trunc(p.upload_numeric)
             AND p.download_numeric=trunc(p.download_numeric)
             AND p.upload_numeric BETWEEN 0 AND 9223372036854775807::numeric
             AND p.download_numeric BETWEEN 0 AND 9223372036854775807::numeric,
             false
           ) AS is_valid
      FROM (
        -- parsed 与 numbers
        SELECT CASE WHEN b.bounded_key_text IS NOT NULL
                         AND b.bounded_key_text::numeric BETWEEN
                             -9223372036854775808::numeric AND 9223372036854775807::numeric
                    THEN b.bounded_key_text::numeric END AS parsed_uid_numeric,
               b.shape_ok,
               CASE WHEN jsonb_typeof(b.upload_json)='number'
                    THEN (b.upload_json #>> '{}')::numeric END AS upload_numeric,
               CASE WHEN jsonb_typeof(b.download_json)='number'
                    THEN (b.download_json #>> '{}')::numeric END AS download_numeric
          FROM (
            -- key_bound 与 components
            SELECT CASE WHEN k.key_syntax_ok IS TRUE
                             AND length(ltrim(k.normalized_key_text,'-')) <= 19
                        THEN k.normalized_key_text END AS bounded_key_text,
                   k.array_len=2 AS shape_ok,
                   CASE WHEN k.array_len=2 THEN k.value_json->0 END AS upload_json,
                   CASE WHEN k.array_len=2 THEN k.value_json->1 END AS download_json
              FROM (
                -- key_lex 的归一（只在语法通过时做）
                SELECT s.key_syntax_ok,
                       CASE WHEN s.key_syntax_ok THEN
                         CASE WHEN left(s.key_text,1)='-' THEN '-' ELSE '' END ||
                         coalesce(nullif(regexp_replace(ltrim(s.key_text,'+-'), '^0+', ''), ''), '0')
                       END AS normalized_key_text,
                       s.value_json, s.array_len
                  FROM (
                    -- key_lex 的语法与 value_shape
                    SELECT entry.key_text, entry.value_json,
                           entry.key_text ~ '^[+-]?[0-9]+$' AS key_syntax_ok,
                           CASE WHEN jsonb_typeof(entry.value_json)='array'
                                THEN jsonb_array_length(entry.value_json) END AS array_len
                      FROM jsonb_each(
                             CASE WHEN jsonb_typeof(p_payload)='object' THEN p_payload ELSE '{}'::jsonb END
                           ) entry(key_text,value_json)
                    OFFSET 0) s
                OFFSET 0) k
            OFFSET 0) b
        OFFSET 0) p
    OFFSET 0) v
$$;

COMMENT ON FUNCTION app.node_traffic_payload_entries(jsonb) IS
  '节点流量上报报文的严格分类（原看板 strict_entries 口径），入库汇总与回填共用的唯一出处。';
GRANT EXECUTE ON FUNCTION app.node_traffic_payload_entries(jsonb) TO aegis_app;
-- +goose StatementEnd

-- +goose StatementBegin
-- 外键（tenants / nodes，ON DELETE CASCADE）在回填之后再加，见文件末尾
CREATE TABLE node_traffic_hourly (
  tenant_id               uuid NOT NULL,
  hour_start              timestamptz NOT NULL
                            CHECK (extract(epoch FROM hour_start) % 3600 = 0),
  node_id                 uuid NOT NULL,
  -- 非重复上报数；重复上报只进 duplicate_report_count
  report_count            bigint NOT NULL DEFAULT 0 CHECK (report_count >= 0),
  duplicate_report_count  bigint NOT NULL DEFAULT 0 CHECK (duplicate_report_count >= 0),
  -- 根不是对象、或含至少一个非法项的非重复上报数
  invalid_report_count    bigint NOT NULL DEFAULT 0 CHECK (invalid_report_count >= 0),
  invalid_entry_count     bigint NOT NULL DEFAULT 0 CHECK (invalid_entry_count >= 0),
  -- 节点列表口径：非重复上报的 total_upload + total_download（Go 端合计，未乘倍率）
  raw_bytes               numeric NOT NULL DEFAULT 0,
  -- 看板口径：严格有效项的上下行合计
  upload_bytes            numeric NOT NULL DEFAULT 0 CHECK (upload_bytes >= 0),
  download_bytes          numeric NOT NULL DEFAULT 0 CHECK (download_bytes >= 0),
  -- 上下行合计 > 0 的有效项数、含这种项的上报数、这种上报最晚的收到时刻
  positive_entry_count    bigint NOT NULL DEFAULT 0 CHECK (positive_entry_count >= 0),
  positive_report_count   bigint NOT NULL DEFAULT 0 CHECK (positive_report_count >= 0),
  last_positive_report_at timestamptz,
  PRIMARY KEY (tenant_id, hour_start, node_id)
);

CREATE TABLE node_user_traffic_hourly (
  tenant_id      uuid NOT NULL,
  hour_start     timestamptz NOT NULL
                   CHECK (extract(epoch FROM hour_start) % 3600 = 0),
  node_id        uuid NOT NULL,
  -- 节点端上报的 uid（subscriptions.node_uid），读时再连订阅
  node_uid       bigint NOT NULL,
  upload_bytes   numeric NOT NULL CHECK (upload_bytes >= 0),
  download_bytes numeric NOT NULL CHECK (download_bytes >= 0),
  -- 贡献了正流量的有效项数（同一份上报里 "7" 与 "007" 各算一项）
  entry_count    bigint NOT NULL CHECK (entry_count > 0),
  last_report_at timestamptz NOT NULL,
  PRIMARY KEY (tenant_id, hour_start, node_id, node_uid)
);

SELECT app.enable_tenant_rls('node_traffic_hourly');
SELECT app.enable_tenant_rls('node_user_traffic_hourly');

-- 应用累加、读，并由 aegis-admin 的保留期任务按批删 70 天以前的桶（读路径最远 61 天）；
-- 不清表
GRANT SELECT, INSERT, UPDATE, DELETE ON node_traffic_hourly, node_user_traffic_hourly TO aegis_app;
REVOKE TRUNCATE ON node_traffic_hourly, node_user_traffic_hourly FROM aegis_app;

COMMENT ON TABLE node_traffic_hourly IS
  '节点流量按小时汇总（节点级）。入库时与上报同事务累加，看板与节点列表只读它。派生读数，证据在 node_traffic_reports。';
COMMENT ON TABLE node_user_traffic_hourly IS
  '节点流量按小时汇总（节点 × uid），只存严格口径下有流量的有效项；归属在读时按 subscriptions.node_uid 连。';
-- +goose StatementEnd

-- 回填（PG18 测试按下面两行标记截取这一段，在回滚的事务里重跑，与原看板口径比对）。
-- 两档窗口见文件头：严格口径近 48 小时、原始合计近 31 天，都按 UTC 整点对齐。
-- rollup-backfill:begin
-- +goose StatementBegin
INSERT INTO node_user_traffic_hourly
  (tenant_id, hour_start, node_id, node_uid, upload_bytes, download_bytes, entry_count, last_report_at)
SELECT r.tenant_id, date_trunc('hour', r.received_at, 'UTC'), r.node_id, e.entry_uid,
       sum(e.entry_upload), sum(e.entry_download), count(*), max(r.received_at)
  FROM node_traffic_reports r
  CROSS JOIN LATERAL app.node_traffic_payload_entries(r.raw_payload) e
 WHERE r.received_at >= date_trunc('hour', now() - interval '48 hours', 'UTC')
   AND r.duplicate_of IS NULL
   AND e.entry_valid AND e.entry_upload + e.entry_download > 0
 GROUP BY 1, 2, 3, 4;

INSERT INTO node_traffic_hourly
  (tenant_id, hour_start, node_id, report_count, duplicate_report_count,
   invalid_report_count, invalid_entry_count, raw_bytes, upload_bytes, download_bytes,
   positive_entry_count, positive_report_count, last_positive_report_at)
SELECT r.tenant_id, date_trunc('hour', r.received_at, 'UTC'), r.node_id,
       count(*) FILTER (WHERE NOT r.is_dup),
       count(*) FILTER (WHERE r.is_dup AND r.strict),
       count(*) FILTER (WHERE r.strict AND NOT r.is_dup AND (NOT r.root_is_object OR q.invalid_entries > 0)),
       coalesce(sum(q.invalid_entries), 0),
       coalesce(sum(r.raw_bytes) FILTER (WHERE NOT r.is_dup), 0),
       coalesce(sum(q.upload_bytes), 0),
       coalesce(sum(q.download_bytes), 0),
       coalesce(sum(q.positive_entries), 0),
       count(*) FILTER (WHERE q.positive_entries > 0),
       max(r.received_at) FILTER (WHERE q.positive_entries > 0)
  FROM (SELECT tenant_id, node_id, received_at, raw_payload,
               duplicate_of IS NOT NULL AS is_dup,
               received_at >= date_trunc('hour', now() - interval '48 hours', 'UTC') AS strict,
               -- 只对严格档读报文（档外的行不解 TOAST）
               CASE WHEN received_at >= date_trunc('hour', now() - interval '48 hours', 'UTC')
                    THEN jsonb_typeof(raw_payload)='object' END AS root_is_object,
               total_upload::numeric + total_download::numeric AS raw_bytes
          FROM node_traffic_reports
         WHERE received_at >= date_trunc('hour', now() - interval '31 days', 'UTC')) r
  CROSS JOIN LATERAL (
    -- 重复上报与严格档外的上报不展开：按空对象分类，各计数为 0
    SELECT count(*) FILTER (WHERE e.entry_valid IS NOT TRUE) AS invalid_entries,
           sum(e.entry_upload) FILTER (WHERE e.entry_valid) AS upload_bytes,
           sum(e.entry_download) FILTER (WHERE e.entry_valid) AS download_bytes,
           count(*) FILTER (WHERE e.entry_valid AND e.entry_upload + e.entry_download > 0) AS positive_entries
      FROM app.node_traffic_payload_entries(
             CASE WHEN r.is_dup OR NOT r.strict THEN '{}'::jsonb ELSE r.raw_payload END) e
  ) q
 GROUP BY 1, 2, 3;
-- +goose StatementEnd
-- rollup-backfill:end

-- +goose StatementBegin
ALTER TABLE node_traffic_hourly
  ADD CONSTRAINT node_traffic_hourly_tenant_id_fkey
    FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE,
  ADD CONSTRAINT node_traffic_hourly_node_id_fkey
    FOREIGN KEY (node_id) REFERENCES nodes(id) ON DELETE CASCADE;
ALTER TABLE node_user_traffic_hourly
  ADD CONSTRAINT node_user_traffic_hourly_tenant_id_fkey
    FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE,
  ADD CONSTRAINT node_user_traffic_hourly_node_id_fkey
    FOREIGN KEY (node_id) REFERENCES nodes(id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
DROP TABLE IF EXISTS node_user_traffic_hourly;
DROP TABLE IF EXISTS node_traffic_hourly;
DROP FUNCTION IF EXISTS app.node_traffic_payload_entries(jsonb);
-- +goose StatementEnd

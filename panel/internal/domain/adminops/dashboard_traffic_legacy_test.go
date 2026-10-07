package adminops

// 原看板流量 SQL（f364a62 的 dashboard.go 原文，只改了常量名），只作 PG18 对照：
// 看板改读小时汇总（00099）之后，同一批上报用它直接展开 raw_payload 算出的结果，
// 必须与新读法逐项相同（dashboard_traffic_pg18_test.go）。不要「顺手」改它——
// 它的价值就在于是旧口径的原文。

const legacyDashboardTrafficClassificationCTE = `
duplicate_quality AS (
  SELECT count(*)::bigint AS duplicate_report_count
    FROM node_traffic_reports
   WHERE tenant_id=$1 AND received_at >= $2 AND received_at < $3
     AND duplicate_of IS NOT NULL
),
report_shape AS MATERIALIZED (
  SELECT id AS report_id, tenant_id, node_id, received_at, raw_payload,
         jsonb_typeof(raw_payload)='object' AS root_is_object
    FROM node_traffic_reports
   WHERE tenant_id=$1 AND received_at >= $2 AND received_at < $3
     AND duplicate_of IS NULL
),
strict_entries AS MATERIALIZED (
  SELECT r.report_id, r.tenant_id, r.node_id, r.received_at,
         parsed.parsed_uid_numeric, numbers.upload_numeric, numbers.download_numeric,
         coalesce(
           parsed.parsed_uid_numeric IS NOT NULL AND components.shape_ok IS TRUE
           AND numbers.upload_numeric=trunc(numbers.upload_numeric)
           AND numbers.download_numeric=trunc(numbers.download_numeric)
           AND numbers.upload_numeric BETWEEN 0 AND 9223372036854775807::numeric
           AND numbers.download_numeric BETWEEN 0 AND 9223372036854775807::numeric,
           false
         ) AS is_valid
    FROM report_shape r
    CROSS JOIN LATERAL jsonb_each(
      CASE WHEN r.root_is_object IS TRUE THEN r.raw_payload ELSE '{}'::jsonb END
    ) entry(key_text,value_json)
    CROSS JOIN LATERAL (
      SELECT entry.key_text ~ '^[+-]?[0-9]+$' AS key_syntax_ok,
             CASE WHEN entry.key_text ~ '^[+-]?[0-9]+$' THEN
               CASE WHEN left(entry.key_text,1)='-' THEN '-' ELSE '' END ||
               coalesce(nullif(regexp_replace(ltrim(entry.key_text,'+-'), '^0+', ''), ''), '0')
             END AS normalized_key_text
    ) key_lex
    CROSS JOIN LATERAL (
      SELECT CASE WHEN key_lex.key_syntax_ok IS TRUE
                       AND length(ltrim(key_lex.normalized_key_text,'-')) <= 19
                  THEN key_lex.normalized_key_text END AS bounded_key_text
    ) key_bound
    CROSS JOIN LATERAL (
      SELECT CASE WHEN key_bound.bounded_key_text IS NOT NULL
                       AND key_bound.bounded_key_text::numeric BETWEEN
                           -9223372036854775808::numeric AND 9223372036854775807::numeric
                  THEN key_bound.bounded_key_text::numeric END AS parsed_uid_numeric
    ) parsed
    CROSS JOIN LATERAL (
      SELECT CASE WHEN jsonb_typeof(entry.value_json)='array'
                  THEN jsonb_array_length(entry.value_json) END AS array_len
    ) value_shape
    CROSS JOIN LATERAL (
      SELECT value_shape.array_len=2 AS shape_ok,
             CASE WHEN value_shape.array_len=2 THEN entry.value_json->0 END AS upload_json,
             CASE WHEN value_shape.array_len=2 THEN entry.value_json->1 END AS download_json
    ) components
    CROSS JOIN LATERAL (
      SELECT CASE WHEN jsonb_typeof(components.upload_json)='number'
                  THEN (components.upload_json #>> '{}')::numeric END AS upload_numeric,
             CASE WHEN jsonb_typeof(components.download_json)='number'
                  THEN (components.download_json #>> '{}')::numeric END AS download_numeric
    ) numbers
),
entry_report_quality AS (
  SELECT report_id,
         count(*) FILTER (WHERE is_valid IS NOT TRUE)::bigint AS invalid_entry_count,
         bool_or(is_valid IS NOT TRUE) AS has_invalid_entry
    FROM strict_entries GROUP BY report_id
),
report_quality AS (
  SELECT count(*) FILTER (
           WHERE r.root_is_object IS NOT TRUE
              OR coalesce(eq.has_invalid_entry,false)
         )::bigint AS invalid_report_count,
         coalesce(sum(eq.invalid_entry_count),0)::bigint AS invalid_entry_count
    FROM report_shape r
    LEFT JOIN entry_report_quality eq ON eq.report_id=r.report_id
),
attributed AS MATERIALIZED (
  SELECT s.report_id, s.tenant_id, s.node_id, s.received_at,
         sub.id AS subscription_id, sub.user_id,
         trunc(s.upload_numeric) AS valid_upload,
         trunc(s.download_numeric) AS valid_download
    FROM strict_entries s
    LEFT JOIN subscriptions sub
      ON sub.tenant_id=s.tenant_id
     AND sub.node_uid=s.parsed_uid_numeric::bigint
   WHERE s.is_valid IS TRUE
),
traffic_totals AS (
  SELECT coalesce(sum(valid_upload+valid_download),0)::numeric AS reported_bytes,
         coalesce(sum(valid_upload+valid_download) FILTER (WHERE subscription_id IS NOT NULL AND user_id IS NOT NULL),0)::numeric AS attributed_bytes
    FROM attributed
)
`

const legacyDashboardNodeTrafficSQL = `WITH ` + legacyDashboardTrafficClassificationCTE + `,
node_groups AS (
  SELECT a.node_id, n.name, n.display_name,
         sum(a.valid_upload)::numeric AS upload_bytes,
         sum(a.valid_download)::numeric AS download_bytes,
         sum(a.valid_upload+a.valid_download)::numeric AS total_bytes,
         count(*)::bigint AS contributing_entry_count,
         count(DISTINCT a.report_id)::bigint AS report_count,
         max(a.received_at)::timestamptz(6) AS last_report_at
    FROM attributed a
    JOIN nodes n ON n.tenant_id=a.tenant_id AND n.id=a.node_id
   WHERE a.valid_upload+a.valid_download > 0
   GROUP BY a.node_id,n.name,n.display_name
),
ranked AS MATERIALIZED (
  SELECT * FROM node_groups ORDER BY total_bytes DESC,node_id ASC LIMIT $4
),
ranking AS (
  SELECT coalesce(sum(total_bytes),0)::numeric AS returned_bytes FROM ranked
)
SELECT coalesce((
         SELECT jsonb_agg(jsonb_build_object(
           'node_id',node_id::text,'name',name,'display_name',display_name,
           'upload_bytes',trim_scale(upload_bytes)::text,'download_bytes',trim_scale(download_bytes)::text,
           'total_bytes',trim_scale(total_bytes)::text,'contributing_entry_count',contributing_entry_count,
           'report_count',report_count,
           'last_report_at',to_char(last_report_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"')
         ) ORDER BY total_bytes DESC,node_id ASC) FROM ranked
       ),'[]'::jsonb),
       trim_scale(t.reported_bytes)::text,trim_scale(t.attributed_bytes)::text,
       trim_scale(t.reported_bytes-t.attributed_bytes)::text,
       trim_scale(r.returned_bytes)::text,trim_scale(t.reported_bytes-r.returned_bytes)::text,
       d.duplicate_report_count,q.invalid_report_count,q.invalid_entry_count
  FROM traffic_totals t CROSS JOIN ranking r
  CROSS JOIN duplicate_quality d CROSS JOIN report_quality q`

const legacyDashboardUserTrafficSQL = `WITH ` + legacyDashboardTrafficClassificationCTE + `,
user_groups AS (
  SELECT a.user_id,u.email::text AS email,
         sum(a.valid_upload)::numeric AS upload_bytes,
         sum(a.valid_download)::numeric AS download_bytes,
         sum(a.valid_upload+a.valid_download)::numeric AS total_bytes,
         count(DISTINCT a.subscription_id)::bigint AS subscription_count,
         count(*)::bigint AS contributing_entry_count,
         max(a.received_at)::timestamptz(6) AS last_report_at
    FROM attributed a
    JOIN users u ON u.tenant_id=a.tenant_id AND u.id=a.user_id
   WHERE a.subscription_id IS NOT NULL
     AND a.user_id IS NOT NULL AND a.valid_upload+a.valid_download > 0
   GROUP BY a.user_id,u.email
),
ranked AS MATERIALIZED (
  SELECT * FROM user_groups ORDER BY total_bytes DESC,user_id ASC LIMIT $4
),
ranking AS (
  SELECT coalesce(sum(total_bytes),0)::numeric AS returned_bytes FROM ranked
)
SELECT coalesce((
         SELECT jsonb_agg(jsonb_build_object(
           'user_id',user_id::text,
           'email_masked',CASE WHEN email ~ '^[^@]+@[^@]+$'
             THEN left(email,1)||'***@'||lower(split_part(email,'@',2)) ELSE '***' END,
           'upload_bytes',trim_scale(upload_bytes)::text,'download_bytes',trim_scale(download_bytes)::text,
           'total_bytes',trim_scale(total_bytes)::text,'subscription_count',subscription_count,
           'contributing_entry_count',contributing_entry_count,
           'last_report_at',to_char(last_report_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"')
         ) ORDER BY total_bytes DESC,user_id ASC) FROM ranked
       ),'[]'::jsonb),
       trim_scale(t.reported_bytes)::text,trim_scale(t.attributed_bytes)::text,
       trim_scale(t.reported_bytes-t.attributed_bytes)::text,
       trim_scale(r.returned_bytes)::text,trim_scale(t.attributed_bytes-r.returned_bytes)::text,
       d.duplicate_report_count,q.invalid_report_count,q.invalid_entry_count
  FROM traffic_totals t CROSS JOIN ranking r
  CROSS JOIN duplicate_quality d CROSS JOIN report_quality q`

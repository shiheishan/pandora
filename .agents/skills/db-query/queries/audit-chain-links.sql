-- 用途：审计哈希链的快速体检：行数、第一版/第二版行数、链序号是否连续、prev_hash 是否接得上上一条。
-- 身份：aegis_app + 租户（audit_events 受 RLS，链按租户各一条）。
-- 变量：tenant、timeout（全表窗口排序，行多时调到 60s）。
-- 边界：这里不重算 entry_hash。完整校验只有 Go 函数 audit.VerifyChain(ctx, tx, tenantID)，
-- 没有命令行入口（只有 platform/audit/chain_pg18_test.go 与 api/admin/step4_pg18_test.go 在调）；
-- 本查询能抓到的是 link（prev_hash 不等于上一条的 entry_hash）和 seq（chain_seq 不连续）两类断点。
-- 排序与 VerifyChain 相同：chain_seq 为空的第一版行在前（按 occurred_at, id），第二版行按 chain_seq。
\if :{?tenant}
\else
  \set tenant '00000000-0000-7000-8000-000000000001'
\endif
\if :{?timeout}
\else
  \set timeout 15s
\endif
BEGIN READ ONLY;
SET LOCAL ROLE aegis_app;
SET LOCAL search_path TO pg_catalog, public, pg_temp;
SET LOCAL jit = off;
SET LOCAL statement_timeout = :'timeout';
SELECT set_config('app.tenant_id', :'tenant', true) AS tenant_set \gset

\echo == 1 行数与链序号范围（max_seq 应等于 v2_rows）
SELECT count(*) AS total_rows,
       count(*) FILTER (WHERE chain_seq IS NULL) AS v1_rows,
       count(*) FILTER (WHERE chain_seq IS NOT NULL) AS v2_rows,
       max(chain_seq) AS max_seq,
       min(occurred_at) AS first_at, max(occurred_at) AS last_at
  FROM audit_events
 WHERE tenant_id = :'tenant';

\echo == 2 断点（空 = 链接与序号都完整；link = prev_hash 接不上，seq = 序号不连续），至多 20 行
WITH ordered AS (
  SELECT id, chain_seq, occurred_at, action, prev_hash,
         lag(entry_hash) OVER w AS expected_prev,
         row_number() OVER w AS pos,
         count(*) FILTER (WHERE chain_seq IS NULL) OVER (PARTITION BY tenant_id) AS v1_rows
    FROM audit_events
   WHERE tenant_id = :'tenant'
  WINDOW w AS (PARTITION BY tenant_id ORDER BY chain_seq NULLS FIRST, occurred_at, id)
)
SELECT id, chain_seq, occurred_at, action,
       CASE WHEN coalesce(prev_hash, ''::bytea) IS DISTINCT FROM coalesce(expected_prev, ''::bytea) THEN 'link'
            ELSE 'seq' END AS reason
  FROM ordered
 WHERE coalesce(prev_hash, ''::bytea) IS DISTINCT FROM coalesce(expected_prev, ''::bytea)
    OR (chain_seq IS NOT NULL AND chain_seq <> pos - v1_rows)
 ORDER BY pos
 LIMIT 20;

ROLLBACK;

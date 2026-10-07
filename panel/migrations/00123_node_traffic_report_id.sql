-- 流量上报按 (节点, X-Report-Id) 幂等（w4deliver，审计 ledger N5）。
--
-- pdnd 给每份流量上报一个编号（请求头 X-Report-Id），结果不确定（传输错误、5xx、408、429）
-- 时原样重发同一份、带同一个编号。原先面板只有「同节点 10 秒内内容相同」的近似去重：
-- 重发晚于 10 秒就再扣一次费；两份相同报文并发到达时两边都看不见对方，也都扣费。
--
-- client_report_id 记节点给的编号（只收 1–64 个 [A-Za-z0-9._:-]，Go 侧校验，CHECK 兜长度），
-- 只写在第一份（入账的那份）上。唯一部分索引 (node_id, client_report_id)：同一编号再来时
-- INSERT … ON CONFLICT DO NOTHING 等前一份提交后判为冲突，改记一行重复件（编号列留空，
-- duplicate_of 指向第一份，只留档、不扣费）。并发的两份由索引串行，不再双扣。
-- 重复件不带编号，所以以后清理留档时（删掉第一份、duplicate_of 置空）不会撞这条唯一索引。
--
-- 编号和留档在同一张表、同一行：保留期与 node_traffic_reports 天然一致（用户定：原始
-- 数据 31 天），清理留档时编号一起走，不需要另一张表另一个清理任务。
-- 不带编号的老节点照旧走 10 秒内容哈希去重。

-- +goose Up
SET LOCAL lock_timeout = '5s';

ALTER TABLE public.node_traffic_reports
  ADD COLUMN client_report_id text
    CONSTRAINT node_traffic_reports_client_report_id_check
    CHECK (client_report_id IS NULL OR octet_length(client_report_id) BETWEEN 1 AND 64);

CREATE UNIQUE INDEX node_traffic_reports_client_report_unique
  ON public.node_traffic_reports (node_id, client_report_id)
  WHERE client_report_id IS NOT NULL;

-- +goose Down
SET LOCAL lock_timeout = '5s';

DROP INDEX IF EXISTS public.node_traffic_reports_client_report_unique;

ALTER TABLE public.node_traffic_reports DROP COLUMN IF EXISTS client_report_id;

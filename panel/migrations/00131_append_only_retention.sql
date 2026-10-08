-- 追加写表的 31 天保留期（用户定：原始数据 31 天、汇总 400 天，每天清理；审计 ledger 第三节第 2 条）。
--
-- 一、app.purge_node_traffic_reports(p_keep_days, p_batch)：新增
--
-- 流量上报留档按 5k 用户、200 节点约 28.8 万行 / 天，从没清理过（一年约 80GB）。照 00100 的
-- app.purge_node_metrics 写法：
--   - SECURITY DEFINER + 固定 search_path，属主是跑迁移的特权角色；
--   - 函数级 session_replication_role = replica：只在本函数执行期间绕过追加写触发器，
--     其它任何语句照旧被拦；不用 DISABLE TRIGGER（要表属主、DDL 锁会挡住并发上报）；
--   - 只删当前租户（app.current_tenant_id()）的行：定义者绕过 RLS，租户边界由函数自己守；
--   - 保留期下限 31 天，低于即拒绝：调用方不能借它删近期证据；
--   - 每次最多删 p_batch 行（1–50000），调用方循环，事务短、锁少。
-- 取行按节点走 (node_id, received_at) 索引（每个节点取最早的一批，再总共取 p_batch 行）：
-- 00132 删掉了 00041 那两条按 (tenant_id, received_at) 的部分索引，这张表上不再有以租户
-- 开头的时间索引；节点数是几百的量级，逐节点探一次索引比为清理单独养一条索引便宜。
--
-- 二、app.purge_subscription_fetch_log(p_keep_days, p_batch)：重写 00018 的同名函数
--
-- 00018 的版本有三处坏：
--   1. 追加写触发器照样触发（SECURITY DEFINER 不绕过触发器），DELETE 一执行就报错；
--   2. 不限租户，一次删所有租户；
--   3. 没有固定 search_path（SECURITY DEFINER 函数可被调用方的 search_path 劫持）。
-- 它从来没有调用方，configure-app-role.sql 还专门把它从 aegis_app 收回。这里删掉旧签名
-- (interval)，按上面同样的边界重写：保留期下限 31 天，只删当前租户，分批。新签名给 aegis_app
-- 执行权（与 purge_node_metrics 一样，运行角色多得到的只有「删本租户 31 天以前的拉取日志」）。
-- 后台行为趋势读 00114 的 activity_daily（已回填 90 天、每轮重算最近 2 个已结束日），
-- 不再直接读 90 天的拉取日志，所以清到 31 天不会让趋势图后段变空。
--
-- 三、node_traffic_reports.duplicate_of 去掉自引用外键
--
-- replica 模式下外键的引用方触发器不生效：删掉一份原报文时，指向它的重复件不会被
-- ON DELETE SET NULL 置空，留下悬空引用；外键本身也就名不副实。改成不带外键的普通 uuid
-- 列（含义不变：重复件指向它重复的那份，那份可能已过保留期被删）。清理按 received_at
-- 从旧到新删，原报文总比它的重复件先到期，悬空最多持续到下一轮把重复件也删掉。
-- 00123 的 (node_id, client_report_id) 唯一部分索引不受影响：重复件不带编号，删原报文时
-- 编号一起走，同一编号以后再来会被当成新报文入账（pdnd 只在结果不确定时立即重发，不会隔
-- 31 天重发）。
--
-- 耗时：删外键只改目录（不扫表），两个函数只是建函数；5k 规模下整个迁移在毫秒级。
-- 发布是停写窗口，ALTER TABLE 的表锁不影响线上。

-- +goose Up
SET LOCAL lock_timeout = '5s';

ALTER TABLE public.node_traffic_reports
  DROP CONSTRAINT IF EXISTS node_traffic_reports_duplicate_of_fkey;

COMMENT ON COLUMN public.node_traffic_reports.duplicate_of IS
  '重复件指向它重复的那份上报（不带外键：那份可能已过 31 天保留期被清理）。';

-- +goose StatementBegin
CREATE FUNCTION app.purge_node_traffic_reports(p_keep_days int, p_batch int)
RETURNS bigint
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public, pg_temp
SET session_replication_role = replica
AS $$
DECLARE
  v_tenant  uuid := app.current_tenant_id();
  v_deleted bigint;
BEGIN
  IF v_tenant IS NULL THEN
    RAISE EXCEPTION 'purge_node_traffic_reports 需要租户作用域' USING ERRCODE = 'insufficient_privilege';
  END IF;
  IF p_keep_days IS NULL OR p_keep_days < 31 THEN
    RAISE EXCEPTION '流量上报留档至少保留 31 天' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_batch IS NULL OR p_batch < 1 OR p_batch > 50000 THEN
    RAISE EXCEPTION '每批删除行数必须在 1 到 50000 之间' USING ERRCODE = 'invalid_parameter_value';
  END IF;

  DELETE FROM public.node_traffic_reports r
   USING (SELECT o.id
            FROM public.nodes n
            CROSS JOIN LATERAL (
              SELECT t.id
                FROM public.node_traffic_reports t
               WHERE t.node_id = n.id
                 AND t.received_at < now() - make_interval(days => p_keep_days)
               ORDER BY t.received_at
               LIMIT p_batch) o
           WHERE n.tenant_id = v_tenant
           LIMIT p_batch) d
   WHERE r.id = d.id
     AND r.tenant_id = v_tenant;
  GET DIAGNOSTICS v_deleted = ROW_COUNT;
  RETURN v_deleted;
END $$;

COMMENT ON FUNCTION app.purge_node_traffic_reports(int, int) IS
  '流量上报留档保留期清理：只删当前租户、至少 31 天以前的上报，每次最多 p_batch 行，由定时任务循环调用。';

REVOKE ALL ON FUNCTION app.purge_node_traffic_reports(int, int) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.purge_node_traffic_reports(int, int) TO aegis_app;
-- +goose StatementEnd

-- +goose StatementBegin
DROP FUNCTION IF EXISTS app.purge_subscription_fetch_log(interval);

CREATE FUNCTION app.purge_subscription_fetch_log(p_keep_days int, p_batch int)
RETURNS bigint
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public, pg_temp
SET session_replication_role = replica
AS $$
DECLARE
  v_tenant  uuid := app.current_tenant_id();
  v_deleted bigint;
BEGIN
  IF v_tenant IS NULL THEN
    RAISE EXCEPTION 'purge_subscription_fetch_log 需要租户作用域' USING ERRCODE = 'insufficient_privilege';
  END IF;
  IF p_keep_days IS NULL OR p_keep_days < 31 THEN
    RAISE EXCEPTION '订阅拉取日志至少保留 31 天' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_batch IS NULL OR p_batch < 1 OR p_batch > 50000 THEN
    RAISE EXCEPTION '每批删除行数必须在 1 到 50000 之间' USING ERRCODE = 'invalid_parameter_value';
  END IF;

  -- (tenant_id, fetched_at DESC) 索引（00018）从最早的一端取一批
  DELETE FROM public.subscription_fetch_log f
   USING (SELECT id
            FROM public.subscription_fetch_log
           WHERE tenant_id = v_tenant
             AND fetched_at < now() - make_interval(days => p_keep_days)
           ORDER BY fetched_at
           LIMIT p_batch) d
   WHERE f.id = d.id
     AND f.tenant_id = v_tenant;
  GET DIAGNOSTICS v_deleted = ROW_COUNT;
  RETURN v_deleted;
END $$;

COMMENT ON FUNCTION app.purge_subscription_fetch_log(int, int) IS
  '订阅拉取日志保留期清理：只删当前租户、至少 31 天以前的记录，每次最多 p_batch 行，由定时任务循环调用。';

REVOKE ALL ON FUNCTION app.purge_subscription_fetch_log(int, int) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.purge_subscription_fetch_log(int, int) TO aegis_app;
-- +goose StatementEnd

COMMENT ON TABLE public.subscription_fetch_log IS
  '订阅拉取审计。IP/UA 仅存带盐哈希，保留 31 天（app.purge_subscription_fetch_log）。失败记录同样保存，用于识别扫描探测。';

-- +goose Down
SET LOCAL lock_timeout = '5s';

COMMENT ON TABLE public.subscription_fetch_log IS
  '订阅拉取审计。IP/UA 仅存带盐哈希，保留 30 天。失败记录同样保存，用于识别扫描探测。';

-- +goose StatementBegin
DROP FUNCTION IF EXISTS app.purge_subscription_fetch_log(int, int);

-- 00018 原文（configure-app-role.sql 的旧版本会再把它从 aegis_app 收回）
CREATE OR REPLACE FUNCTION app.purge_subscription_fetch_log(retain interval DEFAULT '30 days')
RETURNS bigint LANGUAGE plpgsql SECURITY DEFINER AS $$
DECLARE
  removed bigint;
BEGIN
  DELETE FROM subscription_fetch_log WHERE fetched_at < now() - retain;
  GET DIAGNOSTICS removed = ROW_COUNT;
  RETURN removed;
END;
$$;
-- +goose StatementEnd

DROP FUNCTION IF EXISTS app.purge_node_traffic_reports(int, int);

COMMENT ON COLUMN public.node_traffic_reports.duplicate_of IS NULL;

-- 保留期清理可能已经删掉了一些原报文，留下指向它们的重复件：外键先按 NOT VALID 加回，
-- 只约束之后的写入；再试着校验存量，没有悬空引用（新装、没跑过清理的库）就成为已校验的
-- 外键，与 00131 之前的形状逐项一致。有悬空引用时校验失败只回滚这一步，外键保持 NOT VALID，
-- 回滚照常完成（不为了外键去删报文）。再 Up 时照常删掉。
ALTER TABLE public.node_traffic_reports
  ADD CONSTRAINT node_traffic_reports_duplicate_of_fkey
  FOREIGN KEY (duplicate_of) REFERENCES public.node_traffic_reports(id) ON DELETE SET NULL NOT VALID;

-- +goose StatementBegin
DO $$
BEGIN
  ALTER TABLE public.node_traffic_reports
    VALIDATE CONSTRAINT node_traffic_reports_duplicate_of_fkey;
EXCEPTION WHEN foreign_key_violation THEN
  RAISE NOTICE '00131 Down: duplicate reports point at purged originals; node_traffic_reports_duplicate_of_fkey stays NOT VALID';
END $$;
-- +goose StatementEnd

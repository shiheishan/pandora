-- 后台列表分页索引，与探针点保留期清理函数的修正。
--
-- 一、分页索引
--
-- 后台用户列表与订单列表都是「本租户、按创建时间倒序、取一页」：
--   - 用户列表（adminops.ListUsers）ORDER BY created_at DESC, id DESC LIMIT/OFFSET；
--   - 订单列表（adminops.ListOrders）ORDER BY created_at DESC, id DESC LIMIT/OFFSET，
--     用户详情里的「最近 20 单」走已有的 idx_orders_user。
-- 原来 users 只有 (tenant_id, id) 唯一索引与 (tenant_id, status)，orders 只有带
-- user_id / status 前导列的两条：不带筛选的第一页只能全表扫描再排序。两个列表现在先
-- 按这里的顺序取一页 id，再只对这一页拼当前订阅、支付渠道等读模型，取页的代价不随
-- 行数增长。id 跟在 created_at 后面：批量生成用户在一个事务里，created_at 全相同，
-- 翻页顺序也要确定。
--
-- 二、app.purge_node_metrics
--
-- 00015 的本意是「保留期清理以定义者权限绕过追加写触发器」，但函数没有声明
-- SECURITY DEFINER，函数体里的 ALTER TABLE … DISABLE TRIGGER 又要求表属主：运行角色
-- aegis_app 一调就失败（不是属主，DELETE 也已被 configure-app-role 收回），所以它一直
-- 没有调用方，node_metrics 只增不减。这里按原设计补齐，同时收紧边界：
--   - SECURITY DEFINER + 固定 search_path，属主是跑迁移的特权角色；
--   - 不再用 DISABLE TRIGGER（DDL 锁会挡住并发心跳写入，且对整张表生效），改为只在本函数
--     执行期间生效的 session_replication_role = replica，追加写触发器对其它任何语句照旧；
--   - 只删当前租户（app.current_tenant_id()）的行：定义者绕过 RLS，租户边界由函数自己守；
--   - 保留期下限 48 小时（与 00015 的保留策略一致，低于即拒绝），调用方不能借它删近期数据；
--   - 每次最多删 p_batch 行（1–50000），由调用方循环，单个事务短、锁少。
-- aegis_app 仍然不能直接 UPDATE / DELETE node_metrics；它多得到的只有「删本租户 48 小时
-- 以前的探针点」这一件事，这正是 00015 写下的保留策略。
--
-- 发布是停写窗口，普通 CREATE INDEX 的写锁不影响线上。

-- +goose Up

SET LOCAL lock_timeout = '5s';

CREATE INDEX IF NOT EXISTS idx_users_tenant_created
  ON users (tenant_id, created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_orders_tenant_created
  ON orders (tenant_id, created_at DESC, id DESC);

-- +goose StatementBegin
DROP FUNCTION IF EXISTS app.purge_node_metrics(int);

CREATE FUNCTION app.purge_node_metrics(p_keep_hours int, p_batch int)
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
    RAISE EXCEPTION 'purge_node_metrics 需要租户作用域' USING ERRCODE = 'insufficient_privilege';
  END IF;
  IF p_keep_hours IS NULL OR p_keep_hours < 48 THEN
    RAISE EXCEPTION 'node_metrics 至少保留 48 小时' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_batch IS NULL OR p_batch < 1 OR p_batch > 50000 THEN
    RAISE EXCEPTION '每批删除行数必须在 1 到 50000 之间' USING ERRCODE = 'invalid_parameter_value';
  END IF;

  DELETE FROM public.node_metrics m
   USING (SELECT node_id, recorded_at
            FROM public.node_metrics
           WHERE tenant_id = v_tenant
             AND recorded_at < now() - make_interval(hours => p_keep_hours)
           ORDER BY recorded_at
           LIMIT p_batch) d
   WHERE m.node_id = d.node_id AND m.recorded_at = d.recorded_at
     AND m.tenant_id = v_tenant;
  GET DIAGNOSTICS v_deleted = ROW_COUNT;
  RETURN v_deleted;
END $$;

COMMENT ON FUNCTION app.purge_node_metrics(int, int) IS
  '探针点保留期清理：只删当前租户、至少 48 小时以前的点，每次最多 p_batch 行，由定时任务循环调用。';

REVOKE ALL ON FUNCTION app.purge_node_metrics(int, int) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.purge_node_metrics(int, int) TO aegis_app;
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
DROP FUNCTION IF EXISTS app.purge_node_metrics(int, int);

-- 00015 原文
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

DROP INDEX IF EXISTS idx_orders_tenant_created;
DROP INDEX IF EXISTS idx_users_tenant_created;

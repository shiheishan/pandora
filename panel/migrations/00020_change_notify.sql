-- +goose Up
-- +goose StatementBegin
-- 全站数据变更通知
--
-- 目标是「页面永远显示最新数据，不需要手动刷新」。做法有两种：
--
--   A. 在每个写操作的 handler 里发一条推送
--   B. 在数据库层面挂触发器，任何写入自动通知
--
-- 选 B。A 看似简单，但它要求今后每一个新写操作都记得加那一行 ——
-- 漏掉一处的表现是「某个页面偶尔不刷新」，这种问题几乎不会被测出来，
-- 只会变成用户偶尔的抱怨。而且后台任务、定时作业、运维手工执行的 SQL
-- 根本不经过 handler，A 对它们完全无效。
--
-- B 只写一次，覆盖所有写入路径。代价是每次写多一次 NOTIFY 调用，
-- 那点开销远小于它省掉的持续维护成本。

-- 变更通知函数。
--
-- 载荷刻意只放定位信息，不放数据本身：
--   1. pg_notify 的载荷上限是 8000 字节，行数据随时可能超
--   2. 推数据就要在这里做权限判断，而触发器里没有请求上下文
--   3. 前端收到通知后重新拉数据，走的是既有的鉴权路径
CREATE OR REPLACE FUNCTION app.notify_change() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER AS $$
DECLARE
  row_data  jsonb;
  tenant    text;
  owner     text;
  row_id    text;
BEGIN
  row_data := to_jsonb(COALESCE(NEW, OLD));

  tenant := row_data ->> 'tenant_id';
  IF tenant IS NULL THEN
    -- 没有租户列的表不参与推送：无法判断该发给谁，
    -- 广播给所有人则是跨租户泄露
    RETURN COALESCE(NEW, OLD);
  END IF;

  -- user_id 决定这条变更是私有的还是全站的。
  -- 订单、订阅、工单这类带 user_id 的只推给本人；
  -- 套餐、节点、公告这类没有 user_id 的推给整个租户。
  owner  := row_data ->> 'user_id';
  row_id := row_data ->> 'id';

  PERFORM pg_notify('aegis_change', json_build_object(
    'tbl',    TG_TABLE_NAME,
    'op',     TG_OP,
    'tenant', tenant,
    'user',   owner,
    'id',     row_id
  )::text);

  RETURN COALESCE(NEW, OLD);
END;
$$;

COMMENT ON FUNCTION app.notify_change() IS
  '数据变更通知。只发定位信息（表名/操作/租户/归属/主键），前端据此重新拉取。';

-- 给需要实时反映到界面上的表挂上触发器。
--
-- 不是所有表都挂：审计日志、流量上报这类高频写入的表，
-- 每秒可能有成百上千次写，推送它们既没有界面意义，
-- 又会把通知通道淹掉，让真正有用的变更被延迟。
DO $$
DECLARE
  t text;
  watched text[] := ARRAY[
    -- 用户自己的东西：变更后本人页面要立刻反映
    'orders', 'subscriptions', 'subscription_credentials',
    'support_tickets', 'support_messages', 'quota_balances',
    'wallet_accounts', 'wallet_transactions',
    -- 全站共享的东西：变更后所有人的页面都要反映
    'plans', 'plan_versions', 'plan_prices', 'nodes', 'announcements'
  ];
BEGIN
  FOREACH t IN ARRAY watched LOOP
    IF to_regclass('public.' || t) IS NULL THEN
      CONTINUE;
    END IF;
    EXECUTE format('DROP TRIGGER IF EXISTS %I ON public.%I', 'zz_notify_' || t, t);
    -- 触发器名以 zz_ 开头：触发器按名字排序执行，
    -- 通知必须排在业务触发器（校验、审计、账目平衡）之后 ——
    -- 否则一次最终被回滚的写入也会把通知发出去，
    -- 前端就会拉到一个并不存在的变更。
    EXECUTE format(
      'CREATE TRIGGER %I AFTER INSERT OR UPDATE OR DELETE ON public.%I
         FOR EACH ROW EXECUTE FUNCTION app.notify_change()',
      'zz_notify_' || t, t);
  END LOOP;
END;
$$;
-- +goose StatementEnd

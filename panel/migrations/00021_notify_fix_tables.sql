-- +goose Up
-- +goose StatementBegin
-- 修正变更通知覆盖的表名
--
-- 上一版按猜测写了 support_tickets / support_messages / plan_prices /
-- wallet_* 这几个名字，实际的表叫 tickets / ticket_messages / prices，
-- 钱包则根本不是独立表。表不存在时上一版是静默跳过的 ——
-- 结果就是工单变了页面不会刷新，而日志里什么都看不到。
--
-- 静默跳过在这里是个糟糕的选择：它把「配置写错了」变成了
-- 「某个页面偶尔不更新」，后者要难查得多。这一版改为显式对账，
-- 缺表直接在迁移时报出来。
DO $$
DECLARE
  t text;
  missing text[] := '{}';
  watched text[] := ARRAY[
    'orders', 'subscriptions', 'subscription_credentials',
    'tickets', 'ticket_messages', 'quota_balances',
    'plans', 'plan_versions', 'prices', 'nodes', 'announcements'
  ];
BEGIN
  FOREACH t IN ARRAY watched LOOP
    IF to_regclass('public.' || t) IS NULL THEN
      missing := missing || t;
      CONTINUE;
    END IF;
    EXECUTE format('DROP TRIGGER IF EXISTS %I ON public.%I', 'zz_notify_' || t, t);
    EXECUTE format(
      'CREATE TRIGGER %I AFTER INSERT OR UPDATE OR DELETE ON public.%I
         FOR EACH ROW EXECUTE FUNCTION app.notify_change()',
      'zz_notify_' || t, t);
  END LOOP;

  IF array_length(missing, 1) > 0 THEN
    RAISE EXCEPTION '这些表不存在，变更通知无法挂载: %', array_to_string(missing, ', ');
  END IF;
END;
$$;

-- 清掉上一版按错名字建的（如果建成功过）
DROP TRIGGER IF EXISTS zz_notify_support_tickets ON public.tickets;
-- +goose StatementEnd

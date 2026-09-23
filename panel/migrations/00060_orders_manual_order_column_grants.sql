-- +goose Up
-- 补上 orders 的 manual_reason / created_by 列级 INSERT 授权。
--
-- 00036 把 orders 从表级 INSERT 收窄成列级白名单。后来人工单功能给 orders
-- 加了 manual_reason 和 created_by，checkout.go 的插入语句显式写这两列，
-- 却没人回头把它们加进那份白名单——结果是任何下单都 permission denied。
--
-- 现网没炸是因为它的 orders 至今是 aegis_app=arwd 的表级权限，00036 那段
-- 收窄从未在生产生效（goose 不重跑已应用的迁移，那段多半是 00036 落库之后
-- 才补进文件的）。宽权限恰好盖住了这个洞，代价是设计中的最小权限一直没到位。
--
-- 这里只补授权，不动生产那份更宽的权限。把生产收窄到列级最小权限是另一件
-- 事，它得先验证 UPDATE 面同样完整，风险和验证方式都不是一个量级。这条对
-- 两种起点都是安全的加法：生产已有表级 INSERT，多两条列级授权不改变行为；
-- 全新部署则从此能下单。
--
-- 注意 business_request_id 不在这里——它虽然 NOT NULL 且无默认值，但由
-- trg_orders_business_request 触发器填充，触发器赋值不受列权限约束。

-- +goose StatementBegin
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'aegis_app') THEN
    RETURN;
  END IF;

  GRANT INSERT (manual_reason, created_by) ON orders TO aegis_app;

  -- 收口断言：orders 上，代码实际会写的那些列必须都能写。
  --
  -- 这个缺陷的形态是「加了列、代码开始写它、忘了补授权」，它不会在开发机
  -- 上暴露——那里连库的多半是超级用户——只在应用了列级收窄的库上才炸，
  -- 而且炸的是下单这条主路径。把已知的清单钉在这里，下一个漏授权的人在
  -- 迁移这一步就会被拦住。
  --
  -- 清单来源是 checkout.go 与 renewal.go 的 INSERT 语句。它需要跟着代码走，
  -- 所以 run-pg18-gates.sh 里还有一份从源码现扫的检查兜底。
  IF EXISTS (
    SELECT 1
      FROM unnest(ARRAY[
        'tenant_id','order_no','user_id','kind','status','currency',
        'subtotal_amount','discount_amount','tax_amount','total_amount',
        'balance_applied','payable_amount','expires_at','coupon_id',
        'subscription_id','idempotency_key_id','manual_reason','created_by'
      ]) AS col
     WHERE NOT has_column_privilege('aegis_app', 'public.orders', col, 'INSERT')
  ) THEN
    RAISE EXCEPTION '00060: orders 上仍有代码要写、但 aegis_app 无权写的列';
  END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'aegis_app') THEN
    RETURN;
  END IF;
  -- 只在列级收窄确实生效的库上回收。生产那种表级 arwd 的库回收这两列毫无
  -- 意义，反倒会让人以为权限收紧了。
  IF NOT has_table_privilege('aegis_app', 'public.orders', 'INSERT') THEN
    REVOKE INSERT (manual_reason, created_by) ON orders FROM aegis_app;
  END IF;
END $$;
-- +goose StatementEnd

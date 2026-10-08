-- 运行角色补上 payments.method 的列级 INSERT 授权（w9fix：付款方式写进 payments，门户完成页与订单页说「支付宝付了 ¥x」）。
--
-- 00040 把 payments 的 INSERT 收窄成列级白名单，当时代码不写 method（这一列从 00004 起一直为空）。
-- billing 的结算（settlePaymentTx）与迟到付款挂账（quarantineUnexpectedPayment）现在写 method：
-- 回调或查单带回的方式优先，没有就取发起支付那个意图上记的方式。不补授权，列级收窄的库上每笔
-- 回调都会 permission denied。只加一条列级授权，不锁数据、不扫表，毫秒级。
-- configure-app-role.sql 的同一份白名单同步加了 method（两处是同一个口径，见那里的注释）。

-- +goose Up
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

-- +goose StatementBegin
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'aegis_app') THEN
    RETURN;
  END IF;
  GRANT INSERT (method) ON payments TO aegis_app;
END $$;
-- +goose StatementEnd

-- +goose Down
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

-- +goose StatementBegin
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'aegis_app') THEN
    RETURN;
  END IF;
  -- 只在列级收窄确实生效的库上回收（与 00060 同一口径）：表级 INSERT 的库回收这一列没有意义
  IF NOT has_table_privilege('aegis_app', 'public.payments', 'INSERT') THEN
    REVOKE INSERT (method) ON payments FROM aegis_app;
  END IF;
END $$;
-- +goose StatementEnd

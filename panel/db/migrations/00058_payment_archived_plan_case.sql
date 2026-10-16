-- +goose Up
-- 挂账种类加 archived_plan：套餐归档后才到账的新购、续费、叠加、换套餐单不开通，实付自动转入用户余额（用户 2026-10-16 定）。属 payment 模块。
--
-- 只有 payment 的 repo 写 late_payment_cases；archived_plan 案在入账的同一事务里由系统转入余额（status 直接到 applied），
-- 账目复用 late_payment_suspense / late_payment_applied 两种分录，不新增账本种类。
-- 锁与耗时（整个迁移在一个事务里，锁持到提交）：换 CHECK 要 late_payment_cases 的 ACCESS EXCLUSIVE 锁，
--   加约束要扫一遍全表校验，与表行数成正比；挂账案只在迟到付款时产生，表很小，瞬间完成。
--   预检：SELECT count(*) FROM late_payment_cases;（到百万行级再改成 NOT VALID + VALIDATE）。
--   失败回滚：任一步失败（含 lock_timeout 超时）整笔回滚，库保持原样，重试即可。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

ALTER TABLE late_payment_cases DROP CONSTRAINT late_payment_cases_case_kind_check;
ALTER TABLE late_payment_cases ADD CONSTRAINT late_payment_cases_case_kind_check
  CHECK (case_kind IN ('released_order', 'excess_capture', 'ineligible_subscription', 'archived_plan'));

-- +goose Down
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

-- 已有 archived_plan 案时退不回去：表挂了 deny_delete 不能删行，改成别的种类又会丢掉「套餐已归档」这条事实。
-- 这些案都已自动转入余额（applied），要退先人工确认后再处理。
-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM late_payment_cases WHERE case_kind = 'archived_plan') THEN
    RAISE EXCEPTION '已有 archived_plan 挂账案（套餐归档后到账、已自动转入余额），不能退回 00057：先人工核对这些案再决定怎么退';
  END IF;
END
$$;
-- +goose StatementEnd

ALTER TABLE late_payment_cases DROP CONSTRAINT late_payment_cases_case_kind_check;
ALTER TABLE late_payment_cases ADD CONSTRAINT late_payment_cases_case_kind_check
  CHECK (case_kind IN ('released_order', 'excess_capture', 'ineligible_subscription'));

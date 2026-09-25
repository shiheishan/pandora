-- R99（D-C-5）：限速与超额策略解耦。
--
-- 00035 的 plan_versions_throttle_exact 要求「throttle_kbps 有值 ⇔ 策略为
-- throttle」。可数据面从来不看策略：UniProxy 把 throttle_kbps 原样下发成
-- speed_limit，pdnd 按用户常驻令牌桶限速，全程生效；而 throttle 策略本身
-- （流量用完后降速）从没实现过。这条约束因此只剩一个效果——想给套餐限速
-- 就得把策略写成一个并不存在的行为，向导写死 suspend 时限速必定 422。
--
-- 定案：套餐上写多少就限多少，null = 不限，与策略无关。新写入的策略只收
-- suspend 由 adminops 校验负责；存量行一律不改，所以这里只删约束。

-- +goose Up

-- +goose StatementBegin
ALTER TABLE plan_versions DROP CONSTRAINT IF EXISTS plan_versions_throttle_exact;
-- +goose StatementEnd

-- +goose Down

-- 只有存量仍满足旧约束时才能恢复：解耦之后写入的「suspend + 限速」版本
-- 在旧口径下非法，替管理员删掉限速或改写策略都会改变已售套餐的交付，
-- 所以拒绝回滚并说明原因，由人决定怎么处理这些版本。
-- +goose StatementBegin
DO $$
DECLARE
  offending bigint;
BEGIN
  SELECT count(*) INTO offending
    FROM plan_versions
   WHERE (overage_policy = 'throttle' AND throttle_kbps IS NULL)
      OR (overage_policy <> 'throttle' AND throttle_kbps IS NOT NULL);
  IF offending > 0 THEN
    RAISE EXCEPTION
      'rollback refused (plan throttle decoupling): % plan version(s) set throttle_kbps without overage_policy=throttle (or the reverse); fix them before restoring plan_versions_throttle_exact',
      offending;
  END IF;
END
$$;

ALTER TABLE plan_versions
  ADD CONSTRAINT plan_versions_throttle_exact CHECK (
    (overage_policy = 'throttle' AND throttle_kbps IS NOT NULL)
    OR (overage_policy <> 'throttle' AND throttle_kbps IS NULL)
  );
-- +goose StatementEnd

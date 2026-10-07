-- 后台给订阅加流量：发一笔不过期的流量包（用户 2026-10-07 定，w5account）。
--
-- 流量包余额（00070）挂在用户身上、不过期、用完为止，每周期先扣套餐额度再扣它；礼品卡
-- 送的流量走的就是它。后台加流量复用同一张表，只是来源多一种：source = 'admin'，
-- source_id 是这次发放自己的 uuid（billing.GrantTrafficPackAsAdmin 生成），审计里记着
-- 操作人与原因。所以这里只把来源的 CHECK 放宽到 admin。
--
-- 先 NOT VALID 加新约束、再 VALIDATE：加约束只要很短的表锁，校验存量只拿
-- SHARE UPDATE EXCLUSIVE，不挡扣量的 UPDATE。旧约束是 00070 列定义上的匿名 CHECK，
-- 默认名 traffic_pack_grants_source_check。

-- +goose Up
SET LOCAL lock_timeout = '5s';

ALTER TABLE public.traffic_pack_grants
  DROP CONSTRAINT traffic_pack_grants_source_check;
ALTER TABLE public.traffic_pack_grants
  ADD CONSTRAINT traffic_pack_grants_source_check
  CHECK (source IN ('order', 'gift_card', 'migration', 'admin')) NOT VALID;
ALTER TABLE public.traffic_pack_grants
  VALIDATE CONSTRAINT traffic_pack_grants_source_check;

-- +goose Down
SET LOCAL lock_timeout = '5s';

-- 已经发出去的后台流量包是用户的余额，表是追加写、不许删：有这种行时退不回去，
-- 明确拒绝而不是删用户的钱。没有时恢复 00070 的口径，之后可以再 Up。
-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM public.traffic_pack_grants WHERE source = 'admin') THEN
    RAISE EXCEPTION '00129 down refused: admin-granted traffic packs exist and are append-only user balances';
  END IF;
END
$$;
-- +goose StatementEnd
ALTER TABLE public.traffic_pack_grants
  DROP CONSTRAINT traffic_pack_grants_source_check;
ALTER TABLE public.traffic_pack_grants
  ADD CONSTRAINT traffic_pack_grants_source_check
  CHECK (source IN ('order', 'gift_card', 'migration'));

-- +goose Up
-- 分销参数的默认值。
--
-- 费率默认 0：分销要不要开、开多少，是经营决策，不该由代码替老板决定。
-- 默认开着更危险 —— 上线第一天所有订单都在计提佣金，等发现时
-- 账已经记进去了，冲销要一条条来。
--
-- 精度定在 1%：代理谈的是「给你一成」还是「给你两成」，
-- 没有人在谈 12.5%。存成万分比只是为了和优惠券共用一套惯例。

INSERT INTO system_settings (tenant_id, key, value, value_schema)
SELECT t.id, v.key, v.value, v.schema
  FROM tenants t
  CROSS JOIN (VALUES
    ('commission.rate_percent', '0'::jsonb,
     '{"type":"integer","minimum":0,"maximum":50,"title":"分销佣金比例（%）"}'::jsonb),
    ('commission.freeze_days',  '7'::jsonb,
     '{"type":"integer","minimum":0,"maximum":90,"title":"佣金冻结天数"}'::jsonb),
    ('commission.min_withdraw', '1000'::jsonb,
     '{"type":"integer","minimum":0,"title":"最低提现金额（最小货币单位）"}'::jsonb)
  ) AS v(key, value, schema)
 WHERE NOT EXISTS (
   SELECT 1 FROM system_settings s
    WHERE s.tenant_id = t.id AND s.key = v.key);

-- 提现按用户查是最频繁的访问方式：用户看自己的记录、
-- 计算在途金额时也要按用户过滤。
CREATE INDEX IF NOT EXISTS withdrawals_user_idx
  ON withdrawals (tenant_id, user_id, requested_at DESC);

-- 解冻扫描要找「pending 且冻结期已过」的条目。
-- 部分索引只覆盖 pending，因为已解冻的条目永远不会再被这个查询碰到，
-- 而它们会随时间成为表里的绝大多数。
CREATE INDEX IF NOT EXISTS commission_pending_idx
  ON commission_entries (tenant_id, frozen_until)
  WHERE status = 'pending';

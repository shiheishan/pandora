-- Deterministic CNY demo catalog for schema 35+.
-- Production catalog changes must use the Admin authoring API.
-- Re-runs never alter frozen versions, economic price fields, or archived rows.
\set ON_ERROR_STOP on
\set tenant '00000000-0000-7000-8000-000000000001'

BEGIN;
SET LOCAL app.tenant_id = :'tenant';

-- Preserve historical USD rows and archive only offers that are still active.
UPDATE prices
SET status = 'archived', row_version = row_version + 1
WHERE tenant_id = :'tenant'
  AND currency = 'USD'
  AND status = 'active';

INSERT INTO products
  (id, tenant_id, code, name, description, kind, status, sort_order)
VALUES
  ('00000000-0000-7000-8000-00000000a101', :'tenant', 'starter',
   '入门套餐', '轻量上网与日常使用', 'subscription', 'active', 10),
  ('00000000-0000-7000-8000-00000000a102', :'tenant', 'pro',
   '标准套餐', '高清视频与多设备并行', 'subscription', 'active', 20),
  ('00000000-0000-7000-8000-00000000a103', :'tenant', 'ultimate',
   '旗舰套餐', '大流量与全节点解锁', 'subscription', 'active', 30)
ON CONFLICT (id) DO NOTHING;

INSERT INTO plans
  (id, tenant_id, product_id, code, name, description,
   visibility, status, sort_order)
VALUES
  ('00000000-0000-7000-8000-00000000c101', :'tenant',
   '00000000-0000-7000-8000-00000000a101', 'starter', '入门套餐',
   '100 GB / 月 · 2 台设备 · 全区域节点', 'public', 'draft', 10),
  ('00000000-0000-7000-8000-00000000c102', :'tenant',
   '00000000-0000-7000-8000-00000000a102', 'pro', '标准套餐',
   '500 GB / 月 · 4 台设备 · 流媒体优化', 'public', 'draft', 20),
  ('00000000-0000-7000-8000-00000000c103', :'tenant',
   '00000000-0000-7000-8000-00000000a103', 'ultimate', '旗舰套餐',
   '2 TB / 月 · 8 台设备 · 专线优先调度', 'public', 'draft', 30)
ON CONFLICT (id) DO NOTHING;

INSERT INTO plan_versions
  (id, tenant_id, plan_id, version,
   quota_reset_strategy, grace_period_hours, grace_keeps_service,
   renewal_extends_period, renewal_resets_quota, renewal_keeps_addons,
   max_devices, device_release_hours, overage_policy, throttle_kbps)
VALUES
  ('00000000-0000-7000-8000-0000000d1101', :'tenant',
   '00000000-0000-7000-8000-00000000c101', 1,
   'billing_cycle', 48, true, true, true, true, 2, 24, 'suspend', NULL),
  ('00000000-0000-7000-8000-0000000d1102', :'tenant',
   '00000000-0000-7000-8000-00000000c102', 1,
   'billing_cycle', 72, true, true, true, true, 4, 24, 'suspend', NULL),
  ('00000000-0000-7000-8000-0000000d1103', :'tenant',
   '00000000-0000-7000-8000-00000000c103', 1,
   'billing_cycle', 96, true, true, true, true, 8, 12, 'throttle', 5120)
ON CONFLICT (id) DO NOTHING;

INSERT INTO node_pools
  (id, tenant_id, code, name, region, capabilities)
VALUES
  ('00000000-0000-7000-8000-00000000e101', :'tenant',
   'catalog-default', '套餐默认资源池', 'global', ARRAY['standard'])
ON CONFLICT (tenant_id, code) DO NOTHING;

INSERT INTO entitlements (tenant_id, plan_version_id, code, value)
SELECT :'tenant', pv.id, authored.code, authored.value
FROM plan_versions pv
JOIN (VALUES
  ('00000000-0000-7000-8000-0000000d1101'::uuid, 'node_pool.access', '["standard"]'::jsonb),
  ('00000000-0000-7000-8000-0000000d1101'::uuid, 'support.tier', '"community"'::jsonb),
  ('00000000-0000-7000-8000-0000000d1102'::uuid, 'node_pool.access', '["standard","premium"]'::jsonb),
  ('00000000-0000-7000-8000-0000000d1102'::uuid, 'feature.streaming', 'true'::jsonb),
  ('00000000-0000-7000-8000-0000000d1102'::uuid, 'support.tier', '"standard"'::jsonb),
  ('00000000-0000-7000-8000-0000000d1103'::uuid, 'node_pool.access', '["standard","premium","dedicated"]'::jsonb),
  ('00000000-0000-7000-8000-0000000d1103'::uuid, 'feature.streaming', 'true'::jsonb),
  ('00000000-0000-7000-8000-0000000d1103'::uuid, 'feature.priority', 'true'::jsonb),
  ('00000000-0000-7000-8000-0000000d1103'::uuid, 'support.tier', '"priority"'::jsonb)
) AS authored(version_id, code, value) ON authored.version_id = pv.id
WHERE pv.status = 'draft'
ON CONFLICT (plan_version_id, code) DO NOTHING;

INSERT INTO quota_definitions
  (tenant_id, plan_version_id, metric, limit_value, unit, period)
SELECT :'tenant', pv.id, authored.metric, authored.limit_value,
       authored.unit, authored.period
FROM plan_versions pv
JOIN (VALUES
  ('00000000-0000-7000-8000-0000000d1101'::uuid, 'traffic.bytes', 107374182400::bigint, 'bytes', 'cycle'),
  ('00000000-0000-7000-8000-0000000d1101'::uuid, 'devices.active', 2::bigint, 'count', 'cycle'),
  ('00000000-0000-7000-8000-0000000d1102'::uuid, 'traffic.bytes', 536870912000::bigint, 'bytes', 'cycle'),
  ('00000000-0000-7000-8000-0000000d1102'::uuid, 'devices.active', 4::bigint, 'count', 'cycle'),
  ('00000000-0000-7000-8000-0000000d1103'::uuid, 'traffic.bytes', 2199023255552::bigint, 'bytes', 'cycle'),
  ('00000000-0000-7000-8000-0000000d1103'::uuid, 'devices.active', 8::bigint, 'count', 'cycle')
) AS authored(version_id, metric, limit_value, unit, period)
  ON authored.version_id = pv.id
WHERE pv.status = 'draft'
ON CONFLICT (plan_version_id, metric, period) DO NOTHING;

INSERT INTO plan_node_pools (tenant_id, plan_version_id, pool_id)
SELECT :'tenant', pv.id, np.id
FROM plan_versions pv
JOIN node_pools np
  ON np.tenant_id = pv.tenant_id AND np.code = 'catalog-default'
WHERE pv.id IN (
  '00000000-0000-7000-8000-0000000d1101',
  '00000000-0000-7000-8000-0000000d1102',
  '00000000-0000-7000-8000-0000000d1103'
)
  AND pv.status = 'draft'
ON CONFLICT (plan_version_id, pool_id) DO NOTHING;

INSERT INTO prices
  (id, tenant_id, product_id, currency, unit_amount,
   billing_interval, interval_count, trial_days, status)
VALUES
  ('00000000-0000-7000-8000-0000000b1101', :'tenant', '00000000-0000-7000-8000-00000000a101', 'CNY', 990, 'month', 1, 0, 'active'),
  ('00000000-0000-7000-8000-0000000b1111', :'tenant', '00000000-0000-7000-8000-00000000a101', 'CNY', 9900, 'year', 1, 0, 'active'),
  ('00000000-0000-7000-8000-0000000b1102', :'tenant', '00000000-0000-7000-8000-00000000a102', 'CNY', 1990, 'month', 1, 0, 'active'),
  ('00000000-0000-7000-8000-0000000b1112', :'tenant', '00000000-0000-7000-8000-00000000a102', 'CNY', 19900, 'year', 1, 0, 'active'),
  ('00000000-0000-7000-8000-0000000b1103', :'tenant', '00000000-0000-7000-8000-00000000a103', 'CNY', 3990, 'month', 1, 0, 'active'),
  ('00000000-0000-7000-8000-0000000b1113', :'tenant', '00000000-0000-7000-8000-00000000a103', 'CNY', 39900, 'year', 1, 0, 'active')
ON CONFLICT (id) DO NOTHING;

UPDATE plan_versions
SET status = 'published', frozen_at = now(), row_version = row_version + 1
WHERE id IN (
  '00000000-0000-7000-8000-0000000d1101',
  '00000000-0000-7000-8000-0000000d1102',
  '00000000-0000-7000-8000-0000000d1103'
)
  AND status = 'draft';

UPDATE plans
SET current_version_id = CASE id
      WHEN '00000000-0000-7000-8000-00000000c101' THEN '00000000-0000-7000-8000-0000000d1101'::uuid
      WHEN '00000000-0000-7000-8000-00000000c102' THEN '00000000-0000-7000-8000-0000000d1102'::uuid
      WHEN '00000000-0000-7000-8000-00000000c103' THEN '00000000-0000-7000-8000-0000000d1103'::uuid
    END,
    status = 'active',
    row_version = row_version + 1
WHERE id IN (
  '00000000-0000-7000-8000-00000000c101',
  '00000000-0000-7000-8000-00000000c102',
  '00000000-0000-7000-8000-00000000c103'
)
  AND status = 'draft';

UPDATE plans
SET status = 'archived', row_version = row_version + 1
WHERE id = '00000000-0000-7000-8000-00000000c001'
  AND status IN ('draft', 'active');

UPDATE prices
SET status = 'archived', row_version = row_version + 1
WHERE id = '00000000-0000-7000-8000-00000000b003'
  AND status = 'active';

COMMIT;

\echo '--- Active CNY catalog ---'
SELECT pl.sort_order AS ord, pl.name AS plan_name, pr.currency,
       pr.unit_amount, pr.billing_interval,
       qd.limit_value AS traffic_bytes, pv.max_devices
FROM plans pl
JOIN plan_versions pv ON pv.id = pl.current_version_id
JOIN prices pr ON pr.product_id = pl.product_id AND pr.status = 'active'
LEFT JOIN quota_definitions qd
  ON qd.plan_version_id = pv.id AND qd.metric = 'traffic.bytes'
WHERE pl.status = 'active'
ORDER BY pl.sort_order, pr.unit_amount;

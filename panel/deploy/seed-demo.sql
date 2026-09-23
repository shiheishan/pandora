-- Local/E2E demo catalog for schema 35+.
-- Production catalog data must be authored through the Admin API.
-- Re-runs never mutate frozen snapshots or reactivate archived offers.
\set ON_ERROR_STOP on
\set tenant '''00000000-0000-7000-8000-000000000001'''

BEGIN;
SET LOCAL app.tenant_id = '00000000-0000-7000-8000-000000000001';

INSERT INTO products
  (id, tenant_id, code, name, description, kind, status, sort_order)
VALUES
  ('00000000-0000-7000-8000-00000000a001', :tenant, 'standard',
   '标准订阅', '包含基础资源池访问与 500GB 月流量',
   'subscription', 'active', 10)
ON CONFLICT (tenant_id, code) DO NOTHING;

INSERT INTO prices
  (id, tenant_id, product_id, currency, unit_amount,
   billing_interval, interval_count, trial_days, status)
VALUES
  ('00000000-0000-7000-8000-00000000b001', :tenant,
   '00000000-0000-7000-8000-00000000a001', 'USD', 990,
   'month', 1, 0, 'active'),
  ('00000000-0000-7000-8000-00000000b002', :tenant,
   '00000000-0000-7000-8000-00000000a001', 'USD', 9900,
   'year', 1, 0, 'active')
ON CONFLICT (id) DO NOTHING;

INSERT INTO plans
  (id, tenant_id, product_id, code, name, description,
   visibility, status, allow_new_purchase, sort_order)
VALUES
  ('00000000-0000-7000-8000-00000000c001', :tenant,
   '00000000-0000-7000-8000-00000000a001', 'standard', '标准套餐',
   '适合个人日常使用', 'public', 'draft', true, 10)
ON CONFLICT (tenant_id, code) DO NOTHING;

INSERT INTO plan_versions
  (id, tenant_id, plan_id, version,
   quota_reset_strategy, grace_period_hours, grace_keeps_service,
   renewal_extends_period, renewal_resets_quota,
   max_devices, max_concurrent, device_release_hours, overage_policy)
VALUES
  ('00000000-0000-7000-8000-00000000d001', :tenant,
   '00000000-0000-7000-8000-00000000c001', 1,
   'billing_cycle', 72, true, true, true, 3, 3, 24, 'suspend')
ON CONFLICT (plan_id, version) DO NOTHING;

INSERT INTO node_pools
  (id, tenant_id, code, name, region, capabilities)
VALUES
  ('00000000-0000-7000-8000-00000000e001', :tenant,
   'default', '默认资源池', 'ap-east', ARRAY['standard'])
ON CONFLICT (tenant_id, code) DO NOTHING;

-- Child rows are authored only while this deterministic version is a draft.
-- INSERT ... ON CONFLICT alone is insufficient because BEFORE triggers run
-- before PostgreSQL discovers the conflict on an already-frozen row.
INSERT INTO entitlements (tenant_id, plan_version_id, code, value)
SELECT :tenant, pv.id, authored.code, authored.value
FROM plan_versions pv
CROSS JOIN (VALUES
  ('node_pool.access', '["default"]'::jsonb),
  ('feature.multi_device', 'true'::jsonb),
  ('support.tier', '"standard"'::jsonb)
) AS authored(code, value)
WHERE pv.id = '00000000-0000-7000-8000-00000000d001'
  AND pv.status = 'draft'
ON CONFLICT (plan_version_id, code) DO NOTHING;

INSERT INTO quota_definitions
  (tenant_id, plan_version_id, metric, limit_value, unit, period)
SELECT :tenant, pv.id, authored.metric, authored.limit_value,
       authored.unit, authored.period
FROM plan_versions pv
CROSS JOIN (VALUES
  ('traffic.bytes', 500::bigint * 1024 * 1024 * 1024, 'bytes', 'cycle'),
  ('devices.active', 3::bigint, 'count', 'total')
) AS authored(metric, limit_value, unit, period)
WHERE pv.id = '00000000-0000-7000-8000-00000000d001'
  AND pv.status = 'draft'
ON CONFLICT (plan_version_id, metric, period) DO NOTHING;

INSERT INTO plan_node_pools (tenant_id, plan_version_id, pool_id)
SELECT :tenant, pv.id, np.id
FROM plan_versions pv
JOIN node_pools np
  ON np.tenant_id = pv.tenant_id AND np.code = 'default'
WHERE pv.id = '00000000-0000-7000-8000-00000000d001'
  AND pv.status = 'draft'
ON CONFLICT (plan_version_id, pool_id) DO NOTHING;

UPDATE plan_versions
SET status = 'published', frozen_at = now(), row_version = row_version + 1
WHERE id = '00000000-0000-7000-8000-00000000d001'
  AND status = 'draft';

UPDATE plans
SET current_version_id = '00000000-0000-7000-8000-00000000d001',
    status = 'active', row_version = row_version + 1
WHERE id = '00000000-0000-7000-8000-00000000c001'
  AND status = 'draft';

INSERT INTO payment_providers
  (id, tenant_id, code, adapter, display_name,
   supported_currencies, enabled, accepting_new)
VALUES
  ('00000000-0000-7000-8000-00000000f001', :tenant,
   'demo', 'demo_hmac', '演示支付渠道',
   ARRAY['USD']::app.currency_code[], true, true)
ON CONFLICT (tenant_id, code) DO UPDATE
SET enabled = true, accepting_new = true;

COMMIT;

\echo 'Demo catalog is ready:'
SELECT '  plan ' || p.code || ' (' || p.id || ') version v' || pv.version AS info
FROM plans p
JOIN plan_versions pv ON pv.id = p.current_version_id
WHERE p.tenant_id = '00000000-0000-7000-8000-000000000001';

SELECT '  price ' || id || '  ' || unit_amount || ' ' || currency || '/' || billing_interval AS info
FROM prices
WHERE tenant_id = '00000000-0000-7000-8000-000000000001'
ORDER BY unit_amount;

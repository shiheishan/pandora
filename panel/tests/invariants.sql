-- AegisPanel 数据层不变量测试。
--
-- 每个用例都对应 PRD 里一条「验收标准」，做法统一是：
--   执行一段**应当被拒绝**的 SQL，断言它确实被拒绝；
--   以及执行一段**应当被接受**的 SQL，断言它确实通过。
--
-- 全程在一个事务里跑，最后 ROLLBACK，不留任何数据。
-- 用法：psql -f invariants.sql

\set ON_ERROR_STOP on
\timing off
\pset pager off

BEGIN;

SET LOCAL app.tenant_id = '00000000-0000-7000-8000-000000000001';

--------------------------------------------------------------------------------
-- 测试框架
--------------------------------------------------------------------------------

CREATE TEMP TABLE _results (
  seq      serial,
  req      text,     -- PRD 需求编号
  name     text,
  expect   text,     -- 'reject' 或 'accept'
  passed   boolean,
  detail   text
) ON COMMIT DROP;

-- 断言一段 SQL 会被拒绝
CREATE OR REPLACE FUNCTION _reject(p_req text, p_name text, p_sql text)
RETURNS void LANGUAGE plpgsql AS $fn$
DECLARE v_err text;
BEGIN
  BEGIN
    EXECUTE p_sql;
    -- 账本配平是 DEFERRABLE 约束，需强制立即求值才能在此处捕获
    SET CONSTRAINTS ALL IMMEDIATE;
    INSERT INTO _results (req, name, expect, passed, detail)
      VALUES (p_req, p_name, 'reject', false, '未被拒绝 —— 约束失效');
  EXCEPTION WHEN OTHERS THEN
    v_err := regexp_replace(SQLERRM, E'\n.*', '');
    INSERT INTO _results (req, name, expect, passed, detail)
      VALUES (p_req, p_name, 'reject', true, left(v_err, 110));
  END;
  SET CONSTRAINTS ALL DEFERRED;
END $fn$;

-- 断言一段 SQL 会被接受
CREATE OR REPLACE FUNCTION _accept(p_req text, p_name text, p_sql text)
RETURNS void LANGUAGE plpgsql AS $fn$
DECLARE v_err text;
BEGIN
  BEGIN
    EXECUTE p_sql;
    SET CONSTRAINTS ALL IMMEDIATE;
    INSERT INTO _results (req, name, expect, passed, detail)
      VALUES (p_req, p_name, 'accept', true, 'ok');
  EXCEPTION WHEN OTHERS THEN
    v_err := regexp_replace(SQLERRM, E'\n.*', '');
    INSERT INTO _results (req, name, expect, passed, detail)
      VALUES (p_req, p_name, 'accept', false, left(v_err, 110));
  END;
  SET CONSTRAINTS ALL DEFERRED;
END $fn$;

--------------------------------------------------------------------------------
-- 种子数据
--------------------------------------------------------------------------------

-- 第二个租户，用于跨租户隔离测试
INSERT INTO tenants (id, slug, display_name)
VALUES ('00000000-0000-7000-8000-0000000000ff', 'other', 'Other Tenant');

INSERT INTO users (id, tenant_id, email, display_name, status)
VALUES ('00000000-0000-7000-8000-000000000101',
        '00000000-0000-7000-8000-000000000001', 'alice@example.test', 'Alice', 'active'),
       ('00000000-0000-7000-8000-000000000102',
        '00000000-0000-7000-8000-000000000001', 'bob@example.test', 'Bob', 'active');

-- 另一租户的用户（RLS 测试目标）
SET LOCAL app.tenant_id = '00000000-0000-7000-8000-0000000000ff';
INSERT INTO users (id, tenant_id, email, display_name, status)
VALUES ('00000000-0000-7000-8000-0000000001ff',
        '00000000-0000-7000-8000-0000000000ff', 'eve@other.test', 'Eve', 'active');
SET LOCAL app.tenant_id = '00000000-0000-7000-8000-000000000001';

-- 账本科目
INSERT INTO ledger_accounts (id, tenant_id, account_type, normal_balance, currency, owner_user_id)
VALUES ('00000000-0000-7000-8000-000000000201',
        '00000000-0000-7000-8000-000000000001', 'user_balance', 'credit', 'USD',
        '00000000-0000-7000-8000-000000000101');
-- owner_ref 一律加 inv-test- 前缀：平台级账户按
-- (tenant_id, account_type, owner_ref, currency) 唯一，
-- 若沿用 'main'/'stripe' 这类真实命名，本测试会与 e2e 或生产数据撞唯一约束。
-- 事务最终 ROLLBACK，这些账户不会留存。
INSERT INTO ledger_accounts (id, tenant_id, account_type, normal_balance, currency, owner_ref)
VALUES ('00000000-0000-7000-8000-000000000202',
        '00000000-0000-7000-8000-000000000001', 'channel_cash', 'debit', 'USD', 'inv-test-channel'),
       ('00000000-0000-7000-8000-000000000203',
        '00000000-0000-7000-8000-000000000001', 'platform_revenue', 'credit', 'USD', 'inv-test-revenue'),
       ('00000000-0000-7000-8000-000000000204',
        '00000000-0000-7000-8000-000000000001', 'user_balance', 'credit', 'EUR', 'inv-test-eur');

INSERT INTO ledger_transactions (id, tenant_id, kind, currency)
VALUES ('00000000-0000-7000-8000-000000000301',
        '00000000-0000-7000-8000-000000000001', 'order_paid', 'USD'),
       ('00000000-0000-7000-8000-000000000302',
        '00000000-0000-7000-8000-000000000001', 'order_paid', 'USD'),
       ('00000000-0000-7000-8000-000000000303',
        '00000000-0000-7000-8000-000000000001', 'order_paid', 'USD');

-- 商品 / 套餐 / 版本
INSERT INTO products (id, tenant_id, code, name, status)
VALUES ('00000000-0000-7000-8000-000000000401',
        '00000000-0000-7000-8000-000000000001', 'inv-test-product', '测试商品', 'active');

INSERT INTO plans (id, tenant_id, product_id, code, name, status)
VALUES ('00000000-0000-7000-8000-000000000501',
        '00000000-0000-7000-8000-000000000001',
        '00000000-0000-7000-8000-000000000401', 'inv-test-plan', '测试套餐', 'active');

INSERT INTO plan_versions (id, tenant_id, plan_id, version)
VALUES ('00000000-0000-7000-8000-000000000601',
        '00000000-0000-7000-8000-000000000001',
        '00000000-0000-7000-8000-000000000501', 1),
       ('00000000-0000-7000-8000-000000000602',
        '00000000-0000-7000-8000-000000000001',
        '00000000-0000-7000-8000-000000000501', 2);

INSERT INTO subscriptions (id, tenant_id, user_id, plan_id, plan_version_id,
                           status, snapshot_currency, snapshot_amount)
VALUES ('00000000-0000-7000-8000-000000000701',
        '00000000-0000-7000-8000-000000000001',
        '00000000-0000-7000-8000-000000000101',
        '00000000-0000-7000-8000-000000000501',
        '00000000-0000-7000-8000-000000000601', 'pending', 'USD', 999),
       ('00000000-0000-7000-8000-000000000702',
        '00000000-0000-7000-8000-000000000001',
        '00000000-0000-7000-8000-000000000101',
        '00000000-0000-7000-8000-000000000501',
        '00000000-0000-7000-8000-000000000601', 'pending', 'USD', 999);

-- 节点
INSERT INTO node_pools (id, tenant_id, code, name)
VALUES ('00000000-0000-7000-8000-000000000801',
        '00000000-0000-7000-8000-000000000001', 'inv-test-pool', '测试池');

INSERT INTO nodes (id, tenant_id, name, pool_id, status)
VALUES ('00000000-0000-7000-8000-000000000901',
        '00000000-0000-7000-8000-000000000001', 'inv-test-node-a',
        '00000000-0000-7000-8000-000000000801', 'standby'),
       ('00000000-0000-7000-8000-000000000902',
        '00000000-0000-7000-8000-000000000001', 'inv-test-node-b',
        '00000000-0000-7000-8000-000000000801', 'standby');

-- 审批
INSERT INTO approval_requests (id, tenant_id, action_type, reason, requested_by, required_approvals)
VALUES ('00000000-0000-7000-8000-000000000a01',
        '00000000-0000-7000-8000-000000000001', 'refund',
        '用户申请全额退款', '00000000-0000-7000-8000-000000000101', 2);

-- 审计（用于追加写测试）
INSERT INTO audit_events (id, tenant_id, actor_kind, action, outcome)
VALUES ('00000000-0000-7000-8000-000000000b01',
        '00000000-0000-7000-8000-000000000001', 'admin', 'test.seed', 'success');

SET CONSTRAINTS ALL DEFERRED;

--------------------------------------------------------------------------------
-- PAY-005 复式记账
--------------------------------------------------------------------------------

SELECT _reject('PAY-005', '借贷不平的账务交易必须回滚', $sql$
  INSERT INTO ledger_entries (tenant_id, transaction_id, account_id, direction, amount, currency)
  VALUES ('00000000-0000-7000-8000-000000000001',
          '00000000-0000-7000-8000-000000000301',
          '00000000-0000-7000-8000-000000000202', 'debit', 1000, 'USD');
$sql$);

SELECT _reject('PAY-005', '同一交易内混用币种必须回滚', $sql$
  INSERT INTO ledger_entries (tenant_id, transaction_id, account_id, direction, amount, currency)
  VALUES ('00000000-0000-7000-8000-000000000001',
          '00000000-0000-7000-8000-000000000302',
          '00000000-0000-7000-8000-000000000202', 'debit', 1000, 'USD'),
         ('00000000-0000-7000-8000-000000000001',
          '00000000-0000-7000-8000-000000000302',
          '00000000-0000-7000-8000-000000000204', 'credit', 1000, 'EUR');
$sql$);

SELECT _accept('PAY-005', '配平的账务交易应当通过', $sql$
  INSERT INTO ledger_entries (tenant_id, transaction_id, account_id, direction, amount, currency)
  VALUES ('00000000-0000-7000-8000-000000000001',
          '00000000-0000-7000-8000-000000000303',
          '00000000-0000-7000-8000-000000000202', 'debit', 1000, 'USD'),
         ('00000000-0000-7000-8000-000000000001',
          '00000000-0000-7000-8000-000000000303',
          '00000000-0000-7000-8000-000000000203', 'credit', 1000, 'USD');
$sql$);

-- 余额缓存必须与分录求和一致
DO $$
DECLARE v_drift bigint;
BEGIN
  SELECT drift INTO v_drift
  FROM app.verify_ledger_account('00000000-0000-7000-8000-000000000202');
  INSERT INTO _results (req, name, expect, passed, detail)
  VALUES ('PAY-005', '账户缓存余额与分录求和一致', 'accept', v_drift = 0,
          format('drift=%s', v_drift));
END $$;

--------------------------------------------------------------------------------
-- DATA-003 追加写
--------------------------------------------------------------------------------

SELECT _reject('DATA-003', 'ledger_entries 不可 UPDATE', $sql$
  UPDATE ledger_entries SET amount = 1 WHERE transaction_id = '00000000-0000-7000-8000-000000000303';
$sql$);

SELECT _reject('DATA-003', 'ledger_entries 不可 DELETE', $sql$
  DELETE FROM ledger_entries WHERE transaction_id = '00000000-0000-7000-8000-000000000303';
$sql$);

SELECT _reject('SEC-012', 'audit_events 不可 UPDATE（审计不可覆盖）', $sql$
  UPDATE audit_events SET action = 'tampered' WHERE id = '00000000-0000-7000-8000-000000000b01';
$sql$);

SELECT _reject('SEC-012', 'audit_events 不可 DELETE（审计不可删除）', $sql$
  DELETE FROM audit_events WHERE id = '00000000-0000-7000-8000-000000000b01';
$sql$);

--------------------------------------------------------------------------------
-- SUB-004 订阅状态机
--------------------------------------------------------------------------------

SELECT _reject('SUB-004', '非法跳转 pending -> grace 必须拒绝', $sql$
  UPDATE subscriptions SET status = 'grace' WHERE id = '00000000-0000-7000-8000-000000000701';
$sql$);

SELECT _reject('SUB-004', '非法跳转 pending -> past_due 必须拒绝', $sql$
  UPDATE subscriptions SET status = 'past_due' WHERE id = '00000000-0000-7000-8000-000000000701';
$sql$);

SELECT _accept('SUB-004', '合法跳转 pending -> active 应当通过', $sql$
  UPDATE subscriptions SET status = 'active' WHERE id = '00000000-0000-7000-8000-000000000701';
$sql$);

SELECT _accept('SUB-004', '合法链路 active -> past_due -> grace 应当通过', $sql$
  UPDATE subscriptions SET status = 'past_due' WHERE id = '00000000-0000-7000-8000-000000000701';
  UPDATE subscriptions SET status = 'grace' WHERE id = '00000000-0000-7000-8000-000000000701';
$sql$);

--------------------------------------------------------------------------------
-- NODE-010 节点不得从创建中直达 active
--------------------------------------------------------------------------------

SELECT _reject('NODE-010', 'standby -> active 直达必须拒绝', $sql$
  UPDATE nodes SET status = 'active' WHERE id = '00000000-0000-7000-8000-000000000901';
$sql$);

SELECT _reject('NODE-010', 'draft -> active 直达必须拒绝', $sql$
  UPDATE nodes SET status = 'draft' WHERE id = '00000000-0000-7000-8000-000000000902';
  UPDATE nodes SET status = 'active' WHERE id = '00000000-0000-7000-8000-000000000902';
$sql$);

SELECT _accept('NODE-010', 'standby -> canary -> active 应当通过', $sql$
  UPDATE nodes SET status = 'canary' WHERE id = '00000000-0000-7000-8000-000000000901';
  UPDATE nodes SET status = 'active' WHERE id = '00000000-0000-7000-8000-000000000901';
$sql$);

--------------------------------------------------------------------------------
-- SEC-013 双人审批
--------------------------------------------------------------------------------

SELECT _reject('SEC-013', '申请人不能审批自己的申请', $sql$
  INSERT INTO approval_decisions (tenant_id, request_id, decided_by, decision)
  VALUES ('00000000-0000-7000-8000-000000000001',
          '00000000-0000-7000-8000-000000000a01',
          '00000000-0000-7000-8000-000000000101', 'approve');
$sql$);

SELECT _accept('SEC-013', '他人审批应当通过', $sql$
  INSERT INTO approval_decisions (tenant_id, request_id, decided_by, decision)
  VALUES ('00000000-0000-7000-8000-000000000001',
          '00000000-0000-7000-8000-000000000a01',
          '00000000-0000-7000-8000-000000000102', 'approve');
$sql$);

SELECT _reject('SEC-013', '同一人不能重复表态', $sql$
  INSERT INTO approval_decisions (tenant_id, request_id, decided_by, decision)
  VALUES ('00000000-0000-7000-8000-000000000001',
          '00000000-0000-7000-8000-000000000a01',
          '00000000-0000-7000-8000-000000000102', 'reject');
$sql$);

--------------------------------------------------------------------------------
-- SUB-002 套餐版本冻结
--------------------------------------------------------------------------------

UPDATE plan_versions SET frozen_at = now() WHERE id = '00000000-0000-7000-8000-000000000602';

SELECT _reject('SUB-002', '已冻结的套餐版本不可修改', $sql$
  UPDATE plan_versions SET grace_period_hours = 48
   WHERE id = '00000000-0000-7000-8000-000000000602';
$sql$);

--------------------------------------------------------------------------------
-- PAY-001 订单金额恒等式
--------------------------------------------------------------------------------

SELECT _reject('PAY-001', '订单金额恒等式被破坏时必须拒绝', $sql$
  INSERT INTO orders (tenant_id, order_no, user_id, currency,
                      subtotal_amount, discount_amount, tax_amount,
                      total_amount, balance_applied, payable_amount)
  VALUES ('00000000-0000-7000-8000-000000000001', 'BAD-001',
          '00000000-0000-7000-8000-000000000101', 'USD',
          1000, 100, 0, 999, 0, 999);
$sql$);

SELECT _accept('PAY-001', '金额自洽的订单应当通过', $sql$
  INSERT INTO orders (tenant_id, order_no, user_id, currency,
                      subtotal_amount, discount_amount, tax_amount,
                      total_amount, balance_applied, payable_amount)
  VALUES ('00000000-0000-7000-8000-000000000001', 'GOOD-001',
          '00000000-0000-7000-8000-000000000101', 'USD',
          1000, 100, 50, 950, 200, 750);
$sql$);

-- D-E-2（00071）：剩余价值折算只属于变更套餐单，且折完 total 不为负
SELECT _reject('D-E-2', '非变更套餐单不能带剩余价值折算', $sql$
  INSERT INTO orders (tenant_id, order_no, user_id, currency,
                      subtotal_amount, discount_amount, tax_amount, proration_credit_amount,
                      total_amount, balance_applied, payable_amount)
  VALUES ('00000000-0000-7000-8000-000000000001', 'BAD-PRORATE-001',
          '00000000-0000-7000-8000-000000000101', 'USD',
          1000, 0, 0, 300, 700, 0, 700);
$sql$);

SELECT _reject('XBD-015', '人工订单缺少原因时必须拒绝', $sql$
  INSERT INTO orders (tenant_id, order_no, user_id, currency, kind,
                      subtotal_amount, total_amount, payable_amount)
  VALUES ('00000000-0000-7000-8000-000000000001', 'MANUAL-001',
          '00000000-0000-7000-8000-000000000101', 'USD', 'manual', 0, 0, 0);
$sql$);

--------------------------------------------------------------------------------
-- SUB-007 试用防刷
--------------------------------------------------------------------------------

SELECT _accept('SUB-007', '首次领取试用应当通过', $sql$
  INSERT INTO trial_grants (tenant_id, plan_id, user_id)
  VALUES ('00000000-0000-7000-8000-000000000001',
          '00000000-0000-7000-8000-000000000501',
          '00000000-0000-7000-8000-000000000101');
$sql$);

SELECT _reject('SUB-007', '同一用户重复领取同一套餐试用必须拒绝', $sql$
  INSERT INTO trial_grants (tenant_id, plan_id, user_id)
  VALUES ('00000000-0000-7000-8000-000000000001',
          '00000000-0000-7000-8000-000000000501',
          '00000000-0000-7000-8000-000000000101');
$sql$);

SELECT _accept('SUB-007', '换用户但同设备指纹：首次应通过', $sql$
  INSERT INTO trial_grants (tenant_id, plan_id, user_id, device_fingerprint_hash)
  VALUES ('00000000-0000-7000-8000-000000000001',
          '00000000-0000-7000-8000-000000000501',
          '00000000-0000-7000-8000-000000000102', '\x0badc0de');
$sql$);

SELECT _reject('SUB-007', '同设备指纹再次领取必须拒绝（防重复注册刷试用）', $sql$
  INSERT INTO trial_grants (tenant_id, plan_id, device_fingerprint_hash)
  VALUES ('00000000-0000-7000-8000-000000000001',
          '00000000-0000-7000-8000-000000000501', '\x0badc0de');
$sql$);

--------------------------------------------------------------------------------
-- MKT-003 邀请关系不可变
--------------------------------------------------------------------------------

SELECT _accept('MKT-003', '首次绑定邀请关系应当通过', $sql$
  INSERT INTO referrals (tenant_id, referee_user_id, referrer_user_id)
  VALUES ('00000000-0000-7000-8000-000000000001',
          '00000000-0000-7000-8000-000000000102',
          '00000000-0000-7000-8000-000000000101');
$sql$);

SELECT _reject('MKT-003', '更换邀请上级必须拒绝', $sql$
  UPDATE referrals SET referrer_user_id = '00000000-0000-7000-8000-000000000102'
   WHERE referee_user_id = '00000000-0000-7000-8000-000000000102';
$sql$);

SELECT _reject('MKT-003', '自我邀请必须拒绝', $sql$
  INSERT INTO referrals (tenant_id, referee_user_id, referrer_user_id)
  VALUES ('00000000-0000-7000-8000-000000000001',
          '00000000-0000-7000-8000-000000000101',
          '00000000-0000-7000-8000-000000000101');
$sql$);

--------------------------------------------------------------------------------
-- PAY-003 幂等
--------------------------------------------------------------------------------

SELECT _accept('PAY-003', '首次写入幂等键应当通过', $sql$
  INSERT INTO idempotency_keys
    (tenant_id, scope, idempotency_key, request_hash, locked_until)
  VALUES ('00000000-0000-7000-8000-000000000001', 'payment_webhook', 'evt_123',
          '\xaa', now()+interval '5 minutes');
$sql$);

SELECT _reject('PAY-003', '相同作用域下重复幂等键必须拒绝', $sql$
  INSERT INTO idempotency_keys
    (tenant_id, scope, idempotency_key, request_hash, locked_until)
  VALUES ('00000000-0000-7000-8000-000000000001', 'payment_webhook', 'evt_123',
          '\xbb', now()+interval '5 minutes');
$sql$);

--------------------------------------------------------------------------------
-- MKT-001 优惠券不超发
--------------------------------------------------------------------------------

SELECT _accept('MKT-001', '券在限额内应当可用', $sql$
  INSERT INTO coupons (id, tenant_id, code, name, discount_type, discount_value,
                       currency, max_redemptions, redeemed_count)
  VALUES ('00000000-0000-7000-8000-000000000c01',
          '00000000-0000-7000-8000-000000000001', 'INVTEST10', '立减', 'fixed', 1000, 'USD', 5, 5);
$sql$);

SELECT _reject('MKT-001', '兑换数超过上限必须拒绝', $sql$
  UPDATE coupons SET redeemed_count = 6 WHERE id = '00000000-0000-7000-8000-000000000c01';
$sql$);

SELECT _reject('MKT-001', '百分比折扣超过 100% 必须拒绝', $sql$
  INSERT INTO coupons (tenant_id, code, name, discount_type, discount_value)
  VALUES ('00000000-0000-7000-8000-000000000001', 'INVTESTBAD', '错误券', 'percent', 10001);
$sql$);

--------------------------------------------------------------------------------
-- NFR-008 essential 降级开关不可关闭
--------------------------------------------------------------------------------

SELECT _reject('NFR-008', 'essential 开关不可被关闭（登录/续费/配置同步）', $sql$
  UPDATE feature_switches SET enabled = false, reason = '演练'
   WHERE code = 'auth.login' AND tenant_id = '00000000-0000-7000-8000-000000000001';
$sql$);

SELECT _accept('NFR-008', '非 essential 开关可关闭（需填原因）', $sql$
  UPDATE feature_switches SET enabled = false, reason = '遭遇注册洪峰，临时关闭'
   WHERE code = 'auth.registration' AND tenant_id = '00000000-0000-7000-8000-000000000001';
$sql$);

SELECT _reject('NFR-008', '关闭开关但不填原因必须拒绝', $sql$
  UPDATE feature_switches SET enabled = false, reason = NULL
   WHERE code = 'ops.reports' AND tenant_id = '00000000-0000-7000-8000-000000000001';
$sql$);

--------------------------------------------------------------------------------
-- EXT-006 / SEC-014 插件未验签不可启用
--------------------------------------------------------------------------------

SELECT _reject('SEC-014', '未验签插件不可置为 enabled', $sql$
  INSERT INTO plugins (tenant_id, plugin_key, version, kind, display_name,
                       api_min_version, status, signature_verified)
  VALUES ('00000000-0000-7000-8000-000000000001', 'evil', '1.0', 'payment',
          '未签名插件', 'v1', 'enabled', false);
$sql$);

SELECT _reject('EXT-006', '未验签插件不可进程内运行', $sql$
  INSERT INTO plugins (tenant_id, plugin_key, version, kind, display_name,
                       api_min_version, isolation, signature_verified)
  VALUES ('00000000-0000-7000-8000-000000000001', 'evil2', '1.0', 'payment',
          '未签名插件', 'v1', 'in_process_trusted', false);
$sql$);

--------------------------------------------------------------------------------
-- AGT-003 Agent 任务白名单
--------------------------------------------------------------------------------

SELECT _reject('AGT-003', '任意 Shell 任务类型必须被拒绝', $sql$
  INSERT INTO node_tasks (tenant_id, node_id, task_type, expires_at)
  VALUES ('00000000-0000-7000-8000-000000000001',
          '00000000-0000-7000-8000-000000000901', 'shell.exec', now() + interval '5 min');
$sql$);

SELECT _accept('AGT-003', '白名单内任务类型应当通过', $sql$
  INSERT INTO node_tasks (tenant_id, node_id, task_type, expires_at)
  VALUES ('00000000-0000-7000-8000-000000000001',
          '00000000-0000-7000-8000-000000000901', 'health.check', now() + interval '5 min');
$sql$);

--------------------------------------------------------------------------------
-- AGT-007 已发布配置必须带签名
--------------------------------------------------------------------------------

SELECT _reject('AGT-007', '无签名的配置不可发布', $sql$
  INSERT INTO node_configs (tenant_id, scope, version, payload, content_hash, status)
  VALUES ('00000000-0000-7000-8000-000000000001', 'global', 1,
          '{}'::jsonb, '\xaa', 'published');
$sql$);

SELECT _accept('AGT-007', '带签名与有效期的配置可发布', $sql$
  INSERT INTO node_configs (tenant_id, scope, version, payload, content_hash,
                            signature, signature_expires_at, status)
  VALUES ('00000000-0000-7000-8000-000000000001', 'global', 2,
          '{}'::jsonb, '\xbb', '\xcc', now() + interval '1 day', 'published');
$sql$);

--------------------------------------------------------------------------------
-- OPS-005 事务类通知不可退订
--------------------------------------------------------------------------------

SELECT _reject('OPS-005', '事务类通知不可被关闭（安全通知必达）', $sql$
  INSERT INTO notification_preferences (tenant_id, user_id, category, channel, enabled)
  VALUES ('00000000-0000-7000-8000-000000000001',
          '00000000-0000-7000-8000-000000000101', 'transactional', 'email', false);
$sql$);

SELECT _accept('OPS-005', '营销类通知可退订', $sql$
  INSERT INTO notification_preferences (tenant_id, user_id, category, channel, enabled)
  VALUES ('00000000-0000-7000-8000-000000000001',
          '00000000-0000-7000-8000-000000000101', 'marketing', 'email', false);
$sql$);

--------------------------------------------------------------------------------
-- OPS-001 内部备注只能由客服/系统发出
--------------------------------------------------------------------------------

INSERT INTO tickets (id, tenant_id, ticket_no, user_id, subject)
VALUES ('00000000-0000-7000-8000-000000000d01',
        '00000000-0000-7000-8000-000000000001', 'INV-T-0001',
        '00000000-0000-7000-8000-000000000101', '无法连接');

SELECT _reject('OPS-001', '用户身份不能写内部备注', $sql$
  INSERT INTO ticket_messages (tenant_id, ticket_id, author_kind, body, internal_note)
  VALUES ('00000000-0000-7000-8000-000000000001',
          '00000000-0000-7000-8000-000000000d01', 'user', '偷看内部备注', true);
$sql$);

--------------------------------------------------------------------------------
-- USE-002 用量批次去重
--------------------------------------------------------------------------------

INSERT INTO usage_sources (id, tenant_id, kind, node_id)
VALUES ('00000000-0000-7000-8000-000000000e01',
        '00000000-0000-7000-8000-000000000001', 'node',
        '00000000-0000-7000-8000-000000000901');

SELECT _accept('USE-002', '首次提交批次应当通过', $sql$
  INSERT INTO usage_batches (tenant_id, source_id, batch_sequence, content_hash,
                             signature, window_start, window_end)
  VALUES ('00000000-0000-7000-8000-000000000001',
          '00000000-0000-7000-8000-000000000e01', 1, '\xaa', '\xbb',
          now() - interval '1 hour', now());
$sql$);

SELECT _reject('USE-002', '相同序号的批次重传必须拒绝', $sql$
  INSERT INTO usage_batches (tenant_id, source_id, batch_sequence, content_hash,
                             signature, window_start, window_end)
  VALUES ('00000000-0000-7000-8000-000000000001',
          '00000000-0000-7000-8000-000000000e01', 1, '\xcc', '\xdd',
          now() - interval '1 hour', now());
$sql$);

--------------------------------------------------------------------------------
-- NODE-005 开通幂等
--------------------------------------------------------------------------------

SELECT _accept('NODE-005', '首次开通请求应当通过', $sql$
  INSERT INTO provisioning_runs (tenant_id, idempotency_key, mode)
  VALUES ('00000000-0000-7000-8000-000000000001', 'prov-key-1', 'auto_create');
$sql$);

SELECT _reject('NODE-005', '重复点击（相同幂等键）必须拒绝', $sql$
  INSERT INTO provisioning_runs (tenant_id, idempotency_key, mode)
  VALUES ('00000000-0000-7000-8000-000000000001', 'prov-key-1', 'auto_create');
$sql$);

--------------------------------------------------------------------------------
-- DATA-002 / ARC-006 租户隔离（RLS）
--   必须以非 superuser 角色验证 —— superuser 隐含 BYPASSRLS。
--------------------------------------------------------------------------------

DO $$
DECLARE
  v_visible int;
  v_total   int;
BEGIN
  -- 以 superuser 视角：能看到两个租户的全部用户
  SELECT count(*) INTO v_total FROM users;

  SET LOCAL ROLE aegis_app;
  -- 当前会话租户为 default，另一租户的 Eve 必须不可见
  SELECT count(*) INTO v_visible
  FROM users WHERE id = '00000000-0000-7000-8000-0000000001ff';
  RESET ROLE;

  INSERT INTO _results (req, name, expect, passed, detail)
  VALUES ('DATA-002', '应用角色下跨租户用户不可见', 'accept', v_visible = 0,
          format('superuser可见%s行，aegis_app可见跨租户%s行', v_total, v_visible));
END $$;

DO $$
DECLARE v_err text;
BEGIN
  BEGIN
    SET LOCAL ROLE aegis_app;
    -- 试图往另一个租户写数据：WITH CHECK 应当拒绝
    INSERT INTO users (tenant_id, email, display_name)
    VALUES ('00000000-0000-7000-8000-0000000000ff', 'mallory@other.test', 'Mallory');
    RESET ROLE;
    INSERT INTO _results (req, name, expect, passed, detail)
    VALUES ('ARC-006', '应用角色不能跨租户写入', 'reject', false, '未被拒绝 —— RLS 失效');
  EXCEPTION WHEN OTHERS THEN
    v_err := regexp_replace(SQLERRM, E'\n.*', '');
    RESET ROLE;
    INSERT INTO _results (req, name, expect, passed, detail)
    VALUES ('ARC-006', '应用角色不能跨租户写入', 'reject', true, left(v_err, 110));
  END;
END $$;

DO $$
DECLARE v_err text;
BEGIN
  BEGIN
    SET LOCAL ROLE aegis_app;
    UPDATE audit_events SET action = 'tampered'
     WHERE id = '00000000-0000-7000-8000-000000000b01';
    RESET ROLE;
    INSERT INTO _results (req, name, expect, passed, detail)
    VALUES ('SEC-012', '应用角色对审计表无 UPDATE 权限', 'reject', false, '未被拒绝');
  EXCEPTION WHEN OTHERS THEN
    v_err := regexp_replace(SQLERRM, E'\n.*', '');
    RESET ROLE;
    INSERT INTO _results (req, name, expect, passed, detail)
    VALUES ('SEC-012', '应用角色对审计表无 UPDATE 权限', 'reject', true, left(v_err, 110));
  END;
END $$;

DO $$
DECLARE v_err text;
BEGIN
  BEGIN
    SET LOCAL ROLE aegis_app;
    -- 试图给状态机加一条非法边来「合法化」违规跳转
    INSERT INTO subscription_transitions (from_status, to_status) VALUES ('pending', 'grace');
    RESET ROLE;
    INSERT INTO _results (req, name, expect, passed, detail)
    VALUES ('SUB-004', '应用角色不能篡改状态机转换表', 'reject', false, '未被拒绝');
  EXCEPTION WHEN OTHERS THEN
    v_err := regexp_replace(SQLERRM, E'\n.*', '');
    RESET ROLE;
    INSERT INTO _results (req, name, expect, passed, detail)
    VALUES ('SUB-004', '应用角色不能篡改状态机转换表', 'reject', true, left(v_err, 110));
  END;
END $$;

--------------------------------------------------------------------------------
-- 汇总
--------------------------------------------------------------------------------

\echo ''
\echo '================================ 不变量测试结果 ================================'

SELECT
  lpad(seq::text, 2) AS "#",
  rpad(req, 9)       AS "需求",
  rpad(expect, 6)    AS "期望",
  CASE WHEN passed THEN 'PASS' ELSE 'FAIL' END AS "结果",
  name               AS "用例"
FROM _results ORDER BY seq;

\echo ''

SELECT
  count(*)                                  AS "总数",
  count(*) FILTER (WHERE passed)            AS "通过",
  count(*) FILTER (WHERE NOT passed)        AS "失败"
FROM _results;

\echo ''
\echo '--- 失败详情（若有）---'
SELECT req AS "需求", name AS "用例", detail AS "原因"
FROM _results WHERE NOT passed ORDER BY seq;

-- CI 门禁：有失败就抛异常，使 psql -v ON_ERROR_STOP=1 返回非零。
-- 注意不能写成 `SELECT CASE WHEN count(*)>0 THEN 1/0 ELSE 0 END` ——
-- PostgreSQL 会在计划期对常量表达式 1/0 做折叠，即使一条都没失败也会报错。
DO $$
DECLARE v_failed int;
BEGIN
  SELECT count(*) INTO v_failed FROM _results WHERE NOT passed;
  IF v_failed > 0 THEN
    RAISE EXCEPTION '不变量测试有 % 项失败', v_failed;
  END IF;
  RAISE NOTICE '全部不变量测试通过';
END $$;

ROLLBACK;

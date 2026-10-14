-- Pandora 数据层不变量测试（panel/db/migrations）。
--
-- 每个用例对应一条验收标准：应当被拒绝的 SQL 断言被拒；应当被接受的 SQL 断言通过。
-- 全程单事务，末尾 ROLLBACK，不留数据。CI：`"$PSQL" -X -v ON_ERROR_STOP=1 -f tests/invariants.sql`
-- 库已跑迁移且含其它 e2e 夹具；本脚本种子用固定虚构 uuid（00000000-0000-7000-8000-0000000001xx）
-- 与 inv- 前缀邮箱/编码，避免与已有行撞唯一约束。
--
-- 覆盖的验收项：PAY-005 复式记账、DATA-003 追加写、SUB-004 订阅状态（只剩 active / expired）、
--   NODE-010（改成 nodes_active_shape：在役节点必须挂服务器、协议配好）、NODE-011 同机端口兜底（tcp / udp / tcp+udp）、SUB-002 版本与价格冻结、
--   PAY-001 订单金额恒等式与人工单理由、MKT-003 邀请关系、PAY-003 幂等唯一键、MKT-001 优惠券容量、
--   NFR-008 降级开关、OPS-005 事务类通知、OPS-001 内部备注、USE-002（改成 node_traffic_reports 唯一键）、
--   PAY-FEE 渠道类型与用户手续费（00042）。
--
-- 不覆盖的验收项及原因（没有对应的表或机制）：
--   SEC-013 双人审批 — 没有审批表：后台权限只分有和没有。
--   SUB-007 试用防刷 — 没有试用发放表（价格上的 trial_days 只存不用）。
--   EXT-006 / SEC-014 插件验签 — 插件只有 plugin_hooks Webhook，没有可执行插件与签名。
--   AGT-003 节点任务白名单 — 没有节点任务表。
--   AGT-007 已发布配置签名 — 节点通道改 gRPC + mTLS，没有配置签名表。
--   NODE-005 开通幂等 — 没有开通流水表。
--   DATA-002 / ARC-006 租户隔离与应用角色 — 单租户、没有 RLS，迁移不建应用角色。
--   PAY-005 余额缓存与分录一致（app.verify_ledger_*）— 没有这组库函数，余额缓存由 Go 维护；
--     漂移由 e2e.sh / epay_e2e.sh 的漂移 SQL 与后台 /v1/overview 的 ledger_drift_accounts 检查。
--   订阅的 pending / grace / past_due 状态与节点 status 流水线 — 订阅只有 active / expired，节点没有 status 流水线。

\set ON_ERROR_STOP on
\timing off
\pset pager off

BEGIN;

--------------------------------------------------------------------------------
-- 测试框架
--------------------------------------------------------------------------------

CREATE TEMP TABLE _results (
  seq      serial,
  req      text,
  name     text,
  expect   text,
  passed   boolean,
  detail   text
) ON COMMIT DROP;

CREATE OR REPLACE FUNCTION _reject(p_req text, p_name text, p_sql text)
RETURNS void LANGUAGE plpgsql AS $fn$
DECLARE v_err text;
BEGIN
  BEGIN
    EXECUTE p_sql;
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
-- 种子数据（固定虚构 uuid；不插账务交易：它的配平是延迟约束，留在外层会在每次
-- SET CONSTRAINTS ALL IMMEDIATE 时被一起检查。账务交易都在各用例的语句里现建）
--------------------------------------------------------------------------------

INSERT INTO users (id, email, display_name, status)
VALUES ('00000000-0000-7000-8000-000000000101', 'inv-alice@example.test', 'Inv Alice', 'active'),
       ('00000000-0000-7000-8000-000000000102', 'inv-bob@example.test', 'Inv Bob', 'active'),
       ('00000000-0000-7000-8000-000000000103', 'inv-charlie@example.test', 'Inv Charlie', 'active');

-- 用户类科目挂用户、owner_ref 为空；平台类科目不挂用户、必须有 owner_ref（ledger_accounts_owner_shape）
INSERT INTO ledger_accounts (id, account_type, currency, owner_user_id, owner_ref, balance)
VALUES ('00000000-0000-7000-8000-000000000201', 'user_balance', 'USD',
        '00000000-0000-7000-8000-000000000101', '', 0.00),
       ('00000000-0000-7000-8000-000000000202', 'channel_cash', 'USD', NULL, 'inv-test-channel', 0.00),
       ('00000000-0000-7000-8000-000000000203', 'platform_revenue', 'USD', NULL, 'inv-test-revenue', 0.00),
       ('00000000-0000-7000-8000-000000000204', 'channel_cash', 'CNY', NULL, 'inv-test-cny', 0.00);

-- 套餐：版本 601 建成草稿、写好权益与配额后发布（冻结）；602 是之后的新草稿（每个套餐只许一个草稿）
INSERT INTO plans (id, code, name, status)
VALUES ('00000000-0000-7000-8000-000000000501', 'inv-test-plan', 'Inv 测试套餐', 'draft');

INSERT INTO plan_versions (id, plan_id, version, status)
VALUES ('00000000-0000-7000-8000-000000000601', '00000000-0000-7000-8000-000000000501', 1, 'draft');

INSERT INTO entitlements (id, plan_version_id, code, value)
VALUES ('00000000-0000-7000-8000-000000000611',
        '00000000-0000-7000-8000-000000000601', 'inv.feature', 'true'::jsonb);

INSERT INTO quota_definitions (id, plan_version_id, metric, limit_value, unit, period)
VALUES ('00000000-0000-7000-8000-000000000621',
        '00000000-0000-7000-8000-000000000601', 'traffic.bytes', 1073741824, 'bytes', 'cycle');

UPDATE plan_versions SET status = 'published', frozen_at = now()
 WHERE id = '00000000-0000-7000-8000-000000000601';

INSERT INTO plan_versions (id, plan_id, version, status)
VALUES ('00000000-0000-7000-8000-000000000602', '00000000-0000-7000-8000-000000000501', 2, 'draft');

INSERT INTO prices (id, plan_id, currency, unit_amount, billing_interval, interval_count, status)
VALUES ('00000000-0000-7000-8000-000000000631',
        '00000000-0000-7000-8000-000000000501', 'USD', 9.99, 'month', 1, 'active');

INSERT INTO subscriptions (id, user_id, plan_id, plan_version_id, status,
                           current_period_start, current_period_end,
                           snapshot_currency, snapshot_amount,
                           snapshot_interval, snapshot_interval_count)
VALUES ('00000000-0000-7000-8000-000000000701',
        '00000000-0000-7000-8000-000000000101',
        '00000000-0000-7000-8000-000000000501',
        '00000000-0000-7000-8000-000000000601', 'active',
        now() - interval '1 day', now() + interval '29 days',
        'USD', 9.99, 'month', 1),
       ('00000000-0000-7000-8000-000000000702',
        '00000000-0000-7000-8000-000000000102',
        '00000000-0000-7000-8000-000000000501',
        '00000000-0000-7000-8000-000000000601', 'expired',
        now() - interval '60 days', now() - interval '30 days',
        'USD', 9.99, 'month', 1);

INSERT INTO subscription_events (id, subscription_id, event_type, to_status, actor_kind)
VALUES ('00000000-0000-7000-8000-000000000c21',
        '00000000-0000-7000-8000-000000000701', 'activated', 'active', 'system');

INSERT INTO node_pools (id, code, name)
VALUES ('00000000-0000-7000-8000-000000000801', 'inv-test-pool', 'Inv 测试池');

INSERT INTO servers (id, name, status)
VALUES ('00000000-0000-7000-8000-000000000811', 'inv-test-srv', 'draft');

INSERT INTO nodes (id, name, pool_id, serving_status)
VALUES ('00000000-0000-7000-8000-000000000901', 'inv-test-node-a',
        '00000000-0000-7000-8000-000000000801', 'draft');

INSERT INTO audit_events (id, actor_kind, action, outcome)
VALUES ('00000000-0000-7000-8000-000000000b01', 'system', 'inv.test.seed', 'success');

INSERT INTO payment_providers (id, code, adapter, display_name, currencies, enabled, accepting_new)
VALUES ('00000000-0000-7000-8000-000000000b11', 'invtestpay', 'epay', 'Inv Test Pay',
        '["USD"]'::jsonb, true, false);

INSERT INTO orders (id, order_no, user_id, kind, currency,
                    subtotal_amount, discount_amount, proration_credit_amount, waived_amount,
                    total_amount, balance_applied, payable_amount, expires_at)
VALUES ('00000000-0000-7000-8000-000000000c01', 'INV-SEED-001',
        '00000000-0000-7000-8000-000000000101', 'new', 'USD',
        10.00, 0, 0, 0, 10.00, 0, 10.00, now() + interval '30 minutes');

INSERT INTO payments (id, order_id, user_id, provider_id, provider_payment_id, currency, amount)
VALUES ('00000000-0000-7000-8000-000000000c11', '00000000-0000-7000-8000-000000000c01',
        '00000000-0000-7000-8000-000000000101', '00000000-0000-7000-8000-000000000b11',
        'inv-pay-seed-1', 'USD', 10.00);

INSERT INTO payment_events (provider_id, provider_event_id, event_type, signature_verified)
VALUES ('00000000-0000-7000-8000-000000000b11', 'inv-evt-seed-1', 'payment.succeeded', true);

INSERT INTO idempotency_keys (scope, actor_id, idempotency_key, request_hash, locked_until)
VALUES ('inv_test', '00000000-0000-7000-8000-000000000101', 'inv-key-1',
        decode(repeat('aa', 32), 'hex'), now() + interval '5 minutes');

INSERT INTO coupons (id, code, name, amount_off, currency, max_redemptions, redeemed_count)
VALUES ('00000000-0000-7000-8000-000000000c31', 'INVTEST10', 'Inv 立减', 1.00, 'USD', 5, 4);

INSERT INTO tickets (id, ticket_no, user_id, subject, category, priority,
                     sla_first_response_due, sla_resolution_due)
VALUES ('00000000-0000-7000-8000-000000000d01', 'TK20260101-INVAAAAA',
        '00000000-0000-7000-8000-000000000101', 'Inv 无法连接', 'technical', 'normal',
        now() + interval '4 hours', now() + interval '24 hours');

INSERT INTO node_traffic_reports (server_id, report_id, entries, content_sha256,
                                  entry_count, upload_bytes, download_bytes, invalid_count, unlisted_count)
VALUES ('00000000-0000-7000-8000-000000000811', 'inv-report-1', '[]'::jsonb,
        decode(repeat('aa', 32), 'hex'), 0, 0, 0, 0, 0);

SET CONSTRAINTS ALL DEFERRED;

--------------------------------------------------------------------------------
-- PAY-005 复式记账（00009_ledger.sql）
-- 交易与分录在同一条语句里建（数据修改 CTE）：配平是延迟约束触发器 assert_ledger_balanced，
-- 由框架里的 SET CONSTRAINTS ALL IMMEDIATE 当场求值。
--------------------------------------------------------------------------------

-- assert_ledger_balanced：借 10.00 贷 9.00
SELECT _reject('PAY-005', '借贷不平的账务交易必须回滚', $sql$
  WITH t AS (
    INSERT INTO ledger_transactions (id, kind, currency, actor_kind)
    VALUES ('00000000-0000-7000-8000-000000000301', 'commission_accrued', 'USD', 'system')
    RETURNING id
  )
  INSERT INTO ledger_entries (transaction_id, account_id, currency, direction, amount)
  SELECT t.id, v.account_id, 'USD', v.direction, v.amount
    FROM t, (VALUES ('00000000-0000-7000-8000-000000000202'::uuid, 'debit', 10.00),
                    ('00000000-0000-7000-8000-000000000203'::uuid, 'credit', 9.00)) AS v(account_id, direction, amount);
$sql$);

-- 分录币种 = 交易币种 = 科目币种由复合外键钉住（ledger_entries_transaction_fk），
-- 混币种的交易插不进去；配平触发器里的单币种检查是第二道
SELECT _reject('PAY-005', '同一交易内混用币种必须回滚', $sql$
  WITH t AS (
    INSERT INTO ledger_transactions (id, kind, currency, actor_kind)
    VALUES ('00000000-0000-7000-8000-000000000302', 'commission_accrued', 'USD', 'system')
    RETURNING id
  )
  INSERT INTO ledger_entries (transaction_id, account_id, currency, direction, amount)
  SELECT t.id, v.account_id, v.currency, v.direction, 10.00
    FROM t, (VALUES ('00000000-0000-7000-8000-000000000202'::uuid, 'USD', 'debit'),
                    ('00000000-0000-7000-8000-000000000204'::uuid, 'CNY', 'credit')) AS v(account_id, currency, direction);
$sql$);

-- 只有交易、没有分录：提交时同样被配平触发器拦下（至少两条分录）
SELECT _reject('PAY-005', '没有分录的账务交易必须回滚', $sql$
  INSERT INTO ledger_transactions (id, kind, currency, actor_kind)
  VALUES ('00000000-0000-7000-8000-000000000304', 'commission_accrued', 'USD', 'system');
$sql$);

SELECT _accept('PAY-005', '配平的账务交易应当通过', $sql$
  WITH t AS (
    INSERT INTO ledger_transactions (id, kind, currency, actor_kind)
    VALUES ('00000000-0000-7000-8000-000000000303', 'commission_accrued', 'USD', 'system')
    RETURNING id
  )
  INSERT INTO ledger_entries (transaction_id, account_id, currency, direction, amount)
  SELECT t.id, v.account_id, 'USD', v.direction, 10.00
    FROM t, (VALUES ('00000000-0000-7000-8000-000000000202'::uuid, 'debit'),
                    ('00000000-0000-7000-8000-000000000203'::uuid, 'credit')) AS v(account_id, direction);
$sql$);

-- CHECK ledger_accounts_user_nonnegative
SELECT _reject('PAY-005', '用户余额账户不能为负', $sql$
  UPDATE ledger_accounts SET balance = -0.01
   WHERE id = '00000000-0000-7000-8000-000000000201';
$sql$);

--------------------------------------------------------------------------------
-- DATA-003 追加写（deny_mutation 语句级触发器）
--------------------------------------------------------------------------------

SELECT _reject('DATA-003', 'ledger_entries 不可 UPDATE', $sql$
  UPDATE ledger_entries SET amount = 1.00
   WHERE transaction_id = '00000000-0000-7000-8000-000000000303';
$sql$);

SELECT _reject('DATA-003', 'ledger_entries 不可 DELETE', $sql$
  DELETE FROM ledger_entries WHERE transaction_id = '00000000-0000-7000-8000-000000000303';
$sql$);

SELECT _reject('DATA-003', 'ledger_transactions 不可 UPDATE', $sql$
  UPDATE ledger_transactions SET memo = 'tampered'
   WHERE id = '00000000-0000-7000-8000-000000000303';
$sql$);

SELECT _reject('DATA-003', 'audit_events 不可 UPDATE', $sql$
  UPDATE audit_events SET action = 'tampered' WHERE id = '00000000-0000-7000-8000-000000000b01';
$sql$);

SELECT _reject('DATA-003', 'audit_events 不可 DELETE', $sql$
  DELETE FROM audit_events WHERE id = '00000000-0000-7000-8000-000000000b01';
$sql$);

SELECT _reject('DATA-003', 'payments 不可 UPDATE', $sql$
  UPDATE payments SET amount = 1.00 WHERE id = '00000000-0000-7000-8000-000000000c11';
$sql$);

SELECT _reject('DATA-003', 'payments 不可 DELETE', $sql$
  DELETE FROM payments WHERE id = '00000000-0000-7000-8000-000000000c11';
$sql$);

SELECT _reject('DATA-003', 'subscription_events 不可 UPDATE', $sql$
  UPDATE subscription_events SET event_type = 'expired'
   WHERE id = '00000000-0000-7000-8000-000000000c21';
$sql$);

SELECT _reject('DATA-003', 'subscription_events 不可 DELETE', $sql$
  DELETE FROM subscription_events WHERE id = '00000000-0000-7000-8000-000000000c21';
$sql$);

--------------------------------------------------------------------------------
-- SUB-004 订阅状态（00011_subscription.sql：只有 active / expired）
--------------------------------------------------------------------------------

-- CHECK subscriptions.status IN ('active', 'expired')
SELECT _reject('SUB-004', '非法 status pending 必须拒绝', $sql$
  INSERT INTO subscriptions (id, user_id, plan_id, plan_version_id, status,
                             current_period_start, current_period_end,
                             snapshot_currency, snapshot_amount,
                             snapshot_interval, snapshot_interval_count)
  VALUES ('00000000-0000-7000-8000-000000000703',
          '00000000-0000-7000-8000-000000000103',
          '00000000-0000-7000-8000-000000000501',
          '00000000-0000-7000-8000-000000000601', 'pending',
          now(), now() + interval '30 days',
          'USD', 9.99, 'month', 1);
$sql$);

-- CHECK subscriptions_closed_is_expired
SELECT _reject('SUB-004', 'renewal_closed_at 非空而 status=active 必须拒绝', $sql$
  UPDATE subscriptions SET renewal_closed_at = now()
   WHERE id = '00000000-0000-7000-8000-000000000701';
$sql$);

SELECT _accept('SUB-004', 'expired 且 renewal_closed_at 非空应当通过', $sql$
  UPDATE subscriptions SET renewal_closed_at = now()
   WHERE id = '00000000-0000-7000-8000-000000000702';
$sql$);

-- 订阅不删（subscriptions_deny_delete）
SELECT _reject('SUB-004', '订阅不可删除', $sql$
  DELETE FROM subscriptions WHERE id = '00000000-0000-7000-8000-000000000702';
$sql$);

--------------------------------------------------------------------------------
-- NODE-010 在役节点形状（00018_node.sql 的 nodes_active_shape）
--------------------------------------------------------------------------------

SELECT _reject('NODE-010', 'serving_status=active 但缺服务器与协议字段必须拒绝', $sql$
  UPDATE nodes SET serving_status = 'active' WHERE id = '00000000-0000-7000-8000-000000000901';
$sql$);

SELECT _accept('NODE-010', '字段齐全的 active 应当通过', $sql$
  UPDATE nodes SET
    server_id = '00000000-0000-7000-8000-000000000811',
    node_type = 'shadowsocks',
    server_host = 'inv-node.example.test',
    server_port = 18443,
    listen_l4 = 'tcp+udp',
    protocol_schema_version = 1,
    serving_status = 'active'
   WHERE id = '00000000-0000-7000-8000-000000000901';
$sql$);

--------------------------------------------------------------------------------
-- NODE-011 同机端口兜底（00038_node_listen_tcp_udp.sql 的 nodes_listen_tcp_claim_key /
-- nodes_listen_udp_claim_key）。上面 NODE-010 已让 901 以 tcp+udp 占住 811:18443。
-- 未退役、挂了服务器的节点都占端口（含 draft），所以这里用 draft 节点省掉 active 的形状字段。
--------------------------------------------------------------------------------

SELECT _reject('NODE-011', '同端口已有 tcp+udp 时再占 udp 必须拒绝', $sql$
  INSERT INTO nodes (id, name, pool_id, serving_status, server_id, server_port, listen_l4)
  VALUES ('00000000-0000-7000-8000-000000000902', 'inv-test-node-b',
          '00000000-0000-7000-8000-000000000801', 'draft',
          '00000000-0000-7000-8000-000000000811', 18443, 'udp');
$sql$);

SELECT _reject('NODE-011', '同端口已有 tcp+udp 时再占 tcp 必须拒绝', $sql$
  INSERT INTO nodes (id, name, pool_id, serving_status, server_id, server_port, listen_l4)
  VALUES ('00000000-0000-7000-8000-000000000903', 'inv-test-node-c',
          '00000000-0000-7000-8000-000000000801', 'draft',
          '00000000-0000-7000-8000-000000000811', 18443, 'tcp');
$sql$);

SELECT _accept('NODE-011', '另一个端口上 tcp 与 udp 可以共存', $sql$
  INSERT INTO nodes (id, name, pool_id, serving_status, server_id, server_port, listen_l4)
  VALUES ('00000000-0000-7000-8000-000000000904', 'inv-test-node-d',
          '00000000-0000-7000-8000-000000000801', 'draft',
          '00000000-0000-7000-8000-000000000811', 18444, 'tcp'),
         ('00000000-0000-7000-8000-000000000905', 'inv-test-node-e',
          '00000000-0000-7000-8000-000000000801', 'draft',
          '00000000-0000-7000-8000-000000000811', 18444, 'udp');
$sql$);

SELECT _accept('NODE-011', '退役的节点不占端口', $sql$
  INSERT INTO nodes (id, name, pool_id, serving_status, retired_at, server_id, server_port, listen_l4)
  VALUES ('00000000-0000-7000-8000-000000000906', 'inv-test-node-f',
          '00000000-0000-7000-8000-000000000801', 'retired', now(),
          '00000000-0000-7000-8000-000000000811', 18443, 'tcp+udp');
$sql$);

--------------------------------------------------------------------------------
-- SUB-002 套餐版本冻结（00008_catalog.sql 的 plan_versions_guard / entitlements_guard /
-- quota_definitions_guard / prices_guard）
--------------------------------------------------------------------------------

SELECT _reject('SUB-002', '已发布版本不可改', $sql$
  UPDATE plan_versions SET grace_period_hours = 48
   WHERE id = '00000000-0000-7000-8000-000000000601';
$sql$);

SELECT _reject('SUB-002', '已发布版本不可删', $sql$
  DELETE FROM plan_versions WHERE id = '00000000-0000-7000-8000-000000000601';
$sql$);

SELECT _reject('SUB-002', '已发布版本的权益不可改', $sql$
  UPDATE entitlements SET value = 'false'::jsonb
   WHERE id = '00000000-0000-7000-8000-000000000611';
$sql$);

SELECT _reject('SUB-002', '已发布版本不可增配额', $sql$
  INSERT INTO quota_definitions (plan_version_id, metric, limit_value, unit, period)
  VALUES ('00000000-0000-7000-8000-000000000601', 'devices.active', 1, 'count', 'total');
$sql$);

SELECT _reject('SUB-002', '新建版本不能直接是已发布', $sql$
  INSERT INTO plan_versions (plan_id, version, status, frozen_at)
  VALUES ('00000000-0000-7000-8000-000000000501', 3, 'published', now());
$sql$);

SELECT _accept('SUB-002', '草稿版本可改', $sql$
  UPDATE plan_versions SET grace_period_hours = 12
   WHERE id = '00000000-0000-7000-8000-000000000602';
$sql$);

SELECT _accept('SUB-002', '草稿版本可增权益', $sql$
  INSERT INTO entitlements (plan_version_id, code, value)
  VALUES ('00000000-0000-7000-8000-000000000602', 'inv.feature', 'false'::jsonb);
$sql$);

SELECT _reject('SUB-002', '价格不可直接改金额（只能归档新建）', $sql$
  UPDATE prices SET unit_amount = 19.99
   WHERE id = '00000000-0000-7000-8000-000000000631';
$sql$);

SELECT _accept('SUB-002', '价格可以归档一次（row_version 加一）', $sql$
  UPDATE prices SET status = 'archived', row_version = row_version + 1
   WHERE id = '00000000-0000-7000-8000-000000000631';
$sql$);

--------------------------------------------------------------------------------
-- PAY-001 订单金额恒等式（00013_order.sql）
-- 每条只违反注释里那一个约束，其余金额自洽
--------------------------------------------------------------------------------

-- orders_payable_identity：payable 10 ≠ total 10 - balance 2
SELECT _reject('PAY-001', 'payable 与 total-balance 不一致必须拒绝', $sql$
  INSERT INTO orders (order_no, user_id, kind, currency,
                      subtotal_amount, discount_amount, proration_credit_amount, waived_amount,
                      total_amount, balance_applied, payable_amount, expires_at)
  VALUES ('INV-BAD-PAY', '00000000-0000-7000-8000-000000000101', 'new', 'USD',
          10.00, 0, 0, 0, 10.00, 2.00, 10.00, now() + interval '30 minutes');
$sql$);

-- orders_total_identity：total 9.50 ≠ 10 - 1
SELECT _reject('PAY-001', 'total 恒等式被破坏必须拒绝', $sql$
  INSERT INTO orders (order_no, user_id, kind, currency,
                      subtotal_amount, discount_amount, proration_credit_amount, waived_amount,
                      total_amount, balance_applied, payable_amount, expires_at)
  VALUES ('INV-BAD-TOT', '00000000-0000-7000-8000-000000000101', 'new', 'USD',
          10.00, 1.00, 0, 0, 9.50, 0, 9.50, now() + interval '30 minutes');
$sql$);

-- orders_proration_only_upgrade
SELECT _reject('PAY-001', '非 upgrade 单不能带换套餐抵扣', $sql$
  INSERT INTO orders (order_no, user_id, kind, currency,
                      subtotal_amount, discount_amount, proration_credit_amount, waived_amount,
                      total_amount, balance_applied, payable_amount, expires_at)
  VALUES ('INV-BAD-PRO', '00000000-0000-7000-8000-000000000101', 'new', 'USD',
          10.00, 0, 3.00, 0, 7.00, 0, 7.00, now() + interval '30 minutes');
$sql$);

-- orders_waived_only_upgrade
SELECT _reject('PAY-001', '非 upgrade 单不能免零头', $sql$
  INSERT INTO orders (order_no, user_id, kind, currency,
                      subtotal_amount, discount_amount, proration_credit_amount, waived_amount,
                      total_amount, balance_applied, payable_amount, expires_at)
  VALUES ('INV-BAD-WAV', '00000000-0000-7000-8000-000000000101', 'new', 'USD',
          10.00, 0, 0, 0.50, 9.50, 0, 9.50, now() + interval '30 minutes');
$sql$);

-- orders_discount_le_subtotal（total 按恒等式取 greatest(…, 0) = 0）
SELECT _reject('PAY-001', '折扣大于小计必须拒绝', $sql$
  INSERT INTO orders (order_no, user_id, kind, currency,
                      subtotal_amount, discount_amount, proration_credit_amount, waived_amount,
                      total_amount, balance_applied, payable_amount, expires_at)
  VALUES ('INV-BAD-DIS', '00000000-0000-7000-8000-000000000101', 'new', 'USD',
          10.00, 10.01, 0, 0, 0, 0, 0, now() + interval '30 minutes');
$sql$);

SELECT _accept('PAY-001', '金额自洽的订单应当通过', $sql$
  INSERT INTO orders (order_no, user_id, kind, currency,
                      subtotal_amount, discount_amount, proration_credit_amount, waived_amount,
                      total_amount, balance_applied, payable_amount, expires_at)
  VALUES ('INV-GOOD-001', '00000000-0000-7000-8000-000000000101', 'new', 'USD',
          10.00, 1.00, 0, 0, 9.00, 2.00, 7.00, now() + interval '30 minutes');
$sql$);

-- orders_manual_reason：有开单人却没写理由
SELECT _reject('PAY-001', '人工单必须写明理由', $sql$
  INSERT INTO orders (order_no, user_id, kind, currency,
                      subtotal_amount, discount_amount, proration_credit_amount, waived_amount,
                      total_amount, balance_applied, payable_amount, expires_at, created_by)
  VALUES ('INV-BAD-MR0', '00000000-0000-7000-8000-000000000101', 'new', 'USD',
          0, 0, 0, 0, 0, 0, 0, now() + interval '30 minutes',
          '00000000-0000-7000-8000-000000000102');
$sql$);

-- manual_reason 列 CHECK：≤ 500 字
SELECT _reject('PAY-001', '人工单理由超过 500 字必须拒绝', $sql$
  INSERT INTO orders (order_no, user_id, kind, currency,
                      subtotal_amount, discount_amount, proration_credit_amount, waived_amount,
                      total_amount, balance_applied, payable_amount, expires_at,
                      created_by, manual_reason)
  VALUES ('INV-BAD-MR', '00000000-0000-7000-8000-000000000101', 'new', 'USD',
          0, 0, 0, 0, 0, 0, 0, now() + interval '30 minutes',
          '00000000-0000-7000-8000-000000000102', repeat('x', 501));
$sql$);

-- 订单不删（orders_no_delete）
SELECT _reject('PAY-001', '订单不可删除', $sql$
  DELETE FROM orders WHERE id = '00000000-0000-7000-8000-000000000c01';
$sql$);

--------------------------------------------------------------------------------
-- MKT-003 邀请关系不可变（00005_identity.sql）
--------------------------------------------------------------------------------

SELECT _accept('MKT-003', '首次绑定邀请关系应当通过', $sql$
  INSERT INTO referrals (referee_user_id, referrer_user_id)
  VALUES ('00000000-0000-7000-8000-000000000103', '00000000-0000-7000-8000-000000000101');
$sql$);

-- referrals_append_only
SELECT _reject('MKT-003', '更换邀请上级必须拒绝', $sql$
  UPDATE referrals SET referrer_user_id = '00000000-0000-7000-8000-000000000102'
   WHERE referee_user_id = '00000000-0000-7000-8000-000000000103';
$sql$);

-- referrals_no_self
SELECT _reject('MKT-003', '自我邀请必须拒绝', $sql$
  INSERT INTO referrals (referee_user_id, referrer_user_id)
  VALUES ('00000000-0000-7000-8000-000000000102', '00000000-0000-7000-8000-000000000102');
$sql$);

--------------------------------------------------------------------------------
-- PAY-003 幂等（00004_idempotency.sql、00012_payment.sql）
--------------------------------------------------------------------------------

-- idempotency_keys_scope_actor_key
SELECT _reject('PAY-003', '同一操作者同作用域重复幂等键必须拒绝', $sql$
  INSERT INTO idempotency_keys (scope, actor_id, idempotency_key, request_hash, locked_until)
  VALUES ('inv_test', '00000000-0000-7000-8000-000000000101', 'inv-key-1',
          decode(repeat('bb', 32), 'hex'), now() + interval '5 minutes');
$sql$);

SELECT _accept('PAY-003', '别的操作者用同一个键应当通过', $sql$
  INSERT INTO idempotency_keys (scope, actor_id, idempotency_key, request_hash, locked_until)
  VALUES ('inv_test', '00000000-0000-7000-8000-000000000102', 'inv-key-1',
          decode(repeat('bb', 32), 'hex'), now() + interval '5 minutes');
$sql$);

-- payment_events_provider_event_key：同一渠道事件重放只能落一行
SELECT _reject('PAY-003', '重复渠道事件必须拒绝', $sql$
  INSERT INTO payment_events (provider_id, provider_event_id, event_type, signature_verified)
  VALUES ('00000000-0000-7000-8000-000000000b11', 'inv-evt-seed-1', 'payment.succeeded', true);
$sql$);

-- payments_provider_payment_key：同一笔渠道收款不能记两次
SELECT _reject('PAY-003', '重复渠道支付流水号必须拒绝', $sql$
  INSERT INTO payments (order_id, user_id, provider_id, provider_payment_id, currency, amount)
  VALUES ('00000000-0000-7000-8000-000000000c01', '00000000-0000-7000-8000-000000000101',
          '00000000-0000-7000-8000-000000000b11', 'inv-pay-seed-1', 'USD', 10.00);
$sql$);

--------------------------------------------------------------------------------
-- PAY-FEE 渠道类型与用户手续费（00042_payment_fee_crypto.sql）
-- 手续费在 Go 里算（payment/service/surcharge.go），库里只钉范围：固定 0–1000 元、比例 0–100%、
-- 线下收款不收；意图与收款里加收的手续费必须小于整笔金额（订单应付 > 0）。
--------------------------------------------------------------------------------

-- payment_providers_adapter_check：加密货币渠道只收 btcpay、coinpayments
SELECT _accept('PAY-FEE', 'btcpay 渠道带手续费应当通过', $sql$
  INSERT INTO payment_providers (code, adapter, display_name, currencies, fee_fixed, fee_percent)
  VALUES ('inv-btc', 'btcpay', 'Inv BTC', '["CNY"]'::jsonb, 0.30, 1.50);
$sql$);

SELECT _accept('PAY-FEE', 'coinpayments 渠道应当通过', $sql$
  INSERT INTO payment_providers (code, adapter, display_name, currencies)
  VALUES ('inv-cps', 'coinpayments', 'Inv CPS', '["CNY"]'::jsonb);
$sql$);

SELECT _reject('PAY-FEE', '未知渠道类型必须拒绝', $sql$
  INSERT INTO payment_providers (code, adapter, display_name)
  VALUES ('inv-unk', 'stripe', 'Inv Unknown');
$sql$);

-- fee_fixed / fee_percent 的范围 CHECK
SELECT _reject('PAY-FEE', '固定手续费超过 1000 元必须拒绝', $sql$
  UPDATE payment_providers SET fee_fixed = 1000.01 WHERE id = '00000000-0000-7000-8000-000000000b11';
$sql$);

SELECT _reject('PAY-FEE', '比例手续费超过 100% 必须拒绝', $sql$
  UPDATE payment_providers SET fee_percent = 100.01 WHERE id = '00000000-0000-7000-8000-000000000b11';
$sql$);

SELECT _reject('PAY-FEE', '负的手续费必须拒绝', $sql$
  UPDATE payment_providers SET fee_fixed = -0.01 WHERE id = '00000000-0000-7000-8000-000000000b11';
$sql$);

-- payment_providers_offline_no_fee
SELECT _reject('PAY-FEE', '线下收款渠道不能设手续费', $sql$
  UPDATE payment_providers SET fee_percent = 1 WHERE code = 'offline';
$sql$);

-- payment_intents_surcharge：意图 10.00 里加收的手续费不能 ≥ 10.00
SELECT _accept('PAY-FEE', '意图加收的手续费小于整笔应当通过', $sql$
  INSERT INTO payment_intents (order_id, order_no, user_id, provider_id, out_trade_no, currency,
                               amount, surcharge_amount, status, http_method, redirect_url)
  VALUES ('00000000-0000-7000-8000-000000000c01', 'INV-SEED-001', '00000000-0000-7000-8000-000000000101',
          '00000000-0000-7000-8000-000000000b11', 'INV-SEED-001-fee1', 'USD',
          10.30, 0.30, 'cancelled', 'GET', 'https://pay.example.test/i/1');
$sql$);

SELECT _reject('PAY-FEE', '意图加收的手续费等于整笔必须拒绝', $sql$
  INSERT INTO payment_intents (order_id, order_no, user_id, provider_id, out_trade_no, currency,
                               amount, surcharge_amount, status, http_method, redirect_url)
  VALUES ('00000000-0000-7000-8000-000000000c01', 'INV-SEED-001', '00000000-0000-7000-8000-000000000101',
          '00000000-0000-7000-8000-000000000b11', 'INV-SEED-001-fee2', 'USD',
          10.00, 10.00, 'cancelled', 'GET', 'https://pay.example.test/i/2');
$sql$);

-- payments_surcharge
SELECT _reject('PAY-FEE', '收款里的手续费为负必须拒绝', $sql$
  INSERT INTO payments (order_id, user_id, provider_id, provider_payment_id, currency, amount, surcharge_amount)
  VALUES ('00000000-0000-7000-8000-000000000c01', '00000000-0000-7000-8000-000000000101',
          '00000000-0000-7000-8000-000000000b11', 'inv-pay-fee-1', 'USD', 10.30, -0.01);
$sql$);

SELECT _reject('PAY-FEE', '收款里的手续费超过整笔必须拒绝', $sql$
  INSERT INTO payments (order_id, user_id, provider_id, provider_payment_id, currency, amount, surcharge_amount)
  VALUES ('00000000-0000-7000-8000-000000000c01', '00000000-0000-7000-8000-000000000101',
          '00000000-0000-7000-8000-000000000b11', 'inv-pay-fee-2', 'USD', 10.30, 10.31);
$sql$);

--------------------------------------------------------------------------------
-- MKT-001 优惠券不超发（00010_promo.sql）
--------------------------------------------------------------------------------

SELECT _accept('MKT-001', '用到恰好满额应当通过', $sql$
  UPDATE coupons SET redeemed_count = 5 WHERE id = '00000000-0000-7000-8000-000000000c31';
$sql$);

-- coupons_capacity：已确认 + 占用中 ≤ 总容量
SELECT _reject('MKT-001', '占用超过容量必须拒绝', $sql$
  UPDATE coupons SET reserved_count = 1 WHERE id = '00000000-0000-7000-8000-000000000c31';
$sql$);

-- coupons_face_exactly_one
SELECT _reject('MKT-001', '定额与百分比必须恰好一个', $sql$
  INSERT INTO coupons (code, name, currency) VALUES ('INVNOFACE', '无面值', 'USD');
$sql$);

-- percent_off_bp 列 CHECK：1–10000
SELECT _reject('MKT-001', '百分比折扣超过 100% 必须拒绝', $sql$
  INSERT INTO coupons (code, name, percent_off_bp, currency) VALUES ('INVBADPCT', '错误百分比', 10001, 'USD');
$sql$);

--------------------------------------------------------------------------------
-- NFR-008 降级开关（00002_settings.sql 种好的开关行）
--------------------------------------------------------------------------------

-- feature_switches_essential_stays_on
SELECT _reject('NFR-008', 'essential 开关不可被关闭', $sql$
  UPDATE feature_switches SET enabled = false, reason = '演练' WHERE code = 'auth.login';
$sql$);

-- feature_switches_disable_needs_reason
SELECT _reject('NFR-008', '关闭开关但不填原因必须拒绝', $sql$
  UPDATE feature_switches SET enabled = false, reason = NULL WHERE code = 'billing.checkout';
$sql$);

SELECT _accept('NFR-008', '非 essential 开关填了原因可关闭', $sql$
  UPDATE feature_switches SET enabled = false, reason = '遭遇注册洪峰，临时关闭' WHERE code = 'auth.registration';
$sql$);

--------------------------------------------------------------------------------
-- OPS-005 事务类通知不可退订（00026_notify_inbox.sql）
--------------------------------------------------------------------------------

-- notification_preferences_transactional_always_on
SELECT _reject('OPS-005', '事务类通知不可关闭', $sql$
  INSERT INTO notification_preferences (user_id, category, channel, enabled)
  VALUES ('00000000-0000-7000-8000-000000000101', 'transactional', 'email', false);
$sql$);

SELECT _accept('OPS-005', '营销类通知可以关闭', $sql$
  INSERT INTO notification_preferences (user_id, category, channel, enabled)
  VALUES ('00000000-0000-7000-8000-000000000101', 'marketing', 'email', false);
$sql$);

--------------------------------------------------------------------------------
-- OPS-001 内部备注只能由客服或系统写（00027_support.sql）
--------------------------------------------------------------------------------

-- ticket_messages_internal_only_from_staff（带 author_id，避开 ticket_messages_author_shape）
SELECT _reject('OPS-001', '用户身份不能写内部备注', $sql$
  INSERT INTO ticket_messages (ticket_id, author_id, author_kind, body, internal_note)
  VALUES ('00000000-0000-7000-8000-000000000d01',
          '00000000-0000-7000-8000-000000000101', 'user', '偷写内部备注', true);
$sql$);

SELECT _accept('OPS-001', '客服身份写内部备注应当通过', $sql$
  INSERT INTO ticket_messages (ticket_id, author_id, author_kind, body, internal_note)
  VALUES ('00000000-0000-7000-8000-000000000d01',
          '00000000-0000-7000-8000-000000000102', 'agent', '仅客服可见', true);
$sql$);

--------------------------------------------------------------------------------
-- USE-002 用量上报去重（00022_node_traffic.sql：node_traffic_reports (server_id, report_id) 唯一）
--------------------------------------------------------------------------------

-- node_traffic_reports_server_report_key
SELECT _reject('USE-002', '同一服务器同一上报编号重复必须拒绝', $sql$
  INSERT INTO node_traffic_reports (server_id, report_id, entries, content_sha256,
                                    entry_count, upload_bytes, download_bytes, invalid_count, unlisted_count)
  VALUES ('00000000-0000-7000-8000-000000000811', 'inv-report-1', '[]'::jsonb,
          decode(repeat('bb', 32), 'hex'), 0, 0, 0, 0, 0);
$sql$);

SELECT _reject('USE-002', '流量上报不可改', $sql$
  UPDATE node_traffic_reports SET upload_bytes = 1
   WHERE server_id = '00000000-0000-7000-8000-000000000811' AND report_id = 'inv-report-1';
$sql$);

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

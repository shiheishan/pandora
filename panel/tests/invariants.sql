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
--   SUB-007 试用防刷（subscription_trials 每人终身一次）、
--   XB-COMM（对齐 Xboard 的三级分销与自动转余额标记、提现方式与 USDT 打款、按用户单独佣金设置）、
--   XB-REG 注销作废可提现佣金（commission_forfeitures 每人一行、只追加）、
--   PAY-FEE 渠道类型与用户手续费（00042）、
--   XB-TG Telegram 工单提醒映射与自动关闭原因、
--   XB-NODE 节点标签 / 流量上限与超限标记 / 时段倍率 / 中转 / ECH 密钥（00045）、
--   XB-ORDER 流量重置单（00046）与叠加单 stack（00052）、
--   SUB-002 里的两类套餐价格形状与套餐上的流量重置价（00056）、SUB-BONUS 送的流量当期有效（00053）、
--   XB-SUBDOMAIN 订阅域名分组：名称 / 权重 / 地址形状、唯一缺省组、一个用户组只进一个域名组、删用户组级联（00048）。
--
-- 不覆盖的验收项及原因（没有对应的表或机制）：
--   SEC-013 双人审批 — 没有审批表：后台权限只分有和没有。
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

-- p_expect 给了时，拒绝原因里必须出现这个约束名：防止用例因为别的错误（比如字面量写错）被拒、却显示通过
CREATE OR REPLACE FUNCTION _reject(p_req text, p_name text, p_sql text, p_expect text DEFAULT NULL)
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
      VALUES (p_req, p_name, 'reject', p_expect IS NULL OR strpos(v_err, p_expect) > 0,
              left(CASE WHEN p_expect IS NULL OR strpos(v_err, p_expect) > 0 THEN v_err
                        ELSE '被别的错误拒绝（期望 ' || p_expect || '）：' || v_err END, 110));
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
INSERT INTO ledger_accounts (id, account_type, owner_user_id, owner_ref, balance)
VALUES ('00000000-0000-7000-8000-000000000201', 'user_balance',
        '00000000-0000-7000-8000-000000000101', '', 0.00),
       ('00000000-0000-7000-8000-000000000202', 'channel_cash', NULL, 'inv-test-channel', 0.00),
       ('00000000-0000-7000-8000-000000000203', 'platform_revenue', NULL, 'inv-test-revenue', 0.00);

-- 套餐：501 是订阅套餐、502 是一次性套餐（plans.kind，建好后经价格的复合外键钉住）。
-- 501 的版本 601 建成草稿、写好权益与配额后发布（冻结）；602 是之后的新草稿（每个套餐只许一个草稿）
INSERT INTO plans (id, code, name, status, kind, allow_renewal)
VALUES ('00000000-0000-7000-8000-000000000501', 'inv-test-plan', 'Inv 测试套餐', 'draft', 'subscription', true),
       ('00000000-0000-7000-8000-000000000502', 'inv-test-once', 'Inv 一次性套餐', 'draft', 'one_time', false);

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

INSERT INTO prices (id, plan_id, plan_kind, unit_amount, billing_interval, interval_count, status)
VALUES ('00000000-0000-7000-8000-000000000631',
        '00000000-0000-7000-8000-000000000501', 'subscription', 9.99, 'month', 1, 'active');

INSERT INTO subscriptions (id, user_id, plan_id, plan_version_id, status,
                           current_period_start, current_period_end,
                           snapshot_amount,
                           snapshot_interval, snapshot_interval_count)
VALUES ('00000000-0000-7000-8000-000000000701',
        '00000000-0000-7000-8000-000000000101',
        '00000000-0000-7000-8000-000000000501',
        '00000000-0000-7000-8000-000000000601', 'active',
        now() - interval '1 day', now() + interval '29 days',
        9.99, 'month', 1),
       ('00000000-0000-7000-8000-000000000702',
        '00000000-0000-7000-8000-000000000102',
        '00000000-0000-7000-8000-000000000501',
        '00000000-0000-7000-8000-000000000601', 'expired',
        now() - interval '60 days', now() - interval '30 days',
        9.99, 'month', 1);

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

INSERT INTO payment_providers (id, code, adapter, display_name, enabled, accepting_new)
VALUES ('00000000-0000-7000-8000-000000000b11', 'invtestpay', 'epay', 'Inv Test Pay', true, false);

INSERT INTO orders (id, order_no, user_id, kind,
                    subtotal_amount, discount_amount, proration_credit_amount, waived_amount,
                    total_amount, balance_applied, payable_amount, expires_at)
VALUES ('00000000-0000-7000-8000-000000000c01', 'INV-SEED-001',
        '00000000-0000-7000-8000-000000000101', 'new',
        10.00, 0, 0, 0, 10.00, 0, 10.00, now() + interval '30 minutes');

INSERT INTO payments (id, order_id, user_id, provider_id, provider_payment_id, amount)
VALUES ('00000000-0000-7000-8000-000000000c11', '00000000-0000-7000-8000-000000000c01',
        '00000000-0000-7000-8000-000000000101', '00000000-0000-7000-8000-000000000b11',
        'inv-pay-seed-1', 10.00);

INSERT INTO payment_events (provider_id, provider_event_id, event_type, signature_verified)
VALUES ('00000000-0000-7000-8000-000000000b11', 'inv-evt-seed-1', 'payment.succeeded', true);

INSERT INTO idempotency_keys (scope, actor_id, idempotency_key, request_hash, locked_until)
VALUES ('inv_test', '00000000-0000-7000-8000-000000000101', 'inv-key-1',
        decode(repeat('aa', 32), 'hex'), now() + interval '5 minutes');

INSERT INTO coupons (id, code, name, amount_off, max_redemptions, redeemed_count)
VALUES ('00000000-0000-7000-8000-000000000c31', 'INVTEST10', 'Inv 立减', 1.00, 5, 4);

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
    INSERT INTO ledger_transactions (id, kind, actor_kind)
    VALUES ('00000000-0000-7000-8000-000000000301', 'commission_accrued', 'system')
    RETURNING id
  )
  INSERT INTO ledger_entries (transaction_id, account_id, direction, amount)
  SELECT t.id, v.account_id, v.direction, v.amount
    FROM t, (VALUES ('00000000-0000-7000-8000-000000000202'::uuid, 'debit', 10.00),
                    ('00000000-0000-7000-8000-000000000203'::uuid, 'credit', 9.00)) AS v(account_id, direction, amount);
$sql$);

-- 只有交易、没有分录：提交时同样被配平触发器拦下（至少两条分录）
SELECT _reject('PAY-005', '没有分录的账务交易必须回滚', $sql$
  INSERT INTO ledger_transactions (id, kind, actor_kind)
  VALUES ('00000000-0000-7000-8000-000000000304', 'commission_accrued', 'system');
$sql$);

SELECT _accept('PAY-005', '配平的账务交易应当通过', $sql$
  WITH t AS (
    INSERT INTO ledger_transactions (id, kind, actor_kind)
    VALUES ('00000000-0000-7000-8000-000000000303', 'commission_accrued', 'system')
    RETURNING id
  )
  INSERT INTO ledger_entries (transaction_id, account_id, direction, amount)
  SELECT t.id, v.account_id, v.direction, 10.00
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
                             snapshot_amount,
                             snapshot_interval, snapshot_interval_count)
  VALUES ('00000000-0000-7000-8000-000000000703',
          '00000000-0000-7000-8000-000000000103',
          '00000000-0000-7000-8000-000000000501',
          '00000000-0000-7000-8000-000000000601', 'pending',
          now(), now() + interval '30 days',
          9.99, 'month', 1);
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
-- SUB-007 试用防刷（00040_identity_registration.sql 的 subscription_trials：主键 user_id、只追加）
--------------------------------------------------------------------------------

SELECT _accept('SUB-007', '首次发放注册试用应当通过', $sql$
  INSERT INTO subscription_trials (user_id, subscription_id, plan_id, hours)
  VALUES ('00000000-0000-7000-8000-000000000101', '00000000-0000-7000-8000-000000000701',
          '00000000-0000-7000-8000-000000000501', 24);
$sql$);

-- subscription_trials_pkey：每人终身一次
SELECT _reject('SUB-007', '同一用户第二次发放试用必须拒绝', $sql$
  INSERT INTO subscription_trials (user_id, subscription_id, plan_id, hours)
  VALUES ('00000000-0000-7000-8000-000000000101', '00000000-0000-7000-8000-000000000702',
          '00000000-0000-7000-8000-000000000501', 24);
$sql$, 'subscription_trials_pkey');

-- subscription_trials_append_only：发放记录不能改、不能删（删了就能再领一次）
SELECT _reject('SUB-007', '试用发放记录不可删除', $sql$
  DELETE FROM subscription_trials WHERE user_id = '00000000-0000-7000-8000-000000000101';
$sql$, '是只追加的表');

SELECT _reject('SUB-007', '试用时长越界必须拒绝', $sql$
  INSERT INTO subscription_trials (user_id, subscription_id, plan_id, hours)
  VALUES ('00000000-0000-7000-8000-000000000103', '00000000-0000-7000-8000-000000000702',
          '00000000-0000-7000-8000-000000000501', 0);
$sql$, 'subscription_trials_hours_check');

--------------------------------------------------------------------------------
-- SUB-BONUS 送的流量当期有效（00053_subscription_bonus.sql）：
-- quota_balances.remaining = limit_value + bonus_value − consumed（生成列）；bonus_value ≥ 0；不限流量时 remaining 为空
--------------------------------------------------------------------------------

SELECT _accept('SUB-BONUS', '剩余量等于额度 + 赠送 − 已用', $sql$
  DO $do$
  DECLARE v_remaining bigint;
  BEGIN
    INSERT INTO quota_balances (id, subscription_id, metric, period, period_start, period_end,
                                limit_value, bonus_value, consumed)
    VALUES ('00000000-0000-7000-8000-000000000c41', '00000000-0000-7000-8000-000000000701',
            'traffic.bytes', 'cycle', now() - interval '1 day', now() + interval '29 days', 1000, 200, 300);
    SELECT remaining INTO v_remaining FROM quota_balances WHERE id = '00000000-0000-7000-8000-000000000c41';
    IF v_remaining IS DISTINCT FROM 900 THEN
      RAISE EXCEPTION 'remaining = %，应为 1000 + 200 - 300 = 900', v_remaining;
    END IF;
    UPDATE quota_balances SET bonus_value = bonus_value + 50 WHERE id = '00000000-0000-7000-8000-000000000c41';
    SELECT remaining INTO v_remaining FROM quota_balances WHERE id = '00000000-0000-7000-8000-000000000c41';
    IF v_remaining IS DISTINCT FROM 950 THEN
      RAISE EXCEPTION '加送 50 后 remaining = %，应为 950', v_remaining;
    END IF;
  END
  $do$;
$sql$);

SELECT _accept('SUB-BONUS', '不限流量（额度为空）时剩余量为空', $sql$
  DO $do$
  BEGIN
    INSERT INTO quota_balances (id, subscription_id, metric, period, period_start, limit_value, bonus_value, consumed)
    VALUES ('00000000-0000-7000-8000-000000000c42', '00000000-0000-7000-8000-000000000702',
            'traffic.bytes', 'total', now() - interval '60 days', NULL, 100, 10);
    IF (SELECT remaining FROM quota_balances WHERE id = '00000000-0000-7000-8000-000000000c42') IS NOT NULL THEN
      RAISE EXCEPTION '不限流量时 remaining 应为空';
    END IF;
  END
  $do$;
$sql$);

SELECT _reject('SUB-BONUS', '赠送量为负必须拒绝', $sql$
  UPDATE quota_balances SET bonus_value = -1 WHERE id = '00000000-0000-7000-8000-000000000c41';
$sql$, 'quota_balances_bonus_value_check');

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
-- quota_definitions_guard / prices_guard）与两类套餐的价格形状（00056_catalog_two_kinds.sql）
--------------------------------------------------------------------------------

SELECT _reject('SUB-002', '已发布版本不可改', $sql$
  UPDATE plan_versions SET max_devices = 5
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
  UPDATE plan_versions SET max_devices = 2
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

-- 两类套餐（00056）：价格经复合外键 (plan_id, plan_kind) 指向 plans (id, kind)
SELECT _reject('SUB-002', '价格的套餐类型与套餐不符必须拒绝', $sql$
  INSERT INTO prices (plan_id, plan_kind, unit_amount, billing_interval, interval_count, status)
  VALUES ('00000000-0000-7000-8000-000000000501', 'one_time', 9.99, 'one_time', 1, 'active');
$sql$, 'prices_plan_kind_fk');

-- prices_offer_shape：订阅只许 (month,1) (month,3) (month,6) (year,1) 四档
SELECT _reject('SUB-002', '订阅套餐的在售价不在四档里必须拒绝', $sql$
  INSERT INTO prices (plan_id, plan_kind, unit_amount, billing_interval, interval_count, status)
  VALUES ('00000000-0000-7000-8000-000000000501', 'subscription', 19.99, 'month', 2, 'active');
$sql$, 'prices_offer_shape');

SELECT _reject('SUB-002', '订阅套餐不能有一次性在售价', $sql$
  INSERT INTO prices (plan_id, plan_kind, unit_amount, billing_interval, interval_count, status)
  VALUES ('00000000-0000-7000-8000-000000000501', 'subscription', 19.99, 'one_time', 1, 'active');
$sql$, 'prices_offer_shape');

SELECT _accept('SUB-002', '订阅套餐的年付档应当通过', $sql$
  INSERT INTO prices (plan_id, plan_kind, unit_amount, billing_interval, interval_count, status)
  VALUES ('00000000-0000-7000-8000-000000000501', 'subscription', 99.00, 'year', 1, 'active');
$sql$);

-- prices_validity_one_time：有效天数只属于一次性价
SELECT _reject('SUB-002', '订阅价带有效天数必须拒绝', $sql$
  INSERT INTO prices (plan_id, plan_kind, unit_amount, billing_interval, interval_count, validity_days, status)
  VALUES ('00000000-0000-7000-8000-000000000501', 'subscription', 29.99, 'month', 3, 30, 'active');
$sql$, 'prices_validity_one_time');

SELECT _accept('SUB-002', '一次性套餐的一个价（带有效天数）应当通过', $sql$
  INSERT INTO prices (id, plan_id, plan_kind, unit_amount, billing_interval, interval_count, validity_days, status)
  VALUES ('00000000-0000-7000-8000-000000000632', '00000000-0000-7000-8000-000000000502', 'one_time',
          30.00, 'one_time', 1, 90, 'active');
$sql$);

-- prices_one_active_offer：一次性套餐只有一个在售价
SELECT _reject('SUB-002', '一次性套餐第二个在售价必须拒绝', $sql$
  INSERT INTO prices (plan_id, plan_kind, unit_amount, billing_interval, interval_count, status)
  VALUES ('00000000-0000-7000-8000-000000000502', 'one_time', 40.00, 'one_time', 1, 'active');
$sql$, 'prices_one_active_offer');

-- 有价格后套餐类型改不了（复合外键不级联更新）
SELECT _reject('SUB-002', '有价格的套餐不能改类型', $sql$
  UPDATE plans SET kind = 'one_time', allow_renewal = false
   WHERE id = '00000000-0000-7000-8000-000000000501';
$sql$, 'prices_plan_kind_fk');

SELECT _reject('SUB-002', '一次性套餐不能开续费', $sql$
  UPDATE plans SET allow_renewal = true WHERE id = '00000000-0000-7000-8000-000000000502';
$sql$, 'plans_one_time_no_renewal');

-- 流量重置挂在套餐上：reset_price 空 = 不卖、> 0；reset_limit 1–1000 且要有价格
SELECT _accept('SUB-002', '合法的流量重置价与次数上限应当通过', $sql$
  UPDATE plans SET reset_price = 2.00, reset_limit = 3 WHERE id = '00000000-0000-7000-8000-000000000501';
$sql$);

SELECT _reject('SUB-002', '0 元的流量重置价必须拒绝', $sql$
  UPDATE plans SET reset_price = 0 WHERE id = '00000000-0000-7000-8000-000000000502';
$sql$, 'plans_reset_price_check');

SELECT _reject('SUB-002', '没有重置价却设次数上限必须拒绝', $sql$
  UPDATE plans SET reset_limit = 2 WHERE id = '00000000-0000-7000-8000-000000000502';
$sql$, 'plans_reset_limit_needs_price');

--------------------------------------------------------------------------------
-- PAY-001 订单金额恒等式（00013_order.sql）
-- 每条只违反注释里那一个约束，其余金额自洽
--------------------------------------------------------------------------------

-- orders_payable_identity：payable 10 ≠ total 10 - balance 2
SELECT _reject('PAY-001', 'payable 与 total-balance 不一致必须拒绝', $sql$
  INSERT INTO orders (order_no, user_id, kind,
                      subtotal_amount, discount_amount, proration_credit_amount, waived_amount,
                      total_amount, balance_applied, payable_amount, expires_at)
  VALUES ('INV-BAD-PAY', '00000000-0000-7000-8000-000000000101', 'new',
          10.00, 0, 0, 0, 10.00, 2.00, 10.00, now() + interval '30 minutes');
$sql$);

-- orders_total_identity：total 9.50 ≠ 10 - 1
SELECT _reject('PAY-001', 'total 恒等式被破坏必须拒绝', $sql$
  INSERT INTO orders (order_no, user_id, kind,
                      subtotal_amount, discount_amount, proration_credit_amount, waived_amount,
                      total_amount, balance_applied, payable_amount, expires_at)
  VALUES ('INV-BAD-TOT', '00000000-0000-7000-8000-000000000101', 'new',
          10.00, 1.00, 0, 0, 9.50, 0, 9.50, now() + interval '30 minutes');
$sql$);

-- orders_proration_only_upgrade
SELECT _reject('PAY-001', '非 upgrade 单不能带换套餐抵扣', $sql$
  INSERT INTO orders (order_no, user_id, kind,
                      subtotal_amount, discount_amount, proration_credit_amount, waived_amount,
                      total_amount, balance_applied, payable_amount, expires_at)
  VALUES ('INV-BAD-PRO', '00000000-0000-7000-8000-000000000101', 'new',
          10.00, 0, 3.00, 0, 7.00, 0, 7.00, now() + interval '30 minutes');
$sql$);

-- orders_waived_only_upgrade
SELECT _reject('PAY-001', '非 upgrade 单不能免零头', $sql$
  INSERT INTO orders (order_no, user_id, kind,
                      subtotal_amount, discount_amount, proration_credit_amount, waived_amount,
                      total_amount, balance_applied, payable_amount, expires_at)
  VALUES ('INV-BAD-WAV', '00000000-0000-7000-8000-000000000101', 'new',
          10.00, 0, 0, 0.50, 9.50, 0, 9.50, now() + interval '30 minutes');
$sql$);

-- orders_discount_le_subtotal（total 按恒等式取 greatest(…, 0) = 0）
SELECT _reject('PAY-001', '折扣大于小计必须拒绝', $sql$
  INSERT INTO orders (order_no, user_id, kind,
                      subtotal_amount, discount_amount, proration_credit_amount, waived_amount,
                      total_amount, balance_applied, payable_amount, expires_at)
  VALUES ('INV-BAD-DIS', '00000000-0000-7000-8000-000000000101', 'new',
          10.00, 10.01, 0, 0, 0, 0, 0, now() + interval '30 minutes');
$sql$);

SELECT _accept('PAY-001', '金额自洽的订单应当通过', $sql$
  INSERT INTO orders (order_no, user_id, kind,
                      subtotal_amount, discount_amount, proration_credit_amount, waived_amount,
                      total_amount, balance_applied, payable_amount, expires_at)
  VALUES ('INV-GOOD-001', '00000000-0000-7000-8000-000000000101', 'new',
          10.00, 1.00, 0, 0, 9.00, 2.00, 7.00, now() + interval '30 minutes');
$sql$);

-- orders_manual_reason：有开单人却没写理由
SELECT _reject('PAY-001', '人工单必须写明理由', $sql$
  INSERT INTO orders (order_no, user_id, kind,
                      subtotal_amount, discount_amount, proration_credit_amount, waived_amount,
                      total_amount, balance_applied, payable_amount, expires_at, created_by)
  VALUES ('INV-BAD-MR0', '00000000-0000-7000-8000-000000000101', 'new',
          0, 0, 0, 0, 0, 0, 0, now() + interval '30 minutes',
          '00000000-0000-7000-8000-000000000102');
$sql$);

-- manual_reason 列 CHECK：≤ 500 字
SELECT _reject('PAY-001', '人工单理由超过 500 字必须拒绝', $sql$
  INSERT INTO orders (order_no, user_id, kind,
                      subtotal_amount, discount_amount, proration_credit_amount, waived_amount,
                      total_amount, balance_applied, payable_amount, expires_at,
                      created_by, manual_reason)
  VALUES ('INV-BAD-MR', '00000000-0000-7000-8000-000000000101', 'new',
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
  INSERT INTO payments (order_id, user_id, provider_id, provider_payment_id, amount)
  VALUES ('00000000-0000-7000-8000-000000000c01', '00000000-0000-7000-8000-000000000101',
          '00000000-0000-7000-8000-000000000b11', 'inv-pay-seed-1', 10.00);
$sql$);

--------------------------------------------------------------------------------
-- PAY-FEE 渠道类型与用户手续费（00042_payment_fee_crypto.sql）
-- 手续费在 Go 里算（payment/service/surcharge.go），库里只钉范围：固定 0–1000 元、比例 0–100%、
-- 线下收款不收；意图与收款里加收的手续费必须小于整笔金额（订单应付 > 0）。
--------------------------------------------------------------------------------

-- payment_providers_adapter_check：加密货币渠道只收 btcpay、coinpayments
SELECT _accept('PAY-FEE', 'btcpay 渠道带手续费应当通过', $sql$
  INSERT INTO payment_providers (code, adapter, display_name, fee_fixed, fee_percent)
  VALUES ('inv-btc', 'btcpay', 'Inv BTC', 0.30, 1.50);
$sql$);

SELECT _accept('PAY-FEE', 'coinpayments 渠道应当通过', $sql$
  INSERT INTO payment_providers (code, adapter, display_name)
  VALUES ('inv-cps', 'coinpayments', 'Inv CPS');
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
  INSERT INTO payment_intents (order_id, order_no, user_id, provider_id, out_trade_no,
                               amount, surcharge_amount, status, http_method, redirect_url)
  VALUES ('00000000-0000-7000-8000-000000000c01', 'INV-SEED-001', '00000000-0000-7000-8000-000000000101',
          '00000000-0000-7000-8000-000000000b11', 'INV-SEED-001-fee1',
          10.30, 0.30, 'cancelled', 'GET', 'https://pay.example.test/i/1');
$sql$);

SELECT _reject('PAY-FEE', '意图加收的手续费等于整笔必须拒绝', $sql$
  INSERT INTO payment_intents (order_id, order_no, user_id, provider_id, out_trade_no,
                               amount, surcharge_amount, status, http_method, redirect_url)
  VALUES ('00000000-0000-7000-8000-000000000c01', 'INV-SEED-001', '00000000-0000-7000-8000-000000000101',
          '00000000-0000-7000-8000-000000000b11', 'INV-SEED-001-fee2',
          10.00, 10.00, 'cancelled', 'GET', 'https://pay.example.test/i/2');
$sql$);

SELECT _reject('PAY-FEE', '意图加收的手续费为负必须拒绝', $sql$
  INSERT INTO payment_intents (order_id, order_no, user_id, provider_id, out_trade_no,
                               amount, surcharge_amount, status, http_method, redirect_url)
  VALUES ('00000000-0000-7000-8000-000000000c01', 'INV-SEED-001', '00000000-0000-7000-8000-000000000101',
          '00000000-0000-7000-8000-000000000b11', 'INV-SEED-001-fee3',
          10.00, -0.01, 'cancelled', 'GET', 'https://pay.example.test/i/3');
$sql$);

-- payments_surcharge
SELECT _reject('PAY-FEE', '收款里的手续费为负必须拒绝', $sql$
  INSERT INTO payments (order_id, user_id, provider_id, provider_payment_id, amount, surcharge_amount)
  VALUES ('00000000-0000-7000-8000-000000000c01', '00000000-0000-7000-8000-000000000101',
          '00000000-0000-7000-8000-000000000b11', 'inv-pay-fee-1', 10.30, -0.01);
$sql$);

SELECT _reject('PAY-FEE', '收款里的手续费超过整笔必须拒绝', $sql$
  INSERT INTO payments (order_id, user_id, provider_id, provider_payment_id, amount, surcharge_amount)
  VALUES ('00000000-0000-7000-8000-000000000c01', '00000000-0000-7000-8000-000000000101',
          '00000000-0000-7000-8000-000000000b11', 'inv-pay-fee-2', 10.30, 10.31);
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
  INSERT INTO coupons (code, name) VALUES ('INVNOFACE', '无面值');
$sql$);

-- percent_off_bp 列 CHECK：1–10000
SELECT _reject('MKT-001', '百分比折扣超过 100% 必须拒绝', $sql$
  INSERT INTO coupons (code, name, percent_off_bp) VALUES ('INVBADPCT', '错误百分比', 10001);
$sql$);

--------------------------------------------------------------------------------
-- NFR-008 降级开关（00002_settings.sql 种好、00064 删去三个核心项后的开关行）
--------------------------------------------------------------------------------

-- feature_switches_disable_needs_reason
SELECT _reject('NFR-008', '关闭开关但不填原因必须拒绝', $sql$
  UPDATE feature_switches SET enabled = false, reason = NULL WHERE code = 'billing.checkout';
$sql$);

SELECT _accept('NFR-008', '开关填了原因可关闭', $sql$
  UPDATE feature_switches SET enabled = false, reason = '遭遇注册洪峰，临时关闭' WHERE code = 'auth.registration';
$sql$);

--------------------------------------------------------------------------------
-- OPS-005 事务类通知不可退订、营销类只发邮件（00026_notify_inbox.sql、00064）
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

-- notification_preferences_marketing_email_only（00064：营销类只发邮件）
SELECT _reject('OPS-005', '营销类没有 Telegram 偏好', $sql$
  INSERT INTO notification_preferences (user_id, category, channel, enabled)
  VALUES ('00000000-0000-7000-8000-000000000101', 'marketing', 'telegram', false);
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
-- XB-NODE 节点补齐 Xboard（00045_xb_node.sql）：标签、流量上限与超限标记、时段倍率、中转、ECH 密钥。
-- 用 NODE-011 建的草稿节点 904（不在役，改这些列不碰 nodes_active_shape）。
--------------------------------------------------------------------------------

-- nodes_tags_shape
SELECT _reject('XB-NODE', '标签不是数组必须拒绝', $sql$
  UPDATE nodes SET tags = '"香港"'::jsonb WHERE id = '00000000-0000-7000-8000-000000000904';
$sql$);

SELECT _reject('XB-NODE', '标签元素不是字符串必须拒绝', $sql$
  UPDATE nodes SET tags = '["香港", 1]'::jsonb WHERE id = '00000000-0000-7000-8000-000000000904';
$sql$, 'nodes_tags_shape');

SELECT _reject('XB-NODE', '标签超过 10 个必须拒绝', $sql$
  UPDATE nodes SET tags = '["a","b","c","d","e","f","g","h","i","j","k"]'::jsonb
   WHERE id = '00000000-0000-7000-8000-000000000904';
$sql$);

-- nodes_traffic_limit_check / nodes_traffic_reset_day_check / nodes_traffic_used_check
SELECT _reject('XB-NODE', '流量上限为 0 必须拒绝（不限请留 NULL）', $sql$
  UPDATE nodes SET traffic_limit_bytes = 0 WHERE id = '00000000-0000-7000-8000-000000000904';
$sql$);

SELECT _reject('XB-NODE', '重置日 32 必须拒绝', $sql$
  UPDATE nodes SET traffic_reset_day = 32 WHERE id = '00000000-0000-7000-8000-000000000904';
$sql$);

SELECT _reject('XB-NODE', '已用流量为负必须拒绝', $sql$
  UPDATE nodes SET traffic_used_bytes = -1 WHERE id = '00000000-0000-7000-8000-000000000904';
$sql$);

-- nodes_traffic_exceeded_shape：超限标记与「有上限且已用 >= 上限」同真同假
SELECT _reject('XB-NODE', '已用达到上限却没有超限标记必须拒绝', $sql$
  UPDATE nodes SET traffic_limit_bytes = 100, traffic_used_bytes = 100, traffic_exceeded_at = NULL
   WHERE id = '00000000-0000-7000-8000-000000000904';
$sql$);

SELECT _reject('XB-NODE', '没到上限却有超限标记必须拒绝', $sql$
  UPDATE nodes SET traffic_limit_bytes = 100, traffic_used_bytes = 99, traffic_exceeded_at = now()
   WHERE id = '00000000-0000-7000-8000-000000000904';
$sql$);

SELECT _reject('XB-NODE', '不限流量却有超限标记必须拒绝', $sql$
  UPDATE nodes SET traffic_limit_bytes = NULL, traffic_exceeded_at = now()
   WHERE id = '00000000-0000-7000-8000-000000000904';
$sql$);

SELECT _accept('XB-NODE', '有上限、已用达到、带超限标记应当通过', $sql$
  UPDATE nodes SET traffic_limit_bytes = 100, traffic_used_bytes = 150, traffic_exceeded_at = now(),
                   traffic_reset_day = 31, tags = '["香港","IPLC"]'::jsonb
   WHERE id = '00000000-0000-7000-8000-000000000904';
$sql$);

-- nodes_traffic_rate_schedule_shape
SELECT _reject('XB-NODE', '时段倍率超过 12 段必须拒绝', $sql$
  UPDATE nodes SET traffic_rate_schedule = (SELECT jsonb_agg(jsonb_build_object('start', '00:00', 'end', '01:00', 'rate', '1'))
                                              FROM generate_series(1, 13))
   WHERE id = '00000000-0000-7000-8000-000000000904';
$sql$);

-- node_relays 的 CHECK 与外键
SELECT _reject('XB-NODE', '中转端口 0 必须拒绝', $sql$
  INSERT INTO node_relays (parent_node_id, name, server_host, server_port)
  VALUES ('00000000-0000-7000-8000-000000000904', 'inv-relay', 'relay.example.test', 0);
$sql$);

SELECT _reject('XB-NODE', '中转名称为空白必须拒绝', $sql$
  INSERT INTO node_relays (parent_node_id, name, server_host, server_port)
  VALUES ('00000000-0000-7000-8000-000000000904', '  ', 'relay.example.test', 443);
$sql$);

SELECT _reject('XB-NODE', '中转的父节点必须存在', $sql$
  INSERT INTO node_relays (parent_node_id, name, server_host, server_port)
  VALUES ('00000000-0000-7000-8000-0000000009ff', 'inv-relay', 'relay.example.test', 443);
$sql$);

SELECT _accept('XB-NODE', '合法中转应当通过', $sql$
  INSERT INTO node_relays (id, parent_node_id, name, server_host, server_port, sni)
  VALUES ('00000000-0000-7000-8000-000000000e01', '00000000-0000-7000-8000-000000000904',
          'inv-relay', 'relay.example.test', 443, 'node.example.test');
$sql$);

-- node_ech_keys：每节点一把当前钥（node_ech_keys_current_key）、轮换时刻不早于建立时刻
SELECT _accept('XB-NODE', '一把当前 ECH 钥应当通过', $sql$
  INSERT INTO node_ech_keys (id, node_id, public_name, config_list, private_key_sealed)
  VALUES ('00000000-0000-7000-8000-000000000e11', '00000000-0000-7000-8000-000000000904',
          'ech.example.test', '\x0006fe0d00020000'::bytea, '\x01'::bytea);
$sql$);

SELECT _reject('XB-NODE', '同一节点第二把当前 ECH 钥必须拒绝', $sql$
  INSERT INTO node_ech_keys (node_id, public_name, config_list, private_key_sealed)
  VALUES ('00000000-0000-7000-8000-000000000904', 'ech.example.test', '\x0006fe0d00020000'::bytea, '\x01'::bytea);
$sql$, 'node_ech_keys_current_key');

SELECT _reject('XB-NODE', '轮换时刻早于建立时刻必须拒绝', $sql$
  UPDATE node_ech_keys SET superseded_at = created_at - interval '1 second'
   WHERE id = '00000000-0000-7000-8000-000000000e11';
$sql$, 'node_ech_keys_superseded_check');

SELECT _accept('XB-NODE', '当前钥可以轮换（写 superseded_at）', $sql$
  UPDATE node_ech_keys SET superseded_at = now() WHERE id = '00000000-0000-7000-8000-000000000e11';
$sql$);

SELECT _accept('XB-NODE', '旧钥轮换后可以再有一把当前钥', $sql$
  INSERT INTO node_ech_keys (node_id, public_name, config_list, private_key_sealed)
  VALUES ('00000000-0000-7000-8000-000000000904', 'ech.example.test', '\x0006fe0d00020000'::bytea, '\x01'::bytea);
$sql$);

SELECT _reject('XB-NODE', 'ECHConfigList 过短必须拒绝', $sql$
  INSERT INTO node_ech_keys (node_id, public_name, config_list, private_key_sealed)
  VALUES ('00000000-0000-7000-8000-000000000905', 'ech.example.test', '\x00'::bytea, '\x01'::bytea);
$sql$, 'node_ech_keys_config_list_check');

--------------------------------------------------------------------------------
-- XB-COMM 三级分销、提现方式与 USDT 打款、按用户单独佣金设置（00041_commission_levels.sql）
--------------------------------------------------------------------------------

-- withdrawals_method_check：方式是封闭目录
SELECT _reject('XB-COMM', '提现方式不在目录里必须拒绝', $sql$
  INSERT INTO withdrawals (user_id, amount, payout_sealed, method)
  VALUES ('00000000-0000-7000-8000-000000000101', 1.00, '\x01'::bytea, 'bitcoin');
$sql$, 'withdrawals_method_check');

-- method NOT NULL 且没有缺省：新申请必须由代码给出方式
SELECT _reject('XB-COMM', '提现不给方式必须拒绝', $sql$
  INSERT INTO withdrawals (user_id, amount, payout_sealed)
  VALUES ('00000000-0000-7000-8000-000000000101', 1.00, '\x01'::bytea);
$sql$);

-- withdrawals_usdt_amount_shape：实付 USDT 数量只在已打款的 USDT 提现上
SELECT _reject('XB-COMM', '非 USDT 或未打款的提现记实付 USDT 数量必须拒绝', $sql$
  INSERT INTO withdrawals (user_id, amount, payout_sealed, method, paid_usdt_amount)
  VALUES ('00000000-0000-7000-8000-000000000101', 1.00, '\x01'::bytea, 'alipay', 1.5);
$sql$, 'withdrawals_usdt_amount_shape');

SELECT _accept('XB-COMM', '支付宝提现申请可以写入', $sql$
  INSERT INTO withdrawals (user_id, amount, payout_sealed, method)
  VALUES ('00000000-0000-7000-8000-000000000101', 1.00, '\x01'::bytea, 'alipay');
$sql$);

SELECT _accept('XB-COMM', 'USDT（TRC20）提现申请可以写入', $sql$
  INSERT INTO withdrawals (user_id, amount, payout_sealed, method)
  VALUES ('00000000-0000-7000-8000-000000000102', 1.00, '\x01'::bytea, 'usdt_trc20');
$sql$);

-- commission_user_overrides：比例 0–50、范围封闭、两项至少一项
SELECT _reject('XB-COMM', '单独佣金比例超过 50 必须拒绝', $sql$
  INSERT INTO commission_user_overrides (user_id, rate_percent)
  VALUES ('00000000-0000-7000-8000-000000000101', 51);
$sql$);

SELECT _reject('XB-COMM', '单独佣金设置两项都为空必须拒绝', $sql$
  INSERT INTO commission_user_overrides (user_id) VALUES ('00000000-0000-7000-8000-000000000101');
$sql$, 'commission_user_overrides_not_empty');

SELECT _reject('XB-COMM', '单独计佣范围不在目录里必须拒绝', $sql$
  INSERT INTO commission_user_overrides (user_id, scope)
  VALUES ('00000000-0000-7000-8000-000000000101', 'weekly');
$sql$);

SELECT _accept('XB-COMM', '只设计佣范围的单独设置可以写入（比例跟全局）', $sql$
  INSERT INTO commission_user_overrides (user_id, scope)
  VALUES ('00000000-0000-7000-8000-000000000102', 'first_order');
$sql$);

SELECT _accept('XB-COMM', '单独比例为 0（不拿佣金）可以写入', $sql$
  INSERT INTO commission_user_overrides (user_id, rate_percent)
  VALUES ('00000000-0000-7000-8000-000000000103', 0);
$sql$);

-- commission_entries 分级：level 1–3、share 1–100、金额 = trunc(trunc(基数 × 比例) × 份额)（逐级截断到分）
-- 种子订单 c01：基数 10.00，比例 1000bp → 这一单佣金总额 1.00；第 2 级 30% → 0.30
SELECT _accept('XB-COMM', '第 2 级 30% 的条目按逐级截断的金额可以写入', $sql$
  WITH t AS (
    INSERT INTO ledger_transactions (id, kind, actor_kind)
    VALUES ('00000000-0000-7000-8000-000000000341', 'commission_accrued', 'system')
    RETURNING id
  ), le AS (
    INSERT INTO ledger_entries (transaction_id, account_id, direction, amount)
    SELECT t.id, v.account_id, v.direction, 1.00
      FROM t, (VALUES ('00000000-0000-7000-8000-000000000202'::uuid, 'debit'),
                      ('00000000-0000-7000-8000-000000000203'::uuid, 'credit')) AS v(account_id, direction)
  )
  INSERT INTO commission_entries (referrer_user_id, referee_user_id, order_id, order_no,
                                  base_amount, rate_bp, level, share_percent, commission_amount,
                                  frozen_until, accrual_txn_id)
  SELECT '00000000-0000-7000-8000-000000000102', '00000000-0000-7000-8000-000000000101',
         '00000000-0000-7000-8000-000000000c01', 'INV-SEED-001',
         10.00, 1000, 2, 30, 0.30, now(), t.id FROM t;
$sql$);
SELECT _reject('XB-COMM', '分级金额不符公式必须拒绝', $sql$
  WITH t AS (
    INSERT INTO ledger_transactions (id, kind, actor_kind)
    VALUES ('00000000-0000-7000-8000-000000000342', 'commission_accrued', 'system')
    RETURNING id
  ), le AS (
    INSERT INTO ledger_entries (transaction_id, account_id, direction, amount)
    SELECT t.id, v.account_id, v.direction, 1.00
      FROM t, (VALUES ('00000000-0000-7000-8000-000000000202'::uuid, 'debit'),
                      ('00000000-0000-7000-8000-000000000203'::uuid, 'credit')) AS v(account_id, direction)
  )
  INSERT INTO commission_entries (referrer_user_id, referee_user_id, order_id, order_no,
                                  base_amount, rate_bp, level, share_percent, commission_amount,
                                  frozen_until, accrual_txn_id)
  SELECT '00000000-0000-7000-8000-000000000103', '00000000-0000-7000-8000-000000000101',
         '00000000-0000-7000-8000-000000000c01', 'INV-SEED-001',
         10.00, 1000, 3, 30, 0.31, now(), t.id FROM t;
$sql$, 'commission_entries_formula');
SELECT _reject('XB-COMM', '级别超过 3 必须拒绝', $sql$
  WITH t AS (
    INSERT INTO ledger_transactions (id, kind, actor_kind)
    VALUES ('00000000-0000-7000-8000-000000000343', 'commission_accrued', 'system')
    RETURNING id
  ), le AS (
    INSERT INTO ledger_entries (transaction_id, account_id, direction, amount)
    SELECT t.id, v.account_id, v.direction, 1.00
      FROM t, (VALUES ('00000000-0000-7000-8000-000000000202'::uuid, 'debit'),
                      ('00000000-0000-7000-8000-000000000203'::uuid, 'credit')) AS v(account_id, direction)
  )
  INSERT INTO commission_entries (referrer_user_id, referee_user_id, order_id, order_no,
                                  base_amount, rate_bp, level, share_percent, commission_amount,
                                  frozen_until, accrual_txn_id)
  SELECT '00000000-0000-7000-8000-000000000103', '00000000-0000-7000-8000-000000000101',
         '00000000-0000-7000-8000-000000000c01', 'INV-SEED-001',
         10.00, 1000, 4, 30, 0.30, now(), t.id FROM t;
$sql$, 'commission_entries_level_check');
SELECT _reject('XB-COMM', '份额为 0 必须拒绝', $sql$
  WITH t AS (
    INSERT INTO ledger_transactions (id, kind, actor_kind)
    VALUES ('00000000-0000-7000-8000-000000000344', 'commission_accrued', 'system')
    RETURNING id
  ), le AS (
    INSERT INTO ledger_entries (transaction_id, account_id, direction, amount)
    SELECT t.id, v.account_id, v.direction, 1.00
      FROM t, (VALUES ('00000000-0000-7000-8000-000000000202'::uuid, 'debit'),
                      ('00000000-0000-7000-8000-000000000203'::uuid, 'credit')) AS v(account_id, direction)
  )
  INSERT INTO commission_entries (referrer_user_id, referee_user_id, order_id, order_no,
                                  base_amount, rate_bp, level, share_percent, commission_amount,
                                  frozen_until, accrual_txn_id)
  SELECT '00000000-0000-7000-8000-000000000103', '00000000-0000-7000-8000-000000000101',
         '00000000-0000-7000-8000-000000000c01', 'INV-SEED-001',
         10.00, 1000, 3, 0, 0.00, now(), t.id FROM t;
$sql$);

-- commission_entries_order_level_key：一单每一级最多一条（上面已写入 c01 的第 2 级）
SELECT _reject('XB-COMM', '同一单同一级第二条必须拒绝', $sql$
  WITH t AS (
    INSERT INTO ledger_transactions (id, kind, actor_kind)
    VALUES ('00000000-0000-7000-8000-000000000345', 'commission_accrued', 'system')
    RETURNING id
  ), le AS (
    INSERT INTO ledger_entries (transaction_id, account_id, direction, amount)
    SELECT t.id, v.account_id, v.direction, 1.00
      FROM t, (VALUES ('00000000-0000-7000-8000-000000000202'::uuid, 'debit'),
                      ('00000000-0000-7000-8000-000000000203'::uuid, 'credit')) AS v(account_id, direction)
  )
  INSERT INTO commission_entries (referrer_user_id, referee_user_id, order_id, order_no,
                                  base_amount, rate_bp, level, share_percent, commission_amount,
                                  frozen_until, accrual_txn_id)
  SELECT '00000000-0000-7000-8000-000000000103', '00000000-0000-7000-8000-000000000101',
         '00000000-0000-7000-8000-000000000c01', 'INV-SEED-001',
         10.00, 1000, 2, 30, 0.30, now(), t.id FROM t;
$sql$, 'commission_entries_order_level_key');

-- commission_entries_auto_transfer_shape：只有已解冻的条目能挂「等自动转余额」标记
SELECT _reject('XB-COMM', '未解冻的条目挂自动转余额标记必须拒绝', $sql$
  WITH t AS (
    INSERT INTO ledger_transactions (id, kind, actor_kind)
    VALUES ('00000000-0000-7000-8000-000000000346', 'commission_accrued', 'system')
    RETURNING id
  ), le AS (
    INSERT INTO ledger_entries (transaction_id, account_id, direction, amount)
    SELECT t.id, v.account_id, v.direction, 1.00
      FROM t, (VALUES ('00000000-0000-7000-8000-000000000202'::uuid, 'debit'),
                      ('00000000-0000-7000-8000-000000000203'::uuid, 'credit')) AS v(account_id, direction)
  )
  INSERT INTO commission_entries (referrer_user_id, referee_user_id, order_id, order_no,
                                  base_amount, rate_bp, level, share_percent, commission_amount,
                                  frozen_until, accrual_txn_id, auto_transfer_due)
  SELECT '00000000-0000-7000-8000-000000000103', '00000000-0000-7000-8000-000000000101',
         '00000000-0000-7000-8000-000000000c01', 'INV-SEED-001',
         10.00, 1000, 3, 20, 0.20, now(), t.id, true FROM t;
$sql$, 'commission_entries_auto_transfer_shape');

-- withdrawals_usdt_reference_shape：已打款的 USDT 提现，流水号必须是对应链的交易哈希
SELECT _reject('XB-COMM', 'TRC20 打款流水号不是 64 位十六进制必须拒绝', $sql$
  WITH t AS (
    INSERT INTO ledger_transactions (id, kind, actor_kind)
    VALUES ('00000000-0000-7000-8000-000000000351', 'commission_paid', 'system')
    RETURNING id
  ), le AS (
    INSERT INTO ledger_entries (transaction_id, account_id, direction, amount)
    SELECT t.id, v.account_id, v.direction, 1.00
      FROM t, (VALUES ('00000000-0000-7000-8000-000000000202'::uuid, 'debit'),
                      ('00000000-0000-7000-8000-000000000203'::uuid, 'credit')) AS v(account_id, direction)
  )
  INSERT INTO withdrawals (user_id, amount, payout_sealed, method, status,
                           reviewed_at, payout_reference, payout_txn_id, completed_at)
  SELECT '00000000-0000-7000-8000-000000000103', 1.00, '\x01'::bytea, 'usdt_trc20', 'paid',
         now(), 'not-a-hash', t.id, now() FROM t;
$sql$, 'withdrawals_usdt_reference_shape');
SELECT _reject('XB-COMM', 'ERC20 打款流水号缺 0x 必须拒绝', $sql$
  WITH t AS (
    INSERT INTO ledger_transactions (id, kind, actor_kind)
    VALUES ('00000000-0000-7000-8000-000000000352', 'commission_paid', 'system')
    RETURNING id
  ), le AS (
    INSERT INTO ledger_entries (transaction_id, account_id, direction, amount)
    SELECT t.id, v.account_id, v.direction, 1.00
      FROM t, (VALUES ('00000000-0000-7000-8000-000000000202'::uuid, 'debit'),
                      ('00000000-0000-7000-8000-000000000203'::uuid, 'credit')) AS v(account_id, direction)
  )
  INSERT INTO withdrawals (user_id, amount, payout_sealed, method, status,
                           reviewed_at, payout_reference, payout_txn_id, completed_at)
  SELECT '00000000-0000-7000-8000-000000000103', 1.00, '\x01'::bytea, 'usdt_erc20', 'paid',
         now(), 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', t.id, now() FROM t;
$sql$, 'withdrawals_usdt_reference_shape');
SELECT _accept('XB-COMM', 'TRC20 打款流水号是交易哈希可以写入', $sql$
  WITH t AS (
    INSERT INTO ledger_transactions (id, kind, actor_kind)
    VALUES ('00000000-0000-7000-8000-000000000353', 'commission_paid', 'system')
    RETURNING id
  ), le AS (
    INSERT INTO ledger_entries (transaction_id, account_id, direction, amount)
    SELECT t.id, v.account_id, v.direction, 1.00
      FROM t, (VALUES ('00000000-0000-7000-8000-000000000202'::uuid, 'debit'),
                      ('00000000-0000-7000-8000-000000000203'::uuid, 'credit')) AS v(account_id, direction)
  )
  INSERT INTO withdrawals (user_id, amount, payout_sealed, method, status,
                           reviewed_at, payout_reference, payout_txn_id, completed_at)
  SELECT '00000000-0000-7000-8000-000000000103', 1.00, '\x01'::bytea, 'usdt_trc20', 'paid',
         now(), 'abababababababababababababababababababababababababababababababab', t.id, now() FROM t;
$sql$);
SELECT _accept('XB-COMM', '支付宝打款流水号不受哈希形状约束', $sql$
  WITH t AS (
    INSERT INTO ledger_transactions (id, kind, actor_kind)
    VALUES ('00000000-0000-7000-8000-000000000354', 'commission_paid', 'system')
    RETURNING id
  ), le AS (
    INSERT INTO ledger_entries (transaction_id, account_id, direction, amount)
    SELECT t.id, v.account_id, v.direction, 1.00
      FROM t, (VALUES ('00000000-0000-7000-8000-000000000202'::uuid, 'debit'),
                      ('00000000-0000-7000-8000-000000000203'::uuid, 'credit')) AS v(account_id, direction)
  )
  INSERT INTO withdrawals (user_id, amount, payout_sealed, method, status,
                           reviewed_at, payout_reference, payout_txn_id, completed_at)
  SELECT '00000000-0000-7000-8000-000000000103', 1.00, '\x01'::bytea, 'alipay', 'paid',
         now(), 'ALIPAY-INV-0001', t.id, now() FROM t;
$sql$);

--------------------------------------------------------------------------------
-- XB-REG 注销作废可提现佣金（00040_identity_registration.sql：每人最多一行、只追加；00051 起只有人民币）
--------------------------------------------------------------------------------

SELECT _accept('XB-REG', '注销作废记录带对应的冲回交易可以写入', $sql$
  WITH t AS (
    INSERT INTO ledger_transactions (id, kind, actor_kind)
    VALUES ('00000000-0000-7000-8000-000000000361', 'commission_voided', 'system')
    RETURNING id
  ), le AS (
    INSERT INTO ledger_entries (transaction_id, account_id, direction, amount)
    SELECT t.id, v.account_id, v.direction, 1.00
      FROM t, (VALUES ('00000000-0000-7000-8000-000000000202'::uuid, 'debit'),
                      ('00000000-0000-7000-8000-000000000203'::uuid, 'credit')) AS v(account_id, direction)
  )
  INSERT INTO commission_forfeitures (id, user_id, amount, reason, ledger_txn_id)
  SELECT '00000000-0000-7000-8000-000000000371', '00000000-0000-7000-8000-000000000102', 1.00,
         'account_deleted', t.id FROM t;
$sql$);
SELECT _reject('XB-REG', '同一人第二次作废必须拒绝', $sql$
  WITH t AS (
    INSERT INTO ledger_transactions (id, kind, actor_kind)
    VALUES ('00000000-0000-7000-8000-000000000362', 'commission_voided', 'system')
    RETURNING id
  ), le AS (
    INSERT INTO ledger_entries (transaction_id, account_id, direction, amount)
    SELECT t.id, v.account_id, v.direction, 1.00
      FROM t, (VALUES ('00000000-0000-7000-8000-000000000202'::uuid, 'debit'),
                      ('00000000-0000-7000-8000-000000000203'::uuid, 'credit')) AS v(account_id, direction)
  )
  INSERT INTO commission_forfeitures (id, user_id, amount, reason, ledger_txn_id)
  SELECT '00000000-0000-7000-8000-000000000372', '00000000-0000-7000-8000-000000000102', 1.00,
         'account_deleted', t.id FROM t;
$sql$, 'commission_forfeitures_user_key');
SELECT _reject('XB-REG', '作废金额为 0 必须拒绝', $sql$
  INSERT INTO commission_forfeitures (id, user_id, amount, reason, ledger_txn_id)
  VALUES ('00000000-0000-7000-8000-000000000373', '00000000-0000-7000-8000-000000000103', 0.00,
          'account_deleted', '00000000-0000-7000-8000-000000000361');
$sql$, 'commission_forfeitures_amount_check');
SELECT _reject('XB-REG', '作废原因不在清单里必须拒绝', $sql$
  INSERT INTO commission_forfeitures (id, user_id, amount, reason, ledger_txn_id)
  VALUES ('00000000-0000-7000-8000-000000000374', '00000000-0000-7000-8000-000000000103', 1.00,
          'manual', '00000000-0000-7000-8000-000000000361');
$sql$, 'commission_forfeitures_reason_check');
SELECT _reject('XB-REG', '作废记录不可改', $sql$
  UPDATE commission_forfeitures SET amount = 2.00 WHERE id = '00000000-0000-7000-8000-000000000371';
$sql$, '是只追加的表');
SELECT _reject('XB-REG', '作废记录不可删', $sql$
  DELETE FROM commission_forfeitures WHERE id = '00000000-0000-7000-8000-000000000371';
$sql$, '是只追加的表');
SELECT _reject('XB-REG', '两条作废记录共用一笔冲回交易必须拒绝', $sql$
  INSERT INTO commission_forfeitures (id, user_id, amount, reason, ledger_txn_id)
  VALUES ('00000000-0000-7000-8000-000000000376', '00000000-0000-7000-8000-000000000103', 1.00,
          'account_deleted', '00000000-0000-7000-8000-000000000361');
$sql$, 'commission_forfeitures_ledger_txn_key');

--------------------------------------------------------------------------------
-- XB-TG Telegram 工单提醒映射与自动关闭原因（00044_xb_tg.sql）
--------------------------------------------------------------------------------

SELECT _accept('XB-TG', '工单提醒映射写入应当通过', $sql$
  INSERT INTO telegram_ticket_messages (chat_id, message_id, ticket_id)
  VALUES (-1000000000001, 42, '00000000-0000-7000-8000-000000000d01');
$sql$);

-- 主键 (chat_id, message_id)：同一条群消息只能对应一张工单
SELECT _reject('XB-TG', '同一条提醒消息重复映射必须拒绝', $sql$
  INSERT INTO telegram_ticket_messages (chat_id, message_id, ticket_id)
  VALUES (-1000000000001, 42, '00000000-0000-7000-8000-000000000d01');
$sql$);

SELECT _reject('XB-TG', '提醒消息 id 必须为正', $sql$
  INSERT INTO telegram_ticket_messages (chat_id, message_id, ticket_id)
  VALUES (-1000000000001, 0, '00000000-0000-7000-8000-000000000d01');
$sql$);

SELECT _accept('XB-TG', '自动关闭原因应当通过', $sql$
  UPDATE tickets SET status = 'closed', closed_at = now(), closed_reason = 'auto_closed', closed_note = NULL
   WHERE id = '00000000-0000-7000-8000-000000000d01';
$sql$);

SELECT _reject('XB-TG', '未知的关闭原因必须拒绝', $sql$
  UPDATE tickets SET closed_reason = 'timeout' WHERE id = '00000000-0000-7000-8000-000000000d01';
$sql$);

--------------------------------------------------------------------------------
-- XB-ORDER 流量重置单（00046_xb_order.sql）与叠加单（00052_order_two_kinds.sql）。
-- 流量重置价挂在套餐上（plans.reset_price，见 SUB-002）；会员折扣、券限定周期、流量包单已删（00052、00054）
-- 夹具 id 用 00e2x 段
--------------------------------------------------------------------------------

-- orders：traffic_reset 必须指向订阅、同一订阅只许一张未完结
SELECT _accept('XB-ORDER', '指向订阅的流量重置单应当通过', $sql$
  INSERT INTO orders (id, order_no, user_id, kind, subscription_id,
                      subtotal_amount, discount_amount, proration_credit_amount, waived_amount,
                      total_amount, balance_applied, payable_amount, expires_at)
  VALUES ('00000000-0000-7000-8000-000000000e21', 'INV-RESET-001', '00000000-0000-7000-8000-000000000101',
          'traffic_reset', '00000000-0000-7000-8000-000000000701',
          2.00, 0, 0, 0, 2.00, 0, 2.00, now() + interval '30 minutes');
$sql$);

SELECT _reject('XB-ORDER', '没有订阅的流量重置单必须拒绝', $sql$
  INSERT INTO orders (order_no, user_id, kind,
                      subtotal_amount, discount_amount, proration_credit_amount, waived_amount,
                      total_amount, balance_applied, payable_amount, expires_at)
  VALUES ('INV-RESET-002', '00000000-0000-7000-8000-000000000101', 'traffic_reset',
          2.00, 0, 0, 0, 2.00, 0, 2.00, now() + interval '30 minutes');
$sql$, 'orders_target_subscription');

SELECT _reject('XB-ORDER', '同一订阅第二张未完结的流量重置单必须拒绝', $sql$
  INSERT INTO orders (order_no, user_id, kind, subscription_id,
                      subtotal_amount, discount_amount, proration_credit_amount, waived_amount,
                      total_amount, balance_applied, payable_amount, expires_at)
  VALUES ('INV-RESET-003', '00000000-0000-7000-8000-000000000101', 'traffic_reset',
          '00000000-0000-7000-8000-000000000701',
          2.00, 0, 0, 0, 2.00, 0, 2.00, now() + interval '30 minutes');
$sql$, 'orders_one_open_traffic_reset');

-- orders：重置单永远按原价收（用户 10-14 定），没有减免、不挂券。用已过期的单，避开「同一订阅只许一张未完结」的索引
SELECT _accept('XB-ORDER', '没有减免的已过期流量重置单应当通过', $sql$
  INSERT INTO orders (order_no, user_id, kind, status, subscription_id,
                      subtotal_amount, discount_amount, proration_credit_amount, waived_amount,
                      total_amount, balance_applied, payable_amount, expires_at, expired_at)
  VALUES ('INV-RESET-005', '00000000-0000-7000-8000-000000000101', 'traffic_reset', 'expired',
          '00000000-0000-7000-8000-000000000701',
          2.00, 0, 0, 0, 2.00, 0, 2.00, now() - interval '1 minute', now());
$sql$);

SELECT _reject('XB-ORDER', '带减免的流量重置单必须拒绝', $sql$
  INSERT INTO orders (order_no, user_id, kind, status, subscription_id,
                      subtotal_amount, discount_amount, proration_credit_amount, waived_amount,
                      total_amount, balance_applied, payable_amount, expires_at, expired_at)
  VALUES ('INV-RESET-006', '00000000-0000-7000-8000-000000000101', 'traffic_reset', 'expired',
          '00000000-0000-7000-8000-000000000701',
          2.00, 0.50, 0, 0, 1.50, 0, 1.50, now() - interval '1 minute', now());
$sql$, 'orders_traffic_reset_no_discount');

SELECT _reject('XB-ORDER', '挂了券的流量重置单必须拒绝', $sql$
  INSERT INTO orders (order_no, user_id, kind, status, subscription_id, coupon_id,
                      subtotal_amount, discount_amount, proration_credit_amount, waived_amount,
                      total_amount, balance_applied, payable_amount, expires_at, expired_at)
  VALUES ('INV-RESET-007', '00000000-0000-7000-8000-000000000101', 'traffic_reset', 'expired',
          '00000000-0000-7000-8000-000000000701', '00000000-0000-7000-8000-000000000c31',
          2.00, 0, 0, 0, 2.00, 0, 2.00, now() - interval '1 minute', now());
$sql$, 'orders_traffic_reset_no_discount');

SELECT _reject('XB-ORDER', '未知的订单种类必须拒绝', $sql$
  INSERT INTO orders (order_no, user_id, kind, subscription_id,
                      subtotal_amount, discount_amount, proration_credit_amount, waived_amount,
                      total_amount, balance_applied, payable_amount, expires_at)
  VALUES ('INV-RESET-004', '00000000-0000-7000-8000-000000000101', 'reset',
          '00000000-0000-7000-8000-000000000701',
          2.00, 0, 0, 0, 2.00, 0, 2.00, now() + interval '30 minutes');
$sql$, 'orders_kind_check');

-- order_items：重置单的订单行有套餐与版本、没有价格档与周期
SELECT _accept('XB-ORDER', '流量重置订单行应当通过', $sql$
  INSERT INTO order_items (id, order_id, plan_id, plan_version_id, snapshot_name, snapshot_plan_version,
                           quantity, unit_amount, line_amount)
  VALUES ('00000000-0000-7000-8000-000000000e22', '00000000-0000-7000-8000-000000000e21',
          '00000000-0000-7000-8000-000000000501', '00000000-0000-7000-8000-000000000601',
          '流量重置 · Inv 测试套餐', 1, 1, 2.00, 2.00);
$sql$);

SELECT _reject('XB-ORDER', '没有价格档却带周期的套餐订单行必须拒绝', $sql$
  INSERT INTO order_items (order_id, plan_id, plan_version_id, snapshot_name, snapshot_plan_version,
                           snapshot_interval, snapshot_interval_count, quantity, unit_amount, line_amount)
  VALUES ('00000000-0000-7000-8000-000000000e21',
          '00000000-0000-7000-8000-000000000501', '00000000-0000-7000-8000-000000000601',
          '流量重置 · Inv 测试套餐', 1, 'month', 1, 1, 2.00, 2.00);
$sql$, 'order_items_target');

-- orders：叠加单 stack（一次性套餐「叠加到这一份」）必须指向订阅，且与续费、换套餐共用「同一份订阅只许一张未完结」
SELECT _reject('XB-ORDER', '没有订阅的叠加单必须拒绝', $sql$
  INSERT INTO orders (order_no, user_id, kind,
                      subtotal_amount, discount_amount, proration_credit_amount, waived_amount,
                      total_amount, balance_applied, payable_amount, expires_at)
  VALUES ('INV-STACK-002', '00000000-0000-7000-8000-000000000101', 'stack',
          30.00, 0, 0, 0, 30.00, 0, 30.00, now() + interval '30 minutes');
$sql$, 'orders_target_subscription');

SELECT _accept('XB-ORDER', '指向订阅的叠加单应当通过', $sql$
  INSERT INTO orders (id, order_no, user_id, kind, subscription_id,
                      subtotal_amount, discount_amount, proration_credit_amount, waived_amount,
                      total_amount, balance_applied, payable_amount, expires_at)
  VALUES ('00000000-0000-7000-8000-000000000e24', 'INV-STACK-001', '00000000-0000-7000-8000-000000000101',
          'stack', '00000000-0000-7000-8000-000000000701',
          30.00, 0, 0, 0, 30.00, 0, 30.00, now() + interval '30 minutes');
$sql$);

SELECT _reject('XB-ORDER', '同一订阅已有未完结叠加单时再开续费单必须拒绝', $sql$
  INSERT INTO orders (order_no, user_id, kind, subscription_id,
                      subtotal_amount, discount_amount, proration_credit_amount, waived_amount,
                      total_amount, balance_applied, payable_amount, expires_at)
  VALUES ('INV-STACK-003', '00000000-0000-7000-8000-000000000101', 'renewal',
          '00000000-0000-7000-8000-000000000701',
          9.99, 0, 0, 0, 9.99, 0, 9.99, now() + interval '30 minutes');
$sql$, 'orders_one_open_subscription_change');

SELECT _reject('XB-ORDER', '流量包单 addon 已删除必须拒绝', $sql$
  INSERT INTO orders (order_no, user_id, kind,
                      subtotal_amount, discount_amount, proration_credit_amount, waived_amount,
                      total_amount, balance_applied, payable_amount, expires_at)
  VALUES ('INV-ADDON-001', '00000000-0000-7000-8000-000000000101', 'addon',
          5.00, 0, 0, 0, 5.00, 0, 5.00, now() + interval '30 minutes');
$sql$, 'orders_kind_check');

-- order_items：叠加单的订单行同买套餐，指向价格档与周期（库里只钉形状；只许一次性套餐叠加由 catalog 的 UseStack 管）
SELECT _accept('XB-ORDER', '叠加单的订单行应当通过', $sql$
  INSERT INTO order_items (order_id, plan_id, plan_version_id, price_id, snapshot_name, snapshot_plan_version,
                           snapshot_interval, snapshot_interval_count, quantity, unit_amount, line_amount)
  VALUES ('00000000-0000-7000-8000-000000000e24',
          '00000000-0000-7000-8000-000000000501', '00000000-0000-7000-8000-000000000601',
          '00000000-0000-7000-8000-000000000631',
          'Inv 测试套餐', 1, 'month', 1, 1, 30.00, 30.00);
$sql$);

-- traffic_reset_logs：原因加 order（用户购买）
SELECT _accept('XB-ORDER', '用户购买的流量重置日志应当通过', $sql$
  INSERT INTO traffic_reset_logs (subscription_id, user_id, metric, reason, consumed_before, note)
  VALUES ('00000000-0000-7000-8000-000000000701', '00000000-0000-7000-8000-000000000101',
          'traffic.bytes', 'order', 1024, 'INV-RESET-001');
$sql$);

SELECT _reject('XB-ORDER', '未知的重置原因必须拒绝', $sql$
  INSERT INTO traffic_reset_logs (subscription_id, user_id, metric, reason, consumed_before)
  VALUES ('00000000-0000-7000-8000-000000000701', '00000000-0000-7000-8000-000000000101',
          'traffic.bytes', 'purchase', 1024);
$sql$, 'traffic_reset_logs_reason_check');

--------------------------------------------------------------------------------
-- XB-SUBDOMAIN 订阅域名分组（00048_xb_subdomain.sql）
--------------------------------------------------------------------------------

SELECT _accept('XB-SUBDOMAIN', '种子：两个用户组', $sql$
  INSERT INTO user_groups (id, code, name) VALUES
    ('00000000-0000-7000-8000-000000000e01', 'inv-subdomain', 'inv 订阅域名'),
    ('00000000-0000-7000-8000-000000000e02', 'inv-subdomain-2', 'inv 订阅域名 2');
$sql$);

SELECT _accept('XB-SUBDOMAIN', '种子：两个订阅域名组', $sql$
  INSERT INTO subscription_domain_groups (id, name, is_default, position) VALUES
    ('00000000-0000-7000-8000-000000000e11', 'inv-缺省', true, 0),
    ('00000000-0000-7000-8000-000000000e12', 'inv-备用', false, 1);
$sql$);

SELECT _accept('XB-SUBDOMAIN', '种子：缺省组内两个域名', $sql$
  INSERT INTO subscription_domains (group_id, base_url, weight, position) VALUES
    ('00000000-0000-7000-8000-000000000e11', 'https://sub-a.example.test', 2, 0),
    ('00000000-0000-7000-8000-000000000e11', 'https://sub-b.example.test', 1, 1);
$sql$);

SELECT _accept('XB-SUBDOMAIN', '种子：用户组分到缺省组', $sql$
  INSERT INTO subscription_domain_group_user_groups (user_group_id, group_id) VALUES
    ('00000000-0000-7000-8000-000000000e01', '00000000-0000-7000-8000-000000000e11');
$sql$);

SELECT _reject('XB-SUBDOMAIN', '只能有一个缺省组', $sql$
  UPDATE subscription_domain_groups SET is_default = true WHERE id = '00000000-0000-7000-8000-000000000e12';
$sql$, 'subscription_domain_groups_one_default');

SELECT _reject('XB-SUBDOMAIN', '组名首尾不能带空格', $sql$
  INSERT INTO subscription_domain_groups (id, name, is_default, position) VALUES
    ('00000000-0000-7000-8000-000000000e13', ' inv-bad ', false, 2);
$sql$, 'subscription_domain_groups_name_check');

SELECT _reject('XB-SUBDOMAIN', '组名最多 40 个字', $sql$
  INSERT INTO subscription_domain_groups (id, name, is_default, position) VALUES
    ('00000000-0000-7000-8000-000000000e14', 'inv-abcdefghijklmnopqrstuvwxyz0123456789ab', false, 3);
$sql$, 'subscription_domain_groups_name_check');

SELECT _reject('XB-SUBDOMAIN', '组名不能重复', $sql$
  INSERT INTO subscription_domain_groups (id, name, is_default, position) VALUES
    ('00000000-0000-7000-8000-000000000e15', 'inv-缺省', false, 4);
$sql$, 'subscription_domain_groups_name_key');

SELECT _reject('XB-SUBDOMAIN', '组 position 不能超过 19', $sql$
  INSERT INTO subscription_domain_groups (id, name, is_default, position) VALUES
    ('00000000-0000-7000-8000-000000000e16', 'inv-pos20', false, 20);
$sql$, 'subscription_domain_groups_position_check');

SELECT _reject('XB-SUBDOMAIN', '域名权重不能为 0', $sql$
  INSERT INTO subscription_domains (group_id, base_url, weight, position) VALUES
    ('00000000-0000-7000-8000-000000000e11', 'https://sub-w0.example.test', 0, 2);
$sql$, 'subscription_domains_weight_check');

SELECT _reject('XB-SUBDOMAIN', '域名权重不能超过 100', $sql$
  INSERT INTO subscription_domains (group_id, base_url, weight, position) VALUES
    ('00000000-0000-7000-8000-000000000e11', 'https://sub-w101.example.test', 101, 3);
$sql$, 'subscription_domains_weight_check');

SELECT _reject('XB-SUBDOMAIN', '订阅地址必须是 https', $sql$
  INSERT INTO subscription_domains (group_id, base_url, weight, position) VALUES
    ('00000000-0000-7000-8000-000000000e11', 'http://sub-c.example.test', 1, 4);
$sql$, 'subscription_domains_base_url_check');

SELECT _reject('XB-SUBDOMAIN', '订阅地址不能带路径', $sql$
  INSERT INTO subscription_domains (group_id, base_url, weight, position) VALUES
    ('00000000-0000-7000-8000-000000000e11', 'https://sub-c.example.test/path', 1, 5);
$sql$, 'subscription_domains_base_url_check');

SELECT _reject('XB-SUBDOMAIN', '订阅地址主机名必须小写', $sql$
  INSERT INTO subscription_domains (group_id, base_url, weight, position) VALUES
    ('00000000-0000-7000-8000-000000000e11', 'https://Sub-C.example.test', 1, 6);
$sql$, 'subscription_domains_base_url_check');

SELECT _reject('XB-SUBDOMAIN', '订阅地址不能带端口', $sql$
  INSERT INTO subscription_domains (group_id, base_url, weight, position) VALUES
    ('00000000-0000-7000-8000-000000000e11', 'https://sub-c.example.test:8443', 1, 7);
$sql$, 'subscription_domains_base_url_check');

SELECT _reject('XB-SUBDOMAIN', '订阅地址不能是 IP', $sql$
  INSERT INTO subscription_domains (group_id, base_url, weight, position) VALUES
    ('00000000-0000-7000-8000-000000000e11', 'https://192.0.2.10', 1, 7);
$sql$, 'subscription_domains_base_url_check');

SELECT _reject('XB-SUBDOMAIN', '订阅地址不能是单段主机名', $sql$
  INSERT INTO subscription_domains (group_id, base_url, weight, position) VALUES
    ('00000000-0000-7000-8000-000000000e11', 'https://intranet', 1, 7);
$sql$, 'subscription_domains_base_url_check');

SELECT _reject('XB-SUBDOMAIN', '同组不能重复登记同一地址', $sql$
  INSERT INTO subscription_domains (group_id, base_url, weight, position) VALUES
    ('00000000-0000-7000-8000-000000000e11', 'https://sub-a.example.test', 1, 7);
$sql$, 'subscription_domains_pkey');

SELECT _reject('XB-SUBDOMAIN', '同组 position 不能重复', $sql$
  INSERT INTO subscription_domains (group_id, base_url, weight, position) VALUES
    ('00000000-0000-7000-8000-000000000e11', 'https://sub-duppos.example.test', 1, 0);
$sql$, 'subscription_domains_group_position_key');

SELECT _reject('XB-SUBDOMAIN', '同一用户组只能分到一个域名组', $sql$
  INSERT INTO subscription_domain_group_user_groups (user_group_id, group_id) VALUES
    ('00000000-0000-7000-8000-000000000e01', '00000000-0000-7000-8000-000000000e12');
$sql$, 'subscription_domain_group_user_groups_pkey');

SELECT _reject('XB-SUBDOMAIN', '分配不存在的用户组必须拒绝', $sql$
  INSERT INTO subscription_domain_group_user_groups (user_group_id, group_id) VALUES
    ('00000000-0000-7000-8000-000000000e99', '00000000-0000-7000-8000-000000000e12');
$sql$, 'subscription_domain_group_user_groups_user_group_id_fkey');

SELECT _accept('XB-SUBDOMAIN', '删用户组后分配随之消失', $sql$
  DELETE FROM user_groups WHERE id = '00000000-0000-7000-8000-000000000e01';
$sql$);

SELECT _accept('XB-SUBDOMAIN', '确认用户组分配已级联删除', $sql$
  DO $do$
  BEGIN
    IF EXISTS (SELECT 1 FROM subscription_domain_group_user_groups WHERE user_group_id = '00000000-0000-7000-8000-000000000e01') THEN
      RAISE EXCEPTION '分配没有随用户组删除';
    END IF;
  END $do$;
$sql$);

SELECT _accept('XB-SUBDOMAIN', '删域名组后组内域名随之消失', $sql$
  DELETE FROM subscription_domain_groups WHERE id = '00000000-0000-7000-8000-000000000e11';
$sql$);

SELECT _accept('XB-SUBDOMAIN', '确认组内域名已级联删除', $sql$
  DO $do$
  BEGIN
    IF EXISTS (SELECT 1 FROM subscription_domains WHERE group_id = '00000000-0000-7000-8000-000000000e11') THEN
      RAISE EXCEPTION '域名没有随组删除';
    END IF;
  END $do$;
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

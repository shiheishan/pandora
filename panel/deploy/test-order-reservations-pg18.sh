#!/usr/bin/env bash
set -Eeuo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
IMAGE="${PANDORA_TEST_POSTGRES_IMAGE:-postgres:18}"
CONTAINER="pandora-order-reservation-test-$$"
PASSWORD="reservation-test-only"
RACE_A_LOG="$(mktemp)"
RACE_B_LOG="$(mktemp)"

command -v docker >/dev/null 2>&1 || {
  echo "test-order-reservations: docker is required" >&2
  exit 1
}
command -v awk >/dev/null 2>&1 || {
  echo "test-order-reservations: awk is required" >&2
  exit 1
}

cleanup() {
  docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
  rm -f "$RACE_A_LOG" "$RACE_B_LOG"
}
trap cleanup EXIT INT TERM

docker run -d --name "$CONTAINER" \
  -e POSTGRES_PASSWORD="$PASSWORD" \
  -e POSTGRES_DB=aegis_reservation_test \
  "$IMAGE" >/dev/null

for _ in $(seq 1 60); do
  if docker exec "$CONTAINER" pg_isready -U postgres -d aegis_reservation_test >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
docker exec "$CONTAINER" pg_isready -U postgres -d aegis_reservation_test >/dev/null
ready_successes=0
for _ in $(seq 1 60); do
  if docker exec -e PGPASSWORD="$PASSWORD" "$CONTAINER" \
    psql -X -v ON_ERROR_STOP=1 -Atc 'SELECT 1' -U postgres -d postgres \
    2>/dev/null | grep -qx '1'; then
    ready_successes=$((ready_successes + 1))
    if [[ "$ready_successes" -ge 2 ]]; then break; fi
  else
    ready_successes=0
  fi
  sleep 1
done
docker exec -e PGPASSWORD="$PASSWORD" "$CONTAINER" \
  psql -X -v ON_ERROR_STOP=1 -Atc 'SELECT 1' -U postgres -d postgres \
  | grep -qx '1'

psql_in() {
  docker exec -i -e PGPASSWORD="$PASSWORD" "$CONTAINER" \
    psql -X -v ON_ERROR_STOP=1 -U postgres -d aegis_reservation_test "$@"
}

psql_db() {
  local database="$1"
  shift
  docker exec -i -e PGPASSWORD="$PASSWORD" "$CONTAINER" \
    psql -X -v ON_ERROR_STOP=1 -U postgres -d "$database" "$@"
}

up_sql() {
  awk '/^-- \+goose Down/{exit} /^-- \+goose Up/{up=1; next} up' "$1"
}

down_sql() {
  awk '/^-- \+goose Down/{down=1; next} down' "$1"
}

for migration in "$ROOT"/migrations/000{01..35}_*.sql; do
  up_sql "$migration" | psql_in >/dev/null
done

# A schema-35 legacy fixture with one fulfilled order and one open order.
# Old counters intentionally represent both orders, exactly as the pre-00036
# application did. The open order uses 200 minor units of a 1000-unit balance.
psql_in >/dev/null <<'SQL'
INSERT INTO tenants (id, slug, display_name, default_currency)
VALUES
  ('10000000-0000-7000-8000-000000000001', 'reservation-a', 'Reservation A', 'CNY'),
  ('20000000-0000-7000-8000-000000000001', 'reservation-b', 'Reservation B', 'CNY');

INSERT INTO users (id, tenant_id, email, display_name)
VALUES
  ('10000000-0000-7000-8000-000000000011', '10000000-0000-7000-8000-000000000001', 'a@example.test', 'A'),
  ('20000000-0000-7000-8000-000000000011', '20000000-0000-7000-8000-000000000001', 'b@example.test', 'B');

INSERT INTO products (id, tenant_id, code, name)
VALUES
  ('10000000-0000-7000-8000-000000000021',
   '10000000-0000-7000-8000-000000000001', 'legacy-product', 'Legacy Product'),
  ('20000000-0000-7000-8000-000000000021',
   '20000000-0000-7000-8000-000000000001', 'tenant-b-product', 'Tenant B Product');

INSERT INTO plans
  (id, tenant_id, product_id, code, name, purchase_limit_per_user,
   stock_total, stock_reserved)
VALUES
  ('10000000-0000-7000-8000-000000000031',
   '10000000-0000-7000-8000-000000000001',
   '10000000-0000-7000-8000-000000000021',
   'legacy-plan', 'Legacy Plan', 5, 2, 2),
  ('20000000-0000-7000-8000-000000000031',
   '20000000-0000-7000-8000-000000000001',
   '20000000-0000-7000-8000-000000000021',
   'tenant-b-plan', 'Tenant B Plan', NULL, NULL, 0);

INSERT INTO orders
  (id, tenant_id, order_no, user_id, kind, status, currency,
   subtotal_amount, discount_amount, tax_amount, balance_applied,
   total_amount, payable_amount, paid_amount, expires_at, paid_at, fulfilled_at)
VALUES
  ('10000000-0000-7000-8000-000000000041',
   '10000000-0000-7000-8000-000000000001', 'LEGACY-PAID',
   '10000000-0000-7000-8000-000000000011', 'new', 'fulfilled', 'CNY',
   500, 0, 0, 0, 500, 500, 500, now() - interval '1 hour',
   now() - interval '2 hours', now() - interval '2 hours'),
  ('10000000-0000-7000-8000-000000000042',
   '10000000-0000-7000-8000-000000000001', 'LEGACY-OPEN',
   '10000000-0000-7000-8000-000000000011', 'new', 'pending_payment', 'CNY',
   500, 100, 0, 200, 400, 200, 0, now() + interval '30 minutes', NULL, NULL),
  ('10000000-0000-7000-8000-000000000043',
   '10000000-0000-7000-8000-000000000001', 'LEGACY-FULL-BALANCE',
   '10000000-0000-7000-8000-000000000011', 'renewal', 'fulfilled', 'CNY',
   100, 0, 0, 100, 100, 0, 100, now() - interval '1 hour',
   now() - interval '2 hours', now() - interval '2 hours'),
  ('10000000-0000-7000-8000-000000000044',
   '10000000-0000-7000-8000-000000000001', 'LEGACY-MIXED',
   '10000000-0000-7000-8000-000000000011', 'renewal', 'fulfilled', 'CNY',
   500, 0, 0, 200, 500, 300, 500, now() - interval '1 hour',
   now() - interval '2 hours', now() - interval '2 hours'),
  ('10000000-0000-7000-8000-000000000045',
   '10000000-0000-7000-8000-000000000001', 'LEGACY-COUPON-ONLY',
   '10000000-0000-7000-8000-000000000011', 'renewal', 'fulfilled', 'CNY',
   100, 100, 0, 0, 0, 0, 0, now() - interval '1 hour',
   NULL, now() - interval '2 hours'),
  ('10000000-0000-7000-8000-000000000046',
   '10000000-0000-7000-8000-000000000001', 'LEGACY-REFUND-RETRY',
   '10000000-0000-7000-8000-000000000011', 'renewal', 'fulfilled', 'CNY',
   100, 0, 0, 0, 100, 100, 100, now() - interval '1 hour',
   now() - interval '2 hours', now() - interval '2 hours'),
  ('10000000-0000-7000-8000-000000000047',
   '10000000-0000-7000-8000-000000000001', 'LEGACY-REFUND-RACE',
   '10000000-0000-7000-8000-000000000011', 'renewal', 'fulfilled', 'CNY',
   100, 0, 0, 0, 100, 100, 100, now() - interval '1 hour',
   now() - interval '2 hours', now() - interval '2 hours'),
  ('10000000-0000-7000-8000-000000000049',
   '10000000-0000-7000-8000-000000000001', 'LEGACY-REFUND-STATES',
   '10000000-0000-7000-8000-000000000011', 'renewal', 'fulfilled', 'CNY',
   500, 0, 0, 0, 500, 500, 500, now() - interval '1 hour',
   now() - interval '2 hours', now() - interval '2 hours'),
  ('20000000-0000-7000-8000-000000000048',
   '20000000-0000-7000-8000-000000000001', 'TENANT-B-REFUND-RLS',
   '20000000-0000-7000-8000-000000000011', 'renewal', 'fulfilled', 'CNY',
   100, 0, 0, 0, 100, 100, 100, now() - interval '1 hour',
   now() - interval '2 hours', now() - interval '2 hours');

INSERT INTO order_items
  (tenant_id, order_id, product_id, plan_id, snapshot_product_name,
   snapshot_plan_name, snapshot_interval, snapshot_interval_count,
   quantity, unit_amount, line_amount, currency)
VALUES
  ('10000000-0000-7000-8000-000000000001',
   '10000000-0000-7000-8000-000000000041',
   '10000000-0000-7000-8000-000000000021',
   '10000000-0000-7000-8000-000000000031',
   'Legacy Product', 'Legacy Plan', 'month', 1, 1, 500, 500, 'CNY'),
  ('10000000-0000-7000-8000-000000000001',
   '10000000-0000-7000-8000-000000000042',
   '10000000-0000-7000-8000-000000000021',
   '10000000-0000-7000-8000-000000000031',
   'Legacy Product', 'Legacy Plan', 'month', 1, 1, 500, 500, 'CNY'),
  ('10000000-0000-7000-8000-000000000001',
   '10000000-0000-7000-8000-000000000043',
   '10000000-0000-7000-8000-000000000021',
   '10000000-0000-7000-8000-000000000031',
   'Legacy Product', 'Legacy Plan', 'month', 1, 1, 100, 100, 'CNY'),
  ('10000000-0000-7000-8000-000000000001',
   '10000000-0000-7000-8000-000000000044',
   '10000000-0000-7000-8000-000000000021',
   '10000000-0000-7000-8000-000000000031',
   'Legacy Product', 'Legacy Plan', 'month', 1, 1, 500, 500, 'CNY'),
  ('10000000-0000-7000-8000-000000000001',
   '10000000-0000-7000-8000-000000000045',
   '10000000-0000-7000-8000-000000000021',
   '10000000-0000-7000-8000-000000000031',
   'Legacy Product', 'Legacy Plan', 'month', 1, 1, 100, 100, 'CNY'),
  ('10000000-0000-7000-8000-000000000001',
   '10000000-0000-7000-8000-000000000046',
   '10000000-0000-7000-8000-000000000021',
   '10000000-0000-7000-8000-000000000031',
   'Legacy Product', 'Legacy Plan', 'month', 1, 1, 100, 100, 'CNY'),
  ('10000000-0000-7000-8000-000000000001',
   '10000000-0000-7000-8000-000000000047',
   '10000000-0000-7000-8000-000000000021',
   '10000000-0000-7000-8000-000000000031',
   'Legacy Product', 'Legacy Plan', 'month', 1, 1, 100, 100, 'CNY'),
  ('10000000-0000-7000-8000-000000000001',
   '10000000-0000-7000-8000-000000000049',
   '10000000-0000-7000-8000-000000000021',
   '10000000-0000-7000-8000-000000000031',
   'Legacy Product', 'Legacy Plan', 'month', 1, 1, 500, 500, 'CNY'),
  ('20000000-0000-7000-8000-000000000001',
   '20000000-0000-7000-8000-000000000048',
   '20000000-0000-7000-8000-000000000021',
   '20000000-0000-7000-8000-000000000031',
   'Tenant B Product', 'Tenant B Plan', 'month', 1, 1, 100, 100, 'CNY');

INSERT INTO payment_providers
  (id,tenant_id,code,adapter,display_name,enabled)
VALUES
  ('10000000-0000-7000-8000-000000000081',
   '10000000-0000-7000-8000-000000000001','testpay','test','Test Pay',true),
  ('20000000-0000-7000-8000-000000000081',
   '20000000-0000-7000-8000-000000000001','testpay-b','test','Test Pay B',true);
INSERT INTO payment_intents
  (id,tenant_id,order_id,provider_id,currency,amount,status,provider_ref)
VALUES
  ('10000000-0000-7000-8000-000000000082',
   '10000000-0000-7000-8000-000000000001',
   '10000000-0000-7000-8000-000000000041',
   '10000000-0000-7000-8000-000000000081','CNY',500,'succeeded','intent-paid'),
  ('10000000-0000-7000-8000-000000000085',
   '10000000-0000-7000-8000-000000000001',
   '10000000-0000-7000-8000-000000000044',
   '10000000-0000-7000-8000-000000000081','CNY',300,'succeeded','intent-mixed'),
  ('10000000-0000-7000-8000-000000000087',
   '10000000-0000-7000-8000-000000000001',
   '10000000-0000-7000-8000-000000000046',
   '10000000-0000-7000-8000-000000000081','CNY',100,'succeeded','intent-retry'),
  ('10000000-0000-7000-8000-000000000089',
   '10000000-0000-7000-8000-000000000001',
   '10000000-0000-7000-8000-000000000047',
   '10000000-0000-7000-8000-000000000081','CNY',100,'succeeded','intent-race'),
  ('10000000-0000-7000-8000-0000000001b0',
   '10000000-0000-7000-8000-000000000001',
   '10000000-0000-7000-8000-000000000049',
   '10000000-0000-7000-8000-000000000081','CNY',500,'succeeded','intent-legacy-refunds'),
  ('20000000-0000-7000-8000-000000000089',
   '20000000-0000-7000-8000-000000000001',
   '20000000-0000-7000-8000-000000000048',
   '20000000-0000-7000-8000-000000000081','CNY',100,'succeeded','intent-rls-b');
INSERT INTO payments
  (id,tenant_id,order_id,payment_intent_id,provider_id,provider_payment_id,
   currency,amount,status)
VALUES
  ('10000000-0000-7000-8000-000000000083',
   '10000000-0000-7000-8000-000000000001',
   '10000000-0000-7000-8000-000000000041',
   '10000000-0000-7000-8000-000000000082',
   '10000000-0000-7000-8000-000000000081','payment-paid','CNY',500,'succeeded'),
  ('10000000-0000-7000-8000-000000000086',
   '10000000-0000-7000-8000-000000000001',
   '10000000-0000-7000-8000-000000000044',
   '10000000-0000-7000-8000-000000000085',
   '10000000-0000-7000-8000-000000000081','payment-mixed','CNY',300,'succeeded'),
  ('10000000-0000-7000-8000-000000000088',
   '10000000-0000-7000-8000-000000000001',
   '10000000-0000-7000-8000-000000000046',
   '10000000-0000-7000-8000-000000000087',
   '10000000-0000-7000-8000-000000000081','payment-retry','CNY',100,'succeeded'),
  ('10000000-0000-7000-8000-000000000090',
   '10000000-0000-7000-8000-000000000001',
   '10000000-0000-7000-8000-000000000047',
   '10000000-0000-7000-8000-000000000089',
   '10000000-0000-7000-8000-000000000081','payment-race','CNY',100,'succeeded'),
  ('10000000-0000-7000-8000-0000000001b1',
   '10000000-0000-7000-8000-000000000001',
   '10000000-0000-7000-8000-000000000049',
   '10000000-0000-7000-8000-0000000001b0',
   '10000000-0000-7000-8000-000000000081','payment-legacy-refunds','CNY',500,'succeeded'),
  ('20000000-0000-7000-8000-000000000090',
   '20000000-0000-7000-8000-000000000001',
   '20000000-0000-7000-8000-000000000048',
   '20000000-0000-7000-8000-000000000089',
   '20000000-0000-7000-8000-000000000081','payment-rls-b','CNY',100,'succeeded');

INSERT INTO refunds
  (id,tenant_id,order_id,payment_id,currency,amount,reason,status,requested_by,
   failure_message,created_at,updated_at)
VALUES
  ('10000000-0000-7000-8000-0000000001c1','10000000-0000-7000-8000-000000000001',
   '10000000-0000-7000-8000-000000000049','10000000-0000-7000-8000-0000000001b1',
   'CNY',10,'legacy pending','pending','10000000-0000-7000-8000-000000000011',
   NULL,'2026-07-01 00:00:00+00','2026-07-01 00:01:00+00'),
  ('10000000-0000-7000-8000-0000000001c2','10000000-0000-7000-8000-000000000001',
   '10000000-0000-7000-8000-000000000049','10000000-0000-7000-8000-0000000001b1',
   'CNY',20,'legacy approved','approved','10000000-0000-7000-8000-000000000011',
   NULL,'2026-07-01 00:02:00+00','2026-07-01 00:03:00+00'),
  ('10000000-0000-7000-8000-0000000001c3','10000000-0000-7000-8000-000000000001',
   '10000000-0000-7000-8000-000000000049','10000000-0000-7000-8000-0000000001b1',
   'CNY',30,'legacy processing','processing','10000000-0000-7000-8000-000000000011',
   NULL,'2026-07-01 00:04:00+00','2026-07-01 00:05:00+00'),
  ('10000000-0000-7000-8000-0000000001c4','10000000-0000-7000-8000-000000000001',
   '10000000-0000-7000-8000-000000000049','10000000-0000-7000-8000-0000000001b1',
   'CNY',40,'legacy failed','failed','10000000-0000-7000-8000-000000000011',
   'legacy provider failure','2026-07-01 00:06:00+00','2026-07-01 00:07:00+00'),
  ('10000000-0000-7000-8000-0000000001c5','10000000-0000-7000-8000-000000000001',
   '10000000-0000-7000-8000-000000000049','10000000-0000-7000-8000-0000000001b1',
   'CNY',50,'legacy rejected','rejected','10000000-0000-7000-8000-000000000011',
   'legacy approval rejection','2026-07-01 00:08:00+00','2026-07-01 00:09:00+00');

INSERT INTO plan_purchase_counters (tenant_id, plan_id, user_id, purchased)
VALUES ('10000000-0000-7000-8000-000000000001',
        '10000000-0000-7000-8000-000000000031',
        '10000000-0000-7000-8000-000000000011', 2);

INSERT INTO coupons
  (id, tenant_id, code, name, discount_type, discount_value, currency,
   max_redemptions, redeemed_count)
VALUES ('10000000-0000-7000-8000-000000000051',
        '10000000-0000-7000-8000-000000000001',
        'LEGACY100', 'Legacy coupon', 'fixed', 100, 'CNY', 2, 2);
UPDATE orders SET coupon_id = '10000000-0000-7000-8000-000000000051'
 WHERE id IN ('10000000-0000-7000-8000-000000000042',
              '10000000-0000-7000-8000-000000000045');
INSERT INTO coupon_redemptions
  (id, tenant_id, coupon_id, user_id, order_id, discount_amount, currency)
VALUES ('10000000-0000-7000-8000-000000000052',
        '10000000-0000-7000-8000-000000000001',
        '10000000-0000-7000-8000-000000000051',
        '10000000-0000-7000-8000-000000000011',
        '10000000-0000-7000-8000-000000000042', 100, 'CNY'),
       ('10000000-0000-7000-8000-000000000053',
        '10000000-0000-7000-8000-000000000001',
        '10000000-0000-7000-8000-000000000051',
        '10000000-0000-7000-8000-000000000011',
        '10000000-0000-7000-8000-000000000045', 100, 'CNY');

INSERT INTO ledger_accounts
  (id, tenant_id, account_type, normal_balance, currency, owner_user_id)
VALUES ('10000000-0000-7000-8000-000000000061',
        '10000000-0000-7000-8000-000000000001',
        'user_balance', 'credit', 'CNY',
        '10000000-0000-7000-8000-000000000011');
INSERT INTO ledger_accounts
  (id, tenant_id, account_type, normal_balance, currency, owner_ref)
VALUES ('10000000-0000-7000-8000-000000000062',
        '10000000-0000-7000-8000-000000000001',
        'suspense', 'debit', 'CNY', 'legacy_seed');
INSERT INTO ledger_transactions
  (id, tenant_id, kind, currency, source_type, memo, actor_kind)
VALUES ('10000000-0000-7000-8000-000000000063',
        '10000000-0000-7000-8000-000000000001',
        'balance_topup', 'CNY', 'test', 'legacy test balance', 'system');
INSERT INTO ledger_entries
  (tenant_id, transaction_id, account_id, direction, amount, currency)
VALUES
  ('10000000-0000-7000-8000-000000000001',
   '10000000-0000-7000-8000-000000000063',
   '10000000-0000-7000-8000-000000000062', 'debit', 1000, 'CNY'),
  ('10000000-0000-7000-8000-000000000001',
   '10000000-0000-7000-8000-000000000063',
   '10000000-0000-7000-8000-000000000061', 'credit', 1000, 'CNY');
SQL

up_sql "$ROOT/migrations/00036_order_reservations.sql" | psql_in >/dev/null

psql_in <<'SQL'
DO $$
DECLARE
  v_bad int;
BEGIN
  WITH expected(id,leg_status,request_status,key_status,response_code) AS (
    VALUES
      ('10000000-0000-7000-8000-0000000001c1'::uuid,'pending','pending','in_flight',NULL::int),
      ('10000000-0000-7000-8000-0000000001c2'::uuid,'approved','processing','in_flight',NULL::int),
      ('10000000-0000-7000-8000-0000000001c3'::uuid,'processing','processing','in_flight',NULL::int),
      ('10000000-0000-7000-8000-0000000001c4'::uuid,'failed','failed','failed',500),
      ('10000000-0000-7000-8000-0000000001c5'::uuid,'rejected','failed','failed',422)
  )
  SELECT count(*) INTO v_bad
    FROM expected e
    LEFT JOIN refunds r ON r.id=e.id
    LEFT JOIN refund_requests rr ON rr.id=e.id
    LEFT JOIN idempotency_keys k ON k.id=e.id
   WHERE r.id IS NULL OR rr.id IS NULL OR k.id IS NULL
      OR r.status<>e.leg_status OR rr.status<>e.request_status
      OR k.status<>e.key_status OR k.response_code IS DISTINCT FROM e.response_code
      OR k.resource_type<>'refund_request' OR k.resource_id<>e.id
      OR k.actor_id IS DISTINCT FROM r.requested_by
      OR k.request_hash<>rr.request_hash
      OR r.source_kind<>'payment' OR r.refund_request_id<>e.id
      OR r.ledger_txn_id IS NOT NULL OR r.balance_hold_id IS NOT NULL
      OR NOT rr.legacy_backfill
      OR (e.key_status='in_flight' AND
          (k.locked_until IS NULL OR k.response_body IS NOT NULL
           OR k.completed_at IS NOT NULL))
      OR (e.key_status='failed' AND
          (k.locked_until IS NOT NULL OR k.response_body IS NULL
           OR k.completed_at IS NULL));
  IF v_bad<>0 THEN
    RAISE EXCEPTION 'legacy refund state/evidence mapping has % bad rows',v_bad;
  END IF;
END $$;

-- Exercise legal updates from both active and failed migrated states. Each
-- transaction forces all deferred request/key/leg checks, then rolls back so
-- the original legacy evidence remains available for exact Down verification.
BEGIN;
UPDATE refunds SET status='processing'
 WHERE id='10000000-0000-7000-8000-0000000001c1';
UPDATE refund_requests SET status='processing'
 WHERE id='10000000-0000-7000-8000-0000000001c1';
SET CONSTRAINTS ALL IMMEDIATE;
ROLLBACK;

BEGIN;
UPDATE refunds SET status='failed',failure_message='sixth candidate transition'
 WHERE id='10000000-0000-7000-8000-0000000001c3';
UPDATE refund_requests SET status='failed'
 WHERE id='10000000-0000-7000-8000-0000000001c3';
UPDATE idempotency_keys
   SET status='failed',response_code=503,response_body='{"status":"failed"}',
       completed_at=now(),locked_until=NULL
 WHERE id='10000000-0000-7000-8000-0000000001c3';
SET CONSTRAINTS ALL IMMEDIATE;
ROLLBACK;

BEGIN;
UPDATE refunds SET status='processing',failure_message=NULL
 WHERE id='10000000-0000-7000-8000-0000000001c4';
UPDATE refund_requests SET status='processing'
 WHERE id='10000000-0000-7000-8000-0000000001c4';
UPDATE idempotency_keys
   SET status='in_flight',response_code=NULL,response_body=NULL,
       completed_at=NULL,locked_until=now()+interval '5 minutes'
 WHERE id='10000000-0000-7000-8000-0000000001c4';
SET CONSTRAINTS ALL IMMEDIATE;
ROLLBACK;
SELECT 'legacy_refund_state_migration=ok' AS result;

DO $$
DECLARE
  v_sold int;
  v_stock_held int;
  v_purchased int;
  v_purchase_held int;
  v_redeemed int;
  v_coupon_held int;
  v_available bigint;
  v_balance_held bigint;
  v_count int;
  v_reservation uuid;
  v_business uuid;
  v_caught boolean;
BEGIN
  SELECT stock_sold, stock_reserved INTO v_sold, v_stock_held
    FROM plans WHERE id = '10000000-0000-7000-8000-000000000031';
  IF (v_sold, v_stock_held) <> (1, 1) THEN
    RAISE EXCEPTION 'stock backfill mismatch: sold %, held %', v_sold, v_stock_held;
  END IF;

  SELECT purchased, reserved INTO v_purchased, v_purchase_held
    FROM plan_purchase_counters
   WHERE tenant_id = '10000000-0000-7000-8000-000000000001'
     AND plan_id = '10000000-0000-7000-8000-000000000031'
     AND user_id = '10000000-0000-7000-8000-000000000011';
  IF (v_purchased, v_purchase_held) <> (1, 1) THEN
    RAISE EXCEPTION 'purchase backfill mismatch: purchased %, held %',
      v_purchased, v_purchase_held;
  END IF;

  SELECT redeemed_count, reserved_count INTO v_redeemed, v_coupon_held
    FROM coupons WHERE id = '10000000-0000-7000-8000-000000000051';
  IF (v_redeemed, v_coupon_held) <> (1, 1) THEN
    RAISE EXCEPTION 'coupon backfill mismatch: redeemed %, held %',
      v_redeemed, v_coupon_held;
  END IF;

  SELECT -balance_signed INTO v_available
    FROM ledger_accounts
   WHERE id = '10000000-0000-7000-8000-000000000061';
  SELECT -a.balance_signed INTO v_balance_held
    FROM balance_holds h JOIN ledger_accounts a ON a.id = h.hold_account_id
   WHERE h.order_id = '10000000-0000-7000-8000-000000000042';
  IF (v_available, v_balance_held) <> (800, 200) THEN
    RAISE EXCEPTION 'balance conservation mismatch: available %, held %',
      v_available, v_balance_held;
  END IF;

  SELECT count(*) INTO v_count FROM order_reservations;
  IF v_count <> 9 THEN RAISE EXCEPTION 'expected 9 reservations, got %', v_count; END IF;

  SELECT r.id, o.business_request_id INTO v_reservation, v_business
    FROM order_reservations r JOIN orders o ON o.id = r.order_id
   WHERE r.order_id = '10000000-0000-7000-8000-000000000042';

  v_caught := false;
  BEGIN
    UPDATE orders SET status = 'fulfilled'
     WHERE id = '10000000-0000-7000-8000-000000000042';
  EXCEPTION WHEN check_violation THEN v_caught := true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'illegal order transition was accepted'; END IF;

  v_caught := false;
  BEGIN
    UPDATE plans SET stock_reserved = -1
     WHERE id = '10000000-0000-7000-8000-000000000031';
  EXCEPTION WHEN check_violation THEN v_caught := true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'negative stock was accepted'; END IF;

  v_caught := false;
  BEGIN
    INSERT INTO order_stock_reservations
      (tenant_id, reservation_id, order_id, plan_id, quantity)
    VALUES
      ('20000000-0000-7000-8000-000000000001', v_reservation,
       '10000000-0000-7000-8000-000000000042',
       '10000000-0000-7000-8000-000000000031', 1);
  EXCEPTION WHEN foreign_key_violation THEN v_caught := true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'cross-tenant reservation FK was accepted'; END IF;

  SELECT r.id, o.business_request_id INTO v_reservation, v_business
    FROM order_reservations r JOIN orders o ON o.id=r.order_id
   WHERE r.order_id='10000000-0000-7000-8000-000000000041';

  INSERT INTO products (id,tenant_id,code,name)
  VALUES
    ('10000000-0000-7000-8000-000000000022',
     '10000000-0000-7000-8000-000000000001','fk-probe','FK Probe');
  INSERT INTO plans
    (id,tenant_id,product_id,code,name,stock_total,stock_reserved)
  VALUES
    ('10000000-0000-7000-8000-000000000032',
     '10000000-0000-7000-8000-000000000001',
     '10000000-0000-7000-8000-000000000022','fk-probe','FK Probe',10,0);
  v_caught := false;
  BEGIN
    INSERT INTO order_stock_reservations
      (tenant_id,reservation_id,order_id,plan_id,quantity)
    VALUES
      ('10000000-0000-7000-8000-000000000001',v_reservation,
       '10000000-0000-7000-8000-000000000042',
       '10000000-0000-7000-8000-000000000032',1);
  EXCEPTION WHEN foreign_key_violation THEN v_caught := true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'same-tenant cross-order binding was accepted'; END IF;

  v_caught := false;
  BEGIN
    INSERT INTO order_stock_reservations
      (tenant_id,reservation_id,order_id,plan_id,quantity)
    VALUES
      ('10000000-0000-7000-8000-000000000001',v_reservation,
       '10000000-0000-7000-8000-000000000041',
       '10000000-0000-7000-8000-000000000032',1);
    SET CONSTRAINTS ALL IMMEDIATE;
  EXCEPTION WHEN check_violation THEN v_caught := true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'extra stock child was accepted'; END IF;

  v_caught := false;
  BEGIN
    INSERT INTO coupons
      (id,tenant_id,code,name,discount_type,discount_value,currency,
       max_redemptions,redeemed_count)
    VALUES
      ('10000000-0000-7000-8000-000000000091',
       '10000000-0000-7000-8000-000000000001','EXTRA','Extra','fixed',1,'CNY',1,1);
    INSERT INTO coupon_redemptions
      (tenant_id,coupon_id,user_id,order_id,discount_amount,currency,
       reservation_id,status,captured_at)
    VALUES
      ('10000000-0000-7000-8000-000000000001',
       '10000000-0000-7000-8000-000000000091',
       '10000000-0000-7000-8000-000000000011',
       '10000000-0000-7000-8000-000000000041',0,'CNY',v_reservation,'captured',now());
    SET CONSTRAINTS ALL IMMEDIATE;
  EXCEPTION WHEN check_violation THEN v_caught := true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'extra coupon child was accepted'; END IF;

  v_caught := false;
  BEGIN
    INSERT INTO order_reservation_events
      (tenant_id, reservation_id, order_id, from_state, to_state, event_kind,
       business_request_id)
    VALUES
      ('10000000-0000-7000-8000-000000000001', v_reservation,
       '10000000-0000-7000-8000-000000000041', 'held', 'captured', 'capture',
       v_business);
  EXCEPTION WHEN unique_violation THEN v_caught := true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'duplicate capture event was accepted'; END IF;

  v_caught := false;
  BEGIN
    INSERT INTO order_reservation_events
      (tenant_id,reservation_id,order_id,from_state,to_state,event_kind,business_request_id)
    VALUES
      ('10000000-0000-7000-8000-000000000001',v_reservation,
       '10000000-0000-7000-8000-000000000041','held','released','release',v_business);
  EXCEPTION WHEN unique_violation THEN v_caught := true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'captured+released terminal events coexisted'; END IF;

  v_caught := false;
  BEGIN
    UPDATE order_items SET quantity=2
     WHERE order_id='10000000-0000-7000-8000-000000000041';
  EXCEPTION WHEN check_violation THEN v_caught := true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'order item snapshot was mutable'; END IF;

  v_caught := false;
  BEGIN
    DELETE FROM order_items
     WHERE order_id='10000000-0000-7000-8000-000000000041';
  EXCEPTION WHEN check_violation THEN v_caught := true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'order item snapshot was deletable'; END IF;

  v_caught := false;
  BEGIN
    UPDATE orders SET subtotal_amount=subtotal_amount+1,
                      total_amount=total_amount+1,
                      payable_amount=payable_amount+1
     WHERE id='10000000-0000-7000-8000-000000000041';
  EXCEPTION WHEN check_violation THEN v_caught := true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'fulfilled order financial snapshot was mutable'; END IF;

  v_caught := false;
  BEGIN
    INSERT INTO order_items
      (tenant_id,order_id,snapshot_product_name,quantity,unit_amount,line_amount,currency)
    VALUES
      ('20000000-0000-7000-8000-000000000001',
       '10000000-0000-7000-8000-000000000041','cross tenant',1,1,1,'CNY');
  EXCEPTION WHEN foreign_key_violation THEN v_caught := true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'cross-tenant order item FK was accepted'; END IF;

  v_caught := false;
  BEGIN
    INSERT INTO order_items
      (tenant_id,order_id,snapshot_product_name,quantity,unit_amount,line_amount,currency)
    VALUES
      ('10000000-0000-7000-8000-000000000001',
       '10000000-0000-7000-8000-000000000041','extra completed line',1,1,1,'CNY');
    SET CONSTRAINTS ALL IMMEDIATE;
  EXCEPTION WHEN check_violation THEN v_caught := true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'completed order accepted an extra item'; END IF;

  v_caught := false;
  BEGIN
    INSERT INTO payments
      (tenant_id,order_id,payment_intent_id,provider_id,provider_payment_id,
       currency,amount,status)
    VALUES
      ('10000000-0000-7000-8000-000000000001',
       '10000000-0000-7000-8000-000000000042',
       '10000000-0000-7000-8000-000000000082',
       '10000000-0000-7000-8000-000000000081','wrong-intent-order','CNY',200,'succeeded');
  EXCEPTION WHEN foreign_key_violation THEN v_caught := true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'payment accepted a cross-order intent'; END IF;

  INSERT INTO payment_providers
    (id,tenant_id,code,adapter,display_name)
  VALUES
    ('10000000-0000-7000-8000-000000000084',
     '10000000-0000-7000-8000-000000000001','testpay-b','test','Test Pay B');
  v_caught := false;
  BEGIN
    INSERT INTO payments
      (tenant_id,order_id,payment_intent_id,provider_id,provider_payment_id,
       currency,amount,status)
    VALUES
      ('10000000-0000-7000-8000-000000000001',
       '10000000-0000-7000-8000-000000000041',
       '10000000-0000-7000-8000-000000000082',
       '10000000-0000-7000-8000-000000000084','wrong-intent-provider','CNY',500,'succeeded');
  EXCEPTION WHEN foreign_key_violation THEN v_caught := true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'payment accepted a cross-provider intent'; END IF;

  v_caught := false;
  BEGIN
    INSERT INTO refunds
      (tenant_id,order_id,payment_id,currency,amount,reason)
    VALUES
      ('10000000-0000-7000-8000-000000000001',
       '10000000-0000-7000-8000-000000000042',
       '10000000-0000-7000-8000-000000000083','CNY',1,'wrong order probe');
  EXCEPTION WHEN foreign_key_violation THEN v_caught := true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'refund accepted a cross-order payment'; END IF;

  v_caught := false;
  BEGIN
    UPDATE order_reservations SET expires_at=expires_at+interval '1 hour'
     WHERE id=v_reservation;
  EXCEPTION WHEN check_violation THEN v_caught := true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'reservation identity was mutable'; END IF;

  v_caught := false;
  BEGIN
    UPDATE balance_holds SET amount=amount+1
     WHERE order_id='10000000-0000-7000-8000-000000000042';
  EXCEPTION WHEN check_violation THEN v_caught := true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'balance hold amount was mutable'; END IF;

  v_caught := false;
  BEGIN
    INSERT INTO ledger_transactions
      (tenant_id,kind,currency,source_type,source_id,actor_kind)
    VALUES
      ('10000000-0000-7000-8000-000000000001','balance_hold','CNY','order',
       '10000000-0000-7000-8000-000000000042','system');
  EXCEPTION WHEN unique_violation THEN v_caught := true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'duplicate balance business txn was accepted'; END IF;
END;
$$;

-- Refund truth model: payment and captured balance are distinct sources, every
-- successful refund has a balanced ledger transaction, and cached totals only
-- move with successful facts at the deferred commit boundary.
CREATE FUNCTION pg_temp.create_refund_request(
  p_id uuid,p_order uuid,p_currency text,p_amount bigint,p_reason text
) RETURNS uuid
LANGUAGE plpgsql AS $$
DECLARE
  v_hash bytea:=decode(md5(p_id::text||':'||p_order::text||':'||p_amount::text),'hex');
BEGIN
  INSERT INTO idempotency_keys
    (id,tenant_id,scope,idempotency_key,request_hash,status,actor_id,
     locked_until)
  VALUES
    (p_id,'10000000-0000-7000-8000-000000000001','refund_create',
     'refund-test:'||p_id::text,v_hash,'in_flight',
     '10000000-0000-7000-8000-000000000011',now()+interval '5 minutes');
  UPDATE idempotency_keys SET resource_type='refund_request',resource_id=p_id
   WHERE id=p_id;
  INSERT INTO refund_requests
    (id,tenant_id,order_id,currency,requested_amount,business_request_id,
     request_hash,requested_by,reason)
  VALUES
    (p_id,'10000000-0000-7000-8000-000000000001',p_order,p_currency,p_amount,p_id,
     v_hash,'10000000-0000-7000-8000-000000000011',p_reason);
  RETURN p_id;
END;
$$;

DO $$
DECLARE
  v_revenue uuid;
  v_channel uuid;
  v_balance_full uuid;
  v_balance_mixed uuid;
  v_refund uuid;
  v_request uuid;
  v_request_2 uuid;
  v_txn uuid;
  v_delta bigint;
  v_total bigint := 0;
  v_caught boolean;
  v_fulfilled timestamptz;
  v_count int;
BEGIN
  INSERT INTO ledger_accounts
    (tenant_id,account_type,normal_balance,currency,owner_ref)
  VALUES
    ('10000000-0000-7000-8000-000000000001','platform_revenue','credit','CNY',
     'main') RETURNING id INTO v_revenue;
  INSERT INTO ledger_accounts
    (tenant_id,account_type,normal_balance,currency,owner_ref)
  VALUES
    ('10000000-0000-7000-8000-000000000001','channel_cash','debit','CNY',
     'testpay') RETURNING id INTO v_channel;
  SELECT id INTO v_balance_full FROM balance_holds
   WHERE order_id='10000000-0000-7000-8000-000000000043';
  SELECT id INTO v_balance_mixed FROM balance_holds
   WHERE order_id='10000000-0000-7000-8000-000000000044';

  v_caught:=false;
  BEGIN
    v_request:=uuidv7();
    PERFORM pg_temp.create_refund_request(v_request,
      '10000000-0000-7000-8000-000000000043','CNY',1,'no source');
    INSERT INTO refunds
      (tenant_id,refund_request_id,order_id,payment_id,source_kind,currency,amount,reason)
    VALUES
      ('10000000-0000-7000-8000-000000000001',v_request,
       '10000000-0000-7000-8000-000000000043',NULL,'payment','CNY',1,'no source');
  EXCEPTION WHEN foreign_key_violation THEN v_caught:=true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'payment refund without payment was accepted'; END IF;

  v_caught:=false;
  BEGIN
    v_request:=uuidv7();
    PERFORM pg_temp.create_refund_request(v_request,
      '10000000-0000-7000-8000-000000000044','CNY',1,'double source');
    INSERT INTO refunds
      (tenant_id,refund_request_id,order_id,payment_id,balance_hold_id,
       source_kind,currency,amount,reason)
    VALUES
      ('10000000-0000-7000-8000-000000000001',v_request,
       '10000000-0000-7000-8000-000000000044',
       '10000000-0000-7000-8000-000000000086',v_balance_mixed,
       'payment','CNY',1,'double source');
  EXCEPTION WHEN check_violation THEN v_caught:=true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'double-bound refund was accepted'; END IF;

  v_caught:=false;
  BEGIN
    v_request:=uuidv7();
    PERFORM pg_temp.create_refund_request(v_request,
      '10000000-0000-7000-8000-000000000044','CNY',1,'wrong order');
    INSERT INTO refunds
      (tenant_id,refund_request_id,order_id,balance_hold_id,source_kind,currency,amount,reason)
    VALUES
      ('10000000-0000-7000-8000-000000000001',v_request,
       '10000000-0000-7000-8000-000000000044',v_balance_full,
       'balance','CNY',1,'wrong order');
  EXCEPTION WHEN foreign_key_violation THEN v_caught:=true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'balance refund crossed orders'; END IF;

  v_caught:=false;
  BEGIN
    v_request:=uuidv7();
    PERFORM pg_temp.create_refund_request(v_request,
      '10000000-0000-7000-8000-000000000043','CNY',1,'wrong order');
    INSERT INTO refunds
      (tenant_id,refund_request_id,order_id,payment_id,source_kind,currency,amount,reason)
    VALUES
      ('10000000-0000-7000-8000-000000000001',v_request,
       '10000000-0000-7000-8000-000000000043',
       '10000000-0000-7000-8000-000000000086','payment','CNY',1,'wrong order');
  EXCEPTION WHEN foreign_key_violation THEN v_caught:=true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'payment refund crossed orders'; END IF;

  v_caught:=false;
  BEGIN
    v_request:=uuidv7();
    PERFORM pg_temp.create_refund_request(v_request,
      '10000000-0000-7000-8000-000000000044','CNY',1,'wrong currency');
    INSERT INTO refunds
      (tenant_id,refund_request_id,order_id,payment_id,source_kind,currency,amount,reason)
    VALUES
      ('10000000-0000-7000-8000-000000000001',v_request,
       '10000000-0000-7000-8000-000000000044',
       '10000000-0000-7000-8000-000000000086','payment','USD',1,'wrong currency');
  EXCEPTION WHEN foreign_key_violation THEN v_caught:=true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'payment refund crossed currency'; END IF;

  v_caught:=false;
  BEGIN
    v_request:=uuidv7();
    PERFORM pg_temp.create_refund_request(v_request,
      '10000000-0000-7000-8000-000000000043','CNY',101,'over source');
    INSERT INTO refunds
      (tenant_id,refund_request_id,order_id,balance_hold_id,source_kind,currency,amount,reason)
    VALUES
      ('10000000-0000-7000-8000-000000000001',v_request,
       '10000000-0000-7000-8000-000000000043',v_balance_full,
       'balance','CNY',101,'over source');
  EXCEPTION WHEN check_violation THEN v_caught:=true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'balance refund exceeded its source'; END IF;

  v_caught:=false;
  BEGIN
    v_request:=uuidv7();
    PERFORM pg_temp.create_refund_request(v_request,
      '10000000-0000-7000-8000-000000000043','CNY',60,'active one');
    INSERT INTO refunds
      (tenant_id,refund_request_id,order_id,balance_hold_id,source_kind,currency,amount,reason)
    VALUES
      ('10000000-0000-7000-8000-000000000001',v_request,
       '10000000-0000-7000-8000-000000000043',v_balance_full,
       'balance','CNY',60,'active one');
    v_request_2:=uuidv7();
    PERFORM pg_temp.create_refund_request(v_request_2,
      '10000000-0000-7000-8000-000000000043','CNY',50,'active two');
    INSERT INTO refunds
      (tenant_id,refund_request_id,order_id,balance_hold_id,source_kind,currency,amount,reason)
    VALUES
      ('10000000-0000-7000-8000-000000000001',v_request_2,
       '10000000-0000-7000-8000-000000000043',v_balance_full,
       'balance','CNY',50,'active two');
  EXCEPTION WHEN check_violation THEN v_caught:=true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'cumulative active refunds exceeded source'; END IF;

  -- A failed attempt releases capacity.  Re-opening the same actor-bound
  -- request and leg occupies capacity again and may then succeed with exact
  -- provider/ledger evidence.
  PERFORM pg_temp.create_refund_request(
    '10000000-0000-7000-8000-0000000000f1',
    '10000000-0000-7000-8000-000000000046','CNY',60,'retryable refund');
  INSERT INTO refunds
    (id,tenant_id,refund_request_id,order_id,payment_id,
     source_kind,currency,amount,reason)
  VALUES
    ('10000000-0000-7000-8000-0000000000c1',
     '10000000-0000-7000-8000-000000000001',
     '10000000-0000-7000-8000-0000000000f1',
     '10000000-0000-7000-8000-000000000046',
     '10000000-0000-7000-8000-000000000088',
     'payment','CNY',60,'retryable refund');
  UPDATE refund_requests SET status='processing'
   WHERE id='10000000-0000-7000-8000-0000000000f1';
  UPDATE refunds SET status='processing'
   WHERE id='10000000-0000-7000-8000-0000000000c1';
  UPDATE refunds SET status='failed',failure_message='provider timeout'
   WHERE id='10000000-0000-7000-8000-0000000000c1';
  UPDATE refund_requests SET status='failed'
   WHERE id='10000000-0000-7000-8000-0000000000f1';
  UPDATE idempotency_keys
     SET status='failed',response_code=503,
         response_body='{"status":"failed"}',completed_at=now(),locked_until=NULL
   WHERE id='10000000-0000-7000-8000-0000000000f1';
  SET CONSTRAINTS ALL IMMEDIATE;
  SET CONSTRAINTS ALL DEFERRED;

  -- With the failed leg excluded, a full-source attempt is accepted and can
  -- itself fail cleanly without changing either cache.
  PERFORM pg_temp.create_refund_request(
    '10000000-0000-7000-8000-0000000000f2',
    '10000000-0000-7000-8000-000000000046','CNY',100,'capacity release proof');
  INSERT INTO refunds
    (id,tenant_id,refund_request_id,order_id,payment_id,
     source_kind,currency,amount,reason)
  VALUES
    ('10000000-0000-7000-8000-0000000000c3',
     '10000000-0000-7000-8000-000000000001',
     '10000000-0000-7000-8000-0000000000f2',
     '10000000-0000-7000-8000-000000000046',
     '10000000-0000-7000-8000-000000000088',
     'payment','CNY',100,'capacity release proof');
  UPDATE refund_requests SET status='processing'
   WHERE id='10000000-0000-7000-8000-0000000000f2';
  UPDATE refunds SET status='processing'
   WHERE id='10000000-0000-7000-8000-0000000000c3';
  UPDATE refunds SET status='failed',failure_message='test failure'
   WHERE id='10000000-0000-7000-8000-0000000000c3';
  UPDATE refund_requests SET status='failed'
   WHERE id='10000000-0000-7000-8000-0000000000f2';
  UPDATE idempotency_keys
     SET status='failed',response_code=500,
         response_body='{"status":"failed"}',completed_at=now(),locked_until=NULL
   WHERE id='10000000-0000-7000-8000-0000000000f2';
  SET CONSTRAINTS ALL IMMEDIATE;
  SET CONSTRAINTS ALL DEFERRED;

  UPDATE idempotency_keys
     SET status='in_flight',response_code=NULL,response_body=NULL,
         completed_at=NULL,locked_until=now()+interval '5 minutes'
   WHERE id='10000000-0000-7000-8000-0000000000f1';
  UPDATE refund_requests SET status='processing'
   WHERE id='10000000-0000-7000-8000-0000000000f1';
  UPDATE refunds SET status='processing',failure_message=NULL
   WHERE id='10000000-0000-7000-8000-0000000000c1';
  v_caught:=false;
  BEGIN
    v_request:=uuidv7();
    PERFORM pg_temp.create_refund_request(v_request,
      '10000000-0000-7000-8000-000000000046','CNY',50,'retry occupancy proof');
    INSERT INTO refunds
      (tenant_id,refund_request_id,order_id,payment_id,
       source_kind,currency,amount,reason)
    VALUES
      ('10000000-0000-7000-8000-000000000001',v_request,
       '10000000-0000-7000-8000-000000000046',
       '10000000-0000-7000-8000-000000000088',
       'payment','CNY',50,'retry occupancy proof');
  EXCEPTION WHEN check_violation THEN v_caught:=true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'retrying leg did not reoccupy capacity'; END IF;
  INSERT INTO ledger_transactions
    (id,tenant_id,kind,currency,source_type,source_id,memo,actor_kind)
  VALUES
    ('10000000-0000-7000-8000-0000000000c2',
     '10000000-0000-7000-8000-000000000001','refund_issued','CNY','refund',
     '10000000-0000-7000-8000-0000000000c1','retry success','system');
  INSERT INTO ledger_entries
    (tenant_id,transaction_id,account_id,direction,amount,currency)
  VALUES
    ('10000000-0000-7000-8000-000000000001',
     '10000000-0000-7000-8000-0000000000c2',v_revenue,'debit',60,'CNY'),
    ('10000000-0000-7000-8000-000000000001',
     '10000000-0000-7000-8000-0000000000c2',v_channel,'credit',60,'CNY');
  UPDATE refunds SET status='succeeded',provider_refund_id='retry-provider-60',
         ledger_txn_id='10000000-0000-7000-8000-0000000000c2',succeeded_at=now()
   WHERE id='10000000-0000-7000-8000-0000000000c1';
  UPDATE refund_requests SET status='succeeded'
   WHERE id='10000000-0000-7000-8000-0000000000f1';
  UPDATE idempotency_keys
     SET status='succeeded',response_code=200,
         response_body='{"status":"succeeded"}',completed_at=now(),locked_until=NULL
   WHERE id='10000000-0000-7000-8000-0000000000f1';
  UPDATE payments SET status='partially_refunded',refunded_amount=60
   WHERE id='10000000-0000-7000-8000-000000000088';
  UPDATE orders SET status='partially_refunded',refunded_amount=60
   WHERE id='10000000-0000-7000-8000-000000000046';
  SET CONSTRAINTS ALL IMMEDIATE;
  SET CONSTRAINTS ALL DEFERRED;

  -- A fulfilled, externally-zero-pay order can refund its captured balance.
  PERFORM pg_temp.create_refund_request(
    '10000000-0000-7000-8000-0000000000e1',
    '10000000-0000-7000-8000-000000000043','CNY',100,
    'full balance refund');
  INSERT INTO refunds
    (id,tenant_id,refund_request_id,order_id,balance_hold_id,
     source_kind,currency,amount,reason)
  VALUES
    ('10000000-0000-7000-8000-0000000000a1',
     '10000000-0000-7000-8000-000000000001',
     '10000000-0000-7000-8000-0000000000e1',
     '10000000-0000-7000-8000-000000000043',v_balance_full,
     'balance','CNY',100,'full balance refund');
  INSERT INTO ledger_transactions
    (id,tenant_id,kind,currency,source_type,source_id,memo,actor_kind)
  VALUES
    ('10000000-0000-7000-8000-0000000000b1',
     '10000000-0000-7000-8000-000000000001','refund_issued','CNY','refund',
     '10000000-0000-7000-8000-0000000000a1','full balance refund','system');
  INSERT INTO ledger_entries
    (tenant_id,transaction_id,account_id,direction,amount,currency)
  VALUES
    ('10000000-0000-7000-8000-000000000001',
     '10000000-0000-7000-8000-0000000000b1',v_revenue,'debit',100,'CNY'),
    ('10000000-0000-7000-8000-000000000001',
     '10000000-0000-7000-8000-0000000000b1',
     '10000000-0000-7000-8000-000000000061','credit',100,'CNY');
  UPDATE refund_requests SET status='processing'
   WHERE id='10000000-0000-7000-8000-0000000000e1';
  UPDATE refunds SET status='processing'
   WHERE id='10000000-0000-7000-8000-0000000000a1';
  UPDATE refunds SET status='succeeded',
         ledger_txn_id='10000000-0000-7000-8000-0000000000b1',succeeded_at=now()
   WHERE id='10000000-0000-7000-8000-0000000000a1';
  UPDATE refund_requests SET status='succeeded'
   WHERE id='10000000-0000-7000-8000-0000000000e1';
  UPDATE idempotency_keys
     SET status='succeeded',response_code=200,
         response_body='{"status":"succeeded"}',completed_at=now(),locked_until=NULL
   WHERE id='10000000-0000-7000-8000-0000000000e1';
  v_caught:=false;
  BEGIN
    INSERT INTO ledger_entries
      (tenant_id,transaction_id,account_id,direction,amount,currency)
    VALUES
      ('10000000-0000-7000-8000-000000000001',
       '10000000-0000-7000-8000-0000000000b1',v_revenue,'debit',1,'CNY'),
      ('10000000-0000-7000-8000-000000000001',
       '10000000-0000-7000-8000-0000000000b1',
       '10000000-0000-7000-8000-000000000061','credit',1,'CNY');
  EXCEPTION WHEN check_violation THEN v_caught:=true;
  END;
  IF NOT v_caught THEN
    RAISE EXCEPTION 'successful refund ledger accepted a balanced append';
  END IF;
  SELECT fulfilled_at INTO v_fulfilled FROM orders
   WHERE id='10000000-0000-7000-8000-000000000043';
  UPDATE orders SET status='refunded',refunded_amount=100
   WHERE id='10000000-0000-7000-8000-000000000043';
  SET CONSTRAINTS ALL IMMEDIATE;
  IF (SELECT fulfilled_at FROM orders
       WHERE id='10000000-0000-7000-8000-000000000043') IS DISTINCT FROM v_fulfilled THEN
    RAISE EXCEPTION 'zero-pay refund erased fulfilment evidence';
  END IF;
  SET CONSTRAINTS ALL DEFERRED;

  -- Mixed funding accepts one payment-source and one balance-source fact.
  PERFORM pg_temp.create_refund_request(
    '10000000-0000-7000-8000-0000000000e2',
    '10000000-0000-7000-8000-000000000044','CNY',150,
    'mixed-source refund');
  INSERT INTO refunds
    (id,tenant_id,refund_request_id,order_id,payment_id,
     source_kind,currency,amount,reason)
  VALUES
    ('10000000-0000-7000-8000-0000000000a2',
     '10000000-0000-7000-8000-000000000001',
     '10000000-0000-7000-8000-0000000000e2',
     '10000000-0000-7000-8000-000000000044',
     '10000000-0000-7000-8000-000000000086','payment','CNY',100,'mixed payment');
  INSERT INTO ledger_transactions
    (id,tenant_id,kind,currency,source_type,source_id,memo,actor_kind)
  VALUES
    ('10000000-0000-7000-8000-0000000000b2',
     '10000000-0000-7000-8000-000000000001','refund_issued','CNY','refund',
     '10000000-0000-7000-8000-0000000000a2','mixed payment refund','system');
  INSERT INTO ledger_entries
    (tenant_id,transaction_id,account_id,direction,amount,currency)
  VALUES
    ('10000000-0000-7000-8000-000000000001',
     '10000000-0000-7000-8000-0000000000b2',v_revenue,'debit',100,'CNY'),
    ('10000000-0000-7000-8000-000000000001',
     '10000000-0000-7000-8000-0000000000b2',v_channel,'credit',100,'CNY');
  UPDATE refund_requests SET status='processing'
   WHERE id='10000000-0000-7000-8000-0000000000e2';
  UPDATE refunds SET status='processing'
   WHERE id='10000000-0000-7000-8000-0000000000a2';
  UPDATE refunds SET status='succeeded',provider_refund_id='mixed-provider-100',
         ledger_txn_id='10000000-0000-7000-8000-0000000000b2',succeeded_at=now()
   WHERE id='10000000-0000-7000-8000-0000000000a2';

  INSERT INTO refunds
    (id,tenant_id,refund_request_id,order_id,balance_hold_id,
     source_kind,currency,amount,reason)
  VALUES
    ('10000000-0000-7000-8000-0000000000a3',
     '10000000-0000-7000-8000-000000000001',
     '10000000-0000-7000-8000-0000000000e2',
     '10000000-0000-7000-8000-000000000044',v_balance_mixed,
     'balance','CNY',50,'mixed balance');
  INSERT INTO ledger_transactions
    (id,tenant_id,kind,currency,source_type,source_id,memo,actor_kind)
  VALUES
    ('10000000-0000-7000-8000-0000000000b3',
     '10000000-0000-7000-8000-000000000001','refund_issued','CNY','refund',
     '10000000-0000-7000-8000-0000000000a3','mixed balance refund','system');
  INSERT INTO ledger_entries
    (tenant_id,transaction_id,account_id,direction,amount,currency)
  VALUES
    ('10000000-0000-7000-8000-000000000001',
     '10000000-0000-7000-8000-0000000000b3',v_revenue,'debit',50,'CNY'),
    ('10000000-0000-7000-8000-000000000001',
     '10000000-0000-7000-8000-0000000000b3',
     '10000000-0000-7000-8000-000000000061','credit',50,'CNY');
  UPDATE refunds SET status='processing'
   WHERE id='10000000-0000-7000-8000-0000000000a3';
  UPDATE refunds SET status='succeeded',
         ledger_txn_id='10000000-0000-7000-8000-0000000000b3',succeeded_at=now()
   WHERE id='10000000-0000-7000-8000-0000000000a3';
  UPDATE refund_requests SET status='succeeded'
   WHERE id='10000000-0000-7000-8000-0000000000e2';
  UPDATE idempotency_keys
     SET status='succeeded',response_code=200,
         response_body='{"status":"succeeded"}',completed_at=now(),locked_until=NULL
   WHERE id='10000000-0000-7000-8000-0000000000e2';
  UPDATE payments SET status='partially_refunded',refunded_amount=100
   WHERE id='10000000-0000-7000-8000-000000000086';
  UPDATE orders SET status='partially_refunded',refunded_amount=150
   WHERE id='10000000-0000-7000-8000-000000000044';
  SET CONSTRAINTS ALL IMMEDIATE;
  SET CONSTRAINTS ALL DEFERRED;

  -- Repeated payment refunds advance partial caches monotonically to full.
  FOREACH v_delta IN ARRAY ARRAY[100::bigint,200::bigint,200::bigint] LOOP
    v_request:=uuidv7();
    PERFORM pg_temp.create_refund_request(
      v_request,'10000000-0000-7000-8000-000000000041','CNY',v_delta,
      'monotonic payment refund');
    INSERT INTO refunds
      (tenant_id,refund_request_id,order_id,payment_id,
       source_kind,currency,amount,reason)
    VALUES
      ('10000000-0000-7000-8000-000000000001',v_request,
       '10000000-0000-7000-8000-000000000041',
       '10000000-0000-7000-8000-000000000083','payment','CNY',v_delta,
       'monotonic payment refund') RETURNING id INTO v_refund;
    INSERT INTO ledger_transactions
      (tenant_id,kind,currency,source_type,source_id,memo,actor_kind)
    VALUES
      ('10000000-0000-7000-8000-000000000001','refund_issued','CNY','refund',
       v_refund,'monotonic payment refund','system') RETURNING id INTO v_txn;
    INSERT INTO ledger_entries
      (tenant_id,transaction_id,account_id,direction,amount,currency)
    VALUES
      ('10000000-0000-7000-8000-000000000001',v_txn,v_revenue,'debit',v_delta,'CNY'),
      ('10000000-0000-7000-8000-000000000001',v_txn,v_channel,'credit',v_delta,'CNY');
    UPDATE refund_requests SET status='processing' WHERE id=v_request;
    UPDATE refunds SET status='processing' WHERE id=v_refund;
    UPDATE refunds SET status='succeeded',provider_refund_id='provider-'||v_refund::text,
           ledger_txn_id=v_txn,succeeded_at=now() WHERE id=v_refund;
    UPDATE refund_requests SET status='succeeded'
     WHERE id=v_request;
    UPDATE idempotency_keys
       SET status='succeeded',response_code=200,
           response_body='{"status":"succeeded"}',completed_at=now(),locked_until=NULL
     WHERE id=v_request;
    v_total:=v_total+v_delta;
    UPDATE payments
       SET status=CASE WHEN v_total=amount THEN 'refunded' ELSE 'partially_refunded' END,
           refunded_amount=v_total
     WHERE id='10000000-0000-7000-8000-000000000083';
    UPDATE orders
       SET status=CASE WHEN v_total=paid_amount THEN 'refunded' ELSE 'partially_refunded' END,
           refunded_amount=v_total
     WHERE id='10000000-0000-7000-8000-000000000041';
    SET CONSTRAINTS ALL IMMEDIATE;
    SET CONSTRAINTS ALL DEFERRED;
  END LOOP;

  SELECT count(*) INTO v_count FROM refunds
   WHERE order_id='10000000-0000-7000-8000-000000000045';
  IF v_count<>0 THEN RAISE EXCEPTION 'coupon-only zero-total order created refund facts'; END IF;
  IF EXISTS (SELECT 1 FROM app.verify_ledger_all()) THEN
    RAISE EXCEPTION 'refund postings created ledger drift';
  END IF;
END;
$$;
SELECT 'refund_finalized_balanced_append=ok' AS result;
SELECT 'refund_failed_retry=ok' AS result;

-- A real tenant-B request/leg is retained for forced-RLS visibility tests.
BEGIN;
INSERT INTO idempotency_keys
  (id,tenant_id,scope,idempotency_key,request_hash,status,actor_id,
   locked_until)
VALUES
  ('20000000-0000-7000-8000-0000000000e1',
   '20000000-0000-7000-8000-000000000001','refund_create','rls-refund-b',
   decode(md5('rls-refund-b'),'hex'),'in_flight',
   '20000000-0000-7000-8000-000000000011',now()+interval '5 minutes');
UPDATE idempotency_keys SET resource_type='refund_request',
       resource_id='20000000-0000-7000-8000-0000000000e1'
 WHERE id='20000000-0000-7000-8000-0000000000e1';
INSERT INTO refund_requests
  (id,tenant_id,order_id,currency,requested_amount,business_request_id,
   request_hash,requested_by,reason)
VALUES
  ('20000000-0000-7000-8000-0000000000e1',
   '20000000-0000-7000-8000-000000000001',
   '20000000-0000-7000-8000-000000000048','CNY',10,
   '20000000-0000-7000-8000-0000000000e1',decode(md5('rls-refund-b'),'hex'),
   '20000000-0000-7000-8000-000000000011','tenant B RLS evidence');
INSERT INTO refunds
  (id,tenant_id,refund_request_id,order_id,payment_id,
   source_kind,currency,amount,reason)
VALUES
  ('20000000-0000-7000-8000-0000000000a1',
   '20000000-0000-7000-8000-000000000001',
   '20000000-0000-7000-8000-0000000000e1',
   '20000000-0000-7000-8000-000000000048',
   '20000000-0000-7000-8000-000000000090','payment','CNY',10,
   'tenant B RLS evidence');
COMMIT;

DO $$
DECLARE v_caught boolean:=false;
BEGIN
  BEGIN
    UPDATE idempotency_keys
       SET status='succeeded',response_code=200,response_body='{"forged":true}',
           completed_at=now(),locked_until=NULL
     WHERE id='20000000-0000-7000-8000-0000000000e1';
    SET CONSTRAINTS ALL IMMEDIATE;
  EXCEPTION WHEN check_violation THEN v_caught:=true;
  END;
  IF NOT v_caught THEN
    RAISE EXCEPTION 'key succeeded while refund request remained nonterminal';
  END IF;
END $$;

ALTER TABLE idempotency_keys DISABLE TRIGGER zz_idempotency_evidence_guard;
DO $$
DECLARE v_caught boolean:=false;
BEGIN
  BEGIN
    UPDATE idempotency_keys
       SET status='in_flight',response_code=NULL,response_body=NULL,
           completed_at=NULL,locked_until=now()+interval '5 minutes'
     WHERE id='10000000-0000-7000-8000-0000000000e1';
    SET CONSTRAINTS ALL IMMEDIATE;
  EXCEPTION WHEN check_violation THEN v_caught:=true;
  END;
  IF NOT v_caught THEN
    RAISE EXCEPTION 'refund request succeeded while key remained in_flight';
  END IF;
END $$;
ALTER TABLE idempotency_keys ENABLE TRIGGER zz_idempotency_evidence_guard;
SELECT 'refund_idempotency_bidirectional=ok' AS result;

BEGIN;
INSERT INTO idempotency_keys
  (id,tenant_id,scope,idempotency_key,request_hash,status,actor_id,
   locked_until)
VALUES
  ('10000000-0000-7000-8000-0000000000e3',
   '10000000-0000-7000-8000-000000000001','refund_create','cross-actor-proof',
   decode(md5('cross-actor-proof'),'hex'),'in_flight',
   '10000000-0000-7000-8000-000000000011',now()+interval '5 minutes');
UPDATE idempotency_keys SET resource_type='refund_request',
       resource_id='10000000-0000-7000-8000-0000000000e3'
 WHERE id='10000000-0000-7000-8000-0000000000e3';
ALTER TABLE refund_requests DISABLE TRIGGER zz_refund_requests_guard;
DO $$
DECLARE v_caught boolean:=false;
BEGIN
  BEGIN
    INSERT INTO refund_requests
      (id,tenant_id,order_id,currency,requested_amount,business_request_id,
       request_hash,requested_by,reason)
    VALUES
      ('10000000-0000-7000-8000-0000000000e3',
       '10000000-0000-7000-8000-000000000001',
       '10000000-0000-7000-8000-000000000047','CNY',1,
       '10000000-0000-7000-8000-0000000000e3',
       decode(md5('cross-actor-proof'),'hex'),
       '20000000-0000-7000-8000-000000000011','cross-tenant actor');
  EXCEPTION WHEN foreign_key_violation THEN v_caught:=true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'cross-tenant refund requester was accepted'; END IF;
END $$;
ROLLBACK;

-- Schema-35 writers are a deliberate kill-switch after the stop-the-world
-- migration. new, renewal and topup must all be upgraded to persist a claimed
-- idempotency record and the complete reservation graph in one transaction.
DO $$
DECLARE v_kind text; v_caught boolean;
BEGIN
  FOREACH v_kind IN ARRAY ARRAY['new','renewal','topup'] LOOP
    v_caught := false;
    BEGIN
      INSERT INTO orders
        (tenant_id,order_no,user_id,kind,status,currency,subtotal_amount,
         discount_amount,tax_amount,balance_applied,total_amount,payable_amount)
      VALUES
        ('10000000-0000-7000-8000-000000000001','OLD-'||v_kind,
         '10000000-0000-7000-8000-000000000011',v_kind,'pending_payment','CNY',
         1,0,0,0,1,1);
    EXCEPTION WHEN not_null_violation THEN v_caught := true;
    END;
    IF NOT v_caught THEN RAISE EXCEPTION 'old % writer was not isolated',v_kind; END IF;
  END LOOP;
END $$;

DO $$
DECLARE v_caught boolean := false;
BEGIN
  BEGIN
    INSERT INTO idempotency_keys
      (id,tenant_id,scope,idempotency_key,request_hash,status,locked_until)
    VALUES
      ('10000000-0000-7000-8000-000000000092',
       '10000000-0000-7000-8000-000000000001','order_create','zero-pay',
       decode('00','hex'),'in_flight',now()+interval '5 minutes');
    INSERT INTO orders
      (id,tenant_id,order_no,user_id,kind,status,currency,subtotal_amount,
       discount_amount,tax_amount,balance_applied,total_amount,payable_amount,
       paid_amount,paid_at,manual_reason,idempotency_key_id)
    VALUES
      ('10000000-0000-7000-8000-000000000093',
       '10000000-0000-7000-8000-000000000001','ZERO-PAID',
       '10000000-0000-7000-8000-000000000011','manual','paid','CNY',
       0,0,0,0,0,0,0,now(),'zero payable test order',
       '10000000-0000-7000-8000-000000000092');
    INSERT INTO order_reservations
      (id,tenant_id,order_id,user_id,state,expires_at,captured_at)
    VALUES
      ('10000000-0000-7000-8000-000000000094',
       '10000000-0000-7000-8000-000000000001',
       '10000000-0000-7000-8000-000000000093',
       '10000000-0000-7000-8000-000000000011','captured',now()+interval '1 hour',now());
    INSERT INTO order_reservation_events
      (tenant_id,reservation_id,order_id,from_state,to_state,event_kind,business_request_id)
    VALUES
      ('10000000-0000-7000-8000-000000000001',
       '10000000-0000-7000-8000-000000000094',
       '10000000-0000-7000-8000-000000000093',NULL,'held','reserve',
       '10000000-0000-7000-8000-000000000092'),
      ('10000000-0000-7000-8000-000000000001',
       '10000000-0000-7000-8000-000000000094',
       '10000000-0000-7000-8000-000000000093','held','captured','capture',
       '10000000-0000-7000-8000-000000000092');
    SET CONSTRAINTS ALL IMMEDIATE;
  EXCEPTION WHEN check_violation THEN v_caught := true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'zero-pay order stopped at paid instead of fulfilled'; END IF;
END $$;

-- Updating only the order is rejected at the deferred commit boundary.
DO $$
DECLARE v_caught boolean := false;
BEGIN
  BEGIN
    UPDATE orders SET status='paid',paid_at=now(),paid_amount=200
     WHERE id='10000000-0000-7000-8000-000000000042';
    SET CONSTRAINTS ALL IMMEDIATE;
  EXCEPTION WHEN check_violation THEN v_caught := true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'legacy webhook partial capture committed'; END IF;
END $$;

-- A malformed body is durable evidence before parsing, and its raw identity
-- plus terminal assessment can never be overwritten.
INSERT INTO payment_webhook_receipts
  (id,tenant_id,provider_code,http_method,request_path,raw_headers,raw_body,source_ip)
VALUES
  ('10000000-0000-7000-8000-000000000071',
   '10000000-0000-7000-8000-000000000001','unknown/test','POST','/webhook/unknown',
   '{"content-type":"application/json"}',convert_to('{broken','UTF8'),'192.0.2.1');
INSERT INTO payment_webhook_receipts
  (tenant_id,provider_code,http_method,request_path,raw_body)
VALUES
  ('20000000-0000-7000-8000-000000000001','tenant-b/test','POST','/webhook/b',
   convert_to('tenant-b','UTF8'));
DO $$
DECLARE v_caught boolean := false;
BEGIN
  BEGIN
    UPDATE payment_webhook_receipts SET raw_body=convert_to('{}','UTF8')
     WHERE id='10000000-0000-7000-8000-000000000071';
  EXCEPTION WHEN check_violation THEN v_caught := true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'raw webhook evidence was mutable'; END IF;
  UPDATE payment_webhook_receipts
     SET parse_status='malformed',parsed_at=now(),parse_error='invalid JSON',
         signature_status='unavailable',signature_checked_at=now()
   WHERE id='10000000-0000-7000-8000-000000000071';
  v_caught := false;
  BEGIN
    UPDATE payment_webhook_receipts SET parse_error='rewritten'
     WHERE id='10000000-0000-7000-8000-000000000071';
  EXCEPTION WHEN check_violation THEN v_caught := true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'terminal webhook assessment was mutable'; END IF;
END $$;

CREATE ROLE reservation_rls_test NOLOGIN NOSUPERUSER NOBYPASSRLS;
GRANT USAGE ON SCHEMA public, app TO reservation_rls_test;
GRANT SELECT, INSERT ON order_reservations TO reservation_rls_test;
GRANT SELECT, INSERT, UPDATE ON refund_requests, refunds TO reservation_rls_test;
GRANT EXECUTE ON FUNCTION app.current_tenant_id() TO reservation_rls_test;
-- Isolate policy behavior from business guards for the two denied INSERTs.
ALTER TABLE refund_requests DISABLE TRIGGER zz_refund_requests_guard;
ALTER TABLE refunds DISABLE TRIGGER trg_refunds_source_guard;
SET ROLE reservation_rls_test;
SELECT set_config('app.tenant_id', '10000000-0000-7000-8000-000000000001', false);
DO $$
DECLARE v_count int; v_caught boolean := false;
BEGIN
  SELECT count(*) INTO v_count FROM order_reservations;
  IF v_count <> 8 THEN RAISE EXCEPTION 'tenant A RLS count %, expected 8', v_count; END IF;
  IF NOT EXISTS (
       SELECT 1 FROM refund_requests
        WHERE id='10000000-0000-7000-8000-0000000000e1'
     ) OR EXISTS (
       SELECT 1 FROM refund_requests
        WHERE id='20000000-0000-7000-8000-0000000000e1'
     ) OR NOT EXISTS (
       SELECT 1 FROM refunds
        WHERE id='10000000-0000-7000-8000-0000000000a1'
     ) OR EXISTS (
       SELECT 1 FROM refunds
        WHERE id='20000000-0000-7000-8000-0000000000a1'
     ) THEN
    RAISE EXCEPTION 'refund header/leg RLS visibility crossed tenants';
  END IF;
  BEGIN
    INSERT INTO refund_requests
      (tenant_id,order_id,currency,requested_amount,business_request_id,
       request_hash,requested_by,reason)
    VALUES
      ('20000000-0000-7000-8000-000000000001',
       '20000000-0000-7000-8000-000000000048','CNY',1,uuidv7(),
       decode(md5('cross-header'),'hex'),
       '20000000-0000-7000-8000-000000000011','cross tenant');
  EXCEPTION WHEN insufficient_privilege THEN v_caught := true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'cross-tenant refund header insert passed RLS'; END IF;
  v_caught:=false;
  BEGIN
    INSERT INTO refunds
      (tenant_id,refund_request_id,order_id,payment_id,
       source_kind,currency,amount,reason)
    VALUES
      ('20000000-0000-7000-8000-000000000001',
       '20000000-0000-7000-8000-0000000000e1',
       '20000000-0000-7000-8000-000000000048',
       '20000000-0000-7000-8000-000000000090','payment','CNY',1,'cross tenant');
  EXCEPTION WHEN insufficient_privilege THEN v_caught := true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'cross-tenant refund leg insert passed RLS'; END IF;
  UPDATE refund_requests SET reason=reason
   WHERE id='20000000-0000-7000-8000-0000000000e1';
  GET DIAGNOSTICS v_count = ROW_COUNT;
  IF v_count<>0 THEN RAISE EXCEPTION 'cross-tenant refund header update passed RLS'; END IF;
  UPDATE refunds SET reason=reason
   WHERE id='20000000-0000-7000-8000-0000000000a1';
  GET DIAGNOSTICS v_count = ROW_COUNT;
  IF v_count<>0 THEN RAISE EXCEPTION 'cross-tenant refund leg update passed RLS'; END IF;
  v_caught:=false;
  BEGIN
    INSERT INTO order_reservations
      (tenant_id, order_id, user_id, expires_at)
    VALUES
      ('20000000-0000-7000-8000-000000000001',
       '10000000-0000-7000-8000-000000000041',
       '20000000-0000-7000-8000-000000000011', now() + interval '1 hour');
  EXCEPTION WHEN insufficient_privilege THEN v_caught := true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'cross-tenant RLS insert was accepted'; END IF;
END;
$$;
RESET ROLE;
ALTER TABLE refund_requests ENABLE TRIGGER zz_refund_requests_guard;
ALTER TABLE refunds ENABLE TRIGGER trg_refunds_source_guard;
SELECT 'refund_rls=ok' AS result;

SELECT 'order_reservation_pg18_tests=ok' AS result;
SQL

# Two real sessions race 60+60 against one 100-unit payment. Session B starts
# only after A is sleeping inside its transaction, so it must wait on the
# common order lock and then reject using A's newly committed fact.
docker exec -i -e PGPASSWORD="$PASSWORD" -e PGAPPNAME=refund_race_a \
  "$CONTAINER" psql -X -v ON_ERROR_STOP=1 -U postgres -d aegis_reservation_test \
  >"$RACE_A_LOG" 2>&1 <<'SQL' &
BEGIN;
SET LOCAL statement_timeout='10s';
INSERT INTO idempotency_keys
  (id,tenant_id,scope,idempotency_key,request_hash,status,actor_id,
   locked_until)
VALUES
  ('10000000-0000-7000-8000-0000000000d1',
   '10000000-0000-7000-8000-000000000001','refund_create','race-payment-a',
   decode(md5('race-payment-a'),'hex'),'in_flight',
   '10000000-0000-7000-8000-000000000011',now()+interval '5 minutes');
UPDATE idempotency_keys SET resource_type='refund_request',
       resource_id='10000000-0000-7000-8000-0000000000d1'
 WHERE id='10000000-0000-7000-8000-0000000000d1';
INSERT INTO refund_requests
  (id,tenant_id,order_id,currency,requested_amount,business_request_id,
   request_hash,requested_by,reason)
VALUES
  ('10000000-0000-7000-8000-0000000000d1',
   '10000000-0000-7000-8000-000000000001',
   '10000000-0000-7000-8000-000000000047','CNY',60,
   '10000000-0000-7000-8000-0000000000d1',decode(md5('race-payment-a'),'hex'),
   '10000000-0000-7000-8000-000000000011','payment race A');
INSERT INTO refunds
  (id,tenant_id,refund_request_id,order_id,payment_id,
   source_kind,currency,amount,reason)
VALUES
  ('10000000-0000-7000-8000-0000000000d3',
   '10000000-0000-7000-8000-000000000001',
   '10000000-0000-7000-8000-0000000000d1',
   '10000000-0000-7000-8000-000000000047',
   '10000000-0000-7000-8000-000000000090','payment','CNY',60,'payment race A');
SELECT pg_sleep(3);
COMMIT;
SQL
race_a_pid=$!

race_ready=false
for _ in $(seq 1 50); do
  if psql_in -Atc \
    "SELECT count(*) FROM pg_stat_activity WHERE application_name='refund_race_a' AND state='active' AND query LIKE '%pg_sleep%'" \
    | grep -qx '1'; then
    race_ready=true
    break
  fi
  sleep 0.1
done
if [[ "$race_ready" != true ]]; then
  cat "$RACE_A_LOG" >&2
  echo "test-order-reservations: payment race A did not reach barrier" >&2
  exit 1
fi

if docker exec -i -e PGPASSWORD="$PASSWORD" -e PGAPPNAME=refund_race_b \
  "$CONTAINER" psql -X -v ON_ERROR_STOP=1 -U postgres -d aegis_reservation_test \
  >"$RACE_B_LOG" 2>&1 <<'SQL'
BEGIN;
SET LOCAL statement_timeout='10s';
INSERT INTO idempotency_keys
  (id,tenant_id,scope,idempotency_key,request_hash,status,actor_id,
   locked_until)
VALUES
  ('10000000-0000-7000-8000-0000000000d2',
   '10000000-0000-7000-8000-000000000001','refund_create','race-payment-b',
   decode(md5('race-payment-b'),'hex'),'in_flight',
   '10000000-0000-7000-8000-000000000011',now()+interval '5 minutes');
UPDATE idempotency_keys SET resource_type='refund_request',
       resource_id='10000000-0000-7000-8000-0000000000d2'
 WHERE id='10000000-0000-7000-8000-0000000000d2';
INSERT INTO refund_requests
  (id,tenant_id,order_id,currency,requested_amount,business_request_id,
   request_hash,requested_by,reason)
VALUES
  ('10000000-0000-7000-8000-0000000000d2',
   '10000000-0000-7000-8000-000000000001',
   '10000000-0000-7000-8000-000000000047','CNY',60,
   '10000000-0000-7000-8000-0000000000d2',decode(md5('race-payment-b'),'hex'),
   '10000000-0000-7000-8000-000000000011','payment race B');
INSERT INTO refunds
  (id,tenant_id,refund_request_id,order_id,payment_id,
   source_kind,currency,amount,reason)
VALUES
  ('10000000-0000-7000-8000-0000000000d4',
   '10000000-0000-7000-8000-000000000001',
   '10000000-0000-7000-8000-0000000000d2',
   '10000000-0000-7000-8000-000000000047',
   '10000000-0000-7000-8000-000000000090','payment','CNY',60,'payment race B');
COMMIT;
SQL
then
  echo "test-order-reservations: concurrent 60+60 payment refunds both committed" >&2
  exit 1
fi
if ! wait "$race_a_pid"; then
  cat "$RACE_A_LOG" >&2
  exit 1
fi
grep -q 'active refunds 120 exceed payment source funding 100' "$RACE_B_LOG" || {
  cat "$RACE_B_LOG" >&2
  echo "test-order-reservations: payment race failed for the wrong reason" >&2
  exit 1
}
psql_in -Atc "SELECT count(*)=1 AND sum(amount)=60 FROM refunds WHERE order_id='10000000-0000-7000-8000-000000000047' AND status NOT IN ('failed','rejected')" \
  | grep -qx 't'
echo "refund_payment_60_60_concurrency=ok"

# Real mixed funding: payment 60 and captured balance 40 share the same order
# lock, then take different source locks. Both are within their own sources and
# the order total, so both must commit without a deadlock.
docker exec -i -e PGPASSWORD="$PASSWORD" -e PGAPPNAME=refund_mixed_a \
  "$CONTAINER" psql -X -v ON_ERROR_STOP=1 -U postgres -d aegis_reservation_test \
  >"$RACE_A_LOG" 2>&1 <<'SQL' &
BEGIN;
SET LOCAL statement_timeout='10s';
INSERT INTO idempotency_keys
  (id,tenant_id,scope,idempotency_key,request_hash,status,actor_id,locked_until)
VALUES
  ('10000000-0000-7000-8000-0000000000d5','10000000-0000-7000-8000-000000000001',
   'refund_create','race-mixed-payment',decode(md5('race-mixed-payment'),'hex'),'in_flight',
   '10000000-0000-7000-8000-000000000011',now()+interval '5 minutes');
UPDATE idempotency_keys SET resource_type='refund_request',
       resource_id='10000000-0000-7000-8000-0000000000d5'
 WHERE id='10000000-0000-7000-8000-0000000000d5';
INSERT INTO refund_requests
  (id,tenant_id,order_id,currency,requested_amount,business_request_id,
   request_hash,requested_by,reason)
VALUES
  ('10000000-0000-7000-8000-0000000000d5','10000000-0000-7000-8000-000000000001',
   '10000000-0000-7000-8000-000000000044','CNY',60,
   '10000000-0000-7000-8000-0000000000d5',decode(md5('race-mixed-payment'),'hex'),
   '10000000-0000-7000-8000-000000000011','mixed payment race');
INSERT INTO refunds
  (id,tenant_id,refund_request_id,order_id,payment_id,source_kind,currency,amount,reason)
VALUES
  ('10000000-0000-7000-8000-0000000000d7','10000000-0000-7000-8000-000000000001',
   '10000000-0000-7000-8000-0000000000d5','10000000-0000-7000-8000-000000000044',
   '10000000-0000-7000-8000-000000000086','payment','CNY',60,'mixed payment race');
SELECT pg_sleep(3);
COMMIT;
SQL
race_a_pid=$!
race_ready=false
for _ in $(seq 1 50); do
  if psql_in -Atc \
    "SELECT count(*) FROM pg_stat_activity WHERE application_name='refund_mixed_a' AND state='active' AND query LIKE '%pg_sleep%'" \
    | grep -qx '1'; then race_ready=true; break; fi
  sleep 0.1
done
if [[ "$race_ready" != true ]]; then cat "$RACE_A_LOG" >&2; exit 1; fi
if ! docker exec -i -e PGPASSWORD="$PASSWORD" -e PGAPPNAME=refund_mixed_b \
  "$CONTAINER" psql -X -v ON_ERROR_STOP=1 -U postgres -d aegis_reservation_test \
  >"$RACE_B_LOG" 2>&1 <<'SQL'
BEGIN;
SET LOCAL statement_timeout='10s';
INSERT INTO idempotency_keys
  (id,tenant_id,scope,idempotency_key,request_hash,status,actor_id,locked_until)
VALUES
  ('10000000-0000-7000-8000-0000000000d6','10000000-0000-7000-8000-000000000001',
   'refund_create','race-mixed-balance',decode(md5('race-mixed-balance'),'hex'),'in_flight',
   '10000000-0000-7000-8000-000000000011',now()+interval '5 minutes');
UPDATE idempotency_keys SET resource_type='refund_request',
       resource_id='10000000-0000-7000-8000-0000000000d6'
 WHERE id='10000000-0000-7000-8000-0000000000d6';
INSERT INTO refund_requests
  (id,tenant_id,order_id,currency,requested_amount,business_request_id,
   request_hash,requested_by,reason)
VALUES
  ('10000000-0000-7000-8000-0000000000d6','10000000-0000-7000-8000-000000000001',
   '10000000-0000-7000-8000-000000000044','CNY',40,
   '10000000-0000-7000-8000-0000000000d6',decode(md5('race-mixed-balance'),'hex'),
   '10000000-0000-7000-8000-000000000011','mixed balance race');
INSERT INTO refunds
  (id,tenant_id,refund_request_id,order_id,balance_hold_id,
   source_kind,currency,amount,reason)
SELECT
  '10000000-0000-7000-8000-0000000000d8','10000000-0000-7000-8000-000000000001',
  '10000000-0000-7000-8000-0000000000d6','10000000-0000-7000-8000-000000000044',
  h.id,'balance','CNY',40,'mixed balance race'
FROM balance_holds h WHERE h.order_id='10000000-0000-7000-8000-000000000044';
COMMIT;
SQL
then
  cat "$RACE_B_LOG" >&2
  exit 1
fi
if ! wait "$race_a_pid"; then cat "$RACE_A_LOG" >&2; exit 1; fi
psql_in -Atc "SELECT count(*)=2 AND sum(amount)=100 FROM refunds WHERE id IN ('10000000-0000-7000-8000-0000000000d7','10000000-0000-7000-8000-0000000000d8')" \
  | grep -qx 't'
echo "refund_mixed_60_40_concurrency=ok"

# Finalization and entry append serialize on the ledger transaction row. Here
# the append wins; finalization must wake, recount four entries, and fail.
psql_in >/dev/null <<'SQL'
BEGIN;
INSERT INTO idempotency_keys
  (id,tenant_id,scope,idempotency_key,request_hash,status,actor_id,locked_until)
VALUES
  ('10000000-0000-7000-8000-0000000000d9','10000000-0000-7000-8000-000000000001',
   'refund_create','ledger-finalize-race',decode(md5('ledger-finalize-race'),'hex'),'in_flight',
   '10000000-0000-7000-8000-000000000011',now()+interval '5 minutes');
UPDATE idempotency_keys SET resource_type='refund_request',
       resource_id='10000000-0000-7000-8000-0000000000d9'
 WHERE id='10000000-0000-7000-8000-0000000000d9';
INSERT INTO refund_requests
  (id,tenant_id,order_id,currency,requested_amount,business_request_id,
   request_hash,requested_by,reason,status)
VALUES
  ('10000000-0000-7000-8000-0000000000d9','10000000-0000-7000-8000-000000000001',
   '10000000-0000-7000-8000-000000000044','CNY',10,
   '10000000-0000-7000-8000-0000000000d9',decode(md5('ledger-finalize-race'),'hex'),
   '10000000-0000-7000-8000-000000000011','ledger finalize race','pending');
INSERT INTO refunds
  (id,tenant_id,refund_request_id,order_id,payment_id,source_kind,currency,amount,reason)
VALUES
  ('10000000-0000-7000-8000-0000000000da','10000000-0000-7000-8000-000000000001',
   '10000000-0000-7000-8000-0000000000d9','10000000-0000-7000-8000-000000000044',
   '10000000-0000-7000-8000-000000000086','payment','CNY',10,'ledger finalize race');
INSERT INTO ledger_transactions
  (id,tenant_id,kind,currency,source_type,source_id,memo,actor_kind)
VALUES
  ('10000000-0000-7000-8000-0000000000db','10000000-0000-7000-8000-000000000001',
   'refund_issued','CNY','refund','10000000-0000-7000-8000-0000000000da',
   'ledger finalize race','system');
INSERT INTO ledger_entries
  (tenant_id,transaction_id,account_id,direction,amount,currency)
SELECT '10000000-0000-7000-8000-000000000001'::uuid,
       '10000000-0000-7000-8000-0000000000db'::uuid,id,'debit',10,'CNY'
  FROM ledger_accounts
 WHERE tenant_id='10000000-0000-7000-8000-000000000001'
   AND account_type='platform_revenue' AND owner_ref='main'
UNION ALL
SELECT '10000000-0000-7000-8000-000000000001'::uuid,
       '10000000-0000-7000-8000-0000000000db'::uuid,id,'credit',10,'CNY'
  FROM ledger_accounts
 WHERE tenant_id='10000000-0000-7000-8000-000000000001'
   AND account_type='channel_cash' AND owner_ref='testpay';
UPDATE refund_requests SET status='processing'
 WHERE id='10000000-0000-7000-8000-0000000000d9';
UPDATE refunds SET status='processing'
 WHERE id='10000000-0000-7000-8000-0000000000da';
COMMIT;
SQL

docker exec -i -e PGPASSWORD="$PASSWORD" -e PGAPPNAME=refund_ledger_append \
  "$CONTAINER" psql -X -v ON_ERROR_STOP=1 -U postgres -d aegis_reservation_test \
  >"$RACE_A_LOG" 2>&1 <<'SQL' &
BEGIN;
SET LOCAL statement_timeout='10s';
INSERT INTO ledger_entries
  (tenant_id,transaction_id,account_id,direction,amount,currency)
SELECT '10000000-0000-7000-8000-000000000001'::uuid,
       '10000000-0000-7000-8000-0000000000db'::uuid,id,'debit',1,'CNY'
  FROM ledger_accounts
 WHERE tenant_id='10000000-0000-7000-8000-000000000001'
   AND account_type='platform_revenue' AND owner_ref='main'
UNION ALL
SELECT '10000000-0000-7000-8000-000000000001'::uuid,
       '10000000-0000-7000-8000-0000000000db'::uuid,id,'credit',1,'CNY'
  FROM ledger_accounts
 WHERE tenant_id='10000000-0000-7000-8000-000000000001'
   AND account_type='channel_cash' AND owner_ref='testpay';
SELECT pg_sleep(3);
COMMIT;
SQL
race_a_pid=$!
race_ready=false
for _ in $(seq 1 50); do
  if psql_in -Atc \
    "SELECT count(*) FROM pg_stat_activity WHERE application_name='refund_ledger_append' AND state='active' AND query LIKE '%pg_sleep%'" \
    | grep -qx '1'; then race_ready=true; break; fi
  sleep 0.1
done
if [[ "$race_ready" != true ]]; then cat "$RACE_A_LOG" >&2; exit 1; fi
if docker exec -i -e PGPASSWORD="$PASSWORD" -e PGAPPNAME=refund_ledger_finalize \
  "$CONTAINER" psql -X -v ON_ERROR_STOP=1 -U postgres -d aegis_reservation_test \
  >"$RACE_B_LOG" 2>&1 <<'SQL'
BEGIN;
SET LOCAL statement_timeout='10s';
UPDATE refunds
   SET status='succeeded',provider_refund_id='ledger-race-provider',
       ledger_txn_id='10000000-0000-7000-8000-0000000000db',succeeded_at=now()
 WHERE id='10000000-0000-7000-8000-0000000000da';
COMMIT;
SQL
then
  echo "test-order-reservations: ledger append and refund finalization both committed" >&2
  exit 1
fi
if ! wait "$race_a_pid"; then cat "$RACE_A_LOG" >&2; exit 1; fi
grep -q 'successful refund ledger transaction is missing or amount-mismatched' "$RACE_B_LOG" || {
  cat "$RACE_B_LOG" >&2
  exit 1
}
psql_in -Atc "SELECT (SELECT count(*)=4 FROM ledger_entries WHERE transaction_id='10000000-0000-7000-8000-0000000000db') AND (SELECT status='processing' FROM refunds WHERE id='10000000-0000-7000-8000-0000000000da')" \
  | grep -qx 't'
echo "refund_ledger_finalize_append_concurrency=ok"

docker exec -i -e PGPASSWORD="$PASSWORD" \
  -e AEGIS_DB_APP_PASSWORD="reservation-app-test" "$CONTAINER" \
  psql -X -v ON_ERROR_STOP=1 -U postgres -d aegis_reservation_test \
  < "$ROOT/deploy/configure-app-role.sql" >/dev/null
# Idempotence gate: a second bootstrap must not re-expand any sensitive ACL.
docker exec -i -e PGPASSWORD="$PASSWORD" \
  -e AEGIS_DB_APP_PASSWORD="reservation-app-test" "$CONTAINER" \
  psql -X -v ON_ERROR_STOP=1 -U postgres -d aegis_reservation_test \
  < "$ROOT/deploy/configure-app-role.sql" >/dev/null

# Future tables inherit read-only access; repeating bootstrap must not restore
# the former broad default DML grant.
psql_in <<'SQL'
CREATE TABLE acl_future_default_probe (id bigint GENERATED ALWAYS AS IDENTITY, note text);
DO $$
BEGIN
  IF NOT has_table_privilege('aegis_app','acl_future_default_probe','SELECT')
     OR has_table_privilege('aegis_app','acl_future_default_probe','INSERT')
     OR has_table_privilege('aegis_app','acl_future_default_probe','UPDATE')
     OR has_table_privilege('aegis_app','acl_future_default_probe','DELETE') THEN
    RAISE EXCEPTION 'default table privileges are broader than SELECT';
  END IF;
END $$;
DROP TABLE acl_future_default_probe;
SQL

psql_in <<'SQL'
SET ROLE aegis_app;
SELECT set_config('app.tenant_id','10000000-0000-7000-8000-000000000001',false);
DO $$
DECLARE
  v_caught boolean := false;
  v_count int;
  v_idempotency uuid;
  v_order uuid;
  v_reservation uuid;
  v_ledger_debit uuid;
  v_ledger_credit uuid;
  v_ledger_txn uuid;
BEGIN
  IF has_function_privilege(
       'aegis_app','app.purge_subscription_fetch_log(interval)','EXECUTE') THEN
    RAISE EXCEPTION 'app can execute cross-tenant audit retention maintenance';
  END IF;
  BEGIN
    PERFORM app.purge_subscription_fetch_log(interval '30 days');
  EXCEPTION WHEN insufficient_privilege THEN v_caught := true;
  END;
  IF NOT v_caught THEN
    RAISE EXCEPTION 'app executed cross-tenant audit retention maintenance';
  END IF;
  v_caught := false;
  IF NOT has_column_privilege('aegis_app','orders','tenant_id','INSERT')
     OR has_column_privilege('aegis_app','orders','id','INSERT')
     OR has_column_privilege('aegis_app','orders','paid_at','INSERT')
     OR NOT has_column_privilege('aegis_app','order_items','line_amount','INSERT')
     OR has_column_privilege('aegis_app','order_items','created_at','INSERT')
     OR NOT has_column_privilege('aegis_app','payment_intents','provider_ref','INSERT')
     OR has_column_privilege('aegis_app','payment_intents','created_at','INSERT')
     OR NOT has_column_privilege('aegis_app','payments','fee_amount','INSERT')
     OR has_column_privilege('aegis_app','payments','refunded_amount','INSERT')
     OR has_column_privilege('aegis_app','payment_events','processing_status','INSERT')
     OR has_column_privilege('aegis_app','payment_webhook_receipts','parse_status','INSERT')
     OR NOT has_column_privilege('aegis_app','order_reservations','expires_at','INSERT')
     OR has_column_privilege('aegis_app','order_reservations','state','INSERT')
     OR has_column_privilege('aegis_app','order_reservations','held_at','INSERT')
     OR has_column_privilege('aegis_app','order_reservations','legacy_backfill','INSERT')
     OR NOT has_column_privilege('aegis_app','order_stock_reservations','quantity','INSERT')
     OR has_column_privilege('aegis_app','order_stock_reservations','legacy_backfill','INSERT')
     OR NOT has_column_privilege('aegis_app','order_purchase_limit_reservations','quantity','INSERT')
     OR has_column_privilege('aegis_app','order_purchase_limit_reservations','created_at','INSERT')
     OR NOT has_column_privilege('aegis_app','coupon_redemptions','reservation_id','INSERT')
     OR has_column_privilege('aegis_app','coupon_redemptions','status','INSERT')
     OR has_column_privilege('aegis_app','coupon_redemptions','redeemed_at','INSERT')
     OR NOT has_column_privilege('aegis_app','balance_holds','hold_txn_id','INSERT')
     OR has_column_privilege('aegis_app','balance_holds','status','INSERT')
     OR has_column_privilege('aegis_app','balance_holds','legacy_backfill','INSERT')
     OR NOT has_column_privilege('aegis_app','late_payment_cases','suspense_txn_id','INSERT')
     OR has_column_privilege('aegis_app','late_payment_cases','status','INSERT')
     OR has_column_privilege('aegis_app','late_payment_cases','received_at','INSERT')
     OR NOT has_column_privilege('aegis_app','order_reservation_events','to_state','INSERT')
     OR has_column_privilege('aegis_app','order_reservation_events','legacy_backfill','INSERT')
     OR has_column_privilege('aegis_app','order_reservation_events','occurred_at','INSERT')
     OR NOT has_column_privilege('aegis_app','ledger_accounts','account_type','INSERT')
     OR has_column_privilege('aegis_app','ledger_accounts','id','INSERT')
     OR has_column_privilege('aegis_app','ledger_accounts','balance_signed','INSERT')
     OR has_column_privilege('aegis_app','ledger_accounts','created_at','INSERT')
     OR NOT has_column_privilege('aegis_app','ledger_transactions','kind','INSERT')
     OR has_column_privilege('aegis_app','ledger_transactions','id','INSERT')
     OR has_column_privilege('aegis_app','ledger_transactions','occurred_at','INSERT')
     OR NOT has_column_privilege('aegis_app','ledger_entries','amount','INSERT')
     OR has_column_privilege('aegis_app','ledger_entries','id','INSERT')
     OR has_column_privilege('aegis_app','ledger_entries','created_at','INSERT')
     OR has_table_privilege('aegis_app','refunds','INSERT')
     OR has_table_privilege('aegis_app','invoices','INSERT') THEN
    RAISE EXCEPTION 'sensitive INSERT ACL is not least privilege after repeated bootstrap';
  END IF;
  IF NOT has_column_privilege('aegis_app','payment_webhook_receipts','parse_status','UPDATE')
     OR has_column_privilege('aegis_app','payment_webhook_receipts','raw_body','UPDATE') THEN
    RAISE EXCEPTION 'receipt column ACL is not least privilege';
  END IF;
  IF NOT has_column_privilege('aegis_app','order_reservations','state','UPDATE')
     OR has_column_privilege('aegis_app','order_reservations','order_id','UPDATE')
     OR has_column_privilege('aegis_app','order_reservations','legacy_backfill','UPDATE')
     OR has_table_privilege('aegis_app','order_reservations','DELETE')
     OR NOT has_column_privilege('aegis_app','coupon_redemptions','status','UPDATE')
     OR has_column_privilege('aegis_app','coupon_redemptions','discount_amount','UPDATE')
     OR has_column_privilege('aegis_app','coupon_redemptions','legacy_backfill','UPDATE')
     OR has_table_privilege('aegis_app','coupon_redemptions','DELETE')
     OR NOT has_column_privilege('aegis_app','balance_holds','status','UPDATE')
     OR has_column_privilege('aegis_app','balance_holds','amount','UPDATE')
     OR has_column_privilege('aegis_app','balance_holds','legacy_backfill','UPDATE')
     OR has_table_privilege('aegis_app','balance_holds','DELETE')
     OR NOT has_column_privilege('aegis_app','late_payment_cases','status','UPDATE')
     OR has_column_privilege('aegis_app','late_payment_cases','amount','UPDATE')
     OR has_table_privilege('aegis_app','late_payment_cases','DELETE')
     OR has_column_privilege('aegis_app','ledger_accounts','balance_signed','UPDATE')
     OR NOT has_column_privilege('aegis_app','ledger_accounts','updated_at','UPDATE')
     OR has_table_privilege('aegis_app','ledger_accounts','DELETE')
     OR has_table_privilege('aegis_app','ledger_transactions','DELETE')
     OR has_table_privilege('aegis_app','ledger_entries','DELETE') THEN
    RAISE EXCEPTION 'reservation lifecycle ACL is broader than guarded transitions';
  END IF;
  INSERT INTO payment_webhook_receipts
    (tenant_id,provider_code,http_method,request_path,raw_body)
  VALUES
    ('10000000-0000-7000-8000-000000000001','acl/test','POST','/acl',
     convert_to('not-json','UTF8'));
  UPDATE payment_webhook_receipts
     SET parse_status='malformed',parsed_at=now(),parse_error='expected'
   WHERE provider_code='acl/test';
  BEGIN
    UPDATE payment_webhook_receipts SET raw_body=convert_to('{}','UTF8')
     WHERE provider_code='acl/test';
  EXCEPTION WHEN insufficient_privilege THEN v_caught := true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'app role rewrote raw receipt'; END IF;
  SELECT count(*) INTO v_count FROM payment_webhook_receipts;
  IF v_count<>2 THEN RAISE EXCEPTION 'tenant receipt RLS count mismatch: %',v_count; END IF;
  IF NOT has_column_privilege('aegis_app','payment_events','processing_status','UPDATE')
     OR has_column_privilege('aegis_app','payment_events','raw_payload','UPDATE') THEN
    RAISE EXCEPTION 'payment event column ACL is not least privilege';
  END IF;
  INSERT INTO payment_events
    (tenant_id,provider_id,provider_event_id,event_type,provider_payment_id,
     raw_payload,signature_verified)
  VALUES
    ('10000000-0000-7000-8000-000000000001',
     '10000000-0000-7000-8000-000000000081','acl-event','paid','payment-paid',
     '{"event":"paid"}',true);
  UPDATE payment_events
     SET processing_status='processed',processed_at=now()
   WHERE provider_event_id='acl-event';
  v_caught := false;
  BEGIN
    UPDATE payment_events SET raw_payload='{}' WHERE provider_event_id='acl-event';
  EXCEPTION WHEN insufficient_privilege THEN v_caught := true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'app role rewrote payment event evidence'; END IF;
  INSERT INTO payment_events
    (tenant_id,provider_id,provider_event_id,event_type,raw_payload,signature_verified)
  VALUES
    ('10000000-0000-7000-8000-000000000001',
     '10000000-0000-7000-8000-000000000081','acl-event-failed','paid','{}',true);
  UPDATE payment_events
     SET processing_status='failed',processing_error='retryable'
   WHERE provider_event_id='acl-event-failed';
  v_caught := false;
  BEGIN
    UPDATE payment_events SET processed_at=now()
     WHERE provider_event_id='acl-event-failed';
  EXCEPTION WHEN check_violation THEN v_caught := true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'failed event accepted processed_at'; END IF;
  IF NOT has_column_privilege('aegis_app','payment_intents','status','UPDATE')
     OR has_column_privilege('aegis_app','payment_intents','amount','UPDATE')
     OR has_table_privilege('aegis_app','payments','UPDATE')
     OR has_table_privilege('aegis_app','refunds','UPDATE')
     OR has_table_privilege('aegis_app','invoices','UPDATE') THEN
    RAISE EXCEPTION 'finance table ACL is broader than runtime writers';
  END IF;
  INSERT INTO payment_intents
    (tenant_id,order_id,provider_id,currency,amount,status,provider_ref)
  VALUES
    ('10000000-0000-7000-8000-000000000001',
     '10000000-0000-7000-8000-000000000042',
     '10000000-0000-7000-8000-000000000081','CNY',200,'created','acl-intent');
  UPDATE payment_intents SET status='cancelled'
   WHERE provider_ref='acl-intent';
  v_caught := false;
  BEGIN
    UPDATE payments SET amount=amount+1
     WHERE id='10000000-0000-7000-8000-000000000083';
  EXCEPTION WHEN insufficient_privilege THEN v_caught := true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'app role updated immutable payment'; END IF;
  v_caught := false;
  BEGIN
    DELETE FROM payments WHERE id='10000000-0000-7000-8000-000000000083';
  EXCEPTION WHEN insufficient_privilege THEN v_caught := true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'app role deleted payment evidence'; END IF;

  INSERT INTO ledger_accounts
    (tenant_id,account_type,normal_balance,currency,owner_ref)
  VALUES
    ('10000000-0000-7000-8000-000000000001','suspense','debit','CNY',
     'acl-ledger-debit')
  RETURNING id INTO v_ledger_debit;
  INSERT INTO ledger_accounts
    (tenant_id,account_type,normal_balance,currency,owner_ref)
  VALUES
    ('10000000-0000-7000-8000-000000000001','platform_revenue','credit','CNY',
     'acl-ledger-credit')
  RETURNING id INTO v_ledger_credit;
  INSERT INTO ledger_transactions
    (tenant_id,kind,currency,source_type,memo,actor_kind)
  VALUES
    ('10000000-0000-7000-8000-000000000001','balance_topup','CNY',
     'acl','least privilege posting','system')
  RETURNING id INTO v_ledger_txn;
  INSERT INTO ledger_entries
    (tenant_id,transaction_id,account_id,direction,amount,currency,description)
  VALUES
    ('10000000-0000-7000-8000-000000000001',v_ledger_txn,v_ledger_debit,
     'debit',7,'CNY','acl debit'),
    ('10000000-0000-7000-8000-000000000001',v_ledger_txn,v_ledger_credit,
     'credit',7,'CNY','acl credit');
  SELECT count(*) INTO v_count FROM app.verify_ledger_all();
  IF v_count<>0 THEN RAISE EXCEPTION 'legal app ledger posting created drift'; END IF;

  v_caught := false;
  BEGIN
    UPDATE ledger_accounts SET balance_signed=balance_signed+100
     WHERE id=v_ledger_debit;
  EXCEPTION WHEN insufficient_privilege THEN v_caught := true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'app directly changed ledger balance'; END IF;
  v_caught := false;
  BEGIN
    INSERT INTO ledger_transactions
      (id,tenant_id,kind,currency,occurred_at)
    VALUES
      ('10000000-0000-7000-8000-000000000099',
       '10000000-0000-7000-8000-000000000001','forged','CNY',
       now()-interval '1 year');
  EXCEPTION WHEN insufficient_privilege THEN v_caught := true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'app forged ledger transaction identity/time'; END IF;
  v_caught := false;
  BEGIN
    INSERT INTO ledger_entries
      (id,tenant_id,transaction_id,account_id,direction,amount,currency,created_at)
    VALUES
      ('10000000-0000-7000-8000-000000000098',
       '10000000-0000-7000-8000-000000000001',v_ledger_txn,v_ledger_debit,
       'debit',1,'CNY',now()-interval '1 year');
  EXCEPTION WHEN insufficient_privilege THEN v_caught := true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'app forged ledger entry identity/time'; END IF;
  v_caught := false;
  BEGIN
    DELETE FROM ledger_accounts WHERE id=v_ledger_debit;
  EXCEPTION WHEN insufficient_privilege THEN v_caught := true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'app deleted ledger account'; END IF;
  v_caught := false;
  BEGIN
    DELETE FROM ledger_transactions WHERE id=v_ledger_txn;
  EXCEPTION WHEN insufficient_privilege THEN v_caught := true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'app deleted ledger transaction'; END IF;
  v_caught := false;
  BEGIN
    DELETE FROM ledger_entries WHERE transaction_id=v_ledger_txn;
  EXCEPTION WHEN insufficient_privilege THEN v_caught := true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'app deleted ledger entries'; END IF;

  INSERT INTO idempotency_keys
    (tenant_id,scope,idempotency_key,request_hash,status,locked_until)
  VALUES
    ('10000000-0000-7000-8000-000000000001','order_create','acl-renewal',
     decode('01','hex'),'in_flight',now()+interval '5 minutes')
  RETURNING id INTO v_idempotency;
  v_caught := false;
  BEGIN
    INSERT INTO orders
      (tenant_id,order_no,user_id,kind,status,currency,subtotal_amount,
       discount_amount,tax_amount,total_amount,balance_applied,payable_amount,
       idempotency_key_id)
    VALUES
      ('10000000-0000-7000-8000-000000000001','ACL-FORGED-PAID',
       '10000000-0000-7000-8000-000000000011','renewal','paid','CNY',
       10,0,0,10,0,10,v_idempotency);
  EXCEPTION WHEN insufficient_privilege THEN v_caught := true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'app inserted terminal order state'; END IF;
  v_caught := false;
  BEGIN
    INSERT INTO payment_intents
      (tenant_id,order_id,provider_id,currency,amount,status)
    VALUES
      ('10000000-0000-7000-8000-000000000001',
       '10000000-0000-7000-8000-000000000041',
       '10000000-0000-7000-8000-000000000081','CNY',1,'succeeded');
  EXCEPTION WHEN insufficient_privilege THEN v_caught := true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'app inserted terminal payment intent'; END IF;
  v_caught := false;
  BEGIN
    INSERT INTO payments
      (tenant_id,order_id,provider_id,provider_payment_id,currency,amount,status)
    VALUES
      ('10000000-0000-7000-8000-000000000001',
       '10000000-0000-7000-8000-000000000041',
       '10000000-0000-7000-8000-000000000081','acl-forged-reversed',
       'CNY',1,'reversed');
  EXCEPTION WHEN insufficient_privilege THEN v_caught := true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'app inserted terminal payment state'; END IF;
  INSERT INTO orders
    (tenant_id,order_no,user_id,kind,status,currency,subtotal_amount,
     discount_amount,tax_amount,total_amount,balance_applied,payable_amount,
     expires_at,idempotency_key_id)
  VALUES
    ('10000000-0000-7000-8000-000000000001','ACL-RENEWAL',
     '10000000-0000-7000-8000-000000000011','renewal','pending_payment','CNY',
     10,0,0,10,0,10,now()+interval '30 minutes',
     v_idempotency)
  RETURNING id INTO v_order;
  INSERT INTO order_items
    (tenant_id,order_id,product_id,plan_id,snapshot_product_name,
     snapshot_plan_name,snapshot_interval,snapshot_interval_count,
     snapshot_entitlements,snapshot_quotas,quantity,unit_amount,line_amount,currency)
  VALUES
    ('10000000-0000-7000-8000-000000000001',v_order,
     '10000000-0000-7000-8000-000000000021',
     '10000000-0000-7000-8000-000000000031','Legacy Product','Legacy Plan',
     'month',1,'[]','[]',1,10,10,'CNY');
  INSERT INTO order_reservations
    (tenant_id,order_id,user_id,expires_at)
  VALUES
    ('10000000-0000-7000-8000-000000000001',v_order,
     '10000000-0000-7000-8000-000000000011',now()+interval '30 minutes')
  RETURNING id INTO v_reservation;
  INSERT INTO order_reservation_events
    (tenant_id,reservation_id,order_id,from_state,to_state,event_kind,business_request_id)
  VALUES
    ('10000000-0000-7000-8000-000000000001',v_reservation,v_order,
     NULL,'held','reserve',v_idempotency);
  UPDATE order_reservations SET state=state WHERE id=v_reservation;

  v_caught := false;
  BEGIN
    INSERT INTO order_reservations
      (tenant_id,order_id,user_id,expires_at,legacy_backfill)
    VALUES
      ('10000000-0000-7000-8000-000000000001',v_order,
       '10000000-0000-7000-8000-000000000011',now()+interval '1 hour',true);
  EXCEPTION WHEN insufficient_privilege THEN v_caught := true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'app inserted legacy reservation'; END IF;
  v_caught := false;
  BEGIN
    INSERT INTO order_reservations
      (tenant_id,order_id,user_id,state,expires_at,captured_at)
    VALUES
      ('10000000-0000-7000-8000-000000000001',v_order,
       '10000000-0000-7000-8000-000000000011','captured',
       now()+interval '1 hour',now());
  EXCEPTION WHEN insufficient_privilege THEN v_caught := true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'app inserted terminal reservation'; END IF;
  v_caught := false;
  BEGIN
    INSERT INTO order_reservations
      (tenant_id,order_id,user_id,expires_at,held_at)
    VALUES
      ('10000000-0000-7000-8000-000000000001',v_order,
       '10000000-0000-7000-8000-000000000011',now()+interval '1 hour',
       now()-interval '1 year');
  EXCEPTION WHEN insufficient_privilege THEN v_caught := true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'app forged reservation evidence time'; END IF;
  v_caught := false;
  BEGIN
    INSERT INTO order_stock_reservations
      (tenant_id,reservation_id,order_id,plan_id,quantity,legacy_backfill)
    VALUES
      ('10000000-0000-7000-8000-000000000001',v_reservation,v_order,
       '10000000-0000-7000-8000-000000000031',1,true);
  EXCEPTION WHEN insufficient_privilege THEN v_caught := true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'app inserted legacy stock reservation'; END IF;
  v_caught := false;
  BEGIN
    INSERT INTO coupon_redemptions
      (tenant_id,coupon_id,user_id,order_id,discount_amount,currency,
       reservation_id,status)
    VALUES
      ('10000000-0000-7000-8000-000000000001',
       '10000000-0000-7000-8000-000000000051',
       '10000000-0000-7000-8000-000000000011',v_order,0,'CNY',
       v_reservation,'captured');
  EXCEPTION WHEN insufficient_privilege THEN v_caught := true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'app inserted terminal coupon reservation'; END IF;
  v_caught := false;
  BEGIN
    INSERT INTO balance_holds
      (tenant_id,reservation_id,order_id,user_id,currency,amount,status,
       legacy_backfill)
    VALUES
      ('10000000-0000-7000-8000-000000000001',v_reservation,v_order,
       '10000000-0000-7000-8000-000000000011','CNY',1,'captured',true);
  EXCEPTION WHEN insufficient_privilege THEN v_caught := true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'app inserted terminal legacy balance hold'; END IF;
  v_caught := false;
  BEGIN
    INSERT INTO late_payment_cases
      (tenant_id,order_id,payment_id,amount,currency,suspense_txn_id,received_at)
    VALUES
      ('10000000-0000-7000-8000-000000000001',v_order,
       '10000000-0000-7000-8000-000000000083',1,'CNY',v_ledger_txn,
       now()-interval '1 year');
  EXCEPTION WHEN insufficient_privilege THEN v_caught := true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'app forged late-payment evidence time'; END IF;
  v_caught := false;
  BEGIN
    INSERT INTO order_reservation_events
      (tenant_id,reservation_id,order_id,from_state,to_state,event_kind,
       business_request_id,legacy_backfill,occurred_at)
    VALUES
      ('10000000-0000-7000-8000-000000000001',v_reservation,v_order,
       'held','captured','capture',v_idempotency,
       true,now()-interval '1 year');
  EXCEPTION WHEN insufficient_privilege THEN v_caught := true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'app forged reservation event evidence'; END IF;

  v_caught := false;
  BEGIN
    UPDATE order_reservations SET order_id=order_id WHERE id=v_reservation;
  EXCEPTION WHEN insufficient_privilege THEN v_caught := true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'app updated reservation identity'; END IF;
  v_caught := false;
  BEGIN
    DELETE FROM order_reservations WHERE id=v_reservation;
  EXCEPTION WHEN insufficient_privilege THEN v_caught := true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'app deleted reservation evidence'; END IF;
  UPDATE coupon_redemptions SET status=status
   WHERE id='10000000-0000-7000-8000-000000000052';
  UPDATE balance_holds SET status=status
   WHERE order_id='10000000-0000-7000-8000-000000000042';
  v_caught := false;
  BEGIN
    UPDATE coupon_redemptions SET discount_amount=discount_amount
     WHERE id='10000000-0000-7000-8000-000000000052';
  EXCEPTION WHEN insufficient_privilege THEN v_caught := true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'app updated coupon reservation identity'; END IF;
  v_caught := false;
  BEGIN
    DELETE FROM coupon_redemptions
     WHERE id='10000000-0000-7000-8000-000000000052';
  EXCEPTION WHEN insufficient_privilege THEN v_caught := true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'app deleted coupon evidence'; END IF;
  v_caught := false;
  BEGIN
    UPDATE balance_holds SET amount=amount
     WHERE order_id='10000000-0000-7000-8000-000000000042';
  EXCEPTION WHEN insufficient_privilege THEN v_caught := true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'app updated balance hold identity'; END IF;
  v_caught := false;
  BEGIN
    DELETE FROM balance_holds
     WHERE order_id='10000000-0000-7000-8000-000000000042';
  EXCEPTION WHEN insufficient_privilege THEN v_caught := true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'app deleted balance hold evidence'; END IF;
  v_caught := false;
  BEGIN
    UPDATE late_payment_cases SET amount=amount;
  EXCEPTION WHEN insufficient_privilege THEN v_caught := true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'app updated late-payment identity'; END IF;
  v_caught := false;
  BEGIN
    DELETE FROM late_payment_cases;
  EXCEPTION WHEN insufficient_privilege THEN v_caught := true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'app deleted late-payment evidence'; END IF;
  INSERT INTO payments
    (tenant_id,order_id,provider_id,provider_payment_id,payment_intent_id,
     currency,amount,fee_amount,status)
  VALUES
    ('10000000-0000-7000-8000-000000000001',
     '10000000-0000-7000-8000-000000000041',
     '10000000-0000-7000-8000-000000000081','acl-payment',
     '10000000-0000-7000-8000-000000000082','CNY',500,0,'succeeded');

  v_caught := false;
  BEGIN
    INSERT INTO orders
      (tenant_id,order_no,user_id,kind,status,currency,subtotal_amount,
       discount_amount,tax_amount,total_amount,balance_applied,payable_amount,
       paid_at)
    VALUES
      ('10000000-0000-7000-8000-000000000001','ACL-FORGED-TIME',
       '10000000-0000-7000-8000-000000000011','renewal','pending_payment','CNY',
       1,0,0,1,0,1,now());
  EXCEPTION WHEN insufficient_privilege THEN v_caught := true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'app forged order payment time'; END IF;
  v_caught := false;
  BEGIN
    INSERT INTO order_items
      (tenant_id,order_id,snapshot_product_name,quantity,unit_amount,line_amount,
       currency,created_at)
    VALUES
      ('10000000-0000-7000-8000-000000000001',v_order,'forged',1,1,1,'CNY',
       now()-interval '1 year');
  EXCEPTION WHEN insufficient_privilege THEN v_caught := true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'app forged order item time'; END IF;
  v_caught := false;
  BEGIN
    INSERT INTO payments
      (tenant_id,order_id,provider_id,provider_payment_id,currency,amount,
       refunded_amount)
    VALUES
      ('10000000-0000-7000-8000-000000000001',v_order,
       '10000000-0000-7000-8000-000000000081','acl-forged-refund',
       'CNY',1,1);
  EXCEPTION WHEN insufficient_privilege THEN v_caught := true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'app forged initial refunded amount'; END IF;
  v_caught := false;
  BEGIN
    INSERT INTO payment_events
      (tenant_id,provider_id,provider_event_id,event_type,raw_payload,
       processing_status)
    VALUES
      ('10000000-0000-7000-8000-000000000001',
       '10000000-0000-7000-8000-000000000081','acl-forged-event',
       'paid','{}','processed');
  EXCEPTION WHEN insufficient_privilege THEN v_caught := true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'app forged initial event assessment'; END IF;

  v_caught := false;
  BEGIN
    INSERT INTO payment_webhook_receipts
      (tenant_id,provider_code,http_method,request_path,raw_body,parse_status)
    VALUES
      ('10000000-0000-7000-8000-000000000001','forged','POST','/forged',
       convert_to('{}','UTF8'),'parsed');
  EXCEPTION WHEN insufficient_privilege THEN v_caught := true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'app supplied initial receipt assessment'; END IF;

  v_caught := false;
  BEGIN
    INSERT INTO refunds
      (tenant_id,order_id,payment_id,currency,amount,reason)
    VALUES
      ('10000000-0000-7000-8000-000000000001',
       '10000000-0000-7000-8000-000000000041',
       '10000000-0000-7000-8000-000000000083','CNY',1,'forged');
  EXCEPTION WHEN insufficient_privilege THEN v_caught := true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'app inserted refund without writer contract'; END IF;
  v_caught := false;
  BEGIN
    INSERT INTO invoices
      (tenant_id,order_id,invoice_no,snapshot_buyer,snapshot_seller,
       snapshot_lines,currency,subtotal_amount,total_amount)
    VALUES
      ('10000000-0000-7000-8000-000000000001',v_order,'ACL-FORGED-INVOICE',
       '{}','{}','[]','CNY',1,1);
  EXCEPTION WHEN insufficient_privilege THEN v_caught := true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'app inserted invoice without writer contract'; END IF;
END $$;
RESET ROLE;
SQL

psql_in <<'SQL'
SET ROLE aegis_app;
SELECT set_config('app.tenant_id','10000000-0000-7000-8000-000000000001',false);
DO $$
DECLARE
  v_key uuid;
  v_retry uuid;
  v_resource uuid:=uuidv7();
  v_caught boolean;
BEGIN
  IF has_table_privilege('aegis_app','idempotency_keys','INSERT')
     OR has_table_privilege('aegis_app','idempotency_keys','UPDATE')
     OR has_table_privilege('aegis_app','idempotency_keys','DELETE')
     OR has_table_privilege('aegis_app','idempotency_keys','TRUNCATE')
     OR NOT has_column_privilege('aegis_app','idempotency_keys','scope','INSERT')
     OR NOT has_column_privilege('aegis_app','idempotency_keys','actor_id','INSERT')
     OR NOT has_column_privilege('aegis_app','idempotency_keys','locked_until','INSERT')
     OR has_column_privilege('aegis_app','idempotency_keys','created_at','INSERT')
     OR NOT has_column_privilege('aegis_app','idempotency_keys','status','UPDATE')
     OR NOT has_column_privilege('aegis_app','idempotency_keys','resource_id','UPDATE')
     OR has_column_privilege('aegis_app','idempotency_keys','actor_id','UPDATE')
     OR has_column_privilege('aegis_app','idempotency_keys','scope','UPDATE')
     OR has_column_privilege('aegis_app','idempotency_keys','idempotency_key','UPDATE')
     OR has_column_privilege('aegis_app','idempotency_keys','request_hash','UPDATE') THEN
    RAISE EXCEPTION 'idempotency ACL is not least privilege';
  END IF;

  INSERT INTO idempotency_keys
    (tenant_id,scope,idempotency_key,request_hash,status,actor_id,locked_until)
  VALUES
    ('10000000-0000-7000-8000-000000000001','evidence_test',
     'app-positive-success',decode(md5('app-positive-success'),'hex'),'in_flight',
     '10000000-0000-7000-8000-000000000011',now()+interval '5 minutes')
  RETURNING id INTO v_key;
  UPDATE idempotency_keys
     SET resource_type='test_resource',resource_id=v_resource
   WHERE id=v_key;

  v_caught:=false;
  BEGIN
    UPDATE idempotency_keys SET request_hash=decode(md5('tampered'),'hex')
     WHERE id=v_key;
  EXCEPTION WHEN insufficient_privilege OR check_violation THEN v_caught:=true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'app mutated idempotency request identity'; END IF;

  v_caught:=false;
  BEGIN
    UPDATE idempotency_keys SET resource_id=uuidv7() WHERE id=v_key;
  EXCEPTION WHEN check_violation THEN v_caught:=true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'app rebound idempotency resource'; END IF;

  v_caught:=false;
  BEGIN
    UPDATE idempotency_keys
       SET status='succeeded',response_code=NULL,response_body=NULL,
           completed_at=now(),locked_until=NULL
     WHERE id=v_key;
  EXCEPTION WHEN check_violation THEN v_caught:=true;
  END;
  IF NOT v_caught THEN
    RAISE EXCEPTION 'app created succeeded idempotency evidence with NULL response_code';
  END IF;

  v_caught:=false;
  BEGIN
    DELETE FROM idempotency_keys WHERE id=v_key;
  EXCEPTION WHEN insufficient_privilege THEN v_caught:=true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'app deleted idempotency evidence'; END IF;

  v_caught:=false;
  BEGIN
    TRUNCATE idempotency_keys;
  EXCEPTION WHEN insufficient_privilege THEN v_caught:=true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'app truncated idempotency evidence'; END IF;

  v_caught:=false;
  BEGIN
    INSERT INTO idempotency_keys
      (tenant_id,scope,idempotency_key,request_hash,status,actor_id,locked_until)
    VALUES
      ('10000000-0000-7000-8000-000000000001','evidence_test',
       'app-forged-terminal',decode(md5('app-forged-terminal'),'hex'),'succeeded',
       '10000000-0000-7000-8000-000000000011',now()+interval '5 minutes');
  EXCEPTION WHEN check_violation THEN v_caught:=true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'app forged initial idempotency terminal state'; END IF;

  UPDATE idempotency_keys
     SET status='succeeded',response_code=201,response_body='{"ok":true}',
         completed_at=now(),locked_until=NULL
   WHERE id=v_key;
  v_caught:=false;
  BEGIN
    UPDATE idempotency_keys SET response_code=202 WHERE id=v_key;
  EXCEPTION WHEN check_violation THEN v_caught:=true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'app mutated successful idempotency evidence'; END IF;

  INSERT INTO idempotency_keys
    (tenant_id,scope,idempotency_key,request_hash,status,actor_id,locked_until)
  VALUES
    ('10000000-0000-7000-8000-000000000001','evidence_test',
     'app-positive-retry',decode(md5('app-positive-retry'),'hex'),'in_flight',
     '10000000-0000-7000-8000-000000000011',now()+interval '5 minutes')
  RETURNING id INTO v_retry;
  UPDATE idempotency_keys
     SET status='failed',response_code=503,response_body='{"retry":true}',
         completed_at=now(),locked_until=NULL
   WHERE id=v_retry;
  UPDATE idempotency_keys
     SET status='in_flight',response_code=NULL,response_body=NULL,
         completed_at=NULL,locked_until=now()+interval '5 minutes'
   WHERE id=v_retry;
  UPDATE idempotency_keys
     SET status='succeeded',response_code=200,response_body='{"ok":true}',
         completed_at=now(),locked_until=NULL
   WHERE id=v_retry;

  v_caught:=false;
  BEGIN
    UPDATE idempotency_keys
       SET status='succeeded',response_code=200,response_body='{"forged":true}',
           completed_at=now(),locked_until=NULL
     WHERE id='10000000-0000-7000-8000-0000000000d1';
    SET CONSTRAINTS ALL IMMEDIATE;
  EXCEPTION WHEN check_violation THEN v_caught:=true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'app committed one-sided refund key terminal state'; END IF;
  v_caught:=false;
  BEGIN
    UPDATE refund_requests SET status='succeeded'
     WHERE id='20000000-0000-7000-8000-0000000000e1';
  EXCEPTION WHEN insufficient_privilege THEN v_caught:=true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'app unilaterally terminalized refund request'; END IF;
END $$;
RESET ROLE;
SELECT 'idempotency_app_role_evidence=ok' AS result;
SELECT 'idempotency_null_response_code_app_role=ok' AS result;
SQL

# Build one schema-35 template for independent fail-closed Up probes. Clones
# keep each malformed legacy condition isolated and make the expected first
# rejection deterministic.
psql_db postgres -c 'CREATE DATABASE aegis_reservation_negative_base' >/dev/null
for migration in "$ROOT"/migrations/000{01..35}_*.sql; do
  up_sql "$migration" | psql_db aegis_reservation_negative_base >/dev/null
done
psql_db aegis_reservation_negative_base >/dev/null <<'SQL'
INSERT INTO tenants(id,slug,display_name,default_currency)
VALUES('50000000-0000-7000-8000-000000000001','negative-base','Negative Base','CNY');
INSERT INTO users(id,tenant_id,email,display_name)
VALUES('50000000-0000-7000-8000-000000000011',
       '50000000-0000-7000-8000-000000000001','negative@example.test','Negative');
INSERT INTO products(id,tenant_id,code,name)
VALUES('50000000-0000-7000-8000-000000000021',
       '50000000-0000-7000-8000-000000000001','negative','Negative');
INSERT INTO plans(id,tenant_id,product_id,code,name)
VALUES('50000000-0000-7000-8000-000000000031',
       '50000000-0000-7000-8000-000000000001',
       '50000000-0000-7000-8000-000000000021','negative','Negative');
INSERT INTO orders
  (id,tenant_id,order_no,user_id,kind,status,currency,subtotal_amount,
   discount_amount,tax_amount,balance_applied,total_amount,payable_amount,
   paid_amount,expires_at,paid_at,fulfilled_at)
VALUES('50000000-0000-7000-8000-000000000041',
       '50000000-0000-7000-8000-000000000001','NEGATIVE',
       '50000000-0000-7000-8000-000000000011','renewal','fulfilled','CNY',
       100,0,0,0,100,100,100,now()-interval '1 hour',
       now()-interval '2 hours',now()-interval '2 hours');
INSERT INTO order_items
  (tenant_id,order_id,product_id,plan_id,snapshot_product_name,snapshot_plan_name,
   snapshot_interval,snapshot_interval_count,quantity,unit_amount,line_amount,currency)
VALUES('50000000-0000-7000-8000-000000000001',
       '50000000-0000-7000-8000-000000000041',
       '50000000-0000-7000-8000-000000000021',
       '50000000-0000-7000-8000-000000000031',
       'Negative','Negative','month',1,1,100,100,'CNY');
INSERT INTO payment_providers(id,tenant_id,code,adapter,display_name,enabled)
VALUES('50000000-0000-7000-8000-000000000081',
       '50000000-0000-7000-8000-000000000001','negative','test','Negative',true);
SQL

expect_up_rejected() {
  local database="$1"
  local pattern="$2"
  local marker="$3"
  if up_sql "$ROOT/migrations/00036_order_reservations.sql" \
       | psql_db "$database" >"$RACE_A_LOG" 2>&1; then
    echo "expected 00036 Up rejection for $database" >&2
    exit 1
  fi
  grep -q "$pattern" "$RACE_A_LOG"
  echo "$marker=ok"
}

psql_db postgres -c 'CREATE DATABASE aegis_negative_idem_null TEMPLATE aegis_reservation_negative_base' >/dev/null
psql_db aegis_negative_idem_null >/dev/null <<'SQL'
INSERT INTO idempotency_keys
  (tenant_id,scope,idempotency_key,request_hash,status,response_code,response_body,
   locked_until,completed_at)
VALUES('50000000-0000-7000-8000-000000000001','generic','null-success-code',
       decode(md5('null-success-code'),'hex'),'succeeded',NULL,'{"ok":true}',NULL,now());
SQL
expect_up_rejected aegis_negative_idem_null \
  'refused malformed idempotency evidence' idempotency_null_response_code_preflight

psql_db postgres -c 'CREATE DATABASE aegis_negative_refund_key TEMPLATE aegis_reservation_negative_base' >/dev/null
psql_db aegis_negative_refund_key >/dev/null <<'SQL'
INSERT INTO idempotency_keys
  (tenant_id,scope,idempotency_key,request_hash,status,locked_until)
VALUES('50000000-0000-7000-8000-000000000001','refund_create','standalone-refund-key',
       decode(md5('standalone-refund-key'),'hex'),'in_flight',now()+interval '5 minutes');
SQL
expect_up_rejected aegis_negative_refund_key \
  'refused ambiguous legacy refund idempotency' standalone_refund_create_preflight

for payment_status in disputed reversed; do
  database="aegis_negative_payment_${payment_status}"
  psql_db postgres -c "CREATE DATABASE $database TEMPLATE aegis_reservation_negative_base" >/dev/null
  psql_db "$database" -v payment_status="$payment_status" >/dev/null <<'SQL'
INSERT INTO payment_intents
  (id,tenant_id,order_id,provider_id,currency,amount,status,provider_ref)
VALUES('50000000-0000-7000-8000-000000000082',
       '50000000-0000-7000-8000-000000000001',
       '50000000-0000-7000-8000-000000000041',
       '50000000-0000-7000-8000-000000000081','CNY',100,'succeeded','negative');
INSERT INTO payments
  (id,tenant_id,order_id,payment_intent_id,provider_id,provider_payment_id,
   currency,amount,status)
VALUES('50000000-0000-7000-8000-000000000083',
       '50000000-0000-7000-8000-000000000001',
       '50000000-0000-7000-8000-000000000041',
       '50000000-0000-7000-8000-000000000082',
       '50000000-0000-7000-8000-000000000081','negative','CNY',100,:'payment_status');
INSERT INTO refunds
  (tenant_id,order_id,payment_id,currency,amount,reason,status,requested_by)
VALUES('50000000-0000-7000-8000-000000000001',
       '50000000-0000-7000-8000-000000000041',
       '50000000-0000-7000-8000-000000000083','CNY',10,
       'invalid payment source','pending','50000000-0000-7000-8000-000000000011');
SQL
  expect_up_rejected "$database" 'refused ambiguous legacy refund data' \
    "legacy_refund_${payment_status}_payment_preflight"
done

psql_db postgres -c 'CREATE DATABASE aegis_negative_refund_succeeded_at TEMPLATE aegis_reservation_negative_base' >/dev/null
psql_db aegis_negative_refund_succeeded_at >/dev/null <<'SQL'
INSERT INTO payment_intents
  (id,tenant_id,order_id,provider_id,currency,amount,status,provider_ref)
VALUES('50000000-0000-7000-8000-000000000082',
       '50000000-0000-7000-8000-000000000001',
       '50000000-0000-7000-8000-000000000041',
       '50000000-0000-7000-8000-000000000081','CNY',100,'succeeded','negative');
INSERT INTO payments
  (id,tenant_id,order_id,payment_intent_id,provider_id,provider_payment_id,
   currency,amount,status)
VALUES('50000000-0000-7000-8000-000000000083',
       '50000000-0000-7000-8000-000000000001',
       '50000000-0000-7000-8000-000000000041',
       '50000000-0000-7000-8000-000000000082',
       '50000000-0000-7000-8000-000000000081','negative','CNY',100,'succeeded');
INSERT INTO refunds
  (tenant_id,order_id,payment_id,currency,amount,reason,status,requested_by,succeeded_at)
VALUES('50000000-0000-7000-8000-000000000001',
       '50000000-0000-7000-8000-000000000041',
       '50000000-0000-7000-8000-000000000083','CNY',10,
       'invalid success evidence','pending','50000000-0000-7000-8000-000000000011',now());
SQL
expect_up_rejected aegis_negative_refund_succeeded_at \
  'refused malformed legacy refund evidence' legacy_refund_succeeded_at_preflight

# A database with no reservation-domain writes must round-trip Up -> Down.
psql_db postgres -c 'CREATE DATABASE aegis_reservation_clean' >/dev/null
for migration in "$ROOT"/migrations/000{01..35}_*.sql; do
  up_sql "$migration" | psql_db aegis_reservation_clean >/dev/null
done
psql_db aegis_reservation_clean >/dev/null <<'SQL'
INSERT INTO tenants(id,slug,display_name,default_currency)
VALUES('40000000-0000-7000-8000-000000000001','legacy-refund-down','Legacy Refund Down','CNY');
INSERT INTO users(id,tenant_id,email,display_name)
VALUES('40000000-0000-7000-8000-000000000011',
       '40000000-0000-7000-8000-000000000001','legacy-refund-down@example.test','Legacy');
INSERT INTO idempotency_keys
  (id,tenant_id,scope,idempotency_key,request_hash,status,response_code,
   response_body,resource_type,resource_id,locked_until,created_at,completed_at,expires_at)
VALUES
  ('40000000-0000-7000-8000-0000000001d1',
   '40000000-0000-7000-8000-000000000001','generic_evidence','generic-preserve',
   decode(md5('generic-preserve'),'hex'),'succeeded',204,NULL,
   'generic_resource','40000000-0000-7000-8000-0000000001d2',NULL,
   '2026-06-28 00:00:00+00','2026-06-29 00:00:00+00','2026-08-01 00:00:00+00');
INSERT INTO products(id,tenant_id,code,name)
VALUES('40000000-0000-7000-8000-000000000021',
       '40000000-0000-7000-8000-000000000001','legacy-refund','Legacy Refund');
INSERT INTO plans(id,tenant_id,product_id,code,name)
VALUES('40000000-0000-7000-8000-000000000031',
       '40000000-0000-7000-8000-000000000001',
       '40000000-0000-7000-8000-000000000021','legacy-refund','Legacy Refund');
INSERT INTO orders
  (id,tenant_id,order_no,user_id,kind,status,currency,subtotal_amount,
   discount_amount,tax_amount,balance_applied,total_amount,payable_amount,
   paid_amount,expires_at,paid_at,fulfilled_at,created_at,updated_at)
VALUES
  ('40000000-0000-7000-8000-000000000041',
   '40000000-0000-7000-8000-000000000001','LEGACY-REFUND-DOWN',
   '40000000-0000-7000-8000-000000000011','renewal','fulfilled','CNY',
   500,0,0,0,500,500,500,'2026-06-30 00:00:00+00',
   '2026-06-29 00:00:00+00','2026-06-29 00:01:00+00',
   '2026-06-28 00:00:00+00','2026-06-29 00:02:00+00');
INSERT INTO order_items
  (tenant_id,order_id,product_id,plan_id,snapshot_product_name,
   snapshot_plan_name,snapshot_interval,snapshot_interval_count,quantity,
   unit_amount,line_amount,currency)
VALUES
  ('40000000-0000-7000-8000-000000000001',
   '40000000-0000-7000-8000-000000000041',
   '40000000-0000-7000-8000-000000000021',
   '40000000-0000-7000-8000-000000000031',
   'Legacy Refund','Legacy Refund','month',1,1,500,500,'CNY');
INSERT INTO payment_providers(id,tenant_id,code,adapter,display_name,enabled)
VALUES('40000000-0000-7000-8000-000000000081',
       '40000000-0000-7000-8000-000000000001','legacy-refund','test','Legacy Refund',true);
INSERT INTO payment_intents
  (id,tenant_id,order_id,provider_id,currency,amount,status,provider_ref)
VALUES('40000000-0000-7000-8000-000000000082',
       '40000000-0000-7000-8000-000000000001',
       '40000000-0000-7000-8000-000000000041',
       '40000000-0000-7000-8000-000000000081','CNY',500,'succeeded','legacy-refund');
INSERT INTO payments
  (id,tenant_id,order_id,payment_intent_id,provider_id,provider_payment_id,
   currency,amount,status,created_at,updated_at)
VALUES('40000000-0000-7000-8000-000000000083',
       '40000000-0000-7000-8000-000000000001',
       '40000000-0000-7000-8000-000000000041',
       '40000000-0000-7000-8000-000000000082',
       '40000000-0000-7000-8000-000000000081','legacy-refund','CNY',500,'succeeded',
       '2026-06-29 00:03:00+00','2026-06-29 00:04:00+00');
INSERT INTO refunds
  (id,tenant_id,order_id,payment_id,currency,amount,reason,status,requested_by,
   failure_message,created_at,updated_at)
VALUES
  ('40000000-0000-7000-8000-0000000001c1','40000000-0000-7000-8000-000000000001',
   '40000000-0000-7000-8000-000000000041','40000000-0000-7000-8000-000000000083',
   'CNY',10,'pending','pending','40000000-0000-7000-8000-000000000011',NULL,
   '2026-07-01 00:00:00+00','2026-07-01 00:01:00+00'),
  ('40000000-0000-7000-8000-0000000001c2','40000000-0000-7000-8000-000000000001',
   '40000000-0000-7000-8000-000000000041','40000000-0000-7000-8000-000000000083',
   'CNY',20,'approved','approved','40000000-0000-7000-8000-000000000011',NULL,
   '2026-07-01 00:02:00+00','2026-07-01 00:03:00+00'),
  ('40000000-0000-7000-8000-0000000001c3','40000000-0000-7000-8000-000000000001',
   '40000000-0000-7000-8000-000000000041','40000000-0000-7000-8000-000000000083',
   'CNY',30,'processing','processing','40000000-0000-7000-8000-000000000011',NULL,
   '2026-07-01 00:04:00+00','2026-07-01 00:05:00+00'),
  ('40000000-0000-7000-8000-0000000001c4','40000000-0000-7000-8000-000000000001',
   '40000000-0000-7000-8000-000000000041','40000000-0000-7000-8000-000000000083',
   'CNY',40,'failed','failed','40000000-0000-7000-8000-000000000011','failed',
   '2026-07-01 00:06:00+00','2026-07-01 00:07:00+00'),
  ('40000000-0000-7000-8000-0000000001c5','40000000-0000-7000-8000-000000000001',
   '40000000-0000-7000-8000-000000000041','40000000-0000-7000-8000-000000000083',
   'CNY',50,'rejected','rejected','40000000-0000-7000-8000-000000000011','rejected',
   '2026-07-01 00:08:00+00','2026-07-01 00:09:00+00');
CREATE TABLE legacy_refunds_00036_expected AS
SELECT id,to_jsonb(r) AS row_data FROM refunds r;
ALTER TABLE legacy_refunds_00036_expected ADD PRIMARY KEY(id);
CREATE TABLE legacy_idempotency_00036_expected AS
SELECT id,to_jsonb(k) AS row_data FROM idempotency_keys k;
ALTER TABLE legacy_idempotency_00036_expected ADD PRIMARY KEY(id);
SQL
up_sql "$ROOT/migrations/00036_order_reservations.sql" | psql_db aegis_reservation_clean >/dev/null
psql_db aegis_reservation_clean -Atc "SELECT count(*)=5 AND bool_and((r.status IN ('pending','approved','processing') AND rr.status IN ('pending','processing') AND k.status='in_flight' AND k.locked_until IS NOT NULL AND k.completed_at IS NULL) OR (r.status IN ('failed','rejected') AND rr.status='failed' AND k.status='failed' AND k.locked_until IS NULL AND k.completed_at IS NOT NULL)) FROM refunds r JOIN refund_requests rr ON rr.id=r.id JOIN idempotency_keys k ON k.id=r.id WHERE r.id::text LIKE '40000000-0000-7000-8000-0000000001c%'" | grep -qx 't'
down_sql "$ROOT/migrations/00036_order_reservations.sql" | psql_db aegis_reservation_clean >/dev/null
psql_db aegis_reservation_clean >/dev/null <<'SQL'
DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM pg_attribute
     WHERE attrelid='idempotency_keys'::regclass
       AND attname='actor_id' AND NOT attisdropped
  ) OR EXISTS (
    SELECT 1 FROM pg_constraint
     WHERE conrelid='idempotency_keys'::regclass
       AND conname='idempotency_keys_actor_tenant_fk'
  ) OR EXISTS (
    SELECT 1 FROM pg_constraint
     WHERE conrelid='idempotency_keys'::regclass
       AND conname IN ('idempotency_keys_resource_pair',
                       'idempotency_keys_evidence_shape')
  ) OR EXISTS (
    SELECT 1 FROM pg_trigger
     WHERE tgrelid='idempotency_keys'::regclass AND NOT tgisinternal
       AND tgname IN ('zz_idempotency_evidence_guard',
                      'trg_idempotency_no_delete',
                      'trg_refund_idempotency_commit')
  ) OR to_regprocedure('app.guard_idempotency_evidence()') IS NOT NULL
    OR to_regprocedure('app.assert_refund_idempotency_key(uuid,uuid)') IS NOT NULL
    OR to_regprocedure('app.assert_refund_idempotency_key_trigger()') IS NOT NULL
  THEN
    RAISE EXCEPTION 'clean Down retained migration-36 idempotency schema';
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='aegis_app')
     AND (NOT has_table_privilege('aegis_app','idempotency_keys','INSERT')
       OR NOT has_table_privilege('aegis_app','idempotency_keys','UPDATE')
       OR NOT has_table_privilege('aegis_app','idempotency_keys','DELETE')
       OR has_table_privilege('aegis_app','idempotency_keys','TRUNCATE')) THEN
    RAISE EXCEPTION 'clean Down did not restore pre-36 idempotency table ACL';
  END IF;
  IF EXISTS (
    SELECT 1 FROM refunds r
    FULL JOIN legacy_refunds_00036_expected e ON e.id=r.id
     WHERE r.id IS NULL OR e.id IS NULL OR to_jsonb(r)<>e.row_data
  ) OR EXISTS (
    SELECT 1 FROM idempotency_keys
     WHERE id::text LIKE '40000000-0000-7000-8000-0000000001c%'
  ) OR EXISTS (
    SELECT 1 FROM idempotency_keys k
    FULL JOIN legacy_idempotency_00036_expected e ON e.id=k.id
     WHERE k.id IS NULL OR e.id IS NULL OR to_jsonb(k)<>e.row_data
  ) THEN
    RAISE EXCEPTION 'clean Down did not restore exact legacy refund rows/keys';
  END IF;
END $$;
DROP TABLE legacy_refunds_00036_expected;
DROP TABLE legacy_idempotency_00036_expected;
SQL
echo "generic_idempotency_clean_down_exact=ok"
echo "legacy_refund_clean_down_exact=ok"
echo "order_reservation_clean_down=ok"

# Even a superuser-simulated legacy aggregate write is detected by Down's
# post-backfill watermark. (Normal writers are stopped earlier by constraints.)
psql_db postgres -c 'CREATE DATABASE aegis_reservation_corrupt' >/dev/null
for migration in "$ROOT"/migrations/000{01..35}_*.sql; do
  up_sql "$migration" | psql_db aegis_reservation_corrupt >/dev/null
done
psql_db aegis_reservation_corrupt >/dev/null <<'SQL'
INSERT INTO tenants(id,slug,display_name)
VALUES('30000000-0000-7000-8000-000000000001','down-corrupt','Down Corrupt');
INSERT INTO products(id,tenant_id,code,name)
VALUES('30000000-0000-7000-8000-000000000002',
       '30000000-0000-7000-8000-000000000001','p','P');
INSERT INTO plans(id,tenant_id,product_id,code,name)
VALUES('30000000-0000-7000-8000-000000000003',
       '30000000-0000-7000-8000-000000000001',
       '30000000-0000-7000-8000-000000000002','p','P');
SQL
up_sql "$ROOT/migrations/00036_order_reservations.sql" | psql_db aegis_reservation_corrupt >/dev/null
psql_db aegis_reservation_corrupt >/dev/null <<'SQL'
ALTER TABLE plans DISABLE TRIGGER trg_plans_reservation_commit;
UPDATE plans SET stock_reserved=1
 WHERE id='30000000-0000-7000-8000-000000000003';
ALTER TABLE plans ENABLE TRIGGER trg_plans_reservation_commit;
SQL
if down_sql "$ROOT/migrations/00036_order_reservations.sql" | \
   psql_db aegis_reservation_corrupt >/dev/null 2>&1; then
  echo "test-order-reservations: Down accepted aggregate drift" >&2
  exit 1
fi
echo "order_reservation_drift_down_protection=ok"

# The migration-created balance posting makes Down intentionally irreversible.
if down_sql "$ROOT/migrations/00036_order_reservations.sql" | psql_in >/dev/null 2>&1; then
  echo "test-order-reservations: Down unexpectedly succeeded" >&2
  exit 1
fi

echo "order_reservation_down_protection=ok"
echo "order_reservation_pg18_suite=ok"

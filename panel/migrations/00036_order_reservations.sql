-- Order reservation domain: durable resource holds, monotonic lifecycle,
-- late-payment quarantine, and tenant/currency-safe ledger references.
--
-- This migration deliberately refuses ambiguous legacy data. Guessing whether
-- a counter represented a pending order or a completed purchase would create
-- inventory or money. Operators must reconcile such rows before retrying.

-- +goose Up

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

-- +goose StatementBegin
DO $$
DECLARE
  v_detail text;
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
     WHERE table_schema = 'public' AND table_name = 'plans'
       AND column_name = 'row_version'
  ) THEN
    RAISE EXCEPTION 'order reservation migration requires schema 00035';
  END IF;

  SELECT format('order %s has %s total/%s plan-bearing lines', o.id,
                count(oi.id),count(oi.id) FILTER (WHERE oi.plan_id IS NOT NULL))
    INTO v_detail
    FROM orders o
    LEFT JOIN order_items oi
      ON oi.tenant_id = o.tenant_id AND oi.order_id = o.id
   WHERE o.kind IN ('new', 'renewal')
   GROUP BY o.id
  HAVING count(oi.id) <> 1
      OR count(oi.id) FILTER (WHERE oi.plan_id IS NOT NULL) <> 1
   LIMIT 1;
  IF v_detail IS NOT NULL THEN
    RAISE EXCEPTION '00036 refused ambiguous legacy order data: %', v_detail;
  END IF;

  SELECT format('order %s kind=%s has invalid line subtotal/currency shape',o.id,o.kind)
    INTO v_detail FROM orders o
   WHERE (o.kind='topup' AND EXISTS (SELECT 1 FROM order_items oi
          WHERE oi.tenant_id=o.tenant_id AND oi.order_id=o.id))
      OR (o.kind<>'topup' AND
          (coalesce((SELECT sum(oi.line_amount) FROM order_items oi
             WHERE oi.tenant_id=o.tenant_id AND oi.order_id=o.id),0)<>o.subtotal_amount
           OR EXISTS (SELECT 1 FROM order_items oi
             WHERE oi.tenant_id=o.tenant_id AND oi.order_id=o.id
               AND oi.currency<>o.currency)))
   LIMIT 1;
  IF v_detail IS NOT NULL THEN
    RAISE EXCEPTION '00036 refused inconsistent legacy order lines: %',v_detail;
  END IF;

  SELECT format('zero-payable order %s kind=%s status=%s lacks fulfilment evidence',id,kind,status)
    INTO v_detail FROM orders
   WHERE kind<>'topup' AND payable_amount=0
     AND (status NOT IN ('fulfilled','partially_refunded','refunded')
          OR fulfilled_at IS NULL)
   LIMIT 1;
  IF v_detail IS NOT NULL THEN
    RAISE EXCEPTION '00036 refused unsafe zero-payable legacy order: %',v_detail;
  END IF;

  SELECT format('zero-paid order %s status=%s claims refunded_amount=%s',
                id,status,refunded_amount)
    INTO v_detail FROM orders
   WHERE paid_amount=0
     AND (refunded_amount<>0 OR status IN ('partially_refunded','refunded'))
   LIMIT 1;
  IF v_detail IS NOT NULL THEN
    RAISE EXCEPTION '00036 refused impossible zero-paid refund state: %',v_detail;
  END IF;

  SELECT format('order %s has unsupported legacy stock quantity %s', o.id, oi.quantity)
    INTO v_detail
    FROM orders o
    JOIN order_items oi
      ON oi.tenant_id = o.tenant_id AND oi.order_id = o.id
   WHERE o.kind = 'new' AND oi.plan_id IS NOT NULL AND oi.quantity <> 1
   LIMIT 1;
  IF v_detail IS NOT NULL THEN
    RAISE EXCEPTION '00036 refused ambiguous legacy quantity: %', v_detail;
  END IF;

  SELECT format('plan %s stock_reserved=%s but legacy orders=%s',
                p.id, p.stock_reserved, count(o.id))
    INTO v_detail
    FROM plans p
    LEFT JOIN order_items oi
      ON oi.tenant_id = p.tenant_id AND oi.plan_id = p.id
    LEFT JOIN orders o
      ON o.tenant_id = oi.tenant_id AND o.id = oi.order_id AND o.kind = 'new'
   WHERE p.stock_total IS NOT NULL
   GROUP BY p.id, p.stock_reserved
  HAVING p.stock_reserved <> count(o.id)
   LIMIT 1;
  IF v_detail IS NOT NULL THEN
    RAISE EXCEPTION '00036 refused inconsistent legacy stock: %', v_detail;
  END IF;

  SELECT format('purchase counter %s/%s expected %s but found %s',
                pc.plan_id, pc.user_id, count(o.id), pc.purchased)
    INTO v_detail
    FROM plan_purchase_counters pc
    LEFT JOIN order_items oi
      ON oi.tenant_id = pc.tenant_id AND oi.plan_id = pc.plan_id
    LEFT JOIN orders o
      ON o.tenant_id = pc.tenant_id AND o.id = oi.order_id
     AND o.user_id = pc.user_id AND o.kind = 'new'
   GROUP BY pc.tenant_id, pc.plan_id, pc.user_id, pc.purchased
  HAVING pc.purchased <> count(o.id)
   LIMIT 1;
  IF v_detail IS NOT NULL THEN
    RAISE EXCEPTION '00036 refused inconsistent legacy purchase counter: %', v_detail;
  END IF;

  SELECT format('limited plan order %s has no purchase counter for plan %s', o.id, oi.plan_id)
    INTO v_detail
    FROM orders o
    JOIN order_items oi
      ON oi.tenant_id = o.tenant_id AND oi.order_id = o.id
     AND oi.plan_id IS NOT NULL
    JOIN plans p ON p.tenant_id = o.tenant_id AND p.id = oi.plan_id
    LEFT JOIN plan_purchase_counters pc
      ON pc.tenant_id = o.tenant_id AND pc.plan_id = oi.plan_id
     AND pc.user_id = o.user_id
   WHERE o.kind = 'new' AND p.purchase_limit_per_user IS NOT NULL
     AND pc.plan_id IS NULL
   LIMIT 1;
  IF v_detail IS NOT NULL THEN
    RAISE EXCEPTION '00036 refused incomplete legacy purchase data: %', v_detail;
  END IF;

  SELECT format('coupon %s redeemed_count=%s but active redemptions=%s',
                c.id, c.redeemed_count,
                count(cr.id) FILTER (WHERE cr.reverted_at IS NULL))
    INTO v_detail
    FROM coupons c
    LEFT JOIN coupon_redemptions cr
      ON cr.tenant_id = c.tenant_id AND cr.coupon_id = c.id
   GROUP BY c.id, c.redeemed_count
  HAVING c.redeemed_count <>
         count(cr.id) FILTER (WHERE cr.reverted_at IS NULL)
   LIMIT 1;
  IF v_detail IS NOT NULL THEN
    RAISE EXCEPTION '00036 refused inconsistent legacy coupon counter: %', v_detail;
  END IF;

  SELECT format('ledger entry %s crosses tenant or currency', e.id)
    INTO v_detail
    FROM ledger_entries e
    JOIN ledger_transactions t ON t.id = e.transaction_id
    JOIN ledger_accounts a ON a.id = e.account_id
   WHERE e.tenant_id <> t.tenant_id OR e.tenant_id <> a.tenant_id
      OR e.currency <> t.currency OR e.currency <> a.currency
   LIMIT 1;
  IF v_detail IS NOT NULL THEN
    RAISE EXCEPTION '00036 refused inconsistent ledger data: %', v_detail;
  END IF;

  SELECT format('refund %s does not identify its payment funding in the same tenant/order/currency',
                r.id)
    INTO v_detail
    FROM refunds r
    LEFT JOIN payments p
      ON p.id=r.payment_id AND p.tenant_id=r.tenant_id
     AND p.order_id=r.order_id AND p.currency=r.currency
   WHERE p.id IS NULL OR r.amount>p.amount
      OR p.status NOT IN ('succeeded','partially_refunded','refunded')
   LIMIT 1;
  IF v_detail IS NOT NULL THEN
    RAISE EXCEPTION '00036 refused ambiguous legacy refund data: %',v_detail;
  END IF;

  SELECT format('payment %s has active refunds %s above funding %s',
                p.id,sum(r.amount),p.amount)
    INTO v_detail
    FROM payments p JOIN refunds r ON r.payment_id=p.id
   WHERE r.status NOT IN ('failed','rejected')
   GROUP BY p.id,p.amount HAVING sum(r.amount)>p.amount LIMIT 1;
  IF v_detail IS NOT NULL THEN
    RAISE EXCEPTION '00036 refused overcommitted legacy refunds: %',v_detail;
  END IF;

  SELECT format('successful legacy refund %s has no unambiguous ledger evidence link',id)
    INTO v_detail FROM refunds WHERE status='succeeded' LIMIT 1;
  IF v_detail IS NOT NULL THEN
    RAISE EXCEPTION '00036 refused unsafe successful legacy refund: %',v_detail;
  END IF;

  SELECT format('non-successful legacy refund %s carries succeeded_at evidence',id)
    INTO v_detail FROM refunds
   WHERE status<>'succeeded' AND succeeded_at IS NOT NULL LIMIT 1;
  IF v_detail IS NOT NULL THEN
    RAISE EXCEPTION '00036 refused malformed legacy refund evidence: %',v_detail;
  END IF;

  SELECT format('pre-existing refund_create key %s cannot be mapped unambiguously',id)
    INTO v_detail FROM idempotency_keys WHERE scope='refund_create' LIMIT 1;
  IF v_detail IS NOT NULL THEN
    RAISE EXCEPTION '00036 refused ambiguous legacy refund idempotency: %',v_detail;
  END IF;

  SELECT format('legacy refund %s collides with an existing idempotency record',r.id)
    INTO v_detail FROM refunds r JOIN idempotency_keys k ON k.id=r.id LIMIT 1;
  IF v_detail IS NOT NULL THEN
    RAISE EXCEPTION '00036 cannot create deterministic legacy refund request: %',v_detail;
  END IF;

  SELECT format('idempotency key %s has a partial resource binding',id)
    INTO v_detail FROM idempotency_keys
   WHERE (resource_type IS NULL)<>(resource_id IS NULL) LIMIT 1;
  IF v_detail IS NOT NULL THEN
    RAISE EXCEPTION '00036 refused ambiguous idempotency evidence: %',v_detail;
  END IF;

  SELECT format('idempotency key %s has invalid status/evidence shape %s',id,status)
    INTO v_detail FROM idempotency_keys
   WHERE expires_at<=created_at
      OR (status='in_flight' AND
          (locked_until IS NULL OR response_code IS NOT NULL
           OR response_body IS NOT NULL OR completed_at IS NOT NULL))
      OR (status='succeeded' AND
          (locked_until IS NOT NULL OR completed_at IS NULL
           OR response_code IS NULL OR response_code NOT BETWEEN 200 AND 299))
      OR (status='failed' AND
          (locked_until IS NOT NULL OR completed_at IS NULL
           OR response_code IS NULL OR response_code BETWEEN 200 AND 299))
   LIMIT 1;
  IF v_detail IS NOT NULL THEN
    RAISE EXCEPTION '00036 refused malformed idempotency evidence: %',v_detail;
  END IF;

  SELECT format('payment %s refund cache %s has no matching successful refund facts',
                p.id,p.refunded_amount)
    INTO v_detail FROM payments p
   WHERE p.refunded_amount<>coalesce((SELECT sum(r.amount) FROM refunds r
          WHERE r.payment_id=p.id AND r.status='succeeded'),0)
   LIMIT 1;
  IF v_detail IS NOT NULL THEN
    RAISE EXCEPTION '00036 refused inconsistent payment refund cache: %',v_detail;
  END IF;

  SELECT format('order %s refund cache %s has no matching successful refund facts',
                o.id,o.refunded_amount)
    INTO v_detail FROM orders o
   WHERE o.refunded_amount<>coalesce((SELECT sum(r.amount) FROM refunds r
          WHERE r.tenant_id=o.tenant_id AND r.order_id=o.id
            AND r.status='succeeded'),0)
   LIMIT 1;
  IF v_detail IS NOT NULL THEN
    RAISE EXCEPTION '00036 refused inconsistent order refund cache: %',v_detail;
  END IF;

  SELECT format('order %s has %s ledger transactions of kind %s',
                source_id, count(*), kind) INTO v_detail
    FROM ledger_transactions
   WHERE source_type='order' AND source_id IS NOT NULL
     AND kind IN ('order_paid','balance_hold','balance_release')
   GROUP BY tenant_id, source_id, kind HAVING count(*)>1 LIMIT 1;
  IF v_detail IS NOT NULL THEN
    RAISE EXCEPTION '00036 refused duplicate order ledger actions: %', v_detail;
  END IF;

  SELECT format('open balance orders for user %s/%s require %s but available balance is %s',
                o.user_id, o.currency, sum(o.balance_applied),
                coalesce(-min(a.balance_signed), 0))
    INTO v_detail
    FROM orders o
    LEFT JOIN ledger_accounts a
      ON a.tenant_id = o.tenant_id
     AND a.owner_user_id = o.user_id
     AND a.currency = o.currency
     AND a.account_type = 'user_balance'
   WHERE o.status IN ('draft', 'pending_payment', 'processing')
     AND o.balance_applied > 0
   GROUP BY o.tenant_id, o.user_id, o.currency
  HAVING count(a.id) = 0 OR sum(o.balance_applied) > -min(a.balance_signed)
   LIMIT 1;
  IF v_detail IS NOT NULL THEN
    RAISE EXCEPTION '00036 cannot safely create legacy balance holds: %', v_detail;
  END IF;
END;
$$;
-- +goose StatementEnd

-- Exact pre-migration values make an unused migration reversible without
-- attempting to infer legacy aggregate semantics during Down.
CREATE TABLE app.order_reservations_00036_meta (
  singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
  installed_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  created_balance_postings boolean NOT NULL DEFAULT false
);
INSERT INTO app.order_reservations_00036_meta DEFAULT VALUES;

CREATE TABLE app.order_reservations_00036_plans AS
  SELECT tenant_id, id AS plan_id, stock_reserved FROM plans;
ALTER TABLE app.order_reservations_00036_plans ADD PRIMARY KEY (plan_id);

CREATE TABLE app.order_reservations_00036_purchase AS
  SELECT tenant_id, plan_id, user_id, purchased FROM plan_purchase_counters;
ALTER TABLE app.order_reservations_00036_purchase
  ADD PRIMARY KEY (tenant_id, plan_id, user_id);

CREATE TABLE app.order_reservations_00036_coupons AS
  SELECT tenant_id, id AS coupon_id, redeemed_count FROM coupons;
ALTER TABLE app.order_reservations_00036_coupons ADD PRIMARY KEY (coupon_id);

CREATE TABLE app.order_reservations_00036_orders AS
  SELECT tenant_id, id AS order_id, status, updated_at, cancelled_at FROM orders;
ALTER TABLE app.order_reservations_00036_orders ADD PRIMARY KEY (order_id);

-- Any post-migration refund creation or mutation makes Down unsafe. Store the
-- exact legacy row image before adding source/evidence columns.
CREATE TABLE app.order_reservations_00036_refunds AS
  SELECT id AS refund_id, to_jsonb(r) AS row_data FROM refunds r;
ALTER TABLE app.order_reservations_00036_refunds ADD PRIMARY KEY (refund_id);

ALTER TABLE idempotency_keys
  ADD COLUMN actor_id uuid,
  ADD CONSTRAINT idempotency_keys_resource_pair CHECK (
    (resource_type IS NULL)=(resource_id IS NULL)
  ),
  ADD CONSTRAINT idempotency_keys_evidence_shape CHECK (
    expires_at>created_at AND (
      (status='in_flight' AND locked_until IS NOT NULL
       AND response_code IS NULL AND response_body IS NULL
       AND completed_at IS NULL)
      OR
      (status='succeeded' AND locked_until IS NULL
       AND completed_at IS NOT NULL AND response_code IS NOT NULL
       AND response_code BETWEEN 200 AND 299)
      OR
      (status='failed' AND locked_until IS NULL
       AND completed_at IS NOT NULL AND response_code IS NOT NULL
       AND response_code NOT BETWEEN 200 AND 299)
    )
  ),
  ADD CONSTRAINT idempotency_keys_tenant_id_id_key UNIQUE (tenant_id, id),
  ADD CONSTRAINT idempotency_keys_actor_tenant_fk
    FOREIGN KEY (tenant_id,actor_id) REFERENCES users(tenant_id,id)
    ON DELETE RESTRICT;

ALTER TABLE orders
  ADD COLUMN business_request_id uuid,
  ADD COLUMN idempotency_key_id uuid,
  ADD COLUMN expired_at timestamptz,
  ADD COLUMN cancel_reason text,
  ADD COLUMN state_version bigint NOT NULL DEFAULT 1 CHECK (state_version > 0),
  ADD CONSTRAINT orders_idempotency_key_fk
    FOREIGN KEY (tenant_id, idempotency_key_id)
    REFERENCES idempotency_keys (tenant_id, id) ON DELETE RESTRICT;

UPDATE orders SET business_request_id = uuidv7();
ALTER TABLE orders ALTER COLUMN business_request_id SET NOT NULL;

CREATE UNIQUE INDEX orders_business_request_unique
  ON orders (tenant_id, user_id, business_request_id);
CREATE UNIQUE INDEX orders_idempotency_record_unique
  ON orders (tenant_id, idempotency_key_id) WHERE idempotency_key_id IS NOT NULL;
CREATE UNIQUE INDEX orders_one_active_renewal
  ON orders (tenant_id, subscription_id)
  WHERE kind = 'renewal'
    AND status IN ('draft', 'pending_payment', 'processing', 'paid');

UPDATE orders SET expired_at = updated_at
 WHERE status = 'expired' AND expired_at IS NULL;

CREATE TABLE order_transitions (
  from_status text NOT NULL,
  to_status text NOT NULL,
  PRIMARY KEY (from_status, to_status)
);
INSERT INTO order_transitions (from_status, to_status) VALUES
  ('draft','pending_payment'), ('draft','paid'), ('draft','cancelled'), ('draft','expired'),
  ('pending_payment','processing'), ('pending_payment','paid'),
  ('pending_payment','cancelled'), ('pending_payment','expired'),
  ('processing','pending_payment'), ('processing','paid'),
  ('processing','cancelled'), ('processing','expired'),
  ('paid','fulfilled'), ('paid','refunded'), ('paid','partially_refunded'),
  ('fulfilled','refunded'), ('fulfilled','partially_refunded'),
  ('partially_refunded','refunded');

-- +goose StatementBegin
CREATE FUNCTION app.guard_order_transition() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF (to_jsonb(NEW) - ARRAY['status','state_version','paid_amount','refunded_amount',
                            'paid_at','fulfilled_at','cancelled_at','expired_at',
                            'cancel_reason','subscription_id','updated_at'])
     IS DISTINCT FROM
     (to_jsonb(OLD) - ARRAY['status','state_version','paid_amount','refunded_amount',
                            'paid_at','fulfilled_at','cancelled_at','expired_at',
                            'cancel_reason','subscription_id','updated_at']) THEN
    RAISE EXCEPTION 'order identity, pricing, currency and business request are immutable'
      USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.paid_amount < OLD.paid_amount OR NEW.refunded_amount < OLD.refunded_amount THEN
    RAISE EXCEPTION 'order paid/refunded amounts are monotonic'
      USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.paid_amount IS DISTINCT FROM OLD.paid_amount AND NOT
     (OLD.status IS DISTINCT FROM 'paid' AND NEW.status='paid'
      AND NEW.paid_at IS NOT NULL AND NEW.paid_amount=NEW.total_amount) THEN
    RAISE EXCEPTION 'paid amount/time may only be fixed by the paid transition'
      USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.refunded_amount IS DISTINCT FROM OLD.refunded_amount AND NOT
     (NEW.refunded_amount>OLD.refunded_amount AND
      ((NEW.status='partially_refunded' AND
        OLD.status IN ('paid','fulfilled','partially_refunded') AND
        NEW.refunded_amount<NEW.paid_amount)
       OR (NEW.status='refunded' AND OLD.status IN ('paid','fulfilled','partially_refunded')
           AND NEW.refunded_amount=NEW.paid_amount))) THEN
    RAISE EXCEPTION 'refund amount must match its partial/full refund transition'
      USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.status IS NOT DISTINCT FROM OLD.status AND
     (NEW.paid_at IS DISTINCT FROM OLD.paid_at
      OR NEW.fulfilled_at IS DISTINCT FROM OLD.fulfilled_at
      OR NEW.cancelled_at IS DISTINCT FROM OLD.cancelled_at
      OR NEW.expired_at IS DISTINCT FROM OLD.expired_at
      OR NEW.cancel_reason IS DISTINCT FROM OLD.cancel_reason
      OR NEW.subscription_id IS DISTINCT FROM OLD.subscription_id) THEN
    RAISE EXCEPTION 'order lifecycle evidence is immutable outside a state transition'
      USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.paid_at IS DISTINCT FROM OLD.paid_at AND NEW.status<>'paid' THEN
    RAISE EXCEPTION 'paid_at may only be established by the paid transition'
      USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.fulfilled_at IS DISTINCT FROM OLD.fulfilled_at AND NEW.status<>'fulfilled' THEN
    RAISE EXCEPTION 'fulfilled_at may only be established by the fulfilled transition'
      USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.status='fulfilled' AND OLD.status IS DISTINCT FROM 'fulfilled'
     AND NEW.fulfilled_at IS NULL THEN
    RAISE EXCEPTION 'fulfilled transition requires fulfilled_at'
      USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.subscription_id IS DISTINCT FROM OLD.subscription_id
     AND NEW.status<>'fulfilled' THEN
    RAISE EXCEPTION 'subscription link may only be established by fulfilment'
      USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.cancelled_at IS DISTINCT FROM OLD.cancelled_at AND NEW.status<>'cancelled' THEN
    RAISE EXCEPTION 'cancelled_at may only be established by the cancelled transition'
      USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.status='cancelled' AND OLD.status IS DISTINCT FROM 'cancelled'
     AND (NEW.cancelled_at IS NULL OR nullif(btrim(NEW.cancel_reason),'') IS NULL) THEN
    RAISE EXCEPTION 'cancelled transition requires timestamp and reason'
      USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.expired_at IS DISTINCT FROM OLD.expired_at AND NEW.status<>'expired' THEN
    RAISE EXCEPTION 'expired_at may only be established by the expired transition'
      USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.status IS DISTINCT FROM OLD.status THEN
    IF NOT EXISTS (
      SELECT 1 FROM order_transitions
       WHERE from_status = OLD.status AND to_status = NEW.status
    ) THEN
      RAISE EXCEPTION 'illegal order transition % -> % for order %',
        OLD.status, NEW.status, OLD.id USING ERRCODE = 'check_violation';
    END IF;
    NEW.state_version := OLD.state_version + 1;
  ELSIF NEW.state_version IS DISTINCT FROM OLD.state_version THEN
    RAISE EXCEPTION 'order state_version is maintained by the state guard'
      USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.status = 'expired' AND NEW.expired_at IS NULL THEN
    NEW.expired_at := clock_timestamp();
  END IF;
  RETURN NEW;
END;
$$;

CREATE FUNCTION app.guard_order_business_request() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.idempotency_key_id IS NULL THEN
    RAISE EXCEPTION 'new orders require a claimed idempotency record'
      USING ERRCODE = 'not_null_violation';
  END IF;
  IF NEW.business_request_id IS NULL THEN
    NEW.business_request_id := NEW.idempotency_key_id;
  ELSIF NEW.business_request_id <> NEW.idempotency_key_id THEN
    RAISE EXCEPTION 'business_request_id must equal idempotency_key_id'
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER trg_orders_state_guard BEFORE UPDATE ON orders
  FOR EACH ROW EXECUTE FUNCTION app.guard_order_transition();
CREATE TRIGGER trg_orders_business_request BEFORE INSERT ON orders
  FOR EACH ROW EXECUTE FUNCTION app.guard_order_business_request();

ALTER TABLE plans
  ADD COLUMN stock_sold int NOT NULL DEFAULT 0 CHECK (stock_sold >= 0),
  DROP CONSTRAINT plans_stock_not_oversold,
  ADD CONSTRAINT plans_stock_capacity CHECK (
    stock_total IS NULL OR stock_sold + stock_reserved <= stock_total
  );

ALTER TABLE plan_purchase_counters
  ADD COLUMN reserved int NOT NULL DEFAULT 0 CHECK (reserved >= 0);

ALTER TABLE coupons
  ADD COLUMN reserved_count int NOT NULL DEFAULT 0 CHECK (reserved_count >= 0),
  DROP CONSTRAINT coupons_not_oversold,
  ADD CONSTRAINT coupons_capacity CHECK (
    max_redemptions IS NULL OR redeemed_count + reserved_count <= max_redemptions
  );

ALTER TABLE ledger_accounts
  DROP CONSTRAINT ledger_accounts_account_type_check,
  ADD CONSTRAINT ledger_accounts_account_type_check CHECK (
    account_type IN ('user_balance', 'user_balance_hold',
                     'user_commission_pending', 'user_commission_available',
                     'channel_cash', 'platform_revenue', 'platform_fee_expense',
                     'refund_payable', 'dispute_hold', 'suspense',
                     'late_payment_suspense')
  ),
  ADD CONSTRAINT ledger_accounts_tenant_id_currency_key
    UNIQUE (tenant_id, id, currency);

ALTER TABLE ledger_transactions
  ADD CONSTRAINT ledger_transactions_tenant_id_currency_key
    UNIQUE (tenant_id, id, currency);
CREATE UNIQUE INDEX ledger_transactions_order_balance_action_unique
  ON ledger_transactions (tenant_id, source_id, kind)
  WHERE source_type='order' AND source_id IS NOT NULL
    AND kind IN ('order_paid','balance_hold','balance_release');
CREATE UNIQUE INDEX ledger_transactions_refund_action_unique
  ON ledger_transactions (tenant_id,source_id,kind)
  WHERE source_type='refund' AND source_id IS NOT NULL AND kind='refund_issued';

ALTER TABLE ledger_entries
  ADD CONSTRAINT ledger_entries_transaction_tenant_currency_fk
    FOREIGN KEY (tenant_id, transaction_id, currency)
    REFERENCES ledger_transactions (tenant_id, id, currency) ON DELETE RESTRICT,
  ADD CONSTRAINT ledger_entries_account_tenant_currency_fk
    FOREIGN KEY (tenant_id, account_id, currency)
    REFERENCES ledger_accounts (tenant_id, id, currency) ON DELETE RESTRICT;

ALTER TABLE payment_events
  ADD CONSTRAINT payment_events_tenant_id_id_key UNIQUE (tenant_id, id);

ALTER TABLE payments
  ADD CONSTRAINT payments_tenant_id_id_order_currency_key
    UNIQUE (tenant_id, id, order_id, currency);
ALTER TABLE orders
  ADD CONSTRAINT orders_tenant_id_id_currency_key UNIQUE (tenant_id,id,currency);
ALTER TABLE payment_intents
  ADD CONSTRAINT payment_intents_business_identity_key
    UNIQUE (tenant_id,id,order_id,currency,provider_id);

CREATE TABLE refund_requests (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE RESTRICT,
  order_id uuid NOT NULL,
  currency app.currency_code NOT NULL,
  requested_amount app.minor_amount NOT NULL CHECK (requested_amount>0),
  business_request_id uuid NOT NULL,
  request_hash bytea NOT NULL,
  status text NOT NULL DEFAULT 'pending' CHECK (status IN (
    'pending','processing','succeeded','failed','manual_review','partially_succeeded'
  )),
  requested_by uuid,
  approval_request_id uuid,
  reason text NOT NULL,
  legacy_backfill boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT refund_requests_tenant_id_id_order_currency_key
    UNIQUE (tenant_id,id,order_id,currency),
  CONSTRAINT refund_requests_business_unique
    UNIQUE (tenant_id,business_request_id),
  CONSTRAINT refund_requests_business_request_fk
    FOREIGN KEY (tenant_id,business_request_id)
    REFERENCES idempotency_keys(tenant_id,id) ON DELETE RESTRICT,
  CONSTRAINT refund_requests_order_tenant_fk
    FOREIGN KEY (tenant_id,order_id,currency)
    REFERENCES orders(tenant_id,id,currency) ON DELETE RESTRICT,
  CONSTRAINT refund_requests_requested_by_tenant_fk
    FOREIGN KEY (tenant_id,requested_by)
    REFERENCES users(tenant_id,id) ON DELETE RESTRICT
);
CREATE INDEX refund_requests_work_idx ON refund_requests(tenant_id,status,created_at)
  WHERE status IN ('pending','processing','manual_review','partially_succeeded');
CREATE TRIGGER trg_refund_requests_updated_at BEFORE UPDATE ON refund_requests
  FOR EACH ROW EXECUTE FUNCTION app.set_updated_at();
SELECT app.enable_tenant_rls('refund_requests');

INSERT INTO idempotency_keys
  (id,tenant_id,scope,idempotency_key,request_hash,status,actor_id,
   resource_type,resource_id,locked_until,response_code,response_body,completed_at)
SELECT r.id,r.tenant_id,'refund_create','legacy-refund:'||r.id::text,
       decode(md5(r.id::text),'hex'),
       CASE WHEN r.status IN ('failed','rejected') THEN 'failed'
            ELSE 'in_flight' END,
       r.requested_by,'refund_request',r.id,
       CASE WHEN r.status IN ('pending','approved','processing')
            THEN now()+interval '5 minutes' END,
       CASE WHEN r.status='failed' THEN 500
            WHEN r.status='rejected' THEN 422 END,
       CASE WHEN r.status IN ('failed','rejected')
            THEN jsonb_build_object('status',r.status,'legacy',true) END,
       CASE WHEN r.status IN ('failed','rejected') THEN r.updated_at END
  FROM refunds r;

CREATE TABLE app.order_reservations_00036_refund_idempotency AS
  SELECT k.id AS key_id,to_jsonb(k) AS row_data,
         EXISTS (SELECT 1 FROM refunds r WHERE r.id=k.id
          AND k.idempotency_key='legacy-refund:'||r.id::text) AS migration_created
    FROM idempotency_keys k;
ALTER TABLE app.order_reservations_00036_refund_idempotency
  ADD PRIMARY KEY (key_id);

INSERT INTO refund_requests
  (id,tenant_id,order_id,currency,requested_amount,business_request_id,request_hash,status,
   requested_by,approval_request_id,reason,legacy_backfill,created_at,updated_at)
SELECT r.id,r.tenant_id,r.order_id,r.currency,r.amount,r.id,k.request_hash,
       CASE WHEN r.status IN ('failed','rejected') THEN 'failed'
            WHEN r.status IN ('approved','processing') THEN 'processing'
            ELSE 'pending' END,
       r.requested_by,r.approval_request_id,r.reason,true,r.created_at,r.updated_at
  FROM refunds r JOIN idempotency_keys k ON k.id=r.id;

CREATE TABLE app.order_reservations_00036_refund_requests AS
  SELECT id AS request_id,to_jsonb(rr) AS row_data FROM refund_requests rr;
ALTER TABLE app.order_reservations_00036_refund_requests
  ADD PRIMARY KEY (request_id);

ALTER TABLE refunds
  ADD COLUMN refund_request_id uuid,
  ADD COLUMN source_kind text NOT NULL DEFAULT 'payment',
  ADD COLUMN balance_hold_id uuid,
  ADD COLUMN ledger_txn_id uuid,
  ALTER COLUMN payment_id DROP NOT NULL,
  ADD CONSTRAINT refunds_source_kind_check
    CHECK (source_kind IN ('payment','balance')),
  ADD CONSTRAINT refunds_source_exactly_one CHECK (
    (source_kind='payment' AND payment_id IS NOT NULL AND balance_hold_id IS NULL)
    OR
    (source_kind='balance' AND payment_id IS NULL AND balance_hold_id IS NOT NULL)
  ),
  ADD CONSTRAINT refunds_ledger_txn_tenant_fk
    FOREIGN KEY (tenant_id,ledger_txn_id,currency)
    REFERENCES ledger_transactions(tenant_id,id,currency) ON DELETE RESTRICT,
  ADD CONSTRAINT refunds_tenant_id_id_order_currency_key
    UNIQUE (tenant_id, id, order_id, currency);
ALTER TABLE refunds DISABLE TRIGGER trg_refunds_updated_at;
UPDATE refunds SET refund_request_id=id;
ALTER TABLE refunds ENABLE TRIGGER trg_refunds_updated_at;
ALTER TABLE refunds
  ALTER COLUMN refund_request_id SET NOT NULL,
  ADD CONSTRAINT refunds_request_tenant_fk
    FOREIGN KEY (tenant_id,refund_request_id,order_id,currency)
    REFERENCES refund_requests(tenant_id,id,order_id,currency) ON DELETE RESTRICT,
  DROP CONSTRAINT refunds_requested_by_fkey,
  ADD CONSTRAINT refunds_requested_by_tenant_fk
    FOREIGN KEY (tenant_id,requested_by)
    REFERENCES users(tenant_id,id) ON DELETE RESTRICT;
CREATE INDEX idx_refunds_balance_hold ON refunds(balance_hold_id);
CREATE UNIQUE INDEX refunds_request_source_unique ON refunds
  (tenant_id,refund_request_id,source_kind,coalesce(payment_id,balance_hold_id));

-- Every existing order child carries the tenant in its FK.  Keeping the old
-- single-column constraints preserves their historical delete action while
-- these validated composite constraints make cross-tenant references
-- impossible even for privileged import/reconciliation code.
ALTER TABLE order_items ADD CONSTRAINT order_items_order_tenant_fk
  FOREIGN KEY (tenant_id,order_id,currency) REFERENCES orders(tenant_id,id,currency)
  ON DELETE CASCADE;
ALTER TABLE payment_intents ADD CONSTRAINT payment_intents_order_tenant_fk
  FOREIGN KEY (tenant_id,order_id,currency) REFERENCES orders(tenant_id,id,currency)
  ON DELETE CASCADE;
ALTER TABLE payments ADD CONSTRAINT payments_order_tenant_fk
  FOREIGN KEY (tenant_id,order_id,currency) REFERENCES orders(tenant_id,id,currency)
  ON DELETE RESTRICT;
ALTER TABLE refunds ADD CONSTRAINT refunds_order_tenant_fk
  FOREIGN KEY (tenant_id,order_id,currency) REFERENCES orders(tenant_id,id,currency)
  ON DELETE RESTRICT;
ALTER TABLE invoices ADD CONSTRAINT invoices_order_tenant_fk
  FOREIGN KEY (tenant_id,order_id,currency) REFERENCES orders(tenant_id,id,currency)
  ON DELETE RESTRICT;
ALTER TABLE subscription_events ADD CONSTRAINT subscription_events_order_tenant_fk
  FOREIGN KEY (tenant_id,order_id) REFERENCES orders(tenant_id,id)
  DEFERRABLE INITIALLY IMMEDIATE;
ALTER TABLE coupon_redemptions ADD CONSTRAINT coupon_redemptions_order_tenant_fk
  FOREIGN KEY (tenant_id,order_id,currency) REFERENCES orders(tenant_id,id,currency)
  ON DELETE CASCADE;
ALTER TABLE gift_codes ADD CONSTRAINT gift_codes_order_tenant_fk
  FOREIGN KEY (tenant_id,order_id) REFERENCES orders(tenant_id,id)
  DEFERRABLE INITIALLY IMMEDIATE;
ALTER TABLE commission_entries ADD CONSTRAINT commission_entries_order_tenant_fk
  FOREIGN KEY (tenant_id,order_id,currency) REFERENCES orders(tenant_id,id,currency)
  ON DELETE RESTRICT;
ALTER TABLE tickets ADD CONSTRAINT tickets_related_order_tenant_fk
  FOREIGN KEY (tenant_id,related_order_id) REFERENCES orders(tenant_id,id)
  DEFERRABLE INITIALLY IMMEDIATE;
ALTER TABLE orders ADD CONSTRAINT orders_coupon_tenant_fk
  FOREIGN KEY (tenant_id,coupon_id) REFERENCES coupons(tenant_id,id)
  DEFERRABLE INITIALLY IMMEDIATE;
ALTER TABLE orders ADD CONSTRAINT orders_subscription_tenant_fk
  FOREIGN KEY (tenant_id,subscription_id) REFERENCES subscriptions(tenant_id,id)
  DEFERRABLE INITIALLY IMMEDIATE;
ALTER TABLE order_items ADD CONSTRAINT order_items_plan_tenant_fk
  FOREIGN KEY (tenant_id,plan_id) REFERENCES plans(tenant_id,id)
  DEFERRABLE INITIALLY IMMEDIATE;
ALTER TABLE payments ADD CONSTRAINT payments_intent_tenant_fk
  FOREIGN KEY (tenant_id,payment_intent_id,order_id,currency,provider_id)
  REFERENCES payment_intents(tenant_id,id,order_id,currency,provider_id)
  DEFERRABLE INITIALLY IMMEDIATE;
ALTER TABLE refunds ADD CONSTRAINT refunds_payment_tenant_fk
  FOREIGN KEY (tenant_id,payment_id,order_id,currency)
  REFERENCES payments(tenant_id,id,order_id,currency) ON DELETE RESTRICT;
ALTER TABLE commission_entries ADD CONSTRAINT commission_entries_payment_tenant_fk
  FOREIGN KEY (tenant_id,payment_id,order_id,currency)
  REFERENCES payments(tenant_id,id,order_id,currency)
  DEFERRABLE INITIALLY IMMEDIATE;

-- Raw webhook evidence is committed before parsing or signature handling.  The
-- The application role may insert/read receipts and update only the guarded
-- assessment columns; request identity and raw evidence are permanently fixed.
CREATE TABLE payment_webhook_receipts (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE RESTRICT,
  provider_id uuid,
  provider_code text NOT NULL,
  http_method text NOT NULL,
  request_path text NOT NULL,
  query_string text NOT NULL DEFAULT '',
  raw_headers jsonb NOT NULL DEFAULT '{}'::jsonb,
  raw_body bytea NOT NULL CHECK (octet_length(raw_body) <= 4194304),
  parse_status text NOT NULL DEFAULT 'unattempted'
    CHECK (parse_status IN ('unattempted', 'parsed', 'malformed')),
  signature_status text NOT NULL DEFAULT 'unattempted'
    CHECK (signature_status IN ('unattempted', 'verified', 'rejected', 'unavailable')),
  parse_error text,
  parsed_at timestamptz,
  signature_checked_at timestamptz,
  parsed_provider_event_id text,
  payment_event_id uuid,
  source_ip inet,
  received_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  CONSTRAINT payment_webhook_receipts_headers_size
    CHECK (pg_column_size(raw_headers) <= 65536),
  CONSTRAINT payment_webhook_receipts_tenant_id_id_key UNIQUE (tenant_id, id),
  CONSTRAINT payment_webhook_receipts_provider_fk
    FOREIGN KEY (tenant_id, provider_id)
    REFERENCES payment_providers (tenant_id, id) ON DELETE RESTRICT,
  CONSTRAINT payment_webhook_receipts_event_fk
    FOREIGN KEY (tenant_id, payment_event_id)
    REFERENCES payment_events (tenant_id, id) ON DELETE RESTRICT,
  CONSTRAINT payment_webhook_receipts_parse_shape CHECK (
    (parse_status = 'unattempted' AND parsed_at IS NULL)
    OR (parse_status IN ('parsed', 'malformed') AND parsed_at IS NOT NULL)
  ),
  CONSTRAINT payment_webhook_receipts_signature_shape CHECK (
    (signature_status = 'unattempted' AND signature_checked_at IS NULL)
    OR (signature_status <> 'unattempted' AND signature_checked_at IS NOT NULL)
  )
);
CREATE INDEX payment_webhook_receipts_received_idx
  ON payment_webhook_receipts (tenant_id, provider_id, received_at DESC);

COMMENT ON COLUMN payment_webhook_receipts.raw_headers IS
  'Allowlisted diagnostic headers only. Authorization, Cookie, Set-Cookie, API keys and full signatures must be removed or irreversibly redacted before INSERT.';
COMMENT ON COLUMN payment_webhook_receipts.raw_body IS
  'Exact pre-parse request bytes, capped at 4 MiB. Commit this evidence before JSON/form parsing or signature processing.';

CREATE TABLE order_reservations (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  order_id uuid NOT NULL,
  user_id uuid NOT NULL,
  state text NOT NULL DEFAULT 'held'
    CHECK (state IN ('held', 'captured', 'released')),
  expires_at timestamptz NOT NULL,
  held_at timestamptz NOT NULL DEFAULT now(),
  captured_at timestamptz,
  released_at timestamptz,
  release_reason text,
  state_version bigint NOT NULL DEFAULT 1 CHECK (state_version > 0),
  legacy_backfill boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT order_reservations_tenant_id_id_key UNIQUE (tenant_id, id),
  CONSTRAINT order_reservations_tenant_id_id_order_key
    UNIQUE (tenant_id, id, order_id),
  CONSTRAINT order_reservations_tenant_id_id_order_user_key
    UNIQUE (tenant_id, id, order_id, user_id),
  CONSTRAINT order_reservations_order_unique UNIQUE (tenant_id, order_id),
  CONSTRAINT order_reservations_order_fk FOREIGN KEY (tenant_id, order_id)
    REFERENCES orders (tenant_id, id) ON DELETE RESTRICT,
  CONSTRAINT order_reservations_user_fk FOREIGN KEY (tenant_id, user_id)
    REFERENCES users (tenant_id, id) ON DELETE RESTRICT,
  CONSTRAINT order_reservations_state_times CHECK (
    (state = 'held' AND captured_at IS NULL AND released_at IS NULL)
    OR (state = 'captured' AND captured_at IS NOT NULL AND released_at IS NULL)
    OR (state = 'released' AND released_at IS NOT NULL AND captured_at IS NULL)
  )
);
CREATE INDEX order_reservations_expiry_idx
  ON order_reservations (expires_at, id) WHERE state = 'held';

CREATE TABLE order_stock_reservations (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  reservation_id uuid NOT NULL,
  order_id uuid NOT NULL,
  plan_id uuid NOT NULL,
  quantity int NOT NULL CHECK (quantity > 0),
  legacy_backfill boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT order_stock_reservation_unique UNIQUE (tenant_id, order_id, plan_id),
  CONSTRAINT order_stock_reservation_parent_fk
    FOREIGN KEY (tenant_id, reservation_id, order_id)
    REFERENCES order_reservations (tenant_id, id, order_id) ON DELETE RESTRICT,
  CONSTRAINT order_stock_reservation_plan_fk FOREIGN KEY (tenant_id, plan_id)
    REFERENCES plans (tenant_id, id) ON DELETE RESTRICT
);
CREATE INDEX order_stock_reservations_plan_idx
  ON order_stock_reservations (tenant_id, plan_id);

CREATE TABLE order_purchase_limit_reservations (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  reservation_id uuid NOT NULL,
  order_id uuid NOT NULL,
  plan_id uuid NOT NULL,
  user_id uuid NOT NULL,
  quantity int NOT NULL CHECK (quantity > 0),
  legacy_backfill boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT order_purchase_limit_reservation_unique
    UNIQUE (tenant_id, order_id, plan_id, user_id),
  CONSTRAINT order_purchase_limit_parent_fk
    FOREIGN KEY (tenant_id, reservation_id, order_id, user_id)
    REFERENCES order_reservations (tenant_id, id, order_id, user_id) ON DELETE RESTRICT,
  CONSTRAINT order_purchase_limit_counter_fk FOREIGN KEY (tenant_id, plan_id, user_id)
    REFERENCES plan_purchase_counters (tenant_id, plan_id, user_id) ON DELETE RESTRICT
);

ALTER TABLE coupon_redemptions
  ADD COLUMN reservation_id uuid,
  ADD COLUMN status text,
  ADD COLUMN captured_at timestamptz,
  ADD COLUMN released_at timestamptz,
  ADD COLUMN legacy_backfill boolean NOT NULL DEFAULT false;

CREATE TABLE balance_holds (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  reservation_id uuid NOT NULL,
  order_id uuid NOT NULL,
  user_id uuid NOT NULL,
  currency app.currency_code NOT NULL,
  amount app.minor_amount NOT NULL CHECK (amount > 0),
  available_account_id uuid,
  hold_account_id uuid,
  status text NOT NULL DEFAULT 'held'
    CHECK (status IN ('held', 'captured', 'released')),
  hold_txn_id uuid,
  capture_txn_id uuid,
  release_txn_id uuid,
  held_at timestamptz NOT NULL DEFAULT now(),
  captured_at timestamptz,
  released_at timestamptz,
  legacy_backfill boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT balance_holds_order_unique UNIQUE (tenant_id, order_id),
  CONSTRAINT balance_holds_tenant_id_id_order_currency_key
    UNIQUE (tenant_id, id, order_id, currency),
  CONSTRAINT balance_holds_parent_fk
    FOREIGN KEY (tenant_id, reservation_id, order_id, user_id)
    REFERENCES order_reservations (tenant_id, id, order_id, user_id) ON DELETE RESTRICT,
  CONSTRAINT balance_holds_available_account_fk
    FOREIGN KEY (tenant_id, available_account_id, currency)
    REFERENCES ledger_accounts (tenant_id, id, currency) ON DELETE RESTRICT,
  CONSTRAINT balance_holds_hold_account_fk
    FOREIGN KEY (tenant_id, hold_account_id, currency)
    REFERENCES ledger_accounts (tenant_id, id, currency) ON DELETE RESTRICT,
  CONSTRAINT balance_holds_hold_txn_fk
    FOREIGN KEY (tenant_id, hold_txn_id, currency)
    REFERENCES ledger_transactions (tenant_id, id, currency) ON DELETE RESTRICT,
  CONSTRAINT balance_holds_capture_txn_fk
    FOREIGN KEY (tenant_id, capture_txn_id, currency)
    REFERENCES ledger_transactions (tenant_id, id, currency) ON DELETE RESTRICT,
  CONSTRAINT balance_holds_release_txn_fk
    FOREIGN KEY (tenant_id, release_txn_id, currency)
    REFERENCES ledger_transactions (tenant_id, id, currency) ON DELETE RESTRICT,
  CONSTRAINT balance_holds_state_times CHECK (
    (status = 'held' AND captured_at IS NULL AND released_at IS NULL)
    OR (status = 'captured' AND captured_at IS NOT NULL AND released_at IS NULL)
    OR (status = 'released' AND released_at IS NOT NULL AND captured_at IS NULL)
  ),
  CONSTRAINT balance_holds_accounts_required CHECK (
    (available_account_id IS NOT NULL AND hold_account_id IS NOT NULL)
    OR (legacy_backfill AND status IN ('captured', 'released'))
  )
);
CREATE INDEX balance_holds_account_idx
  ON balance_holds (tenant_id, available_account_id) WHERE status = 'held';

ALTER TABLE refunds ADD CONSTRAINT refunds_balance_hold_tenant_fk
  FOREIGN KEY (tenant_id,balance_hold_id,order_id,currency)
  REFERENCES balance_holds(tenant_id,id,order_id,currency) ON DELETE RESTRICT;

CREATE TABLE late_payment_cases (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  order_id uuid NOT NULL,
  payment_id uuid NOT NULL,
  payment_event_id uuid,
  amount app.minor_amount NOT NULL CHECK (amount > 0),
  currency app.currency_code NOT NULL,
  status text NOT NULL DEFAULT 'suspense'
    CHECK (status IN ('suspense', 'refund_pending', 'refunded',
                      'manual_review', 'applied')),
  suspense_txn_id uuid NOT NULL,
  refund_id uuid,
  refund_txn_id uuid,
  business_request_id uuid NOT NULL DEFAULT uuidv7(),
  resolution_reason text,
  received_at timestamptz NOT NULL DEFAULT now(),
  resolved_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT late_payment_cases_tenant_id_id_key UNIQUE (tenant_id, id),
  CONSTRAINT late_payment_cases_payment_unique UNIQUE (tenant_id, payment_id),
  CONSTRAINT late_payment_cases_order_fk FOREIGN KEY (tenant_id, order_id)
    REFERENCES orders (tenant_id, id) ON DELETE RESTRICT,
  CONSTRAINT late_payment_cases_payment_fk
    FOREIGN KEY (tenant_id, payment_id, order_id, currency)
    REFERENCES payments (tenant_id, id, order_id, currency) ON DELETE RESTRICT,
  CONSTRAINT late_payment_cases_event_fk FOREIGN KEY (tenant_id, payment_event_id)
    REFERENCES payment_events (tenant_id, id) ON DELETE SET NULL,
  CONSTRAINT late_payment_cases_suspense_txn_fk
    FOREIGN KEY (tenant_id, suspense_txn_id, currency)
    REFERENCES ledger_transactions (tenant_id, id, currency) ON DELETE RESTRICT,
  CONSTRAINT late_payment_cases_refund_fk
    FOREIGN KEY (tenant_id, refund_id, order_id, currency)
    REFERENCES refunds (tenant_id, id, order_id, currency) ON DELETE RESTRICT,
  CONSTRAINT late_payment_cases_refund_txn_fk
    FOREIGN KEY (tenant_id, refund_txn_id, currency)
    REFERENCES ledger_transactions (tenant_id, id, currency) ON DELETE RESTRICT,
  CONSTRAINT late_payment_cases_resolution CHECK (
    (status IN ('refunded', 'applied') AND resolved_at IS NOT NULL)
    OR status NOT IN ('refunded', 'applied')
  )
);
CREATE INDEX late_payment_cases_work_idx
  ON late_payment_cases (tenant_id, status, received_at)
  WHERE status IN ('suspense', 'refund_pending', 'manual_review');

CREATE TABLE order_reservation_events (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  reservation_id uuid NOT NULL,
  order_id uuid NOT NULL,
  from_state text,
  to_state text NOT NULL CHECK (to_state IN ('held', 'captured', 'released')),
  event_kind text NOT NULL
    CHECK (event_kind IN ('backfill', 'reserve', 'capture', 'release', 'expire', 'cancel')),
  business_request_id uuid NOT NULL,
  actor_kind text NOT NULL DEFAULT 'system'
    CHECK (actor_kind IN ('user', 'admin', 'system', 'reconciliation')),
  actor_id uuid,
  reason text,
  metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
  legacy_backfill boolean NOT NULL DEFAULT false,
  occurred_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT order_reservation_events_parent_fk
    FOREIGN KEY (tenant_id, reservation_id, order_id)
    REFERENCES order_reservations (tenant_id, id, order_id) ON DELETE RESTRICT
);
CREATE UNIQUE INDEX order_reservation_events_terminal_unique
  ON order_reservation_events (reservation_id)
  WHERE to_state IN ('captured', 'released');
CREATE UNIQUE INDEX order_reservation_events_initial_unique
  ON order_reservation_events (reservation_id)
  WHERE to_state = 'held';
CREATE INDEX order_reservation_events_order_idx
  ON order_reservation_events (tenant_id, order_id, occurred_at);

-- Backfill the parent lifecycle before its child resources.
INSERT INTO order_reservations
  (tenant_id, order_id, user_id, state, expires_at, held_at,
   captured_at, released_at, release_reason, legacy_backfill, created_at, updated_at)
SELECT o.tenant_id, o.id, o.user_id,
       CASE
         WHEN o.status IN ('draft', 'pending_payment', 'processing') THEN 'held'
         WHEN o.status IN ('paid', 'fulfilled', 'refunded', 'partially_refunded') THEN 'captured'
         ELSE 'released'
       END,
       coalesce(o.expires_at, o.created_at + interval '30 minutes'),
       o.created_at,
       CASE WHEN o.status IN ('paid', 'fulfilled', 'refunded', 'partially_refunded')
            THEN coalesce(o.fulfilled_at, o.paid_at, o.updated_at) END,
       CASE WHEN o.status IN ('cancelled', 'expired')
            THEN coalesce(o.cancelled_at, o.expired_at, o.updated_at) END,
       CASE WHEN o.status = 'expired' THEN 'legacy_expired'
            WHEN o.status = 'cancelled' THEN 'legacy_cancelled' END,
       true, o.created_at, o.updated_at
  FROM orders o;

INSERT INTO order_stock_reservations
  (tenant_id, reservation_id, order_id, plan_id, quantity, legacy_backfill, created_at)
SELECT o.tenant_id, r.id, o.id, oi.plan_id, oi.quantity, true, o.created_at
  FROM orders o
  JOIN order_reservations r
    ON r.tenant_id = o.tenant_id AND r.order_id = o.id
  JOIN order_items oi
    ON oi.tenant_id = o.tenant_id AND oi.order_id = o.id
WHERE o.kind = 'new' AND oi.plan_id IS NOT NULL;

INSERT INTO order_purchase_limit_reservations
  (tenant_id, reservation_id, order_id, plan_id, user_id, quantity,
   legacy_backfill, created_at)
SELECT o.tenant_id, r.id, o.id, oi.plan_id, o.user_id, oi.quantity,
       true, o.created_at
  FROM orders o
  JOIN order_reservations r
    ON r.tenant_id = o.tenant_id AND r.order_id = o.id
  JOIN order_items oi
    ON oi.tenant_id = o.tenant_id AND oi.order_id = o.id
  JOIN plan_purchase_counters pc
    ON pc.tenant_id = o.tenant_id AND pc.plan_id = oi.plan_id
   AND pc.user_id = o.user_id
 WHERE o.kind = 'new' AND oi.plan_id IS NOT NULL;

UPDATE plans p
   SET stock_sold = x.sold,
       stock_reserved = x.held
  FROM (
    SELECT sr.plan_id,
           coalesce(sum(sr.quantity) FILTER (WHERE r.state = 'captured'), 0)::int AS sold,
           coalesce(sum(sr.quantity) FILTER (WHERE r.state = 'held'), 0)::int AS held
      FROM order_stock_reservations sr
      JOIN order_reservations r ON r.id = sr.reservation_id
     GROUP BY sr.plan_id
  ) x
 WHERE p.id = x.plan_id;
UPDATE plans p SET stock_sold = 0, stock_reserved = 0
 WHERE NOT EXISTS (SELECT 1 FROM order_stock_reservations sr WHERE sr.plan_id = p.id);

UPDATE plan_purchase_counters pc
   SET purchased = x.captured,
       reserved = x.held,
       updated_at = now()
  FROM (
    SELECT pr.tenant_id, pr.plan_id, pr.user_id,
           coalesce(sum(pr.quantity) FILTER (WHERE r.state = 'captured'), 0)::int AS captured,
           coalesce(sum(pr.quantity) FILTER (WHERE r.state = 'held'), 0)::int AS held
      FROM order_purchase_limit_reservations pr
      JOIN order_reservations r ON r.id = pr.reservation_id
     GROUP BY pr.tenant_id, pr.plan_id, pr.user_id
  ) x
 WHERE pc.tenant_id = x.tenant_id AND pc.plan_id = x.plan_id
   AND pc.user_id = x.user_id;

UPDATE coupon_redemptions cr
   SET reservation_id = r.id,
       status = CASE
         WHEN cr.reverted_at IS NOT NULL THEN 'released'
         WHEN r.state = 'held' THEN 'held'
         WHEN r.state = 'captured' THEN 'captured'
         ELSE 'released'
       END,
       captured_at = CASE WHEN cr.reverted_at IS NULL AND r.state = 'captured'
                          THEN coalesce(o.fulfilled_at, o.paid_at, o.updated_at) END,
       released_at = CASE WHEN cr.reverted_at IS NOT NULL OR r.state = 'released'
                          THEN coalesce(cr.reverted_at, o.cancelled_at, o.expired_at, o.updated_at) END,
       legacy_backfill = true
  FROM orders o
  JOIN order_reservations r
    ON r.tenant_id = o.tenant_id AND r.order_id = o.id
 WHERE cr.tenant_id = o.tenant_id AND cr.order_id = o.id;

ALTER TABLE coupon_redemptions
  ALTER COLUMN reservation_id SET NOT NULL,
  ALTER COLUMN status SET NOT NULL,
  ALTER COLUMN status SET DEFAULT 'held',
  ADD CONSTRAINT coupon_redemptions_parent_fk
    FOREIGN KEY (tenant_id, reservation_id, order_id, user_id)
    REFERENCES order_reservations (tenant_id, id, order_id, user_id) ON DELETE RESTRICT,
  ADD CONSTRAINT coupon_redemptions_coupon_tenant_fk
    FOREIGN KEY (tenant_id, coupon_id)
    REFERENCES coupons (tenant_id, id) ON DELETE RESTRICT,
  ADD CONSTRAINT coupon_redemptions_status_check
    CHECK (status IN ('held', 'captured', 'released')),
  ADD CONSTRAINT coupon_redemptions_state_times CHECK (
    (status = 'held' AND captured_at IS NULL AND released_at IS NULL)
    OR (status = 'captured' AND captured_at IS NOT NULL AND released_at IS NULL)
    OR (status = 'released' AND released_at IS NOT NULL AND captured_at IS NULL)
  );
CREATE INDEX coupon_redemptions_user_active_idx
  ON coupon_redemptions (tenant_id, coupon_id, user_id)
  WHERE status IN ('held', 'captured');

UPDATE coupons c
   SET redeemed_count = x.captured,
       reserved_count = x.held,
       updated_at = now()
  FROM (
    SELECT coupon_id,
           count(*) FILTER (WHERE status = 'captured')::int AS captured,
           count(*) FILTER (WHERE status = 'held')::int AS held
      FROM coupon_redemptions GROUP BY coupon_id
  ) x
 WHERE c.id = x.coupon_id;
UPDATE coupons c SET redeemed_count = 0, reserved_count = 0, updated_at = now()
 WHERE NOT EXISTS (SELECT 1 FROM coupon_redemptions cr WHERE cr.coupon_id = c.id);

-- Captured/released legacy orders are evidence-only rows: old code already
-- posted captured balance directly, while released orders never debited it.
INSERT INTO balance_holds
  (tenant_id, reservation_id, order_id, user_id, currency, amount,
   available_account_id, status, captured_at, released_at, legacy_backfill,
   held_at, created_at, updated_at)
SELECT o.tenant_id, r.id, o.id, o.user_id, o.currency, o.balance_applied,
       a.id, r.state, r.captured_at, r.released_at, true,
       r.held_at, o.created_at, o.updated_at
  FROM orders o
  JOIN order_reservations r
    ON r.tenant_id = o.tenant_id AND r.order_id = o.id
  LEFT JOIN ledger_accounts a
    ON a.tenant_id=o.tenant_id AND a.owner_user_id=o.user_id
   AND a.currency=o.currency AND a.account_type='user_balance'
 WHERE o.balance_applied > 0 AND r.state <> 'held';

-- Open legacy balance orders receive real double-entry holds. This is the only
-- safe way to prevent them from double-spending immediately after migration.
-- +goose StatementBegin
DO $$
DECLARE
  x record;
  v_available uuid;
  v_hold uuid;
  v_txn uuid;
  v_created boolean := false;
BEGIN
  FOR x IN
    SELECT o.*, r.id AS reservation_id
      FROM orders o
      JOIN order_reservations r
        ON r.tenant_id = o.tenant_id AND r.order_id = o.id
     WHERE o.balance_applied > 0 AND r.state = 'held'
     ORDER BY o.tenant_id, o.user_id, o.currency, o.created_at, o.id
  LOOP
    SELECT id INTO v_available
      FROM ledger_accounts
     WHERE tenant_id = x.tenant_id AND owner_user_id = x.user_id
       AND currency = x.currency AND account_type = 'user_balance'
     FOR UPDATE;

    INSERT INTO ledger_accounts
      (tenant_id, account_type, normal_balance, currency, owner_user_id)
    VALUES (x.tenant_id, 'user_balance_hold', 'credit', x.currency, x.user_id)
    ON CONFLICT (tenant_id, account_type, owner_user_id, currency)
      WHERE owner_user_id IS NOT NULL
    DO UPDATE SET updated_at = ledger_accounts.updated_at
    RETURNING id INTO v_hold;

    INSERT INTO ledger_transactions
      (tenant_id, kind, currency, source_type, source_id, memo, actor_kind)
    VALUES (x.tenant_id, 'balance_hold', x.currency, 'order', x.id,
            '00036 legacy open-order balance hold', 'system')
    RETURNING id INTO v_txn;

    INSERT INTO ledger_entries
      (tenant_id, transaction_id, account_id, direction, amount, currency, description)
    VALUES
      (x.tenant_id, v_txn, v_available, 'debit', x.balance_applied, x.currency,
       'order balance reserved'),
      (x.tenant_id, v_txn, v_hold, 'credit', x.balance_applied, x.currency,
       'order balance hold liability');

    INSERT INTO balance_holds
      (tenant_id, reservation_id, order_id, user_id, currency, amount,
       available_account_id, hold_account_id, status, hold_txn_id,
       legacy_backfill, held_at, created_at, updated_at)
    VALUES
      (x.tenant_id, x.reservation_id, x.id, x.user_id, x.currency,
       x.balance_applied, v_available, v_hold, 'held', v_txn,
       true, x.created_at, x.created_at, x.updated_at);
    v_created := true;
  END LOOP;

  IF v_created THEN
    UPDATE app.order_reservations_00036_meta
       SET created_balance_postings = true;
  END IF;
END;
$$;
-- +goose StatementEnd

INSERT INTO order_reservation_events
  (tenant_id, reservation_id, order_id, from_state, to_state, event_kind,
   business_request_id, actor_kind, reason, legacy_backfill, occurred_at)
SELECT r.tenant_id, r.id, r.order_id, NULL, 'held', 'backfill',
       o.business_request_id, 'system', 'schema_00036_backfill', true, r.created_at
  FROM order_reservations r
  JOIN orders o ON o.tenant_id = r.tenant_id AND o.id = r.order_id;

INSERT INTO order_reservation_events
  (tenant_id, reservation_id, order_id, from_state, to_state, event_kind,
   business_request_id, actor_kind, reason, legacy_backfill, occurred_at)
SELECT r.tenant_id, r.id, r.order_id, 'held', r.state, 'backfill',
       o.business_request_id, 'system', 'schema_00036_terminal_backfill', true,
       coalesce(r.captured_at,r.released_at,r.updated_at)
  FROM order_reservations r
  JOIN orders o ON o.tenant_id = r.tenant_id AND o.id = r.order_id
 WHERE r.state IN ('captured','released');

-- Post-backfill snapshots are the rollback watermark.  Down is allowed only
-- while the schema is still byte-for-byte at this logical watermark.
CREATE TABLE app.order_reservations_00036_post_plans AS
  SELECT tenant_id, id AS plan_id, stock_reserved, stock_sold FROM plans;
ALTER TABLE app.order_reservations_00036_post_plans ADD PRIMARY KEY (plan_id);
CREATE TABLE app.order_reservations_00036_post_purchase AS
  SELECT tenant_id, plan_id, user_id, purchased, reserved
    FROM plan_purchase_counters;
ALTER TABLE app.order_reservations_00036_post_purchase
  ADD PRIMARY KEY (tenant_id, plan_id, user_id);
CREATE TABLE app.order_reservations_00036_post_coupons AS
  SELECT tenant_id, id AS coupon_id, redeemed_count, reserved_count FROM coupons;
ALTER TABLE app.order_reservations_00036_post_coupons ADD PRIMARY KEY (coupon_id);
CREATE TABLE app.order_reservations_00036_post_orders AS
  SELECT tenant_id, id AS order_id, status, updated_at, cancelled_at, expired_at,
         cancel_reason, state_version, idempotency_key_id
    FROM orders;
ALTER TABLE app.order_reservations_00036_post_orders ADD PRIMARY KEY (order_id);

-- +goose StatementBegin
CREATE FUNCTION app.guard_order_reservation_transition() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF (to_jsonb(NEW) - ARRAY['state','state_version','captured_at','released_at',
                            'release_reason','legacy_backfill','updated_at'])
     IS DISTINCT FROM
     (to_jsonb(OLD) - ARRAY['state','state_version','captured_at','released_at',
                            'release_reason','legacy_backfill','updated_at']) THEN
    RAISE EXCEPTION 'reservation identity, amount and expiry fields are immutable'
      USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.state IS DISTINCT FROM OLD.state THEN
    IF OLD.state <> 'held' OR NEW.state NOT IN ('captured', 'released') THEN
      RAISE EXCEPTION 'illegal reservation transition % -> % for reservation %',
        OLD.state, NEW.state, OLD.id USING ERRCODE = 'check_violation';
    END IF;
    NEW.state_version := OLD.state_version + 1;
    NEW.legacy_backfill := false;
    NEW.updated_at := clock_timestamp();
  ELSIF NEW.state_version IS DISTINCT FROM OLD.state_version
     OR NEW.legacy_backfill IS DISTINCT FROM OLD.legacy_backfill
     OR NEW.captured_at IS DISTINCT FROM OLD.captured_at
     OR NEW.released_at IS DISTINCT FROM OLD.released_at
     OR NEW.release_reason IS DISTINCT FROM OLD.release_reason THEN
    RAISE EXCEPTION 'reservation lifecycle metadata is guard-maintained'
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END;
$$;

CREATE FUNCTION app.guard_coupon_reservation_transition() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF (to_jsonb(NEW) - ARRAY['status','captured_at','released_at','reverted_at',
                            'revert_reason','legacy_backfill'])
     IS DISTINCT FROM
     (to_jsonb(OLD) - ARRAY['status','captured_at','released_at','reverted_at',
                            'revert_reason','legacy_backfill']) THEN
    RAISE EXCEPTION 'coupon reservation identity and discount are immutable'
      USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.status IS DISTINCT FROM OLD.status THEN
    IF OLD.status <> 'held' OR NEW.status NOT IN ('captured', 'released') THEN
      RAISE EXCEPTION 'illegal coupon reservation transition % -> %',
        OLD.status, NEW.status USING ERRCODE = 'check_violation';
    END IF;
    NEW.legacy_backfill := false;
  ELSIF NEW.legacy_backfill IS DISTINCT FROM OLD.legacy_backfill
     OR NEW.captured_at IS DISTINCT FROM OLD.captured_at
     OR NEW.released_at IS DISTINCT FROM OLD.released_at
     OR NEW.reverted_at IS DISTINCT FROM OLD.reverted_at
     OR NEW.revert_reason IS DISTINCT FROM OLD.revert_reason THEN
    RAISE EXCEPTION 'coupon legacy marker is guard-maintained'
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END;
$$;

CREATE FUNCTION app.guard_balance_hold_transition() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF (to_jsonb(NEW) - ARRAY['status','capture_txn_id','release_txn_id',
                            'captured_at','released_at','legacy_backfill','updated_at'])
     IS DISTINCT FROM
     (to_jsonb(OLD) - ARRAY['status','capture_txn_id','release_txn_id',
                            'captured_at','released_at','legacy_backfill','updated_at']) THEN
    RAISE EXCEPTION 'balance hold identity, amount and hold posting are immutable'
      USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.status IS DISTINCT FROM OLD.status THEN
    IF OLD.status <> 'held' OR NEW.status NOT IN ('captured', 'released') THEN
      RAISE EXCEPTION 'illegal balance hold transition % -> %',
        OLD.status, NEW.status USING ERRCODE = 'check_violation';
    END IF;
    NEW.legacy_backfill := false;
    NEW.updated_at := clock_timestamp();
  ELSIF NEW.legacy_backfill IS DISTINCT FROM OLD.legacy_backfill
     OR NEW.capture_txn_id IS DISTINCT FROM OLD.capture_txn_id
     OR NEW.release_txn_id IS DISTINCT FROM OLD.release_txn_id
     OR NEW.captured_at IS DISTINCT FROM OLD.captured_at
     OR NEW.released_at IS DISTINCT FROM OLD.released_at THEN
    RAISE EXCEPTION 'balance hold legacy marker is guard-maintained'
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END;
$$;

CREATE FUNCTION app.guard_late_payment_case() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
  v_event_provider uuid;
  v_event_payment text;
  v_payment_provider uuid;
  v_payment_ref text;
BEGIN
  IF TG_OP = 'UPDATE' AND
     (to_jsonb(NEW) - ARRAY['status','refund_id','refund_txn_id','resolution_reason',
                            'resolved_at','updated_at'])
     IS DISTINCT FROM
     (to_jsonb(OLD) - ARRAY['status','refund_id','refund_txn_id','resolution_reason',
                            'resolved_at','updated_at']) THEN
    RAISE EXCEPTION 'late payment evidence identity and amount are immutable'
      USING ERRCODE = 'check_violation';
  END IF;
  IF TG_OP = 'UPDATE' AND NEW.status IS DISTINCT FROM OLD.status AND NOT (
       (OLD.status='suspense' AND NEW.status IN ('refund_pending','manual_review','applied'))
    OR (OLD.status='refund_pending' AND NEW.status IN ('refunded','manual_review'))
    OR (OLD.status='manual_review' AND NEW.status IN ('refund_pending','refunded','applied'))
  ) THEN
    RAISE EXCEPTION 'illegal late payment transition % -> %', OLD.status, NEW.status
      USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.status='refunded' AND
     (NEW.refund_id IS NULL OR NEW.refund_txn_id IS NULL OR NEW.resolved_at IS NULL) THEN
    RAISE EXCEPTION 'refunded late payment requires refund evidence and ledger transaction'
      USING ERRCODE = 'check_violation';
  END IF;

  IF NEW.payment_event_id IS NOT NULL THEN
    SELECT pe.provider_id, pe.provider_payment_id
      INTO v_event_provider, v_event_payment
      FROM payment_events pe
     WHERE pe.tenant_id = NEW.tenant_id AND pe.id = NEW.payment_event_id;
    SELECT p.provider_id, p.provider_payment_id
      INTO v_payment_provider, v_payment_ref
      FROM payments p
     WHERE p.tenant_id = NEW.tenant_id AND p.id = NEW.payment_id;
    IF v_event_provider IS DISTINCT FROM v_payment_provider
       OR v_event_payment IS DISTINCT FROM v_payment_ref THEN
      RAISE EXCEPTION 'late payment event does not identify the referenced payment'
        USING ERRCODE = 'foreign_key_violation';
    END IF;
  END IF;
  RETURN NEW;
END;
$$;

CREATE FUNCTION app.guard_payment_webhook_receipt() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF (to_jsonb(NEW) - ARRAY['parse_status','signature_status','parse_error',
                            'parsed_at','signature_checked_at',
                            'parsed_provider_event_id','payment_event_id'])
     IS DISTINCT FROM
     (to_jsonb(OLD) - ARRAY['parse_status','signature_status','parse_error',
                            'parsed_at','signature_checked_at',
                            'parsed_provider_event_id','payment_event_id']) THEN
    RAISE EXCEPTION 'webhook raw evidence and request identity are immutable'
      USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.parse_status IS DISTINCT FROM OLD.parse_status AND NOT
     (OLD.parse_status='unattempted' AND NEW.parse_status IN ('parsed','malformed')) THEN
    RAISE EXCEPTION 'illegal webhook parse status transition % -> %',
      OLD.parse_status, NEW.parse_status USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.parse_status IS NOT DISTINCT FROM OLD.parse_status AND
     (NEW.parse_error IS DISTINCT FROM OLD.parse_error
      OR NEW.parsed_at IS DISTINCT FROM OLD.parsed_at
      OR NEW.parsed_provider_event_id IS DISTINCT FROM OLD.parsed_provider_event_id) THEN
    RAISE EXCEPTION 'webhook parse assessment is immutable after its single transition'
      USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.signature_status IS DISTINCT FROM OLD.signature_status AND NOT
     (OLD.signature_status='unattempted' AND
      NEW.signature_status IN ('verified','rejected','unavailable')) THEN
    RAISE EXCEPTION 'illegal webhook signature status transition % -> %',
      OLD.signature_status, NEW.signature_status USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.signature_status IS NOT DISTINCT FROM OLD.signature_status AND
     NEW.signature_checked_at IS DISTINCT FROM OLD.signature_checked_at THEN
    RAISE EXCEPTION 'webhook signature assessment is immutable after its single transition'
      USING ERRCODE = 'check_violation';
  END IF;
  IF OLD.payment_event_id IS NOT NULL AND
     NEW.payment_event_id IS DISTINCT FROM OLD.payment_event_id THEN
    RAISE EXCEPTION 'webhook normalized event link is write-once'
      USING ERRCODE = 'check_violation';
  END IF;
  IF OLD.payment_event_id IS NULL AND NEW.payment_event_id IS NOT NULL AND
     (NEW.parse_status<>'parsed' OR NEW.signature_status<>'verified') THEN
    RAISE EXCEPTION 'webhook event link requires parsed and verified assessment'
      USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.payment_event_id IS NOT NULL AND EXISTS (
    SELECT 1 FROM payment_events pe
     WHERE pe.tenant_id=NEW.tenant_id AND pe.id=NEW.payment_event_id
       AND ((NEW.provider_id IS NOT NULL AND pe.provider_id<>NEW.provider_id)
         OR (NEW.parsed_provider_event_id IS NOT NULL
             AND pe.provider_event_id<>NEW.parsed_provider_event_id))
  ) THEN
    RAISE EXCEPTION 'normalized payment event does not match receipt assessment'
      USING ERRCODE = 'foreign_key_violation';
  END IF;
  RETURN NEW;
END;
$$;

CREATE FUNCTION app.guard_payment_intent() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF (to_jsonb(NEW)-ARRAY['status','provider_ref','action_payload','failure_code',
                           'failure_message','expires_at','updated_at']) IS DISTINCT FROM
     (to_jsonb(OLD)-ARRAY['status','provider_ref','action_payload','failure_code',
                           'failure_message','expires_at','updated_at']) THEN
    RAISE EXCEPTION 'payment intent identity, order, provider, currency and amount are immutable'
      USING ERRCODE='check_violation';
  END IF;
  IF NEW.status IS DISTINCT FROM OLD.status AND NOT (
       (OLD.status='created' AND NEW.status IN ('requires_action','processing','succeeded','failed','cancelled','expired'))
    OR (OLD.status='requires_action' AND NEW.status IN ('processing','succeeded','failed','cancelled','expired'))
    OR (OLD.status='processing' AND NEW.status IN ('succeeded','failed'))
  ) THEN
    RAISE EXCEPTION 'illegal payment intent transition % -> %',OLD.status,NEW.status
      USING ERRCODE='check_violation';
  END IF;
  IF OLD.provider_ref IS NOT NULL AND NEW.provider_ref IS DISTINCT FROM OLD.provider_ref THEN
    RAISE EXCEPTION 'payment intent provider reference is write-once'
      USING ERRCODE='check_violation';
  END IF;
  RETURN NEW;
END;
$$;

CREATE FUNCTION app.guard_payment_evidence() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF (to_jsonb(NEW)-ARRAY['status','refunded_amount','updated_at']) IS DISTINCT FROM
     (to_jsonb(OLD)-ARRAY['status','refunded_amount','updated_at']) THEN
    RAISE EXCEPTION 'payment order/provider/currency/amount/fee/method evidence is immutable'
      USING ERRCODE='check_violation';
  END IF;
  IF NEW.refunded_amount<OLD.refunded_amount THEN
    RAISE EXCEPTION 'payment refunded amount is monotonic' USING ERRCODE='check_violation';
  END IF;
  IF NEW.refunded_amount IS DISTINCT FROM OLD.refunded_amount AND NOT
     ((NEW.status='partially_refunded' AND NEW.refunded_amount>0
       AND NEW.refunded_amount<NEW.amount)
       OR (NEW.status='refunded' AND NEW.refunded_amount=NEW.amount)) THEN
    RAISE EXCEPTION 'payment refunded amount must match partial/full refund status'
      USING ERRCODE='check_violation';
  END IF;
  IF NEW.status IS DISTINCT FROM OLD.status AND NOT (
       (OLD.status='succeeded' AND NEW.status IN ('partially_refunded','refunded','disputed','reversed'))
    OR (OLD.status='partially_refunded' AND NEW.status IN ('refunded','disputed','reversed'))
    OR (OLD.status='disputed' AND NEW.status IN ('reversed','refunded'))
  ) THEN
    RAISE EXCEPTION 'illegal payment transition % -> %',OLD.status,NEW.status
      USING ERRCODE='check_violation';
  END IF;
  IF (NEW.status='refunded' AND NEW.refunded_amount<>NEW.amount)
     OR (NEW.status='partially_refunded' AND
         (NEW.refunded_amount<=0 OR NEW.refunded_amount>=NEW.amount)) THEN
    RAISE EXCEPTION 'payment refund status/amount mismatch' USING ERRCODE='check_violation';
  END IF;
  RETURN NEW;
END;
$$;

CREATE FUNCTION app.guard_refund_evidence() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF OLD.status IN ('succeeded','rejected') AND to_jsonb(NEW) IS DISTINCT FROM to_jsonb(OLD) THEN
    RAISE EXCEPTION 'terminal refund evidence is immutable'
      USING ERRCODE='check_violation';
  END IF;
  IF (to_jsonb(NEW)-ARRAY['status','provider_refund_id','approval_request_id',
                           'entitlement_revoked','commission_reversed','failure_message',
                           'ledger_txn_id','succeeded_at','updated_at']) IS DISTINCT FROM
     (to_jsonb(OLD)-ARRAY['status','provider_refund_id','approval_request_id',
                           'entitlement_revoked','commission_reversed','failure_message',
                           'ledger_txn_id','succeeded_at','updated_at']) THEN
    RAISE EXCEPTION 'refund source/order/currency/amount/reason/requester are immutable'
      USING ERRCODE='check_violation';
  END IF;
  IF NEW.status IS DISTINCT FROM OLD.status AND NOT (
       (OLD.status='pending' AND NEW.status IN ('approved','processing','rejected'))
    OR (OLD.status='approved' AND NEW.status IN ('processing','rejected'))
    OR (OLD.status='processing' AND NEW.status IN ('succeeded','failed'))
    OR (OLD.status='failed' AND NEW.status='processing')
  ) THEN
    RAISE EXCEPTION 'illegal refund transition % -> %',OLD.status,NEW.status
      USING ERRCODE='check_violation';
  END IF;
  IF NEW.status='succeeded' AND NEW.succeeded_at IS NULL THEN
    RAISE EXCEPTION 'successful refund requires succeeded_at' USING ERRCODE='check_violation';
  END IF;
  IF OLD.provider_refund_id IS NOT NULL
     AND NEW.provider_refund_id IS DISTINCT FROM OLD.provider_refund_id THEN
    RAISE EXCEPTION 'provider refund reference is write-once'
      USING ERRCODE='check_violation';
  END IF;
  IF OLD.ledger_txn_id IS NOT NULL
     AND NEW.ledger_txn_id IS DISTINCT FROM OLD.ledger_txn_id THEN
    RAISE EXCEPTION 'refund ledger link is write-once'
      USING ERRCODE='check_violation';
  END IF;
  RETURN NEW;
END;
$$;

CREATE FUNCTION app.guard_idempotency_evidence() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP='INSERT' THEN
    IF NEW.status<>'in_flight' OR NEW.resource_type IS NOT NULL
       OR NEW.resource_id IS NOT NULL THEN
      RAISE EXCEPTION 'idempotency claims must start unbound and in_flight'
        USING ERRCODE='check_violation';
    END IF;
    RETURN NEW;
  END IF;

  IF OLD.status='succeeded' AND to_jsonb(NEW) IS DISTINCT FROM to_jsonb(OLD) THEN
    RAISE EXCEPTION 'successful idempotency evidence is immutable'
      USING ERRCODE='check_violation';
  END IF;
  IF OLD.status='failed' AND NEW.status='failed'
     AND to_jsonb(NEW) IS DISTINCT FROM to_jsonb(OLD) THEN
    RAISE EXCEPTION 'failed idempotency evidence changes only through controlled retry'
      USING ERRCODE='check_violation';
  END IF;
  IF (NEW.id,NEW.tenant_id,NEW.scope,NEW.idempotency_key,NEW.request_hash,
      NEW.actor_id,NEW.created_at,NEW.expires_at) IS DISTINCT FROM
     (OLD.id,OLD.tenant_id,OLD.scope,OLD.idempotency_key,OLD.request_hash,
      OLD.actor_id,OLD.created_at,OLD.expires_at) THEN
    RAISE EXCEPTION 'idempotency tenant/scope/key/hash/actor/time identity is immutable'
      USING ERRCODE='check_violation';
  END IF;
  IF OLD.resource_type IS NOT NULL AND
     (NEW.resource_type,NEW.resource_id) IS DISTINCT FROM
     (OLD.resource_type,OLD.resource_id) THEN
    RAISE EXCEPTION 'idempotency resource binding is write-once'
      USING ERRCODE='check_violation';
  END IF;
  IF NEW.status IS DISTINCT FROM OLD.status AND NOT (
       (OLD.status='in_flight' AND NEW.status IN ('succeeded','failed'))
    OR (OLD.status='failed' AND NEW.status='in_flight')
  ) THEN
    RAISE EXCEPTION 'illegal idempotency transition % -> %',OLD.status,NEW.status
      USING ERRCODE='check_violation';
  END IF;
  RETURN NEW;
END;
$$;

CREATE FUNCTION app.guard_refund_request() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP='UPDATE' AND
     (to_jsonb(NEW)-ARRAY['status','approval_request_id','updated_at']) IS DISTINCT FROM
     (to_jsonb(OLD)-ARRAY['status','approval_request_id','updated_at']) THEN
    RAISE EXCEPTION 'refund request identity, amount, actor and business key are immutable'
      USING ERRCODE='check_violation';
  END IF;
  IF TG_OP='UPDATE' AND OLD.status='succeeded'
     AND to_jsonb(NEW) IS DISTINCT FROM to_jsonb(OLD) THEN
    RAISE EXCEPTION 'terminal refund request is immutable'
      USING ERRCODE='check_violation';
  END IF;
  IF TG_OP='UPDATE' AND NEW.status IS DISTINCT FROM OLD.status AND NOT (
       (OLD.status='pending' AND NEW.status IN ('processing','failed','manual_review'))
    OR (OLD.status='processing' AND NEW.status IN
          ('succeeded','failed','manual_review','partially_succeeded'))
    OR (OLD.status='failed' AND NEW.status='processing')
    OR (OLD.status IN ('manual_review','partially_succeeded') AND
        NEW.status IN ('processing','succeeded','failed','partially_succeeded','manual_review'))
  ) THEN
    RAISE EXCEPTION 'illegal refund request transition % -> %',OLD.status,NEW.status
      USING ERRCODE='check_violation';
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM idempotency_keys k
     WHERE k.tenant_id=NEW.tenant_id AND k.id=NEW.business_request_id
       AND k.scope='refund_create' AND k.resource_type='refund_request'
       AND k.resource_id=NEW.id
       AND k.status IN ('in_flight','succeeded','failed')
       AND k.request_hash=NEW.request_hash
       AND k.actor_id IS NOT DISTINCT FROM NEW.requested_by
  ) THEN
    RAISE EXCEPTION 'refund request requires a bound refund_create idempotency record'
      USING ERRCODE='foreign_key_violation';
  END IF;
  RETURN NEW;
END;
$$;

CREATE FUNCTION app.guard_refund_source() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
  v_source_amount bigint;
  v_order_paid bigint;
  v_order_payable bigint;
  v_order_balance bigint;
  v_active bigint;
  v_order_active bigint;
  v_debits bigint;
  v_credits bigint;
  v_entries int;
  v_provider_code text;
  v_balance_account uuid;
BEGIN
  IF TG_OP='INSERT' AND NEW.status<>'pending' THEN
    RAISE EXCEPTION 'refunds must be inserted in pending state'
      USING ERRCODE='check_violation';
  END IF;

  -- One fixed lock root serializes payment-only and mixed-source requests for
  -- the same order; source rows are then locked after the order.
  SELECT paid_amount,payable_amount,balance_applied
    INTO v_order_paid,v_order_payable,v_order_balance
    FROM orders
   WHERE tenant_id=NEW.tenant_id AND id=NEW.order_id AND currency=NEW.currency
   FOR UPDATE;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'refund order tenant/currency identity is invalid'
      USING ERRCODE='foreign_key_violation';
  END IF;

  IF NEW.source_kind='payment' THEN
    SELECT p.amount,pp.code
      INTO v_source_amount,v_provider_code
      FROM payments p JOIN payment_providers pp
        ON pp.tenant_id=p.tenant_id AND pp.id=p.provider_id
     WHERE p.tenant_id=NEW.tenant_id AND p.id=NEW.payment_id
       AND p.order_id=NEW.order_id AND p.currency=NEW.currency
       AND p.status IN ('succeeded','partially_refunded','refunded')
     FOR UPDATE OF p;
    IF NOT FOUND OR v_order_payable<=0 OR v_source_amount>v_order_payable THEN
      RAISE EXCEPTION 'payment refund source is not real funding for this order'
        USING ERRCODE='foreign_key_violation';
    END IF;
  ELSIF NEW.source_kind='balance' THEN
    SELECT h.amount,h.available_account_id
      INTO v_source_amount,v_balance_account
      FROM balance_holds h
     WHERE h.tenant_id=NEW.tenant_id AND h.id=NEW.balance_hold_id
       AND h.order_id=NEW.order_id AND h.currency=NEW.currency
       AND h.status='captured'
     FOR UPDATE OF h;
    IF NOT FOUND OR v_order_balance<=0 OR v_source_amount<>v_order_balance
       OR v_balance_account IS NULL THEN
      RAISE EXCEPTION 'balance refund source is not captured funding for this order'
        USING ERRCODE='foreign_key_violation';
    END IF;
    IF NEW.provider_refund_id IS NOT NULL THEN
      RAISE EXCEPTION 'balance refunds cannot carry a provider refund reference'
        USING ERRCODE='check_violation';
    END IF;
  ELSE
    RAISE EXCEPTION 'unknown refund source kind %',NEW.source_kind
      USING ERRCODE='check_violation';
  END IF;

  SELECT coalesce(sum(r.amount),0) INTO v_active
    FROM refunds r
   WHERE r.tenant_id=NEW.tenant_id AND r.id<>NEW.id
     AND r.source_kind=NEW.source_kind
     AND ((NEW.source_kind='payment' AND r.payment_id=NEW.payment_id)
       OR (NEW.source_kind='balance' AND r.balance_hold_id=NEW.balance_hold_id))
     AND r.status NOT IN ('failed','rejected');
  IF NEW.status NOT IN ('failed','rejected') THEN v_active:=v_active+NEW.amount; END IF;
  IF v_active>v_source_amount THEN
    RAISE EXCEPTION 'active refunds % exceed % source funding %',
      v_active,NEW.source_kind,v_source_amount USING ERRCODE='check_violation';
  END IF;

  SELECT coalesce(sum(r.amount),0) INTO v_order_active
    FROM refunds r
   WHERE r.tenant_id=NEW.tenant_id AND r.order_id=NEW.order_id AND r.id<>NEW.id
     AND r.status NOT IN ('failed','rejected');
  IF NEW.status NOT IN ('failed','rejected') THEN
    v_order_active:=v_order_active+NEW.amount;
  END IF;
  IF v_order_active>v_order_paid THEN
    RAISE EXCEPTION 'active mixed-source refunds % exceed order paid amount %',
      v_order_active,v_order_paid USING ERRCODE='check_violation';
  END IF;

  IF NEW.status='succeeded' THEN
    IF NEW.ledger_txn_id IS NULL OR NEW.succeeded_at IS NULL
       OR (NEW.source_kind='payment' AND
           nullif(btrim(NEW.provider_refund_id),'') IS NULL) THEN
      RAISE EXCEPTION 'successful refund requires source-specific and ledger evidence'
        USING ERRCODE='check_violation';
    END IF;
    -- Serialize finalization against ledger entry INSERT.  The matching entry
    -- guard takes the same transaction-row lock, so no balanced pair can race
    -- into the transaction after the exact-two-entry proof below.
    PERFORM 1 FROM ledger_transactions
     WHERE tenant_id=NEW.tenant_id AND id=NEW.ledger_txn_id
     FOR UPDATE;
    SELECT count(e.id),
           coalesce(sum(e.amount) FILTER (WHERE e.direction='debit'),0),
           coalesce(sum(e.amount) FILTER (WHERE e.direction='credit'),0)
      INTO v_entries,v_debits,v_credits
      FROM ledger_transactions t LEFT JOIN ledger_entries e ON e.transaction_id=t.id
     WHERE t.tenant_id=NEW.tenant_id AND t.id=NEW.ledger_txn_id
       AND t.currency=NEW.currency AND t.kind='refund_issued'
       AND t.source_type='refund' AND t.source_id=NEW.id
     GROUP BY t.id;
    IF NOT FOUND OR v_entries<>2 OR v_debits<>NEW.amount OR v_credits<>NEW.amount
       OR NOT EXISTS (
         SELECT 1 FROM ledger_entries e JOIN ledger_accounts a ON a.id=e.account_id
          WHERE e.transaction_id=NEW.ledger_txn_id AND e.tenant_id=NEW.tenant_id
            AND e.currency=NEW.currency AND e.direction='debit' AND e.amount=NEW.amount
            AND a.tenant_id=NEW.tenant_id AND a.currency=NEW.currency
            AND a.account_type='platform_revenue' AND a.owner_user_id IS NULL
            AND a.owner_ref='main'
       )
       OR (NEW.source_kind='payment' AND NOT EXISTS (
         SELECT 1 FROM ledger_entries e JOIN ledger_accounts a ON a.id=e.account_id
          WHERE e.transaction_id=NEW.ledger_txn_id AND e.tenant_id=NEW.tenant_id
            AND e.currency=NEW.currency AND e.direction='credit' AND e.amount=NEW.amount
            AND a.tenant_id=NEW.tenant_id AND a.currency=NEW.currency
            AND a.account_type='channel_cash' AND a.owner_user_id IS NULL
            AND a.owner_ref=v_provider_code
       ))
       OR (NEW.source_kind='balance' AND NOT EXISTS (
         SELECT 1 FROM ledger_entries e JOIN ledger_accounts a ON a.id=e.account_id
          WHERE e.transaction_id=NEW.ledger_txn_id AND e.tenant_id=NEW.tenant_id
            AND e.currency=NEW.currency AND e.direction='credit' AND e.amount=NEW.amount
            AND e.account_id=v_balance_account
            AND a.tenant_id=NEW.tenant_id AND a.currency=NEW.currency
            AND a.account_type='user_balance'
       )) THEN
      RAISE EXCEPTION 'successful refund ledger transaction is missing or amount-mismatched'
        USING ERRCODE='check_violation';
    END IF;
  ELSIF NEW.ledger_txn_id IS NOT NULL OR NEW.succeeded_at IS NOT NULL THEN
    RAISE EXCEPTION 'non-successful refund cannot carry success ledger evidence'
      USING ERRCODE='check_violation';
  END IF;
  RETURN NEW;
END;
$$;

CREATE FUNCTION app.guard_finalized_refund_ledger_entry() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  PERFORM 1 FROM ledger_transactions
   WHERE tenant_id=NEW.tenant_id AND id=NEW.transaction_id
   FOR UPDATE;
  IF EXISTS (
    SELECT 1 FROM refunds r
     WHERE r.tenant_id=NEW.tenant_id
       AND r.ledger_txn_id=NEW.transaction_id
       AND r.status='succeeded'
  ) THEN
    RAISE EXCEPTION 'successful refund ledger transaction is finalized'
      USING ERRCODE='check_violation';
  END IF;
  RETURN NEW;
END;
$$;
ALTER FUNCTION app.guard_finalized_refund_ledger_entry() SECURITY DEFINER;
ALTER FUNCTION app.guard_finalized_refund_ledger_entry()
  SET search_path = pg_catalog, public;
REVOKE EXECUTE ON FUNCTION app.guard_finalized_refund_ledger_entry()
  FROM PUBLIC, aegis_app;

CREATE FUNCTION app.assert_refund_consistency(
  p_tenant uuid,p_order uuid,p_payment uuid
) RETURNS void
LANGUAGE plpgsql AS $$
DECLARE
  v_detail text;
BEGIN
  IF p_order IS NOT NULL THEN
    SELECT format('refund %s has invalid %s source or success evidence',r.id,r.source_kind)
      INTO v_detail
      FROM refunds r
      JOIN orders o
        ON o.tenant_id=r.tenant_id AND o.id=r.order_id AND o.currency=r.currency
      LEFT JOIN payments p
        ON p.tenant_id=r.tenant_id AND p.id=r.payment_id
       AND p.order_id=r.order_id AND p.currency=r.currency
      LEFT JOIN balance_holds h
        ON h.tenant_id=r.tenant_id AND h.id=r.balance_hold_id
       AND h.order_id=r.order_id AND h.currency=r.currency
     WHERE r.tenant_id=p_tenant AND r.order_id=p_order AND (
          (r.status<>'succeeded' AND
           (r.succeeded_at IS NOT NULL OR r.ledger_txn_id IS NOT NULL))
       OR (r.status='succeeded' AND
           (r.succeeded_at IS NULL OR r.ledger_txn_id IS NULL
            OR (r.source_kind='payment' AND
                nullif(btrim(r.provider_refund_id),'') IS NULL)))
       OR (r.source_kind='payment' AND
           (p.id IS NULL OR p.status NOT IN ('succeeded','partially_refunded','refunded')
            OR o.payable_amount<=0 OR p.amount>o.payable_amount
            OR (SELECT coalesce(sum(rp.amount),0) FROM refunds rp
                 WHERE rp.tenant_id=r.tenant_id AND rp.source_kind='payment'
                   AND rp.payment_id=r.payment_id
                   AND rp.status NOT IN ('failed','rejected'))>p.amount))
       OR (r.source_kind='balance' AND
           (h.id IS NULL OR h.status<>'captured' OR o.balance_applied<=0
            OR h.amount<>o.balance_applied OR h.available_account_id IS NULL
            OR r.provider_refund_id IS NOT NULL
            OR (SELECT coalesce(sum(rb.amount),0) FROM refunds rb
                 WHERE rb.tenant_id=r.tenant_id AND rb.source_kind='balance'
                   AND rb.balance_hold_id=r.balance_hold_id
                   AND rb.status NOT IN ('failed','rejected'))>h.amount))
     ) LIMIT 1;
    IF v_detail IS NOT NULL THEN
      RAISE EXCEPTION 'refund source invariant: %',v_detail
        USING ERRCODE='check_violation';
    END IF;

    v_detail:=NULL;
    SELECT format('order %s cached refund %s, successful facts %s, active facts %s',
                  o.id,o.refunded_amount,
                  coalesce(sum(r.amount) FILTER (WHERE r.status='succeeded'),0),
                  coalesce(sum(r.amount) FILTER (WHERE r.status NOT IN ('failed','rejected')),0))
      INTO v_detail
      FROM orders o LEFT JOIN refunds r
        ON r.tenant_id=o.tenant_id AND r.order_id=o.id
     WHERE o.tenant_id=p_tenant AND o.id=p_order
     GROUP BY o.id,o.status,o.paid_amount,o.refunded_amount
    HAVING o.refunded_amount<>
             coalesce(sum(r.amount) FILTER (WHERE r.status='succeeded'),0)
        OR coalesce(sum(r.amount) FILTER
             (WHERE r.status NOT IN ('failed','rejected')),0)>o.paid_amount
        OR (o.refunded_amount>0 AND o.refunded_amount<o.paid_amount
            AND o.status<>'partially_refunded')
        OR (o.paid_amount>0 AND o.refunded_amount=o.paid_amount
            AND o.status<>'refunded')
        OR (o.status='partially_refunded' AND
            (o.refunded_amount<=0 OR o.refunded_amount>=o.paid_amount))
        OR (o.status='refunded' AND
            (o.paid_amount<=0 OR o.refunded_amount<>o.paid_amount));
    IF v_detail IS NOT NULL THEN
      RAISE EXCEPTION 'order refund invariant: %',v_detail USING ERRCODE='check_violation';
    END IF;
  END IF;

  v_detail:=NULL;
  IF p_payment IS NOT NULL THEN
    SELECT format('payment %s cached refund %s, successful facts %s',
                  p.id,p.refunded_amount,
                  coalesce(sum(r.amount) FILTER (WHERE r.status='succeeded'),0))
      INTO v_detail
      FROM payments p LEFT JOIN refunds r
        ON r.tenant_id=p.tenant_id AND r.payment_id=p.id
       AND r.source_kind='payment'
     WHERE p.tenant_id=p_tenant AND p.id=p_payment
     GROUP BY p.id,p.status,p.amount,p.refunded_amount
    HAVING p.refunded_amount<>
             coalesce(sum(r.amount) FILTER (WHERE r.status='succeeded'),0)
        OR (p.refunded_amount>0 AND p.refunded_amount<p.amount
            AND p.status<>'partially_refunded')
        OR (p.refunded_amount=p.amount AND p.status<>'refunded')
        OR (p.status='partially_refunded' AND
            (p.refunded_amount<=0 OR p.refunded_amount>=p.amount))
        OR (p.status='refunded' AND p.refunded_amount<>p.amount);
    IF v_detail IS NOT NULL THEN
      RAISE EXCEPTION 'payment refund invariant: %',v_detail USING ERRCODE='check_violation';
    END IF;
  END IF;
END;
$$;

CREATE FUNCTION app.assert_refund_consistency_trigger() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
  v_tenant uuid;
  v_order uuid;
  v_payment uuid;
BEGIN
  IF TG_OP<>'DELETE' THEN
    v_tenant:=NEW.tenant_id;
    IF TG_TABLE_NAME='orders' THEN v_order:=NEW.id;
    ELSE v_order:=NEW.order_id;
    END IF;
    v_payment:=NULL;
    IF TG_TABLE_NAME='payments' THEN v_payment:=NEW.id;
    ELSIF TG_TABLE_NAME='refunds' THEN v_payment:=NEW.payment_id;
    END IF;
    PERFORM app.assert_refund_consistency(v_tenant,v_order,v_payment);
  END IF;
  IF TG_OP<>'INSERT' THEN
    v_tenant:=OLD.tenant_id;
    IF TG_TABLE_NAME='orders' THEN v_order:=OLD.id;
    ELSE v_order:=OLD.order_id;
    END IF;
    v_payment:=NULL;
    IF TG_TABLE_NAME='payments' THEN v_payment:=OLD.id;
    ELSIF TG_TABLE_NAME='refunds' THEN v_payment:=OLD.payment_id;
    END IF;
    PERFORM app.assert_refund_consistency(v_tenant,v_order,v_payment);
  END IF;
  RETURN NULL;
END;
$$;

CREATE FUNCTION app.assert_refund_request(p_tenant uuid,p_request uuid)
RETURNS void
LANGUAGE plpgsql AS $$
DECLARE
  v_detail text;
BEGIN
  SELECT format('request %s status %s/key %s has %s legs/%s amount/%s succeeded',
                rr.id,rr.status,k.status,count(r.id),coalesce(sum(r.amount),0),
                count(r.id) FILTER (WHERE r.status='succeeded'))
    INTO v_detail
    FROM refund_requests rr
    LEFT JOIN idempotency_keys k
      ON k.tenant_id=rr.tenant_id AND k.id=rr.business_request_id
    LEFT JOIN refunds r
      ON r.tenant_id=rr.tenant_id AND r.refund_request_id=rr.id
   WHERE rr.tenant_id=p_tenant AND rr.id=p_request
   GROUP BY rr.id,rr.status,rr.requested_amount,k.id,k.status,k.scope,
            k.request_hash,k.actor_id,k.resource_type,k.resource_id
  HAVING k.id IS NULL OR k.scope<>'refund_create'
      OR k.request_hash<>rr.request_hash
      OR k.actor_id IS DISTINCT FROM rr.requested_by
      OR k.resource_type<>'refund_request' OR k.resource_id<>rr.id
      OR (rr.status IN ('pending','processing','manual_review','partially_succeeded')
          AND k.status<>'in_flight')
      OR (rr.status='succeeded' AND k.status<>'succeeded')
      OR (rr.status='failed' AND k.status<>'failed')
      OR count(r.id)=0 OR coalesce(sum(r.amount),0)<>rr.requested_amount
      OR (rr.status='succeeded' AND
          count(r.id) FILTER (WHERE r.status='succeeded')<>count(r.id))
      OR (rr.status='failed' AND
          count(r.id) FILTER (WHERE r.status IN ('failed','rejected'))<>count(r.id))
      OR (rr.status IN ('pending','processing') AND
          count(r.id) FILTER (WHERE r.status IN ('succeeded','failed','rejected'))>0)
      OR (rr.status IN ('manual_review','partially_succeeded') AND
          (count(r.id) FILTER (WHERE r.status='succeeded')=0
           OR count(r.id) FILTER (WHERE r.status='succeeded')=count(r.id)));
  IF v_detail IS NOT NULL THEN
    RAISE EXCEPTION 'refund request invariant: %',v_detail USING ERRCODE='check_violation';
  END IF;
END;
$$;

CREATE FUNCTION app.assert_refund_idempotency_key(p_tenant uuid,p_key uuid)
RETURNS void
LANGUAGE plpgsql AS $$
DECLARE
  v_detail text;
BEGIN
  SELECT format('refund key %s status %s is not bidirectionally bound',k.id,k.status)
    INTO v_detail
    FROM idempotency_keys k
    LEFT JOIN refund_requests rr
      ON rr.tenant_id=k.tenant_id AND rr.business_request_id=k.id
   WHERE k.tenant_id=p_tenant AND k.id=p_key AND k.scope='refund_create'
   GROUP BY k.id,k.status,k.request_hash,k.actor_id,k.resource_type,k.resource_id,
            rr.id,rr.status,rr.request_hash,rr.requested_by
  HAVING rr.id IS NULL OR k.request_hash<>rr.request_hash
      OR k.actor_id IS DISTINCT FROM rr.requested_by
      OR k.resource_type<>'refund_request' OR k.resource_id<>rr.id
      OR (rr.status IN ('pending','processing','manual_review','partially_succeeded')
          AND k.status<>'in_flight')
      OR (rr.status='succeeded' AND k.status<>'succeeded')
      OR (rr.status='failed' AND k.status<>'failed');
  IF v_detail IS NOT NULL THEN
    RAISE EXCEPTION 'refund idempotency invariant: %',v_detail
      USING ERRCODE='check_violation';
  END IF;
END;
$$;

CREATE FUNCTION app.assert_refund_request_trigger() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
  v_tenant uuid;
  v_request uuid;
BEGIN
  IF TG_OP<>'DELETE' THEN
    v_tenant:=NEW.tenant_id;
    IF TG_TABLE_NAME='refund_requests' THEN v_request:=NEW.id;
    ELSE v_request:=NEW.refund_request_id;
    END IF;
    PERFORM app.assert_refund_request(v_tenant,v_request);
  END IF;
  IF TG_OP<>'INSERT' THEN
    v_tenant:=OLD.tenant_id;
    IF TG_TABLE_NAME='refund_requests' THEN v_request:=OLD.id;
    ELSE v_request:=OLD.refund_request_id;
    END IF;
    PERFORM app.assert_refund_request(v_tenant,v_request);
  END IF;
  RETURN NULL;
END;
$$;

CREATE FUNCTION app.assert_refund_idempotency_key_trigger() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP<>'DELETE' THEN
    PERFORM app.assert_refund_idempotency_key(NEW.tenant_id,NEW.id);
  END IF;
  IF TG_OP<>'INSERT' THEN
    PERFORM app.assert_refund_idempotency_key(OLD.tenant_id,OLD.id);
  END IF;
  RETURN NULL;
END;
$$;

CREATE FUNCTION app.guard_payment_event_evidence() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF (to_jsonb(NEW)-ARRAY['processing_status','processing_error','processed_at'])
     IS DISTINCT FROM
     (to_jsonb(OLD)-ARRAY['processing_status','processing_error','processed_at']) THEN
    RAISE EXCEPTION 'payment event raw payload, headers and provider identity are immutable'
      USING ERRCODE='check_violation';
  END IF;
  IF OLD.processing_status IN ('processed','ignored') AND
     to_jsonb(NEW) IS DISTINCT FROM to_jsonb(OLD) THEN
    RAISE EXCEPTION 'terminal payment event assessment is immutable'
      USING ERRCODE='check_violation';
  END IF;
  IF NEW.processing_status IS DISTINCT FROM OLD.processing_status AND NOT
     (OLD.processing_status IN ('pending','failed')
      AND NEW.processing_status IN ('processed','ignored','failed')) THEN
    RAISE EXCEPTION 'illegal payment event assessment transition % -> %',
      OLD.processing_status,NEW.processing_status USING ERRCODE='check_violation';
  END IF;
  IF (NEW.processing_status='pending' AND
      (NEW.processing_error IS NOT NULL OR NEW.processed_at IS NOT NULL))
     OR (NEW.processing_status IN ('processed','ignored') AND NEW.processed_at IS NULL)
     OR (NEW.processing_status='failed' AND
         (nullif(btrim(NEW.processing_error),'') IS NULL OR NEW.processed_at IS NOT NULL)) THEN
    RAISE EXCEPTION 'payment event assessment status/time/error shape is invalid'
      USING ERRCODE='check_violation';
  END IF;
  RETURN NEW;
END;
$$;

CREATE FUNCTION app.guard_invoice_evidence() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF (to_jsonb(NEW)-'status') IS DISTINCT FROM (to_jsonb(OLD)-'status') THEN
    RAISE EXCEPTION 'invoice order, number, buyer/seller/lines and amounts are immutable'
      USING ERRCODE='check_violation';
  END IF;
  IF NEW.status IS DISTINCT FROM OLD.status AND NOT (
       (OLD.status='draft' AND NEW.status IN ('issued','void'))
    OR (OLD.status='issued' AND NEW.status IN ('void','credited'))
  ) THEN
    RAISE EXCEPTION 'illegal invoice transition % -> %',OLD.status,NEW.status
      USING ERRCODE='check_violation';
  END IF;
  RETURN NEW;
END;
$$;

-- Column ACLs keep generated evidence out of normal INSERT statements. These
-- value guards also make the application-supplied status columns fail closed:
-- terminal rows may only be produced by a controlled transition.
CREATE FUNCTION app.guard_finance_initial_state() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF current_user = 'aegis_app' AND TG_TABLE_NAME = 'orders'
     AND NEW.status NOT IN ('draft','pending_payment') THEN
    RAISE EXCEPTION 'orders must be inserted in draft or pending_payment state'
      USING ERRCODE = 'insufficient_privilege';
  ELSIF current_user = 'aegis_app' AND TG_TABLE_NAME = 'payment_intents'
        AND NEW.status NOT IN ('created','requires_action') THEN
    RAISE EXCEPTION 'payment intents must be inserted in created or requires_action state'
      USING ERRCODE = 'insufficient_privilege';
  ELSIF current_user = 'aegis_app' AND TG_TABLE_NAME = 'payments'
        AND NEW.status <> 'succeeded' THEN
    RAISE EXCEPTION 'payments must be inserted as succeeded evidence'
      USING ERRCODE = 'insufficient_privilege';
  END IF;
  RETURN NEW;
END;
$$;

CREATE FUNCTION app.guard_ledger_account_evidence() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'ledger accounts are financial evidence and cannot be deleted'
      USING ERRCODE = 'insufficient_privilege';
  END IF;
  IF (to_jsonb(NEW) - ARRAY['balance_signed','updated_at']) IS DISTINCT FROM
     (to_jsonb(OLD) - ARRAY['balance_signed','updated_at']) THEN
    RAISE EXCEPTION 'ledger account identity and classification are immutable'
      USING ERRCODE = 'insufficient_privilege';
  END IF;
  IF NEW.balance_signed IS DISTINCT FROM OLD.balance_signed
     AND pg_trigger_depth() < 2 THEN
    RAISE EXCEPTION 'ledger balance cache is maintained only by ledger entries'
      USING ERRCODE = 'insufficient_privilege';
  END IF;
  IF pg_trigger_depth() < 2
     AND NEW.updated_at IS DISTINCT FROM OLD.updated_at THEN
    RAISE EXCEPTION 'ledger account timestamp is trigger-maintained'
      USING ERRCODE = 'insufficient_privilege';
  END IF;
  RETURN NEW;
END;
$$;

-- This trigger function needs to update the cached balance although the app
-- role itself has no UPDATE(balance_signed) privilege. A fixed search_path and
-- trigger-only return type keep the definer boundary narrow.
ALTER FUNCTION app.apply_ledger_entry_to_account() SECURITY DEFINER;
ALTER FUNCTION app.apply_ledger_entry_to_account()
  SET search_path = pg_catalog, public;

-- Every writer may temporarily create an intermediate state, but the commit
-- boundary must expose one coherent order/resource aggregate.  This deliberately
-- makes schema-35 order/webhook writers fail closed after the stop-the-world
-- migration instead of silently consuming inventory twice.
CREATE FUNCTION app.assert_order_reservation_target(
  p_table text, p_new jsonb, p_old jsonb
) RETURNS void
LANGUAGE plpgsql AS $$
DECLARE
  v_tenant uuid := nullif(coalesce(p_new->>'tenant_id', p_old->>'tenant_id'),'')::uuid;
  v_order uuid;
  v_reservation uuid;
  v_plan uuid;
  v_user uuid;
  v_coupon uuid;
  v_detail text;
BEGIN
  IF p_table='orders' THEN
    v_order := nullif(coalesce(p_new->>'id',p_old->>'id'),'')::uuid;
  ELSIF p_table='order_items' THEN
    v_order := nullif(coalesce(p_new->>'order_id',p_old->>'order_id'),'')::uuid;
    v_plan := nullif(coalesce(p_new->>'plan_id',p_old->>'plan_id'),'')::uuid;
  ELSIF p_table='order_reservations' THEN
    v_order := nullif(coalesce(p_new->>'order_id',p_old->>'order_id'),'')::uuid;
    v_reservation := nullif(coalesce(p_new->>'id',p_old->>'id'),'')::uuid;
  ELSIF p_table IN ('order_stock_reservations','order_purchase_limit_reservations',
                    'coupon_redemptions','balance_holds','order_reservation_events') THEN
    v_order := nullif(coalesce(p_new->>'order_id',p_old->>'order_id'),'')::uuid;
    v_reservation := nullif(coalesce(p_new->>'reservation_id',p_old->>'reservation_id'),'')::uuid;
    v_plan := nullif(coalesce(p_new->>'plan_id',p_old->>'plan_id'),'')::uuid;
    v_user := nullif(coalesce(p_new->>'user_id',p_old->>'user_id'),'')::uuid;
    v_coupon := nullif(coalesce(p_new->>'coupon_id',p_old->>'coupon_id'),'')::uuid;
  ELSIF p_table='plans' THEN
    v_plan := nullif(coalesce(p_new->>'id',p_old->>'id'),'')::uuid;
  ELSIF p_table='plan_purchase_counters' THEN
    v_plan := nullif(coalesce(p_new->>'plan_id',p_old->>'plan_id'),'')::uuid;
    v_user := nullif(coalesce(p_new->>'user_id',p_old->>'user_id'),'')::uuid;
  ELSIF p_table='coupons' THEN
    v_coupon := nullif(coalesce(p_new->>'id',p_old->>'id'),'')::uuid;
  END IF;

  IF v_order IS NOT NULL THEN
    SELECT format('order %s status %s has reservation count/state %s/%s',
                  o.id,o.status,count(r.id),min(r.state)) INTO v_detail
      FROM orders o LEFT JOIN order_reservations r
        ON r.tenant_id=o.tenant_id AND r.order_id=o.id
     WHERE o.tenant_id=v_tenant AND o.id=v_order
     GROUP BY o.id,o.status
    HAVING count(r.id)<>1 OR min(r.state) IS DISTINCT FROM CASE
      WHEN o.status IN ('draft','pending_payment','processing') THEN 'held'
      WHEN o.status IN ('paid','fulfilled','refunded','partially_refunded') THEN 'captured'
      ELSE 'released' END;
    IF v_detail IS NOT NULL THEN
      RAISE EXCEPTION 'reservation/order invariant: %',v_detail USING ERRCODE='check_violation';
    END IF;

    SELECT format('zero-payable order %s lacks fulfilment evidence',id) INTO v_detail
      FROM orders WHERE tenant_id=v_tenant AND id=v_order
       AND kind<>'topup' AND payable_amount=0
       AND (status NOT IN ('fulfilled','partially_refunded','refunded')
            OR fulfilled_at IS NULL);
    IF v_detail IS NOT NULL THEN
      RAISE EXCEPTION 'zero-payable order must capture and fulfil atomically: %',v_detail
        USING ERRCODE='check_violation';
    END IF;

    SELECT format('reservation %s event chain mismatch',r.id) INTO v_detail
      FROM order_reservations r WHERE r.tenant_id=v_tenant AND r.order_id=v_order
       AND ((SELECT count(*) FROM order_reservation_events e
              WHERE e.reservation_id=r.id AND e.to_state='held')<>1
         OR EXISTS (SELECT 1 FROM order_reservation_events e JOIN orders o
              ON o.tenant_id=e.tenant_id AND o.id=e.order_id
             WHERE e.reservation_id=r.id AND e.to_state='held'
               AND (e.from_state IS NOT NULL OR e.event_kind NOT IN ('reserve','backfill')
                    OR e.business_request_id<>o.business_request_id))
         OR (r.state='held' AND EXISTS (SELECT 1 FROM order_reservation_events e
              WHERE e.reservation_id=r.id AND e.to_state IN ('captured','released')))
         OR (r.state IN ('captured','released') AND
             (SELECT count(*) FROM order_reservation_events e
               WHERE e.reservation_id=r.id AND e.to_state=r.state)<>1)
         OR EXISTS (SELECT 1 FROM order_reservation_events e JOIN orders o
             ON o.tenant_id=e.tenant_id AND o.id=e.order_id
             WHERE e.reservation_id=r.id AND e.to_state IN ('captured','released')
               AND NOT e.legacy_backfill
               AND (e.from_state IS DISTINCT FROM 'held'
                    OR e.business_request_id<>o.business_request_id
                    OR (e.to_state='captured' AND e.event_kind<>'capture')
                    OR (e.to_state='released' AND e.event_kind NOT IN ('release','expire','cancel')))));
    IF v_detail IS NOT NULL THEN
      RAISE EXCEPTION 'reservation event invariant: %',v_detail USING ERRCODE='check_violation';
    END IF;

    SELECT format('order %s has an invalid item/resource graph',o.id) INTO v_detail
      FROM orders o WHERE o.tenant_id=v_tenant AND o.id=v_order
       AND ((o.kind IN ('new','renewal') AND
             ((SELECT count(*) FROM order_items oi WHERE oi.tenant_id=o.tenant_id
               AND oi.order_id=o.id)<>1 OR NOT EXISTS (SELECT 1 FROM order_items oi
               WHERE oi.tenant_id=o.tenant_id AND oi.order_id=o.id
                 AND oi.plan_id IS NOT NULL)))
         OR (o.kind='topup' AND EXISTS (SELECT 1 FROM order_items oi
               WHERE oi.tenant_id=o.tenant_id AND oi.order_id=o.id))
         OR (o.kind<>'topup' AND (coalesce((SELECT sum(oi.line_amount)
               FROM order_items oi WHERE oi.tenant_id=o.tenant_id AND oi.order_id=o.id),0)
               <>o.subtotal_amount OR EXISTS (SELECT 1 FROM order_items oi
               WHERE oi.tenant_id=o.tenant_id AND oi.order_id=o.id
                 AND oi.currency<>o.currency)))
         OR (o.kind='new' AND ((SELECT count(*) FROM order_stock_reservations sr
               WHERE sr.tenant_id=o.tenant_id AND sr.order_id=o.id)<>1
             OR NOT EXISTS (SELECT 1 FROM order_items oi JOIN order_stock_reservations sr
              ON sr.tenant_id=oi.tenant_id AND sr.order_id=oi.order_id
             AND sr.plan_id=oi.plan_id AND sr.quantity=oi.quantity
             WHERE oi.tenant_id=o.tenant_id AND oi.order_id=o.id)))
         OR (o.kind<>'new' AND (EXISTS (SELECT 1 FROM order_stock_reservations sr
               WHERE sr.tenant_id=o.tenant_id AND sr.order_id=o.id)
             OR EXISTS (SELECT 1 FROM order_purchase_limit_reservations pr
               WHERE pr.tenant_id=o.tenant_id AND pr.order_id=o.id)))
         OR (o.kind='new' AND EXISTS (SELECT 1 FROM order_items oi JOIN plans p
              ON p.tenant_id=oi.tenant_id AND p.id=oi.plan_id
             WHERE oi.tenant_id=o.tenant_id AND oi.order_id=o.id
               AND p.purchase_limit_per_user IS NOT NULL)
             AND ((SELECT count(*) FROM order_purchase_limit_reservations pr
                    WHERE pr.tenant_id=o.tenant_id AND pr.order_id=o.id)<>1
               OR NOT EXISTS (SELECT 1 FROM order_items oi JOIN plans p
              ON p.tenant_id=oi.tenant_id AND p.id=oi.plan_id
              JOIN order_purchase_limit_reservations pr
                ON pr.tenant_id=oi.tenant_id AND pr.order_id=oi.order_id
               AND pr.plan_id=oi.plan_id AND pr.user_id=o.user_id
               AND pr.quantity=oi.quantity
             WHERE oi.tenant_id=o.tenant_id AND oi.order_id=o.id
               AND p.purchase_limit_per_user IS NOT NULL)))
         OR (o.kind='new' AND NOT EXISTS (SELECT 1 FROM order_items oi JOIN plans p
              ON p.tenant_id=oi.tenant_id AND p.id=oi.plan_id
             WHERE oi.tenant_id=o.tenant_id AND oi.order_id=o.id
               AND p.purchase_limit_per_user IS NOT NULL)
             AND EXISTS (SELECT 1 FROM order_purchase_limit_reservations pr
               WHERE pr.tenant_id=o.tenant_id AND pr.order_id=o.id)));
    IF v_detail IS NOT NULL THEN
      RAISE EXCEPTION 'resource child invariant: %',v_detail USING ERRCODE='check_violation';
    END IF;

    SELECT format('order %s coupon reservation mismatch',o.id) INTO v_detail
      FROM orders o WHERE o.tenant_id=v_tenant AND o.id=v_order AND
       ((o.coupon_id IS NOT NULL AND ((SELECT count(*) FROM coupon_redemptions cr
          WHERE cr.tenant_id=o.tenant_id AND cr.order_id=o.id)<>1
         OR NOT EXISTS (SELECT 1 FROM coupon_redemptions cr
          WHERE cr.tenant_id=o.tenant_id AND cr.order_id=o.id AND cr.user_id=o.user_id
            AND cr.coupon_id=o.coupon_id AND cr.currency=o.currency
            AND cr.discount_amount=o.discount_amount)))
        OR (o.coupon_id IS NULL AND EXISTS (SELECT 1 FROM coupon_redemptions cr
          WHERE cr.tenant_id=o.tenant_id AND cr.order_id=o.id)));
    IF v_detail IS NOT NULL THEN
      RAISE EXCEPTION 'coupon/order invariant: %',v_detail USING ERRCODE='check_violation';
    END IF;

    SELECT format('order %s balance hold mismatch',o.id) INTO v_detail
      FROM orders o WHERE o.tenant_id=v_tenant AND o.id=v_order AND
       ((o.balance_applied>0 AND ((SELECT count(*) FROM balance_holds h
          WHERE h.tenant_id=o.tenant_id AND h.order_id=o.id)<>1
         OR NOT EXISTS (SELECT 1 FROM balance_holds h
          WHERE h.tenant_id=o.tenant_id AND h.order_id=o.id AND h.user_id=o.user_id
            AND h.currency=o.currency AND h.amount=o.balance_applied)))
        OR (o.balance_applied=0 AND EXISTS (SELECT 1 FROM balance_holds h
          WHERE h.tenant_id=o.tenant_id AND h.order_id=o.id)));
    IF v_detail IS NOT NULL THEN
      RAISE EXCEPTION 'balance/order invariant: %',v_detail USING ERRCODE='check_violation';
    END IF;
  END IF;

  SELECT format('stock aggregate mismatch for plan %s',p.id) INTO v_detail
    FROM plans p WHERE p.tenant_id=v_tenant
     AND (p.id=v_plan OR EXISTS (SELECT 1 FROM order_stock_reservations sr
          WHERE sr.tenant_id=v_tenant AND sr.plan_id=p.id
            AND (sr.order_id=v_order OR sr.reservation_id=v_reservation)))
     AND (p.stock_reserved<>coalesce((SELECT sum(sr.quantity)::int
          FROM order_stock_reservations sr JOIN order_reservations r ON r.id=sr.reservation_id
          WHERE sr.tenant_id=p.tenant_id AND sr.plan_id=p.id AND r.state='held'),0)
       OR p.stock_sold<>coalesce((SELECT sum(sr.quantity)::int
          FROM order_stock_reservations sr JOIN order_reservations r ON r.id=sr.reservation_id
          WHERE sr.tenant_id=p.tenant_id AND sr.plan_id=p.id AND r.state='captured'),0)) LIMIT 1;
  IF v_detail IS NOT NULL THEN
    RAISE EXCEPTION 'stock conservation invariant: %',v_detail USING ERRCODE='check_violation';
  END IF;

  SELECT format('purchase aggregate mismatch for %s/%s',pc.plan_id,pc.user_id) INTO v_detail
    FROM plan_purchase_counters pc WHERE pc.tenant_id=v_tenant
     AND ((pc.plan_id=v_plan AND (v_user IS NULL OR pc.user_id=v_user)) OR EXISTS (
          SELECT 1 FROM order_purchase_limit_reservations pr WHERE pr.tenant_id=v_tenant
           AND pr.plan_id=pc.plan_id AND pr.user_id=pc.user_id
           AND (pr.order_id=v_order OR pr.reservation_id=v_reservation)))
     AND (pc.reserved<>coalesce((SELECT sum(pr.quantity)::int
          FROM order_purchase_limit_reservations pr JOIN order_reservations r ON r.id=pr.reservation_id
          WHERE pr.tenant_id=pc.tenant_id AND pr.plan_id=pc.plan_id
            AND pr.user_id=pc.user_id AND r.state='held'),0)
       OR pc.purchased<>coalesce((SELECT sum(pr.quantity)::int
          FROM order_purchase_limit_reservations pr JOIN order_reservations r ON r.id=pr.reservation_id
          WHERE pr.tenant_id=pc.tenant_id AND pr.plan_id=pc.plan_id
            AND pr.user_id=pc.user_id AND r.state='captured'),0)) LIMIT 1;
  IF v_detail IS NOT NULL THEN
    RAISE EXCEPTION 'purchase conservation invariant: %',v_detail USING ERRCODE='check_violation';
  END IF;

  SELECT format('coupon aggregate mismatch for %s',c.id) INTO v_detail
    FROM coupons c WHERE c.tenant_id=v_tenant
     AND (c.id=v_coupon OR EXISTS (SELECT 1 FROM coupon_redemptions cr
          WHERE cr.tenant_id=v_tenant AND cr.coupon_id=c.id
            AND (cr.order_id=v_order OR cr.reservation_id=v_reservation)))
     AND (c.reserved_count<>coalesce((SELECT count(*)::int FROM coupon_redemptions cr
          WHERE cr.tenant_id=c.tenant_id AND cr.coupon_id=c.id AND cr.status='held'),0)
       OR c.redeemed_count<>coalesce((SELECT count(*)::int FROM coupon_redemptions cr
          WHERE cr.tenant_id=c.tenant_id AND cr.coupon_id=c.id AND cr.status='captured'),0)) LIMIT 1;
  IF v_detail IS NOT NULL THEN
    RAISE EXCEPTION 'coupon conservation invariant: %',v_detail USING ERRCODE='check_violation';
  END IF;

  SELECT format('coupon reservation %s lifecycle mismatch',cr.id) INTO v_detail
    FROM coupon_redemptions cr JOIN order_reservations r ON r.id=cr.reservation_id
   WHERE cr.tenant_id=v_tenant AND (cr.order_id=v_order OR cr.reservation_id=v_reservation)
     AND cr.status<>r.state LIMIT 1;
  IF v_detail IS NOT NULL THEN
    RAISE EXCEPTION 'coupon lifecycle invariant: %',v_detail USING ERRCODE='check_violation';
  END IF;

  SELECT format('balance hold %s lifecycle/posting mismatch',h.id) INTO v_detail
    FROM balance_holds h JOIN order_reservations r ON r.id=h.reservation_id
    LEFT JOIN ledger_accounts aa ON aa.id=h.available_account_id
    LEFT JOIN ledger_accounts ha ON ha.id=h.hold_account_id
   WHERE h.tenant_id=v_tenant AND (h.order_id=v_order OR h.reservation_id=v_reservation)
     AND (h.status<>r.state OR (h.status='held' AND h.hold_txn_id IS NULL)
       OR (h.available_account_id IS NOT NULL AND
           (aa.tenant_id<>h.tenant_id OR aa.currency<>h.currency
            OR aa.account_type<>'user_balance' OR aa.owner_user_id<>h.user_id))
       OR (h.hold_account_id IS NOT NULL AND
           (ha.tenant_id<>h.tenant_id OR ha.currency<>h.currency
            OR ha.account_type<>'user_balance_hold' OR ha.owner_user_id<>h.user_id))
       OR (NOT h.legacy_backfill AND h.status='captured' AND h.capture_txn_id IS NULL)
       OR (NOT h.legacy_backfill AND h.status='released' AND h.release_txn_id IS NULL)
       OR (h.hold_txn_id IS NOT NULL AND
           ((SELECT count(*) FROM ledger_entries e WHERE e.transaction_id=h.hold_txn_id)<>2
            OR NOT EXISTS (SELECT 1 FROM ledger_transactions t
            JOIN ledger_entries d ON d.transaction_id=t.id
            JOIN ledger_entries c ON c.transaction_id=t.id AND c.id<>d.id
           WHERE t.id=h.hold_txn_id AND t.tenant_id=h.tenant_id
             AND t.kind='balance_hold'
             AND t.currency=h.currency AND t.source_type='order' AND t.source_id=h.order_id
             AND d.account_id=h.available_account_id
             AND d.direction='debit' AND d.amount=h.amount AND d.currency=h.currency
             AND c.account_id=h.hold_account_id AND c.direction='credit'
             AND c.amount=h.amount AND c.currency=h.currency)))
       OR (h.capture_txn_id IS NOT NULL AND NOT EXISTS (SELECT 1
            FROM ledger_transactions t JOIN ledger_entries d ON d.transaction_id=t.id
           WHERE t.id=h.capture_txn_id AND t.tenant_id=h.tenant_id
             AND t.kind='order_paid'
             AND t.currency=h.currency AND t.source_type='order' AND t.source_id=h.order_id
             AND d.account_id=h.hold_account_id AND d.direction='debit'
             AND d.amount=h.amount AND d.currency=h.currency
             AND (SELECT count(*) FROM ledger_entries x
                   WHERE x.transaction_id=t.id AND x.account_id=h.hold_account_id)=1
             AND EXISTS (SELECT 1 FROM ledger_entries rev
                   JOIN ledger_accounts ra ON ra.id=rev.account_id
                   JOIN orders o ON o.tenant_id=h.tenant_id AND o.id=h.order_id
                  WHERE rev.transaction_id=t.id AND rev.direction='credit'
                    AND rev.currency=h.currency AND rev.amount=o.total_amount
                    AND ra.tenant_id=h.tenant_id AND ra.currency=h.currency
                    AND ra.account_type='platform_revenue')))
       OR (h.release_txn_id IS NOT NULL AND
           ((SELECT count(*) FROM ledger_entries e WHERE e.transaction_id=h.release_txn_id)<>2
            OR NOT EXISTS (SELECT 1 FROM ledger_transactions t
            JOIN ledger_entries d ON d.transaction_id=t.id
            JOIN ledger_entries c ON c.transaction_id=t.id AND c.id<>d.id
           WHERE t.id=h.release_txn_id AND t.tenant_id=h.tenant_id
             AND t.kind='balance_release'
             AND t.currency=h.currency AND t.source_type='order' AND t.source_id=h.order_id
             AND d.account_id=h.hold_account_id AND d.direction='debit'
             AND d.amount=h.amount AND d.currency=h.currency
             AND c.account_id=h.available_account_id AND c.direction='credit'
             AND c.amount=h.amount AND c.currency=h.currency)))) LIMIT 1;
  IF v_detail IS NOT NULL THEN
    RAISE EXCEPTION 'balance hold invariant: %',v_detail USING ERRCODE='check_violation';
  END IF;
END;
$$;

CREATE FUNCTION app.assert_order_reservation_invariants() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP <> 'DELETE' THEN
    PERFORM app.assert_order_reservation_target(TG_TABLE_NAME,to_jsonb(NEW),NULL);
  END IF;
  IF TG_OP <> 'INSERT' THEN
    PERFORM app.assert_order_reservation_target(TG_TABLE_NAME,NULL,to_jsonb(OLD));
  END IF;
  RETURN NULL;
END;
$$;

CREATE FUNCTION app.guard_order_item_immutable() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP='DELETE' OR to_jsonb(NEW) IS DISTINCT FROM to_jsonb(OLD) THEN
    RAISE EXCEPTION 'order item identity, plan and financial snapshot are immutable'
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN CASE WHEN TG_OP='DELETE' THEN OLD ELSE NEW END;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER trg_order_reservations_state_guard
  BEFORE UPDATE ON order_reservations
  FOR EACH ROW EXECUTE FUNCTION app.guard_order_reservation_transition();
CREATE TRIGGER trg_order_items_immutable
  BEFORE UPDATE OR DELETE ON order_items
  FOR EACH ROW EXECUTE FUNCTION app.guard_order_item_immutable();
CREATE TRIGGER trg_coupon_redemptions_state_guard
  BEFORE UPDATE ON coupon_redemptions
  FOR EACH ROW EXECUTE FUNCTION app.guard_coupon_reservation_transition();
CREATE TRIGGER trg_balance_holds_state_guard
  BEFORE UPDATE ON balance_holds
  FOR EACH ROW EXECUTE FUNCTION app.guard_balance_hold_transition();
CREATE TRIGGER trg_late_payment_cases_guard
  BEFORE INSERT OR UPDATE ON late_payment_cases
  FOR EACH ROW EXECUTE FUNCTION app.guard_late_payment_case();
CREATE TRIGGER trg_payment_webhook_receipts_guard
  BEFORE UPDATE ON payment_webhook_receipts
  FOR EACH ROW EXECUTE FUNCTION app.guard_payment_webhook_receipt();
CREATE TRIGGER trg_payment_webhook_receipts_no_delete
  BEFORE DELETE ON payment_webhook_receipts
  FOR EACH STATEMENT EXECUTE FUNCTION app.deny_delete();
CREATE TRIGGER trg_payment_intents_evidence_guard
  BEFORE UPDATE ON payment_intents FOR EACH ROW EXECUTE FUNCTION app.guard_payment_intent();
CREATE TRIGGER trg_orders_initial_state_guard
  BEFORE INSERT ON orders FOR EACH ROW EXECUTE FUNCTION app.guard_finance_initial_state();
CREATE TRIGGER trg_payment_intents_initial_state_guard
  BEFORE INSERT ON payment_intents FOR EACH ROW EXECUTE FUNCTION app.guard_finance_initial_state();
CREATE TRIGGER trg_payments_initial_state_guard
  BEFORE INSERT ON payments FOR EACH ROW EXECUTE FUNCTION app.guard_finance_initial_state();
CREATE TRIGGER trg_ledger_accounts_evidence_guard
  BEFORE UPDATE OR DELETE ON ledger_accounts
  FOR EACH ROW EXECUTE FUNCTION app.guard_ledger_account_evidence();
CREATE TRIGGER trg_refund_ledger_entries_finalized
  BEFORE INSERT ON ledger_entries
  FOR EACH ROW EXECUTE FUNCTION app.guard_finalized_refund_ledger_entry();
CREATE TRIGGER trg_payments_evidence_guard
  BEFORE UPDATE ON payments FOR EACH ROW EXECUTE FUNCTION app.guard_payment_evidence();
CREATE TRIGGER zz_idempotency_evidence_guard
  BEFORE INSERT OR UPDATE ON idempotency_keys
  FOR EACH ROW EXECUTE FUNCTION app.guard_idempotency_evidence();
CREATE TRIGGER trg_idempotency_no_delete
  BEFORE DELETE OR TRUNCATE ON idempotency_keys
  FOR EACH STATEMENT EXECUTE FUNCTION app.deny_delete();
CREATE TRIGGER zz_refunds_evidence_guard
  BEFORE UPDATE ON refunds FOR EACH ROW EXECUTE FUNCTION app.guard_refund_evidence();
CREATE TRIGGER trg_refunds_source_guard
  BEFORE INSERT OR UPDATE ON refunds FOR EACH ROW EXECUTE FUNCTION app.guard_refund_source();
CREATE TRIGGER trg_refunds_no_delete
  BEFORE DELETE ON refunds FOR EACH STATEMENT EXECUTE FUNCTION app.deny_delete();
CREATE TRIGGER zz_refund_requests_guard
  BEFORE INSERT OR UPDATE ON refund_requests
  FOR EACH ROW EXECUTE FUNCTION app.guard_refund_request();
CREATE TRIGGER trg_refund_requests_no_delete
  BEFORE DELETE ON refund_requests FOR EACH STATEMENT EXECUTE FUNCTION app.deny_delete();
CREATE TRIGGER trg_payment_events_evidence_guard
  BEFORE UPDATE ON payment_events FOR EACH ROW EXECUTE FUNCTION app.guard_payment_event_evidence();
CREATE TRIGGER trg_invoices_evidence_guard
  BEFORE UPDATE ON invoices FOR EACH ROW EXECUTE FUNCTION app.guard_invoice_evidence();

CREATE CONSTRAINT TRIGGER trg_orders_reservation_commit
  AFTER INSERT OR UPDATE OR DELETE ON orders
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  EXECUTE FUNCTION app.assert_order_reservation_invariants();
CREATE CONSTRAINT TRIGGER trg_order_items_reservation_commit
  AFTER INSERT OR UPDATE OR DELETE ON order_items
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  EXECUTE FUNCTION app.assert_order_reservation_invariants();
CREATE CONSTRAINT TRIGGER trg_reservations_commit
  AFTER INSERT OR UPDATE OR DELETE ON order_reservations
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  EXECUTE FUNCTION app.assert_order_reservation_invariants();
CREATE CONSTRAINT TRIGGER trg_stock_reservations_commit
  AFTER INSERT OR UPDATE OR DELETE ON order_stock_reservations
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  EXECUTE FUNCTION app.assert_order_reservation_invariants();
CREATE CONSTRAINT TRIGGER trg_purchase_reservations_commit
  AFTER INSERT OR UPDATE OR DELETE ON order_purchase_limit_reservations
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  EXECUTE FUNCTION app.assert_order_reservation_invariants();
CREATE CONSTRAINT TRIGGER trg_coupon_reservations_commit
  AFTER INSERT OR UPDATE OR DELETE ON coupon_redemptions
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  EXECUTE FUNCTION app.assert_order_reservation_invariants();
CREATE CONSTRAINT TRIGGER trg_balance_holds_commit
  AFTER INSERT OR UPDATE OR DELETE ON balance_holds
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  EXECUTE FUNCTION app.assert_order_reservation_invariants();
CREATE CONSTRAINT TRIGGER trg_reservation_events_commit
  AFTER INSERT OR UPDATE OR DELETE ON order_reservation_events
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  EXECUTE FUNCTION app.assert_order_reservation_invariants();
CREATE CONSTRAINT TRIGGER trg_refunds_consistency_commit
  AFTER INSERT OR UPDATE OR DELETE ON refunds
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  EXECUTE FUNCTION app.assert_refund_consistency_trigger();
CREATE CONSTRAINT TRIGGER trg_orders_refunds_consistency_commit
  AFTER INSERT OR UPDATE OR DELETE ON orders
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  EXECUTE FUNCTION app.assert_refund_consistency_trigger();
CREATE CONSTRAINT TRIGGER trg_payments_refunds_consistency_commit
  AFTER INSERT OR UPDATE OR DELETE ON payments
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  EXECUTE FUNCTION app.assert_refund_consistency_trigger();
CREATE CONSTRAINT TRIGGER trg_refund_legs_request_commit
  AFTER INSERT OR UPDATE OR DELETE ON refunds
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  EXECUTE FUNCTION app.assert_refund_request_trigger();
CREATE CONSTRAINT TRIGGER trg_refund_requests_commit
  AFTER INSERT OR UPDATE OR DELETE ON refund_requests
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  EXECUTE FUNCTION app.assert_refund_request_trigger();
CREATE CONSTRAINT TRIGGER trg_refund_idempotency_commit
  AFTER INSERT OR UPDATE OR DELETE ON idempotency_keys
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  EXECUTE FUNCTION app.assert_refund_idempotency_key_trigger();
CREATE CONSTRAINT TRIGGER trg_plans_reservation_commit
  AFTER INSERT OR UPDATE OR DELETE ON plans
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  EXECUTE FUNCTION app.assert_order_reservation_invariants();
CREATE CONSTRAINT TRIGGER trg_purchase_counters_commit
  AFTER INSERT OR UPDATE OR DELETE ON plan_purchase_counters
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  EXECUTE FUNCTION app.assert_order_reservation_invariants();
CREATE CONSTRAINT TRIGGER trg_coupons_reservation_commit
  AFTER INSERT OR UPDATE OR DELETE ON coupons
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  EXECUTE FUNCTION app.assert_order_reservation_invariants();

CREATE TRIGGER trg_order_reservations_updated_at BEFORE UPDATE ON order_reservations
  FOR EACH ROW EXECUTE FUNCTION app.set_updated_at();
CREATE TRIGGER trg_balance_holds_updated_at BEFORE UPDATE ON balance_holds
  FOR EACH ROW EXECUTE FUNCTION app.set_updated_at();
CREATE TRIGGER trg_late_payment_cases_updated_at BEFORE UPDATE ON late_payment_cases
  FOR EACH ROW EXECUTE FUNCTION app.set_updated_at();

SELECT app.enable_tenant_rls('order_reservations');
SELECT app.enable_tenant_rls('order_stock_reservations');
SELECT app.enable_tenant_rls('order_purchase_limit_reservations');
SELECT app.enable_tenant_rls('balance_holds');
SELECT app.enable_tenant_rls('late_payment_cases');
SELECT app.enable_tenant_rls('order_reservation_events');
SELECT app.enable_tenant_rls('payment_webhook_receipts');
SELECT app.make_append_only('order_stock_reservations');
SELECT app.make_append_only('order_purchase_limit_reservations');
SELECT app.make_append_only('order_reservation_events');

-- Fail the migration itself unless every migrated refund request, leg and
-- refund_create key forms one coherent, bidirectionally linked aggregate.
-- +goose StatementBegin
DO $$
DECLARE
  v_row record;
  v_requests bigint;
  v_keys bigint;
BEGIN
  FOR v_row IN SELECT tenant_id,id FROM refund_requests LOOP
    PERFORM app.assert_refund_request(v_row.tenant_id,v_row.id);
  END LOOP;
  FOR v_row IN
    SELECT tenant_id,id FROM idempotency_keys WHERE scope='refund_create'
  LOOP
    PERFORM app.assert_refund_idempotency_key(v_row.tenant_id,v_row.id);
  END LOOP;
  FOR v_row IN
    SELECT DISTINCT tenant_id,order_id,payment_id FROM refunds
  LOOP
    PERFORM app.assert_refund_consistency(
      v_row.tenant_id,v_row.order_id,v_row.payment_id
    );
  END LOOP;
  SELECT count(*) INTO v_requests FROM refund_requests;
  SELECT count(*) INTO v_keys FROM idempotency_keys WHERE scope='refund_create';
  IF v_requests<>v_keys THEN
    RAISE EXCEPTION 'refund migration consistency count mismatch: requests %, keys %',
      v_requests,v_keys USING ERRCODE='check_violation';
  END IF;
END $$;
-- +goose StatementEnd

-- Default future rows to non-legacy after the migration-created rows exist.
ALTER TABLE order_reservations ALTER COLUMN legacy_backfill SET DEFAULT false;
ALTER TABLE order_stock_reservations ALTER COLUMN legacy_backfill SET DEFAULT false;
ALTER TABLE order_purchase_limit_reservations ALTER COLUMN legacy_backfill SET DEFAULT false;
ALTER TABLE coupon_redemptions ALTER COLUMN legacy_backfill SET DEFAULT false;
ALTER TABLE balance_holds ALTER COLUMN legacy_backfill SET DEFAULT false;
ALTER TABLE order_reservation_events ALTER COLUMN legacy_backfill SET DEFAULT false;

-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'aegis_app') THEN
    REVOKE INSERT,UPDATE,DELETE,TRUNCATE ON idempotency_keys FROM aegis_app;
    GRANT INSERT (tenant_id,scope,idempotency_key,request_hash,status,actor_id,
                  locked_until) ON idempotency_keys TO aegis_app;
    GRANT UPDATE (status,response_code,response_body,locked_until,completed_at,
                  resource_type,resource_id) ON idempotency_keys TO aegis_app;
    REVOKE UPDATE, DELETE, TRUNCATE ON order_reservation_events FROM aegis_app;
    REVOKE UPDATE, DELETE, TRUNCATE ON order_items FROM aegis_app;
    REVOKE UPDATE, DELETE, TRUNCATE ON order_stock_reservations FROM aegis_app;
    REVOKE UPDATE, DELETE, TRUNCATE ON order_purchase_limit_reservations FROM aegis_app;
    REVOKE DELETE, TRUNCATE ON payment_webhook_receipts FROM aegis_app;
    REVOKE UPDATE ON payment_webhook_receipts FROM aegis_app;
    GRANT UPDATE (parse_status, signature_status, parse_error, parsed_at,
                  signature_checked_at, parsed_provider_event_id, payment_event_id)
      ON payment_webhook_receipts TO aegis_app;
    REVOKE UPDATE, DELETE, TRUNCATE ON payment_events FROM aegis_app;
    GRANT UPDATE (processing_status,processing_error,processed_at)
      ON payment_events TO aegis_app;
    REVOKE UPDATE, DELETE, TRUNCATE
      ON orders,payment_intents,payments,refunds,refund_requests,invoices
      FROM aegis_app;
    GRANT UPDATE (status,paid_amount,paid_at,fulfilled_at,subscription_id)
      ON orders TO aegis_app;
    GRANT UPDATE (status,provider_ref) ON payment_intents TO aegis_app;
    REVOKE UPDATE,DELETE,TRUNCATE ON order_reservations,coupon_redemptions,
      balance_holds,late_payment_cases FROM aegis_app;
    GRANT UPDATE (state,captured_at,released_at,release_reason)
      ON order_reservations TO aegis_app;
    GRANT UPDATE (status,captured_at,released_at,reverted_at,revert_reason)
      ON coupon_redemptions TO aegis_app;
    GRANT UPDATE (status,capture_txn_id,release_txn_id,captured_at,released_at)
      ON balance_holds TO aegis_app;
    GRANT UPDATE (status,refund_id,refund_txn_id,resolution_reason,resolved_at)
      ON late_payment_cases TO aegis_app;
    REVOKE INSERT ON orders,order_items,payment_intents,payments,payment_events,
      payment_webhook_receipts,refunds,refund_requests,invoices,order_reservations,
      order_stock_reservations,order_purchase_limit_reservations,
      coupon_redemptions,balance_holds,late_payment_cases,
      order_reservation_events,ledger_accounts,ledger_transactions,
      ledger_entries FROM aegis_app;
    GRANT INSERT (tenant_id,order_no,user_id,kind,status,currency,
                  subtotal_amount,discount_amount,tax_amount,total_amount,
                  balance_applied,payable_amount,expires_at,coupon_id,
                  subscription_id,idempotency_key_id) ON orders TO aegis_app;
    GRANT INSERT (tenant_id,order_id,product_id,price_id,plan_id,plan_version_id,
                  snapshot_product_name,snapshot_plan_name,snapshot_plan_version,
                  snapshot_interval,snapshot_interval_count,snapshot_entitlements,
                  snapshot_quotas,quantity,unit_amount,line_amount,currency)
      ON order_items TO aegis_app;
    GRANT INSERT (tenant_id,order_id,provider_id,currency,amount,status,
                  provider_ref,action_payload,expires_at)
      ON payment_intents TO aegis_app;
    GRANT INSERT (tenant_id,order_id,provider_id,provider_payment_id,
                  payment_intent_id,currency,amount,fee_amount,status)
      ON payments TO aegis_app;
    GRANT INSERT (tenant_id,provider_id,provider_event_id,event_type,
                  provider_payment_id,raw_payload,signature_verified)
      ON payment_events TO aegis_app;
    GRANT INSERT (tenant_id,provider_id,provider_code,http_method,request_path,
                  query_string,raw_headers,raw_body,source_ip)
      ON payment_webhook_receipts TO aegis_app;
    GRANT INSERT (tenant_id,order_id,user_id,expires_at)
      ON order_reservations TO aegis_app;
    GRANT INSERT (tenant_id,reservation_id,order_id,plan_id,quantity)
      ON order_stock_reservations TO aegis_app;
    GRANT INSERT (tenant_id,reservation_id,order_id,plan_id,user_id,quantity)
      ON order_purchase_limit_reservations TO aegis_app;
    GRANT INSERT (tenant_id,coupon_id,user_id,order_id,discount_amount,currency,
                  reservation_id) ON coupon_redemptions TO aegis_app;
    GRANT INSERT (tenant_id,reservation_id,order_id,user_id,currency,amount,
                  available_account_id,hold_account_id,hold_txn_id)
      ON balance_holds TO aegis_app;
    GRANT INSERT (tenant_id,order_id,payment_id,payment_event_id,amount,currency,
                  suspense_txn_id) ON late_payment_cases TO aegis_app;
    GRANT INSERT (tenant_id,reservation_id,order_id,from_state,to_state,event_kind,
                  business_request_id,actor_kind,actor_id,reason,metadata)
      ON order_reservation_events TO aegis_app;
    GRANT INSERT (tenant_id,account_type,normal_balance,currency,owner_user_id,
                  owner_ref) ON ledger_accounts TO aegis_app;
    GRANT INSERT (tenant_id,kind,currency,source_type,source_id,memo,actor_kind,
                  actor_id) ON ledger_transactions TO aegis_app;
    GRANT INSERT (tenant_id,transaction_id,account_id,direction,amount,currency,
                  description) ON ledger_entries TO aegis_app;
    REVOKE INSERT,UPDATE,DELETE,TRUNCATE
      ON refunds,refund_requests,invoices FROM aegis_app;
    REVOKE UPDATE,DELETE,TRUNCATE ON ledger_accounts FROM aegis_app;
    GRANT UPDATE (updated_at) ON ledger_accounts TO aegis_app;
    REVOKE INSERT, UPDATE, DELETE ON order_transitions FROM aegis_app;
  END IF;
END $$;
-- +goose StatementEnd

COMMENT ON TABLE order_reservations IS
  'Single monotonic lifecycle for stock, purchase-limit, coupon, and balance holds.';
COMMENT ON TABLE late_payment_cases IS
  'Money received after reservation release; quarantine in suspense and never auto-fulfil.';

-- +goose Down

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

-- +goose StatementBegin
DO $$
DECLARE
  v_installed timestamptz;
  v_balance_postings boolean;
BEGIN
  SELECT installed_at, created_balance_postings
    INTO v_installed, v_balance_postings
    FROM app.order_reservations_00036_meta WHERE singleton;

  IF v_balance_postings THEN
    RAISE EXCEPTION
      'cannot rollback 00036: migration created durable ledger postings for legacy balance holds';
  END IF;

  IF EXISTS (
       SELECT 1 FROM orders o
        WHERE NOT EXISTS (
          SELECT 1 FROM app.order_reservations_00036_orders b
           WHERE b.order_id = o.id AND b.tenant_id = o.tenant_id
        )
     )
     OR EXISTS (
       SELECT 1 FROM app.order_reservations_00036_post_orders b
       LEFT JOIN orders o ON o.tenant_id=b.tenant_id AND o.id=b.order_id
       WHERE o.id IS NULL OR o.status IS DISTINCT FROM b.status
          OR o.updated_at IS DISTINCT FROM b.updated_at
          OR o.cancelled_at IS DISTINCT FROM b.cancelled_at
          OR o.expired_at IS DISTINCT FROM b.expired_at
          OR o.cancel_reason IS DISTINCT FROM b.cancel_reason
          OR o.state_version IS DISTINCT FROM b.state_version
          OR o.idempotency_key_id IS DISTINCT FROM b.idempotency_key_id
     )
     OR EXISTS (
       SELECT 1 FROM plans p FULL JOIN app.order_reservations_00036_post_plans b
         ON b.tenant_id=p.tenant_id AND b.plan_id=p.id
       WHERE p.id IS NULL OR b.plan_id IS NULL
          OR p.stock_reserved IS DISTINCT FROM b.stock_reserved
          OR p.stock_sold IS DISTINCT FROM b.stock_sold
     )
     OR EXISTS (
       SELECT 1 FROM plan_purchase_counters pc
       FULL JOIN app.order_reservations_00036_post_purchase b
         ON b.tenant_id=pc.tenant_id AND b.plan_id=pc.plan_id AND b.user_id=pc.user_id
       WHERE pc.plan_id IS NULL OR b.plan_id IS NULL
          OR pc.purchased IS DISTINCT FROM b.purchased
          OR pc.reserved IS DISTINCT FROM b.reserved
     )
     OR EXISTS (
       SELECT 1 FROM coupons c FULL JOIN app.order_reservations_00036_post_coupons b
         ON b.tenant_id=c.tenant_id AND b.coupon_id=c.id
       WHERE c.id IS NULL OR b.coupon_id IS NULL
          OR c.redeemed_count IS DISTINCT FROM b.redeemed_count
          OR c.reserved_count IS DISTINCT FROM b.reserved_count
     )
     OR EXISTS (
       SELECT 1 FROM refunds r
       FULL JOIN app.order_reservations_00036_refunds b ON b.refund_id=r.id
       WHERE r.id IS NULL OR b.refund_id IS NULL
          OR (to_jsonb(r)-ARRAY['refund_request_id','source_kind','balance_hold_id','ledger_txn_id'])
             IS DISTINCT FROM b.row_data
          OR r.source_kind<>'payment' OR r.payment_id IS NULL
          OR r.balance_hold_id IS NOT NULL OR r.ledger_txn_id IS NOT NULL
     )
     OR EXISTS (
       SELECT 1 FROM refund_requests rr
       FULL JOIN app.order_reservations_00036_refund_requests b
         ON b.request_id=rr.id
       WHERE rr.id IS NULL OR b.request_id IS NULL OR to_jsonb(rr)<>b.row_data
     )
     OR EXISTS (
       SELECT 1 FROM idempotency_keys k
       FULL JOIN app.order_reservations_00036_refund_idempotency b
         ON b.key_id=k.id
       WHERE k.id IS NULL OR b.key_id IS NULL OR to_jsonb(k)<>b.row_data
     )
     OR EXISTS (SELECT 1 FROM order_reservations WHERE NOT legacy_backfill)
     OR EXISTS (SELECT 1 FROM order_stock_reservations WHERE NOT legacy_backfill)
     OR EXISTS (SELECT 1 FROM order_purchase_limit_reservations WHERE NOT legacy_backfill)
     OR EXISTS (SELECT 1 FROM coupon_redemptions WHERE NOT legacy_backfill)
     OR EXISTS (SELECT 1 FROM balance_holds WHERE NOT legacy_backfill)
     OR EXISTS (SELECT 1 FROM order_reservation_events WHERE NOT legacy_backfill)
     OR EXISTS (SELECT 1 FROM payment_webhook_receipts)
     OR EXISTS (SELECT 1 FROM late_payment_cases) THEN
    RAISE EXCEPTION
      'cannot rollback 00036 after order reservation domain writes';
  END IF;
END;
$$;
-- +goose StatementEnd

ALTER TABLE commission_entries DROP CONSTRAINT commission_entries_payment_tenant_fk;
ALTER TABLE refunds DROP CONSTRAINT refunds_balance_hold_tenant_fk;
ALTER TABLE refunds DROP CONSTRAINT refunds_payment_tenant_fk;
ALTER TABLE refunds DROP CONSTRAINT refunds_ledger_txn_tenant_fk;
ALTER TABLE refunds DROP CONSTRAINT refunds_request_tenant_fk;
ALTER TABLE refunds DROP CONSTRAINT refunds_requested_by_tenant_fk;
ALTER TABLE refunds ADD CONSTRAINT refunds_requested_by_fkey
  FOREIGN KEY (requested_by) REFERENCES users(id) ON DELETE SET NULL;
ALTER TABLE payments DROP CONSTRAINT payments_intent_tenant_fk;
ALTER TABLE order_items DROP CONSTRAINT order_items_plan_tenant_fk;
ALTER TABLE orders DROP CONSTRAINT orders_subscription_tenant_fk;
ALTER TABLE orders DROP CONSTRAINT orders_coupon_tenant_fk;
ALTER TABLE tickets DROP CONSTRAINT tickets_related_order_tenant_fk;
ALTER TABLE commission_entries DROP CONSTRAINT commission_entries_order_tenant_fk;
ALTER TABLE gift_codes DROP CONSTRAINT gift_codes_order_tenant_fk;
ALTER TABLE coupon_redemptions DROP CONSTRAINT coupon_redemptions_order_tenant_fk;
ALTER TABLE subscription_events DROP CONSTRAINT subscription_events_order_tenant_fk;
ALTER TABLE invoices DROP CONSTRAINT invoices_order_tenant_fk;
ALTER TABLE refunds DROP CONSTRAINT refunds_order_tenant_fk;
ALTER TABLE payments DROP CONSTRAINT payments_order_tenant_fk;
ALTER TABLE payment_intents DROP CONSTRAINT payment_intents_order_tenant_fk;
ALTER TABLE order_items DROP CONSTRAINT order_items_order_tenant_fk;
ALTER TABLE payment_intents
  DROP CONSTRAINT payment_intents_business_identity_key;

DROP TRIGGER IF EXISTS trg_late_payment_cases_updated_at ON late_payment_cases;
DROP TRIGGER IF EXISTS trg_coupons_reservation_commit ON coupons;
DROP TRIGGER IF EXISTS trg_purchase_counters_commit ON plan_purchase_counters;
DROP TRIGGER IF EXISTS trg_plans_reservation_commit ON plans;
DROP TRIGGER IF EXISTS trg_order_items_reservation_commit ON order_items;
DROP TRIGGER IF EXISTS trg_order_items_immutable ON order_items;
DROP TRIGGER IF EXISTS trg_orders_reservation_commit ON orders;
DROP TRIGGER IF EXISTS trg_reservations_commit ON order_reservations;
DROP TRIGGER IF EXISTS trg_stock_reservations_commit ON order_stock_reservations;
DROP TRIGGER IF EXISTS trg_purchase_reservations_commit ON order_purchase_limit_reservations;
DROP TRIGGER IF EXISTS trg_coupon_reservations_commit ON coupon_redemptions;
DROP TRIGGER IF EXISTS trg_balance_holds_commit ON balance_holds;
DROP TRIGGER IF EXISTS trg_reservation_events_commit ON order_reservation_events;
DROP TRIGGER IF EXISTS trg_payment_webhook_receipts_no_delete ON payment_webhook_receipts;
DROP TRIGGER IF EXISTS trg_payment_webhook_receipts_guard ON payment_webhook_receipts;
DROP TRIGGER IF EXISTS trg_payment_events_evidence_guard ON payment_events;
DROP TRIGGER IF EXISTS trg_refund_requests_commit ON refund_requests;
DROP TRIGGER IF EXISTS trg_refund_legs_request_commit ON refunds;
DROP TRIGGER IF EXISTS trg_refund_idempotency_commit ON idempotency_keys;
DROP TRIGGER IF EXISTS trg_payments_refunds_consistency_commit ON payments;
DROP TRIGGER IF EXISTS trg_orders_refunds_consistency_commit ON orders;
DROP TRIGGER IF EXISTS trg_refunds_consistency_commit ON refunds;
DROP TRIGGER IF EXISTS trg_refund_requests_no_delete ON refund_requests;
DROP TRIGGER IF EXISTS zz_refund_requests_guard ON refund_requests;
DROP TRIGGER IF EXISTS trg_refunds_no_delete ON refunds;
DROP TRIGGER IF EXISTS trg_refunds_source_guard ON refunds;
DROP TRIGGER IF EXISTS zz_refunds_evidence_guard ON refunds;
DROP TRIGGER IF EXISTS trg_payments_evidence_guard ON payments;
DROP TRIGGER IF EXISTS trg_idempotency_no_delete ON idempotency_keys;
DROP TRIGGER IF EXISTS zz_idempotency_evidence_guard ON idempotency_keys;
DROP TRIGGER IF EXISTS trg_ledger_accounts_evidence_guard ON ledger_accounts;
DROP TRIGGER IF EXISTS trg_refund_ledger_entries_finalized ON ledger_entries;
DROP TRIGGER IF EXISTS trg_payments_initial_state_guard ON payments;
DROP TRIGGER IF EXISTS trg_payment_intents_initial_state_guard ON payment_intents;
DROP TRIGGER IF EXISTS trg_orders_initial_state_guard ON orders;
DROP TRIGGER IF EXISTS trg_payment_intents_evidence_guard ON payment_intents;
DROP TRIGGER IF EXISTS trg_invoices_evidence_guard ON invoices;
DROP TRIGGER IF EXISTS trg_late_payment_cases_guard ON late_payment_cases;
DROP TRIGGER IF EXISTS trg_balance_holds_updated_at ON balance_holds;
DROP TRIGGER IF EXISTS trg_order_reservations_updated_at ON order_reservations;
DROP TRIGGER IF EXISTS trg_balance_holds_state_guard ON balance_holds;
DROP TRIGGER IF EXISTS trg_coupon_redemptions_state_guard ON coupon_redemptions;
DROP TRIGGER IF EXISTS trg_order_reservations_state_guard ON order_reservations;
DROP FUNCTION IF EXISTS app.guard_balance_hold_transition();
DROP FUNCTION IF EXISTS app.guard_coupon_reservation_transition();
DROP FUNCTION IF EXISTS app.guard_order_reservation_transition();
DROP FUNCTION IF EXISTS app.guard_payment_webhook_receipt();
DROP FUNCTION IF EXISTS app.guard_payment_event_evidence();
DROP FUNCTION IF EXISTS app.assert_refund_request_trigger();
DROP FUNCTION IF EXISTS app.assert_refund_request(uuid,uuid);
DROP FUNCTION IF EXISTS app.assert_refund_idempotency_key_trigger();
DROP FUNCTION IF EXISTS app.assert_refund_idempotency_key(uuid,uuid);
DROP FUNCTION IF EXISTS app.guard_refund_request();
DROP FUNCTION IF EXISTS app.assert_refund_consistency_trigger();
DROP FUNCTION IF EXISTS app.assert_refund_consistency(uuid,uuid,uuid);
DROP FUNCTION IF EXISTS app.guard_refund_source();
DROP FUNCTION IF EXISTS app.guard_refund_evidence();
DROP FUNCTION IF EXISTS app.guard_idempotency_evidence();
DROP FUNCTION IF EXISTS app.guard_payment_evidence();
DROP FUNCTION IF EXISTS app.guard_payment_intent();
DROP FUNCTION IF EXISTS app.guard_invoice_evidence();
DROP FUNCTION IF EXISTS app.guard_finance_initial_state();
DROP FUNCTION IF EXISTS app.guard_ledger_account_evidence();
DROP FUNCTION IF EXISTS app.guard_finalized_refund_ledger_entry();
DROP FUNCTION IF EXISTS app.guard_late_payment_case();
DROP FUNCTION IF EXISTS app.assert_order_reservation_invariants();
DROP FUNCTION IF EXISTS app.assert_order_reservation_target(text, jsonb, jsonb);
DROP FUNCTION IF EXISTS app.guard_order_item_immutable();

ALTER FUNCTION app.apply_ledger_entry_to_account() SECURITY INVOKER;
ALTER FUNCTION app.apply_ledger_entry_to_account() RESET search_path;

-- Restore exact legacy aggregates before removing the reservation detail.
UPDATE plans p SET stock_reserved = b.stock_reserved
  FROM app.order_reservations_00036_plans b WHERE b.plan_id = p.id;
UPDATE plan_purchase_counters pc SET purchased = b.purchased, updated_at = now()
  FROM app.order_reservations_00036_purchase b
 WHERE b.tenant_id = pc.tenant_id AND b.plan_id = pc.plan_id AND b.user_id = pc.user_id;
UPDATE coupons c SET redeemed_count = b.redeemed_count, updated_at = now()
  FROM app.order_reservations_00036_coupons b WHERE b.coupon_id = c.id;

DROP TABLE order_reservation_events;
DROP TABLE late_payment_cases;
DROP TABLE balance_holds;

ALTER TABLE coupon_redemptions
  DROP CONSTRAINT coupon_redemptions_state_times,
  DROP CONSTRAINT coupon_redemptions_status_check,
  DROP CONSTRAINT coupon_redemptions_coupon_tenant_fk,
  DROP CONSTRAINT coupon_redemptions_parent_fk,
  DROP COLUMN legacy_backfill,
  DROP COLUMN released_at,
  DROP COLUMN captured_at,
  DROP COLUMN status,
  DROP COLUMN reservation_id;
DROP INDEX IF EXISTS coupon_redemptions_user_active_idx;

DROP TABLE order_purchase_limit_reservations;
DROP TABLE order_stock_reservations;
DROP TABLE order_reservations;

DROP TABLE payment_webhook_receipts;

ALTER TABLE ledger_entries
  DROP CONSTRAINT ledger_entries_account_tenant_currency_fk,
  DROP CONSTRAINT ledger_entries_transaction_tenant_currency_fk;
ALTER TABLE ledger_transactions
  DROP CONSTRAINT ledger_transactions_tenant_id_currency_key;
DROP INDEX IF EXISTS ledger_transactions_order_balance_action_unique;
DROP INDEX IF EXISTS ledger_transactions_refund_action_unique;
ALTER TABLE payment_events
  DROP CONSTRAINT payment_events_tenant_id_id_key;
ALTER TABLE payments
  DROP CONSTRAINT payments_tenant_id_id_order_currency_key;
ALTER TABLE refunds
  DROP CONSTRAINT refunds_tenant_id_id_order_currency_key;
DROP INDEX IF EXISTS idx_refunds_balance_hold;
DROP INDEX IF EXISTS refunds_request_source_unique;
ALTER TABLE refunds
  DROP CONSTRAINT refunds_source_exactly_one,
  DROP CONSTRAINT refunds_source_kind_check,
  ALTER COLUMN payment_id SET NOT NULL,
  DROP COLUMN ledger_txn_id,
  DROP COLUMN balance_hold_id,
  DROP COLUMN source_kind,
  DROP COLUMN refund_request_id;
DROP TABLE refund_requests;
DELETE FROM idempotency_keys k
 USING app.order_reservations_00036_refund_idempotency b
 WHERE b.key_id=k.id AND b.migration_created;
ALTER TABLE orders DROP CONSTRAINT orders_tenant_id_id_currency_key;
ALTER TABLE ledger_accounts
  DROP CONSTRAINT ledger_accounts_tenant_id_currency_key,
  DROP CONSTRAINT ledger_accounts_account_type_check,
  ADD CONSTRAINT ledger_accounts_account_type_check CHECK (
    account_type IN ('user_balance', 'user_commission_pending',
                     'user_commission_available', 'channel_cash',
                     'platform_revenue', 'platform_fee_expense',
                     'refund_payable', 'dispute_hold', 'suspense')
  );

ALTER TABLE coupons
  DROP CONSTRAINT coupons_capacity,
  DROP COLUMN reserved_count,
  ADD CONSTRAINT coupons_not_oversold
    CHECK (max_redemptions IS NULL OR redeemed_count <= max_redemptions);

ALTER TABLE plan_purchase_counters DROP COLUMN reserved;

ALTER TABLE plans
  DROP CONSTRAINT plans_stock_capacity,
  DROP COLUMN stock_sold,
  ADD CONSTRAINT plans_stock_not_oversold
    CHECK (stock_total IS NULL OR stock_reserved <= stock_total);

DROP TRIGGER IF EXISTS trg_orders_business_request ON orders;
DROP TRIGGER IF EXISTS trg_orders_state_guard ON orders;
DROP FUNCTION IF EXISTS app.guard_order_business_request();
DROP FUNCTION IF EXISTS app.guard_order_transition();
DROP TABLE order_transitions;
DROP INDEX IF EXISTS orders_one_active_renewal;
DROP INDEX IF EXISTS orders_idempotency_record_unique;
DROP INDEX IF EXISTS orders_business_request_unique;
UPDATE orders o
   SET status=b.status, updated_at=b.updated_at, cancelled_at=b.cancelled_at
  FROM app.order_reservations_00036_orders b
 WHERE b.tenant_id=o.tenant_id AND b.order_id=o.id;
ALTER TABLE orders
  DROP CONSTRAINT orders_idempotency_key_fk,
  DROP COLUMN state_version,
  DROP COLUMN cancel_reason,
  DROP COLUMN expired_at,
  DROP COLUMN idempotency_key_id,
  DROP COLUMN business_request_id;
-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'aegis_app') THEN
    -- Migration 00011 granted table-level INSERT/UPDATE/DELETE.  Remove the
    -- migration-36 column ACLs before restoring that exact pre-Up capability.
    REVOKE INSERT (tenant_id,scope,idempotency_key,request_hash,status,actor_id,
                   locked_until) ON idempotency_keys FROM aegis_app;
    REVOKE UPDATE (status,response_code,response_body,locked_until,completed_at,
                   resource_type,resource_id) ON idempotency_keys FROM aegis_app;
    GRANT INSERT,UPDATE,DELETE ON idempotency_keys TO aegis_app;
  END IF;
END $$;
-- +goose StatementEnd
ALTER TABLE idempotency_keys
  DROP CONSTRAINT idempotency_keys_actor_tenant_fk,
  DROP CONSTRAINT idempotency_keys_evidence_shape,
  DROP CONSTRAINT idempotency_keys_resource_pair,
  DROP CONSTRAINT idempotency_keys_tenant_id_id_key,
  DROP COLUMN actor_id;

DROP TABLE app.order_reservations_00036_coupons;
DROP TABLE app.order_reservations_00036_post_orders;
DROP TABLE app.order_reservations_00036_post_coupons;
DROP TABLE app.order_reservations_00036_post_purchase;
DROP TABLE app.order_reservations_00036_post_plans;
DROP TABLE app.order_reservations_00036_orders;
DROP TABLE app.order_reservations_00036_refunds;
DROP TABLE app.order_reservations_00036_refund_requests;
DROP TABLE app.order_reservations_00036_refund_idempotency;
DROP TABLE app.order_reservations_00036_purchase;
DROP TABLE app.order_reservations_00036_plans;
DROP TABLE app.order_reservations_00036_meta;

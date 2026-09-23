-- +goose Up

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

-- This slice starts from an unused quarantine surface. Refuse to guess how
-- older rows were posted: they need an explicit reconciliation migration.
-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM public.late_payment_cases)
     OR EXISTS (
       SELECT 1 FROM public.ledger_transactions
        WHERE kind='late_payment_suspense'
     ) THEN
    RAISE EXCEPTION
      'cannot install 00040: pre-existing late-payment evidence requires explicit reconciliation';
  END IF;
END $$;
-- +goose StatementEnd

CREATE TABLE app.order_release_00040_meta (
  singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
  installed_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  used boolean NOT NULL DEFAULT false
);
INSERT INTO app.order_release_00040_meta(singleton) VALUES (true);
REVOKE ALL ON app.order_release_00040_meta FROM PUBLIC;

ALTER TABLE public.late_payment_cases
  ADD COLUMN case_kind text NOT NULL DEFAULT 'released_order'
    CHECK (case_kind IN ('released_order','excess_capture')),
  ADD CONSTRAINT late_payment_cases_suspense_txn_unique
  UNIQUE (tenant_id, suspense_txn_id);

CREATE UNIQUE INDEX ledger_transactions_late_payment_unique
  ON public.ledger_transactions (tenant_id, source_id)
  WHERE source_type='payment' AND kind='late_payment_suspense';

-- A release may win while a provider is processing. The order row is the
-- serialization point; after it is locked, that intent must be allowed to
-- enter the same terminal state as the order.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.guard_payment_intent() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
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
    OR (OLD.status='processing' AND NEW.status IN ('succeeded','failed','cancelled','expired'))
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
-- +goose StatementEnd

-- Strictly prove that money received after release is represented by one
-- payment, one quarantine transaction and one case. The suspense credit is
-- the gross payment; channel cash plus fee expense are the matching debits.
-- +goose StatementBegin
CREATE FUNCTION app.assert_late_payment_suspense(
  p_tenant uuid, p_payment uuid, p_txn uuid
) RETURNS void
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
  v_case public.late_payment_cases%ROWTYPE;
  v_order_status text;
  v_order_business uuid;
  v_order_user uuid;
  v_order_paid_at timestamptz;
  v_payment_amount bigint;
  v_payment_fee bigint;
  v_payment_currency text;
  v_payment_status text;
  v_paid_at timestamptz;
  v_provider uuid;
  v_provider_ref text;
  v_provider_code text;
  v_reservation uuid;
  v_reservation_state text;
  v_released_at timestamptz;
  v_txn_kind text;
  v_txn_currency text;
  v_txn_source_type text;
  v_txn_source uuid;
  v_txn_actor text;
  v_entry_count integer;
  v_expected_count integer;
  v_net bigint;
  v_terminal_events integer;
  v_event_matches integer;
  v_prior_payments integer;
BEGIN
  SELECT * INTO STRICT v_case
    FROM public.late_payment_cases c
   WHERE c.tenant_id=p_tenant AND c.payment_id=p_payment
     AND c.suspense_txn_id=p_txn;

  SELECT p.amount,p.fee_amount,p.currency::text,p.status,p.paid_at,
         p.provider_id,p.provider_payment_id,pp.code,
         o.status,o.business_request_id,o.user_id,o.paid_at,
         r.id,r.state,r.released_at
    INTO STRICT v_payment_amount,v_payment_fee,v_payment_currency,v_payment_status,
         v_paid_at,v_provider,v_provider_ref,v_provider_code,
         v_order_status,v_order_business,v_order_user,v_order_paid_at,
         v_reservation,v_reservation_state,v_released_at
    FROM public.payments p
    JOIN public.payment_providers pp
      ON pp.tenant_id=p.tenant_id AND pp.id=p.provider_id
    JOIN public.orders o
      ON o.tenant_id=p.tenant_id AND o.id=p.order_id
    JOIN public.order_reservations r
      ON r.tenant_id=o.tenant_id AND r.order_id=o.id
   WHERE p.tenant_id=p_tenant AND p.id=p_payment
     AND p.order_id=v_case.order_id AND p.currency=v_case.currency;

  IF v_payment_amount<>v_case.amount OR v_payment_currency<>v_case.currency::text
     OR v_payment_status NOT IN ('succeeded','partially_refunded','refunded') THEN
    RAISE EXCEPTION 'late payment case does not match successful payment evidence'
      USING ERRCODE='check_violation';
  END IF;
  IF v_case.case_kind='released_order' THEN
    IF v_order_status NOT IN ('cancelled','expired')
       OR v_reservation_state<>'released' OR v_released_at IS NULL
       OR v_paid_at<v_released_at OR v_case.received_at<v_released_at THEN
      RAISE EXCEPTION 'late payment was not received after an exact order release'
        USING ERRCODE='check_violation';
    END IF;

    SELECT count(*) INTO v_terminal_events
      FROM public.order_reservation_events e
     WHERE e.tenant_id=p_tenant AND e.reservation_id=v_reservation
       AND e.order_id=v_case.order_id AND e.from_state='held'
       AND e.to_state='released' AND e.business_request_id=v_order_business
       AND ((v_order_status='cancelled' AND e.event_kind='cancel'
             AND e.actor_kind='user' AND e.actor_id=v_order_user
             AND e.reason='user_cancelled')
         OR (v_order_status='expired' AND e.event_kind='expire'
             AND e.actor_kind='system' AND e.actor_id IS NULL
             AND e.reason='reservation_expired')
         OR (e.event_kind='backfill' AND e.legacy_backfill
             AND e.actor_kind='system' AND e.actor_id IS NULL
             AND e.reason='schema_00036_terminal_backfill'));
    IF v_terminal_events<>1 THEN
      RAISE EXCEPTION 'late payment order lacks its exact terminal reservation event'
        USING ERRCODE='check_violation';
    END IF;
  ELSIF v_case.case_kind='excess_capture' THEN
    IF v_order_status NOT IN ('paid','fulfilled') OR v_reservation_state<>'captured'
       OR v_order_paid_at IS NULL OR v_paid_at<v_order_paid_at THEN
      RAISE EXCEPTION 'excess capture requires an already paid captured order'
        USING ERRCODE='check_violation';
    END IF;
    SELECT count(*) INTO v_prior_payments
      FROM public.payments p
     WHERE p.tenant_id=p_tenant AND p.order_id=v_case.order_id
       AND p.id<>p_payment
       AND p.status IN ('succeeded','partially_refunded','refunded')
       AND (p.provider_id<>v_provider OR p.provider_payment_id<>v_provider_ref)
       AND p.paid_at<=v_order_paid_at;
    IF v_prior_payments<1 THEN
      RAISE EXCEPTION 'excess capture lacks a distinct prior successful payment'
        USING ERRCODE='check_violation';
    END IF;
  ELSE
    RAISE EXCEPTION 'unsupported late payment case kind %',v_case.case_kind
      USING ERRCODE='check_violation';
  END IF;

  IF v_case.payment_event_id IS NULL THEN
    RAISE EXCEPTION 'automatic payment quarantine requires provider event evidence'
      USING ERRCODE='check_violation';
  END IF;
  SELECT count(*) INTO v_event_matches
    FROM public.payment_events e
   WHERE e.tenant_id=p_tenant AND e.id=v_case.payment_event_id
     AND e.provider_id=v_provider AND e.provider_payment_id=v_provider_ref
     AND e.event_type='payment.succeeded'
     AND e.signature_verified AND e.processing_status='processed'
     AND e.processed_at IS NOT NULL AND e.processing_error IS NULL;
  IF v_event_matches<>1 THEN
    RAISE EXCEPTION 'late payment case event does not match processed provider evidence'
      USING ERRCODE='check_violation';
  END IF;

  SELECT t.kind,t.currency::text,t.source_type,t.source_id,t.actor_kind
    INTO STRICT v_txn_kind,v_txn_currency,v_txn_source_type,v_txn_source,v_txn_actor
    FROM public.ledger_transactions t
   WHERE t.tenant_id=p_tenant AND t.id=p_txn;
  IF v_txn_kind<>'late_payment_suspense' OR v_txn_currency<>v_case.currency::text
     OR v_txn_source_type<>'payment' OR v_txn_source<>p_payment
     OR v_txn_actor<>'system' THEN
    RAISE EXCEPTION 'late payment suspense transaction identity is invalid'
      USING ERRCODE='check_violation';
  END IF;

  v_net := v_payment_amount-v_payment_fee;
  v_expected_count := 1 + CASE WHEN v_net>0 THEN 1 ELSE 0 END
                         + CASE WHEN v_payment_fee>0 THEN 1 ELSE 0 END;
  SELECT count(*) INTO v_entry_count
    FROM public.ledger_entries e WHERE e.transaction_id=p_txn;
  IF v_entry_count<>v_expected_count
     OR NOT EXISTS (
       SELECT 1 FROM public.ledger_entries e
       JOIN public.ledger_accounts a
         ON a.tenant_id=e.tenant_id AND a.id=e.account_id
      WHERE e.transaction_id=p_txn AND e.tenant_id=p_tenant
        AND e.direction='credit' AND e.amount=v_payment_amount
        AND e.currency=v_case.currency
        AND a.account_type='late_payment_suspense'
        AND a.normal_balance='credit' AND a.owner_user_id IS NULL
        AND a.owner_ref='main'
     )
     OR (v_net>0 AND NOT EXISTS (
       SELECT 1 FROM public.ledger_entries e
       JOIN public.ledger_accounts a
         ON a.tenant_id=e.tenant_id AND a.id=e.account_id
      WHERE e.transaction_id=p_txn AND e.tenant_id=p_tenant
        AND e.direction='debit' AND e.amount=v_net AND e.currency=v_case.currency
        AND a.account_type='channel_cash' AND a.normal_balance='debit'
        AND a.owner_user_id IS NULL AND a.owner_ref=v_provider_code
     ))
     OR (v_payment_fee>0 AND NOT EXISTS (
       SELECT 1 FROM public.ledger_entries e
       JOIN public.ledger_accounts a
         ON a.tenant_id=e.tenant_id AND a.id=e.account_id
      WHERE e.transaction_id=p_txn AND e.tenant_id=p_tenant
        AND e.direction='debit' AND e.amount=v_payment_fee
        AND e.currency=v_case.currency
        AND a.account_type='platform_fee_expense' AND a.normal_balance='debit'
        AND a.owner_user_id IS NULL AND a.owner_ref=v_provider_code
     )) THEN
    RAISE EXCEPTION 'late payment suspense ledger shape is invalid'
      USING ERRCODE='check_violation';
  END IF;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.assert_late_payment_suspense_trigger() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
  v_tenant uuid;
  v_payment uuid;
  v_txn uuid;
BEGIN
  IF TG_TABLE_NAME='late_payment_cases' THEN
    v_tenant := NEW.tenant_id;
    v_payment := NEW.payment_id;
    v_txn := NEW.suspense_txn_id;
  ELSIF TG_TABLE_NAME='ledger_transactions' THEN
    IF NEW.kind<>'late_payment_suspense' OR NEW.source_type<>'payment' THEN
      RETURN NULL;
    END IF;
    v_tenant := NEW.tenant_id;
    v_payment := NEW.source_id;
    v_txn := NEW.id;
  ELSE
    SELECT t.tenant_id,t.source_id,t.id
      INTO v_tenant,v_payment,v_txn
      FROM public.ledger_transactions t
     WHERE t.id=NEW.transaction_id
       AND t.kind='late_payment_suspense' AND t.source_type='payment';
    IF NOT FOUND THEN
      RETURN NULL;
    END IF;
  END IF;
  PERFORM app.assert_late_payment_suspense(v_tenant,v_payment,v_txn);
  RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE CONSTRAINT TRIGGER trg_late_payment_case_suspense_commit
  AFTER INSERT OR UPDATE ON public.late_payment_cases
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  EXECUTE FUNCTION app.assert_late_payment_suspense_trigger();
CREATE CONSTRAINT TRIGGER trg_late_payment_transaction_commit
  AFTER INSERT ON public.ledger_transactions
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  EXECUTE FUNCTION app.assert_late_payment_suspense_trigger();
CREATE CONSTRAINT TRIGGER trg_late_payment_entries_commit
  AFTER INSERT ON public.ledger_entries
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  EXECUTE FUNCTION app.assert_late_payment_suspense_trigger();
CREATE TRIGGER trg_late_payment_cases_no_delete
  BEFORE DELETE ON public.late_payment_cases
  FOR EACH STATEMENT EXECUTE FUNCTION app.deny_delete();

REVOKE ALL ON FUNCTION app.assert_late_payment_suspense(uuid,uuid,uuid),
  app.assert_late_payment_suspense_trigger() FROM PUBLIC, aegis_app;

-- Commission rows are a materialized business view over immutable ledger
-- postings. Reject rows without exact accrual evidence and allow the web role
-- only the pending -> available transition backed by an exact settlement.
-- +goose StatementBegin
CREATE FUNCTION app.guard_commission_entry() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
  v_rate_percent integer;
  v_referral_risk text;
BEGIN
  IF TG_OP='INSERT' THEN
    SELECT COALESCE((SELECT (s.value #>> '{}')::integer
                       FROM public.system_settings s
                      WHERE s.tenant_id=NEW.tenant_id
                        AND s.key='commission.rate_percent'),0)
      INTO v_rate_percent;
    SELECT r.risk_flag INTO v_referral_risk
      FROM public.referrals r
     WHERE r.tenant_id=NEW.tenant_id
       AND r.referrer_user_id=NEW.referrer_user_id
       AND r.referee_user_id=NEW.referee_user_id;
    IF NOT FOUND THEN
      RAISE EXCEPTION 'commission referral relationship is missing'
        USING ERRCODE='check_violation';
    END IF;
    IF NEW.status<>'pending' OR NEW.accrual_txn_id IS NULL
       OR NEW.settle_txn_id IS NOT NULL OR NEW.reversal_txn_id IS NOT NULL
       OR NEW.commission_amount<=0 OR NEW.rate_bp<=0 OR NEW.rate_bp>5000
       OR v_rate_percent<=0 OR NEW.rate_bp<>v_rate_percent*100
       OR v_referral_risk='confirmed_fraud'
       OR NEW.frozen_until IS NULL THEN
      RAISE EXCEPTION 'commission must match eligible referral settings and exact accrual evidence'
        USING ERRCODE='check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF (to_jsonb(NEW)-ARRAY['status','settle_txn_id','review_required',
                           'review_reason','updated_at']) IS DISTINCT FROM
     (to_jsonb(OLD)-ARRAY['status','settle_txn_id','review_required',
                           'review_reason','updated_at']) THEN
    RAISE EXCEPTION 'commission identity, amount and accrual evidence are immutable'
      USING ERRCODE='check_violation';
  END IF;
  IF OLD.settle_txn_id IS NOT NULL
     AND NEW.settle_txn_id IS DISTINCT FROM OLD.settle_txn_id THEN
    RAISE EXCEPTION 'commission settlement evidence is write-once'
      USING ERRCODE='check_violation';
  END IF;
  IF NEW.status IS DISTINCT FROM OLD.status AND NOT (
    OLD.status='pending' AND NEW.status='available'
    AND OLD.settle_txn_id IS NULL AND NEW.settle_txn_id IS NOT NULL
  ) THEN
    RAISE EXCEPTION 'illegal commission transition % -> %',OLD.status,NEW.status
      USING ERRCODE='check_violation';
  END IF;
  IF (NEW.review_required,NEW.review_reason) IS DISTINCT FROM
     (OLD.review_required,OLD.review_reason) AND NOT (
    OLD.status='pending' AND NEW.status='pending'
    AND NOT OLD.review_required AND NEW.review_required
    AND OLD.review_reason IS NULL AND NEW.review_reason IS NOT NULL
    AND btrim(NEW.review_reason)<>''
    AND NEW.settle_txn_id IS NOT DISTINCT FROM OLD.settle_txn_id
  ) THEN
    RAISE EXCEPTION 'illegal commission review quarantine transition'
      USING ERRCODE='check_violation';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.assert_commission_entry(p_tenant uuid, p_entry uuid)
RETURNS void
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
  v_commission public.commission_entries%ROWTYPE;
  v_order_user uuid;
  v_order_status text;
  v_order_currency text;
  v_order_total bigint;
  v_count integer;
  v_txn_kind text;
  v_txn_currency text;
  v_txn_source_type text;
  v_txn_source uuid;
  v_txn_actor text;
BEGIN
  SELECT * INTO STRICT v_commission
    FROM public.commission_entries c
   WHERE c.tenant_id=p_tenant AND c.id=p_entry;
  SELECT o.user_id,o.status,o.currency::text,o.total_amount
    INTO STRICT v_order_user,v_order_status,v_order_currency,v_order_total
    FROM public.orders o
   WHERE o.tenant_id=p_tenant AND o.id=v_commission.order_id;

  IF v_commission.status NOT IN ('pending','available')
     OR v_commission.frozen_until IS NULL
     OR v_commission.reversal_txn_id IS NOT NULL
     OR v_commission.reversed_reason IS NOT NULL
     OR (v_commission.review_required AND
         (v_commission.review_reason IS NULL OR btrim(v_commission.review_reason)=''))
     OR (NOT v_commission.review_required AND v_commission.review_reason IS NOT NULL)
     OR v_commission.referee_user_id<>v_order_user
     OR v_commission.currency::text<>v_order_currency
     OR v_commission.base_amount<>v_order_total
     OR v_commission.commission_amount
        <>v_commission.base_amount*v_commission.rate_bp/10000
     OR v_commission.commission_amount<=0
     OR v_commission.rate_bp<=0 OR v_commission.rate_bp>5000
     OR (v_commission.status='pending' AND
         v_order_status NOT IN ('paid','fulfilled','partially_refunded','refunded'))
     OR (v_commission.status='available' AND
         v_order_status NOT IN ('paid','fulfilled'))
     OR NOT EXISTS (
       SELECT 1 FROM public.referrals r
        WHERE r.tenant_id=p_tenant
          AND r.referee_user_id=v_commission.referee_user_id
          AND r.referrer_user_id=v_commission.referrer_user_id
     ) THEN
    RAISE EXCEPTION 'commission business source or amount is invalid'
      USING ERRCODE='check_violation';
  END IF;

  SELECT t.kind,t.currency::text,t.source_type,t.source_id,t.actor_kind
    INTO STRICT v_txn_kind,v_txn_currency,v_txn_source_type,v_txn_source,v_txn_actor
    FROM public.ledger_transactions t
   WHERE t.tenant_id=p_tenant AND t.id=v_commission.accrual_txn_id;
  SELECT count(*) INTO v_count FROM public.ledger_entries e
   WHERE e.transaction_id=v_commission.accrual_txn_id;
  IF v_txn_kind<>'commission_accrued'
     OR v_txn_currency<>v_commission.currency::text
     OR v_txn_source_type IS DISTINCT FROM 'order'
     OR v_txn_source IS DISTINCT FROM v_commission.order_id
     OR v_txn_actor<>'system'
     OR v_count<>2
     OR NOT EXISTS (
       SELECT 1 FROM public.ledger_entries e
       JOIN public.ledger_accounts a
         ON a.tenant_id=e.tenant_id AND a.id=e.account_id
      WHERE e.transaction_id=v_commission.accrual_txn_id
        AND e.tenant_id=p_tenant AND a.tenant_id=p_tenant
        AND e.direction='debit' AND e.amount=v_commission.commission_amount
        AND e.currency=v_commission.currency
        AND a.account_type='platform_revenue' AND a.owner_user_id IS NULL
        AND a.owner_ref='main' AND a.currency=v_commission.currency
        AND a.normal_balance='credit'
     )
     OR NOT EXISTS (
       SELECT 1 FROM public.ledger_entries e
       JOIN public.ledger_accounts a
         ON a.tenant_id=e.tenant_id AND a.id=e.account_id
      WHERE e.transaction_id=v_commission.accrual_txn_id
        AND e.tenant_id=p_tenant AND a.tenant_id=p_tenant
        AND e.direction='credit' AND e.amount=v_commission.commission_amount
        AND e.currency=v_commission.currency
        AND a.account_type='user_commission_pending'
        AND a.owner_user_id=v_commission.referrer_user_id
        AND a.currency=v_commission.currency AND a.normal_balance='credit'
     ) THEN
    RAISE EXCEPTION 'commission accrual ledger shape is invalid'
      USING ERRCODE='check_violation';
  END IF;

  IF v_commission.status='pending' AND v_commission.settle_txn_id IS NOT NULL THEN
    RAISE EXCEPTION 'pending commission cannot have settlement evidence'
      USING ERRCODE='check_violation';
  ELSIF v_commission.status='available' THEN
    IF v_commission.settle_txn_id IS NULL THEN
      RAISE EXCEPTION 'available commission requires settlement evidence'
        USING ERRCODE='check_violation';
    END IF;
    SELECT t.kind,t.currency::text,t.source_type,t.source_id,t.actor_kind
      INTO STRICT v_txn_kind,v_txn_currency,v_txn_source_type,v_txn_source,v_txn_actor
      FROM public.ledger_transactions t
     WHERE t.tenant_id=p_tenant AND t.id=v_commission.settle_txn_id;
    SELECT count(*) INTO v_count FROM public.ledger_entries e
     WHERE e.transaction_id=v_commission.settle_txn_id;
    IF v_txn_kind<>'commission_settled'
       OR v_txn_currency<>v_commission.currency::text
       OR v_txn_source_type IS DISTINCT FROM 'commission_entry'
       OR v_txn_source IS DISTINCT FROM v_commission.id
       OR v_txn_actor<>'system'
       OR v_count<>2
       OR NOT EXISTS (
         SELECT 1 FROM public.ledger_entries e
         JOIN public.ledger_accounts a
           ON a.tenant_id=e.tenant_id AND a.id=e.account_id
        WHERE e.transaction_id=v_commission.settle_txn_id
          AND e.tenant_id=p_tenant AND a.tenant_id=p_tenant
          AND e.direction='debit' AND e.amount=v_commission.commission_amount
          AND e.currency=v_commission.currency
          AND a.account_type='user_commission_pending'
          AND a.owner_user_id=v_commission.referrer_user_id
          AND a.currency=v_commission.currency AND a.normal_balance='credit'
       )
       OR NOT EXISTS (
         SELECT 1 FROM public.ledger_entries e
         JOIN public.ledger_accounts a
           ON a.tenant_id=e.tenant_id AND a.id=e.account_id
        WHERE e.transaction_id=v_commission.settle_txn_id
          AND e.tenant_id=p_tenant AND a.tenant_id=p_tenant
          AND e.direction='credit' AND e.amount=v_commission.commission_amount
          AND e.currency=v_commission.currency
          AND a.account_type='user_commission_available'
          AND a.owner_user_id=v_commission.referrer_user_id
          AND a.currency=v_commission.currency AND a.normal_balance='credit'
       ) THEN
      RAISE EXCEPTION 'commission settlement ledger shape is invalid'
        USING ERRCODE='check_violation';
    END IF;
  END IF;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.assert_commission_entry_trigger() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, public, pg_temp
AS $$
BEGIN
  PERFORM app.assert_commission_entry(NEW.tenant_id,NEW.id);
  RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- Withdrawals are financial evidence, not an ordinary mutable workflow row.
-- The application role may request, review and post payouts only through this
-- explicit state/evidence contract.
-- +goose StatementBegin
CREATE FUNCTION app.guard_withdrawal() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, app, pg_temp
AS $$
BEGIN
  IF TG_OP='INSERT' THEN
    IF NEW.status<>'requested' OR NEW.reject_reason IS NOT NULL
       OR NEW.approval_request_id IS NOT NULL OR NEW.payout_reference IS NOT NULL
       OR NEW.proof_object_key IS NOT NULL OR NEW.hold_txn_id IS NOT NULL
       OR NEW.payout_txn_id IS NOT NULL OR NEW.return_txn_id IS NOT NULL
       OR NEW.completed_at IS NOT NULL THEN
      RAISE EXCEPTION 'withdrawal must begin as an unreviewed request'
        USING ERRCODE='check_violation';
    END IF;
    IF session_user='aegis_app' AND
       (app.current_actor_id() IS NULL OR NEW.user_id<>app.current_actor_id()) THEN
      RAISE EXCEPTION 'withdrawal requester must match the current actor'
        USING ERRCODE='check_violation';
    END IF;
    RETURN NEW;
  END IF;

  IF (to_jsonb(NEW)-ARRAY['status','reject_reason','payout_reference',
                           'payout_txn_id','completed_at','updated_at']) IS DISTINCT FROM
     (to_jsonb(OLD)-ARRAY['status','reject_reason','payout_reference',
                           'payout_txn_id','completed_at','updated_at']) THEN
    RAISE EXCEPTION 'withdrawal identity, amount, payout secret and reserved evidence are immutable'
      USING ERRCODE='check_violation';
  END IF;
  IF NEW.status IS DISTINCT FROM OLD.status AND NOT (
       (OLD.status='requested' AND NEW.status IN ('reviewing','approved','rejected'))
    OR (OLD.status='reviewing' AND NEW.status IN ('approved','rejected'))
    OR (OLD.status='approved' AND NEW.status='processing')
    OR (OLD.status='processing' AND NEW.status='paid')
  ) THEN
    RAISE EXCEPTION 'illegal withdrawal transition % -> %',OLD.status,NEW.status
      USING ERRCODE='check_violation';
  END IF;
  IF OLD.payout_txn_id IS NOT NULL AND NEW.payout_txn_id IS DISTINCT FROM OLD.payout_txn_id THEN
    RAISE EXCEPTION 'withdrawal payout transaction is write-once'
      USING ERRCODE='check_violation';
  END IF;
  IF OLD.payout_reference IS NOT NULL AND
     NEW.payout_reference IS DISTINCT FROM OLD.payout_reference THEN
    RAISE EXCEPTION 'withdrawal payout reference is write-once'
      USING ERRCODE='check_violation';
  END IF;
  IF OLD.completed_at IS NOT NULL AND NEW.completed_at IS DISTINCT FROM OLD.completed_at THEN
    RAISE EXCEPTION 'withdrawal completion time is write-once'
      USING ERRCODE='check_violation';
  END IF;

  IF NEW.status IN ('requested','reviewing','approved') AND
     (NEW.reject_reason IS NOT NULL OR NEW.payout_txn_id IS NOT NULL
      OR NEW.payout_reference IS NOT NULL OR NEW.completed_at IS NOT NULL) THEN
    RAISE EXCEPTION 'pre-payout withdrawal cannot carry terminal evidence'
      USING ERRCODE='check_violation';
  ELSIF NEW.status='rejected' AND
     (nullif(btrim(NEW.reject_reason),'') IS NULL OR NEW.completed_at IS NULL
      OR NEW.payout_txn_id IS NOT NULL OR NEW.payout_reference IS NOT NULL) THEN
    RAISE EXCEPTION 'rejected withdrawal requires reason and completion only'
      USING ERRCODE='check_violation';
  ELSIF NEW.status='processing' AND
     (NEW.payout_txn_id IS NULL OR NEW.reject_reason IS NOT NULL
      OR NEW.payout_reference IS NOT NULL OR NEW.completed_at IS NOT NULL) THEN
    RAISE EXCEPTION 'processing withdrawal requires only payout transaction evidence'
      USING ERRCODE='check_violation';
  ELSIF NEW.status='paid' AND
     (NEW.payout_txn_id IS NULL OR nullif(btrim(NEW.payout_reference),'') IS NULL
      OR NEW.completed_at IS NULL OR NEW.reject_reason IS NOT NULL) THEN
    RAISE EXCEPTION 'paid withdrawal requires transaction, reference and completion evidence'
      USING ERRCODE='check_violation';
  ELSIF NEW.status IN ('failed','returned') THEN
    RAISE EXCEPTION 'failed or returned withdrawals require a future explicit return-ledger contract'
      USING ERRCODE='check_violation';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.assert_withdrawal(p_tenant uuid,p_withdrawal uuid)
RETURNS void
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
  v_withdrawal public.withdrawals%ROWTYPE;
  v_kind text;
  v_currency text;
  v_source_type text;
  v_source uuid;
  v_actor text;
  v_count integer;
BEGIN
  SELECT * INTO STRICT v_withdrawal FROM public.withdrawals w
   WHERE w.tenant_id=p_tenant AND w.id=p_withdrawal;
  IF NOT EXISTS (SELECT 1 FROM public.users u
                  WHERE u.tenant_id=p_tenant AND u.id=v_withdrawal.user_id)
     OR v_withdrawal.approval_request_id IS NOT NULL
     OR v_withdrawal.proof_object_key IS NOT NULL
     OR v_withdrawal.hold_txn_id IS NOT NULL
     OR v_withdrawal.return_txn_id IS NOT NULL THEN
    RAISE EXCEPTION 'withdrawal identity or unsupported evidence is invalid'
      USING ERRCODE='check_violation';
  END IF;

  IF v_withdrawal.status IN ('requested','reviewing','approved') THEN
    IF v_withdrawal.reject_reason IS NOT NULL OR v_withdrawal.payout_txn_id IS NOT NULL
       OR v_withdrawal.payout_reference IS NOT NULL OR v_withdrawal.completed_at IS NOT NULL THEN
      RAISE EXCEPTION 'pre-payout withdrawal carries terminal evidence'
        USING ERRCODE='check_violation';
    END IF;
  ELSIF v_withdrawal.status='rejected' THEN
    IF nullif(btrim(v_withdrawal.reject_reason),'') IS NULL
       OR v_withdrawal.completed_at IS NULL OR v_withdrawal.payout_txn_id IS NOT NULL
       OR v_withdrawal.payout_reference IS NOT NULL THEN
      RAISE EXCEPTION 'rejected withdrawal evidence is invalid'
        USING ERRCODE='check_violation';
    END IF;
  ELSIF v_withdrawal.status='processing' THEN
    IF v_withdrawal.payout_txn_id IS NULL OR v_withdrawal.reject_reason IS NOT NULL
       OR v_withdrawal.payout_reference IS NOT NULL OR v_withdrawal.completed_at IS NOT NULL THEN
      RAISE EXCEPTION 'processing withdrawal evidence is invalid'
        USING ERRCODE='check_violation';
    END IF;
  ELSIF v_withdrawal.status='paid' THEN
    IF v_withdrawal.payout_txn_id IS NULL
       OR nullif(btrim(v_withdrawal.payout_reference),'') IS NULL
       OR v_withdrawal.completed_at IS NULL OR v_withdrawal.reject_reason IS NOT NULL THEN
      RAISE EXCEPTION 'paid withdrawal evidence is invalid'
        USING ERRCODE='check_violation';
    END IF;
  ELSE
    RAISE EXCEPTION 'unsupported withdrawal status %',v_withdrawal.status
      USING ERRCODE='check_violation';
  END IF;

  IF v_withdrawal.payout_txn_id IS NULL THEN
    IF EXISTS (SELECT 1 FROM public.ledger_transactions t
                WHERE t.tenant_id=p_tenant AND t.kind='commission_paid'
                  AND t.source_type='withdrawal' AND t.source_id=v_withdrawal.id) THEN
      RAISE EXCEPTION 'pre-payout withdrawal has detached payout ledger evidence'
        USING ERRCODE='check_violation';
    END IF;
    RETURN;
  END IF;

  SELECT t.kind,t.currency::text,t.source_type,t.source_id,t.actor_kind
    INTO STRICT v_kind,v_currency,v_source_type,v_source,v_actor
    FROM public.ledger_transactions t
   WHERE t.tenant_id=p_tenant AND t.id=v_withdrawal.payout_txn_id;
  SELECT count(*) INTO v_count FROM public.ledger_entries e
   WHERE e.transaction_id=v_withdrawal.payout_txn_id;
  IF v_kind<>'commission_paid' OR v_currency<>v_withdrawal.currency::text
     OR v_source_type IS DISTINCT FROM 'withdrawal'
     OR v_source IS DISTINCT FROM v_withdrawal.id
     OR v_actor<>'admin' OR v_count<>2
     OR NOT EXISTS (
       SELECT 1 FROM public.ledger_entries e
       JOIN public.ledger_accounts a
         ON a.tenant_id=e.tenant_id AND a.id=e.account_id
      WHERE e.transaction_id=v_withdrawal.payout_txn_id
        AND e.tenant_id=p_tenant AND a.tenant_id=p_tenant
        AND e.direction='debit' AND e.amount=v_withdrawal.amount
        AND e.currency=v_withdrawal.currency
        AND a.account_type='user_commission_available'
        AND a.owner_user_id=v_withdrawal.user_id
        AND a.currency=v_withdrawal.currency AND a.normal_balance='credit'
     )
     OR NOT EXISTS (
       SELECT 1 FROM public.ledger_entries e
       JOIN public.ledger_accounts a
         ON a.tenant_id=e.tenant_id AND a.id=e.account_id
      WHERE e.transaction_id=v_withdrawal.payout_txn_id
        AND e.tenant_id=p_tenant AND a.tenant_id=p_tenant
        AND e.direction='credit' AND e.amount=v_withdrawal.amount
        AND e.currency=v_withdrawal.currency
        AND a.account_type='channel_cash' AND a.owner_user_id IS NULL
        AND a.owner_ref='payout' AND a.currency=v_withdrawal.currency
        AND a.normal_balance='debit'
     ) THEN
    RAISE EXCEPTION 'commission payout ledger shape is invalid'
      USING ERRCODE='check_violation';
  END IF;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.assert_withdrawal_trigger() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, public, pg_temp
AS $$
BEGIN
  PERFORM app.assert_withdrawal(NEW.tenant_id,NEW.id);
  RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- Revalidate the materialized commission row when an immutable ledger shape
-- is first created or when somebody attempts to append another entry later.
-- +goose StatementBegin
CREATE FUNCTION app.assert_commission_ledger_trigger() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
  v_tenant uuid;
  v_txn uuid;
  v_kind text;
  v_source_type text;
  v_source uuid;
  v_entry uuid;
  v_withdrawal uuid;
BEGIN
  IF TG_TABLE_NAME='ledger_transactions' THEN
    v_tenant := NEW.tenant_id;
    v_txn := NEW.id;
    v_kind := NEW.kind;
    v_source_type := NEW.source_type;
    v_source := NEW.source_id;
  ELSE
    SELECT t.tenant_id,t.id,t.kind,t.source_type,t.source_id
      INTO STRICT v_tenant,v_txn,v_kind,v_source_type,v_source
      FROM public.ledger_transactions t
     WHERE t.id=NEW.transaction_id;
  END IF;

  IF v_kind='commission_paid' THEN
    IF v_source_type IS DISTINCT FROM 'withdrawal' OR v_source IS NULL THEN
      RAISE EXCEPTION 'commission_paid requires withdrawal source'
        USING ERRCODE='check_violation';
    END IF;
    SELECT w.id INTO v_withdrawal FROM public.withdrawals w
     WHERE w.tenant_id=v_tenant AND w.id=v_source AND w.payout_txn_id=v_txn;
    IF v_withdrawal IS NULL THEN
      RAISE EXCEPTION 'commission_paid lacks its exact withdrawal evidence row'
        USING ERRCODE='check_violation';
    END IF;
    PERFORM app.assert_withdrawal(v_tenant,v_withdrawal);
    RETURN NULL;
  ELSIF v_kind='commission_accrued' THEN
    IF v_source_type IS DISTINCT FROM 'order' OR v_source IS NULL THEN
      RAISE EXCEPTION 'commission_accrued requires order source'
        USING ERRCODE='check_violation';
    END IF;
    SELECT c.id INTO v_entry
      FROM public.commission_entries c
     WHERE c.tenant_id=v_tenant AND c.accrual_txn_id=v_txn
       AND c.order_id=v_source;
  ELSIF v_kind='commission_settled' THEN
    IF v_source_type IS DISTINCT FROM 'commission_entry' OR v_source IS NULL THEN
      RAISE EXCEPTION 'commission_settled requires commission entry source'
        USING ERRCODE='check_violation';
    END IF;
    SELECT c.id INTO v_entry
      FROM public.commission_entries c
     WHERE c.tenant_id=v_tenant AND c.id=v_source AND c.settle_txn_id=v_txn;
  ELSE
    RETURN NULL;
  END IF;
  IF v_entry IS NULL THEN
    RAISE EXCEPTION 'commission ledger transaction lacks its exact evidence row'
      USING ERRCODE='check_violation';
  END IF;
  PERFORM app.assert_commission_entry(v_tenant,v_entry);
  RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER trg_commission_entry_guard
  BEFORE INSERT OR UPDATE ON public.commission_entries
  FOR EACH ROW EXECUTE FUNCTION app.guard_commission_entry();
CREATE CONSTRAINT TRIGGER trg_commission_entry_commit
  AFTER INSERT OR UPDATE ON public.commission_entries
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  EXECUTE FUNCTION app.assert_commission_entry_trigger();
CREATE CONSTRAINT TRIGGER trg_commission_transaction_commit
  AFTER INSERT ON public.ledger_transactions
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  EXECUTE FUNCTION app.assert_commission_ledger_trigger();
CREATE CONSTRAINT TRIGGER trg_commission_ledger_entries_commit
  AFTER INSERT ON public.ledger_entries
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  EXECUTE FUNCTION app.assert_commission_ledger_trigger();
CREATE TRIGGER trg_commission_entries_no_delete
  BEFORE DELETE ON public.commission_entries
  FOR EACH STATEMENT EXECUTE FUNCTION app.deny_delete();

CREATE TRIGGER trg_withdrawal_guard
  BEFORE INSERT OR UPDATE ON public.withdrawals
  FOR EACH ROW EXECUTE FUNCTION app.guard_withdrawal();
CREATE CONSTRAINT TRIGGER trg_withdrawal_commit
  AFTER INSERT OR UPDATE ON public.withdrawals
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  EXECUTE FUNCTION app.assert_withdrawal_trigger();
CREATE TRIGGER trg_withdrawals_no_delete
  BEFORE DELETE ON public.withdrawals
  FOR EACH STATEMENT EXECUTE FUNCTION app.deny_delete();

REVOKE ALL ON FUNCTION app.guard_commission_entry(),
  app.assert_commission_entry(uuid,uuid),
  app.assert_commission_entry_trigger(),
  app.assert_commission_ledger_trigger(),
  app.guard_withdrawal(),app.assert_withdrawal(uuid,uuid),
  app.assert_withdrawal_trigger() FROM PUBLIC, aegis_app;

-- +goose StatementBegin
DO $$
DECLARE
  v_row record;
BEGIN
  FOR v_row IN SELECT tenant_id,id FROM public.commission_entries LOOP
    PERFORM app.assert_commission_entry(v_row.tenant_id,v_row.id);
  END LOOP;
  FOR v_row IN SELECT tenant_id,id FROM public.withdrawals LOOP
    PERFORM app.assert_withdrawal(v_row.tenant_id,v_row.id);
  END LOOP;
  IF EXISTS (
    SELECT 1 FROM public.ledger_transactions t
    LEFT JOIN public.commission_entries c
      ON c.tenant_id=t.tenant_id AND c.accrual_txn_id=t.id
     AND c.order_id=t.source_id
   WHERE t.kind='commission_accrued'
     AND (t.source_type IS DISTINCT FROM 'order' OR t.source_id IS NULL OR c.id IS NULL)
  ) THEN
    RAISE EXCEPTION 'commission_accrued history lacks exact commission evidence'
      USING ERRCODE='check_violation';
  END IF;
  IF EXISTS (
    SELECT 1 FROM public.ledger_transactions t
    LEFT JOIN public.commission_entries c
      ON c.tenant_id=t.tenant_id AND c.id=t.source_id AND c.settle_txn_id=t.id
   WHERE t.kind='commission_settled'
     AND (t.source_type IS DISTINCT FROM 'commission_entry'
          OR t.source_id IS NULL OR c.id IS NULL)
  ) THEN
    RAISE EXCEPTION 'commission_settled history lacks exact commission evidence'
      USING ERRCODE='check_violation';
  END IF;
  IF EXISTS (
    SELECT 1 FROM public.ledger_transactions t
    LEFT JOIN public.withdrawals w
      ON w.tenant_id=t.tenant_id AND w.id=t.source_id AND w.payout_txn_id=t.id
   WHERE t.kind='commission_paid'
     AND (t.source_type IS DISTINCT FROM 'withdrawal'
          OR t.source_id IS NULL OR w.id IS NULL)
  ) THEN
    RAISE EXCEPTION 'commission_paid history lacks exact withdrawal evidence'
      USING ERRCODE='check_violation';
  END IF;
END $$;
-- +goose StatementEnd

-- Narrow the runtime role in this migration rather than relying on bootstrap
-- scripts which do not run during an in-place upgrade.
REVOKE INSERT, UPDATE, DELETE, TRUNCATE
  ON public.commission_entries FROM aegis_app;
REVOKE INSERT (
  id,tenant_id,referrer_user_id,referee_user_id,order_id,payment_id,currency,
  base_amount,rate_bp,commission_amount,status,frozen_until,review_required,
  review_reason,accrual_txn_id,settle_txn_id,reversal_txn_id,reversed_reason,
  created_at,updated_at
) ON public.commission_entries FROM aegis_app;
REVOKE UPDATE (
  id,tenant_id,referrer_user_id,referee_user_id,order_id,payment_id,currency,
  base_amount,rate_bp,commission_amount,status,frozen_until,review_required,
  review_reason,accrual_txn_id,settle_txn_id,reversal_txn_id,reversed_reason,
  created_at,updated_at
) ON public.commission_entries FROM aegis_app;
GRANT INSERT (
  tenant_id,referrer_user_id,referee_user_id,order_id,currency,
  base_amount,rate_bp,commission_amount,frozen_until,
  review_required,review_reason,accrual_txn_id
) ON public.commission_entries TO aegis_app;
GRANT UPDATE (status,settle_txn_id,review_required,review_reason,updated_at)
  ON public.commission_entries TO aegis_app;

REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON public.withdrawals FROM aegis_app;
REVOKE INSERT (
  id,tenant_id,user_id,currency,amount,payout_detail_encrypted,key_version,
  status,reject_reason,approval_request_id,payout_reference,proof_object_key,
  hold_txn_id,payout_txn_id,return_txn_id,requested_at,completed_at,updated_at
) ON public.withdrawals FROM aegis_app;
REVOKE UPDATE (
  id,tenant_id,user_id,currency,amount,payout_detail_encrypted,key_version,
  status,reject_reason,approval_request_id,payout_reference,proof_object_key,
  hold_txn_id,payout_txn_id,return_txn_id,requested_at,completed_at,updated_at
) ON public.withdrawals FROM aegis_app;
GRANT INSERT (tenant_id,user_id,currency,amount,payout_detail_encrypted,status)
  ON public.withdrawals TO aegis_app;
GRANT UPDATE (status,reject_reason,payout_reference,payout_txn_id,completed_at,updated_at)
  ON public.withdrawals TO aegis_app;

GRANT INSERT (case_kind) ON public.late_payment_cases TO aegis_app;

-- This write-once watermark is transaction based, so a transaction which
-- began before the migration but commits after it cannot evade Down safety by
-- carrying an older now() timestamp.
-- +goose StatementBegin
CREATE FUNCTION app.mark_order_release_00040_used() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, public, pg_temp
AS $$
BEGIN
  IF TG_TABLE_NAME='late_payment_cases'
     OR TG_TABLE_NAME='commission_entries'
     OR TG_TABLE_NAME='withdrawals'
     OR (TG_TABLE_NAME='order_reservation_events'
         AND to_jsonb(NEW)->>'event_kind' IN ('cancel','expire'))
     OR (TG_TABLE_NAME='payment_intents'
         AND to_jsonb(NEW)->>'status' IN ('cancelled','expired')
         AND to_jsonb(NEW)->>'status' IS DISTINCT FROM
             to_jsonb(OLD)->>'status') THEN
    UPDATE app.order_release_00040_meta SET used=true WHERE singleton;
  END IF;
  RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER trg_order_release_00040_late_used
  AFTER INSERT OR UPDATE ON public.late_payment_cases
  FOR EACH ROW EXECUTE FUNCTION app.mark_order_release_00040_used();
CREATE TRIGGER trg_order_release_00040_commission_used
  AFTER INSERT OR UPDATE ON public.commission_entries
  FOR EACH ROW EXECUTE FUNCTION app.mark_order_release_00040_used();
CREATE TRIGGER trg_order_release_00040_withdrawal_used
  AFTER INSERT OR UPDATE ON public.withdrawals
  FOR EACH ROW EXECUTE FUNCTION app.mark_order_release_00040_used();
CREATE TRIGGER trg_order_release_00040_event_used
  AFTER INSERT ON public.order_reservation_events
  FOR EACH ROW EXECUTE FUNCTION app.mark_order_release_00040_used();
CREATE TRIGGER trg_order_release_00040_intent_used
  AFTER UPDATE ON public.payment_intents
  FOR EACH ROW EXECUTE FUNCTION app.mark_order_release_00040_used();

REVOKE ALL ON FUNCTION app.mark_order_release_00040_used()
  FROM PUBLIC, aegis_app;

GRANT UPDATE (cancelled_at,expired_at,cancel_reason)
  ON public.orders TO aegis_app;

-- +goose Down

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

-- +goose StatementBegin
DO $$
DECLARE
  v_used boolean;
  v_later boolean := false;
BEGIN
  PERFORM pg_catalog.pg_advisory_xact_lock(400040,1);
  IF current_setting('app.order_release_writers_stopped',true) IS DISTINCT FROM 'yes'
     OR current_setting('app.allow_order_release_schema40_down',true) IS DISTINCT FROM 'yes' THEN
    RAISE EXCEPTION '00040 Down requires stopped order/payment writers and downgrade approval';
  END IF;
  IF to_regclass('public.goose_db_version') IS NOT NULL THEN
    EXECUTE 'SELECT EXISTS (SELECT 1 FROM public.goose_db_version WHERE version_id>40 AND is_applied)'
      INTO v_later;
  END IF;
  IF v_later THEN
    RAISE EXCEPTION '00040 Down refused because a later migration is applied';
  END IF;
  IF to_regclass('app.order_release_00040_meta') IS NULL
     OR to_regprocedure('app.mark_order_release_00040_used()') IS NULL THEN
    RAISE EXCEPTION '00040 Down requires intact schema-40 objects';
  END IF;

  -- Stop a writer before consulting the watermark. ACCESS EXCLUSIVE conflicts
  -- with every table writer lock and remains held until the Down transaction
  -- commits, closing the read-used/drop-trigger TOCTOU window.
  LOCK TABLE public.orders, public.payment_intents,
    public.order_reservation_events, public.late_payment_cases,
    public.commission_entries, public.withdrawals, public.ledger_transactions,
    public.ledger_entries IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE app.order_release_00040_meta IN ACCESS EXCLUSIVE MODE;

  SELECT used INTO STRICT v_used
    FROM app.order_release_00040_meta WHERE singleton FOR UPDATE;
  IF v_used THEN
    RAISE EXCEPTION 'cannot rollback 00040: release, commission or withdrawal evidence exists';
  END IF;
END $$;
-- +goose StatementEnd

REVOKE UPDATE (cancelled_at,expired_at,cancel_reason)
  ON public.orders FROM aegis_app;
REVOKE INSERT (case_kind) ON public.late_payment_cases FROM aegis_app;
REVOKE INSERT (
  tenant_id,referrer_user_id,referee_user_id,order_id,currency,
  base_amount,rate_bp,commission_amount,frozen_until,
  review_required,review_reason,accrual_txn_id
) ON public.commission_entries FROM aegis_app;
REVOKE UPDATE (status,settle_txn_id,review_required,review_reason,updated_at)
  ON public.commission_entries FROM aegis_app;
GRANT INSERT, UPDATE, DELETE ON public.commission_entries TO aegis_app;
REVOKE INSERT (tenant_id,user_id,currency,amount,payout_detail_encrypted,status)
  ON public.withdrawals FROM aegis_app;
REVOKE UPDATE (status,reject_reason,payout_reference,payout_txn_id,completed_at,updated_at)
  ON public.withdrawals FROM aegis_app;
GRANT INSERT, UPDATE, DELETE ON public.withdrawals TO aegis_app;

DROP TRIGGER trg_order_release_00040_intent_used ON public.payment_intents;
DROP TRIGGER trg_order_release_00040_event_used ON public.order_reservation_events;
DROP TRIGGER trg_order_release_00040_withdrawal_used ON public.withdrawals;
DROP TRIGGER trg_order_release_00040_commission_used ON public.commission_entries;
DROP TRIGGER trg_order_release_00040_late_used ON public.late_payment_cases;
DROP FUNCTION app.mark_order_release_00040_used();

DROP TRIGGER trg_late_payment_cases_no_delete ON public.late_payment_cases;
DROP TRIGGER trg_late_payment_entries_commit ON public.ledger_entries;
DROP TRIGGER trg_late_payment_transaction_commit ON public.ledger_transactions;
DROP TRIGGER trg_late_payment_case_suspense_commit ON public.late_payment_cases;
DROP FUNCTION app.assert_late_payment_suspense_trigger();
DROP FUNCTION app.assert_late_payment_suspense(uuid,uuid,uuid);
DROP TRIGGER trg_commission_entries_no_delete ON public.commission_entries;
DROP TRIGGER trg_commission_ledger_entries_commit ON public.ledger_entries;
DROP TRIGGER trg_commission_transaction_commit ON public.ledger_transactions;
DROP TRIGGER trg_commission_entry_commit ON public.commission_entries;
DROP TRIGGER trg_commission_entry_guard ON public.commission_entries;
DROP TRIGGER trg_withdrawals_no_delete ON public.withdrawals;
DROP TRIGGER trg_withdrawal_commit ON public.withdrawals;
DROP TRIGGER trg_withdrawal_guard ON public.withdrawals;
DROP FUNCTION app.assert_commission_entry_trigger();
DROP FUNCTION app.assert_commission_ledger_trigger();
DROP FUNCTION app.assert_withdrawal_trigger();
DROP FUNCTION app.assert_withdrawal(uuid,uuid);
DROP FUNCTION app.guard_withdrawal();
DROP FUNCTION app.assert_commission_entry(uuid,uuid);
DROP FUNCTION app.guard_commission_entry();
DROP INDEX public.ledger_transactions_late_payment_unique;
ALTER TABLE public.late_payment_cases
  DROP CONSTRAINT late_payment_cases_suspense_txn_unique,
  DROP COLUMN case_kind;

-- Restore the pre-00040 processing transition contract.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.guard_payment_intent() RETURNS trigger
LANGUAGE plpgsql
AS $$
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
-- +goose StatementEnd

DROP TABLE app.order_release_00040_meta;

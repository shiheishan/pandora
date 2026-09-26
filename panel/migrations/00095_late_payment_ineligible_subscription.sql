-- 订阅终态时的续费 / 变更结算（契约 R117）。
--
-- 建续费单与变更单时校验过订阅状态，结算时却不再校验：订阅若在支付窗口里被改成
-- 终态（expired 没有出边，cancelled 只能到 expired），履约无条件写 active，被
-- subscription_transitions 的触发器拒绝，整笔结算回滚——连 payment_events 都不
-- 落库，渠道回调无限重投，后台「标记已付」得到 500。
--
-- 现在 settlePaymentTx 锁住订阅后按建单同一口径复核（active / trialing / grace /
-- past_due），不合格就把钱隔离进挂账，订单与订阅都不动。这里给挂账加第三种
-- case_kind，并照 00040 的风格给守卫补一支：
--   - 订单是续费或变更单、从未被捕获（paid_at 为空）；预留图要么还 held（订单
--     待支付），要么在收款之后才被释放（订单之后被取消或过期）；
--   - 挂账还在 suspense 时，订阅必须确实不在可续费的状态组里。
-- 订单留在待支付，由过期扫描或用户取消照常释放、退回余额冻结；释放路径不再把
-- 这类已隔离的收款当作「已入账」（release.go），否则冻结的余额永远退不回来。
--
-- 其余检查（渠道事件证据、分录形状、金额与币种）沿用函数末尾的公共部分不变。

-- +goose Up

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

ALTER TABLE public.late_payment_cases
  DROP CONSTRAINT late_payment_cases_case_kind_check,
  ADD CONSTRAINT late_payment_cases_case_kind_check
    CHECK (case_kind IN ('released_order','excess_capture','ineligible_subscription'));

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.assert_late_payment_suspense(
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
  v_order_kind text;
  v_sub_status text;
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
  ELSIF v_case.case_kind='ineligible_subscription' THEN
    -- 续费 / 变更单结算时订阅已不收这笔钱：订单从未被捕获（待支付，或之后才被
    -- 取消 / 过期释放），钱在释放之前就已到账。
    SELECT o.kind,s.status INTO v_order_kind,v_sub_status
      FROM public.orders o
      JOIN public.subscriptions s
        ON s.tenant_id=o.tenant_id AND s.id=o.subscription_id AND s.user_id=o.user_id
     WHERE o.tenant_id=p_tenant AND o.id=v_case.order_id;
    IF NOT FOUND OR v_order_kind NOT IN ('renewal','upgrade')
       OR v_order_status NOT IN ('pending_payment','processing','cancelled','expired')
       OR v_order_paid_at IS NOT NULL
       OR NOT ((v_reservation_state='held' AND v_order_status IN ('pending_payment','processing'))
            OR (v_reservation_state='released' AND v_released_at IS NOT NULL
                AND v_paid_at<v_released_at)) THEN
      RAISE EXCEPTION 'ineligible subscription payment requires an uncaptured renewal or plan change order'
        USING ERRCODE='check_violation';
    END IF;
    -- 资格是收款那一刻的事实，只在挂账仍是 suspense 时复核：之后订阅可能被人工
    -- 恢复（paused -> active），转入余额等处理不该因此被拒。状态组与 Go 的
    -- subscriptionAcceptsPaidChange 相同，由 billing 的单测钉住。
    IF v_case.status='suspense'
       AND v_sub_status IN ('active','trialing','grace','past_due') THEN
      RAISE EXCEPTION 'ineligible subscription payment requires a subscription that no longer accepts it'
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

-- +goose Down

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

-- 回滚会让已隔离的这类款项失去守卫能认的分支，之后任何处理都过不了提交检查。
-- 有这类挂账就拒绝回滚，要先人工对账。
-- +goose StatementBegin
DO $$
BEGIN
  LOCK TABLE public.late_payment_cases IN SHARE ROW EXCLUSIVE MODE;
  IF EXISTS (SELECT 1 FROM public.late_payment_cases
              WHERE case_kind='ineligible_subscription') THEN
    RAISE EXCEPTION
      'cannot rollback 00095: ineligible_subscription late payment cases exist and need explicit reconciliation';
  END IF;
END $$;
-- +goose StatementEnd

ALTER TABLE public.late_payment_cases
  DROP CONSTRAINT late_payment_cases_case_kind_check,
  ADD CONSTRAINT late_payment_cases_case_kind_check
    CHECK (case_kind IN ('released_order','excess_capture'));

-- 还原 00040 的函数体（逐字）
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.assert_late_payment_suspense(
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

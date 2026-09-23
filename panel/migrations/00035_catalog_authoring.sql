-- Catalog authoring invariants: optimistic concurrency, lifecycle control,
-- immutable published snapshots, and unambiguous active-price selection.

-- +goose Up

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

-- Refuse to install over ambiguous history. Neither condition has a safe,
-- deterministic repair that a schema migration can choose on the operator's
-- behalf.
-- +goose StatementBegin
DO $$
DECLARE
  v_plan_id uuid;
  v_version_id uuid;
  v_product_id uuid;
  v_currency app.currency_code;
  v_interval text;
  v_interval_count smallint;
  v_group_id uuid;
  v_tenant_id uuid;
BEGIN
  SELECT p.id, p.current_version_id
    INTO v_plan_id, v_version_id
    FROM plans p
    LEFT JOIN plan_versions pv
      ON pv.tenant_id = p.tenant_id
     AND pv.id = p.current_version_id
   WHERE p.current_version_id IS NOT NULL
     AND (pv.id IS NULL OR pv.plan_id IS DISTINCT FROM p.id)
   LIMIT 1;

  IF FOUND THEN
    RAISE EXCEPTION
      'catalog authoring migration refused: plan % points at version % owned by another plan',
      v_plan_id, v_version_id
      USING ERRCODE = 'foreign_key_violation';
  END IF;

  IF EXISTS (
    SELECT 1 FROM plans
     WHERE status = 'active' AND current_version_id IS NULL
  ) THEN
    RAISE EXCEPTION
      'catalog authoring migration refused: an active plan has no current version'
      USING ERRCODE = 'check_violation';
  END IF;

  IF EXISTS (
    SELECT 1 FROM plans
     WHERE visible_from IS NOT NULL
       AND visible_until IS NOT NULL
       AND visible_until <= visible_from
  ) THEN
    RAISE EXCEPTION
      'catalog authoring migration refused: a plan has an invalid visibility window'
      USING ERRCODE = 'check_violation';
  END IF;

  IF EXISTS (
    SELECT 1 FROM plan_versions
     WHERE (overage_policy = 'throttle' AND throttle_kbps IS NULL)
        OR (overage_policy <> 'throttle' AND throttle_kbps IS NOT NULL)
  ) THEN
    RAISE EXCEPTION
      'catalog authoring migration refused: throttle plan version lacks throttle_kbps'
      USING ERRCODE = 'check_violation';
  END IF;

  IF EXISTS (
    SELECT 1 FROM plan_versions
     WHERE (quota_reset_strategy = 'fixed_day' AND quota_reset_day IS NULL)
        OR (quota_reset_strategy <> 'fixed_day' AND quota_reset_day IS NOT NULL)
  ) THEN
    RAISE EXCEPTION
      'catalog authoring migration refused: a plan version has inconsistent quota reset fields'
      USING ERRCODE = 'check_violation';
  END IF;

  IF EXISTS (
    SELECT 1 FROM plan_versions
     WHERE frozen_at IS NOT NULL AND frozen_at < created_at
  ) THEN
    RAISE EXCEPTION
      'catalog authoring migration refused: a plan version was frozen before it was created'
      USING ERRCODE = 'check_violation';
  END IF;

  IF EXISTS (
    SELECT 1
      FROM plans p
      CROSS JOIN LATERAL unnest(p.visible_group_ids) AS visible_group(group_id)
     WHERE NOT EXISTS (
       SELECT 1 FROM user_groups ug
        WHERE ug.tenant_id = p.tenant_id
          AND ug.id = visible_group.group_id
     )
  ) THEN
    RAISE EXCEPTION
      'catalog authoring migration refused: a plan references a missing or cross-tenant visible group'
      USING ERRCODE = 'foreign_key_violation';
  END IF;

  IF EXISTS (
    SELECT 1 FROM plans
     WHERE (visibility = 'group' AND cardinality(visible_group_ids) = 0)
        OR (visibility <> 'group' AND cardinality(visible_group_ids) <> 0)
  ) THEN
    RAISE EXCEPTION
      'catalog authoring migration refused: visibility and visible groups are inconsistent'
      USING ERRCODE = 'check_violation';
  END IF;

  SELECT tenant_id, product_id
    INTO v_tenant_id, v_product_id
    FROM plans
   GROUP BY tenant_id, product_id
  HAVING count(*) > 1
   LIMIT 1;

  IF FOUND THEN
    RAISE EXCEPTION
      'catalog authoring migration refused: tenant % has multiple plans for product %',
      v_tenant_id, v_product_id
      USING ERRCODE = 'unique_violation';
  END IF;

  SELECT product_id, currency, billing_interval, interval_count, user_group_id
    INTO v_product_id, v_currency, v_interval, v_interval_count, v_group_id
    FROM prices
   WHERE status = 'active'
   GROUP BY tenant_id, product_id, currency, billing_interval,
            interval_count, user_group_id
  HAVING count(*) > 1
   LIMIT 1;

  IF FOUND THEN
    RAISE EXCEPTION
      'catalog authoring migration refused: duplicate active price for product %, currency %, interval % x %, user_group %',
      v_product_id, v_currency, v_interval, v_interval_count,
      COALESCE(v_group_id::text, '<public>')
      USING ERRCODE = 'unique_violation';
  END IF;

  -- Predict the post-backfill draft set before adding its partial unique index.
  SELECT pv.tenant_id, pv.plan_id
    INTO v_tenant_id, v_plan_id
    FROM plan_versions pv
   WHERE pv.frozen_at IS NULL
     AND NOT EXISTS (
       SELECT 1 FROM plans p
        WHERE p.tenant_id = pv.tenant_id AND p.current_version_id = pv.id
     )
     AND NOT EXISTS (
       SELECT 1 FROM subscriptions s
        WHERE s.tenant_id = pv.tenant_id AND s.plan_version_id = pv.id
     )
     AND NOT EXISTS (
       SELECT 1 FROM order_items oi
        WHERE oi.tenant_id = pv.tenant_id AND oi.plan_version_id = pv.id
     )
   GROUP BY pv.tenant_id, pv.plan_id
  HAVING count(*) > 1
   LIMIT 1;

  IF FOUND THEN
    RAISE EXCEPTION
      'catalog authoring migration refused: tenant % plan % has multiple draft versions',
      v_tenant_id, v_plan_id
      USING ERRCODE = 'unique_violation';
  END IF;
END;
$$;
-- +goose StatementEnd

-- The historical trigger blocks updates to already-frozen rows, including the
-- lifecycle backfill below. DDL is transactional, so a failed migration restores
-- the original trigger automatically.
DROP TRIGGER IF EXISTS trg_plan_versions_frozen ON plan_versions;

-- Remember only the legacy rows this migration freezes. A safe immediate Down
-- can then restore schema-34 data exactly instead of pretending the historical
-- creation timestamp was a publication timestamp.
CREATE TABLE app.catalog_authoring_00035_backfill (
  plan_version_id uuid PRIMARY KEY,
  previous_frozen_at timestamptz,
  recorded_at timestamptz NOT NULL DEFAULT now()
);
REVOKE ALL ON app.catalog_authoring_00035_backfill FROM PUBLIC;

INSERT INTO app.catalog_authoring_00035_backfill
  (plan_version_id, previous_frozen_at)
SELECT pv.id, pv.frozen_at
  FROM plan_versions pv
 WHERE pv.frozen_at IS NULL
   AND (
     EXISTS (
       SELECT 1 FROM plans p
        WHERE p.tenant_id = pv.tenant_id
          AND p.current_version_id = pv.id
     )
     OR EXISTS (
       SELECT 1 FROM subscriptions s
        WHERE s.tenant_id = pv.tenant_id
          AND s.plan_version_id = pv.id
     )
     OR EXISTS (
       SELECT 1 FROM order_items oi
        WHERE oi.tenant_id = pv.tenant_id
          AND oi.plan_version_id = pv.id
     )
   );

ALTER TABLE plans
  ADD COLUMN row_version bigint NOT NULL DEFAULT 1 CHECK (row_version > 0);

ALTER TABLE prices
  ADD COLUMN row_version bigint NOT NULL DEFAULT 1 CHECK (row_version > 0);

ALTER TABLE plan_versions
  ADD COLUMN row_version bigint NOT NULL DEFAULT 1 CHECK (row_version > 0),
  ADD COLUMN status text NOT NULL DEFAULT 'draft'
    CHECK (status IN ('draft', 'published', 'retired'));

-- Existing frozen/current/consumed versions are historical publications even
-- if older code forgot to set frozen_at. Backfill that fact deterministically;
-- versions never exposed outside authoring remain drafts.
UPDATE plan_versions pv
   SET frozen_at = COALESCE(pv.frozen_at, now()),
       status = 'published'
 WHERE pv.frozen_at IS NOT NULL
    OR EXISTS (
         SELECT 1 FROM plans p
          WHERE p.tenant_id = pv.tenant_id
            AND p.current_version_id = pv.id
       )
    OR EXISTS (
         SELECT 1 FROM subscriptions s
          WHERE s.tenant_id = pv.tenant_id
            AND s.plan_version_id = pv.id
       )
    OR EXISTS (
         SELECT 1 FROM order_items oi
          WHERE oi.tenant_id = pv.tenant_id
            AND oi.plan_version_id = pv.id
       );

ALTER TABLE plan_versions
  ADD CONSTRAINT plan_versions_status_freeze_consistent CHECK (
    (status = 'draft' AND frozen_at IS NULL)
    OR (status IN ('published', 'retired') AND frozen_at IS NOT NULL)
  ),
  ADD CONSTRAINT plan_versions_freeze_time_valid CHECK (
    frozen_at IS NULL OR frozen_at >= created_at
  ),
  ADD CONSTRAINT plan_versions_reset_day_exact CHECK (
    (quota_reset_strategy = 'fixed_day' AND quota_reset_day IS NOT NULL)
    OR (quota_reset_strategy <> 'fixed_day' AND quota_reset_day IS NULL)
  ),
  ADD CONSTRAINT plan_versions_throttle_exact CHECK (
    (overage_policy = 'throttle' AND throttle_kbps IS NOT NULL)
    OR (overage_policy <> 'throttle' AND throttle_kbps IS NULL)
  );

-- A three-column candidate key lets the current-version FK prove that the
-- selected version belongs to this exact tenant and plan.
ALTER TABLE plan_versions
  ADD CONSTRAINT plan_versions_tenant_plan_id_id_key
  UNIQUE (tenant_id, plan_id, id);

ALTER TABLE plans
  ADD CONSTRAINT plans_tenant_product_unique UNIQUE (tenant_id, product_id);

ALTER TABLE plans DROP CONSTRAINT IF EXISTS plans_current_version_fk;
ALTER TABLE plans
  ADD CONSTRAINT plans_current_version_membership_fk
  FOREIGN KEY (tenant_id, id, current_version_id)
  REFERENCES plan_versions (tenant_id, plan_id, id)
  ON DELETE NO ACTION
  DEFERRABLE INITIALLY DEFERRED;

-- PostgreSQL 14-compatible NULL semantics: public and group-scoped offers use
-- separate partial indexes, so NULL public groups cannot bypass uniqueness.
CREATE UNIQUE INDEX prices_one_active_public_offer_key
  ON prices (
    tenant_id, product_id, currency, billing_interval, interval_count
  )
  WHERE status = 'active' AND user_group_id IS NULL;

CREATE UNIQUE INDEX prices_one_active_group_offer_key
  ON prices (
    tenant_id, product_id, currency, billing_interval, interval_count,
    user_group_id
  )
  WHERE status = 'active' AND user_group_id IS NOT NULL;

COMMENT ON INDEX prices_one_active_public_offer_key IS
  'One active public offer per tenant/product/currency/interval (PostgreSQL 14 compatible).';
COMMENT ON INDEX prices_one_active_group_offer_key IS
  'One active group offer per tenant/product/currency/interval/user group.';

CREATE UNIQUE INDEX plan_versions_one_draft_per_plan
  ON plan_versions (tenant_id, plan_id)
  WHERE status = 'draft';

-- These indexes keep historical-reference checks and operational diagnostics
-- bounded as the order and subscription tables grow.
CREATE INDEX idx_order_items_price_ref
  ON order_items (tenant_id, price_id)
  WHERE price_id IS NOT NULL;
CREATE INDEX idx_subscriptions_price_ref
  ON subscriptions (tenant_id, price_id)
  WHERE price_id IS NOT NULL;
CREATE INDEX idx_subscription_items_price_ref
  ON subscription_items (tenant_id, price_id)
  WHERE price_id IS NOT NULL;

-- Do not add a global prices.currency IN (CNY, USD) constraint here. The table
-- is also a historical ledger reference and an import/API integration target;
-- SQL cannot identify "new admin authoring" without over-constraining those
-- legitimate paths. The Admin authoring service must allowlist CNY/USD, while
-- this migration preserves existing ISO-4217 history.

-- Plan lifecycle is monotonic. A permanently archived catalog entry is never
-- silently reactivated; a replacement must be authored and published explicitly.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.guard_plan_catalog_transition() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'UPDATE'
     AND NEW.status IS DISTINCT FROM OLD.status
     AND NOT (
       (OLD.status = 'draft' AND NEW.status IN ('active', 'archived'))
       OR (OLD.status = 'active' AND NEW.status = 'archived')
     ) THEN
    RAISE EXCEPTION 'plan % has invalid lifecycle transition: % -> %',
      OLD.id, OLD.status, NEW.status
      USING ERRCODE = 'check_violation';
  END IF;

  IF NEW.current_version_id IS NULL THEN
    IF NEW.status = 'active' THEN
      RAISE EXCEPTION 'active plan % must have a published current version', NEW.id
        USING ERRCODE = 'check_violation';
    END IF;
  ELSE
    -- FOR SHARE serializes this choice with publication/retirement updates.
    PERFORM 1
      FROM plan_versions pv
     WHERE pv.tenant_id = NEW.tenant_id
       AND pv.plan_id = NEW.id
       AND pv.id = NEW.current_version_id
       AND pv.status = 'published'
       AND pv.frozen_at IS NOT NULL
     FOR SHARE;
    IF NOT FOUND THEN
      RAISE EXCEPTION
        'plan % current version % must belong to the plan and be frozen+published',
        NEW.id, NEW.current_version_id
        USING ERRCODE = 'foreign_key_violation';
    END IF;
  END IF;

  IF NEW.visible_from IS NOT NULL
     AND NEW.visible_until IS NOT NULL
     AND NEW.visible_until <= NEW.visible_from THEN
    RAISE EXCEPTION 'plan % has an invalid visibility window', NEW.id
      USING ERRCODE = 'check_violation';
  END IF;

  IF NEW.visibility = 'group' THEN
    IF cardinality(NEW.visible_group_ids) = 0 OR EXISTS (
      SELECT 1
        FROM unnest(NEW.visible_group_ids) AS requested(group_id)
       WHERE NOT EXISTS (
         SELECT 1 FROM user_groups ug
          WHERE ug.tenant_id = NEW.tenant_id
            AND ug.id = requested.group_id
       )
    ) THEN
      RAISE EXCEPTION 'plan % has invalid visible groups', NEW.id
        USING ERRCODE = 'foreign_key_violation';
    END IF;
  ELSIF cardinality(NEW.visible_group_ids) <> 0 THEN
    RAISE EXCEPTION 'plan % has visible groups but visibility is %',
      NEW.id, NEW.visibility
      USING ERRCODE = 'check_violation';
  END IF;

  RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER trg_plans_catalog_transition
  BEFORE UPDATE OF status, current_version_id, visibility, visible_group_ids,
    visible_from, visible_until ON plans
  FOR EACH ROW EXECUTE FUNCTION app.guard_plan_catalog_transition();

CREATE TRIGGER trg_plans_catalog_insert
  BEFORE INSERT ON plans
  FOR EACH ROW EXECUTE FUNCTION app.guard_plan_catalog_transition();

-- Published versions are semantic snapshots. Freezing a legacy draft is treated
-- as publication, preserving the existing "UPDATE frozen_at = now()" contract.
-- A frozen version may only move from published to retired; its authoring fields
-- and frozen timestamp remain immutable. DELETE is rejected as well.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.guard_frozen_plan_version() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    IF OLD.frozen_at IS NOT NULL THEN
      RAISE EXCEPTION
        'plan version % was frozen at % and cannot be deleted',
        OLD.id, OLD.frozen_at
        USING ERRCODE = 'insufficient_privilege';
    END IF;
    RETURN OLD;
  END IF;

  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'draft' OR NEW.frozen_at IS NOT NULL THEN
      RAISE EXCEPTION
        'new plan version % must start as an unfrozen draft', NEW.id
        USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;

  IF OLD.status = 'draft'
     AND OLD.frozen_at IS NULL
     AND NEW.status = 'draft'
     AND NEW.frozen_at IS NOT NULL THEN
    NEW.status := 'published';
  END IF;

  IF NEW.status = 'published' AND NEW.frozen_at IS NULL THEN
    NEW.frozen_at := now();
  END IF;

  IF NEW.status IS DISTINCT FROM OLD.status
     AND NOT (
       (OLD.status = 'draft' AND NEW.status = 'published')
       OR (OLD.status = 'published' AND NEW.status = 'retired')
     ) THEN
    RAISE EXCEPTION 'plan version % has invalid lifecycle transition: % -> %',
      OLD.id, OLD.status, NEW.status
      USING ERRCODE = 'check_violation';
  END IF;

  IF NEW.status = 'retired' AND OLD.status IS DISTINCT FROM 'retired'
     AND EXISTS (
       SELECT 1 FROM plans p
        WHERE p.tenant_id = OLD.tenant_id
          AND p.current_version_id = OLD.id
     ) THEN
    RAISE EXCEPTION
      'plan version % is still selected as a current version and cannot be retired',
      OLD.id
      USING ERRCODE = 'foreign_key_violation';
  END IF;

  IF OLD.frozen_at IS NOT NULL
     AND (to_jsonb(NEW) - ARRAY['status', 'row_version']::text[])
         IS DISTINCT FROM
         (to_jsonb(OLD) - ARRAY['status', 'row_version']::text[]) THEN
    RAISE EXCEPTION
      'plan version % was frozen at %; publish a new version instead of editing it',
      OLD.id, OLD.frozen_at
      USING ERRCODE = 'insufficient_privilege';
  END IF;

  RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER trg_plan_versions_frozen
  BEFORE INSERT OR UPDATE OR DELETE ON plan_versions
  FOR EACH ROW EXECUTE FUNCTION app.guard_frozen_plan_version();

-- Child rows are part of the immutable plan-version snapshot. Check both sides
-- of an UPDATE so moving a row into or out of a frozen version is impossible.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.assert_plan_version_mutable(
  p_tenant_id uuid, p_plan_version_id uuid
) RETURNS void
LANGUAGE plpgsql AS $$
DECLARE
  v_frozen_at timestamptz;
BEGIN
  SELECT frozen_at
    INTO v_frozen_at
    FROM plan_versions
   WHERE tenant_id = p_tenant_id
     AND id = p_plan_version_id
   FOR SHARE;

  IF NOT FOUND THEN
    RAISE EXCEPTION 'plan version % does not exist in tenant %',
      p_plan_version_id, p_tenant_id
      USING ERRCODE = 'foreign_key_violation';
  END IF;

  IF v_frozen_at IS NOT NULL THEN
    RAISE EXCEPTION
      'plan version % was frozen at %; its snapshot children are immutable',
      p_plan_version_id, v_frozen_at
      USING ERRCODE = 'insufficient_privilege';
  END IF;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.guard_plan_version_child() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    PERFORM app.assert_plan_version_mutable(NEW.tenant_id, NEW.plan_version_id);
    RETURN NEW;
  ELSIF TG_OP = 'UPDATE' THEN
    IF NEW.tenant_id IS NOT DISTINCT FROM OLD.tenant_id
       AND NEW.plan_version_id IS NOT DISTINCT FROM OLD.plan_version_id THEN
      PERFORM app.assert_plan_version_mutable(OLD.tenant_id, OLD.plan_version_id);
    ELSIF OLD.plan_version_id < NEW.plan_version_id THEN
      PERFORM app.assert_plan_version_mutable(OLD.tenant_id, OLD.plan_version_id);
      PERFORM app.assert_plan_version_mutable(NEW.tenant_id, NEW.plan_version_id);
    ELSE
      PERFORM app.assert_plan_version_mutable(NEW.tenant_id, NEW.plan_version_id);
      PERFORM app.assert_plan_version_mutable(OLD.tenant_id, OLD.plan_version_id);
    END IF;
    RETURN NEW;
  ELSE
    -- A mutable parent draft is deleted before its ON DELETE CASCADE child
    -- triggers run. At trigger depth > 1, a missing parent is therefore the
    -- expected positive cascade path; direct child deletes still require and
    -- lock a live mutable parent.
    IF pg_trigger_depth() > 1 AND NOT EXISTS (
      SELECT 1 FROM plan_versions
       WHERE tenant_id = OLD.tenant_id
         AND id = OLD.plan_version_id
    ) THEN
      RETURN OLD;
    END IF;
    PERFORM app.assert_plan_version_mutable(OLD.tenant_id, OLD.plan_version_id);
    RETURN OLD;
  END IF;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER trg_entitlements_frozen_version
  BEFORE INSERT OR UPDATE OR DELETE ON entitlements
  FOR EACH ROW EXECUTE FUNCTION app.guard_plan_version_child();

CREATE TRIGGER trg_quota_definitions_frozen_version
  BEFORE INSERT OR UPDATE OR DELETE ON quota_definitions
  FOR EACH ROW EXECUTE FUNCTION app.guard_plan_version_child();

CREATE TRIGGER trg_plan_node_pools_frozen_version
  BEFORE INSERT OR UPDATE OR DELETE ON plan_node_pools
  FOR EACH ROW EXECUTE FUNCTION app.guard_plan_version_child();

-- Prices are append-only offer records. Corrections are represented by archiving
-- the old row and creating a new row, even before the old price is purchased;
-- this makes the admin contract independent of a reference-check race.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.guard_immutable_price() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'price % is append-only and cannot be deleted', OLD.id
      USING ERRCODE = 'insufficient_privilege';
  END IF;

  IF (to_jsonb(NEW) - ARRAY['status', 'row_version']::text[])
     IS DISTINCT FROM
     (to_jsonb(OLD) - ARRAY['status', 'row_version']::text[]) THEN
    RAISE EXCEPTION
      'price % is immutable; archive it and create a new price instead', OLD.id
      USING ERRCODE = 'insufficient_privilege';
  END IF;

  IF NEW.status IS DISTINCT FROM OLD.status
     AND NOT (OLD.status = 'active' AND NEW.status = 'archived') THEN
    RAISE EXCEPTION
      'referenced price % may only transition active -> archived', OLD.id
      USING ERRCODE = 'check_violation';
  END IF;

  IF NEW.status IS DISTINCT FROM OLD.status
     AND NEW.row_version <> OLD.row_version + 1 THEN
    RAISE EXCEPTION
      'price % archive must increment row_version exactly once', OLD.id
      USING ERRCODE = 'check_violation';
  END IF;

  IF NEW.status IS NOT DISTINCT FROM OLD.status
     AND NEW.row_version IS DISTINCT FROM OLD.row_version THEN
    RAISE EXCEPTION
      'price % row_version may only change during archival', OLD.id
      USING ERRCODE = 'check_violation';
  END IF;

  RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER trg_prices_immutable
  BEFORE UPDATE OR DELETE ON prices
  FOR EACH ROW EXECUTE FUNCTION app.guard_immutable_price();

COMMENT ON COLUMN plans.row_version IS
  'Optimistic concurrency token for admin catalog writes; services must compare and increment it atomically.';
COMMENT ON COLUMN plan_versions.row_version IS
  'Optimistic concurrency token for draft plan-version authoring.';
COMMENT ON COLUMN prices.row_version IS
  'Optimistic concurrency token for admin price authoring and archival.';
COMMENT ON COLUMN plan_versions.status IS
  'Authoring lifecycle: draft -> published -> retired. Publishing freezes the semantic snapshot.';

-- +goose Down

-- Rolling back after the new guarantees have become semantically observable is
-- unsafe. Refuse rather than silently re-enable edits to sold prices or frozen
-- snapshots, or discard lifecycle/concurrency facts.
-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM plans WHERE row_version <> 1)
     OR EXISTS (SELECT 1 FROM plan_versions WHERE row_version <> 1)
     OR EXISTS (SELECT 1 FROM prices WHERE row_version <> 1) THEN
    RAISE EXCEPTION
      'cannot rollback catalog authoring invariants after row_version was used';
  END IF;

  IF EXISTS (SELECT 1 FROM plan_versions WHERE status = 'retired') THEN
    RAISE EXCEPTION
      'cannot rollback catalog authoring invariants while retired plan versions exist';
  END IF;

  IF EXISTS (
    SELECT 1 FROM plan_versions
     WHERE (status = 'draft' AND frozen_at IS NOT NULL)
        OR (status = 'published' AND frozen_at IS NULL)
  ) THEN
    RAISE EXCEPTION
      'cannot rollback catalog authoring invariants with incompatible lifecycle/freeze values';
  END IF;

END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS trg_prices_immutable ON prices;
DROP FUNCTION IF EXISTS app.guard_immutable_price();

DROP TRIGGER IF EXISTS trg_plan_node_pools_frozen_version ON plan_node_pools;
DROP TRIGGER IF EXISTS trg_quota_definitions_frozen_version ON quota_definitions;
DROP TRIGGER IF EXISTS trg_entitlements_frozen_version ON entitlements;
DROP FUNCTION IF EXISTS app.guard_plan_version_child();
DROP FUNCTION IF EXISTS app.assert_plan_version_mutable(uuid, uuid);

DROP TRIGGER IF EXISTS trg_plan_versions_frozen ON plan_versions;
DROP FUNCTION IF EXISTS app.guard_frozen_plan_version();

DROP TRIGGER IF EXISTS trg_plans_catalog_insert ON plans;
DROP TRIGGER IF EXISTS trg_plans_catalog_transition ON plans;
DROP FUNCTION IF EXISTS app.guard_plan_catalog_transition();

DROP INDEX IF EXISTS plan_versions_one_draft_per_plan;
DROP INDEX IF EXISTS prices_one_active_group_offer_key;
DROP INDEX IF EXISTS prices_one_active_public_offer_key;
DROP INDEX IF EXISTS idx_subscription_items_price_ref;
DROP INDEX IF EXISTS idx_subscriptions_price_ref;
DROP INDEX IF EXISTS idx_order_items_price_ref;

ALTER TABLE plans DROP CONSTRAINT IF EXISTS plans_current_version_membership_fk;
ALTER TABLE plans
  ADD CONSTRAINT plans_current_version_fk
  FOREIGN KEY (tenant_id, current_version_id)
  REFERENCES plan_versions (tenant_id, id)
  ON DELETE SET NULL;

ALTER TABLE plan_versions
  DROP CONSTRAINT IF EXISTS plan_versions_tenant_plan_id_id_key;
ALTER TABLE plans DROP CONSTRAINT IF EXISTS plans_tenant_product_unique;

-- Undo only the legacy freezes performed by this migration. New API writes are
-- already excluded above by row_version/retired guards.
UPDATE plan_versions pv
   SET frozen_at = b.previous_frozen_at,
       status = 'draft'
  FROM app.catalog_authoring_00035_backfill b
 WHERE b.plan_version_id = pv.id;

ALTER TABLE plan_versions
  DROP CONSTRAINT IF EXISTS plan_versions_throttle_exact,
  DROP CONSTRAINT IF EXISTS plan_versions_reset_day_exact,
  DROP CONSTRAINT IF EXISTS plan_versions_freeze_time_valid,
  DROP CONSTRAINT IF EXISTS plan_versions_status_freeze_consistent;

ALTER TABLE plan_versions
  DROP COLUMN IF EXISTS status,
  DROP COLUMN IF EXISTS row_version;
ALTER TABLE plans DROP COLUMN IF EXISTS row_version;
ALTER TABLE prices DROP COLUMN IF EXISTS row_version;

DROP TABLE IF EXISTS app.catalog_authoring_00035_backfill;

-- Restore the historical plan-version UPDATE guard exactly.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.guard_frozen_plan_version() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF OLD.frozen_at IS NOT NULL THEN
    RAISE EXCEPTION
      '套餐版本 % 已于 % 冻结（SUB-002），如需变更请发布新版本',
      OLD.id, OLD.frozen_at
      USING ERRCODE = 'insufficient_privilege';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER trg_plan_versions_frozen BEFORE UPDATE ON plan_versions
  FOR EACH ROW EXECUTE FUNCTION app.guard_frozen_plan_version();

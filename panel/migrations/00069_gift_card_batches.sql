-- 礼品卡批次（M3）：把「一次生成的一批码」落成实体，承载一次性导出。
--
-- 此前批次只是 gift_card_codes.batch_id 上的一个裸 UUID，明文码可以被任何
-- 有只读权限的管理员反复导出。卡密是等价现金，设计要求「完整卡码只在生成
-- 那一刻与一次导出里出现，之后只看掩码」：这需要一个地方记下「这一批导出过没有、
-- 谁导出的」，也就是这张表的 exported_at / exported_by。
--
-- 存量批次按码上的 batch_id 分组回填，exported_at 一律记为迁移时间（D-C-4
-- 方案 a，从严）：旧接口下它们早已能被反复导出明文，不能再当作「未导出」。
-- exported_by 留空，表示由迁移而非某位管理员标记。
--
-- 行本身几乎不可变：生成时写一次，导出时补一次 exported_at / exported_by，
-- 之后任何改动都拒绝。用触发器而不是列级授权守住这一点 ——
-- deploy/configure-app-role.sql 会把列级授权统一提升回表级。

-- +goose Up

-- +goose StatementBegin
CREATE TABLE gift_card_batches (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id    uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  template_id  uuid NOT NULL,
  -- 生码时的前缀（大写字母数字，最多 8 位）；掩码按「前缀 + 随机段前 4 位」显示
  prefix       text NOT NULL DEFAULT '' CHECK (prefix ~ '^[A-Z0-9]{0,8}$'),
  count        int NOT NULL CHECK (count > 0),
  expires_at   timestamptz(6),
  created_by   uuid,
  created_at   timestamptz(6) NOT NULL DEFAULT now(),
  exported_at  timestamptz(6),
  exported_by  uuid,
  CONSTRAINT gift_card_batches_tenant_id_id_key UNIQUE (tenant_id, id),
  CONSTRAINT gift_card_batches_template_fk
    FOREIGN KEY (tenant_id, template_id)
    REFERENCES gift_card_templates (tenant_id, id) ON DELETE RESTRICT,
  CONSTRAINT gift_card_batches_created_by_fk
    FOREIGN KEY (tenant_id, created_by) REFERENCES users (tenant_id, id),
  CONSTRAINT gift_card_batches_exported_by_fk
    FOREIGN KEY (tenant_id, exported_by) REFERENCES users (tenant_id, id),
  -- 有导出人就一定有导出时间；反过来不成立（迁移回填的批次没有导出人）
  CONSTRAINT gift_card_batches_export_evidence
    CHECK (exported_by IS NULL OR exported_at IS NOT NULL)
);

CREATE INDEX idx_gift_card_batches_template
  ON gift_card_batches (tenant_id, template_id, created_at DESC);
CREATE INDEX idx_gift_card_batches_created
  ON gift_card_batches (tenant_id, created_at DESC);

SELECT app.enable_tenant_rls('gift_card_batches');
GRANT SELECT, INSERT, UPDATE ON gift_card_batches TO aegis_app;
REVOKE DELETE, TRUNCATE ON gift_card_batches FROM aegis_app;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.guard_gift_card_batch() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'gift card batches are append-only'
      USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.id IS DISTINCT FROM OLD.id OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
     OR NEW.template_id IS DISTINCT FROM OLD.template_id
     OR NEW.prefix IS DISTINCT FROM OLD.prefix OR NEW.count IS DISTINCT FROM OLD.count
     OR NEW.expires_at IS DISTINCT FROM OLD.expires_at
     OR NEW.created_by IS DISTINCT FROM OLD.created_by
     OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION 'gift card batch identity is immutable'
      USING ERRCODE = 'check_violation';
  END IF;
  -- 一次性导出：导出标记只能从空写成有值，写上之后不可撤回或改写
  IF OLD.exported_at IS NOT NULL AND
     (NEW.exported_at IS DISTINCT FROM OLD.exported_at
      OR NEW.exported_by IS DISTINCT FROM OLD.exported_by) THEN
    RAISE EXCEPTION 'gift card batch export mark is write-once'
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER gift_card_batches_guard
  BEFORE UPDATE OR DELETE ON gift_card_batches
  FOR EACH ROW EXECUTE FUNCTION app.guard_gift_card_batch();
-- +goose StatementEnd

-- +goose StatementBegin
-- 回填存量批次。一批码只来自一个模板、共享同一前缀与有效期（GenerateCodes
-- 的写法）；min/max 只是为了在分组里取出那个共同值。码 = 前缀 + 12 位随机段，
-- 所以前缀取码去掉末尾 12 位；形状不符的历史码前缀记为空串。
-- 生成人从当时的审计记录 gift_card.codes_generated 里找回，找不到就留空。
INSERT INTO gift_card_batches
  (id, tenant_id, template_id, prefix, count, expires_at, created_by, created_at, exported_at)
SELECT g.batch_id, g.tenant_id, g.template_id,
       CASE WHEN g.prefix ~ '^[A-Z0-9]{0,8}$' THEN g.prefix ELSE '' END,
       g.count, g.expires_at,
       (SELECT a.actor_id FROM audit_events a
         WHERE a.tenant_id = g.tenant_id
           AND a.action = 'gift_card.codes_generated'
           AND a.after_digest->>'batch_id' = g.batch_id::text
           AND EXISTS (SELECT 1 FROM users u
                        WHERE u.tenant_id = a.tenant_id AND u.id = a.actor_id)
         ORDER BY a.occurred_at LIMIT 1),
       g.created_at, now()
  FROM (SELECT c.tenant_id, c.batch_id,
               (array_agg(c.template_id ORDER BY c.created_at, c.id))[1] AS template_id,
               left(min(c.code), greatest(length(min(c.code)) - 12, 0)) AS prefix,
               count(*)::int AS count,
               max(c.expires_at) AS expires_at,
               min(c.created_at) AS created_at
          FROM gift_card_codes c
         WHERE c.batch_id IS NOT NULL
         GROUP BY c.tenant_id, c.batch_id) g;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE gift_card_codes
  ADD CONSTRAINT gift_card_codes_batch_fk
    FOREIGN KEY (tenant_id, batch_id)
    REFERENCES gift_card_batches (tenant_id, id) ON DELETE RESTRICT;
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
ALTER TABLE gift_card_codes DROP CONSTRAINT IF EXISTS gift_card_codes_batch_fk;
DROP TRIGGER IF EXISTS gift_card_batches_guard ON gift_card_batches;
DROP FUNCTION IF EXISTS app.guard_gift_card_batch();
DROP TABLE IF EXISTS gift_card_batches;
-- +goose StatementEnd

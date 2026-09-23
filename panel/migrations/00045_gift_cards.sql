-- +goose Up

-- 礼品卡 / 卡密（对标 Xboard gift-card）。
--
-- 三张表分工：
--   gift_card_templates   —— 卡型：送什么、谁能用、能用几次
--   gift_card_codes       —— 具体的码，一码一行
--   gift_card_redemptions —— 兑换流水，追加写
--
-- 为什么模板和码分开：一次活动往往批量生成几千个码，它们共享同一套奖励
-- 与限制规则。规则放在码上意味着改一次活动规则要 UPDATE 几千行，
-- 而且历史码和新码会悄悄不一致。

CREATE TABLE gift_card_templates (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  name          text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 120),
  description   text NOT NULL DEFAULT '',
  -- general：送余额 / 流量 / 延长到期
  -- plan   ：直接兑换一个套餐
  -- mystery：从若干奖品里加权随机抽一个
  type          text NOT NULL CHECK (type IN ('general','plan','mystery')),
  status        text NOT NULL DEFAULT 'active'
                CHECK (status IN ('active','paused','archived')),

  -- 奖励定义。按 type 解释：
  --   general -> {"balance":1000,"traffic_bytes":1073741824,"expire_days":30,"reset_quota":false}
  --   plan    -> {"plan_id":"...","price_id":"...","periods":1}
  --   mystery -> {"pool":[{"weight":70,"label":"1 元","balance":100}, ...]}
  rewards       jsonb NOT NULL DEFAULT '{}'::jsonb,

  -- 领取条件：{"new_user_only":false,"paid_user_only":false,
  --            "allowed_plan_ids":[],"require_invite":false}
  conditions    jsonb NOT NULL DEFAULT '{}'::jsonb,

  -- 使用限制：{"max_use_per_user":1,"cooldown_hours":0}
  limits        jsonb NOT NULL DEFAULT '{}'::jsonb,

  theme_color   text NOT NULL DEFAULT '',
  created_by    uuid REFERENCES users(id),
  created_at    timestamptz(6) NOT NULL DEFAULT now(),
  updated_at    timestamptz(6) NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, name)
);

CREATE TABLE gift_card_codes (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id    uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  template_id  uuid NOT NULL REFERENCES gift_card_templates(id) ON DELETE RESTRICT,
  -- 码全大写存储。用户输入时大小写混杂是常态，统一在写入前折叠，
  -- 查询才能走唯一索引而不是 lower() 表达式扫描。
  code         text NOT NULL CHECK (code = upper(code) AND length(code) BETWEEN 8 AND 32),
  status       text NOT NULL DEFAULT 'unused'
               CHECK (status IN ('unused','used','disabled','expired')),
  batch_id     uuid,
  expires_at   timestamptz(6),
  used_by      uuid REFERENCES users(id),
  used_at      timestamptz(6),
  created_at   timestamptz(6) NOT NULL DEFAULT now(),
  updated_at   timestamptz(6) NOT NULL DEFAULT now(),
  -- 码在租户内唯一。跨租户允许重复：不同站点各自发各自的卡，
  -- 强行全局唯一只会让批量生成更容易撞。
  UNIQUE (tenant_id, code),
  -- used 状态必须同时有人和时间。少任何一个都说明兑换过程中断了，
  -- 那种记录事后没法解释：钱发出去了却不知道给了谁。
  CHECK ((status = 'used' AND used_by IS NOT NULL AND used_at IS NOT NULL)
      OR (status <> 'used' AND used_by IS NULL AND used_at IS NULL))
);

CREATE INDEX idx_gift_card_codes_template ON gift_card_codes (tenant_id, template_id, status);
CREATE INDEX idx_gift_card_codes_batch ON gift_card_codes (tenant_id, batch_id)
  WHERE batch_id IS NOT NULL;

CREATE TABLE gift_card_redemptions (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  code_id       uuid NOT NULL REFERENCES gift_card_codes(id) ON DELETE RESTRICT,
  template_id   uuid NOT NULL REFERENCES gift_card_templates(id) ON DELETE RESTRICT,
  user_id       uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  -- 实际发出去的东西。盲盒抽到哪个奖品必须记下来 ——
  -- 不记的话，用户说「我抽到的是 10 元」时没有任何东西能对质。
  granted       jsonb NOT NULL,
  ledger_txn_id uuid,
  redeemed_at   timestamptz(6) NOT NULL DEFAULT now(),
  -- 一个码只能兑一次。这条唯一约束是并发安全的最后一道防线：
  -- 就算应用层的行锁出了问题，数据库也不会让同一个码兑出两份奖励。
  UNIQUE (code_id)
);

CREATE INDEX idx_gift_card_redemptions_user
  ON gift_card_redemptions (tenant_id, user_id, redeemed_at DESC);
CREATE INDEX idx_gift_card_redemptions_template
  ON gift_card_redemptions (tenant_id, template_id, redeemed_at DESC);

ALTER TABLE gift_card_templates ENABLE ROW LEVEL SECURITY;
ALTER TABLE gift_card_codes ENABLE ROW LEVEL SECURITY;
ALTER TABLE gift_card_redemptions ENABLE ROW LEVEL SECURITY;

CREATE POLICY gift_card_templates_tenant ON gift_card_templates
  USING (tenant_id = app.current_tenant_id());
CREATE POLICY gift_card_codes_tenant ON gift_card_codes
  USING (tenant_id = app.current_tenant_id());
CREATE POLICY gift_card_redemptions_tenant ON gift_card_redemptions
  USING (tenant_id = app.current_tenant_id());

GRANT SELECT, INSERT, UPDATE ON gift_card_templates TO aegis_app;
GRANT SELECT, INSERT, UPDATE ON gift_card_codes TO aegis_app;
GRANT SELECT, INSERT ON gift_card_redemptions TO aegis_app;

-- 兑换记录是追加写：改一条兑换流水等于篡改「谁在什么时候拿到了什么」。
-- +goose StatementBegin
CREATE TRIGGER gift_card_redemptions_append_only
  BEFORE UPDATE OR DELETE ON gift_card_redemptions
  FOR EACH ROW EXECUTE FUNCTION app.deny_mutation();
-- +goose StatementEnd

-- +goose StatementBegin
INSERT INTO permissions (code, domain, description, high_risk)
VALUES ('marketing.giftcard.read',  'marketing', '查看礼品卡模板与卡密', false),
       ('marketing.giftcard.write', 'marketing', '创建礼品卡、批量生码、启停卡密', true)
ON CONFLICT (code) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_code)
SELECT r.id, p.code
  FROM roles r
 CROSS JOIN (VALUES ('marketing.giftcard.read'), ('marketing.giftcard.write')) AS p(code)
 WHERE r.is_system AND r.code IN ('tenant_admin', 'platform_admin')
ON CONFLICT (role_id, permission_code) DO NOTHING;
-- +goose StatementEnd

-- +goose Down

DROP TRIGGER IF EXISTS gift_card_redemptions_append_only ON gift_card_redemptions;
DROP TABLE IF EXISTS gift_card_redemptions;
DROP TABLE IF EXISTS gift_card_codes;
DROP TABLE IF EXISTS gift_card_templates;
DELETE FROM role_permissions
 WHERE permission_code IN ('marketing.giftcard.read','marketing.giftcard.write');
DELETE FROM permissions
 WHERE code IN ('marketing.giftcard.read','marketing.giftcard.write');

-- +goose Up

-- 站点设置：一个键一行，值是 JSON。各键的类型、缺省值和校验在 settings 模块的 service 里定义，
-- 缺行就取缺省值，所以这里不种设置行。秘密值（如 SMTP 口令）存的是 Sealer 封存后的 base64 串。
-- updated_by 不对用户表建外键：settings 不依赖任何业务模块。
CREATE TABLE settings (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  key        text NOT NULL,
  value      jsonb NOT NULL,
  updated_by uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT settings_key_unique UNIQUE (key),
  -- 键名形如 site.timezone、mail.smtp_host：小写点分，至少两段
  CONSTRAINT settings_key_format CHECK (key ~ '^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$' AND char_length(key) <= 128)
);

-- 降级开关：enabled = 功能可用。essential 的开关关不掉，关闭必须写原因，两条都由 CHECK 兜底。
-- changed_by 同样不建外键。
CREATE TABLE feature_switches (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  code       text NOT NULL,
  enabled    boolean NOT NULL DEFAULT true,
  essential  boolean NOT NULL DEFAULT false,
  reason     text,
  changed_by uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT feature_switches_code_unique UNIQUE (code),
  CONSTRAINT feature_switches_essential_stays_on CHECK (NOT essential OR enabled),
  CONSTRAINT feature_switches_disable_needs_reason CHECK (enabled OR reason IS NOT NULL),
  CONSTRAINT feature_switches_reason_shape CHECK (reason IS NULL OR (btrim(reason) <> '' AND char_length(reason) <= 500))
);

-- 种子开关，全部开启；与 service.SeedSwitches 由测试对照。
INSERT INTO feature_switches (code, enabled, essential) VALUES
  ('auth.login', true, true),
  ('subscription.renewal', true, true),
  ('client.config_sync', true, true),
  ('auth.registration', true, false),
  ('billing.checkout', true, false),
  ('marketing.giftcard.redeem', true, false),
  ('notify.email', true, false),
  ('admin.writes', true, false);

-- +goose Down
DROP TABLE IF EXISTS feature_switches;
DROP TABLE IF EXISTS settings;

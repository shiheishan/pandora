-- +goose Up
-- appearance 模块：门户主题（site_themes）与内容插槽（site_slots）。依赖 00005_identity.sql 的 users。
--
-- 主题只管颜色：tokens 形如 {"light":{CSS 变量:值}, "dark":{…}}，键名白名单与取值规则在 appearance service
-- （DesignTokenKeys，唯一来源是前端 styles/design-tokens.ts）；branding 只有 site_name、tagline、logo 三个键。
-- 自定义 CSS 本期停用（门户 CSP 不允许注入 <style>），不建列。
-- 插槽内容是服务端净化后的 HTML，原文不存；插槽位白名单与前端挂载点一一对应，从没保存过的插槽位没有行。

CREATE TABLE site_themes (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  code        text NOT NULL CHECK (code ~ '^[a-z][a-z0-9_-]{1,38}$'),
  name        text NOT NULL CHECK (char_length(btrim(name)) BETWEEN 1 AND 60),
  -- 内置主题不能原地改、不能删，只能另存为自定义主题
  is_builtin  boolean NOT NULL DEFAULT false,
  is_active   boolean NOT NULL DEFAULT false,
  tokens      jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(tokens) = 'object'),
  branding    jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(branding) = 'object'),
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT site_themes_code_key UNIQUE (code)
);

-- 最多一套生效主题：激活是「先全关再开一个」，库里用部分唯一索引兜底
CREATE UNIQUE INDEX site_themes_one_active ON site_themes ((true)) WHERE is_active;

CREATE TABLE site_slots (
  slot_key    text PRIMARY KEY CHECK (slot_key IN (
                'portal.login.notice',
                'portal.home.banner',
                'portal.home.aside',
                'portal.sidebar.extra',
                'portal.subscribe.notice',
                'portal.plans.notice',
                'portal.footer'
              )),
  content     text NOT NULL DEFAULT '' CHECK (octet_length(content) <= 32768),
  enabled     boolean NOT NULL DEFAULT true,
  updated_by  uuid REFERENCES users(id) ON DELETE SET NULL,
  updated_at  timestamptz NOT NULL DEFAULT now()
);

-- 内置主题「默认 · 纸白」，装好即生效。两组取值与前端 styles/design-tokens.ts 的 COLOR_TOKENS 逐字一致
-- （appearance service 的 seed 测试逐条对照）；站点名 Pandora。
INSERT INTO site_themes (code, name, is_builtin, is_active, tokens, branding)
VALUES ('paper', '默认 · 纸白', true, true,
  '{
    "light":{
      "--bg":"#f5f4f0","--surface":"#fff","--surface-2":"#f9f8f5",
      "--surface-3":"#efede8","--border":"#e7e5df","--border-strong":"#dcd9d1",
      "--border-hover":"#c9c6bd","--text":"#1c1c1f","--text-2":"#62615c",
      "--text-3":"#6e6c66","--brand":"#b9442b","--brand-hover":"#a33a23",
      "--brand-ink":"#8c3019","--brand-soft":"#f6e4de","--brand-tint":"#fbf3ef",
      "--on-brand":"#fff","--ok":"#1f7a4f","--ok-soft":"#e3f3ea",
      "--ok-line":"#c9f0d8","--warn":"#9a5c00","--warn-soft":"#fbf0dc",
      "--warn-line":"#f0d9b0","--danger":"#b3263a","--danger-soft":"#f8e1e3",
      "--danger-line":"#eec3c8","--danger-tint":"#fdf3f3","--info":"#34507c",
      "--info-soft":"#e6ebf3","--info-line":"#cdd6e4","--on-ink":"#fff",
      "--hdr":"rgba(245,244,240,.9)","--scrim":"rgba(20,20,24,.4)","--shadow":"0 16px 36px -14px rgba(28,28,31,.25)",
      "--on-danger":"#fff","--toast-ok":"#6fcf9b","--toast-danger":"#f07a86",
      "--shadow-segment":"0 1px 2px rgba(20,20,24,.08)","--sidebar":"#151518","--sidebar-active":"#26262a",
      "--sidebar-text":"#ecebe7","--sidebar-text-2":"#9a988f","--sidebar-text-3":"#8a8880",
      "--sidebar-brand":"#e46e52"
    },
    "dark":{
      "--bg":"#111113","--surface":"#1a1a1d","--surface-2":"#1f1f22",
      "--surface-3":"#26262a","--border":"#2b2b2f","--border-strong":"#37373c",
      "--border-hover":"#4a4a50","--text":"#ecebe7","--text-2":"#9a988f",
      "--text-3":"#8a8880","--brand":"#e46e52","--brand-hover":"#ec8267",
      "--brand-ink":"#f2a58f","--brand-soft":"#3a231c","--brand-tint":"#201816",
      "--on-brand":"#170d0a","--ok":"#6fcf9b","--ok-soft":"#15271e",
      "--ok-line":"#1f3a2b","--warn":"#f0b35a","--warn-soft":"#2c2213",
      "--warn-line":"#4a3a1c","--danger":"#f07a86","--danger-soft":"#2c1519",
      "--danger-line":"#4d252b","--danger-tint":"#221417","--info":"#93b1dc",
      "--info-soft":"#172031","--info-line":"#2a3a52","--on-ink":"#111113",
      "--hdr":"rgba(17,17,19,.88)","--scrim":"rgba(0,0,0,.55)","--shadow":"0 16px 40px -12px rgba(0,0,0,.6)",
      "--on-danger":"#1a0f12","--toast-ok":"#1f7a4f","--toast-danger":"#b3263a",
      "--shadow-segment":"0 1px 2px rgba(20,20,24,.08)","--sidebar":"#0a0a0b","--sidebar-active":"#1f1f22",
      "--sidebar-text":"#ecebe7","--sidebar-text-2":"#9a988f","--sidebar-text-3":"#8a8880",
      "--sidebar-brand":"#e46e52"
    }
  }'::jsonb,
  '{"site_name":"Pandora"}'::jsonb);

-- +goose Down
DROP TABLE IF EXISTS site_slots;
DROP TABLE IF EXISTS site_themes;

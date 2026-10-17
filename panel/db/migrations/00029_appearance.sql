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

-- 内置主题「默认 · 森林绿」（code 仍是 paper），装好即生效。两组取值与前端 styles/design-tokens.ts 的 COLOR_TOKENS 逐字一致
-- （appearance service 的 seed 测试逐条对照）；站点名 Pandora。
INSERT INTO site_themes (code, name, is_builtin, is_active, tokens, branding)
VALUES ('paper', '默认 · 森林绿', true, true,
  '{
    "light":{
      "--bg":"#f4f5f2","--surface":"#fff","--surface-2":"#f6f8f6",
      "--surface-3":"#eef1ee","--border":"#e4e8e5","--border-strong":"#d5dcd7",
      "--border-hover":"#b9c3bc","--text":"#141715","--text-2":"#6a706b",
      "--text-3":"#969b96","--brand":"#2c5a3f","--brand-hover":"#244c35",
      "--brand-ink":"#1f3d2b","--brand-soft":"#dfe8e1","--brand-tint":"#f3f7f4",
      "--on-brand":"#fff","--brand-deep":"#1f3d2b","--brand-line":"#b4c8ba",
      "--brand-mark":"#4cbb72","--ok":"#1f7a4f","--ok-soft":"#e3f3ea",
      "--ok-line":"#c9f0d8","--warn":"#8a5a00","--warn-soft":"#fbf1dc",
      "--warn-line":"#f0d9b0","--danger":"#a3283a","--danger-soft":"#f9e4e6",
      "--danger-line":"#eec3c8","--danger-tint":"#fdf3f3","--info":"#34507c",
      "--info-soft":"#e6ebf3","--info-line":"#cdd6e4","--on-ink":"#fff",
      "--hdr":"rgba(244,245,242,.9)","--scrim":"rgba(20,40,30,.28)","--shadow":"0 24px 48px -20px rgba(20,40,30,.5)",
      "--shade":"#14281e","--lift":"0 0 0 1px rgba(20,40,30,.035),0 30px 60px -32px rgba(20,40,30,.22),0 6px 16px -8px rgba(20,40,30,.09)","--on-danger":"#fff",
      "--toast-ok":"#6fcf9b","--toast-danger":"#f07a86","--shadow-segment":"0 2px 6px -2px rgba(20,40,30,.25)",
      "--sidebar":"#151518","--sidebar-active":"#26262a","--sidebar-text":"#ecebe7",
      "--sidebar-text-2":"#9a988f","--sidebar-text-3":"#8a8880","--sidebar-brand":"#e46e52"
    },
    "dark":{
      "--bg":"#0e1210","--surface":"#161b18","--surface-2":"#1a201c",
      "--surface-3":"#202722","--border":"#262e29","--border-strong":"#2f3832",
      "--border-hover":"#46524a","--text":"#e7ebe7","--text-2":"#9ba59e",
      "--text-3":"#7a847d","--brand":"#5cbf83","--brand-hover":"#72cc95",
      "--brand-ink":"#a5dcb8","--brand-soft":"#1d3125","--brand-tint":"#141b16",
      "--on-brand":"#06120b","--brand-deep":"#3f9c66","--brand-line":"#2f4c3a",
      "--brand-mark":"#4cbb72","--ok":"#6fcf9b","--ok-soft":"#15271e",
      "--ok-line":"#1f3a2b","--warn":"#f0b35a","--warn-soft":"#2c2213",
      "--warn-line":"#4a3a1c","--danger":"#f07a86","--danger-soft":"#2c1519",
      "--danger-line":"#4d252b","--danger-tint":"#221417","--info":"#93b1dc",
      "--info-soft":"#172031","--info-line":"#2a3a52","--on-ink":"#0e1210",
      "--hdr":"rgba(14,18,16,.88)","--scrim":"rgba(0,0,0,.55)","--shadow":"0 24px 48px -20px rgba(0,0,0,.7)",
      "--shade":"#000","--lift":"0 0 0 1px rgba(255,255,255,.04),0 30px 60px -32px rgba(0,0,0,.7),0 6px 16px -8px rgba(0,0,0,.4)","--on-danger":"#1a0f12",
      "--toast-ok":"#1f7a4f","--toast-danger":"#b3263a","--shadow-segment":"0 2px 6px -2px rgba(0,0,0,.5)",
      "--sidebar":"#0a0a0b","--sidebar-active":"#1f1f22","--sidebar-text":"#ecebe7",
      "--sidebar-text-2":"#9a988f","--sidebar-text-3":"#8a8880","--sidebar-brand":"#e46e52"
    }
  }'::jsonb,
  '{"site_name":"Pandora"}'::jsonb);

-- +goose Down
DROP TABLE IF EXISTS site_slots;
DROP TABLE IF EXISTS site_themes;

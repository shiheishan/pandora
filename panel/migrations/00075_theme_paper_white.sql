-- 内置主题换成设计稿的「默认 · 纸白」（契约 5.A D-D-4 / D-E-4，缺陷 20 同批）。
--
-- 00051（default / midnight / aurora）与 00055（stellar 三套）的内置主题用的是
-- 旧门户的变量名（brand-2、fg、line、r-lg…），新门户一个都不认；而且它们是
-- 扁平的一组值，靠行内样式盖过 [data-theme="dark"]，用户切到暗色时照样被
-- 浅色值覆盖。用户拍板只保留设计稿这一套，所以这里：
--
--   · 删掉那六个内置主题（只删 is_builtin，管理员自建的主题不动）；
--   · 新建内置主题 paper「默认 · 纸白」并激活 —— 先全部取消激活，再插入，
--     之前生效的自定义主题会因此失效，这是「只保留一套」的本意；
--   · tokens 分 light / dark 两组，键就是 CSS 变量名（带 --），名单与取值以
--     前端 panel/frontend/src/styles/design-tokens.ts 的 COLOR_TOKENS 为唯一来源：
--     模块文件 html:root 的 33 个 + 规范页补充的 10 个，共 43 个，两组键完全相同
--     （appearance.DesignTokenKeys 与 theme_seed_test.go 逐条对照 design-tokens.ts）；
--   · branding 只放站点名称 Pandora，notify 的 {{site}} 与发件人名都从这里取。
--
-- 同一租户已有 code=paper 的自定义主题时 INSERT 会撞唯一键、整个迁移失败：
-- 宁可停下来让人处理，也不覆盖管理员自己的主题。

-- +goose Up
DELETE FROM site_themes
 WHERE is_builtin
   AND code IN ('default', 'midnight', 'aurora', 'stellar', 'stellar-dark', 'stellar-light');

UPDATE site_themes SET is_active = false, updated_at = now() WHERE is_active;

INSERT INTO site_themes (tenant_id, code, name, is_builtin, is_active, tokens, branding)
SELECT t.id, 'paper', '默认 · 纸白', true, true,
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
  '{"site_name":"Pandora"}'::jsonb
  FROM tenants t;

-- +goose Down
-- 回到 00051 + 00055 的样子：删掉 paper，六个内置主题按原值补回，
-- 没有任何主题生效时让 default 生效（00051 的初始状态）。
-- 被 Up 取消激活的自定义主题无从得知，不恢复。
DELETE FROM site_themes WHERE is_builtin AND code = 'paper';

-- +goose StatementBegin
INSERT INTO site_themes (tenant_id, code, name, is_builtin, is_active, tokens, branding)
SELECT t.id, v.code, v.name, true, false, v.tokens::jsonb, v.branding::jsonb
  FROM tenants t
 CROSS JOIN (VALUES
   ('default', '默认（潘多拉紫）',
    '{"brand":"#6d5efc","brand-2":"#a855f7","brand-soft":"rgba(109,94,252,.14)",'
    '"brand-on-soft":"#9c92ff","r-lg":"14px"}',
    '{"site_name":"潘多拉面板","tagline":"稳定、快速、随处可用"}'),
   ('midnight', '午夜（低饱和）',
    '{"brand":"#7c8aa5","brand-2":"#94a3b8","brand-soft":"rgba(124,138,165,.16)",'
    '"brand-on-soft":"#aab6c8","r-lg":"10px"}',
    '{"site_name":"潘多拉面板","tagline":""}'),
   ('aurora', '极光（青绿）',
    '{"brand":"#0f9d8a","brand-2":"#14b8a6","brand-soft":"rgba(15,157,138,.14)",'
    '"brand-on-soft":"#3ec9b4","r-lg":"18px"}',
    '{"site_name":"潘多拉面板","tagline":""}'),
   ('stellar', 'Stellar（星蓝）',
    '{"brand":"#3b82f6","brand-2":"#2563eb","brand-soft":"rgba(59,130,246,.14)",'
    '"brand-on-soft":"#7dabfa","r-lg":"12px",'
    '"glow":"radial-gradient(1200px 500px at 50% -20%,rgba(59,130,246,.18),transparent 70%)"}',
    '{"site_name":"Stellar","tagline":"畅连全球网络"}'),
   ('stellar-dark', 'Stellar 深空',
    '{"brand":"#3b82f6","brand-2":"#2563eb","brand-soft":"rgba(59,130,246,.16)",'
    '"brand-on-soft":"#7dabfa","r-lg":"12px",'
    '"bg":"#080b12","bg-elev":"#0c1119","surface":"#0f1520","surface-2":"#141c2a",'
    '"surface-3":"#1a2333",'
    '"line":"rgba(125,171,250,.10)","line-2":"rgba(125,171,250,.18)",'
    '"fg":"#e8edf6","fg-2":"#94a3b8","fg-3":"#6b7a91",'
    '"glow":"radial-gradient(1200px 500px at 50% -20%,rgba(59,130,246,.20),transparent 70%)"}',
    '{"site_name":"Stellar","tagline":"畅连全球网络"}'),
   ('stellar-light', 'Stellar 晴空',
    '{"brand":"#2563eb","brand-2":"#1d4ed8","brand-soft":"rgba(37,99,235,.10)",'
    '"brand-on-soft":"#1d4ed8","r-lg":"12px",'
    '"bg":"#f7f9fc","bg-elev":"#ffffff","surface":"#ffffff","surface-2":"#f1f5fb",'
    '"surface-3":"#e6edf7",'
    '"line":"rgba(30,58,110,.09)","line-2":"rgba(30,58,110,.16)",'
    '"fg":"#0d1526","fg-2":"#55637a","fg-3":"#6b7a91",'
    '"glow":"radial-gradient(1200px 500px at 50% -20%,rgba(37,99,235,.10),transparent 70%)"}',
    '{"site_name":"Stellar","tagline":"畅连全球网络"}')
 ) AS v(code, name, tokens, branding)
ON CONFLICT (tenant_id, code) DO NOTHING;
-- +goose StatementEnd

UPDATE site_themes s SET is_active = true, updated_at = now()
 WHERE s.is_builtin AND s.code = 'default'
   AND NOT EXISTS (SELECT 1 FROM site_themes x WHERE x.tenant_id = s.tenant_id AND x.is_active);

-- +goose Up

-- 第二套主题包：Stellar（星蓝）。
--
-- 前三套（default / midnight / aurora）只换了品牌色那几个 token，
-- 背景、卡片、描边全都沿用 portal 内置的值。结果是三套主题看上去
-- 只有按钮和链接的颜色不同，换主题的感知很弱。
--
-- 这一套把底色也一起换掉：深色下是偏蓝的近黑（#0a0e17 一路到
-- #1a2233），浅色下是冷调白。品牌色走 #3b82f6 → #2563eb 这条
-- 常见的蓝，和底色同源，整体是一个色系而不是「深灰底 + 一点彩色」。
--
-- 为什么能改底色：applySiteTheme 把 token 写在 <html> 的行内样式上，
-- 行内样式的优先级高于 [data-theme="dark"] 这类属性选择器，所以
-- --bg / --surface 这些本来由配色方案决定的变量也能被主题覆盖。
--
-- 代价是深浅两套配色共用同一份 token。深色的 #0a0e17 放到浅色下面
-- 就是一块黑板。所以这里只放两套配色下都成立的值——品牌色、圆角、
-- 光晕——底色留给 stellar-dark / stellar-light 两个变体各自去定。

-- +goose StatementBegin
INSERT INTO site_themes (tenant_id, code, name, is_builtin, is_active, tokens, branding)
SELECT t.id, v.code, v.name, true, false, v.tokens::jsonb, v.branding::jsonb
  FROM tenants t
 CROSS JOIN (VALUES
   -- 中性变体：只换品牌色，底色跟随用户选的深浅色。想要「换个颜色
   -- 但别动我习惯的背景」的人用这个。
   ('stellar', 'Stellar（星蓝）',
    '{"brand":"#3b82f6","brand-2":"#2563eb","brand-soft":"rgba(59,130,246,.14)",'
    '"brand-on-soft":"#7dabfa","r-lg":"12px",'
    '"glow":"radial-gradient(1200px 500px at 50% -20%,rgba(59,130,246,.18),transparent 70%)"}',
    '{"site_name":"Stellar","tagline":"畅连全球网络"}'),

   -- 深色变体：整站压成偏蓝的近黑。
   -- 这几个层级之间的差要够小，否则卡片会像贴在背景上的白纸；
   -- 又不能太小，不然层级消失、整屏糊成一片。
   ('stellar-dark', 'Stellar 深空',
    '{"brand":"#3b82f6","brand-2":"#2563eb","brand-soft":"rgba(59,130,246,.16)",'
    '"brand-on-soft":"#7dabfa","r-lg":"12px",'
    '"bg":"#080b12","bg-elev":"#0c1119","surface":"#0f1520","surface-2":"#141c2a",'
    '"surface-3":"#1a2333",'
    '"line":"rgba(125,171,250,.10)","line-2":"rgba(125,171,250,.18)",'
    '"fg":"#e8edf6","fg-2":"#94a3b8","fg-3":"#6b7a91",'
    '"glow":"radial-gradient(1200px 500px at 50% -20%,rgba(59,130,246,.20),transparent 70%)"}',
    '{"site_name":"Stellar","tagline":"畅连全球网络"}'),

   -- 浅色变体：冷调白，不是纯白——纯白配蓝色主色会显得发灰，
   -- 底色带一点蓝反而让品牌色更立得住。
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

-- +goose Down

-- 只删这三套，且只在它们没被启用时删。
-- 正在生效的主题被迁移回滚掉，站点会当场变回默认配色，而管理员
-- 不会知道是谁动的。宁可留一行数据，也不要一次无声的换脸。
DELETE FROM site_themes
 WHERE code IN ('stellar', 'stellar-dark', 'stellar-light')
   AND is_builtin
   AND NOT is_active;

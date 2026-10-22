---
paths:
  - "panel/internal/module/appearance/**"
  - "panel/frontend/src/styles/design-tokens.ts"
  - "panel/frontend/src/styles/brand-palette.ts"
---

# 外观：主题令牌、品牌与内容插槽

- 主题、品牌、插槽都会渲染进门户、被所有访客看到，写入一律走白名单，门户读取时再按同一套白名单过滤一次（库里被手工改过的行也出不去）。
- 令牌名单以前端 `panel/frontend/src/styles/design-tokens.ts` 的 `COLOR_TOKENS` 为唯一来源，后端 `DesignTokenKeys`（`panel/internal/module/appearance/service/tokens.go`）照抄一份，`tokens_test.go` 逐条对照：前端加减颜色变量时后端白名单与内置主题种子（`00029_appearance.sql`）一起改。主题只管颜色，字号、间距、圆角不进白名单。
- 深色组跟品牌色走的 9 个令牌（`--brand`、`--brand-hover`、`--brand-ink`、`--brand-deep`、`--brand-line`、`--brand-mark`、`--brand-soft`、`--brand-tint`、`--on-brand`）由前端 `src/styles/brand-palette.ts` 的 `darkBrandTokens` 从浅色 `--brand` 推出（10-21）：后台主题编辑器保存时写进请求，门户 `pickThemeTokens` 读取时再算一次覆盖库里的值（旧主题不写迁移也对），两边同一个函数。后端只当令牌原样透传、不做颜色运算；改推导只改这一个文件与 `tests/brand-palette.test.ts`（缺省品牌色恒等、对比度不低于缺省深色组）。深色组仍不开放编辑。
- tokens 形状只能是 `{"light":{…}, "dark":{…}}`；不认识的键一律拒绝而不是静默丢掉。错误逐个标在 `tokens.<组>.<变量名>`、`branding.<键>` 上，一次回齐成一个 422。
- 站点名只有一个来源：系统设置 `site.name`（settings 模块，后台「内容与外观 → 主题与插槽」的站点设置卡改）。门户标题、入口页 `<title>`、邮件 `{{site}}` 与发件人名、订阅配置名、知识库 `{{siteName}}` 都取它，主题里没有站点名。
- branding 只有 `BrandingKeys` 里的标语一个键（主题 Logo 10-21 删了，00062 把库里的 `logo` 键去掉）；收到 `site_name` 回 422 标在 `branding.site_name`（提示去站点设置改），`logo` 与其它未知键标在 `branding`。库里旧行带的 `site_name`（如 00029 种子）由 `filterBranding` 读时丢掉。门户读取经 `filterBranding` 再过一遍。
- `PageMeta`（`service/pagemeta.go`）是入口页与门户外观用的进程内快照：站点名取 settings、标语取生效主题；成功缓存 30 秒、读失败 5 秒（沿用上次成功的值），本进程保存站点设置（settings 经 `SiteChangeListener` 回调 `SiteSettingsChanged`）、保存或激活主题后，都在提交后（`tx.AfterCommit`）立即失效，30 秒只是兜底；用 `testkit.TxCtx` 的用例里提交钩子不执行，要断言新值先手动 `invalidatePageMeta()`。`GET v1/appearance` 的顶层 `site_name` 取它，不为站点名多查一次库。
- 入口页改写：`handler/index_meta.go` 的 `IndexMeta` 把快照转成 `webapp.IndexMeta`（门户 `<title>` = 站点名，后台 = 「站点名 控制台」；图标恒用内置 `DefaultIcon`），`serve.go` 经 `webapp.WithIndexMeta` 挂到两个入口页。
  - 门户：描述 = 站点设置 `site.seo_description`，空则标语，再空用站点名；`site.seo_keywords` 非空才写 keywords；Open Graph 与 canonical 指向 settings 的 `SiteURL`（后台可改的主域名，空就用配置的 `public_base_url`；全仓读站点地址的约定见 `panel-conventions.md`「站点地址」）；`site.allow_indexing` 关着（缺省，`settingsvc.DefaultSiteAllowIndexing` 是唯一定义）时写 `robots noindex, nofollow`。
  - 后台：描述照旧（标语或站点名），恒写 `noindex, nofollow`，不写 keywords、canonical、Open Graph。
  - SEO 三项与 `SiteURL` 都在 `PageMeta` 快照里（站点设置提交后同样立即失效，改主域名也走 `SiteChangeListener`）；快照兜底值的收录开关取 `DefaultSiteAllowIndexing`。
- `/robots.txt`（`handler/robots.go`）：门户 `Robots` 允许收录时 `Allow: /$` + `Allow: /assets/` + `Disallow: /`（放行首页与静态资源：不放行 `/assets/` 搜索引擎拿不到样式和脚本、会判页面不可用），否则整站 `Disallow: /`；后台引擎挂 `RobotsDenyAll`，恒为整站 `Disallow: /`、不读设置。正文固定，不写后台前缀、订阅前缀或任何别的路径（robots.txt 公开，写进去等于公布）；不做 sitemap。
- 门户首页（10-20）：未登录访客打开门户根 `/` 时，入口页 `<div id="root">` 前有服务端渲染的首页片段（站点名、标语、介绍、在售套餐与价格、常见问题、登录 / 注册入口；只做中文）。登录 / 注册入口按 settings 的注册方式出（10-21：关闭时只给登录、套餐卡「登录后购买」，仅凭邀请码时注明需要邀请码）。站点名、标语取本模块的 `PageMeta` 快照；开关、介绍、常见问题是系统设置的 home 组（settings `home.go`，后台「内容与外观 › 主题与插槽」的首页卡）；套餐取 catalog 的匿名目录。组装在 `internal/app/portal_home.go`，渲染在 `internal/pkg/webapp/home.go`（`WithHome`），本模块不碰首页。首页开关关着时 `/` 就是登录页；收录开关（`site.allow_indexing`）照旧缺省关，与首页开关互不影响。
- 同一时刻只有一套主题生效（部分唯一索引 `site_themes_one_active` 兜底）。内置主题不能原地改，只能另存为（`Create` 为 true，撞已有 code 回 409，不覆盖别人的主题）；内置主题与生效中的主题都删不掉（`DeleteTheme`）。
- 主题没有自定义 CSS（10-14 删掉，门户 CSP 也不允许注入 `<style>`）：请求带 `custom_css` 按未知字段回 400，保存主题成功只回 `{saved:true}`；要做得先有 CSS 净化方案并过安全评审。
- 插槽 HTML 经 `SanitizeHTML` 净化后才落库，被丢掉的内容回给管理员；插槽位 `SlotCatalog` 写在代码里，与前端挂载点、迁移里 `slot_key` 的 CHECK 三处同步改。现有 5 个（10-21 删了 `portal.sidebar.extra`、`portal.footer`），CHECK 最新一次在 00062，`TestSlotCatalogMatchesMigration` 读这份迁移做集合比较；再改 CHECK 时把用例里的迁移路径换成新的。
  - 白名单（`service/sanitize.go`）：全局只放行 `class` 和 `title`，不放 `style`（能做透明层盖住支付按钮）；`class` 只留 `slot-` 开头的类名（`cleanClass`，其余类名丢掉、元素保留——门户有 `ph-` 等全局样式类，任意类名能蹭全局样式做覆盖层）
  - `img` 的 src（`safeSrc`）只放行 `/` 开头（不含 `//`）的站内路径与 `data:image/{png,jpeg,gif,webp};base64,`，与门户 CSP `img-src 'self' data:` 对齐，不放 http/https、不放 svg
  - 链接 href（`safeHref`）放行 http/https/mailto、站内路径与 `#` 锚点，不放 `data:`；地址里 `#` 后面去掉开头所有 `/` 以 `quick-login`、`mail-login` 开头的整条 href 丢掉（`isLoginLink`，防诱导访客登进别人的账号；对 http(s) 链接同样适用）
  - 后台插槽卡的副标题（`admin/screens/content/ThemeTab.tsx`）写着这几条，改规则时同步
- 读出再净化：`Public`（`GET v1/appearance`，门户每次加载都调）对每个插槽内容再过一次 `SanitizeHTML`，库里被手工改过、或规则收紧前存的内容也按现行规则出去；再净化后为空的不下发。不每请求现算：`service/slot_memo.go` 的 `slotMemo` 按插槽 key 记「原文 → 净化结果」，原文没变直接用；`SaveSlot` 在 `tx.AfterCommit(ctx, "appearance:slots", …)` 里清空（同模块的写路径，直接调，不走注入）。原文比较是兜底：用 `testkit.TxCtx` 的用例提交钩子不跑，靠它照样重算

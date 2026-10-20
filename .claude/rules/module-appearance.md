---
paths:
  - "panel/internal/module/appearance/**"
  - "panel/frontend/src/styles/design-tokens.ts"
---

# 外观：主题令牌、品牌与内容插槽

- 主题、品牌、插槽都会渲染进门户、被所有访客看到，写入一律走白名单，门户读取时再按同一套白名单过滤一次（库里被手工改过的行也出不去）。
- 令牌名单以前端 `panel/frontend/src/styles/design-tokens.ts` 的 `COLOR_TOKENS` 为唯一来源，后端 `DesignTokenKeys`（`panel/internal/module/appearance/service/tokens.go`）照抄一份，`tokens_test.go` 逐条对照：前端加减颜色变量时后端白名单与内置主题种子（`00029_appearance.sql`）一起改。主题只管颜色，字号、间距、圆角不进白名单。
- tokens 形状只能是 `{"light":{…}, "dark":{…}}`；不认识的键一律拒绝而不是静默丢掉。错误逐个标在 `tokens.<组>.<变量名>`、`branding.<键>` 上，一次回齐成一个 422。
- 站点名只有一个来源：系统设置 `site.name`（settings 模块，后台「内容与外观 → 主题与插槽」的站点设置卡改）。门户标题、入口页 `<title>`、邮件 `{{site}}` 与发件人名、订阅配置名、知识库 `{{siteName}}` 都取它，主题里没有站点名。
- branding 只有 `BrandingKeys` 里的标语、Logo 两个键；收到 `site_name` 回 422 标在 `branding.site_name`（提示去站点设置改），其它未知键标在 `branding`。库里旧行带的 `site_name`（如 00029 种子）由 `filterBranding` 读时丢掉，不写迁移。Logo 只收 `data:image/` 的 base64 且有 `MaxLogoBytes` 上限（门户 CSP 是 `img-src 'self' data:`，它随每次 `GET v1/appearance` 下发）。门户读取经 `filterBranding` 再过一遍。
- `PageMeta`（`service/pagemeta.go`）是入口页与门户外观用的进程内快照：站点名取 settings、标语与 Logo 取生效主题；成功缓存 30 秒、读失败 5 秒（沿用上次成功的值），本进程保存站点设置（settings 经 `SiteChangeListener` 回调 `SiteSettingsChanged`）、保存或激活主题后，都在提交后（`tx.AfterCommit`）立即失效，30 秒只是兜底；用 `testkit.TxCtx` 的用例里提交钩子不执行，要断言新值先手动 `invalidatePageMeta()`。`GET v1/appearance` 的顶层 `site_name` 取它，不为站点名多查一次库。
- 入口页改写：`handler/index_meta.go` 的 `IndexMeta` 把快照转成 `webapp.IndexMeta`（门户 `<title>` = 站点名，后台 = 「站点名 控制台」；描述用标语，没有就用站点名；图标用 Logo，没有用内置 `DefaultIcon`），`serve.go` 经 `webapp.WithIndexMeta` 挂到两个入口页。
- 同一时刻只有一套主题生效（部分唯一索引 `site_themes_one_active` 兜底）。内置主题不能原地改，只能另存为（`Create` 为 true，撞已有 code 回 409，不覆盖别人的主题）；内置主题与生效中的主题都删不掉（`DeleteTheme`）。
- 主题没有自定义 CSS（10-14 删掉，门户 CSP 也不允许注入 `<style>`）：请求带 `custom_css` 按未知字段回 400，保存主题成功只回 `{saved:true}`；要做得先有 CSS 净化方案并过安全评审。
- 插槽 HTML 经 `SanitizeHTML` 净化后才落库，被丢掉的内容回给管理员；全局只放行 `class` 和 `title`，不放 `style`（能做透明层盖住支付按钮）。插槽位 `SlotCatalog` 写在代码里，与前端挂载点、迁移里 `slot_key` 的 CHECK 三处同步改。

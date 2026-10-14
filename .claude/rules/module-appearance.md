---
paths:
  - "panel/internal/module/appearance/**"
  - "panel/frontend/src/styles/design-tokens.ts"
---

# 外观：主题令牌、品牌与内容插槽

- 主题、品牌、插槽都会渲染进门户、被所有访客看到，写入一律走白名单，门户读取时再按同一套白名单过滤一次（库里被手工改过的行也出不去）。
- 令牌名单以前端 `panel/frontend/src/styles/design-tokens.ts` 的 `COLOR_TOKENS` 为唯一来源，后端 `DesignTokenKeys`（`panel/internal/module/appearance/service/tokens.go`）照抄一份，`tokens_test.go` 逐条对照：前端加减颜色变量时后端白名单与内置主题种子（`00029_appearance.sql`）一起改。主题只管颜色，字号、间距、圆角不进白名单。
- tokens 形状只能是 `{"light":{…}, "dark":{…}}`；不认识的键一律拒绝而不是静默丢掉。错误逐个标在 `tokens.<组>.<变量名>`、`branding.<键>` 上，一次回齐成一个 422。
- branding 只有 `BrandingKeys` 里的站点名（必填）、标语、Logo 三个键；Logo 只收 `data:image/` 的 base64 且有 `MaxLogoBytes` 上限（门户 CSP 是 `img-src 'self' data:`，它随每次 `GET v1/appearance` 下发）。门户读取经 `filterBranding` 再过一遍。
- 同一时刻只有一套主题生效（部分唯一索引 `site_themes_one_active` 兜底）。内置主题不能原地改，只能另存为（`Create` 为 true，撞已有 code 回 409，不覆盖别人的主题）；内置主题与生效中的主题都删不掉（`DeleteTheme`）。
- 自定义 CSS 已停用（门户 CSP 不允许注入 `<style>`），`Theme` 里没有这一项。
- 插槽 HTML 经 `SanitizeHTML` 净化后才落库，被丢掉的内容回给管理员；全局只放行 `class` 和 `title`，不放 `style`（能做透明层盖住支付按钮）。插槽位 `SlotCatalog` 写在代码里，与前端挂载点、迁移里 `slot_key` 的 CHECK 三处同步改。
- 邮件里的 `{{site}}` 与发件人名取 settings 的站点名（`panel/internal/module/notify/service/mailer.go`），不是主题 branding 里的站点名；两处是分开配的。

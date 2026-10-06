---
paths:
  - "panel/internal/domain/appearance/**"
  - "panel/frontend/src/styles/design-tokens.ts"
---

# 外观：主题令牌与站点品牌

- 主题令牌的名单与取值以前端 `panel/frontend/src/styles/design-tokens.ts` 的 `COLOR_TOKENS` 为唯一来源，后端 `DesignTokenKeys` 照抄一份白名单（light / dark 两组）。前端加减颜色变量时后端白名单与内置主题迁移要一起改（守卫 `paper_theme_test.go:TestDesignTokenKeysMatchFrontend`、`TestPaperThemeSeedMatchesFrontend`）。主题只管颜色，字号、间距、圆角不进白名单
- tokens 与 branding 都是保存时严格校验（所有字段错误按 `tokens.<组>.<键>`、`branding.<键>` 一次回齐成一个 422），门户读取时再按白名单过滤一遍，门户永远拿不到白名单外的键
- branding 只有 site_name（必填）、tagline、logo（小尺寸 data:image）三个键，未知键拒绝
- 站点名只有一处来源：生效主题的 `branding.site_name`，经 `SiteNameTx` 读；notify 的 `{{site}}` 和发件人名都走它，不要另存一份站点名
- 同一时刻只有一套主题生效（部分唯一索引兜底）；内置主题不可原地改，想改就另存为（Create=true，撞已有 code 回 409，不覆盖别人的主题）；生效中的主题与内置主题都删不掉
- custom_css 本期停用（新门户的 CSP 不允许注入 `<style>`）：保存非空即拒、读取不下发。`SanitizeCSS` 暂无生产调用方，恢复自定义 CSS 时复用它

# panel/internal/domain/appearance/
> L2 | 父级: /panel/internal/domain/CLAUDE.md

外观域：主题（配色令牌 + 站点品牌）与门户插槽。主题令牌的名单与取值以前端 panel/frontend/src/styles/design-tokens.ts 为唯一来源（43 个颜色变量，light / dark 两组），后端照抄一份白名单并由测试逐条对照；保存时严格校验、门户读取时再过滤一遍，门户永远拿不到白名单外的键。custom_css 本期停用（保存拒绝、读取不下发）。站点名只有一处来源：生效主题的 branding.site_name，notify 的 {{site}} 与发件人名都经 SiteNameTx 读它。

成员清单
service.go: 主题与插槽读写；Public 一次取齐生效主题（tokens 过滤、custom_css 置空）与启用插槽；SaveTheme 先按表 CHECK 与令牌白名单校验再入库（缺陷 20），内置主题不可原地改；激活、删除、插槽保存都写审计
tokens.go: DesignTokenKeys 白名单与 light/dark 分组、normalizeTokens（保存校验）与 filterTokens（读取过滤）、DefaultSiteName 与 SiteNameTx
sanitize.go: 插槽 HTML 白名单净化；SanitizeCSS 随 custom_css 停用暂无生产调用方，恢复自定义 CSS 时复用
*_test.go: theme_seed_test.go 守 00051/00055 历史种子能被旧前端接受；paper_theme_test.go 守 00075「默认 · 纸白」与 design-tokens.ts 逐字一致、Down 原值恢复、保存校验；theme_pg18_test.go 为 PG18 集成测试（run-pg18-gates.sh 的 appearance 域）

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

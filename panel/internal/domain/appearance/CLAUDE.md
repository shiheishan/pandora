# panel/internal/domain/appearance/
> L2 | 父级: /panel/internal/domain/CLAUDE.md

外观域：主题（配色令牌 + 站点品牌）与门户插槽。租户可以有多套主题（内置「默认 · 纸白」+ 管理员新建的自定义主题），同一时刻只有一套生效（部分唯一索引兜底），切换即换门户配色与站点名。主题令牌的名单与取值以前端 panel/frontend/src/styles/design-tokens.ts 为唯一来源（43 个颜色变量，light / dark 两组），后端照抄一份白名单并由测试逐条对照；branding 只有站点名（必填）、标语、Logo（data:image/ 小图）三个键。两者都是保存时严格校验（字段错误逐个标在 tokens.<组>.<键>、branding.<键> 上，一次回齐）、门户读取时再过滤一遍，门户永远拿不到白名单外的东西。新建 / 另存为带 Create：撞已有 code 回 409，不覆盖别人的主题；内置主题不可原地改，生效中与内置主题删不掉。custom_css 本期停用（保存拒绝、读取不下发）。站点名只有一处来源：生效主题的 branding.site_name，notify 的 {{site}} 与发件人名都经 SiteNameTx 读它。

成员清单
service.go: 主题与插槽读写；Public 一次取齐生效主题（tokens 与 branding 过滤、custom_css 置空）与启用插槽；SaveTheme 先按表 CHECK、令牌白名单与品牌规则校验、全部字段错误合进一个 422 再入库（缺陷 20），Create 撞已有 code 409，内置主题不可原地改；激活、删除、插槽保存都写审计
tokens.go: DesignTokenKeys 白名单与 light/dark 分组、normalizeTokens（保存校验，取值错误逐键标 tokens.<组>.<键>）与 filterTokens（读取过滤）、DefaultSiteName 与 SiteNameTx
branding.go: BrandingKeys（site_name / tagline / logo）、normalizeBranding（站点名必填 ≤40、标语 ≤80、Logo 只收 48KB 内的 PNG / JPEG / WebP / SVG data URL，未知键拒绝）与 filterBranding（门户读取只放行三个已知键）
sanitize.go: 插槽 HTML 白名单净化；SanitizeCSS 随 custom_css 停用暂无生产调用方，恢复自定义 CSS 时复用
*_test.go: theme_seed_test.go 守 00051/00055 历史种子满足当年前端的校验规则；paper_theme_test.go 守 00075「默认 · 纸白」与 design-tokens.ts 逐字一致、Down 原值恢复、保存校验与逐键字段错误；branding_test.go 守品牌规则、读取过滤与字段错误一次回齐；theme_pg18_test.go（迁移后状态、内置不可改、Down）与 theme_switch_pg18_test.go（另存为不覆盖 409、编辑、激活换门户令牌与 SiteNameTx、删除守卫、审计）为 PG18 集成测试（run-pg18-gates.sh 的 appearance 域，不带 -run 过滤，包内全跑）

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

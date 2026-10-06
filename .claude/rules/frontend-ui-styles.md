---
paths:
  - "panel/frontend/src/ui/**"
  - "panel/frontend/src/styles/**"
  - "panel/frontend/src/showcase/**"
---

# 面板前端：组件库与设计令牌

- 设计稿 `设计规范.dc.html` 是唯一依据：颜色、字号、间距、圆角、控件高度都先落成令牌，组件（CSS Modules）只引用令牌，不写色值
- 两个入口的差异（门户用朱砂、后台保持墨色中性，控件高度、圆角、字号等）全部来自 `styles/roles.css` 的同名角色令牌，按 `<html data-app>` 切换
  - 组件里不写 admin / portal 分支
  - 确实需要入口选择器时用 `:root[data-app='…']`。现有两处：`Button.module.css` 的后台小号按钮字号，`Toast.module.css` 的门户窄屏上移
- `ui/*.module.css` 只能引用全局令牌或本文件声明的局部变量。守卫：`tests/tokens.test.ts` 的 "ui/%s only references declared custom properties"
- 样式一律用 CSS Modules；React 的 `style` 属性只用于真正动态的尺寸（列宽、抽屉宽度、骨架尺寸）
- 弹层用浏览器原生能力，不自己造
  - `Modal` / `Drawer` 基于 `<dialog>.showModal()`
  - Toast 容器是 popover，每条提示都要重新推到顶层，否则会被已打开的 dialog 盖住
- 页面只从 `ui/index.ts` import。`cx`、`useModalDialog`、`Field` 是内部实现，不导出
- 颜色令牌（43 个）有四处同源，增、删、改任何一个都要几处一起改：
  - `styles/tokens.css`：浅色与 `[data-theme='dark']` 两组的键必须完全相同
  - `styles/design-tokens.ts` 的 `COLOR_TOKENS`：`tests/tokens.test.ts` 逐值核对 CSS 文本，showcase 核对浏览器算出的值
  - Go 端 `panel/internal/domain/appearance/tokens.go` 的 `DesignTokenKeys`，守卫是 `paper_theme_test.go` 的 `TestDesignTokenKeysMatchFrontend`
  - 内置「默认 · 纸白」主题的种子 `panel/migrations/00075_theme_paper_white.sql`，守卫是同文件的 `TestPaperThemeSeedMatchesFrontend`
- 上面两个 Go 测试用正则逐行解析 `design-tokens.ts` 里 `c('组', '--名', '浅', '深', '用途'),` 这种写法，并期望正好 43 个。改写法或改个数要同步改 Go 那边的解析
- `COLOR_TOKENS` 同时是门户主题覆盖的白名单（`portal/appearance.ts` 的 `THEMEABLE_TOKENS`）和后台主题编辑器的键集。主题只覆盖颜色：字号、间距、圆角不进白名单，以免换主题让排版走样
- `styles/index.css` 的 @import 顺序是 字体 → 令牌 → 角色 → 基础，角色令牌引用前面的令牌
- 字体只用 `styles/fonts/` 里随包自带的 Geist woff2 子集，不走 Google Fonts。守卫：`tests/tokens.test.ts` 的 "every @font-face source is a bundled file"
- 外框与组件只用 960 与 640 两个断点，写作 `max-width: 959px` / `639px`
- 令牌与组件的验收在 showcase 里做（`npm run dev:showcase`，只存在于 dev）

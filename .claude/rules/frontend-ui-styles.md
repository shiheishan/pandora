---
paths:
  - "panel/frontend/src/ui/**"
  - "panel/frontend/src/styles/**"
  - "panel/frontend/src/showcase/**"
---

# 面板前端：组件库与设计令牌

- 设计稿 `设计规范.dc.html` 是唯一依据：颜色、字号、间距、圆角、控件高度都先落成令牌，组件（CSS Modules）只引用令牌，不写色值
- 两个入口的差异（门户用森林绿、后台保持墨色中性，控件高度、圆角、字号、输入框边框 `--control-border-width` / `--control-bg`、浮层 `--popover-border` / `--popover-shadow`、字段标签 `--label-*` 等）全部来自 `styles/roles.css` 的同名角色令牌，按 `<html data-app>` 切换；角色令牌与 `design-tokens.ts` 的 `ROLE_TOKENS` 同源，两个入口的键必须相同
  - 组件里不写 admin / portal 分支
  - 确实需要入口选择器时用 `:root[data-app='…']`。现有两处：`Button.module.css` 的后台小号按钮字号，`Toast.module.css` 的门户窄屏上移
- 后台（10-16 设计稿）在全局令牌上另有覆盖：`roles.css` 的 `html[data-app='admin']`（与 `[data-theme='dark']`）两块，把 `--surface*`、`--border`、`--border-strong` 换成设计稿的值（卡片与页面同底、层次靠线），所有 `--radius-*` 归零（直角）；值与 `design-tokens.ts` 的 `ADMIN_OVERRIDES` / `ADMIN_RADIUS` 同源（`tests/tokens.test.ts` 的 "admin overrides"，showcase 在后台模式下照覆盖值核对）。这些不进 `COLOR_TOKENS`，与 Go 端、门户主题无关
  - 选择器用 `html[...]`：要压过 `:root`，又要输给 `:root[data-theme='dark']`
  - 10-16 全局颜色换成门户森林绿，后台还没重做：凡是值变了的颜色键都在这两块里钉回原来的「纸白」（`ADMIN_OVERRIDES` 同步），后台外观不变；以后重做后台时再删
  - 后台的浮层统一墨色细框、无阴影，输入框只有下边线、聚焦把下边线描黑；组件里写 `var(--popover-border)` 等角色令牌，不写 admin 分支
- `ui/*.module.css` 只能引用全局令牌或本文件声明的局部变量。守卫：`tests/tokens.test.ts` 的 "ui/%s only references declared custom properties"
- 样式一律用 CSS Modules；React 的 `style` 属性只用于真正动态的尺寸（列宽、抽屉宽度、骨架尺寸）
- 弹层用浏览器原生能力，不自己造
  - `Modal` / `Drawer` 基于 `<dialog>.showModal()`
  - 例外：`Select` 是自绘的 listbox（10-16 设计稿），列表 `position: fixed` 就地渲染在组件里、不 portal 到 body（在 `<dialog>` 里才留在顶层）；Esc 收起时拦住事件，不让外层弹窗跟着关
  - Toast 容器是 popover，每条提示都要重新推到顶层，否则会被已打开的 dialog 盖住
- 10-16 新增的共用组件：`FilterTabs`（radiogroup + 可移动 tabindex）、`Toolbar` / `SearchField`、`Table` 的排序与表尾、`Modal` 的定宽与头体底结构；`Select` 的纯逻辑（方向键、首字母、定位）在 `ui/select-logic.ts`，配单测，组件只管摆放与事件
- 页面只从 `ui/index.ts` import。`cx`、`useModalDialog`、`Field` 是内部实现，不导出
- 颜色令牌（48 个，10-16 起默认是门户森林绿）有四处同源，增、删、改任何一个都要几处一起改：
  - `styles/tokens.css`：浅色与 `[data-theme='dark']` 两组的键必须完全相同
  - `styles/design-tokens.ts` 的 `COLOR_TOKENS`：`tests/tokens.test.ts` 逐值核对 CSS 文本，showcase 核对浏览器算出的值
  - Go 端 `panel/internal/module/appearance/service/tokens.go` 的 `DesignTokenKeys`，守卫是同目录 `tokens_test.go` 的 `TestDesignTokenKeysMatchFrontend`
  - 内置「默认 · 森林绿」主题（code `paper`）的种子在 `panel/db/migrations/00029_appearance.sql`，守卫是 `tokens_test.go` 的 `TestPaperThemeSeedMatchesFrontend`
- 上面两个 Go 测试用正则逐行解析 `design-tokens.ts` 里 `c('组', '--名', '浅', '深', '用途'),` 这种写法，并期望正好 48 个。改写法或改个数要同步改 Go 那边的解析
- `COLOR_TOKENS` 同时是门户主题覆盖的白名单（`portal/appearance.ts` 的 `THEMEABLE_TOKENS`）和后台主题编辑器的键集。主题只覆盖颜色：字号、间距、圆角不进白名单，以免换主题让排版走样
- `styles/index.css` 的 @import 顺序是 字体 → 令牌 → 角色 → 基础，角色令牌引用前面的令牌
- 字体只用 `styles/fonts/` 里随包自带的 woff2，不走 Google Fonts。守卫：`tests/tokens.test.ts` 的 "every @font-face source is a bundled file" 与 "every display @font-face source…"
  - Geist（`fonts.css`，两个入口）：后台正文与等宽
  - 门户展示字体（`display-fonts.css`，只由 `portal/main.tsx` 引入）：Courier Prime 400/700（`--font-display`：大号数字、金额、Logo）与 Noto Serif SC 900（`--font-serif`：档位名）。档位名是任意中文，Noto Serif SC 照 fontsource 分成 101 片 unicode-range，浏览器只下载用到的片；都是 OFL，许可证随文件放
  - 正文字体走角色令牌 `--font-ui`：门户是系统字体栈 `--font-system`（样稿 v5），后台是 Geist `--font-sans`
- 外框与组件只用 960 与 640 两个断点，写作 `max-width: 959px` / `639px`
- 令牌与组件的验收在 showcase 里做（`npm run dev:showcase`，只存在于 dev）

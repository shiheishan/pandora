# panel/frontend/src/styles/
> L2 | 父级: /panel/frontend/CLAUDE.md

全局样式与设计令牌，门户、后台与 showcase 三个入口共用。设计稿 设计规范.dc.html 是唯一依据：颜色、字号、间距、圆角、控件高度都先落成令牌，组件（第 ④ 步起的 CSS Modules）只引用令牌、不写色值。
分三层：tokens.css 是设计稿原值（明暗两组语义色 + 不随主题变化的尺度）；roles.css 把「门户用朱砂标主要操作、后台保持墨色中性」收成同名角色令牌（--accent、--h-control、--radius-control …），按 <html data-app> 切换，于是一套组件同时服务两个入口；base.css 是元素默认样式。主题由 <html data-theme="light|dark"> 决定，写入方是 core/theme-boot.js 与 core/theme.ts。
CSP 约束：style-src 'self' 无 unsafe-inline，只用静态 CSS 文件与 CSS Modules；font-src 'self'，字体随包自带、不走 Google Fonts，vite.config.ts 把 assetsInlineLimit 设 0 保证不被内联成 data:。

成员清单
index.css: 全局样式唯一入口，按 字体 → 令牌 → 角色 → 基础 的顺序 @import，被三个入口的 main.tsx 各引入一次
fonts.css: @font-face 声明 Geist 与 Geist Mono，各两个可变字体子集（latin 常驻、latin-ext 按 unicode-range 按需），字重 400–600 共用一个文件
fonts/: Geist / Geist Mono 的 woff2 子集（取自 @fontsource-variable/geist 与 geist-mono 5.3.0 的 latin、latin-ext normal 文件）与 OFL.txt；OFL 未声明保留字体名，子集可沿用 Geist 字族名
tokens.css: 语义色 43 个（与设计稿模块文件 html:root 块逐字一致的 33 个 + 规范页补充的危险按钮字、Toast 圆点、分段阴影、后台侧栏 10 个），[data-theme='dark'] 覆盖同一组键；字体栈、八档字号、字距行高、八档间距、六档圆角、布局尺寸；< 640 把 --gutter 收到 16
roles.css: [data-app='portal'|'admin'] 两组角色令牌：强调色与其上字色、输入框聚焦外圈、键盘焦点色、链接样式、正文字号行高（14/1.55 与 13/1.5）、控件三档高度（40/36/32 与 32/28/24）、按钮与输入框水平内边距、弹窗按钮高度（36 与 32）、开关选中圆钮色（白与页面底色）、控件与卡片圆角、卡片内边距
base.css: 盒模型、页面底色与正文、表单控件继承字体、链接、选区、:focus-visible 外圈、后台窄滚动条、.mono 与 .num 工具类、减弱动态效果
design-tokens.ts: 设计稿原值的 TS 转写（COLOR_TOKENS / TYPE_SCALE / SPACE_SCALE / RADIUS_SCALE / ROLE_TOKENS）与 normalizeCssValue；tests/tokens.test.ts 用它逐值核对 CSS 文本，showcase 用它核对浏览器计算值——改令牌要两边一起改，测试会拦住只改一边

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

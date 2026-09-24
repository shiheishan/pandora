# panel/frontend/src/showcase/
> L2 | 父级: /panel/frontend/CLAUDE.md

只在开发时存在的演示页：npm run dev:showcase（vite --mode showcase）打开，vite.config.ts 在 build 时直接拒绝这个 mode，所以它永远进不了嵌入产物。用途是对照设计稿验收令牌与组件：页面顶部可切主题（浅色/深色）与入口（门户/后台，即 <html data-app>），并实时把浏览器算出的令牌值与 styles/design-tokens.ts 的设计稿原值逐条比对，显示「N 个令牌与设计稿一致」或列出不一致项。

成员清单
index.html: showcase 入口，pandora-app 标记为 showcase、默认 data-app="portal"
main.tsx: 引入全局样式并挂载 Showcase
Showcase.tsx: 按设计规范 02 颜色、03 字体、04 尺寸铺开令牌，05 展示当前入口的角色令牌；第 ④ 步的组件演示接在这里
Showcase.module.css: 演示页排版，照搬设计规范页的版式，只用令牌

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

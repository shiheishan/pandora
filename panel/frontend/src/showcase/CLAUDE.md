# panel/frontend/src/showcase/
> L2 | 父级: /panel/frontend/CLAUDE.md

只在开发时存在的演示页：npm run dev:showcase（vite --mode showcase）打开，vite.config.ts 在 build 时直接拒绝这个 mode，所以它永远进不了嵌入产物。用途是对照设计稿验收令牌与组件：页面顶部可切主题（浅色/深色）与入口（门户/后台，即 <html data-app>），并实时把浏览器算出的令牌值与 styles/design-tokens.ts 的设计稿原值逐条比对，显示「N 个令牌与设计稿一致」或列出不一致项。

成员清单
index.html: showcase 入口，pandora-app 标记为 showcase、默认 data-app="portal"
main.tsx: 引入全局样式，在 ToastProvider 里挂载 Showcase
Showcase.tsx: 按设计规范 02 颜色、03 字体、04 尺寸铺开令牌，05 展示当前入口的角色令牌，06 挂组件演示；顶部的主题与入口切换用 ui/Segmented
ComponentsDemo.tsx: 组件演示区，按规范 05「组件」顺序列出每个组件的全部变体与状态（表格可切有数据/加载中/空；普通弹窗、危险确认、重新验证身份、抽屉；弹窗开着时发 Toast 验证层级；头像菜单带深色模式开关）
Showcase.module.css: 演示页排版，照搬设计规范页的版式，只用令牌；< 640 时演示行改单列

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

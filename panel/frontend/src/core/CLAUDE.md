# panel/frontend/src/core/
> L2 | 父级: /panel/frontend/CLAUDE.md

与界面无关的前端底层，两个入口共用：主题状态，以及第 ⑤ 步起的唯一 HTTP 出口 api.ts、SSE、hash 路由。这里的模块不 import 组件，组件与页面 import 它们。

成员清单
theme-boot.js: 首帧前的主题引导，经典脚本（非 module），读 localStorage 的 pandora-theme 写 <html data-theme>；由 vite.config.ts 的 themeBoot 插件按内容哈希输出到 assets/ 并注入到 <meta charset> 之后，因为 CSP 禁止内联脚本、而 module 脚本延迟执行会让深色用户闪白
theme.ts: 主题状态，真相在 <html data-theme> 上不另存 React 状态；setTheme 写存储并改属性、toggleTheme、subscribeTheme 同步其它标签页（storage 事件）、useTheme 经 useSyncExternalStore 供组件订阅；只有 light / dark，非 dark 一律按 light
theme.test.ts: theme.ts 的单元测试，伪造 document / window 覆盖解析、持久化、存储不可用、订阅与跨标签页同步

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

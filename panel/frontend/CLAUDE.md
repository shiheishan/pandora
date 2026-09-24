# panel/frontend/
> L2 | 父级: /panel/CLAUDE.md

面板前端源码：管理后台（admin）与用户门户（portal）按 Claude Design 设计稿完全重写，不复用任何旧前端代码，后端行为一律以 Go 代码为准，接口以 panel/docs/redesign/api-contract.md 为准。
一个工程两个入口，但分两次构建：vite --mode admin|portal 各以 src/<app> 为根、产物写到 dist/<app>，因为两个网关各自只下发自己目录下的 / 与 /assets/*，一次多入口构建会让两边共用一个 assets/。make frontend-embed 把 dist/{admin,portal} 同步进 panel/web/{admin,portal}，panel/web/app_test.go 对真实产物做最终裁决。
部署前提（违反即上线白屏）：base './' 且所有请求走相对路径，后台在 nginx 高熵前缀后面；CSP 为 script-src/style-src 'self' 无 unsafe-inline，禁止内联脚本、内联 <style> 与运行时注入样式的 CSS-in-JS，样式只用 CSS 文件或 CSS Modules；hash 路由，缺失资源后端一律 404；认证是 Bearer 头，SSE 只能用 fetch 流读。
依赖极少：运行时只有 react / react-dom（react-query、zod 随底层接入），不用任何 UI 组件库，Geist 字体（OFL）以 woff2 子集随包自带；版本全部精确锁定。typescript 停在 6.0.x，因为 typescript-eslint 8 只支持 <6.1。

成员清单
package.json: 脚本入口 dev:admin/dev:portal/dev:showcase（演示页，仅 dev）、lint、typecheck（应用与 node 两个 tsconfig）、test（vitest run）、build（两次 --mode 构建）、check（前四者串联，make frontend-check 调用）；engines node >=22.12
package-lock.json: npm ci 的锁文件，CI 与 frontend-embed 都只走 npm ci
vite.config.ts: mode 即入口，未知 mode 直接报错，showcase 只许 dev、build 时拒绝；base './'、publicDir 关闭、不产 manifest、assetsInlineLimit 0（CSP 不放行字体 data:）；themeBoot 插件把 src/core/theme-boot.js 按内容哈希输出到 assets/ 并注入 <meta charset> 之后；vitest 在 test mode 下以工程根为根
tsconfig.json: 浏览器侧 src/ 的严格类型检查（bundler 解析、react-jsx、noUncheckedIndexedAccess），types 只有 vite/client
tsconfig.node.json: vite.config.ts 与 tests/ 的 node 侧类型检查（lib 带 DOM，测试要伪造浏览器对象），与浏览器侧隔开，node 类型不漏进应用代码
eslint.config.js: flat config，JS/TS 推荐规则 + React Hooks 规则，src/ 的 ts/tsx/js 用浏览器全局、配置与 tests/ 用 node 全局
.gitignore: node_modules/ 与 dist/ 不入库
src/admin/: 管理后台入口，index.html 带 pandora-app=admin 标记与 <html data-app="admin">（角色令牌据此切到墨色中性），main.tsx 引入全局样式并挂载 App.tsx；目前是空壳
src/portal/: 用户门户入口，结构与 admin 对称，标记 pandora-app=portal 与 data-app="portal"
src/styles/: 全局样式与设计令牌：Geist 字体、明暗两组语义色、尺度、门户/后台角色令牌、元素默认样式，以及供测试与演示页核对的设计稿原值；见 src/styles/CLAUDE.md
src/ui/: 自研组件库（按钮、表单控件、标签、卡片、表格、标签页、分段、弹窗与底部抽屉、侧边抽屉、Toast、菜单、骨架、空状态），一套实现经角色令牌服务两个入口，弹层基于原生 <dialog> 与 popover；见 src/ui/CLAUDE.md
src/core/: 与界面无关的底层：首帧前主题引导脚本与主题状态（第 ⑤ 步起加 api.ts、SSE、路由）；见 src/core/CLAUDE.md
src/showcase/: 只在 dev 存在的令牌与组件演示页，浏览器内逐条核对令牌与设计稿；见 src/showcase/CLAUDE.md
tests/entries.test.ts: 入口源文件契约——域标记正确、只有外链 module script、无内联样式；与 panel/web/app_test.go 同一组前提，前移到 npm test 暴露
tests/tokens.test.ts: 令牌契约——tokens.css / roles.css 与设计稿逐值一致、明暗两组键相同、所有 var() 都有定义、样式不引用外部来源、字体文件都在包内
tests/theme-boot.test.ts: 用 node:vm 执行引导脚本覆盖各种存储状态，核对它与 theme.ts 同键；vite 配置拒绝构建 showcase 与未知 mode、引导脚本带内容哈希、不内联资源

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

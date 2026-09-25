# panel/frontend/
> L2 | 父级: /panel/CLAUDE.md

面板前端源码：管理后台（admin）与用户门户（portal）按 Claude Design 设计稿完全重写，不复用任何旧前端代码，后端行为一律以 Go 代码为准，接口以 panel/docs/redesign/api-contract.md 为准。
一个工程两个入口，但分两次构建：vite --mode admin|portal 各以 src/<app> 为根、产物写到 dist/<app>，因为两个网关各自只下发自己目录下的 / 与 /assets/*，一次多入口构建会让两边共用一个 assets/。make frontend-embed 把 dist/{admin,portal} 同步进 panel/web/{admin,portal}，panel/web/app_test.go 对真实产物做最终裁决。
部署前提（违反即上线白屏）：base './' 且所有请求走相对路径，后台在 nginx 高熵前缀后面；CSP 为 script-src/style-src 'self' 无 unsafe-inline，禁止内联脚本、内联 <style> 与运行时注入样式的 CSS-in-JS，样式只用 CSS 文件或 CSS Modules；hash 路由，缺失资源后端一律 404；认证是 Bearer 头，SSE 只能用 fetch 流读。
依赖极少：运行时只有 react / react-dom / @tanstack/react-query / zod（zod 关掉 JIT 以免触犯无 unsafe-eval 的 CSP），不用任何 UI 组件库，Geist 字体（OFL）以 woff2 子集随包自带；版本全部精确锁定。typescript 停在 6.0.x，因为 typescript-eslint 8 只支持 <6.1。

成员清单
package.json: 脚本入口 dev:admin/dev:portal/dev:showcase（演示页，仅 dev）、lint、typecheck（应用与 node 两个 tsconfig）、test（vitest run）、build（两次 --mode 构建）、check（前四者串联，make frontend-check 调用）；engines node >=22.12
package-lock.json: npm ci 的锁文件，CI 与 frontend-embed 都只走 npm ci
vite.config.ts: mode 即入口，未知 mode 直接报错，showcase 只许 dev、build 时拒绝；base './'、publicDir 关闭、不产 manifest、assetsInlineLimit 0（CSP 不放行字体 data:）；themeBoot 插件把 src/core/theme-boot.js 按内容哈希输出到 assets/ 并注入 <meta charset> 之后；define __APP_RELEASE__ 取 PANDORA_RELEASE（缺省 dev）；serve 时 /v1 走 PANDORA_API 代理，未设则挂 dev/mock-api；vitest 在 test mode 下以工程根为根
tsconfig.json: 浏览器侧 src/ 的严格类型检查（bundler 解析、react-jsx、noUncheckedIndexedAccess），types 只有 vite/client
tsconfig.node.json: vite.config.ts、tests/ 与 dev/ 的 node 侧类型检查（lib 带 DOM，测试要伪造浏览器对象；允许 .ts 扩展名导入，vite 原生配置加载要求），与浏览器侧隔开，node 类型不漏进应用代码
eslint.config.js: flat config，JS/TS 推荐规则 + React Hooks 规则，src/ 的 ts/tsx/js 用浏览器全局、配置与 tests/、dev/ 用 node 全局
.gitignore: node_modules/ 与 dist/ 不入库
src/admin/: 管理后台入口与外框：登录页、深色侧栏 + 顶栏 + 页头标签、⌘K、实时事件、改密码、常驻 reauth 对话框（接到 api 的 requestReauth）；入口按 GET v1/me 的权限码隐藏；十个模块页经 screens/ 登记表懒加载；见 src/admin/CLAUDE.md
src/portal/: 用户门户入口与外框：登录 / 两步注册 / 快捷登录、顶栏导航与头像菜单、< 640 底部标签栏、邀请与快捷登录链接、外观令牌；十一个页面经 screens/ 登记表懒加载；见 src/portal/CLAUDE.md
src/shell/: 两个入口共用的外框底座：每入口一份运行时（令牌 + api + QueryClient）、登录态、退出、实时事件、Logo、页面容器 ScreenFrame（Suspense + 错误边界）；见 src/shell/CLAUDE.md
src/env.d.ts: 构建期常量 __APP_RELEASE__ 的类型声明
dev/: 只在 vite serve 存在的假后端：外壳 mock-api.ts + 按入口拆分的模块假接口 mock/，本机无 PostgreSQL 时在浏览器里按契约走通外壳与各页面；见 dev/CLAUDE.md
src/styles/: 全局样式与设计令牌：Geist 字体、明暗两组语义色、尺度、门户/后台角色令牌、元素默认样式，以及供测试与演示页核对的设计稿原值；见 src/styles/CLAUDE.md
src/ui/: 自研组件库（按钮、表单控件、标签、卡片、表格、标签页、分段、弹窗与底部抽屉、侧边抽屉、Toast、菜单、骨架、空状态），一套实现经角色令牌服务两个入口，弹层基于原生 <dialog> 与 popover；见 src/ui/CLAUDE.md
src/core/: 与界面无关的底层：主题引导与状态、唯一 HTTP 出口 api.ts（相对 v1/ 路径、Bearer、错误信封、幂等键、reauth 重放、非 JSON 响应的 requestRaw）、文件下载 download.ts、令牌存储、fetch 流 SSE、react-query 客户端与实时失效、hash 路由、金额 / 计数 / 字节 / 时间格式化；见 src/core/CLAUDE.md
src/showcase/: 只在 dev 存在的令牌与组件演示页，浏览器内逐条核对令牌与设计稿；见 src/showcase/CLAUDE.md
tests/entries.test.ts: 入口源文件契约——域标记正确、只有外链 module script、无内联样式；与 panel/web/app_test.go 同一组前提，前移到 npm test 暴露
tests/tokens.test.ts: 令牌契约——tokens.css / roles.css 与设计稿逐值一致、明暗两组键相同、所有 var() 都有定义、样式不引用外部来源、字体文件都在包内
tests/mock-helpers.ts: 假后端测试共用辅助——serve 把 mockApi 挂到本地 HTTP 服务（非 API 路径回 418 代表交给 vite）、close、loginAs、bearer、mockFetch（可选请求体与幂等键）；每个测试文件各起各的服务，模块假数据按文件隔离
tests/mock-api.test.ts: 假后端外壳守卫——matchPattern；外壳接口、模块分发、权限 404 先于 reauth、reauth 不消耗幂等键、同键重放与换请求 409、只重放 2xx（4xx 后同键重新执行，条件改好即成功）（调账打在真实种子用户上，余额经详情接口核对、重放不再记账，种子外的 id 回 404）
tests/mock-admin-users.test.ts: 用户第 ④ 步假接口——流量重置先 reauth、清零与日志、重放、无生效订阅 422，批量预览 / 导出 / 生成同一份名单，用户组删除 409，设备模式校验
tests/mock-admin-plans.test.ts: 套餐假接口——目录能被页面 schema 接住、向导单事务新建与幂等重放、编辑向导的 null = 不动与开新版本、草稿版本全流程、价格与销售开关 503、流量包 updated_at 乐观锁
tests/mock-admin-marketing.test.ts: 营销假接口——礼品卡掩码、一次性导出（非 JSON 重放不带 Content-Disposition）与未知字段 400
tests/mock-admin-nodes.test.ts: 节点与服务器假接口——节点列表能被页面 schema 接住、复制出新节点、非法状态边与已部署节点迁移回 409、协议按 schema 校验；服务器 schema、状态机与进入 ready 的前提、PATCH 清空与容量下限、删除仅草稿或已退役并级联静默、安装令牌幂等；节点池新建 / 编辑 / 删除守卫；全局路由 revision 冲突、删除被引用出站 409、匹配类型校验与发布
tests/mock-admin-content.test.ts: 内容与外观假接口——只读账号整块 404、公告状态机与版本冲突、知识库新版本归档同受众旧版与重复归档、内置主题 43 键、插槽净化与空内容 dropped 为 null、站点时区校验
tests/mock-admin-system.test.ts: 通知与插件假接口——只读账号只开放模板、SMTP 整体覆盖与密码保留 / 清空、注册校验、Telegram chat id 缺省不改与测试回落、模板变量白名单 / 预览 / 恢复默认 / 测试信要 reauth、钩子 upsert 一次性密钥、内网地址与未知事件 422、越界 500、投递记录 null、删除 404
tests/mock-portal.test.ts: 门户假接口——外框读接口来自各页面模块、快捷登录令牌一次性往返
tests/mock-admin-billing.test.ts: 订单与收款假接口守卫（后台前端一第 ⑥ 步；起服务与发请求用 mock-helpers，登录与 reauth 辅助留在文件内）：只读账号只看得到订单列表；订单 schema、多值状态、user_id 与用户详情同一份；人工开单先 reauth、201 重放、三种结算与拒绝项；标记已支付开通；取消 CAS 与重放；挂账按币种合计与只能转一次；渠道启停；收入调整登记、冲销与重复冲销 409
tests/theme-boot.test.ts: 用 node:vm 执行引导脚本覆盖各种存储状态，核对它与 theme.ts 同键；vite 配置拒绝构建 showcase 与未知 mode、引导脚本带内容哈希、不内联资源、假后端只在 serve 时挂上

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

# panel/frontend/
> L2 | 父级: /panel/CLAUDE.md

独立 React + TypeScript + Vite 工程，接现有 Go API。一套 src/ 两个入口：vite --mode admin / --mode portal 分别以 apps/admin、apps/portal 为 root，产物落到 dist/admin、dist/portal。这是候选实现：make frontend-embed 把 dist 同步进 panel/web/*/app，两个网关经 platform/webapp 在 /app/ 下发（入口 CSP 只许同源脚本，产物不得有内联脚本）；生产入口 / 仍是 panel/web 的手写单页，尚未部署切换。前端已实现而后端尚未提供的接口只能登记在 src/core/contracts.ts 并默认关闭；旧单页有而 React 还没有的操作登记在 tests/legacy-parity.ts，清单归零才具备切换条件；tests/api-surface.test.ts 同时守这两张表。数据层用 @tanstack/react-query，UI 用 antd（zhCN），校验用 zod，路由用 hash router。所有 HTTP 只经 src/core/api.ts。

成员清单
package.json / package-lock.json: 依赖精确锁定；scripts：dev:admin(5173)、dev:portal(5174)、typecheck、build（typecheck + 双 mode 构建）、test（vitest）、check
vite.config.ts / vitest.config.ts / tsconfig.json: 双 mode 的 root/outDir 与测试配置；vitest 全局 testTimeout 30s、maxWorkers 2（2 核 CI runner 上 antd+jsdom 单例可超 10s），用例不再各自传超时
apps/admin/index.html、apps/portal/index.html: 两个入口页
src/main.tsx: 启动：ConfigProvider(zhCN)、QueryClientProvider、createHashRouter、lazy + Suspense 路由装载
src/styles.css: 全局样式
src/app/: 壳层：Frame 布局、Gateway 入口守卫、Login、SettingsLayout、SidebarMenu、navigation/sections 导航表
src/components/: 通用组件：common、UnsavedChangesGuard 未保存离开拦截
src/core/: 无 UI 语义的核心：api.ts HTTP 封装、auth、realtime(SSE)、cachePolicy、data、drafts/formDraft 草稿、dialogs/FormDialog、operations、protocol/protocolInputs 节点协议表单、refunds、numbers、runtime（域/API 基址/legacyEntry 原版入口/hasContract 契约开关）、contracts（待接后端契约登记表）、appearance
src/features/: 业务页面：Overview/Orders/Plans/Tickets 共用页，admin/ 与 portal/ 各自的功能页
tests/: vitest 单测 *.test.ts(x)，覆盖 gateway、cache policy、contracts、表单编辑与导出；api-surface.test.ts 扫描 src/ 的 v1 路径与权限码对照 Go 路由与迁移权限字典，并解析旧单页的字符串拼接调用、按域对照 React 调用与迁移清单；legacy-parity.ts 旧页独有操作迁移清单（放 tests/ 是因为 src/ 里的 v1/ 字面量都会被当成 React 调用）
README.md / VALIDATION.md: 第一轮可集成候选说明与验收记录
BUILD-SHA256SUMS.txt: 构建产物校验和

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

---
paths:
  - "panel/frontend/**"
---

# 面板前端：CSP、相对路径与依赖

- 接口形状与后端行为都以 Go 代码为准（`panel/internal/api` 的路由与处理器），前端只经 `src/core/api.ts` 发请求
- 网关的 CSP（`panel/internal/platform/webapp/webapp.go` 的 `indexCSP`）是 `script-src 'self'`、`style-src 'self'`，没有 unsafe-inline，也没有 unsafe-eval；`font-src 'self'`，`img-src 'self' data:`。违反了也能本地跑通，上线却会白屏，所以：
  - `index.html` 里不写内联 `<script>`、`<style>` 或 `style=""`。守卫：`tests/entries.test.ts`，以及嵌入后由 `panel/web/app_test.go` 的 `TestEmbeddedAppsHaveEntryForTheirDomain` 检查
  - 不用 CSS-in-JS，也不在运行时注入 `<style>`。样式只写 `.css` 或 CSS Modules。运行时要改样式就走 CSSOM（`element.style` / `setProperty`，React 的 `style` 属性也算），它不受 style-src 约束，先例是 `src/portal/appearance.ts` 的 `useAppearanceTheme`
  - 首帧前必须执行的脚本只能做成外链经典脚本，参照 `src/core/theme-boot.js` 加 `vite.config.ts` 的 `themeBoot` 插件
  - 不引入运行时要 `eval` / `new Function` 的库；zod 必须保持 `z.config({ jitless: true })`（在 `src/core/api.ts`），守卫是 `src/core/api.test.ts` 的 "turns off zod JIT…"
  - 资源一律不内联成 `data:`（`assetsInlineLimit: 0`）。字体随包自带，样式不引外部来源。守卫：`tests/theme-boot.test.ts` 与 `tests/tokens.test.ts` 的 "stylesheets stay self-contained"
  - 产物里要带新的资源类型（如 wasm）时，`webapp.go` 的 `contentTypes` 与 `indexCSP` 要一起改
- 地址一律相对：后台挂在反向代理的高熵前缀后面，前缀剥掉以后才转发
  - Vite 的 `base` 保持 `'./'`
  - 请求路径只写 `v1/...`，不写 `/v1` 或绝对 URL；`resolveApiUrl` 会拒绝不合规的写法
  - 页面地址只用 hash 路由。网关只下发 `/` 与 `/assets/*`，public 网关根下还有订阅通配，任何两段式路径都会撞上
- 依赖：运行时依赖只有 react、react-dom、@tanstack/react-query、zod，不引入任何 UI 组件库
  - `package.json` 里版本全部精确锁定，不写 `^` 或 `~`
  - 装依赖只走 `npm ci`（`panel/Makefile` 的 `frontend-check` / `frontend-embed`）
- typescript 停在 6.0.x：typescript-eslint 8 的 peer 要求是 `<6.1.0`（见 `package-lock.json`）
- 只在开发期存在的东西不能进产物：showcase 的 mode 在 build 时会被拒绝，`dev/` 假后端只在 serve 时挂上。守卫：`tests/theme-boot.test.ts`
- 验证：`make frontend-check` 跑 lint、typecheck、vitest 和两次构建；`make frontend-embed` 把产物同步进 `panel/web/{admin,portal}`，最后由 `panel/web/app_test.go` 对真实产物裁决

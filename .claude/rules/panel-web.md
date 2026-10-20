---
paths:
  - "panel/web/**"
---

# 前端产物嵌入（panel/web）

- `panel/web/app.go` 用 `//go:embed all:admin all:portal` 嵌入两个域的产物，导出 `AdminApp` / `PortalApp`。`all:` 前缀不能省：Rolldown 产出下划线开头的公共 chunk，默认规则会漏掉它们，页面运行时才 404。
- 仓库里 `panel/web/admin/`、`panel/web/portal/` 只提交带 `<meta name="pandora-placeholder" content="unbuilt" />` 的占位 `index.html`，保证没有 npm 的机器上 go build / go test 照常通过；真实产物由 `make frontend-embed` 覆盖，不入库。
  - 这个标记是「占位页不得发布」的判据，三处在用：`panel/deploy/build-release.sh`、`.github/workflows/pandora-native.yml`、`panel/web/app_test.go`。改标记要三处同改。
- 入口带 `<meta name="pandora-app" content="admin|portal">`，`TestEmbeddedAppsHaveEntryForTheirDomain` 据此确认两个域没有嵌反。
- 嵌的是真产物时，`app_test.go` 的 `checkBuiltEntry` 还守三条：入口不许有内联脚本和内联样式（CSP 是 `script-src 'self'`、`style-src 'self'`）；入口引用的每个 `./` 资源都在 `assets/` 下且确实嵌进了二进制；不嵌 `.vite` 构建元数据。
- 下发在 `panel/internal/pkg/webapp`：`serve.go` 用 `webapp.Mount` 把 `PortalApp` / `AdminApp` 挂到两个引擎的根上，只认入口 `/` 与 `/assets/*`，不做 SPA 回退（缺的资源就是 404）；其余路径归 `/v1`、`/healthz`、`/readyz` 和门户的订阅通配（见 `.claude/rules/panel-http.md`）。
  - 入口的 CSP 是 `webapp.go` 的 `indexCSP`，扩展名到 MIME 的表是 `contentTypes`；产物要带新的资源类型时两处一起改（wasm 还要 `wasm-unsafe-eval`）。
  - 入口页的 `<head>` 可经 `WithIndexMeta`（`index_meta.go`）按站点信息改写：替换 `<title>`，在 `</head>` 前插入 `<meta name="description">`、`<meta name="application-name">`（后台登录页据此取站点名）与 `<link rel="icon">`（只收 `data:image/`，CSP 的 `img-src` 已放行 `data:`），以及 `keywords`、`robots`、`<link rel="canonical">`（`URL` 只收 http(s) 绝对地址）与 Open Graph（`OpenGraph` 为真时写 og:type=website、og:site_name、og:title、og:description、og:url，空值那条不写）。`IndexMeta` 只放 string / bool，`rendered` 按 `==` 比较复用结果。只改文字、值经 `html.EscapeString`，不加脚本与样式，CSP 逐字不变；改写结果按 meta 缓存，ETag 随改写内容变。调用方给的函数要便宜（appearance 的 `PageMeta` 快照）。
  - 门户入口页还可经 `WithHome`（`home.go`）在 `<div id="root">` 前插入首页片段（`#root` 的兄弟节点，React 挂载不覆盖它）：`Home` 只含字符串与字符串结构体切片，由 app 层 `portal_home.go` 组好（价格串、用量串都在 Go 里算），本包不依赖任何模块。片段只用 `html/template`（自动转义）渲染，**不许有 `<script>`、`<style>`、`style=`、事件属性**（CSP 没有 nonce / hash），图片只收 `data:image/` 的 Logo（`template.URL` 只在 `sanitizeIcon` 过后才用）；类名统一 `ph-` 前缀，样式在门户前端的全局样式 `src/portal/home.css`。`#ph-home` 的 id、`#/login` `#/register` 链接与前端 `portal/home.ts`、`entry-links.ts` 对应。入口缓存按 meta + 首页原始输入复用（`Home.equal` 比回调原样返回的值，变了才清洗、重渲染），ETag 随片段变；`WithHome` 返回 false 或站点名为空时入口与不带它时逐字一致。套餐描述多行按空格接上。入口里找不到 `<div id="root">` 就不插，所以 `web/app_test.go` 对真实产物断言门户入口恰好一个 `<div id="root">`。守卫：`home_test.go`（无内联脚本 / 样式 / 事件属性、用户输入被转义、Logo 只收 data:）。
  - 两个引擎都有 `/robots.txt`（appearance 的 handler，`serve.go` 挂）：门户允许收录时放行 `/$` 与 `/assets/`（静态资源要让搜索引擎拿得到，否则判页面不可用），其余 Disallow；后台恒为整站 Disallow。正文固定，不含任何路径前缀。
  - 这两个引擎的根路由是公开的，`internal/app/serve_pg_test.go` 的鉴权清单里登记着 `GET/HEAD /` 与 `/assets/*filepath`。

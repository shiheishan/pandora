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
- 新栈的 `serve` 现在还没有引用 `AdminApp` / `PortalApp`，根下也没有设 CSP 头（`panel/internal/app/serve.go`）。接回前端下发时，按上面这条 CSP 前提设头，并保持根下只下发入口与 `/assets/*`：其余路径归 `/v1`、`/healthz`、`/readyz` 和门户的订阅通配（见 `.claude/rules/panel-http.md`）。

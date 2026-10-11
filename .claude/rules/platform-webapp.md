---
paths:
  - "panel/web/**"
  - "panel/internal/platform/webapp/**"
---

# 前端嵌入与根下发

- 仓库里 `panel/web/admin/`、`panel/web/portal/` 只提交带 `<meta name="pandora-placeholder" …>` 标记的占位 `index.html`，保证没有 npm 的机器上 go build / go test 照常通过；真实产物由 `make frontend-embed` 覆盖，不入库
  - 这个标记被三处当作「占位页不得发布」的判据：`panel/deploy/build-release.sh`、`.github/workflows/pandora-native.yml`、`panel/web/app_test.go`。改标记要三处同改
  - 入口还带 `<meta name="pandora-app" content="admin|portal">`，`app_test.go` 的 `TestEmbeddedAppsHaveEntryForTheirDomain` 据此确认两个域没有嵌反
- `webapp` 只认入口 `/` 与 `/assets/*`，不做 SPA 回退：根下其余路径归 `/v1`、`/healthz` 与 public 的订阅通配 `/{prefix}/{token}`。不要往这里加回退或新的路径类别
- 入口 CSP（`webapp.go` 的 `indexCSP`）与 MIME 表 `contentTypes` 一一对应：产物里出现新资源类型时两处一起改（前端侧的约束见 frontend 的规则）

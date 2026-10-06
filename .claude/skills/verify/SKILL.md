---
name: verify
description: pandora 改完代码后怎么验证、推送前跑什么、推送后怎么等 CI 结论，以及每个 CI job 各管什么。改了 Go、前端、迁移、部署脚本或 workflow，要跑测试、准备提交或推送、CI 变红要找原因时使用。
---

# 验证 pandora 的改动

## 按改动选检查

| 改了什么 | 本机至少跑 |
|---|---|
| panel 的 Go 代码 | 在 `panel/`：`go vet ./...`、`go test ./...`（`make test` 带 `-race`） |
| pdnd 的 Go 代码 | 在 `pdnd/`：`go vet ./...`、`go test ./...` |
| 面板前端 | 在 `panel/frontend/`：`npm ci` 后 `npm run check`（lint、typecheck、vitest、admin 与 portal 两次构建）；动了接口形状或嵌入，再在 `panel/` 跑 `make frontend-embed` 和 `go test ./web/... ./internal/platform/webapp/... ./internal/api/...` |
| 迁移或 SQL | `make check-migrations`（临时库演练）；PG18 用例要 Docker，跑 `panel/deploy/run-pg18-gates.sh` |
| 部署脚本 | `panel/deploy/` 下对应的 `*_mock_test.sh` / `*_static_test.sh` / `*_test.sh` |
| pdnd 协议或发布 | `python3 pdnd/release/check_native_panel_parity.py`；发布链看 `pdnd/release/build.sh` 与 `runtime-acceptance.sh` |

## 坑

- `go build` / `go test` 别和 `npm ci` 同时跑：`node_modules` 里带 Go 包，并发时 Go 会假失败。
- 推送前对最后一个提交跑全量，不要只跑改到的包：注释里写到表名也会触发 schema 契约测试，SQL 字符串、路由表、权限字典都有整包扫描的源码契约。
- PG18 用例在没有 Docker 的地方会跳过，跳过不等于通过；只有 GitHub 的 panel-pg18 job 真跑。

## 推送后等结论

维护者本机主目录的 `ops-local/memoh-ci/`（不入库，worktree 里没有）提供两个等待脚本，推送后用它们，不要手写轮询循环：

- `wait-status.sh <提交>`：等检查机回放 CI，比 GitHub 早几分钟出结论。退出 0 通过，1 失败，2 检查机没接单或超时（改看 GitHub）。手动重跑同一提交时设 `MEMOH_FRESH=1`。
- `wait-github.sh <提交>`：等 GitHub Actions 全部结束，并从日志核对 PG18 没有 SKIP。合进 main，或改动涉及迁移、SQL、panel/internal 数据层时，必须等它退出 0。

检查机跑不了 ARM64、PG18 和 smoke，这三项只以 GitHub 为准。

## CI 各 job 管什么

workflow 默认 `shell: bash`，即 `-eo pipefail`，`| tee` 不会吞掉失败。

- **pandora-native.yml**
  - pdnd 的 Ubuntu race/vet、原生 ubuntu-24.04-arm 的 ARM64 race 门、`-tags interop` 的非 race 外部客户端门，以及 amd64/arm64 双架构构建门禁。
  - release-manifest 对刚构建的 amd64 发布二进制跑 `runtime-acceptance.sh`（Go 模拟面板，signed 与 compat 两种接入各冷启动两次）。
  - panel 的 nodefabric 契约、前端嵌入与根下发契约（占位入口）、表登记簿、权限字典。
  - panel-frontend 对新前端跑 lint、typecheck、vitest 和双入口构建，再用真实产物 `make frontend-embed` 跑 web、webapp、api 的 Go 契约；占位页没被替换即失败。
- **panel-pg18.yml**
  - panel-unit 跑 panel 全量 build、vet 和 go test（PG18 用例在此跳过），是 CI 上唯一跑 panel 全部单元测试的地方。
  - PG18 集成门禁的触发面是整棵 `panel/internal` 加 cmd 与 web 的 Go 源码，不拖 pdnd 的重任务；runner 自带 Docker 跑 `run-pg18-gates.sh`，goose 版本跟 `build-release.sh` 走。
- **panel-smoke.yml**
  - 新前端对真实网关的联调冒烟，触发面含 `panel/frontend/src` 与 `panel/tests`。
  - 经 `deploy/run-smoke-stack.sh` 起一次性的 PG18 加网关，读表先于写路径。
  - 再经 `deploy/run-smoke-e2e.sh` 在同一栈上跑 `panel/tests` 下的六个 e2e 脚本，含内鬼检测评估 `risk_e2e.sh`。全部跑完再判，任一失败 job 即变红。
  - 最后用 `panel/tools/loadtest` 做一次 200 用户、5 节点、60 秒的压测工具试跑，要求零 5xx、零签名失败。
- **panel-deploy.yml**
  - 按 `panel/deploy` 与 `panel/migrations` 路径触发，逐个点名跑不需要数据库和 root 的 deploy 桩测试。
  - 迁移脚本拿真实迁移目录校验编号：严格递增、不重复、允许历史空号。

路径过滤：只改仓库根的文档不触发 workflow；被过滤目录里的任何文件改动都会触发对应的 workflow。

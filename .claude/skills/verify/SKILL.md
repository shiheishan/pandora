---
name: verify
description: pandora 改完代码后本地跑什么、推送后必须等哪个 CI 结论才算完成，以及每个 CI job 各管什么。改了 Go、前端、迁移、部署脚本或 workflow，要验证、准备推送、等 CI 或查 CI 为什么变红时使用。
---

# 验证 pandora 的改动

原则：本地轻、远端重。

- 本地只跑秒级到几分钟、不起 Docker、不做完整构建的检查。
- 全量测试、race、集成门禁交给检查机和 GitHub。
- **推送后检查机全量通过，才算完成。**

## 本地层：改了什么 → 本地跑什么

| 改了什么 | 本地跑 |
|---|---|
| panel 的 Go 代码 | 在 `panel/` 对改到的文件跑 `gofmt -l`；对改到的包跑 `go vet` 和 `go test`，例如 `go test ./internal/domain/billing/...` |
| 动了 SQL 字符串、表名、路由、权限码 | 除改到的包外，加跑整包扫描的源码契约：`go test ./internal/platform/db/ ./internal/api/...`。注释里写到表名也会触发 schema 契约测试 |
| pdnd 的 Go 代码 | 在 `pdnd/` 对改到的文件跑 `gofmt -l`，对改到的包跑 `go vet` 和 `go test` |
| pdnd 的协议、能力矩阵，或面板的协议 schema | `python3 pdnd/release/check_native_panel_parity.py` |
| 面板前端 | 在 `panel/frontend/` 跑 `npm run lint`、`npm run typecheck`、`npm run test`（依赖变了先 `npm ci`），不构建 |
| 部署脚本 | 在 `panel/deploy/` 跑对应的 `*_mock_test.sh` / `*_static_test.sh` / `*_test.sh` |
| 迁移文件 | `bash panel/deploy/check-migrations_mock_test.sh`（编号与 Up 标记），外加改到的 Go 包 |

本地跑不了的：
- `release-stop-the-world_mock_test.sh` 要 root。
- `release-stop-the-world_test.ps1` 要 PowerShell。
- `build-release.sh` 要 GNU tar。

## 远端层：改了什么 → 推送后必须等哪个结论

两个等待脚本在维护者本机主目录的 `ops-local/memoh-ci/`（不入库，worktree 里没有）。推送后用它们等，不要手写轮询循环。

- `wait-status.sh <提交>`：等检查机回放。退出 0 通过，1 失败，2 检查机没接单或超时（改看 GitHub）。手动重跑同一提交时设 `MEMOH_FRESH=1`。
- `wait-github.sh <提交>`：等 GitHub Actions 全部结束，并从日志核对 PG18 没有 SKIP。退出 0 才算过。

| 改了什么 | 推送后必须等到 |
|---|---|
| 任何改动 | `wait-status.sh` 通过。检查机跑 panel 与 pdnd 全量测试、race、vet，以及除 ARM64 外的 pandora-native job、panel-unit、deploy 桩测试 |
| panel/internal 数据层、SQL、迁移 | 再等 `wait-github.sh`：PG18 集成门禁和 check-migrations 临时库演练只在 GitHub 跑 |
| 前端 `src/`、`panel/tests` | 再等 `wait-github.sh`：冒烟栈、e2e 脚本、压测工具试跑、前端双入口构建与嵌入契约 |
| pdnd 内核或协议 | 再等 `wait-github.sh`：ARM64 race、interop、runtime-acceptance |
| 合进 main | 一律等 `wait-github.sh` |

## 坑

- `go build` / `go test` 不要和 `npm ci` 同时跑：`node_modules` 里带 Go 包，并发时 Go 会假失败。
- PG18 用例在没有 Docker 的地方会跳过，跳过不等于通过。只有 GitHub 的 panel-pg18 job 真跑，`wait-github.sh` 会把 SKIP 判为失败。

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

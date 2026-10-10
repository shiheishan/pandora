---
name: verify
description: pandora 改完代码后本地跑什么、推送后必须等哪个 CI 结论才算完成，以及每个 CI job 各管什么。改了 Go、前端、迁移、部署脚本或 workflow，要验证、准备推送、等 CI 时使用；CI 变红了怎么查见 ci-triage。
---

# 验证 pandora 的改动

原则：本地轻、远端重。

- 本地只跑秒级到几分钟、不起 Docker、不做完整构建的检查。
- 全量测试、race、集成门禁交给检查机和 GitHub。
- **推送后检查机全量通过，才算完成。**

## 本地层：改了什么 → 本地跑什么

下表适用于 panel 和 pdnd：本机的 go 命令一律加 `GOTOOLCHAIN=go<go.mod 版本>`（缺省 Go 比 go.mod 新，pdnd 不加会链接失败；原因见根 CLAUDE.md「环境与工具坑」），结论才与 CI 同口径。

| 改了什么 | 本地跑 |
|---|---|
| panel 的 Go 代码 | 在 `panel/` 对改到的文件跑 `gofmt -l`；对改到的包跑 `go vet` 和 `go test`，例如 `go test ./internal/domain/billing/...` |
| 动了 SQL 字符串、表名、路由、权限码 | 除改到的包外，加跑整包扫描的源码契约：`go test ./internal/platform/db/ ./internal/api/...` |
| pdnd 的 Go 代码 | 在 `pdnd/` 对改到的文件跑 `gofmt -l`，对改到的包跑 `go vet` 和 `go test` |
| pdnd 的协议、能力矩阵，或面板的协议 schema | `python3 pdnd/release/check_native_panel_parity.py` |
| 面板前端 | 在 `panel/frontend/` 跑 `npm run lint`、`npm run typecheck`、`npm run test`（依赖变了先 `npm ci`），不构建 |
| 部署脚本 | 在 `panel/deploy/` 跑对应的 `*_mock_test.sh` / `*_static_test.sh` / `*_test.sh` |
| 迁移文件 | 本地命令和自证见 new-migration skill 第 4 节；外加改到的 Go 包 |

本地跑不了的：
- 迁移往返和 PG18 用例要 Docker，本机没有（见根 CLAUDE.md「环境与工具坑」），只在 GitHub 的 panel-pg18 job 里跑；本机上 PG18 用例会跳过，**跳过不等于通过**（`wait-github.sh` 把 SKIP 判为失败）。注释里写到保留表名会触发 schema 契约测试，同见根 CLAUDE.md。
- `release-stop-the-world_mock_test.sh` 要 root，只在 GitHub 的 deploy-root-mock-tests job 里跑。
- `release-stop-the-world_test.ps1` 要 PowerShell。
- `build-release.sh` 要 GNU tar。

## 远端层：推送后等什么

两个等待脚本在维护者本机主目录的 `ops-local/memoh-ci/`（不入库，worktree 里没有）。推送后用它们等，不要手写轮询循环。

- `wait-status.sh <提交>`：等检查机回放。退出 0 通过，1 失败，2 检查机没接单或超时（改看 GitHub；检查机自身故障见 ci-triage）。`MEMOH_FRESH=1` 只让脚本只认本次启动之后新写入的状态，不会触发重跑。真要重跑，请用户在云电脑上删掉该提交的 done 标记，且只对仍是分支头的提交有效（已不是头的，删了也不会重跑）。依据是 `ops-local/memoh-ci/wait-status.sh` 头注与同目录 README。
- `wait-github.sh <提交>`：等 GitHub Actions 全部结束，并从日志核对 PG18 没有 SKIP。退出 0 才算过。退出 1 先核 run 的分支再查原因（同一个 sha 推到两个分支时可能算进别的分支的 run，见 ci-triage）。
- **任何改动**都要 `wait-status.sh` 通过；**合进 main** 一律再等 `wait-github.sh`。
- job 清单以 `.github/workflows/` 为准，下表只写看 yaml 看不出的：每个 job 管什么、检查机跑不跑、什么改动之后必须等它。

| workflow / job | 管什么 | 检查机 | 什么时候必须等它 |
|---|---|---|---|
| pandora-native.yml | pdnd 的 vet / race / 双架构构建，panel 契约，panel-frontend 的 lint / typecheck / vitest / 双入口构建；ARM64 race、interop、runtime-acceptance | 跑，除 ARM64 外 | 检查机部分随 `wait-status.sh`；改了 pdnd 内核或协议，再等 `wait-github.sh` 看 ARM64 race、interop、runtime-acceptance |
| panel-pg18.yml / panel-unit | panel 全量 build / vet / go test（PG18 用例在此跳过，是 CI 上唯一跑 panel 全部单元测试的地方） | 跑 | 随 `wait-status.sh` |
| panel-pg18.yml / panel-pg18 | 先迁移往返，再 `run-pg18-gates.sh` 的全部 PG18 域门禁，以及 check-migrations 临时库演练 | 整个跳过 | 改了 `panel/internal` 数据层、SQL、迁移，等 `wait-github.sh`；往返口径与看结论的命令见 new-migration skill 与 `rules/panel-migrations.md` |
| panel-smoke.yml / 冒烟栈 | 读表、`panel/tests` 的 e2e 脚本（清单以 `run-smoke-e2e.sh` 的 `SCRIPTS` 为准，任一失败即红）、压测工具试跑（零 5xx 零签名失败）、购买路径无头浏览器（Playwright，见 `rules/frontend-browser-e2e.md`） | 不跑 | 改了前端 `src/`、`panel/tests`，等 `wait-github.sh` |
| go-vulncheck.yml / govulncheck | govulncheck 扫 panel、pdnd、subscription-e2e/tools 三个模块的被调用漏洞，另扫发布物视图。**只报告、不挡合并**：步骤永远退出 0，结论在 job summary 与 warning 注解里（扫描出错是 error）。眼下不定时跑（deps-upgrade 第 8 节） | 回放（同样不靠退出码） | 改了 go.mod、go.sum 或升依赖之后看 summary；怎么读、怎么本地复现见 deps-upgrade skill |
| panel-deploy.yml / deploy-mock-tests | 不需要数据库和 root 的 deploy 桩测试 | 跑 | 改了 `panel/deploy`，随 `wait-status.sh` |
| panel-deploy.yml / deploy-root-mock-tests | 以 root 跑 `release-stop-the-world_mock_test.sh` | 明说跳过 | 动了发布控制器、`check-migrations.sh` 的凭据或 `migrate.sh`，等 `wait-github.sh` |

新加不需要数据库和 root 的 deploy 桩测试，要补进 workflow 里 `tests=(...)` 的清单。

路径过滤：只改仓库根的文档不触发 workflow；被过滤目录里的任何文件改动都会触发对应的 workflow。

## 坑

- `go build` / `go test` 不要和 `npm ci` 同时跑（见根 CLAUDE.md「环境与工具坑」）。
- 检查机串行：几路同时推会排队，`wait-status.sh` 等到 1800 秒退出 2 时，别当红，按 GitHub 的结论判（`wait-github.sh`）。

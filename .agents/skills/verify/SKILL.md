---
name: verify
description: pandora 改完代码后本地跑什么、推送后必须等哪个 CI 结论才算完成，以及每个 CI job 各管什么。改了面板 Go 代码、迁移、前端、pdnd、proto、部署脚本或 workflow，要验证、准备推送、等 CI 时使用；CI 变红了怎么查见 ci-triage。
---

# 验证 pandora 的改动

原则：本地轻、远端重。

- 本地只跑秒级到几分钟、不起 Docker 的检查。
- 真库真 Redis 上的测试、迁移往返、e2e 脚本、冒烟栈、Playwright、race 全量交给 GitHub。
- **推送后 `wait-github.sh` 退出 0，才算完成。**

## 本地层：改了什么 → 本地跑什么

本机的 go 命令一律加 `GOTOOLCHAIN=go<go.mod 版本>`（现在是 `go1.26.9`；本机缺省 Go 更新，见根 CLAUDE.md「环境与工具坑」），结论才与 CI 同口径。下表的 panel 命令都在 `panel/` 下跑。

| 改了什么 | 本地跑 |
|---|---|
| panel 的 Go 代码 | 改到的文件 `gofmt -l`；改到的包 `go vet` 与 `go test`，如 `go test ./internal/module/order/...`；再跑 lint：`go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.5.0 run ./...`（含 depguard 分层） |
| 加、删、改名了 `internal/module/<模块>` 或 `internal/pkg/<包>` | `go run ./tools/depguardgen` 重新生成 `.golangci.yml`（不手改），`go run ./tools/depguardgen -check` 确认不漂移，再跑 lint |
| 新文件或大改的 .go 文件 | `go test ./tools/refactorcheck/`（单文件 ≤800 行） |
| 迁移 `panel/db/migrations/` | 见 new-migration skill「自证」；外加读写这些表的模块包 |
| `nodeapi/` 的 proto | 按 `.github/workflows/nodeapi.yml` 的 buf 步骤（本机没有 buf 就交给 CI），再 build 依赖它的 panel 与 pdnd |
| pdnd 的 Go 代码 | 在 `pdnd/` 对改到的文件 `gofmt -l`，对改到的包 `go vet` 和 `go test` |
| pdnd 的协议、能力矩阵，或面板的协议 schema（`internal/module/node/protocol`） | `python3 pdnd/release/check_native_panel_parity.py`；改了订阅渲染或字段翻译再跑 subscription-e2e |
| 面板前端 | 在 `panel/frontend/` 跑 `npm run lint`、`npm run typecheck`、`npm run test`（依赖变了先 `npm ci`）；`npm run check` 会再构建一遍，按需 |
| 部署脚本 `panel/deploy/` | 跑对应的 `*_mock_test.sh` / `*_static_test.sh` / `*_test.sh` |

本地跑不了的（本机没有 docker、psql、redis-cli）：

- PG 用例（经 `internal/pkg/testkit` 取库的 `*_pg_test.go` 等）在 `PANDORA_TEST_DATABASE_URL` 没设时跳过，**跳过不等于通过**。
- 迁移往返、`panel/tests/*.sh` 与 `invariants.sql`、冒烟栈、Playwright 购买路径：只在 GitHub。
- 部署脚本里 `db-roles_pg18_test.sh`（要 PG18）、`redis-hardening_versions_docker_test.sh`（要 Docker）、`backup-provenance_mock_test.sh`（要 Linux root）只在 GitHub 的对应 job 跑。
- `build-release.sh` 要 GNU tar。

## 远端层：推送后等什么

等待脚本在维护者本机主目录的 `ops-local/memoh-ci/`（不入库，worktree 里没有）。推送后用它们等，不要手写轮询循环。

- `wait-github.sh <提交>`：等 GitHub Actions 全部结束。退出 0 才算完成；1 是有失败（先核 run 的分支再查原因：同一个 sha 推到两个分支时可能算进别的分支的 run，见 ci-triage）；2 是没触发或超时。
- `wait-status.sh <提交>`：等检查机（云电脑回放 CI）的结论，只当早期信号，比 GitHub 早几分钟报红。退出 2 是检查机没接单或超时，以 GitHub 为准。检查机没有 Docker：要 PG / Redis 服务容器或 Docker 的 job 它跑不了，这些 job 在检查机上的红以 GitHub 为准（跳过名单是 `ops-local/memoh-ci/ci-replay.py` 的 `SKIP`）。
- job 清单以 `.github/workflows/` 为准，下表只写看 yaml 看不出的：每个 job 管什么、什么改动之后要特别看它。

| workflow / job | 管什么 | 什么时候要特别看它 |
|---|---|---|
| panel-newstack.yml / lint | golangci-lint（含 depguard 分层）；`depguardgen -check` 防 `.golangci.yml` 漂移 | 改了 panel Go 代码、加删模块 |
| panel-newstack.yml / test | PG18 + Redis 上的 `go test -race -p 1 ./...`（PG 用例在这里真跑）；二进制 `migrate up → status → down → up`；`serve` 的 healthz / readyz | 改了 repo、service、迁移 |
| panel-newstack.yml / e2e | 新二进制上 `panel/tests` 的 e2e 脚本（清单是该步骤里的 `for s in …`）与 `invariants.sql` | 改了接口、钱、订单、订阅、风控，或 `panel/tests` |
| panel-newstack.yml / node-e2e | nodesim 冒烟、到期、真 pdnd 的删人断线 | 改了 node 模块、节点通道、`nodeapi`、pdnd 的 node / panel / kernel |
| panel-smoke.yml / smoke | `run-smoke-stack.sh` 起 pandora，页面 schema 解析真响应，Playwright 购买路径（`panel/frontend/tests/browser/`） | 改了前端 `src/`、接口形状、购买与支付流程 |
| pandora-native.yml | pdnd 的 vet / race / 双架构构建、interop、ARM64 race；面板与 pdnd 的协议对照；面板内嵌前端契约；panel-frontend 的 lint / typecheck / vitest / 构建与嵌入 | 改了 pdnd 内核或协议、前端 |
| panel-deploy.yml | 部署脚本桩测试；Redis 加固块在各支持版本上起得来；PG18 上属主迁移、收权限、以 pandora_app 跑 serve；备份链（root） | 改了 `panel/deploy`、`configure-app-role.sql`、迁移里的授权或 SECURITY DEFINER 函数 |
| nodeapi.yml | buf lint、格式、生成代码一致、对上一提交不破坏兼容 | 改了 `nodeapi/` |
| go-vulncheck.yml | govulncheck 扫 panel、pdnd、subscription-e2e/tools。**只报告、不挡合并**：结论在 job summary 与 warning 注解里 | 改了 go.mod / go.sum 后看 summary；怎么读见 deps-upgrade |

新加不需要数据库和 root 的部署桩测试，补进 panel-deploy.yml 里两处 `tests=(...)` 清单；新加 e2e 脚本，补进 panel-newstack.yml 对应步骤的 `for s in …`。

路径过滤：只改仓库根的文档不触发 workflow；改了哪个目录触发哪些 workflow，看各 yaml 的 `paths`。

## 坑

- `go build` / `go test` 不要和 `npm ci` 同时跑（见根 CLAUDE.md「环境与工具坑」）。
- CI 的 test job 按包串行（`-p 1`），所有包共用一个库：夹具里的唯一值（邮箱、名字、代码）要带随机后缀，否则单包能过、合起来撞。
- 一次推多个提交只有最新那个有 run，等的时候传最新的 sha。

# Pandora Panel

Xboard 类代理订阅面板（`panel/`）加自研 NativeCore 节点端（`pdnd/`）。

- 技术栈：Go 1.26、PostgreSQL 18、Valkey 8。
- 面板前端在 `panel/frontend`（React + TypeScript + Vite），构建产物嵌入 `panel/web`，由网关在根 `/` 下发。
- 项目全貌、部署与进度看 README.md。

## 交互

- 每次回答以「哥」开头。
- 思考用英文，交互用中文，代码注释用中文。

## 在哪里找约定

- 按目录生效的模块约定在 `.claude/rules/`，改到对应文件时自动加载。
- 怎么验证、推送后怎么等 CI、每个 CI job 管什么，在 verify skill（`.claude/skills/verify/`）。

## 仓库约定

- panel 与 pdnd 是两个独立的 Go module：面板是 `github.com/aegispanel/aegis`，pdnd 沿用旧名 `github.com/aegispanel/nodeagent`。
- 本地数据基座是 `panel/deploy/docker-compose.yml`，PostgreSQL 与 Valkey 只绑 `127.0.0.1:5433` / `6380`。
- 全仓库用 LF，只有 `*.ps1` 用 CRLF（`.gitattributes`）。
- 只删注释、不改行为的全仓批量提交，登记在 `.git-blame-ignore-revs`；clone 后执行 `git config blame.ignoreRevsFile .git-blame-ignore-revs` 启用。
- `panel/migrations/RESERVED-TABLES.md` 登记迁移里保留、但 Go 从不引用的表，以及它们的锁定原因。删表前先读它。

## 既定范式

先找系统里已有的做法并沿用，不另起炉灶：

- 日志用 platform/logging（log/slog）。
- 响应与错误用 platform/httpx。
- 配置只经 platform/config，panel 里只有它读进程环境变量。
  - 守卫 `panel/internal/platform/config/envaccess_test.go` 扫 panel/internal 与 panel/cmd 的非测试 Go 文件。
  - 豁免逐文件登记在同一文件的 `envAccessExemptions`，豁免失效也会变红。
- 前端 HTTP 只经 `panel/frontend/src/core/api.ts`。

## 单文件 ≤800 行

豁免之外不允许有超过 800 行的 .go 文件（含测试）。豁免只有两类：

1. 整目录豁免两个 fork 来的第三方目录，保持上游的文件划分以便合上游，其中 14 个文件超限（reality 8、realityquic 6）：
   - `pdnd/internal/reality/**`：fork 自 XTLS/REALITY（MPL-2.0，本身基于 Go crypto/tls，见该目录 LICENSE），34 个文件。
   - `pdnd/internal/realityquic/**`：fork 自 apernet/quic-go（见该目录 README），193 个文件。
2. 两个只剩一个超长测试函数、纯挪动拆不开的 PG18 测试，逐个登记，拆到 800 行以内就必须移出：
   - `panel/internal/middleware/idempotency_pg18_test.go`
   - `panel/internal/domain/billing/order_release_pg18_test.go`

守卫与处理：

- 守卫是 panel 的 `tools/refactorcheck/linelimit_test.go` 与 pdnd 的 `linelimit_test.go`，随 `go test ./...` 扫描全部 .go；豁免外超限即红，豁免过期也红。
- 守卫变红时先按主题拆分，只挪代码，用 `panel/tools/refactorcheck` 证明是纯挪动（用法见 `.claude/rules/tools-refactorcheck.md`）。不要加豁免。
- 改豁免表要用户授权。

## 红线：仓库公开

- 仓库只放产品，不写入任何具体部署的值：服务器 IP、域名、后台路径前缀、密钥、从生产导出的数据。
- 测试夹具只用虚构数据，模板只含占位符。
- 真实服务器 IP、域名和私有 gitleaks 规则 `gitleaks-private.toml`，只放在被 git 忽略、只在维护者本机存在的 `ops-local/`。
- 提交前闸门 `.githooks/pre-commit`：
  - 用 gitleaks 扫暂存区，公开规则是 `.gitleaks.toml`，`ops-local/gitleaks-private.toml` 存在时一并加载；没装 gitleaks 也拒绝提交。
  - clone 后执行 `git config core.hooksPath .githooks` 启用。
  - worktree 里没有 `ops-local/`，私有规则不会加载。
- gitleaks 命中必须停下处理。扫描不得与提交、推送串在同一条命令里。

## 长任务的任务清单

- 任务会话开工时，先在 worktree 里建 `.claude/TASKS.md`（被 git 忽略），把要做的事列成勾选清单。
- 每完成一项，立刻打勾并写一句结论。
- 新发现和对话里的新决定，当场追加进清单。
- 上下文压缩后，先读 `TASKS.md` 再继续。

## 大任务拆子 agent

- 工作量大、能按互不重叠的文件或主题切开的任务（审计、迁移、批量删除、多模块修复），拆给子 agent 并行做。小任务不拆，拆分本身有成本。
- 每个子 agent 必须交回：改了哪些文件、跑了什么命令、关键输出。
- 主会话核对证据后才接受：自己读 diff、重跑关键命令，不只信转述。
- 收尾附一张表：子任务 | 子 agent | 结论 | 证据。

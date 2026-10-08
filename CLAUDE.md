# Pandora Panel

Xboard 类代理订阅面板（`panel/`）加自研 NativeCore 节点端（`pdnd/`）。

- 技术栈：Go 1.26、PostgreSQL 18、Valkey 8。
- 面板前端在 `panel/frontend`（React + TypeScript + Vite），构建产物嵌入 `panel/web`，由网关在根 `/` 下发。
- 项目全貌、部署与进度看 README.md。

## 交互

- 每次回答以「哥」开头。
- 思考用英文，交互用中文，代码注释用中文。
- 某一步不需要我参与时，就继续往下做，把进度说明和下一步操作放在同一条消息里。只有在两种情况下才停下来问我：没有我就无法继续时；或者要做破坏性操作之前，包括删除数据、强制推送（force-push），以及修改本仓库以外的任何东西。我发来测试机 IP，即同意按 test-machine skill 登记（`~/.ssh/config`、`~/ai/servers/`）。

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
  - 私有规则由 `ops-local/gitleaks/gen-private.sh` 从 `~/.ssh/config` 与 `~/ai/servers` 生成（真实 IP 的点分与短横线写法），加测试机后重跑；worktree 里的提交回主仓库的 `ops-local/` 加载。
- gitleaks 命中必须停下处理。扫描不得与提交、推送串在同一条命令里。

## 长任务的任务清单

- 任务会话开工时，先在 worktree 里建 `.claude/TASKS.md`（被 git 忽略），把要做的事列成勾选清单。
- 每完成一项，立刻打勾并写一句结论。
- 新发现和对话里的新决定，当场追加进清单。

## 大任务拆子 agent

- 工作量大、能按互不重叠的文件或主题切开的任务（审计、迁移、批量删除、多模块修复），拆给子 agent 并行做。小任务不拆，拆分本身有成本。
- 派子 agent 时显式传 model（`opus` = Opus 5.5，`sonnet` = Sonnet 5.5，`haiku` = Haiku 5.5）：
  - 任务边界清楚、结果能验证（搜索定位、读代码总结、纯挪动拆文件、按明确规则批量修改、跑命令收集输出）→ `sonnet`。
  - 需要深度判断（架构取舍、原因不明的 bug、安全/对抗式审查、方向不明需自主探索）→ `opus`。
  - 跨模块的功能实现，或涉及钱、权限、认证、迁移、节点内核的改动 → `opus`：表面边界清楚，实际全是取舍；这类任务一跑就是一小时，返工比直接用 opus 贵。
  - 短任务拿不准先用 `sonnet`，证据核对不过再用 `opus` 重跑。
  - 量大、在意成本或速度、结果好核对的辅助活 → `haiku`：在长日志、长文档里提取某个具体数据，批量总结、分类，跑固定查询并整理输出。它给 opus / sonnet 当下手，不当主 agent 统筹。
  - 复杂的 agent 式编码、需要深度推理的工作，不用 `haiku`。
- 每个子 agent 必须交回：改了哪些文件、跑了什么命令、关键输出。
- 主会话核对证据后才接受：自己读 diff、重跑关键命令，不只信转述。
- 收尾附一张表：子任务 | 子 agent（模型） | 结论 | 证据。

# Compact instructions

压缩时优先保留：

- 最终目标和完成标准（原样保留，不要改写）
- 基准测试命令，以及各步骤优化前后的数字
- 试过但失败的方案及原因
- 我中途追加的要求和限制
- 当前做到哪一步、下一步做什么

可以丢弃：完整日志、大段测试输出、已经读过的文件全文。

压缩后先重新读 `.claude/TASKS.md`（顶部「压缩后先读」一节）再继续。

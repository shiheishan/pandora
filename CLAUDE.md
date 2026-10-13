# Pandora Panel

Xboard 类代理订阅面板（`panel/`）加自研 NativeCore 节点端（`pdnd/`）。

- 项目全貌、部署与进度看 README.md；当前任务看 `.claude/TASKS.md`。
- 面板前端在 `panel/frontend`（React + TypeScript + Vite），构建产物嵌入 `panel/web`，由面板在根 `/` 下发。

## 当前阶段：后端换主流技术栈（2026-10-12 起）

先把后端换成主流、有社区和第三方维护的技术栈，把架构理顺；性能与占用的系统优化放到换栈完成之后。

| 方面 | 用什么 |
|---|---|
| Web 框架 | gin |
| 数据库访问 | GORM + PostgreSQL 18 |
| 缓存、限流、会话 | Redis（go-redis），替换 Valkey |
| 队列与定时任务 | asynq |
| 配置 | viper |
| 日志 | zap + lumberjack 滚动 |
| 鉴权 | JWT（短期访问令牌）+ Redis 里的会话（可吊销） |
| 节点端 ↔ 面板 | gRPC 双向流 + protobuf，mTLS 做加密与认证 |
| 钱 | decimal（Go 用 shopspring/decimal，库里 NUMERIC），不用浮点 |
| 迁移 | goose 版本化 SQL，每个都有 Down |

- 每个请求带 req_id（`X-Request-ID`，没有就生成），写进日志、错误响应、gRPC 元数据和队列任务。
- 每个接口单独定义请求与响应 DTO，不把数据库模型直接序列化出去。
- 前端不换；接口的 JSON 形状尽量保持不变，前端照常能用。
- 换栈期间旧代码（chi、pgx 手写 SQL、自研令牌、签名通道与 UniProxy、Valkey）只修阻断性 bug，不加功能、不做性能优化。

## 取舍原则

- 先看需求，再找现成方案：成熟的第三方库或标准做法能解决的，不自己写。不为理论上的性能优势引入自研协议、自研格式或额外的复杂度。
- 方案看四件事：可维护性、安全、用户体验、性能。换栈阶段可维护性和安全优先；性能只防明显退化（N+1、全表扫描、无界并发、每请求多余的往返），细调放到换栈之后。
- 安全不让步：认证、权限、钱、密钥相关的做法不能比现在弱。
- 改动量不是取舍理由；也不为「以后可能用到」提前做抽象或通用化。
- 换掉一种做法就删掉旧的，不留长期双轨。

## 架构规矩（新代码必须满足）

1. **分层**：handler 只做参数绑定与校验、调 service、写响应；service 放业务规则和事务边界；repository 是唯一使用 GORM 的地方。handler、中间件不碰数据库。
2. **模块边界**：每张表只有一个属主模块；别的模块要数据，调属主模块的 service，不直接读写别人的表；模块之间不循环依赖。面板是一个进程（serve / worker / migrate 等子命令），不为了「看起来模块化」拆进程、拆包。
3. **迁移与 DDL**：迁移是 goose 版本化 SQL，每个都有 Down，确实不可逆的写明原因；不用 GORM AutoMigrate。CI 跑 up → down → up。会锁大表、失败代价高的 DDL 写清预检、超时和失败后怎么回滚。
4. **数据约束进数据库**：NOT NULL、CHECK、UNIQUE、外键能加就加；钱和余额的变动走只追加的账本。
5. **回归普通**：错误、响应、日志、配置、鉴权各只有一套统一做法；用社区里最常见的写法，不发明新机制。

分层和模块边界用 golangci-lint 的规则（如 depguard）钉住，不再写自研的源码扫描测试。

## 交互

- 每次回答以「哥」开头。
- 交互用中文，代码注释用中文。
- 某一步不需要我参与时，就继续往下做，把进度说明和下一步操作放在同一条消息里。只有在两种情况下才停下来问我：没有我就无法继续时；或者要做破坏性操作之前，包括删除数据、强制推送（force-push），以及修改本仓库以外的任何东西。我发来测试机 IP，即同意按 test-machine skill 登记（`~/.ssh/config`、`~/ai/servers/`）。
- 工程取舍按上面的原则自己定并说明理由；产品规则交我定。

## 协作方式

### 角色

- **主 Agent**（当前会话）：先调查，再拆解任务、分配子 Agent；负责任务管理、冲突解决、build / test、集成、提交、推送和等 CI。
- **opus 子 Agent**（Agent 工具，显式传 `model: opus`）：只在大任务里用，每个负责一块（一个模块或一个主题），在自己的 worktree 里读代码、定细节、把编码派给 composer、核对 composer 的改动并提交。
- **composer-2.5**：写代码的主力，用 composer-dispatch skill 派（`claude -p` 经本机 magpie 代理，参数在它的 launch.sh 里），不用 Agent 工具。它只有读写文件的工具，不能跑命令。
  - 模型用 magpie 的顺序分组 `group/gemini`：composer-2.5 不可用时依次换到 vef、兔子的 gemini-3.8-flash，所以同一次派活实际可能由三者之一完成。核对改动不因模型降低标准；Gemini 常见小错是排序、测试夹具不合库约束、漏装配。

### 按任务大小选路

- 小任务（改几行，转手比自己做还贵）：主 Agent 自己做。
- 中等任务：主 Agent → composer-2.5。
- 大任务（能按模块或主题切成几块并行）：主 Agent → 每块一个 opus 子 Agent → opus 再派 composer-2.5。开法见 dispatch-task skill。

### 拆分

- 调查优先：先读代码，定好接口、数据结构、迁移号、要改的文件，写进任务说明；子 Agent 不负责定这些。
- 按覆盖面切：一块是一组互不重叠的文件或一个主题，同一个文件只归一个子 Agent。共享文件（公共类型、路由表、proto、迁移）由主 Agent 先写好，或先派一块单独做完。
- 任务说明写到不用再判断：只许改的文件、要做什么、可核对的完成标准。子 Agent 看不到主对话，也读不到 `~/.claude/CLAUDE.md`。

### 子 Agent 规则

- 不 build、不 test、不 typecheck（不跑 go build/test/vet、npm run typecheck/test 等）；要验证的命令写进交回说明，由主 Agent 跑。
- 只改任务说明里列出的文件；需要动别的文件时不改，在交回说明里写明。
- 尽早提交、尽早返回：composer 改完文件就返回，由派它的人核对并提交；opus 子 Agent 每核对完一块就在自己的分支上提交，做完立刻交回，遇到卡点也先交回。
- 交回说明：改了哪些文件、提交 sha（opus）、做了什么、没做完或拿不准的。

### 收回、集成与清理

- 主 Agent 自己读 diff、跑 build / test，不只信转述。问题小的自己改；大的写新任务说明再派，返工次数记进 TASKS.md。
- 认证、权限、钱、迁移、节点通道的改动，合入前另派一个没参与实现的 opus 子 Agent 只读审查（或用内置 security-review）。
- 子 Agent 的分支合入后，立刻删掉它的 worktree、本地分支和中间产物（`.claude/runs/` 的运行记录、构建产物、临时目录）；没合入的先问我。
- 要用浏览器的验证（flow-walk、ux-review）与上服务器的操作由主 Agent 做。
- 收尾附一张表：子任务 | 执行者 | 结论 | 证据。

## 在哪里找约定

- 按目录生效的约定在 `.claude/rules/`，改到对应文件时自动加载。它们描述的是旧代码，某块迁到新栈后同步改写或删除。
- 验证、推送后等 CI：verify skill。
- skill 用 `~/ai/skills/bin/skills` 管理：项目自己的 skill 在 `.agents/skills/`（`.claude/skills` 是指向它的链接）；共享的 composer-dispatch、skill-creator 是本机链接，不入库。新做或改 skill 用 skill-creator。

## 仓库约定

- panel 与 pdnd 是两个独立的 Go module：面板是 `github.com/aegispanel/aegis`，pdnd 沿用旧名 `github.com/aegispanel/nodeagent`。
- 本地数据基座是 `panel/dev/docker-compose.yml`（PostgreSQL 与 Valkey 只绑 `127.0.0.1:5433` / `6380`；换栈时改成 Redis）。
- 全仓库用 LF，只有 `*.ps1` 用 CRLF（`.gitattributes`）。
- 只删注释、不改行为的全仓批量提交，登记在 `.git-blame-ignore-revs`；clone 后执行 `git config blame.ignoreRevsFile .git-blame-ignore-revs` 启用。
- 单个 .go 文件不超过 800 行（含测试）。豁免只有 fork 来的 `pdnd/internal/reality/**`、`pdnd/internal/realityquic/**`（保持上游的文件划分以便合上游），和两个逐个登记的超长 PG18 测试（`panel/internal/middleware/idempotency_pg18_test.go`、`panel/internal/domain/billing/order_release_pg18_test.go`，随旧代码删除）。守卫是 panel 的 `tools/refactorcheck/linelimit_test.go` 与 pdnd 的 `linelimit_test.go`；超限按主题拆分，不加豁免，改豁免表要我授权。
- `panel/migrations/RESERVED-TABLES.md` 登记迁移里保留、但 Go 从不引用的表及原因，删表前先读它。

## 红线：仓库公开

- 仓库只放产品，不写入任何具体部署的值：服务器 IP、域名、后台路径前缀、密钥、从生产导出的数据。
- 测试夹具只用虚构数据，模板只含占位符。
- 真实服务器 IP、域名和私有 gitleaks 规则 `gitleaks-private.toml`，只放在被 git 忽略、只在维护者本机存在的 `ops-local/`。
- 提交前闸门 `.githooks/pre-commit`：
  - 用 gitleaks 扫暂存区，公开规则是 `.gitleaks.toml`，`ops-local/gitleaks-private.toml` 存在时一并加载；没装 gitleaks 也拒绝提交。
  - clone 后执行 `git config core.hooksPath .githooks` 启用。
  - 私有规则由 `ops-local/gitleaks/gen-private.sh` 从 `~/.ssh/config` 与 `~/ai/servers` 生成（真实 IP 的点分与短横线写法），加测试机后重跑；worktree 里的提交回主仓库的 `ops-local/` 加载。
- gitleaks 命中必须停下处理。扫描不得与提交、推送串在同一条命令里。
- 现场值（IP、域名、后台前缀）只放 `~/.ssh/config`、`~/ai/servers/`、`ops-local/<目录>/`；口令只经 stdin 或 0600 文件传，不出现在命令行参数、输出和记录里。
- 不读 `ops-local/**/secrets/`（子 Agent 的任务说明同样适用）。
- 测试机不重装、不改套餐。删机只删我在对话里点名的那几台（或我明说授权删的范围），先列清单与账单，再用 test-machine skill 的 `vultr-delete.sh --yes` 删并撤登记；我自己在控制台删的，说删了之后 agent 去掉登记（见 test-machine skill「回收」）。

## 长任务的任务清单

- 任务会话开工时，先在 worktree 里建 `.claude/TASKS.md`（被 git 忽略），把要做的事列成勾选清单。
- 每完成一项，立刻打勾并写一句结论。
- 新发现和对话里的新决定，当场追加进清单。

## 环境与工具坑

- go 与 npm：`go build/test/vet` 不与 `npm ci` 并发（node_modules 里的 flatted 带 Go 包，并发时 go 假失败）。
- 本机 Go：缺省 1.27.1，比 go.mod 的 1.26.9 新，pdnd 在 1.27 下链接失败（sing-box v1.13.14 用 go:linkname 取 x/net http2 的私有符号）。本机所有 go 命令加 `GOTOOLCHAIN=go1.26.9`（模块缓存里已有，`GOPROXY=off` 也能切），报告里注明本机 Go 版本。升 Go 1.27 前先解决这个 linkname，见 deps-upgrade skill。
- 本机（这台 Mac）没有 docker、psql、redis-cli：PG18 用例、迁移往返、冒烟栈、Playwright 购买路径本机跑不了；本机跳过不等于通过，以 CI 为准（verify skill）。
- 保留表名（RESERVED-TABLES.md 登记的）不写进 Go 注释和迁移注释：表登记簿测试对 Go 源码整词匹配，注释里的表名也算引用。
- 1Password SSH agent：推送或 ssh 报签名失败，多半是沙箱连不上 agent 或 agent 锁着。Bash 调用设 `dangerouslyDisableSandbox: true` 重试，仍失败就请我解锁；不改走 HTTPS。
- 测试机上起后台进程一律 `ssh -n host 'cd /root/x; setsid -f ./x > log 2>&1 < /dev/null'`，两台机器分两条命令发；远端 `pkill -f` 的模式要锚定（`pkill -f '^vmstat'`），否则会误杀自己的 ssh 会话；限时的故障注入在远端自带撤销（`setsid -f sh -c 'sleep N; iptables -D …'`）。本机是 macOS，没有 setsid。
- 等 CI：前台跑主目录 `ops-local/memoh-ci/` 的 `wait-status.sh` / `wait-github.sh`，Bash timeout 设 600000，wait-status 加 `MEMOH_PICKUP=540`；被截断就原样再跑，累计 wait-status 30 分钟、wait-github 60 分钟仍无结论按退出 2 处理。
- 子 Agent 再派的子 Agent：完成通知落到主 Agent，派出方按 output 文件自己取结果。

# Compact instructions

压缩时优先保留：

- 最终目标和完成标准（原样保留，不要改写）
- 当前阶段的决定与我中途追加的要求和限制
- 试过但失败的方案及原因
- 基准测试命令，以及各步骤前后的数字
- 当前做到哪一步、下一步做什么

可以丢弃：完整日志、大段测试输出、已经读过的文件全文。

压缩后先重新读 `.claude/TASKS.md`（顶部「压缩后先读」一节）再继续。

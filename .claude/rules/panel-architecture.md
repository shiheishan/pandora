---
paths:
  - "panel/internal/**"
  - "panel/cmd/**"
  - "panel/tools/depguardgen/**"
  - "panel/.golangci.yml"
---

# 面板分层、模块边界与事务

设计稿 `docs/architecture.md` 第 3、4 节是依据；这里只记改代码时容易踩的点。

## 分层由 depguard 钉住
- 规则在 `panel/.golangci.yml`，由 `go run ./tools/depguardgen` 按 `panel/internal/module/` 的目录生成，不手改；加模块、改规则后重新生成，CI 用 `-check` 查漂移。改规则改生成器。
- 几条通用规则：`handler-no-db`（handler、task、grpc 不碰 gorm、`internal/pkg/db`、`internal/pkg/tx`）、`service-no-gorm`（service 不碰 gorm 与 `internal/pkg/db`，事务只经 `internal/pkg/tx`）、`model-plain`、`pure-packages`（`render`、`protocol` 不依赖任何模块与库）、`pkg-no-business`（`internal/pkg` 不依赖模块与 app）、`httpx-no-db`（中间件不碰库）。
- 每个模块一条 `module-<名>-boundary`：不许 import 别的模块的 repo、model、handler、task、grpc、provider，模块之间只调 service；`module-<名>-entry`：本模块的 handler、task、grpc 也不许 import 本模块的 repo 与 model。
- 技术栈规则：`stack-conventions` 禁 chi、pgxpool、`log`、`log/slog`（日志用 zap）；`stack-no-database-sql` 禁生产代码用 `database/sql`（`internal/app/migrate.go`、`internal/pkg/testkit`、`db/migrations` 例外，测试不管）；`stack-no-pgx` 禁 `github.com/jackc/pgx`（含 pgconn，测试同样适用；只有 `internal/pkg/db`、`testkit`、`migrate.go`、`db/migrations/migrations_test.go` 例外）。
- lint 命令：`go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.5.0 run ./...`（本机加 `GOTOOLCHAIN=go1.26.9`）。

## 模块与表属主
- 每张表只有一个属主模块（表见 `docs/architecture.md` 4.1）。要别的模块的数据，调属主 service 的批量读方法，一条 SQL，不逐行查。
- 依赖方向见 4.2。反向通知用构造时注入的接口或回调（如 order 的 `PaidHook` 回调 commission，subscription 的 `RosterNotifier` 由 node 实现），不反向 import、不造事件总线。
- 跨模块的适配（把一个模块的 service 包成另一个模块要的接口）集中在 `panel/internal/app/adapters.go`，装配在 `wire.go`。

## 事务
- 事务只经 `tx.Manager.Do`：ctx 里已有事务就加入，只有最外层提交。事务边界在 service 的公开方法；被别的 service 调的方法加入调用方事务。
- repo 一律经 `db.DB.Conn(ctx)` 取连接（有事务取事务）。
- 提交后的副作用（发实时事件、推节点纪元、入队任务、Kick 投递）用 `tx.AfterCommit(ctx, key, fn)` 登记：回滚丢弃，key 非空时同一事务内去重；ctx 里没有事务时立即执行。不要在事务里直接发。
- 隔离级别 READ COMMITTED 加显式行锁；状态更新带 `WHERE status = 旧状态`，影响 0 行当并发冲突。

## 库里留什么
- 约束类的进库：NOT NULL、CHECK、UNIQUE、外键、追加写触发器 `deny_mutation` / `deny_delete`、账本配平 `assert_ledger_balanced`。业务状态机与编排在 Go。
- 运行时只以 `pandora_app` 连库（`panel/deploy/configure-app-role.sql` 收窄），不要为了绕开触发器换特权连接。没有 RLS、没有多租户。
- 前后端接口以代码为准：改接口形状时同步改 handler 的 DTO 与前端对应的 zod schema，没有单独的契约文档。

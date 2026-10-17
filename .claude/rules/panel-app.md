---
paths:
  - "panel/cmd/**"
  - "panel/internal/app/**"
  - "panel/internal/pkg/config/**"
  - "panel/internal/pkg/jobs/**"
---

# 进程装配、配置与后台任务

## 一个二进制
- `panel/cmd/pandora` 只调 `app`；子命令在 `panel/internal/app/root.go` 注册：`serve`、`worker`、`migrate`（up / down / status）、`admin`（create、reset-password、grant、revoke、list、has-admin）、`backup`。
- `serve`：public（缺省 127.0.0.1:9000）、admin（127.0.0.1:9001）两个 HTTP 监听加节点 gRPC（`grpc.node.addr` 缺省 `:9443`，面板自己终结 mTLS，不经 nginx）。SSE hub、节点会话、心跳都在进程内，按单实例设计。
- `worker`：asynq Server 与 Scheduler，Scheduler 只在 worker 里跑一份；重活放 worker，serve 只管请求。
- 停机：先让 SSE 长连接结束再 Shutdown HTTP（否则一直等到超时），gRPC 停之前先关节点会话。新加的常驻循环要挂在同一个停机流程里。

## admin 子命令
- 「第一个管理员从哪来」只走 `pandora admin create`，只在服务器本机跑，不做成 HTTP 接口。
- 口令只从标准输入读（`--password-stdin`），不加 `--password` 参数：进程参数会进 ps 与 shell 历史。安装器靠 `has-admin` 的退出码判断要不要问（见 `.claude/rules/deploy-scripts.md`）。

## 配置
- viper：`/etc/pandora/config.yaml` 加 `PANDORA_` 前缀环境变量覆盖。秘密（`panel/internal/pkg/config/load.go` 的 `secretKeys`：JWT 密钥、主密钥等）只能来自环境变量或 `*_FILE`，写在配置文件里启动失败。
- 校验集中在 `panel/internal/pkg/config/validate.go`：production 下 HTTP 监听必须绑回环、`public_base_url` 必须 https、pprof 地址只能是回环。新加的校验写在这里，不散到各模块。
- `env` 缺省 production（失败即关）；本地开发与 CI 显式设 `PANDORA_ENV=development`。
- pprof 只开在独立回环地址（`startPprofServer` 用自己的 mux，不用 `DefaultServeMux`），不挂到业务路由：堆里有明文密钥。

## 后台任务（asynq）
- 约定在 `panel/internal/pkg/jobs`：载荷外层带 `req_id`（`NewTask` 从 ctx 取），处理器经 `Middleware` 把 req_id 放进 ctx 与日志；模块经 `Register` 挂任务与周期。
- 定时任务的载荷不带 req_id、字节固定（`newPeriodicTask`），由 `Middleware` 每次运行生成新的：一次运行一个 req_id，`asynq.Unique`（键是载荷的 md5）在重启前后也能去重。不要给周期任务载荷加随机或时间字段。
- 保留期清理的分批循环用 `jobs.PurgeInBatches(ctx, batch, maxBatches, purge)`（不满一批或到批数上限停，ctx 取消即停），各 task 包只留自己的批大小与批数常量，不再手写循环；service 的清理方法收 `limit` 参数，批大小由 task 定。
- 周期任务带 `asynq.Unique`、`MaxRetry(0)`（下一个周期就是重试）；队列 critical / default / low，分配见 `docs/architecture.md` 第 12 节。库里的 `SKIP LOCKED`、租约、状态条件仍是第二道保险，不要因为有 Unique 就删掉。

## 每日任务分钟表（Scheduler 用 UTC）
每天一次的任务各占一分钟，不许两个落在同一分钟；新加或改时间先改这张表，再改 `task.go` 的 `Cronspec` 与对应 `task_test.go`，`docs/architecture.md` 第 12 节那行跟着改。

| UTC | 任务类型 | 所在 |
|---|---|---|
| 03:40 | `retention:idempotency` | `module/idempotency/task` |
| 03:41 | `retention:audit` | `module/audit/task` |
| 03:44 | `retention:node` | `module/node/task` |
| 03:47 | `retention:identity` | `module/identity/task` |
| 03:50 | `retention:subscription_fetch_log` | `module/subscription/task` |
| 03:53 | `retention:notify` | `module/notify/task` |
| 03:56 | `retention:plugin` | `module/plugin/task` |
| 03:57 | `retention:activity_daily` | `module/report/task` |
| 03:59 | `retention:payment_unverified_events` | `module/payment/task` |

---
paths:
  - "panel/**/*_pg_test.go"
  - "panel/internal/pkg/testkit/**"
  - "panel/db/migrations/migrations_test.go"
---

# 连真库的测试（*_pg_test.go）

- 连 PostgreSQL 的用例放在 `*_pg_test.go`，经 `panel/internal/pkg/testkit` 的 `testkit.DB(t)` 拿库：读 `PANDORA_TEST_DATABASE_URL`，为空就 `t.Skip`。每个测试进程（一个包）第一次调用时从模板库复制一个自己的库 `<库名>_run_<pid>_<随机>`；模板 `<库名>_tpl_<迁移摘要>` 由第一个用到的进程在咨询锁下建好并迁移，迁移文件一变就换新模板；进程已退出的库下次自动删掉。
- 本机用 `.agents/skills/verify/scripts/local-db.sh` 起 PG 后真跑（verify skill「本机 PG」，`TZ=UTC`）；没起 PG 时全部跳过，跳过不等于通过。CI 是 `.github/workflows/panel-newstack.yml` 的 Tests 步骤（PG18 服务容器，`go test -race -p 4`）。
- 包与包之间各用各的库、可以并行；同一个包里的用例共用一个库（`t.Parallel` 的用例也是）。DSN 指的那个库只当维护库，`db/migrations` 的 `TestRoundTrip`（downAll 再 Up）与 `internal/pkg/tx` 的测试直接用它，别的测试别直接连 DSN。所以：
  - 用例默认用 `testkit.TxCtx(t, d)`：事务挂在 ctx 上，`tx.Manager.Do` 会加入它，测试结束回滚，不留数据。代价是 `AfterCommit` 钩子不执行。
  - 要测真实提交（并发、提交后钩子、实时事件）时才直接用 `testkit.DB`，开头用 `testkit.Truncate(t, d, 表…)` 清掉要用的表。它在事务里临时 `session_replication_role = replica` 绕过只追加表的守卫，只给测试库用，产品代码不这么做。
  - 提交了数据的用例别假定同包别的用例没留数据；按自己建的 id 查，不按全表计数断言。
- Redis 用 `testkit.Redis(t)` 起 miniredis，不连真 Redis。
- 夹具只用虚构数据（仓库公开）。
- 证明测试真的在测：把被测代码临时改回错误写法，确认新用例变红再改回来，报告里写明做过。

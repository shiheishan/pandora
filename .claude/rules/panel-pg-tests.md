---
paths:
  - "panel/**/*_pg_test.go"
  - "panel/internal/pkg/testkit/**"
  - "panel/db/migrations/migrations_test.go"
---

# 连真库的测试（*_pg_test.go）

- 连 PostgreSQL 的用例放在 `*_pg_test.go`，经 `panel/internal/pkg/testkit` 的 `testkit.DB(t)` 拿库：读 `PANDORA_TEST_DATABASE_URL`，为空就 `t.Skip`；每个测试进程第一次调用时 `migrations.Up` 到最新。
- 本机没有 PG，这些用例全部跳过；跳过不等于通过，以 CI `.github/workflows/panel-newstack.yml` 的 Tests 步骤为准（PG18 + Redis 服务容器，`go test -race -p 1`）。推送前把 SQL 和夹具多看一遍，预留一次 CI 往返。
- CI 按包串行（`-p 1`），所有包共用一个库，`TestRoundTrip` 还会把它 DownAll 再 Up。所以：
  - 用例默认用 `testkit.TxCtx(t, d)`：事务挂在 ctx 上，`tx.Manager.Do` 会加入它，测试结束回滚，不留数据。代价是 `AfterCommit` 钩子不执行。
  - 要测真实提交（并发、提交后钩子、实时事件）时才直接用 `testkit.DB`，开头用 `testkit.Truncate(t, d, 表…)` 清掉要用的表。它在事务里临时 `session_replication_role = replica` 绕过只追加表的守卫，只给测试库用，产品代码不这么做。
  - 提交了数据的用例别假定别的包的表是空的；按自己建的 id 查，不按全表计数断言。
- Redis 用 `testkit.Redis(t)` 起 miniredis，不连真 Redis。
- 夹具只用虚构数据（仓库公开）。
- 证明测试真的在测：把被测代码临时改回错误写法，确认新用例变红再改回来，报告里写明做过。

---
paths:
  - "panel/internal/module/*/repo/**"
  - "panel/db/migrations/**"
---

# 写 SQL 时的性能坑

写 SQL 先防明显退化（根 CLAUDE.md「取舍原则」）：N+1、全表扫描、无界并发、每请求多余的往返。下面几条是旧栈在 5k 用户、200 节点规模上实测踩过、换了 GORM 照样会踩的写法。改 SQL 前后用 `EXPLAIN (ANALYZE, BUFFERS)` 对照。

- **for 循环里查库（N+1）**：每条订阅查一次配额、每个 id 一条插入。改成一条带 `= ANY(?)` 的查询，批量写用 `CreateInBatches`（如 `panel/internal/module/giftcard/repo/codes.go`）。
- **整表聚合被按单行 JOIN**：先 GROUP BY 全表、外面再 JOIN 的视图或子查询，过滤推不进去，只查一条也扫全表。写成按驱动行的 LATERAL 聚合。
- **LATERAL / 相关子查询在 LIMIT 之前**：对全部行算完读模型再取一页。先在主表上按排序键取一页 id（配好对应索引），再只对这一页拼读模型。
- **请求时解析原始上报**：看板每次扫原始上报做聚合。入库时就按口径写汇总表，读路径只读汇总（节点流量读 `node_traffic_hourly`，见 `panel/internal/module/node/repo/report_reads.go`）。
- **读路径上的写**：心跳、拉配置这类高频路径不要无条件 UPDATE；没变就不写（`IS DISTINCT FROM`，照 `panel/internal/module/node/repo/session.go`），清理挪到 asynq 定时任务。
- **只增不删的表**：在线记录、指标、审计、流水要有保留期任务，分批删、每批有上限（照 `panel/internal/module/audit/task/task.go` 的 `TypeRetention`）；追加写表走 SECURITY DEFINER 清理函数，不为清理削弱触发器保护（见 `.claude/rules/panel-migrations.md`）。
- **按列查却没索引**：新增按某列查找的路径，先对照迁移里的 CREATE INDEX 确认索引建在这一列上（旧栈曾按 token_prefix 查凭据而索引建在 token_hash 上）。
- **长事务里算 Argon2id**：每次十几 MiB、几十毫秒，持着连接和行锁去算会拖垮连接池。`panel/internal/module/identity/service/password.go` 的 `passwordHasher` 在事务外经并发闸算好，再开事务。
- **JIT**：PG18 缺省 jit=on，代价估计一过阈值就编译，编译常比执行慢。部署和开发库都已关（`panel/deploy/postgresql-pandora.conf`、`configure-app-role.sql` 按库给 `pandora_app` 设 `jit = off`），不要在会话里打开。
- **语句超时**：`pandora_app` 按库设了 `statement_timeout = '15s'`。确实要更长的后台任务在自己的事务里 `SET LOCAL statement_timeout`，不要放宽全局值；能分批的先分批。

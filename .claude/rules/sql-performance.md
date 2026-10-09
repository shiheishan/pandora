---
paths:
  - "panel/internal/domain/**"
  - "panel/internal/platform/db/**"
  - "panel/internal/middleware/**"
  - "panel/migrations/**"
---

# 写 SQL 时的性能坑（2026-10-06 生产规模实测踩过的）

5k 用户、200 节点在 2c4g 上实测，慢的全是下面这些写法，不是框架。改 SQL 前后用 bench-eval skill 在 5k 库副本上对照。

- **整表聚合的视图被按单行 JOIN**：视图里先 GROUP BY 全表、外面再 JOIN，过滤条件推不进去，只查一条也扫全表（在线设备视图曾单条 82ms、后台用户列表因此 470 秒）。视图写成按驱动行 LATERAL 聚合，或调用方自己按单行 LATERAL
- **LATERAL / 相关子查询在 LIMIT 之前**：对全部行算完读模型再取一页。先在主表上按排序键取一页 id（配 `(tenant_id, created_at DESC, id DESC)` 这类索引），再只对这一页拼读模型
- **每行调用读设置的 SQL 函数**：函数体里有子查询就不能内联，每行读一次 system_settings。不相关的值写成标量子查询（成为 InitPlan，整条语句只算一次）
- **内联后的表达式被抄写**：可内联的 SQL 函数被拉平后，每引用一次上一步的列，就把那一步的整棵表达式（含正则）再算一遍。逐步计算的管线每步一层子查询加 `OFFSET 0` 隔开（00099 的 `node_traffic_payload_entries`）
- **JIT**：PG18 缺省 jit=on，估价过 10 万就编译，编译远慢于执行；是否触发取决于统计信息新旧。部署已关（`deploy/postgresql-pandora.conf` 的 `jit = off`，开发数据基座同值 + 按库给 aegis_app 设 jit=off），不要在会话里再打开
- **请求时解析原文**：看板每次对 24 小时上报 jsonb_each + 正则。入库时就按口径写汇总表，读路径只读汇总
- **for 循环里查库（N+1）**：每条订阅查一次配额、每个 uid 4 条语句、每个 IP 一条插入。改成 `= ANY($n::uuid[])`、`unnest` 批量写、一条 CTE
- **读路径上的写**：每次拉配置无条件 UPDATE、每个请求同步删旧 nonce。没变就不写（`IS DISTINCT FROM`），清理挪到后台定时任务
- **只增不删的表**：在线记录、探针点、汇总表要有保留期任务（分批删、每批有上限）；追加写表（`app.make_append_only`）不能为清理削弱保护，走带边界的 SECURITY DEFINER 函数（00100 `app.purge_node_metrics`）
- **按列查却没索引**：新增按某列查找的路径先对照迁移里的 CREATE INDEX（曾经按 token_prefix 查订阅凭据而索引建在 token_hash 上）
- **一个请求开多个事务**：每个事务至少 BEGIN、设租户、COMMIT 三次往返（`InTx` 已把 BEGIN 与设租户合成一次）。热路径能一条语句取齐就用 `QueryRowScoped`
- **长事务里算 Argon2**：19 MiB、几十毫秒一次，持着连接和行锁去算会拖垮连接池。先经全局名额算好再开事务
- **语句超时**：aegis_app 按库设了 statement_timeout=15s。合理需要更长的后台任务在自己的事务里 `SET LOCAL statement_timeout`，不要放宽全局值

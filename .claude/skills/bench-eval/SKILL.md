---
name: bench-eval
description: 在开发对照机（vultr-sgp-pt-bench）的 5k 实测库副本上，给 SQL 或迁移改动做改前改后 EXPLAIN 对照、正确性比对与回填耗时测量。验收性能类任务分支、判断某条 SQL 改写值不值、复现实测慢查询、估迁移回填时长时使用。
---

# 5k 库改前改后对照

目标：每个性能改动都有「同一台机器、同一份数据、改前 vs 改后」的数字，并且结果集不变。单条查询快不等于并发下稳，最终成绩以 runbook（`panel/tools/loadtest/README.md`）的整机压测为准；这里是合并前的快速判分。

## 对照机上有什么

- 容器 `bench-pg`：postgres:18-alpine，512M、max_connections 60、shared_buffers 128MB（与生产 compose 一致），端口只绑本机回环。
- 库 `aegis`：2026-10-06 实测 5k 库原样（用户 5001、订阅 5000、节点 200），是训练集基线，**不要改它**。dump 在容器内 `/tmp/d.pgdump`，宿主 `/root/aegis-5k.pgdump`。
- 角色 aegis（超级）、aegis_app（与生产一样 NOSUPERUSER NOBYPASSRLS）、aegis_idempotency_owner；恢复 dump 前三个都要先建好。
- `aegis_train*`、`aegis_holdout*` 归评测集（`ops-local/bench/`），留出集的规模与分布不告诉写代码的一方。
- 改前基线：`/root/bench/base/`；各次运行的原始输出：`/root/bench/runs/`。

## 做法

1. 建副本并只应用该分支新增迁移的 Up 段：`scripts/prep-copy.sh aegis_cmp_<名字> <worktree>/panel/migrations/<新迁移>.sql ...`。会打印 restore 秒数和每个迁移的耗时：迁移里有回填的话，这个耗时就是回填成本，要按生产量级外推。
2. EXPLAIN 脚本照 `ops-local/vultr-test/5k-diag/q*-explain.sql` 的写法：`BEGIN; SET LOCAL ROLE aegis_app; SELECT set_config('app.tenant_id', …, true);`，再 PREPARE、EXPLAIN (ANALYZE, BUFFERS)、ROLLBACK；每段用「-- A1 说明」这样的行开头。
3. 跑：`scripts/explain.sh <库名> <脚本.sql> [on|off|both]`，输出每段的 Execution Time。生产已关 JIT，以 off 为准，on 用来看 JIT 是否会被触发。
4. 判分：
   - 改后与改前在同一份数据上比，提升要超过噪声（同一脚本至少连跑两遍）；
   - 正确性：改写过的查询，改前改后的结果集必须一致（`SELECT md5(string_agg(t::text, '|' ORDER BY t::text)) FROM (<查询>) t` 两边比对），不一致直接判失败；
   - 评测集交付后，以训练集和留出集都不变差为合并条件。只有训练集变好，按过拟合处理。

## 坑

- psql `-q` 不回显 SQL 注释，输出只能按顺序和段对上：用 explain.sh（它会把「-- A1」行换成 `\echo` 标记），不要自己数。
- `EXECUTE q(ARRAY(SELECT …))` 会报 cannot use subquery in EXECUTE parameter，同一事务后面全部变 aborted：先 `\gset` 取值再传，或把子查询写进 PREPARE 体。
- 从 pg_stat_statements 或日志抄来的语句常被压成一行，行内的 `--` 注释会把后面整段都吞掉：先去掉注释再用。
- **统计信息新旧会改变计划**：刚 ANALYZE 过的库估价低、JIT 不触发，实测时估价高一倍、触发 JIT。对比前两边都 ANALYZE；要复现线上问题，就额外测一份不 ANALYZE 的。
- 5k 库里的 node_alive_ips 全部早已过期，与负载中的真实状态不同；测在线设备相关的查询，要把 last_seen_at 平移到窗口内（评测集的 aegis_train_live 已经这样做了）。
- `CREATE DATABASE … TEMPLATE aegis` 要求模板库上没有其他连接，评测集在用时会失败；prep-copy 用 pg_restore，约 12 秒，不受影响。
- 对照机是共享 CPU，评测集、构建、压测同时跑时数字会抖一倍（同一条查询实测过 16ms 和 73ms）：正式判分前先确认机器空闲（`uptime`、`docker stats`），并记下 steal。
- ssh、docker exec、psql 三层嵌套时引号极易出错：把 SQL 写进文件再 scp，`docker exec -i … psql < 文件`；脚本里用 `bash -s` 加 heredoc。
- 副本名必须是 `aegis_cmp_` 开头，同名会被删掉重建；不要拿 `aegis` 本身、`aegis_train*`、`aegis_holdout*` 做实验。

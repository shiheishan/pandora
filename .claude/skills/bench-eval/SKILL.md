---
name: bench-eval
description: pandora 的 SQL 性能判分：在开发对照机（vultr-sgp-pt-bench）上用 ops-local/bench 评测集给 SQL 改动做改前改后判分（用例逐字取自 Go 源码，训练集 + 留出集，通用计划，结果集哈希一致，噪声底），另有单条 SQL 改前改后 EXPLAIN 的快速诊断。验收性能类分支、判断某条 SQL 改写值不值、换栈（gin + GORM）每步的性能闸门、复现实测慢查询时使用。
---

# SQL 改前改后判分

目标：每个性能改动都有「同一台机器、同一份数据、改前 vs 改后」的数字，并且结果集不变。单条查询快不等于并发下稳，最终成绩以 runbook（`panel/tools/loadtest/README.md`）的整机压测为准；这里是合并前的判分。

- **主流程**：评测集判分（下面第 1–6 节）。验收性能类分支、换栈每一步的性能闸门都走它。
- **快速诊断**：单条 SQL 在 5k 库副本上看计划（最后一节）。只用来找原因，不能当合并依据。
- 迁移回填耗时见 new-migration skill。

## 评测集在哪

评测集本体在 `ops-local/bench/`（不进仓库，0700）。完整说明读 `ops-local/bench/README.md`（用例清单、阈值依据、判分规则）。

| 路径 | 是什么 |
|---|---|
| `run.sh <数据集> <版本> [选项]` | 本机入口：同步到对照机 → 机器上跑 → 拉回结果并打印 summary.md |
| `judge.sh compare / gate / consistency / drift` | 本机比较结果目录，不连机器 |
| `remote/cases/<id>/` | 用例：`case.json`（参数、阈值、判分方式、出处）、`before.sql`、`after.sql`、`after.json` |
| `remote/versions/<版本>/` | 版本：`migration.sql`、`session.sql`、`cases/<id>.sql|json` |
| `remote/datasets/datasets.json` | 数据集配置（运行库、模板库、是否平移时间、绑定参数） |
| `tools/extract_cases.py` | 从 f364a62 的 Go 源码抽用例，`--check` 复核 |
| `tools/extract_w5l.py` | 按 BASE 与 HEAD 两个提交抽改前改后 SQL 的范本（w5latency 判分用） |
| `tools/build_w5l_templates.sh` | 给评测模板补迁移，建新结构的模板 |
| `tools/w5l_driver.sh` | 4 个数据集 × 5 个版本的总驱动，范本 |
| `results/` | 每次运行一个目录；汇总在 `results/_<任务>_judge/` |

数据集：

| 数据集 | 是什么 |
|---|---|
| `train` / `train_live` | 5k 实测库克隆；`_live` 把在线 IP、nonce、心跳平移到 now() 附近（每个用例开跑前重新平移） |
| `holdout` / `holdout_stale` | 留出集；`_stale` 是统计信息陈旧的变体 |
| `w5l_*` | 同上四个，但模板是「原模板 + 迁移 00098–00123」（9051135 结构）；`w5l_orders` 见第 5 节 |

**留出集只给总协调用**：规模与分布不在这里写，也不要把 `holdout/`、留出集绑定参数和明细结果贴进给实现方的 brief。brief 里只说「训练集与留出集都不变差」，防止对着留出集调参。

跑法与判法的要点（判分器 `remote/bench.py` 实现）：

- 执行上下文同生产：以 `aegis_app` 登录、`search_path = pg_catalog, public, pg_temp`、每次请求一个事务并 `set_config('app.tenant_id', …, true)`、同一连接 PREPARE 后反复 EXECUTE（pgx 语句缓存），会话开头补生产的 `effective_cache_size / work_mem / maintenance_work_mem`（`datasets/pg-session.sql`）。
- 性能：热身 5 次（前 5 次必为 custom plan）+ 测 7 次，每次独立事务回滚，取中位；探针超 2 秒的慢用例改为热身 1 + 测 3。
- 正确性：同一个 REPEATABLE READ 事务里先跑改前、再套迁移跑改后，比「全列排序后哈希」；写语句比写后表状态；`LIMIT` 有并列的用 top-k 判分。
- 噪声底：变好或变差要同时超过 `max(3×轮间CV, 5%)` 和 0.2ms，否则算「持平（噪声内）」。
- 阈值：节点类 5ms、订阅 5ms、门户与后台 50ms、看板 200ms；节点、订阅、门户触发 JIT 直接判失败。

## 1. 开工前

1. 读 `~/ai/servers/vultr-sgp-pt-bench/AGENTS.md`（登录与红线），不凭别名直接 ssh。
2. 对照机是 2c4g 共享型，同一条查询在忙时能差一倍多。确认机器空闲：`ssh vultr-sgp-pt-bench 'uptime; docker stats --no-stream; vmstat 1 5 | tail -2'`；压测、构建、`e2e/ab.sh` 都不要同时跑；**不要并行跑两个 run.sh**。
3. 判分器每个用例开跑前最多等 120 秒让别的库安静（`--quiet-wait`），并把 CPU steal、他库活跃会话数写进结果。数字异常先看这两列，再看计划。

## 2. 给分支建「评测结构」和用例

### 2.1 找出改了哪些 SQL

在分支 worktree 的 `panel/` 下：`go run ./tools/refactorcheck sqlset -base <BASE> -head <HEAD>`，DIFF 行就是改过的 SQL 声明。BASE 是分支基点（改前），HEAD 是分支头（改后）。每条要么已有用例（`ls ops-local/bench/remote/cases`，看 `case.json` 的 `source`），要么要新建。

### 2.2 模板结构要和 BASE 对上

评测模板（`aegis_train_tpl` 等）停在 f364a62 的迁移 00097。BASE 的迁移号超过 00097，改前 SQL 会引用模板里没有的对象（例：w5latency 的 BASE 9051135 读 `node_traffic_hourly`，那是 00099），这时要给模板补迁移，否则改前就报错：

```bash
# 在仓库根目录。Up 段目录放 scratchpad；范围固定从 98 到 BASE 的最大迁移号，不是增量
.claude/skills/bench-eval/scripts/extract-up.sh <临时目录> 98 <BASE最大号> panel/migrations <BASE>
ops-local/bench/tools/build_w5l_templates.sh <临时目录> <后缀>
```

产物是 `aegis_cmp_<后缀>_{train,holdout,holdout_stale}_tpl`（克隆现有模板再套迁移，不改原模板；holdout 两个设了 `ALLOW_CONNECTIONS false`，陈旧变体不 ANALYZE 且关 autovacuum）。然后在 `remote/datasets/datasets.json` 里照 `w5l_*` 四条加四个数据集：

- 运行库名必须 `aegis_cmp_` 开头（判分器每次会 `DROP DATABASE … WITH (FORCE)` 重建）；模板指向新模板。
- 留出类数据集的名字里保留 `holdout`：`judge.sh gate --noise` 靠目录名里有没有 `holdout` 区分噪声来源。
- BASE 迁移号不超过 00097 就直接用 `train / train_live / holdout / holdout_stale`。

### 2.3 建用例

范本：`remote/cases/w5l_orders_list/case.json`；生成脚本范本：`tools/extract_w5l.py`（复制成 `extract_<任务>.py`，改 BASE/HEAD 与用例表）。

- **改前 SQL**：`git show <BASE>:<文件>` 取 Go 源码里的原文，写进 `before.sql`；**改后 SQL**：`git show <HEAD>:<文件>`，写进 `after.sql`，参数形状变了写 `after.json`（`{"params": [...]}`）。不动仓库工作区。
- 动态拼接的语句（筛选、分页、批次）不能手抄：在 `git archive` 导出的临时副本里跑一个 Go 小测试，调用真实的拼接函数导出 SQL 与参数形状（`extract_w5l.py` 的 `--dump` 目录就是这些导出）。
- `case.json` 关键字段：`threshold_ms`、`params`（`{tenant}`、`{user}`、`{node}`… 占位符，键取自 `datasets/train.bindings.json`）、`compare.mode`（`set` 或 `topk`）、`write`、`state_sql`（写语句比写后表状态）、`jit_forbidden`、`note`。
- **一条改动把一条 SQL 拆成多条或挪到 Go 里**：用一条等价 SQL（CTE 或标量子查询拼成一行）表达最终结果集，并在 `note` 里写清等价关系（例：`w5l_dbstats` 把三条独立语句各包一层标量子查询拼成一行）。表达不了的只能走端到端 A/B（`ops-local/bench/e2e/`）。
- `EXECUTE` 的参数里不能放子查询：原文里由上一条语句结果传入的数组，改写成 `ANY(ARRAY(SELECT …))` 放进语句体（例：`w5l_user_detail_quotas`）。
- 判分器的 `EXECUTE` 至少要一个参数；没参数的语句末尾补 `WHERE $1::uuid IS NOT NULL` 并传租户，改前改后同补（例：`w5l_dbstats`）。
- SQL 文本没改、改动在缓存或往返数的用例，也建用例（`case.json` 里标 `sql_identical`），作回归哨兵：它们应该持平，变差就是抓到了副作用。

### 2.4 建版本目录

| 版本 | 内容 |
|---|---|
| `before` | 不用建，沿用各用例 `before.sql` |
| `after` | 特殊标签：先找 `versions/after/cases/<id>.sql`，没有再用 `cases/<id>/after.sql`；分支自带的迁移 Up 段放 `versions/after/migration.sql`（只有注释算没有） |
| `before_gp` | `session.sql` 一行 `SET plan_cache_mode = force_generic_plan;`，不放 `cases/` |
| `after_gp` | 同样的 `session.sql`，加 `cases/<id>.sql|json`（改了的用例）；**分支有迁移的话，`migration.sql` 也要在这里放一份**，判分器按版本目录各读各的 |

为什么要 `_gp`：pgx 缓存语句后，PG 从第 6 次执行起可能换通用计划；强制通用计划测的是最坏形态。参数化的 SQL 改动都要跑。部署配置类改动（如 `SET jit = off;`）同样写进某个版本的 `session.sql`。

没放进 `cases/` 的用例沿用 `before.sql`（结果里标「该版本未改此用例」）。`run.sh` 每次 `rsync --delete` 同步 `remote/`，改完用例直接重跑即可；`meta.json` 里的 `case_sha` / `before_sha` 能核对两次运行用的是不是同一份 SQL。

## 3. 跑

每个数据集 5 步，用例用 `--cases` 限定到本任务的用例。训练侧用 `train_live` 与 `train`，留出侧用 `holdout` 与 `holdout_stale`，各一遍：

```bash
cd ops-local/bench
./run.sh <数据集> before    --cases <id,id,…> --rounds 3                      # 改前，同时是噪声底
./run.sh <数据集> after     --cases <id,id,…> --rounds 3                      # 改后
./run.sh <数据集> before    --cases <id,id,…> --rounds 3                      # 第二份基线：噪声底 + 判分器稳定性
./run.sh <数据集> before_gp --cases <id,id,…> --rounds 3 --skip-correctness   # 通用计划，改前
./run.sh <数据集> after_gp  --cases <id,id,…> --rounds 3                      # 通用计划，改后
```

- w5latency 全套（29 个用例 × 4 个数据集 × 5 步）约 1 小时 45 分，超过 Bash 工具的单次上限。复制 `tools/w5l_driver.sh` 改 `CASES` 和数据集名，用 `run_in_background` 起，看 `results/_<任务>_driver.log`。
- 驱动按 `results/*-<数据集>-<版本>` 是否已有目录来**跳过**：SQL 改过要重判时，先把旧结果目录挪走或换新数据集前缀，否则整套被跳过。
- 驱动开头和每个数据集跑完都记一次 `uptime / docker stats / vmstat`，这是「机器当时是否空闲」的证据，留在日志里。
- `holdout*` 的运行库跑完即删，机器上的留出集结果拉回后也删（驱动再 `rm -rf` 一遍）。

## 4. 判

对每个数据集、普通与 `_gp` 各一对：

```bash
cd ops-local/bench
./judge.sh compare     <A=before目录> <B=after目录> --noise <before目录1>,<before目录2>
./judge.sh consistency <before目录1> <before目录2>          # 判分器自己稳不稳：判定应一致
./judge.sh gate <A_train> <B_train> <A_hold> <B_hold> --noise <train基线1,2>,<hold基线1,2>
./judge.sh drift <holdout的某次> <holdout_stale的同版本>     # 看统计信息陈旧带来的计划漂移
```

`gate` 的四个目录是（训练侧 before、after，留出侧 before、after）。w5latency 配成 `train_live` × `holdout`、`train` × `holdout_stale` 两组，`_gp` 版本各再配一组，共 4 次 gate。`_gp` 的 compare/gate 只有一份基线，不带 `--noise`。

通过标准（accept-task 照此验收）：

| 看什么 | 通过 | 不通过 |
|---|---|---|
| 正确性 | 每个用例 `pass` | `fail` / `error`：先按第 4.1 节分「真回退」与「预期的语义变化」 |
| 变差 | 没有「变差」 | 训练集或留出集任何一边变差（超过噪声底）即退回 |
| 变好 | 训练集与留出集**都**变好才记功 | 只训练集变好按过拟合处理，不记功；只写进报告 |
| 判定 PASS/FAIL | 改后不比改前多出 FAIL | 改前就 FAIL 的用例（如超阈值）改后仍 FAIL，要对照改前是否同样 FAIL，不算本分支引入 |
| 判分器稳定 | `consistency` 一致 | 「不一致（阈值边界内）」只表示用例中位贴着阈值、判定会翻转，报告里点名，不归咎分支 |
| 环境 | `meta.json` 的 `steal_pct_run` 低，`summary.md` 里各用例 steal% 低、「他库活跃」为 0 | 某次 steal 偏高或有他库活跃：该次作废重跑 |

`gate` 末行「存在回退或正确性失败，不予保留」是机械结论，只要有一个用例否决就会出现；逐行读，别只看末行。汇总表照 `results/_w5l_judge/table.md` 的格式（每个用例一行：训练集 / 留出集的前→后中位和变好持平变差，普通与 GP 各一对，再加正确性列）。

### 4.1 正确性 fail：真回退还是预期的语义变化

先看该用例结果目录的 `correctness.json`（`checks`、`diff_sample`）和 `summary.md`，再判：

| 现象 | 判断 | 做法 |
|---|---|---|
| 训练集留出集都 fail，差异行是值错、多行、少行，而分支本意不改语义 | 真回退 | 退回分支，把 `diff_sample` 贴给实现方 |
| 写后表状态不同（`state_sql`） | 多半真回退 | 同上 |
| 只在留出类数据集 fail、训练集 pass，且这条 SQL 的语义变化有出处 | 可能是预期的语义变化 | 走下面三步核实 |
| `WARN before 自身两次结果不同` | 查询本身不确定（含 `now()` 边界等） | 先让用例确定（固定相关输入）再比，不能拿它判分支 |
| `WARN 行顺序与 before 不同（集合相同）` | 只是提示 | 不影响通过 |
| top-k 用例 `新版本不是参考全集的合法 top-k` | 排序键、行归属或行数不对 | 当真回退查 |

判「预期的语义变化」要三件都有：

1. **出处**：能指出引入语义变化的提交，且它是有意的、不在本分支的意图之外。`git log -S'<语句片段>' <BASE>..<HEAD> -- <文件>`，并把提交号写进该用例 `case.json` 的 `note`。例：`w5l_overview_subs`（看板订阅计数）的差异来自 c7a9c8f（到期计数补 `current_period_end > now()` 下限），不是 w5latency 的改动；训练集 5k 库里没有恰好落在边界上的行，所以训练侧 pass；留出侧的 `holdout` 与 `holdout_stale`（普通与 `_gp` 共 4 次运行）fail，gate 对它显示「否决」。
2. **差异形状吻合**：`diff_sample` 里差的行和列恰好是该语义覆盖的那类，方向符合改动意图（例：到期计数只会变小）。
3. **硬证据（有疑问时）**：新建一个 `<id>_sem` 用例，`before.sql` 手工套上同样的语义变化，再判一遍，必须结果一致。

满足后：该用例在结论表里单列「预期语义变化（提交 sha）」，性能照 compare 的结果记；其余用例照常用 gate。**不改判分器、不从用例清单里删它、不放宽比较方式**。三件缺任何一件，按真回退处理。

## 5. 订单等空表用例：补合成数据集

评测集本身没有订单（5k 实测库里没有），订单列表和计数用例在 `train` 上改前改后都在 0.2–0.8ms，全是「持平（噪声内）」，什么也证明不了。这类用例（以及别的实测库里没数据的表）要补一个合成数据集：

- 在对应结构的训练集克隆上造数，模板命名 `aegis_cmp_<名字>_tpl`，数据集条目参照 `datasets.json` 里的 `w5l_orders`（`live: false`、`bindings: train.bindings.json`、运行库 `aegis_cmp_` 开头）。`w5l_orders` 的模板是 9051135 结构的训练集克隆加合成订单约 6 千单和订单项；造数脚本没有留在 `ops-local/bench/tools/`，模板只在对照机上，机器重建后要重造。
- 行数要大到改前能被量出来（`w5l_orders` 上改前 5–54ms，改后 2–26ms，7 个用例都变好）；形状要贴近真实（同一用户多单、状态分布、金额币种），不要全是同一值。
- 只用 `--cases` 跑这几个用例，仍然跑完 5 步（含 `_gp`）；结果目录 `results/*-w5l_orders-*`。
- 合成集没有留出集对应物，只算**辅助证据**。这几个用例在 `train / train_live / holdout / holdout_stale` 上仍须「不变差」，结论表里两类证据分开列。

## 6. 结果在哪、对照机上留下什么

| 位置 | 内容 |
|---|---|
| `ops-local/bench/results/<UTC>-<数据集>-<版本>/` | `summary.md/json/csv`、`perf.json`、`correctness.json`、`meta.json`、`plans/`、`run.log` |
| `ops-local/bench/results/_<任务>_judge/` | 本次判分的 compare / consistency / gate 输出与汇总表（照 `_w5l_judge/` 的做法） |
| `ops-local/bench/results/_<任务>_driver.log`、`_*.console` | 驱动日志（含机器空闲快照）与各次控制台输出 |
| 对照机 `/root/bench/evalset/results/` | 非留出集的原始结果（留出集拉回后已删）；`/root/bench/runs/` 是 explain.sh 的原始输出，`/root/bench/tmp/` 是 prep-copy 的迁移文件与判分器工作目录 |

结论表交给用户时附：用的 BASE / HEAD 提交、数据集模板、结果目录名、机器空闲证据（steal、驱动日志里的快照）。

对照机上会留下 `aegis_cmp_*`：

- 数据集运行库（`aegis_cmp_w5l_train` 等，每次运行重建；`holdout*` 的跑完即删）；
- 补迁移的模板 `aegis_cmp_<后缀>_*_tpl`、合成集模板（复跑要用）；
- 快速诊断的副本 `aegis_cmp_<名字>`。

默认留着，直到该任务验收完且修复轮的重判也结束。要清理时，先 `docker exec bench-pg psql -U postgres -c '\l+ aegis_cmp_*'` 列出清单给用户确认（动对照机属于改仓库外的东西），只删本任务建的 `aegis_cmp_` 开头的库（`DROP DATABASE … WITH (FORCE)`）。**不碰**：`aegis`（5k 基线）、`aegis_train_tpl / aegis_holdout_tpl / aegis_holdout_stale_tpl`、`aegis_train*`、`aegis_holdout*`。

## 快速诊断：单条 SQL 改前改后

用来复现慢查询、看一条改写是否值得做。没有噪声底、没有留出集，**不能当合并依据**。

对照机：容器 `bench-pg`（postgres:18，512M、max_connections 60、shared_buffers 128MB，与生产 compose 一致，只绑本机回环），库 `aegis` 是 5k 实测库原样，**不要改它**；dump 在容器内 `/tmp/d.pgdump`。

1. 建副本并只应用该分支新增迁移的 Up 段：`scripts/prep-copy.sh aegis_cmp_<名字> <worktree>/panel/migrations/<新迁移>.sql ...`（pg_restore 约 12 秒，打印每个迁移的耗时）。
2. EXPLAIN 脚本照 `ops-local/vultr-test/5k-diag/q*-explain.sql` 的写法：`BEGIN; SET LOCAL ROLE aegis_app; SELECT set_config('app.tenant_id', …, true);`，再 PREPARE、EXPLAIN (ANALYZE, BUFFERS)、ROLLBACK；每段用「-- A1 说明」这样的行开头。
3. 跑：`scripts/explain.sh <库名> <脚本.sql> [on|off|both]`，输出每段的 Execution Time。生产已关 JIT，以 off 为准，on 用来看 JIT 是否会被触发。
4. 比：同一脚本至少连跑两遍，提升要超过两遍之间的差；改写过的查询用 `SELECT md5(string_agg(t::text, '|' ORDER BY t::text)) FROM (<查询>) t` 在两边比结果集，不一致直接判失败。

坑：

- psql `-q` 不回显 SQL 注释，输出只能按顺序和段对上：用 explain.sh（它把「-- A1」行换成 `\echo` 标记），不要自己数。
- `EXECUTE q(ARRAY(SELECT …))` 会报 cannot use subquery in EXECUTE parameter，同一事务后面全部变 aborted：先 `\gset` 取值再传，或把子查询写进 PREPARE 体。
- 从 pg_stat_statements 或日志抄来的语句常被压成一行，行内的 `--` 注释会把后面整段都吞掉：先去掉注释再用。
- **统计信息新旧会改变计划**：刚 ANALYZE 过的库估价低、JIT 不触发，实测时估价高一倍、触发 JIT。对比前两边都 ANALYZE；要复现线上问题，就额外测一份不 ANALYZE 的。
- `aegis` 里的 `node_alive_ips` 全部早已过期，与负载中的真实状态不同；测在线设备相关的查询，要把 `last_seen_at` 平移到窗口内（评测集的 `train_live` 用 `ops-local/bench/remote/datasets/live-shift.sql` 做了这件事）。
- `CREATE DATABASE … TEMPLATE aegis` 要求模板库上没有其他连接，评测集在用时会失败；prep-copy 用 pg_restore，不受影响。
- ssh、docker exec、psql 三层嵌套时引号极易出错：把 SQL 写进文件再 scp，`docker exec -i … psql < 文件`；脚本里用 `bash -s` 加 heredoc。
- 副本名必须是 `aegis_cmp_` 开头，同名会被删掉重建；不要拿 `aegis`、`aegis_train*`、`aegis_holdout*` 做实验。
- 对照机是共享 CPU：先确认空闲（见第 1 节）。

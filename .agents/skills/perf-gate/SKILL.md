---
name: perf-gate
description: pandora 的同机性能闸门：在固定测试机上先量基线提交、再量候选提交，按相对变化判定改动变快、持平还是变慢，产出「改前改后」数字。只要涉及性能或占用就用它——做性能优化、改订阅拉取、节点建流与名单、后台统计、连接池、缓存、SQL、队列任务等热路径，有人问「这样会不会变慢」「快了多少」，要给优化配数字，或合入前想确认没有性能退化，都先用这个 skill，即使没提「闸门」二字。里程碑级的绝对容量测试（多台 Vultr、真节点）见 panel/tests/perf/README.md。节点端（pdnd）的同机闸门也在这里（pdnd/tests/perf/gate）。
---

# 性能闸门

性能改动的事实来源是测出来的数字，不是推理。闸门做的事：同一台机器、同一份数据、同样的压力，基线与候选各跑一遍，逐项比相对变化，给出 **退回 / 未达预期 / 可合**。

脚本与机制全在 `panel/tests/perf/gate/`（入口 `gate.sh`，细节见同目录 `README.md`）。这个 skill 讲什么时候用、怎么排步骤、怎么读结论。

## 先过一遍：这次改动需要闸门吗

| 情况 | 怎么做 |
|---|---|
| 性能优化（任何「为了更快、更省」的改动） | 必须过闸门，带目标指标（`--target`） |
| 改了热路径的查询、缓存、并发、事务或数据量（订阅拉取、节点会话与名单、上报、后台列表与统计、连接池、中间件、每请求都走的代码） | 过闸门，不给 `--target`，只看有没有退化 |
| 热路径上的纯重构（拆函数、改名，不动查询与数据结构） | 先拿本地证据：用 `panel/internal/pkg/reqstats` 写一条 PG 用例钉住一次请求或一轮的 SQL 条数（参照 `internal/pkg/db/stats_pg_test.go`），纯内存部分在改前改后各跑 `go test -bench . -benchmem -count 10` 用 `benchstat` 比。SQL 条数变了或 benchmark 明显变差，再上闸门；否则合入，由发版前的整合闸门兜底 |
| 文档、前端样式、只在后台低频点的功能、迁移只加列 | 不用 |

优化要从测出来的瓶颈出发（P8 报告 `ops-local/p8/REPORT.md`、闸门结果、pprof、`pg_stat_statements`），先写清「哪个指标、预计变多少」，再动代码。

## 场景与指标

一次运行约 30 分钟，按固定顺序跑完下面全部场景（`remote.sh run`）：

| 场景 | 测什么 | 主要指标（`compare.py` 里的名字） |
|---|---|---|
| 连入 | 200 台 nodesim 冷启动全部连上 | `connect.t_all` |
| 静默 5 分钟 | 只有心跳与上报时的占用 | `idle.cpu.<服务>`、`idle.mem.<服务>`、`idle.pg_new_sessions_per_min`、`idle.pg_stmt_per_min` |
| 后台读 | 等统计任务跑过一轮后，`k6/admin.js` 10 次/秒 × 70 秒 | `admin.timeseries.p50/p95/sql`、`admin.other.p50` |
| 订阅拉取阶梯 | 20→50→100→200→400→800 次/秒各 60 秒，饱和即停 | `pull.<速率>.p50/p95/p99/cpu_ms/pg_cpu_ms/sql/pg_sessions/mem_max`、`pull.max_ok_rate` |
| 重启重连 ×3 | `systemctl restart pandora-serve` 后 200 台全部回来 | `reconnect.t_all`、`reconnect.align_busy`、`reconnect.pg_stmt` |

改动落在哪条路径，就把对应指标作为 `--target`。例：连接池 → `idle.pg_new_sessions_per_min`；订阅拉取 → `pull.max_ok_rate`；建流 → `reconnect.t_all`；统计曲线 → `admin.timeseries`。

## 步骤

前提：测试机已按 `gate/README.md`「测试机的前提」「一次性准备」装好、造好模板（`gate.sh check` 通过）。主目录 `ops-local/netcup-perf/gate.env` 里有 `GATE_HOST=<别名>`。动机器前先读 `~/ai/servers/<别名>/AGENTS.md`。

1. **定基线**：候选分支的合并基点（`git merge-base <主线> <候选>`），或主线当前提交。基线和候选的差别只能是这次改动。
2. **当天现跑基线两遍（A/A）**：`gate.sh run <名>-a <基线>`、`gate.sh run <名>-b <基线>`。数据里有随日期变的东西（到期、统计汇总），旧基线不能拿来比；两遍之差是噪声底。同一天内多项改动共用一个基线时，A/A 只跑一次。
3. **跑候选**：`gate.sh run <候选名> <候选提交>`。
4. **对比**：`gate.sh compare <名>-a <候选名> --aa <名>-b [--target <指标前缀>]…`。
5. **判定与处理**：
   - **退回**：有指标退化。先把候选再跑一遍排除噪声；确认是真退化就改代码，不合。
   - **未达预期**：没退化，但目标指标没改善。优化没生效，回头查原因（profile、SQL 计数），别靠调阈值过关。
   - **可合**：把改善项的改前改后数字写进提交说明或 PR，并记进 TASKS.md。
6. **多项一起合入后、发版前**：对整合后的提交再跑一遍，与同一基线比，确认叠加后没有互相抵消，也兜住没单独过闸门的小改动。

每次 `gate.sh run` 都会在机上先构建、再后台跑、等完、取回，前台要等 30 分钟以上，所以：

- 在 Claude Code 里用 Bash 的 `run_in_background: true` 跑 `gate.sh run`，完成通知来了再看结果；中间去做别的事。
- 同一时间只跑一个运行，测量不和构建、PG 维护重叠（`gate.sh run` 自己保证先构建再开测）。
- ssh、scp 要连 1Password SSH agent：Bash 调用设 `dangerouslyDisableSandbox: true`。

## 读结果

结果在主目录 `ops-local/netcup-perf/gate/<label>/`（被 git 忽略）：`result.json` 汇总，`snap/` 原始快照，`k6/` 客户端数据，`serve.log`、`worker.log`，`notes.txt` 异常备注。

- 延迟用服务端访问日志（`access_stats.py`），不受压测端排队影响。k6 的数字只用来看失败和掉请求。
- 阈值：p50 +5%（n≥100）、p95/p99 +10%（p99 n≥1000）、内存 +10%、每请求 CPU +5%。给了 `--aa` 时取缺省值与两倍噪声中较大的一个。样本不够判「不能判」，不算通过也不算退化。
- 闸门只管性能，不验证正确性（缓存失效、并发正确性靠单元与 PG 用例和 CI）。
- nodesim 的节拍必须与真实节点（pdnd `node/server.go` 的 `Run`）一致：上报与心跳每轮都按 `panel.Jitter` 抖动 ±10%。不抖的话，冷启动越快，200 台的心跳越整齐地挤在同一秒，静默期的连接池占用和内存是被模拟器放大的（10-14：o3 把冷启动压到 1 秒后，旧 nodesim 的心跳不抖，面板静默期常驻 26 条连接，误当成连接池改动的退化）。看到静默期 `pool.serve.wait_count` 接近心跳总数、或常驻连接数随冷启动变快而涨，先怀疑这个。
- 换了 nodesim 二进制（`/root/gate/bin/nodesim`，从 `pdnd/release/nodesim` 交叉编译），旧结果不能和新的比：先重跑基线 A/A 两遍，再跑候选。
- 只比相对变化：面板侧被切到 2 核 4G，与 P8 的 Vultr 2c4g 同规格，但机器不同、没有 nginx、走回环，绝对数不能和 Vultr 上的数字比。

报告给用户用一张表：指标 | 基线 | 候选 | 变化 | 判定，外加总评与噪声底，并注明运行的 label、提交、日期。

## 什么时候要重做模板

`reset` 每次从模板库重建，再跑 `migrate up`，新迁移会自动生效。以下情况重跑 `gate.sh seed` 与 `snapshot`（约 40 分钟，节点接入有限速）：

- `perfseed` 或 nodesim 改了造数口径；
- 迁移改了已有数据的形状，模板升级上来与新造的数据不一样；
- 模板放了太久，到期、统计这类随日期变的数据已明显偏离（同一天的 A/B 不受影响，跨很多天的对比才有关系）。

重做模板之后，旧的运行结果都不能再和新的比。

## 节点端闸门（pdnd）

改 **pdnd** 热路径时必须考虑节点端闸门：kernel 适配器、转发与计量、用户表与名单应用、会话与缓存、hy2/TUIC 的 UDP 收发等。**性能优化**必须带 `--target` 前缀；只防退化可以不带。

脚本在 `pdnd/tests/perf/gate/`（入口 `gate.sh`，机制见同目录 `README.md` 与设计契约 `.claude/briefs/nodegate-design.md`）。

步骤与面板闸门相同：**当天基线跑两遍 A/A** → **跑候选** → `gate.sh compare <基线-a> <候选> --aa <基线-b> [--target …]`。

| 改动落在 | `--target` 前缀示例 | 主要指标键（`result.json`） |
|---|---|---|
| 接入 / 名单全量 | `roster.` | `roster.enroll_s`、`roster.connect_s`、`roster.full_ack_s`、`roster.health_s` |
| 名单增量 | `roster.` | `roster.delta_ack_s`、`roster.delta_conn_drops` |
| 空闲长连接占用 | `idle.` | `idle.rss_kb_per_conn`、`idle.cpu_pct`、`idle.connect_p50` |
| 握手 | `handshake.<协议>` | `handshake.<P>.rate`、`handshake.<P>.p50` |
| 吞吐 | `tput.` | `tput.<P>.mbps`、`tput.<P>.cores_per_gbps` |
| UDP | `udp.` | `udp.<P>.<R>.mbps`、`udp.<P>.<R>.loss_pct` |
| 面板故障 | `failover.` | `failover.session_recover_s`、`failover.conn_drops` |

- **单机**：`ops-local/nodegate/gate.env` 里 `NODEGATE_HOST=<别名>`；结果在 `ops-local/nodegate/<label>/`。
- **发版前绝对数**：多机三台 Vultr 内网机，`NODEGATE_NODE` / `LOAD1` / `LOAD2` 与 `NODE_IP`、`LOAD1_IP`、`CIDR`。
- `gate.sh run` 前台很久：Claude Code 里 Bash **`run_in_background: true`**；ssh 要 **`dangerouslyDisableSandbox: true`**。
- 读结果格式同面板：指标 | 基线 | 候选 | 变化 | 判定 + 总评；注明 label、提交 sha、`tools_id`、日期。
- 换了 `tools/`（`tools_id` 变）须重跑基线 A/A。

## 红线

- 测试机上只放 seed 造的虚构数据，不上传任何密钥、1Password 内容或私有 gitleaks 规则；测试端口只听回环。
- 机上日志、安装记录可能带后台路径前缀：`gate.sh` 的输出已打码，自己 ssh 看日志时先过 `sed -E 's#/ops_[0-9a-f]+#/ops_<隐去>#g'`，安装日志不原样输出。
- 仓库里不写机器别名、IP、域名，结果不进仓库。
- 测试机不重装、不改系统级设置、不改 SSH；新装的东西登记到机上 `/root/README.md`「装了什么」。

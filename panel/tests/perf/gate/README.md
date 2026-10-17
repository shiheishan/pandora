# 性能闸门

同一台测试机上、同一份数据、同样的压力，先量基线提交、再量候选提交，按相对变化判定一项改动是变快了、持平还是变慢了。
用来给每一项性能优化出「改前改后」的数字，也用来防止普通改动悄悄变慢。

- **只判相对变化**：面板侧（serve、worker、PostgreSQL、Redis）被切到 2 个核、4G 内存，与 P8 在 Vultr 2c4g 上测的规格相同，
  但机器不同、没有 nginx、压测走回环，绝对数字只能和同一台机器上的数字比。里程碑上的绝对数仍在 Vultr 上测（`../README.md`）。
- **基线当天现跑**：数据里有到期时间、统计汇总这类随日期变的东西，几天前的基线数字不能拿来比。每次比较都现跑一遍基线。
- **先量噪声**：同一提交跑两遍（A/A），两遍之差就是这台机器的噪声。`compare.py --aa` 用它放宽阈值，噪声大到盖过阈值的指标判「不能判」以外，还会标「噪声大」。

## 文件

| 文件 | 在哪跑 | 做什么 |
|---|---|---|
| `gate.sh` | 本机 | 入口：上传机上脚本、按提交构建、后台跑一次、等完、取回、对比 |
| `remote.sh` | 测试机（root） | 前置检查、一次性造数与快照、每次运行的重置与四个场景 |
| `collect.py` | 测试机 | 快照（cgroup CPU 与内存、PG 计数、连接池 metrics、Redis）、内存采样、等 nodesim 连齐、汇总 `result.json` |
| `compare.py` | 本机 | 读两次运行的 `result.json`，逐项判定并给总评 |

测试机用 ssh 别名指定：环境变量 `GATE_HOST`，或主仓库 `ops-local/netcup-perf/gate.env` 里写一行 `GATE_HOST=<别名>`。
仓库里不写机器；结果取回到 `ops-local/netcup-perf/gate/<label>/`（被 git 忽略）。

## 测试机的前提

- 按 `panel/deploy/install.sh` 装好，跳过 nginx（`PANDORA_SKIP_NGINX=1`），`public_base_url` 用虚构域名 `https://gate.example.test`，
  `/etc/hosts` 里把它指到 `127.0.0.1`（节点通道的服务端证书只签了这个主机名，nodesim 必须用它连）。
- systemd drop-in：serve 的节点通道与 metrics 只听回环（`PANDORA_GRPC_NODE_ADDR=127.0.0.1:9443`、`PANDORA_OBSERVABILITY_METRICS_ADDR=127.0.0.1:9464`），
  worker 的 metrics `127.0.0.1:9465`；压测从同一个回环地址来，把每 IP、每账号、认证的限流放大到压不到。
- PostgreSQL 开 `pg_stat_statements`（`../sql/pg_stat_statements.sql` 的 setup）。
- 切核：`system.slice` 限 0-1 号核与 4G，`user.slice`（ssh 起的 nodesim、k6）用其余核。`remote.sh check` 不满足就拒绝跑。
- `/root/gate/bin/` 放 k6、perfseed、nodesim（`panel/tools/perfseed`、`pdnd/release/nodesim` 交叉编译）；
  `/root/gate/secrets/` 放 `admin_email`、`admin_pass`（0600）。
- 所有测试端口只听回环，不对公网提供任何服务；机器上只有造出来的虚构数据，不放任何真实密钥。

## 一次性准备

```bash
panel/tests/perf/gate/gate.sh check      # 切核、单元、只听回环
panel/tests/perf/gate/gate.sh seed       # 200 台服务器、5000 用户、30 天历史；约 40 分钟（节点接入每 IP 每分钟 10 次）
panel/tests/perf/gate/gate.sh snapshot   # 库做成模板 pandora_gate_base，nodesim 身份目录打包
```

`seed` 最后会等统计任务（每 10 分钟一轮、一轮最多补 7 天）把近 16 天的行为汇总补齐，让模板像一个跑了很久的面板。

## 每次运行

```bash
gate.sh run b0-a <基线提交>
gate.sh run b0-b <基线提交>              # 第二遍，量噪声
gate.sh run o1 <候选提交>
gate.sh compare b0-a o1 --aa b0-b --target idle.pg_new_sessions
```

一次运行约 30 分钟，在测试机后台跑（ssh 断了不影响），`gate.sh` 每 30 秒报一次进度，结束后自动取回。顺序固定：

1. **重置**：停 nodesim 与服务 → 换成这个提交的二进制（与 `build-release.sh` 同样的编译参数，前端用占位页）→ 删库、从模板重建 →
   `bootstrap.sh roles` → `pandora migrate up`（候选提交可能带新迁移）→ `bootstrap.sh grants` → 清空 Redis → 还原 nodesim 身份 → 起服务。
2. **连入**：nodesim 带 200 台服务器连上（`connect.t_all` 是冷启动全部连上的用时），再静置 60 秒。
3. **静默**（5 分钟）：没有用户流量，只有节点心跳与上报；量四个服务的 CPU、内存、每分钟新建 PG 连接与 SQL 条数。
4. **后台读**：先等统计任务在重置后跑过一轮，再用 `../k6/admin.js` 每秒 10 个请求打 70 秒；看行为趋势与各列表的服务端延迟。
5. **订阅拉取阶梯**：`../k6/sub_pull.js` 按 20、50、100、200、400、800 次/秒各打 60 秒；某一级 p95 超过 1 秒、失败超过 1%、
   掉请求超过 1% 或到达不足 95% 就算饱和，不再往上加（`pull_max_ok_rate` 是最高的不饱和那一级）。
6. **重启重连** ×3：`systemctl restart pandora-serve`，量全部 200 台重新连上的用时、「建流对齐超时」次数、收到的全量与增量名单。

延迟用服务端访问日志（`access_stats.py`），不受压测端排队影响；k6 的客户端数字也留在结果里。

## 判定

`compare.py` 的阈值沿用 P8 之前的闸门规则：延迟 p50 +5%（至少 100 个样本）、p95/p99 +10%（p99 至少 1000 个样本）、内存 +10%、
每请求 CPU +5%；给了 `--aa` 时，阈值取缺省值与两倍噪声中大的那个。计数类指标在 0 附近有绝对容差。

- **退回**：有任何一项退化。先看是不是噪声（再跑一遍候选），确认是真退化就改代码。
- **未达预期**：给了 `--target`，但目标指标没有一项改善。
- **可合**：没有退化；结果里列出改善项，作为这项优化的改前改后数字。

## 注意

- 测量与构建、PG 维护不同时跑：`gate.sh run` 先构建再开测；同一时间只跑一个运行。
- 每次运行的完整数据都在结果目录：`result.json`（汇总）、`snap/`（快照）、`k6/`、`serve.log`、`worker.log`、`nodesim-status.json`、`notes.txt`（异常备注）。
- 内存退化要定位时跑诊断运行：`GATE_PPROF=1 gate.sh run <label> <提交>`。serve 经 /run 下的运行时 drop-in 开 pprof（`127.0.0.1:6061`，下一次不带 `GATE_PPROF` 的运行重置时撤掉），每个窗口结束取一份堆剖析到 `pprof/<窗口>.heap`（`go tool pprof`）与 `.heap.txt`（带 MemStats）。诊断运行可以缩短各段（`GATE_IDLE_SECS`、`GATE_PULL_RATES` 等），只用来定位，不拿来比。
- 机器上的服务日志里可能有后台路径前缀：在对话、报告里引用日志前先打码（`gate.sh` 的输出已经打码）。

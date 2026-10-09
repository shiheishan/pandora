---
name: perf-gate
description: pandora 性能闸门：证明某个分支「同机、同数据、不变慢、带余量」，出一页 verdict.md 交 accept-task。按改动类型选层（SQL 走 bench-eval；往返与分配次数走 CI 守卫，P0 落地后接上；Go 热路径与资源占用走 Vultr 同机 A/B），同机 A/B 是改前改后两个发布包原地升级、先测 A/A 噪声底、再各跑静默 A 档（完全静默）、B 档（30% 在线）与稳态、最后升回改前，按新标准与比值线（p50 ≤+5%、p99 ≤+10%、内存 ≤+10%、节流不增）判分。验收性能类分支、第 ③ 阶段每路优化、换栈 S0–S9 每一步，或总协调说「性能闸门」「改前改后 A/B」「同机对比」「有没有变慢」「A/A 噪声」时使用。整轮成绩单（seed、30 分钟稳态全套、和上一轮比）用 prod-retest；单条 SQL 判分用 bench-eval；节点端 pdnd 压测用 node-accept。
---

# 性能闸门

目标：每个性能相关的分支合并前，都有一份「同一台机器、同一份数据、改前 vs 改后」的结论，回答两件事：**有没有比改前变差**（回退闸门，不过就退回），**离新标准还差多少**（新标准，写进结论与 TASKS）。

分工：本 skill 只做一个分支的改前改后判断，借用 prod-retest 的机器、装法、seed 和稳态脚本，不重复它的步骤；整轮成绩单是 prod-retest 的事。新标准的数值只在 `.claude/perf-plan/PLAN.md`「新标准」一处，判定线分别写在 `panel/tools/loadtest/quiet/report.go`（静默两档）与 `prod-retest/scripts/targets.py` 的 `classify()`（端点分档）。

## 选层（按改动类型，可以叠加）

| 改了什么 | 走哪层 | 合并依据 |
|---|---|---|
| SQL 文本、索引、迁移里的函数、计划相关的会话参数 | bench-eval 评测集判分 | 训练集与留出集都不变差、结果一致（见 bench-eval） |
| 每请求库往返数、Valkey 往返数、热路径分配次数 | CI 守卫（见「CI 守卫层」） | P0 落地前空缺；落地后守卫测试红即退回 |
| Go 热路径、缓存、连接池、节点协议节拍、部署参数（nginx、systemd、GOGC 等）、任何会动 CPU 或内存的改动 | Vultr 同机 A/B（下文） | verdict.md 的结论 |

改了 SQL 又改了 Go 的，两层都走。只改注释、纯挪动的不走（见 accept-task 的自证）。

## 同机 A/B

### 0. 准备（一次，改前改后共用）

- 机器：按 prod-retest「机器与现场参数」开面板机与压测机（test-machine 登记，走同机房 VPC，跑前报用户机器与估算）。延迟标准按 4c8g 面板机考；静默与内存两条 2c4g 也必须达标，4c8g 上测的静默数只作参考。
- 现场目录 `ops-local/<轮次>/gate-<分支短名>/`，`env.sh` 照 prod-retest 的 `scripts/env.example.sh` 填，`LOCAL_DIR` 指向这个目录；机器名只经 `PANEL_HOST`、`LOADGEN_HOST` 传，不写进脚本。
- **改前 = 分支基点**（`git merge-base feat/panel-redesign <分支>`），**改后 = 分支头**。两个包一开始就在面板机上按 panel-install「首装」第 2、3、5 步各打一份（`PANDORA_VERSION=vt-<sha>`，各解到自己的目录），测量开始后不再在面板机上构建（2c4g 构建约 10 分钟，会污染测量）。
- 面板机装改前版本（panel-install 首装或原地升级），按 prod-retest「准备」做压测工具、realip、seed（1 万用户 / 1000 节点），然后 `bash .claude/skills/perf-gate/scripts/push.sh <env.sh>`。
- 节点代际：两边都用同一个 `-node-behavior`（现在是 `current`；N1 落地 `next` 后改用 `next`，改前改后也必须一致，verdict 会核）。

### 1. 改前：A/A 与各场景

每场静默约 26 分钟（T−8m 起节点、15 分钟窗口、收尾 2 分钟），下一场等上一场节点退出才能起（quiet-start.sh 会拒绝重叠：两批 1000 节点叠在一起会触发 Valkey 超时，w10quiet 踩过）。

```bash
S=.claude/skills/perf-gate/scripts; P=.claude/skills/prod-retest/scripts; E=ops-local/<轮次>/gate-<名>/env.sh
bash $S/quiet-start.sh $E <名>-before-qA    /root/lt-results/10k-seed/lt-manifest.json A
bash $S/quiet-start.sh $E <名>-before-qA-aa /root/lt-results/10k-seed/lt-manifest.json A
bash $S/quiet-start.sh $E <名>-before-qB    /root/lt-results/10k-seed/lt-manifest.json B
bash $S/quiet-start.sh $E <名>-before-qB-aa /root/lt-results/10k-seed/lt-manifest.json B
bash $P/start.sh $E <名>-before-steady /root/lt-results/10k-seed/lt-manifest.json
bash $P/pull.sh $E <场景>        # 每场两端都 done 后拉回
```

- A/A 就是改前版本同档连跑两场，两档都要（噪声底随档位差很多）；稳态 A/A 只在改动碰门户、后台或写路径时加（再跑一场 `<名>-before-steady-aa`）。
- quiet-start.sh 每场前自己做测前清理：压测机没有残留 loadtest、面板机 `swapoff -a && swapon -a`。
- 正式轮不开 pg_stat_statements 与 pprof：开着的话先按 prod-retest「收尾」关掉（`pgstat.sh disable --yes`、还原 `.env` 重启网关）。稳态场景照 prod-retest 的采样器跑，两边同口径；run-collect 里的 pgss reset 与 pprof 抓取在关着时会报错并跳过，看 collect.log 即可。

### 2. 改后

1. 面板机进改后包的 `deploy/` 跑 install.sh，做法与核对点见 panel-install「原地升级」（日志打时间戳、迁移号、healthz、登录）。
2. 升级前的 pg_dump 会把 PG 页挤进 swap（w10quiet v1 的已用被低估 88 MB），所以升级后第一场照样由 quiet-start.sh 清 swap；1000 个节点会在升级停服时断线重连，第一场的 T−8m 起跑段足够它们回到稳态。
3. 同样跑 `<名>-after-qA`、`<名>-after-qB`、`<名>-after-steady` 并拉回。

### 3. 判分

```bash
python3 .claude/skills/perf-gate/scripts/verdict.py --title "<分支> <基点短 sha> → <头短 sha>" \
  --quiet  <目录>/<名>-before-qA <目录>/<名>-after-qA <目录>/<名>-before-qA-aa \
  --quiet  <目录>/<名>-before-qB <目录>/<名>-after-qB <目录>/<名>-before-qB-aa \
  --steady <目录>/<名>-before-steady <目录>/<名>-after-steady \
  > <目录>/verdict.md
```

- 它在仓库 `panel/` 下 `go build` 一份 loadtest 调 `quiet-report -json`（与 `npm ci` 不并发，见根 CLAUDE.md）；静默档位按 `nodes.json` 的 `meta.online_ratio` 自动定。
- 一页四节：同机同数据核对（节点与用户负载参数、`host.txt` 的 machine-id 与面板版本、窗口、MemTotal、诊断开关、稳态请求总数差 ≤ 5%）、回退闸门、新标准、CI 守卫。
- **回退闸门的线**（用户 10-09；每请求 CPU 取 `stack-migration-design.md` §6）：各节点与用户端点 p50 ≤ +5%（样本 ≥ 100）、p99 ≤ +10%（样本 ≥ 1000）；整机已用与面板 + 数据库 PSS ≤ +10%；面板 + 数据库每请求 CPU ≤ +5%；三网关 Δnr_throttled 不增。超线但在「线 + A/A 噪声底」以内判复测。
- **结论**，accept-task 照它办：
  - `不能判`：同机同数据核对没过，换场景重跑；
  - `退回`：有一项变差超出线与噪声底；
  - `复测`：只落在噪声带，改前再跑一场（升回改前后补一场，构成 A-B-A 也行）再判；
  - `可合，未达新标准 N 项`：没变差，没达标的项进 TASKS；brief 承诺了其中某项的，按退回处理；
  - `达标`。
  - 后缀「非正式数据」：诊断开关开着或机器指纹缺失，只能参考，不作合并依据。
- 延迟是压测机端口径；新标准里节点、订阅、后台的线是服务端计时，压测机端判偏严，超线不到 2ms 的项在报告里注明待服务端计时复核（逐路由服务端计时等 P0 的访问日志）。

### 4. 升回改前

- 分支带新迁移（`git diff --name-only <基点>..<头> -- panel/migrations`）：先用**改后**包的 `migrate.sh rollback-to <改前最大迁移号>`（前提、确认短语与 irreversible 的限制见 panel-ops 与 `panel/deploy/MIGRATION-RUNBOOK.md` 第 2 节，会删数据，先问用户），再跑改前包的 install.sh；回不去的（irreversible、Down 拒绝）停下报告，不重装数据基座。
- 没有新迁移：直接跑改前包的 install.sh。
- 核对：三网关 healthz 200、`/opt/aegispanel/deploy/release-artifact.env` 的版本是改前、迁移号等于改前包最大号；在面板机 `/root/README.md` 的「现状」写上。

### 原因诊断（不进判分）

结论是退回或复测、要找原因时，另起诊断场：先按 prod-retest「观测开关」开 pg_stat_statements 与 aegis-node 的 pprof，再 `quiet-start.sh … <A|B> --diag`（多抓 T+5m 的 60 秒 CPU profile 与堆、窗口两端 pgss reset / 导出前 30）。两边诊断场的 pgss 并排：`verdict.py pgss <改前>/pgss/pgstat-*-total.csv <改后>/pgss/pgstat-*-total.csv 15`。内存拆账看各场的 `mem-detail.txt`、`kern-*.txt`、`pg-smaps-*.txt`。

### 只改部署参数时：一场内 A-B-A

环境变量、systemd 资源上限这类不用打包的改动，用 `scripts/toggle-ab.sh` 在同一批节点下做缺省 → 变体 → 缺省三段（每段 8 分钟，切换后等 3 分钟），比两个发布包便宜得多：

- 压测机照常起节点，窗口拉长到覆盖三段：`WINDOW_MIN=35 ONLINE_RATIO=<0|0.3> setsid -f ./run-quiet.sh …`；面板机在 T 起 `toggle-ab.sh <输出文件> <单元> <drop-in 内容文件>`（不起 gate-collect）。
- 退出（含被 kill）时它一定删掉 drop-in 并重启单元。判法：变体段要落在两个缺省段之外、差值超过两缺省段之差，才算有差别（w10quiet 的 GOMAXPROCS=1：46.8 落在 42.9 与 49.4 之间，判无收益）。结论人写进 verdict.md，格式同上。

## CI 守卫层（P0 落地前空缺）

`.claude/perf-plan/PLAN.md` 第 0 波 P0：往返记账器（访问日志带 `db_rt`/`kv_rt`）、逐路由往返预算 PG18 测试、只读 InTx 静态守卫、「一份 push 往返 ≤ 2、WAL ≤ 6KB」守卫，以及 `stack-migration-design.md` §6 的分配上限（`AllocsPerRun`）。这些是确定值，不受机器噪声影响，比 Vultr A/B 便宜，能拦的先在这层拦。

P0 那路合并时补本节：守卫测试名与所在 CI job、预算表的位置、改预算要谁同意、访问日志 `db_rt`/`kv_rt` 在 A/B 里怎么按路由出分布，并把 verdict.py 第 4 节从「空缺」改成读这些结果。在此之前，accept-task 对往返类改动只能看分支自带的测试与 Vultr A/B。

## 坑

- **pgrep 匹配到自己**：等待循环里 `pgrep -f '<模式>'` 会匹配执行它的 shell（w10quiet v3 因此没清 swap）。等节点退出用 `pgrep -x loadtest`。
- **两批节点重叠**：前一场节点还没退出就起下一场，2000 个节点同时在线，会触发 Valkey 超时与 nonce 回落 PG（w10quiet 对照段 PG 从 20 涨到 25），这一场作废。
- **带 1000 节点重启 aegis-node**：w10quiet 之前的版本在 256M 上限下启动峰值会被 OOM 杀（`NRestarts`）。改前版本早于 f6deb9e 时，升级或回滚后看 `systemctl show aegis-node -p NRestarts`，非 0 这一场作废。
- **只有换入没有换出**：多半是开窗前换出的旧页被读回，不是内存吃紧；但新标准要求为 0，所以测前必须清 swap。
- **只改节点数或在线比例就不是同一口径**：verdict 会因负载参数不一致判不能判；容量爬坡是 prod-retest 的 `targets.py scale`。
- **负载要真的一样**：verdict 的「稳态请求总数差 ≤ 5%」拦的是节点没起齐、半途断线这类情况（看 `nodes.log` 进度行的 `started=`、`streams=`，与 `nodes.json` 的 `meta.fleet.stream_drops`）。seed 的套餐配额是 1024 GB（`seed/options.go`），B 档每个在线用户每分钟约 9 MiB，一天的闸门用不完，不必为配额重 seed。
- **改前改后的面板版本**：`host.txt` 的 `panel_release` 取自 `release-artifact.env`；改前改后相同说明没升级成功，verdict 判不能判。

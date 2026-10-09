---
name: prod-retest
description: pandora 面板整机压测与复测：在已按生产方式装好面板的一次性 Vultr 面板机上（装法见 panel-install）seed 5k 或 1 万用户 / 1000 节点的数据，用压测机跑 runbook 第 8 节的 30 分钟稳态（模拟节点 + 用户 + burst）和静默场景，采集 pprof、pg_stat_statements、CPU 拆分与连接池旁证，按分档目标出成绩单并与上一轮对比。用户或总协调说「面板复测」「面板压测」「整机压测」「出成绩单」「换栈检查点」「10k 基线」「静默 CPU」「加一档节点数」「跟上一轮比」时使用。不是本 skill：节点端（pdnd）压测、节点验收、VPC 复测、10 万连接用 node-accept；单条 SQL 改前改后判分用 bench-eval；逐协议能不能连用 node-e2e。
---

# 生产规模复测

目标：每个换栈检查点都在**同一种机器、同一种装法、同一套负载参数**下重跑一遍，得到可以逐项对比的成绩单。工具只负责测得出、测得准；测出的问题记进成绩单，不在压测中途改面板。

runbook 是 `panel/tools/loadtest/README.md`，本 skill 是它的「照做版」：把 2026-10-06/07 两轮（`ops-local/vultr-test/`、`ops-local/vultr-test2/`）手工做过的压测步骤固化成脚本，背景和原因看 runbook 对应节号。装面板属于 panel-install，开机与回收属于 test-machine。某几轮的实测结论（观测开销、p99 尾巴等）不写在这里，在对应轮次的 `ops-local/<轮次>/` 里（例：`ops-local/vultr-test2/5k-r5/notes-from-skill.md`）。

## 红线

- 红线（仓库公开、现场值与口令的放置、测试机不删不重装）见根 CLAUDE.md「红线」与 test-machine skill。
- 不在对照机 bench 的 `bench-pg`、`aegis*` 库、`bench-e2e-*` 容器上动手（bench 当压测机时只用它跑 loadtest）。
- 本仓库的代码不改；发现产品 bug 记进成绩单，交总协调派任务。

## 机器与现场参数

| 角色 | 规格 | 跑什么 |
|---|---|---|
| 面板机 `$PANEL_HOST`（5k 档） | Vultr 共享型 2c4g，Debian 13 | install.sh 生产模式（三网关 + nginx + Docker 里 PG18/Valkey，装法见 panel-install），seed、采集脚本 |
| 面板机 `$PANEL_HOST`（1 万用户 / 1000 节点档） | 4c8g 独享（`voc-c-4c-8gb-75s-amd`，用户 10-09 定：这一档的延迟目标按 4c8g 面板机考） | 同上 |
| 压测机 `$LOADGEN_HOST` | 同机房 2c4g | `loadtest nodes / users / burst` |
| （1000 节点档）压测机 ×2 | 同机房 2c4g | 一台只跑 `loadtest nodes -nodes 1000 -stagger 180s`，一台只跑 `loadtest users`；nodes 单进程 1000 节点的资源见 runbook 第 11.2 节 |

10-09 之前的 10k-r1 基线是在 2c4g 上跑的，换 4c8g 后的数不能直接当「同机型对比」，对比表和成绩单要写明各轮的面板机机型。静默场景的两条资源标准（内存、CPU）不分机型。

开机与登记走 test-machine skill，装面板走 panel-install skill。每轮在 `ops-local/<轮次>/` 下准备：

- `env.sh`：从 `scripts/env.example.sh` 复制后填写（ssh 别名、域名、IP、管理员邮箱、结果目录、对比基线）；
- `admin-cred.txt`（第 1 行邮箱、第 2 行口令）、`admin-path.txt`（后台前缀），都 0600；
- `phase-a-notes.md`：现场记录，可以含真实值，格式仿 `ops-local/vultr-test/phase2-notes.md`。

下文 `$S` 指本 skill 的 `scripts/` 目录，命令都在仓库根目录执行；各脚本的用法看文件头注释。

## 准备：面板机装好 → 压测工具 → realip → seed

1. **先按 panel-install 装好面板机**（版本、证书、nginx、管理员都在那边）：做到后台能登录、三网关 healthz 200、https 可访问，并把 `admin-cred.txt`、`admin-path.txt` 拉回 `ops-local/<轮次>/`。压测机只需要 test-machine 的工具链脚本（要在上面构建 loadtest）。
2. **压测工具**：
   - 面板机和压测机各用同一份源码构建：`cd panel && CGO_ENABLED=0 go build -buildvcs=false -o <路径>/loadtest ./tools/loadtest`。面板机放 `/root/lt/loadtest`，压测机放 `/root/loadtest`。
   - 然后执行 `$S/push-scripts.sh ops-local/<轮次>/env.sh`，推送采集脚本和压测机上的 0600 地址、口令文件。
3. **realip**（原因与做法见 runbook 第 4 节）：在面板机执行 `/root/lt/nginx-realip.sh enable <LOADGEN_IP>`（脚本由上一步推上去）。验证：从压测机带 `X-Real-IP: 198.18.x.x` 请求 /healthz，`/var/log/nginx/aegis-access.log` 最后一行的来源应是这个地址。收尾或删机前 `nginx-realip.sh disable`。
4. **seed**（面板机，参数与自检见 runbook 7.2）：
   - 包装脚本只从 0600 文件读口令，并经环境变量 `LOADTEST_DATABASE_URL` 传给 seed（`seed/options.go` 认它），不放命令行参数；参考 `ops-local/vultr-test2/` 现场的 `run-seed-5k.sh`、`ops-local/vultr-test2/10k-r1/scripts/run-seed-10k.sh`；参数 `-users 5000 -nodes 198 -label 5k -out /root/lt-results/5k-seed/lt-manifest.json`。
   - **1 万用户 / 1000 节点档**（2026-10-08 起的整机标准）：参数 `-users 10000 -nodes 1000 -label 10k -admin-workers 8`，并直连面板机回环网关（`-admin-base http://127.0.0.1:9001 -node-base http://127.0.0.1:9003 -public-base http://127.0.0.1:9000`，端口取 `.env`），不经 nginx；单会话要约 25 分钟，8 会话预计 5 到 6 分钟。上一批留在库里时 `-retire-previous`（缺省开）会先清上一批（顺序与可重复性见 `.claude/rules/tools-loadtest.md`「造数与清理」、runbook 第 11.1 节），中途失败原样重跑。
   - 用 `cd /root/lt; setsid -f ./run-seed-….sh > 日志 2>&1 < /dev/null` 起，退出码必须为 0（自检不过就是非 0）。
   - 记下 `seed_timings`（耗时参考值在 runbook 7.2）。
   - manifest 拉回 `ops-local/<轮次>/<label>-seed/`（0600），再拷一份到压测机。
   - 在面板机 `/root/README.md` 的「现状」补上结果目录。

## 跑一场：观测开关 → 起跑 → 看进度 → 拉回 → 报告 → 观测开销 → 收尾

1. **观测开关**（做法与原因见 runbook 第 3 节，pgstat 会重启 PG）：先 `cp -p /opt/aegispanel/deploy/.env /root/env.pre-pprof.bak`，再按 runbook 开 pprof 与 `/root/lt/pgstat.sh enable --yes`。同一轮多个场景之间不必关了再开，最后一个场景跑完再关。
2. **起跑**：`$S/start.sh ops-local/<轮次>/env.sh <场景> /root/lt-results/<label>-seed/lt-manifest.json`。
   - 它先在面板机起 `run-collect.sh`：采样器、T−1m pgstat reset、T+15m pprof、T+30m 导出、T+31m 存日志；
   - 90 秒后在压测机起 `run-load.sh`：nodes 立即起跑并在 60 秒内错开，T 起 users，T+20m burst；
   - T 取「现在 + 330 秒」。
   - users 参数固定为 `-sub-interval 30m -portal-rate 5 -admin-rate 0.5 -login-rate 0.05 -portal-users 200`，节点 `-node-behavior current -strict`，与 2026-10 各轮一致。要换参数就是另一套口径，不能和旧轮直接比。
3. **看进度**：`$S/peek.sh ops-local/<轮次>/env.sh <场景>`，一条命令看两端的时间轴、nodes/users 最后一行、procs 最近一格、pgact、负载、内存和 nginx 5xx 行数。
   - users 开跑前会打限流预检，有 WARN 就停下调速率。
   - 预热 200 个门户登录应在 20 多秒内完成、0 失败。
4. **拉回**：两端 timeline 都出现 done 以后（T+33m 左右），执行 `$S/pull.sh ops-local/<轮次>/env.sh <场景>`，产物在 `ops-local/<轮次>/<场景>/{panel,loadgen}/`，另有两端的原样 tgz。
5. **自动部分**：`$S/report.sh ops-local/<轮次>/env.sh <场景> [基线场景目录]`。
   - 会生成 `auto-summary.md`、`auto-targets.md`、`auto-cpu.md` 和 `compare.md`。
   - 两轮的差别写进环境变量 `CMP_NOTE`。
   - 加了节点档就再跑 `python3 $S/targets.py scale <各档目录>`，得到 CPU 随节点数的斜率和顶到 50% 的外推节点数。
6. **成绩单**：人写 `summary.md`，格式见下一节。
7. **观测开销对照**（每轮做一次，压测停了、观测开关还开着时）：在面板机执行 `cd /root/lt; setsid -f ./overhead.sh /root/lt-results/overhead 90 > /root/lt-results/overhead.out 2>&1 < /dev/null`，约 12 分钟。它依次量基线、全部采样器、各采样器单独开，结果写在 `overhead.csv`。拉回后写进成绩单的 e 节。
8. **收尾**（最后一个场景之后）：
   - `/root/lt/pgstat.sh disable --yes`；
   - `cp -p /root/env.pre-pprof.bak /opt/aegispanel/deploy/.env`，再 `systemctl restart aegis-public aegis-admin aegis-node`；
   - 核对 pprof 端口已关、三网关 healthz 200。
   - realip 是否保留，按总协调的要求定；收尾或删机前执行 `nginx-realip.sh disable`。
   - 两端结果目录不删。

### 静默场景（用户 2026-10-07 的资源标准）

标准：5000 用户整机内存约 1G；**静默运行（只有节点在心跳、拉取、上报，没人操作）时面板 + 数据库合计 CPU ≤ 单核 30%**。每个检查点都测一次：
- 节点照常起（200 档，必要时加 300 档），**不起 users、不做 burst**，稳态 15 分钟。
- 1000 节点档：压测机上 `panel/tools/loadtest/scripts/run-quiet.sh <manifest> <node-url> <T> 1000`（T−8 分钟起跑，180 秒错开），面板机 `quiet-collect.sh <目录> <T> 15`，采完用 `loadtest quiet-report -dir <目录>`（`-strict` 不达标退出码非 0）出判定；口径与上面一致，细节见 runbook 第 11.3 节。
- 采样只用轻量方式：开头与结尾各读一次 `/proc/stat` 与各进程 `/proc/<pid>/stat`（含 cutime/cstime）做差，加 `vmstat 5`；**不跑 sample-procs 与 sample-pgact**——它们自身开销大（`overhead.sh` 实测，数字见各轮 ops-local 结果），会把静默 CPU 抬过线。
- 成绩单单列一节：面板 + 数据库（三网关、postgres、valkey、nginx）合计 CPU、整机已用内存、swap，对照标准判过或不过。

### 加一档节点（同样 5k 用户）

seed 不能往已有资源池追加节点：每次都会新建资源池和套餐。加档要保留上一批节点同时在线，所以不用 `-retire-previous`。做法是：

- 执行 `seed -users 5000 -nodes <N> -label 5k-n<N> -retire-previous=false`，在新资源池里放 N 个节点和一批新的 5000 用户，每个节点的名单仍然恰好 5000 人；
- 旧批的订阅如果还 active，会多出一批「在册但没人拉」的数据，要在成绩单里写明口径差别。

最干净的做法是按 runbook 7.1 重装数据基座（要再跑 panel-install 要求保留的发布包目录里的 install.sh）。但那会删库，要先得到用户同意。

## 成绩单（summary.md）

顺序固定，每一节都要有：

1. **时间轴**（UTC）：采集起、nodes 起（相对 T 多少）、T、预热结果、pprof、burst、导出、users 与 nodes 结束及退出码。偏离 runbook 的地方写在这里。
2. **要点**：3 到 5 条，先给结论。
3. **a. runbook 9.2 及格线**（来自 auto-summary，逐条对照 runbook 9.2；零 5xx 压测工具和 nginx 都要算）。另附网关 cgroup（nr_throttled、mem_max_events、oom_kill）与各进程 CPU 和 PSS。
4. **b. 分档用户目标**（用户 2026-10-07 定，来自 auto-targets，`targets.py` 的 `classify()` 按端点名分档）：

   | 档 | 端点 | 目标 |
   |---|---|---|
   | 门户 | `public:` 除登录外，含订阅拉取 | p50 < 5ms、p99 < 50ms |
   | 节点 | `node:` 除 stream 长连接 | p99 < 20ms |
   | 后台单条 | `admin:GET /v1/me`、`GET /v1/<资源>/{id}`、单条写 | p50 < 5ms、p99 < 50ms |
   | 后台列表、搜索、看板 | 其余 `admin:` | p50 < 50ms、p99 < 200ms |
   | 登录 | `POST /v1/auth/login`（门户与后台） | 不设速度目标；只看不超时、不排队（无 503） |
   | 全部 | — | 零 5xx、不超时、不换页、连接池不排队 |

   - 机型：1 万用户 / 1000 节点档按 4c8g 面板机考（用户 10-09 定），5k 档历史轮次是 2c4g；成绩单开头写明本轮面板机机型。

   - users 和 nodes 用稳态窗口 [T, T+30m) 的统计；预热、burst、节点的 config/report 与 stream 只有全程统计。
   - 连接池直接证据：三个网关每分钟各打一行「数据库连接池统计」（`platform/db/poolstats.go`，有取连接或建连变化时才打），字段 `empty_acquires`、`acquire_wait_ms` 是「池子不够」的直接证据；`acquired` 贴着 `max` 但 `empty_acquires` 为 0 只是轮着用、没有排队。成绩单取窗口内这几行：`journalctl -u aegis-<public|admin|node> --since … | grep 数据库连接池统计`。旁证两条：T+15m 的 goroutine profile 里停在 `pgxpool.*Acquire|puddle` 的个数，以及 pgact.csv 里各网关已建连接是否顶到池上限（public 16、admin 15、node 15，见 `.env.example`；`conn_public/admin/node` 三列同时数回环 TCP 与 unix socket 两种连法）。
5. **c. 与上一轮对比**（compare.md）：
   - 每节点每分钟请求数和节点总 QPS；
   - 5xx 与超时；
   - 各端点 p99；
   - 整机和各进程 CPU（含 postgres）；
   - swap 与内存；
   - 等连接池的 goroutine；
   - 两轮 pg_stat_statements 前 10。
   - 只改节点数的两档，另附 `targets.py scale` 的表和外推。
6. **d. pprof CPU 前 10**：三个网关各一张。
7. **e. 整机 CPU 拆分**（auto-cpu）：
   - /proc/stat 的 user、system、softirq、steal；
   - softirq 各类每秒次数；
   - 全部进程按名汇总，Docker 守护进程等也要算上；
   - 已回收子进程的 CPU（cutime）；
   - vmstat 的 st 与上下文切换。
   - 写明观测工具自身占了多少（含观测开销约多少，换机型或改采样器后重跑 `overhead.sh`）。
8. **没达标的项：根因猜测 + 证据**：每条要指到具体数字或文件，例如 pprof 热点、pg_stat_statements 某行、pgact 某个时刻、timeline 某一格。
9. **原始数据清单**。

## 坑

- 远端起后台脚本、`pkill -f` 的写法见根 CLAUDE.md「环境与工具坑」（start.sh 已这样做）。
- **`-retire-previous` 出错**：先看 `seed.log` 的 `retire_previous` 段，中途失败原样重跑即可（顺序见 `.claude/rules/tools-loadtest.md`）。
- **压测机必须带 X-Real-IP，面板机要先信任它**：否则所有请求来源 IP 相同，会撞上按 IP 限流（每分钟 240 次）和 IP 聚类，198 个节点共用一个地址就会大面积 503。做法见 runbook 第 4 节。
- **镜像自带约 7.7G 的 `/swapfile`，内存吃紧会被它掩盖**：峰值没撑爆不等于没问题，可能在换页。成绩单的「不换页」要看 sample-procs 的 `_system` 行里 `pswpin`/`pswpout` 的逐行差，再配合 vmswap 的各进程 VmSwap；换页必须记录，不能只报内存峰值。
- **5k 档的 seed 在面板机上跑会撞 nginx 节点接口的按 IP 限流**：seed 的来源是面板机自己，不在 realip 信任表里。接入节点那一段 nginx 会记 503，seed 重试后通过。数 5xx 时要把这段排除在窗口外，不要误判成压测的 5xx。1 万 / 1000 档直连回环网关（上面 seed 第 4 步），不经 nginx，没有这条。
- **采集脚本自身有开销**：sample-procs.sh 每 5 秒对每个 PID 起几次 cat 和 awk 并读 smaps_rollup，sample-pgact.sh 每 10 秒一次 `docker exec psql`；短命进程退出后只记在父进程的 cutime 上，所以整机 CPU 会明显高于各进程之和。成绩单里的「整机 CPU」要注明含观测开销，外推容量时给「含」与「扣除」两个数；换机型或改采样器后重跑 `overhead.sh`。
- **整机与各进程之和的差额**用 sample-cpustat.sh 的 allprocs.csv 补齐（procs.csv 只统计三网关、postgres、valkey、nginx）。
- **解读 p99 先看 timeline 最大格**：burst（T+20m）和每小时整点的 node push 会形成 p99 尾巴，稳态 p99 含这几格，JSON 只有 10 秒一格的 max，事后剔除不了。先看 nodes.json 的 timeline，找最大的几格落在哪里；原因不明的尾巴在 pgact 里对上时间。
- **稳态统计的 codes 可能不含 transport 错误**：targets.py 的超时一律取全程计数兜底，再看 nodes.json 的 timeline 判断它落在不在稳态窗口里。
- **nodes 收尾时会记 transport:timeout**：到时关停那一格（稳态外）的 alive 和 push 会各记 1 次。稳态窗口内为 0 才算不超时。

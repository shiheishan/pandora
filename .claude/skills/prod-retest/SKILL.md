---
name: prod-retest
description: pandora 生产规模复测：在一次性 Vultr 面板机上按生产方式（install.sh）装指定版本、seed 5k 级数据，用压测机跑 runbook 第 8 节的 30 分钟稳态（节点 + 用户 + burst），采集 pprof、pg_stat_statements、CPU 拆分与连接池旁证，按分档目标出成绩单并与上一轮对比。用户或总协调说「复测」「压测」「出成绩单」「换栈检查点」「加一档节点」「跟上一轮比」时使用。
---

# 生产规模复测

目标：每个换栈检查点都在**同一种机器、同一种装法、同一套负载参数**下重跑一遍，得到可以逐项对比的成绩单。工具只负责测得出、测得准；测出的问题记进成绩单，不在压测中途改面板。

runbook 是 `panel/tools/loadtest/README.md`，本 skill 是它的「照做版」：把 2026-10-06/07 两轮（`ops-local/vultr-test/`、`ops-local/vultr-test2/`）手工做过的步骤固化成脚本，并把踩过的坑写在最后。

## 红线

- 仓库公开：IP、域名、口令、后台前缀一律不进仓库，也不进本 skill。现场值只放 `~/.ssh/config`、`~/ai/servers/`、`ops-local/<轮次>/`（被 git 忽略）。
- 报告和对话里写别名或 `<PANEL_IP>` 一类占位符；口令只经 stdin 或 0600 文件传，不出现在命令行参数与输出里。
- 不删、不重装测试机；不在对照机 bench 的 `bench-pg`、`aegis*` 库、`bench-e2e-*` 容器上动手（bench 当压测机时只用它跑 loadtest）。
- 不读 `ops-local/**/secrets/`。
- 本仓库的代码不改；发现产品 bug 记进成绩单，交总协调派任务。

## 机器与现场参数

| 角色 | 规格 | 跑什么 |
|---|---|---|
| 面板机 `$PANEL_HOST` | Vultr 共享型 2c4g，Debian 13 | install.sh 生产模式（三网关 + nginx + Docker 里 PG18/Valkey），seed、采集脚本 |
| 压测机 `$LOADGEN_HOST` | 同机房 2c4g | `loadtest nodes / users / burst` |

开机与登记走 test-machine skill。每轮在 `ops-local/<轮次>/` 下准备：

- `env.sh`：从 `scripts/env.example.sh` 复制后填写（ssh 别名、域名、IP、管理员邮箱、结果目录、对比基线）；
- `admin-cred.txt`（第 1 行邮箱、第 2 行口令）、`admin-path.txt`（后台前缀），都 0600；
- `phase-a-notes.md`：现场记录，可以含真实值，格式仿 `ops-local/vultr-test/phase2-notes.md`。

下文 `$S` 指本 skill 的 `scripts/` 目录，命令都在仓库根目录执行。ssh 需要 1Password SSH agent，子 agent 要关沙箱才能连上。

## A 段：装机 → 证书 → install → realip → seed

1. **工具链**（面板机，压测机同理）：`ssh $PANEL_HOST 'bash -s' < .claude/skills/test-machine/scripts/install-toolchain.sh`。装的是 Go 1.26 系列最新版和 Node 22。
2. **源码**：本机 `git archive --prefix=pandora-<sha>/ <sha> > /tmp/p.tgz`，scp 后解到面板机 `/root/src/`。
3. **构建**：在面板机执行 `cd /root/src/pandora-<sha>/panel && PANDORA_VERSION=vt-<sha> bash deploy/build-release.sh /root/release`。2c4g 上约 10 分钟。必须显式给版本号。
4. **前置包**（Debian 主仓）：`apt-get update && apt-get install -y docker.io docker-compose age nginx certbot`。ufw 开着就执行 `ufw allow 80/tcp; ufw allow 443/tcp`。
5. **证书**：装 e65faec 及以后的发布包，直接在第 6 步给 install.sh 加 `PANDORA_CERTBOT=1`（它自己用 webroot 申请、只停用发行版原样的 default 链接、渲染 nginx 并 `nginx -t` 后 reload、ufw active 时放行 80/443）。更早的发布包仍要手工：趁 Debian 默认站点还占着 80，执行 `certbot certonly --webroot -w /var/www/html -d <域名> --non-interactive --agree-tos --register-unsafely-without-email`，再 `rm /etc/nginx/sites-enabled/default`。没有域名就用 `<IP 用横线>.sslip.io`。撞上 Let's Encrypt 限额就停下汇报。
6. **安装**：
   - 先核 `sha256sum -c *.tar.gz.sha256`，再解到 `/opt/pandora-release/`，在包内 `SHA256SUMS` 上执行 `sha256sum -c`。
   - 然后：`cd <包>/deploy && PANDORA_ASSUME_YES=1 PANDORA_PUBLIC_BASE_URL=https://<域名> ./install.sh > /root/install-1.log 2>&1`，再 `chmod 600` 这个日志（里面有后台前缀）。
   - 核对：迁移 `0 → <最新号>`；三网关 :9000/:9001/:9003 的 healthz 都是 200。
   - **保留发布包目录**：档与档之间重装数据基座要再跑它的 install.sh。
7. **nginx**：执行 `/opt/aegispanel/deploy/render-nginx.sh && nginx -t && systemctl reload nginx`。核对 https 的 /healthz 返回 200、http 跳 308、后台入口返回 200。
8. **管理员**：
   - 在面板机上生成随机口令写进 `/root/lt-admin-cred.txt`（0600）；`AEGIS_ADMIN_PATH` 写进 `/root/lt-admin-path.txt`。
   - 执行 `cd /opt/aegispanel && set -a && . deploy/.env && set +a && printf %s "$pw" | ./bin/aegis-adminctl create --email ltadmin@example.com --password-stdin --role platform_admin`。
   - 两个文件 scp 回 `ops-local/<轮次>/`，保持 0600。
9. **压测工具**：
   - 面板机和压测机各用同一份源码构建：`cd panel && CGO_ENABLED=0 go build -buildvcs=false -o <路径>/loadtest ./tools/loadtest`。面板机放 `/root/lt/loadtest`，压测机放 `/root/loadtest`。
   - 然后执行 `$S/push-scripts.sh ops-local/<轮次>/env.sh`，推送采集脚本和压测机上的 0600 地址、口令文件。
10. **realip**：在面板机执行 `/root/lt/nginx-realip.sh enable <LOADGEN_IP>`。验证方法：从压测机带 `X-Real-IP: 198.18.x.x` 请求 /healthz，`/var/log/nginx/aegis-access.log` 最后一行的来源应是这个地址。
11. **seed**（面板机，runbook 7.2）：
    - 包装脚本只从 0600 文件读口令，参考 `ops-local/vultr-test2/` 现场的 `run-seed-5k.sh`。
    - 参数：`-users 5000 -nodes 198 -label 5k -out /root/lt-results/5k-seed/lt-manifest.json`。
    - 用 `cd /root/lt; setsid -f ./run-seed-….sh > 日志 2>&1 < /dev/null` 起，退出码必须为 0：自检要求首尾两个节点的签名配置和 UniProxy 都通过、名单恰好是本批用户、订阅拉取 ok。
    - 记下 `seed_timings`：198 个节点约 5 分钟，300 个约 7.7 分钟，后台请求按 260ms 间隔。
    - manifest 拉回 `ops-local/<轮次>/<label>-seed/`（0600），再拷一份到压测机。
12. 在面板机 `/root/README.md` 补「现状」：版本、目录、结果目录、口令文件位置（只写位置不写值）。

## B 段：采集 → 节点 → 用户 → burst → 导出 → 成绩单

1. **观测开关**（runbook 第 3 节）：
   - 先 `cp -p /opt/aegispanel/deploy/.env /root/env.pre-pprof.bak`；
   - 在 `.env` 的三行 `AEGIS_{PUBLIC,ADMIN,NODE}_PPROF_ADDR=` 填 `127.0.0.1:6060/6061/6062`；
   - 执行 `systemctl restart aegis-public aegis-admin aegis-node`，再执行 `/root/lt/pgstat.sh enable --yes`（会重启 PG）。
   - 同一轮多个场景之间不必关了再开，最后一个场景跑完再关。
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
- 采样只用轻量方式：开头与结尾各读一次 `/proc/stat` 与各进程 `/proc/<pid>/stat`（含 cutime/cstime）做差，加 `vmstat 5`；**不跑 sample-procs 与 sample-pgact**——它们自身约占 20 个单核百分点（`overhead.sh` 实测），会把静默 CPU 抬过线。
- 成绩单单列一节：面板 + 数据库（三网关、postgres、valkey、nginx）合计 CPU、整机已用内存、swap，对照标准判过或不过。

### 加一档节点（同样 5k 用户）

seed 不能往已有资源池追加节点：每次都会新建资源池和套餐。而且 `-retire-previous` 遇到已接入并跑过的节点会失败，见「坑」。做法是：

- 执行 `seed -users 5000 -nodes <N> -label 5k-n<N> -retire-previous=false`，在新资源池里放 N 个节点和一批新的 5000 用户，每个节点的名单仍然恰好 5000 人；
- 旧批的订阅如果还 active，会多出一批「在册但没人拉」的数据，要在成绩单里写明口径差别。

最干净的做法是按 runbook 7.1 重装数据基座。但那会删库，要先得到用户同意。

## 成绩单（summary.md）

顺序固定，每一节都要有：

1. **时间轴**（UTC）：采集起、nodes 起（相对 T 多少）、T、预热结果、pprof、burst、导出、users 与 nodes 结束及退出码。偏离 runbook 的地方写在这里。
2. **要点**：3 到 5 条，先给结论。
3. **a. runbook 9.2 及格线**（来自 auto-summary）：
   - 节点 p99 < 300ms，逐端点看稳态值；
   - CPU 平均 < 50%，15k 档才判，其他档只作参考；
   - 内存峰值 ≤ 75%；
   - 稳态内不持续换页；
   - 零 5xx：压测工具和 nginx 都要算。
   - 另附网关 cgroup（nr_throttled、mem_max_events、oom_kill）与各进程 CPU 和 PSS。
4. **b. 分档用户目标**（用户 2026-10-07 定，来自 auto-targets，`targets.py` 的 `classify()` 按端点名分档）：

   | 档 | 端点 | 目标 |
   |---|---|---|
   | 门户 | `public:` 除登录外，含订阅拉取 | p50 < 5ms、p99 < 50ms |
   | 节点 | `node:` 除 stream 长连接 | p99 < 20ms |
   | 后台单条 | `admin:GET /v1/me`、`GET /v1/<资源>/{id}`、单条写 | p50 < 5ms、p99 < 50ms |
   | 后台列表、搜索、看板 | 其余 `admin:` | p50 < 50ms、p99 < 200ms |
   | 登录 | `POST /v1/auth/login`（门户与后台） | 不设速度目标；只看不超时、不排队（无 503） |
   | 全部 | — | 零 5xx、不超时、不换页、连接池不排队 |

   - users 和 nodes 用稳态窗口 [T, T+30m) 的统计；预热、burst、节点的 config/report 与 stream 只有全程统计。
   - 连接池拿不到 pgxpool.Stat()，面板没有导出。只能用两个旁证：T+15m 的 goroutine profile 里停在 `pgxpool.*Acquire|puddle` 的个数，以及 pgact.csv 里各网关已建连接是否顶到池上限（public 16、admin 15、node 15）。
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
   - 全部进程按名汇总，docker-proxy 和 Docker 守护进程也要算上；
   - 已回收子进程的 CPU（cutime）；
   - vmstat 的 st 与上下文切换。
   - 写明观测工具自身占了多少（见「坑」）。
8. **没达标的项：根因猜测 + 证据**：每条要指到具体数字或文件，例如 pprof 热点、pg_stat_statements 某行、pgact 某个时刻、timeline 某一格。
9. **原始数据清单**。

## scripts/

| 脚本 | 在哪跑 | 做什么 |
|---|---|---|
| `env.example.sh` | — | 现场参数模板，只有占位符；复制到 `ops-local/<轮次>/env.sh` 再填 |
| `push-scripts.sh` | 本机 | 推采集脚本（仓库 `panel/tools/loadtest/scripts/*` + 本目录）到面板机 `/root/lt/`，推 run-load/run-split 与 0600 地址、口令文件到压测机 |
| `start.sh` | 本机 | 共用一个 T 起两端：面板机 run-collect，90 秒后压测机 run-load |
| `peek.sh` | 本机 | 一条命令看两端实时进度（只读） |
| `pull.sh` | 本机 | 两端结果原样打包拉回，并检查有没有混进后台前缀 |
| `report.sh` | 本机 | 生成 auto-summary / auto-targets / auto-cpu / compare |
| `run-collect.sh` | 面板机 | 采集时间轴（采样器、pgstat reset 与导出、pprof、内存快照、日志） |
| `run-load.sh` | 压测机 | nodes → users → burst 时间轴 |
| `run-split.sh` | 压测机 | 诊断用：只跑节点侧、只跑用户侧，或只跑前 50 个节点 |
| `sample-vmswap.sh` | 面板机 | 各进程 VmSwap / VmRSS |
| `sample-pgact.sh` | 面板机 | pg_stat_activity（aegis_app）与各网关到 :5433 的已建连接数（连接池旁证） |
| `sample-cpustat.sh` | 面板机 | /proc/stat、/proc/softirqs、全部进程按名的 CPU 与已回收子进程 CPU |
| `trim-access.sh` | 面板机 | nginx access log 裁到 [T−6m, T+W+10m] |
| `overhead.sh` | 面板机 | 观测工具自身开销对照 |
| `summarize.py` | 本机 | runbook 9.2 及格线、cgroup、各进程、端点全表、pgact、pg_stat_statements 前 10 |
| `targets.py` | 本机 | `targets` 分档目标表与 pprof 前 10；`compare` 两轮对比；`scale` CPU 随节点数外推 |
| `cpu.py` | 本机 | 整机 CPU 拆分 |

仓库里的 `sample-procs.sh`、`sample-cgroup.sh`、`pgstat.sh`、`snapshot-mem.sh`、`grab-pprof.sh`、`nginx-realip.sh` 不复制进来，由 push-scripts.sh 直接从 `panel/tools/loadtest/scripts/` 推上去。

## 坑

- **ssh 起后台脚本会挂住会话**：远端写成 `cd /root/lt && setsid nohup ./x > log 2>&1 &` 时，`&` 作用于整个 `&&` 列表，bash 会 fork 一个子 shell，它的 stdout/stderr 仍是 ssh 的管道并一直等 x 结束，于是 ssh 不返回，同一条本机命令里的下一条 ssh 发不出去。2026-10-07 踩了三次，两次让压测机晚起 1.5 分钟。一律写成 `ssh -n host 'cd /root/lt; setsid -f ./x > log 2>&1 < /dev/null'`（start.sh 已这样做），两台机器分两条命令发。真挂住时，先停本机那条命令（远端的 x 已在自己的会话里，不受影响），再单独补发第二台，否则它会在第一条返回时晚发。
- **`-retire-previous`（缺省开）遇到已接入并跑过的节点会 409**：`POST /v1/nodes/status:batch` 回「节点仍有控制面、身份或任务依赖，不能退役」。而且这一步**不是原子的**：旧批订阅已在前一个事务里置为 expired，节点停在 draining，seed 以退出码 1 退出。加档时用 `-retire-previous=false`，见上文。
- **seed 在面板机上跑会撞 nginx 节点接口的按 IP 限流**：seed 的来源是面板机自己，不在 realip 信任表里。接入 198 个节点那两分钟 nginx 记了 501 次 503，seed 重试后通过。数 5xx 时要把这段排除在窗口外，不要误判成压测的 5xx。
- **pg_stat_statements 开关要重启 PG**：网关会断几秒。所以同一轮的多个场景之间不关；`shared_preload_libraries` 原来有值时脚本会拒绝执行。
- **采集脚本自身有开销**：
  - sample-procs.sh 每 5 秒对每个 PID 各起几次 cat 和 awk，postgres 就有 26 到 34 个 PID；它还读 smaps_rollup，这部分记在内核时间上。
  - sample-pgact.sh 每 10 秒一次 `docker exec psql`，dockerd、containerd、runc 和 shim 都要跑一遍。
  - 这些短命进程退出后只记在父进程的 cutime 上，所以整机 CPU 会明显高于各进程之和。
  - 2026-10-07 用 `overhead.sh` 在空载面板机上实测（单核 = 100）：全部采样器 +19.7，其中 sample-procs +12.7（大半是 system）、sample-pgact +3.5、vmswap +0.6、cpustat +0.3。也就是整机两核的 10%。
  - 成绩单里的「整机 CPU」要注明含观测开销约 20；外推容量时给「含」与「扣除」两个数。换机型或改采样器后，重跑一次 overhead.sh。
- **procs.csv 原来只统计 6 个进程**（三网关、postgres、valkey、nginx），看不到 docker-proxy、dockerd、containerd 和采样器。整机与各进程之和的差额要用 sample-cpustat.sh 的 allprocs.csv 补齐。
- **docker-proxy 不可忽略**：网关到 `127.0.0.1:5433` 走 Docker 的用户态转发，每个库往返多一次拷贝和两次切换。5k、198 节点时它占单核 6.9%，比 nginx 还高。
- **burst 和整点会形成 p99 尾巴**：
  - T+20m 的 burst 让全部节点经事件流同时重拉名单，UniProxy/user 单格最大到 0.7 秒；
  - 每小时 00 分，所有节点的 push 同时写新一小时的流量行，那一格 active 到 7、等 LWLock 5 条，node 网关的池被开满。
  - 稳态 p99 包含这两格，JSON 只有 10 秒一格的 max，没有逐请求数据，事后剔除不了。解读 p99 时先看 nodes.json 的 timeline，找最大的几格落在哪里。
- **还有一个原因不明的尾巴**：r3 在 T+8m（08:43:00）那一格 heartbeat 到 513ms，像是周期性后台任务。下一轮应在 pgact 里对上时间，或者临时把 pgact 间隔缩到 2 秒。
- **稳态统计的 codes 可能不含 transport 错误**：targets.py 的超时一律取全程计数兜底，再看 nodes.json 的 timeline 判断它落在不在稳态窗口里。
- **nodes 收尾时会记 transport:timeout**：到时关停那一格（稳态外）的 alive 和 push 会各记 1 次。稳态窗口内为 0 才算不超时。
- **300 个节点时来源 IP 会两两共用**：seed 给节点的 real_ip 在 203.0.113.1 到 .254 里循环，第 255 到 300 个节点与第 1 到 46 个共用地址，每个地址每分钟约 24 次，离 nginx 的 240 次/分还很远。到 2500 个以上节点才要担心限流。
- **install.sh 的 CHANGE_ME 误报**：首装会提示「还有 1 项需要手工填写」，命中的其实是 .env 里一行注释，AEGIS_PUBLIC_BASE_URL 已经填好，可以忽略。这是第 2 波修安装链的输入。
- **安装链不申请证书、不放行 ufw、不渲染 nginx**：A 段第 4、5、7 步手工做。Debian 默认站点要在拿到证书后、渲染前删掉，否则会和 aegis.conf 抢 80 端口的 default_server。
- **Debian 最小镜像没有 `/usr/bin/time`**：包装脚本里用 bash 的 `time`。
- **`pgrep -fa` 会把带口令的命令行打出来**：runbook 7.2 的 `-database-url "$AEGIS_DATABASE_URL"` 让口令出现在 argv 里。排查进程时用 `pgrep -a <名字>` 并 `cut` 掉参数，或者改用 loadtest 的环境变量回落，不传这个参数。
- **`pkill -f '<模式>'` 会连带杀掉执行它的那条 ssh 的 bash**：远端命令行本身也含这个模式。停采样器用 PID，或用 `pkill -f '^vmstat'` 这类锚定的写法。
- **网络与 TLS 不是延迟大头**：同机房 ping 0.47ms，keep-alive 下 /healthz 的 p50 是 0.5ms。新建 TLS 连接要 35ms，但 nginx 的 keepalive 缺省 75 秒，nodesim 空闲 90 秒，都长于节点节拍，连接会被复用。nginx access log 里有 request_time 和 upstream_time，可以用来核对时间花在了网关内部。

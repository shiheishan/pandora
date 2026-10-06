# 面板压测 Runbook

在 Vultr 上实测面板在真实规模附近的资源占用。节点固定 200 个（2 台真 pdnd + 198 个模拟节点），用户分 5,000 / 10,000 / 15,000 三档。

这份手册只讲怎么测、测什么、存在哪。测出来的问题先记下来，等基线出来再决定怎么调，不在压测中途改面板。

> 文中所有地址、域名、口令一律是占位符：`<PANEL_DOMAIN>`、`<PANEL_IP>`、`<LOADGEN_IP>`、`<NODE1_IP>`、`<NODE2_IP>`、`<ADMIN_PATH>`、`<ADMIN_EMAIL>`。真实值只在机器上和你自己的笔记里，不进仓库。

---

## 0. 角色与产物

| 机器 | 规格 | 做什么 | 结果目录 |
|---|---|---|---|
| 面板机 `<PANEL_IP>` | Vultr 共享型 2c4g | install.sh 生产模式装面板，nginx 在前；跑采集脚本 | `/root/lt-results/<场景>/` |
| 压测机 `<LOADGEN_IP>` | 同机房 1c | 跑 `loadtest nodes / users / burst` | `~/lt-results/<场景>/` |
| 节点机 ×2 `<NODE1_IP>` `<NODE2_IP>` | 同机房 1c | 真 pdnd（pandora-native） | 节点日志 |

`<场景>` 命名：`idle`、`5k-r1`、`5k-r2`、`10k-r1`、`10k-r2`、`15k-r1`、`15k-r2`、`15k-24h`。

压测结束后，把两台机器上的 `lt-results` 整个拉回本机，存到仓库目录之外。产物里有节点私钥（`lt-manifest.json`）和测试库的 SQL 文本，**不进仓库**。

工具一览（都在 `panel/tools/loadtest/`）：

- `go run ./tools/loadtest seed|nodes|users|burst`，每个子命令加 `-h` 看参数。
- `scripts/`：在面板机上以 root 运行的采集脚本。
  - `pgstat.sh`
  - `sample-procs.sh`
  - `snapshot-mem.sh`
  - `grab-pprof.sh`
  - `nginx-realip.sh`

---

## 1. 开机

1. 在同一个机房开四台机器：面板机 2c4g，压测机、两台节点机各 1c。系统用 Debian 13 或 Ubuntu 24.04。
2. 把 `<PANEL_DOMAIN>` 的 A 记录指向 `<PANEL_IP>`。生产模式要求 `AEGIS_PUBLIC_BASE_URL` 是 https 公网域名，证书由 install 链申请。
3. 记下每台机器的规格和开机时间，写进 `lt-results/env.txt`：
   - `lscpu`
   - `free -m`
   - `uname -a`
   - Vultr 套餐名

## 2. 按生产方式装面板（面板机）

1. 取发布包：用仓库 `panel/deploy/build-release.sh` 构建，或下载对应版本的发布包。解压到 root 可写、其他人不可写的目录。
2. 安装：

   ```bash
   sudo PANDORA_PUBLIC_BASE_URL=https://<PANEL_DOMAIN> ./install.sh
   ```

   - 首装会生成 `/opt/aegispanel/deploy/.env`：里面是密钥和随机生成的后台前缀 `AEGIS_ADMIN_PATH`，0600 权限。
   - PG18 和 Valkey 跑在 Docker 数据基座里，只绑 `127.0.0.1:5433` 和 `127.0.0.1:6380`。
3. 渲染 nginx：

   ```bash
   sudo /opt/aegispanel/deploy/render-nginx.sh
   ```

   - 模板在 server 块里 include `/etc/aegispanel/cloudflare-realip.conf`，但两个安装脚本都不生成这个文件，生成它的 `update-cloudflare-realip.sh` 也不在发布包里（见报告「遗留问题」）。
   - 文件缺失时 `nginx -t` 会失败。所以压测机上先补一个空文件：

     ```bash
     sudo install -d /etc/aegispanel && sudo touch /etc/aegispanel/cloudflare-realip.conf
     ```

   - 压测期间这个文件会被 `nginx-realip.sh` 顶替（见第 4 步）。
4. 建后台管理员（以 root，照 install.sh 结尾的提示）：

   ```bash
   cd /opt/aegispanel && set -a && . deploy/.env && set +a && ./bin/aegis-adminctl create --email <ADMIN_EMAIL> --password-stdin --role platform_admin
   ```

   - 口令从标准输入给，不要写进命令行。
   - 管理员账号和口令只记在你自己的密码库里。
5. 记下随包的资源上限。它们就是生产形态，压测按它测，不要改：
   - systemd：`aegis-public` 和 `aegis-node` 是 `CPUQuota=60%`、`MemoryMax=256M`；`aegis-admin` 是 `80%`、`384M`。
   - Docker 数据基座：PG `max_connections=60`、`shared_buffers=128MB`、容器 512M；Valkey `maxmemory 96mb allkeys-lru`。
   - `platform/db` 连接池每个网关最多 8 个连接，写死在代码里。

   如果结果撞了这些顶，报告里要写成「撞上限」，不能写成「机器不够」。

## 3. 打开观测开关（面板机）

1. pprof：在 `.env` 里填三个回环地址。地址只能是回环 IP 加端口，`localhost` 也会被拒，非回环会让网关拒绝启动。

   ```bash
   AEGIS_PUBLIC_PPROF_ADDR=127.0.0.1:6060
   AEGIS_ADMIN_PPROF_ADDR=127.0.0.1:6061
   AEGIS_NODE_PPROF_ADDR=127.0.0.1:6062
   ```

   填好后重启三个网关：

   ```bash
   sudo systemctl restart aegis-public aegis-admin aegis-node
   ```

   日志里会出现一行 warn「pprof 诊断端口已开启」。
2. pg_stat_statements：

   ```bash
   sudo ./pgstat.sh enable --yes
   ```

   - 会重启 PG，网关几秒连不上库。
   - 如果 `shared_preload_libraries` 原来就有值，脚本会拒绝执行，交给人处理。
3. 把 `scripts/` 整个 scp 到面板机，例如放到 `/root/lt/`。六个文件要放在同一个目录里。

## 4. 真实来源 IP（面板机）

压测机经 nginx 打面板时，所有模拟用户都来自同一个 IP，会出两个问题：

- 风控的 IP 聚类、应用层的每 IP / 每 /24 限流全部压在一个地址上。
- nginx 自己的 `limit_conn 24` 会把整台压测机限成 24 个并发。

所以压测期间只对压测机采信 `X-Real-IP`：

```bash
sudo /root/lt/nginx-realip.sh enable <LOADGEN_IP>
sudo /root/lt/nginx-realip.sh status
```

- 脚本会先备份原文件，`nginx -t` 通过才 reload，失败自动回滚。
- 压测工具在每个请求里带该模拟用户固定的 `X-Real-IP`，地址取自 198.18.0.0/15，用户轮流分到 512 个 /24 里。
- 随包模板 `deploy/nginx-aegis.conf` 不改。

## 5. 装两台真 pdnd 并接入（节点机）

1. 按 `pdnd/release/README.md` 装 pandora-native。
2. 在后台节点页新建服务器和节点，签发接入令牌，在节点机上执行两段式接入。
3. 每一档 seed 之后，在后台节点抽屉里把这两个节点的资源池改成那一档 manifest 里的 `pool_id`，让它们和模拟节点下发同一份用户表。
4. 核对：pdnd 日志里出现「用户已同步 总数=<该档用户数>」。
5. 两台真节点是对照组：用来核对模拟节点的请求节奏。办法是在面板机的 nginx access log 里，比较真节点 IP 与模拟节点的同类请求次数。

## 6. 构建压测工具（压测机）

```bash
git clone <仓库地址> pandora && cd pandora && git checkout <本次提交>
cd panel && CGO_ENABLED=0 go build -o ~/loadtest ./tools/loadtest
scp ~/loadtest root@<PANEL_IP>:/root/lt/loadtest
```

- Go 版本与 `panel/go.mod` 一致。
- seed 要在面板机上跑：库只绑回环，而且这样 `.env` 里的密钥不用离开面板机。

## 7. 每档的造数（面板机）

```bash
cd /root/lt
set -a; . /opt/aegispanel/deploy/.env; . /opt/aegispanel/deploy/release-artifact.env; set +a
export LOADTEST_ADMIN_PASSWORD='<管理员口令>'   # 只在这个 shell 里
./loadtest seed -database-url "$AEGIS_DATABASE_URL" \
  -admin-base "https://<PANEL_DOMAIN>/$AEGIS_ADMIN_PATH" -node-base https://<PANEL_DOMAIN> \
  -public-base https://<PANEL_DOMAIN> -admin-email <ADMIN_EMAIL> \
  -users 5000 -nodes 198 -label 5k -out /root/lt-results/5k-seed/lt-manifest.json \
  | tee /root/lt-results/5k-seed/seed.log
```

- **连的是运行角色**：`AEGIS_DATABASE_URL` 是 aegis_app，所以 RLS 和列级授权都真的在起作用。
- **`.env` 的两个作用**：
  - `AEGIS_MASTER_KEY` 用来像面板那样加密存一份订阅令牌；
  - `release-artifact.env` 提供节点接入时要对上的版本与摘要（`PANDORA_NATIVE_*`）。
- **可重复执行**：每次 seed 先把上一批 `loadtest-` 节点经后台退役，把 `@loadtest.invalid` 用户的订阅转成 expired，再用新命名空间造数。
  - 追加写表删不掉，所以旧行会留在库里，表会逐档变大。
  - 想每档都从干净库起，就重装数据基座，这一点待定，见报告。
- **耗时**：后台每 IP 每分钟限 240 次，所以 198 个节点的接入大约要 3.5 分钟；15k 用户的 SQL 批量插入在秒级到十几秒。
  - 各阶段耗时打在 stdout 上，也写进 manifest 的 `seed_timings`。
  - 「每档造数耗时」直接取这里。
- seed 最后会自检：首尾两个节点的签名配置、UniProxy 配置，用户列表恰好是本批全部用户，再用第一个用户拉一次订阅。自检失败就退出码非 0，不要往下跑。
- **把 manifest 拷到压测机**，权限 0600：

  ```bash
  scp /root/lt-results/5k-seed/lt-manifest.json <LOADGEN_IP>:~/lt-results/5k-seed/
  ```

## 8. 场景顺序与每一档的流程

顺序：**空载 → 5k → 10k → 15k**。

- 每档 30 分钟稳态，期间做一次 burst。
- 每档跑两次（`r1`、`r2`）：共享型 CPU 有邻居干扰，两次结果差得远就再跑一次。
- 最后 15k 档跑 24 小时混合。

### 8.1 空载（`idle`）

只有面板、两台真 pdnd 和采集，不跑 seed，也不跑压测工具，采 30 分钟。

### 8.2 一次 30 分钟的档（例：`5k-r1`）

时间轴，T 为开始时刻：

| 时刻 | 面板机（root，`/root/lt`） | 压测机 |
|---|---|---|
| T−5m | `./sample-procs.sh /root/lt-results/5k-r1/procs.csv 5 &`（全程后台跑） | 启动 nodes（见下），让 198 个节点在 60 秒内错开起跑并进入稳态 |
| T−1m | `./pgstat.sh reset`；`./snapshot-mem.sh /root/lt-results/5k-r1 before` | |
| T | | 启动 users（见下），时长 30 分钟 |
| T+15m | `./grab-pprof.sh /root/lt-results/5k-r1 30` | |
| T+20m | | `burst` 一次 |
| T+30m | `./pgstat.sh export /root/lt-results/5k-r1 50`；`./snapshot-mem.sh /root/lt-results/5k-r1 after`；停 sample-procs | users 自动结束；nodes 再跑 1 分钟后结束 |
| T+31m | 存 nginx 日志：`cp /var/log/nginx/*access*.log /root/lt-results/5k-r1/`；存网关日志：`cp /var/log/aegis/*.log /root/lt-results/5k-r1/` | |
| T+31m | 记 cgroup 节流：`cat /sys/fs/cgroup/system.slice/aegis-{public,admin,node}.service/cpu.stat > /root/lt-results/5k-r1/cgroup-cpu.txt`（压测前也记一份做差） | |

压测机上的命令。`R` 是本次结果目录，`M` 是本档的 manifest：

```bash
R=~/lt-results/5k-r1; M=~/lt-results/5k-seed/lt-manifest.json; mkdir -p $R
export LOADTEST_ADMIN_EMAIL=<ADMIN_EMAIL> LOADTEST_ADMIN_PASSWORD='<管理员口令>'

# 198 个模拟节点：签名通道 + UniProxy + 每节点一条 SSE；-strict 让签名失败与 5xx 反映在退出码上
~/loadtest nodes -manifest $M -node-url https://<PANEL_DOMAIN> -stagger 60s -duration 37m \
  -out $R -strict > $R/nodes.log 2>&1 &

# 用户混合流量（T 时启动）
~/loadtest users -manifest $M -public-url https://<PANEL_DOMAIN> \
  -admin-url https://<PANEL_DOMAIN>/<ADMIN_PATH> \
  -duration 30m -sub-rate <见下表> -portal-rate 5 -admin-rate 0.5 -login-rate 0.05 \
  -portal-users 200 -out $R -strict > $R/users.log 2>&1 &

# T+20m：改一个用户（设备数覆盖 + 换用户组），触发 200 个节点经事件流重拉
~/loadtest burst -manifest $M -admin-url https://<PANEL_DOMAIN>/<ADMIN_PATH> -count 1 -out $R
```

速率建议（总协调拟，可改）。订阅拉取按「每人每 30 分钟一次」估，远比真实客户端（常见 1–24 小时更新一次）激进：

| 档 | `-sub-rate` | 每凭据每小时 | 订阅凭据上限 |
|---|---|---|---|
| 5k | 2.8 | 2 | 60 |
| 10k | 5.6 | 2 | 60 |
| 15k | 8.3 | 2 | 60 |

- users 开跑前会按速率逐维预估面板各个限流（每 IP、每 /24、每账号、订阅每小时、登录、后台每 IP），每维打一行 ok 或 WARN。出现 WARN 先调速率，再开跑。
- 后台每 IP 每分钟限 240 次；要压更高，给 `-admin-ips` 多个虚构地址。
- 节点侧稳态的请求量（不用配，由面板下发的节拍决定）：
  - 每个节点每 15 秒 4 个签名请求加 1 次用户拉取，每 60 秒 push 和 alive 各一次，每 30 秒一次签名心跳。
  - 200 个节点合计约 80 req/s。

### 8.3 15k 档 24 小时混合（`15k-24h`）

- 和 8.2 相同，只是 nodes 和 users 的 `-duration` 改成 `24h`（nodes 用 `24h10m`）。
- burst 每 2 小时一次：

  ```bash
  ~/loadtest burst ... -count 12 -interval 2h
  ```

  次数为偶数，用户会回到原状。
- 面板机：
  - `sample-procs.sh` 全程跑；
  - `snapshot-mem.sh` 每小时打一次，标签用 `h01`…`h24`；
  - `pgstat.sh export` 在第 1、12、24 小时各导一次；
  - `grab-pprof.sh` 在第 1、12、24 小时各抓一次，比较 heap 有没有持续增长。
- 默认 `-traffic-mib 8` 跑 24 小时，每个在线用户约 12GB，可能把 1TB 额度之外的套餐用完。seed 默认每人 1024GB，不会触顶；如果改了 `-traffic-gb`，要相应调小 `-traffic-mib`。

## 9. 结果怎么读

每次运行的产物：

- 压测机：
  - `nodes.json/.txt`、`users.json/.txt`、`users-warmup.json/.txt`、`burst.json`、`burst-http.json/.txt`、各自的 `.log`
- 面板机：
  - `procs.csv`
  - `pgstat-*-{total,mean,calls}.csv`
  - `pg-memory-*.txt`、`valkey-*.txt`
  - `*-{cpu,heap,allocs,goroutine}-*.pprof`
  - nginx 与网关日志、`cgroup-cpu.txt`

JSON 里每个端点都有 count、QPS、p50/p95/p99/max（毫秒）、错误码分布、自定义标签，以及 10 秒一格的时间线。

- 节点侧的标签：`sig_fail`、`auth_fail`、`etag_304`、`trigger:stream`、`phase:*`。
- burst 的尖峰：在 nodes.json 的时间线里看，按 burst.json 的 `trigger_unix_ms` 对齐。

及格线（总协调拟）逐条对照：

| 及格线 | 数据来源 | 怎么判 |
|---|---|---|
| 节点接口 p99 < 300ms | `nodes.json` 里每个 `node:` 端点的 `p99_ms` | 每个端点都要过，不只看合计 |
| 15k 档 CPU 平均 < 50% | `procs.csv` 的 `_system` 行：`cpu_pct` 平均 ÷（核数 × 100） | 只算稳态的 30 分钟；同时看各网关的 cgroup `nr_throttled` 是否在涨（撞上 CPUQuota） |
| 整机内存留 25% 余量，24 小时内不持续上涨 | `procs.csv` 的 `_system` 行：`rss_kb` = MemTotal − MemAvailable；24h 档的各进程 `pss_kb` 趋势；pprof heap 对比 | 峰值 ≤ 75% MemTotal；24 小时的线性趋势不显著为正 |
| 零 5xx | nodes / users / burst 三份 JSON 的 `server_5xx`；nginx access log 里 status ≥ 500 的行数 | 都必须是 0；`transport:*`（连接错误、超时）单列说明 |

SQL 热点看 `pgstat-*-total.csv` 前十。结合 pprof 的 CPU 火焰图，定位到具体的接口和查询。

## 10. 收尾与删机

1. 面板机：
   - `./nginx-realip.sh disable`
   - `./pgstat.sh disable --yes`
   - 清空 `.env` 里三个 `*_PPROF_ADDR`，然后 `systemctl restart aegis-public aegis-admin aegis-node`
   - 删机前这些都可以跳过；保留机器的话必须做。
2. 把两台机器的 `lt-results` 拉回本机，打包后存到仓库目录之外。
3. 在 Vultr 控制台删除四台机器，删 `<PANEL_DOMAIN>` 的 DNS 记录。
4. 测试用的管理员口令作废。

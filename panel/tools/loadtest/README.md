# 面板压测 Runbook

在 Vultr 上实测面板在真实规模附近的资源占用。节点固定 200 个（2 台真 pdnd + 198 个模拟节点），用户分 5,000 / 10,000 / 15,000 三档。

> 2026-10-08 起整机标准升到 **1 万用户、1000 节点**，造数、模拟节点和静默测量的做法见第 11 节；下面各节仍以 200 节点档为例，参数照第 11 节替换即可。

这份手册只讲怎么测、测什么、存在哪。测出来的问题先记下来，等基线出来再决定怎么调，不在压测中途改面板。

> 文中所有地址、域名、口令一律是占位符：`<PANEL_DOMAIN>`、`<PANEL_IP>`、`<LOADGEN_IP>`、`<NODE1_IP>`、`<NODE2_IP>`、`<ADMIN_PATH>`、`<ADMIN_EMAIL>`、`<RELEASE_DIR>`。真实值只在机器上和你自己的笔记里，不进仓库。

## 定案（总协调已定，照做）

| 事项 | 做法 |
|---|---|
| 库 | **每档干净库**：同一档的 r1 / r2 之间不重装，档与档之间重装数据基座（第 7 节） |
| 资源上限 | **按生产上限测**：CPUQuota、MemoryMax、连接池上限（缺省值）都不改。只有某档撞上限时，才在该档补一轮放开上限的对照（第 9.4 节），结果写成「撞上限」，不写成「机器不够」 |
| 真实来源 IP | 压测机**直连源站**，不经 Cloudflare；压测期间用 `nginx-realip.sh` 顶替信任表（第 4 节） |
| 及格线 | 照原四条；另把「15k 档稳态时三个网关的 `nr_throttled` 增量」列为**必报观测项**（不是及格线） |
| 速率 | 稳态各档订阅按每人每 30 分钟一次（偏激进，测余量）；24 小时那轮可选「贴近真实」：每人每 6 小时一次 |

---

## 0. 角色与产物

| 机器 | 规格 | 做什么 | 结果目录 |
|---|---|---|---|
| 面板机 `<PANEL_IP>` | Vultr 共享型 2c4g | install.sh 生产模式装面板，nginx 在前；跑 seed 与采集脚本 | `/root/lt-results/<场景>/` |
| 压测机 `<LOADGEN_IP>` | 同机房 1c | 跑 `loadtest nodes / users / burst` | `~/lt-results/<场景>/` |
| 节点机 ×2 `<NODE1_IP>` `<NODE2_IP>` | 同机房 1c | 真 pdnd（pandora-native），对照组 | 节点日志 |

`<场景>` 命名：`idle`、`5k-r1`、`5k-r2`、`10k-r1`、`10k-r2`、`15k-r1`、`15k-r2`、`15k-24h`；放开上限的对照轮加后缀 `-uncapped`。

压测结束后，把两台机器上的 `lt-results` 整个拉回本机，存到仓库目录之外。产物里有节点私钥（`lt-manifest.json`）和测试库的 SQL 文本，**不进仓库**。

工具（都在 `panel/tools/loadtest/`）：

- `loadtest seed|nodes|users|burst`，每个子命令加 `-h` 看参数。
- `scripts/`：在面板机上跑的采集脚本。
  - `pgstat.sh`：pg_stat_statements 开启、清零、导出、撤销
  - `sample-procs.sh`：各进程与整机的 CPU、RSS/PSS，整机另记 swap 总量、余量与累计换入换出页数
  - `sample-cgroup.sh`：三个网关的 cgroup 节流与内存水位（`nr_throttled`、`memory.events`）
  - `snapshot-mem.sh`：PG 与 Valkey 内部的内存快照
  - `grab-pprof.sh`：三个网关的 CPU、heap、allocs、goroutine profile
  - `nginx-realip.sh`：压测期间的真实来源 IP 开关

---

## 1. 开机

1. 在同一个机房开四台机器：面板机 2c4g，压测机、两台节点机各 1c。系统用 Debian 13 或 Ubuntu 24.04。
2. 把 `<PANEL_DOMAIN>` 的 A 记录指向 `<PANEL_IP>`，**只做 DNS 解析，不开 Cloudflare 代理**（压测机要直连源站）。生产模式要求 `AEGIS_PUBLIC_BASE_URL` 是 https 公网域名，证书由 install 链申请。
3. 记下每台机器的规格和开机时间，写进 `lt-results/env.txt`：
   - `lscpu`
   - `free -m`
   - `uname -a`
   - Vultr 套餐名

## 2. 按生产方式装面板（面板机）

1. 取发布包：用仓库 `panel/deploy/build-release.sh` 构建，或下载对应版本的发布包。解压到 root 可写、其他人不可写的目录，下文记作 `<RELEASE_DIR>`。**保留这个目录**：档与档之间重装数据基座要再跑一次它的 install.sh。
2. 安装：

   ```bash
   cd <RELEASE_DIR> && sudo PANDORA_CERTBOT=1 PANDORA_PUBLIC_BASE_URL=https://<PANEL_DOMAIN> ./install.sh
   ```

   - 首装会生成 `/opt/aegispanel/deploy/.env`：里面是密钥和随机生成的后台前缀 `AEGIS_ADMIN_PATH`，0600 权限。
   - PG18 和 Valkey 跑在 Docker 数据基座里，只绑 `127.0.0.1:5433` 和 `127.0.0.1:6380`；网关经 `/opt/aegispanel/deploy/run/` 下的 unix socket 连它们（install.sh 确认 socket 可用后改写 `.env`），不走 docker-proxy。
   - install.sh 最后一步配 nginx：ufw 开着就放行 80/443；没有证书且给了 `PANDORA_CERTBOT=1` 就用 certbot webroot 申请（等于同意 Let's Encrypt 订户协议）；停用 Debian 自带的默认站点；渲染、`nginx -t`、reload。
3. 只在 install.sh 跳过了 nginx（没装 nginx、没给证书）时，手工渲染并加载：

   ```bash
   sudo /opt/aegispanel/deploy/render-nginx.sh && sudo nginx -t && sudo systemctl reload nginx
   ```

   - 渲染器会在 `/etc/aegispanel/cloudflare-realip.conf` 不存在时写一份**不信任任何代理**的默认文件，全新安装的 `nginx -t` 直接能过。
   - 压测站点不在 Cloudflare 后面，**不要**跑 `update-cloudflare-realip.sh`。
4. 建后台管理员（以 root，照 install.sh 结尾的提示）：

   ```bash
   cd /opt/aegispanel && set -a && . deploy/.env && set +a && ./bin/aegis-adminctl create --email <ADMIN_EMAIL> --password-stdin --role platform_admin
   ```

   - 口令从标准输入给，不要写进命令行。
   - 管理员账号和口令只记在你自己的密码库里。
   - 每次重装数据基座后要再建一次（管理员在库里）。
5. 记下随包的资源上限。它们就是生产形态，压测按它测：
   - systemd：`aegis-public` 和 `aegis-node` 是 `CPUQuota=60%`、`MemoryMax=256M`；`aegis-admin` 是 `80%`、`384M`。
   - Docker 数据基座：PG `max_connections=60`、`shared_buffers=128MB`、容器 512M；Valkey `maxmemory 96mb allkeys-lru`。
   - `platform/db` 连接池上限可配置：缺省 public 16、admin 15、node 15，算式和环境变量 `AEGIS_{PUBLIC,ADMIN,NODE}_DB_MAX_CONNS` 见 `panel/deploy/.env.example`，压测按缺省测。

## 3. 打开观测开关（面板机）

1. 把 `scripts/` 整个 scp 到面板机，例如放到 `/root/lt/`，所有脚本放在同一个目录里。
2. pprof：在 `.env` 里填三个回环地址。地址只能是回环 IP 加端口，`localhost` 也会被拒，非回环会让网关拒绝启动。

   ```bash
   AEGIS_PUBLIC_PPROF_ADDR=127.0.0.1:6060
   AEGIS_ADMIN_PPROF_ADDR=127.0.0.1:6061
   AEGIS_NODE_PPROF_ADDR=127.0.0.1:6062
   ```

   填好后重启三个网关：

   ```bash
   sudo systemctl restart aegis-public aegis-admin aegis-node
   ```

   - 日志里会出现一行 warn「pprof 诊断端口已开启」。
   - 这三行写在 `.env` 里，重装数据基座不受影响。
3. pg_stat_statements：

   ```bash
   sudo /root/lt/pgstat.sh enable --yes
   ```

   - 会重启 PG，网关几秒连不上库。
   - 如果 `shared_preload_libraries` 原来就有值，脚本会拒绝执行，交给人处理。
   - 设置写在 PG 数据卷里，**每次重装数据基座后要再开一次**。

## 4. 真实来源 IP（面板机）

压测机经 nginx 打面板时，所有模拟用户都来自同一个 IP，会出两个问题：

- 风控的 IP 聚类、应用层的每 IP / 每 /24 限流全部压在一个地址上。
- nginx 自己的 `limit_conn 24` 会把整台压测机限成 24 个并发。

所以压测期间只对压测机采信 `X-Real-IP`：

```bash
sudo /root/lt/nginx-realip.sh enable <LOADGEN_IP>
sudo /root/lt/nginx-realip.sh status
```

- 它整份顶替第 2 步生成的信任表，原文件备份成 `.loadtest-orig`。
- `nginx -t` 通过才 reload，失败自动回滚。
- 压测工具在每个请求里带该模拟用户固定的 `X-Real-IP`，地址取自 198.18.0.0/15，用户轮流分到 512 个 /24 里。
- 模拟节点同样带 `X-Real-IP`：取自 seed 给它所在服务器登记的虚构公网地址（203.0.113.0/24，写在 manifest 的 `real_ip`）。nginx 对 `/v1/nodes/` 与 `/api/v1/server/UniProxy/` 按来源 IP 限 240 次/分（突发 60/120），不带的话 198 个节点共用压测机一个地址，大面积 503。
- 开着它跨档也没关系：重装数据基座不动 nginx。
- 随包模板 `deploy/nginx-aegis.conf` 不改。

## 5. 节点机准备（两台节点机）

1. 每档都要重新接入（真节点的身份在库里，档与档之间重装数据基座后身份就作废了），这里只做一次性准备：确认系统时间同步（签名时间窗口很窄），记下 `<NODE1_IP>`、`<NODE2_IP>`。
2. 真节点的作用是对照组：在面板机的 nginx access log 里，比较真节点 IP 与模拟节点的同类请求次数，核对模拟节点的请求节奏。
   - 模拟节点按 `-node-behavior` 模拟两代 pdnd（见 8.1）。发布包里的 pdnd 拉生效配置时带请求头 `X-Applied-Effective-Release`，就是 current（缺省）；更早的版本对应 legacy。两边不是同一代时，同类请求次数对不上是预期的。

## 6. 构建压测工具（压测机）

```bash
git clone <仓库地址> pandora && cd pandora && git checkout <本次提交>
cd panel && CGO_ENABLED=0 go build -o ~/loadtest ./tools/loadtest
scp ~/loadtest root@<PANEL_IP>:/root/lt/loadtest
```

- Go 版本与 `panel/go.mod` 一致。
- seed 要在面板机上跑：库只绑回环，而且这样 `.env` 里的密钥不用离开面板机。

## 7. 每一档开始前：干净库 → 造数 → 真节点接入

### 7.1 重装数据基座（5k、10k、15k 档开始前各做一次；同档 r1 / r2 之间不做）

```bash
sudo systemctl stop aegis-public aegis-admin aegis-node
cd /opt/aegispanel/deploy && sudo docker compose down -v      # 删掉 PG 与 Valkey 的数据卷
cd <RELEASE_DIR> && sudo ./install.sh                         # .env 已在：走升级路径，先拉起空库做一次（很小的）升级前备份，空库跳过迁移预检
```

- install.sh 会重新拉起数据基座、从空库迁移到最新、收窄 aegis_app、装回二进制并启动三网关、做健康检查。
- `.env`（密钥、后台前缀、pprof 地址）不变；nginx 按模板重新渲染（原配置备份在 `/var/backups/aegispanel/`），手工改过 `aegis.conf` 的话要重做。
- 之后补三件事：
  1. 重建管理员（第 2 节第 4 步）；
  2. 重开 pg_stat_statements（第 3 节第 3 步）；
  3. 记一行 `env.txt`：`<档> 重装于 <时间>`。

### 7.2 造数（面板机）

```bash
cd /root/lt && mkdir -p /root/lt-results/5k-seed
set -a; . /opt/aegispanel/deploy/.env; . /opt/aegispanel/deploy/release-artifact.env; set +a
export LOADTEST_ADMIN_PASSWORD='<管理员口令>'   # 只在这个 shell 里
./loadtest seed \
  -admin-base "https://<PANEL_DOMAIN>/$AEGIS_ADMIN_PATH" -node-base https://<PANEL_DOMAIN> \
  -public-base https://<PANEL_DOMAIN> -admin-email <ADMIN_EMAIL> \
  -users 5000 -nodes 198 -label 5k -out /root/lt-results/5k-seed/lt-manifest.json \
  | tee /root/lt-results/5k-seed/seed.log
```

- **连的是运行角色**：不传 `-database-url`，seed 从环境变量 `AEGIS_DATABASE_URL`（上面 source 的 `.env`）取，那是 aegis_app，所以 RLS 和列级授权都真的在起作用。
  - 不要把连接串写进命令行参数：里面有 aegis_app 的口令，`ps` / `pgrep -fa` 对本机所有用户可见。
- **两份环境文件的作用**：
  - `.env` 里的 `AEGIS_MASTER_KEY` 用来像面板那样加密存一份订阅令牌；
  - `release-artifact.env` 提供节点接入时要对上的版本与摘要（`PANDORA_NATIVE_*`）。
- **耗时**：后台每 IP 每分钟限 240 次，请求按 260ms 间隔排队，所以建服务器、建节点、接入、激活占了绝大部分时间。2026-10-07 实测：198 个节点整段约 5 分钟（总计 303 秒，后台请求 797 次），300 个节点约 7.7 分钟；5k 用户的 SQL 批量写入约 11 秒（`users_total`），15k 档以各阶段打印为准。
  - 单会话时请求按 260ms 间隔串行，1000 个节点要 4000 个后台请求（建服务器、建节点、签令牌、激活各一千次），约 17 分钟，加接入合计约 25 分钟。`-admin-workers N` 把这些请求分给 N 个并行会话，见第 11.1 节。
  - 各阶段耗时打在 stdout 上，也写进 manifest 的 `seed_timings`。
  - 「每档造数耗时」直接取这里。
- **自检**：seed 最后会核对首尾两个节点的签名配置、UniProxy 配置，用户列表恰好是本批全部用户，再用第一个用户拉一次订阅。自检失败就退出码非 0，不要往下跑。
- **把 manifest 拷到压测机**，权限 0600：

  ```bash
  scp /root/lt-results/5k-seed/lt-manifest.json <LOADGEN_IP>:~/lt-results/5k-seed/
  ```

### 7.3 两台真 pdnd 接入本档的资源池

1. 在后台节点页，新建两台服务器、两个节点，**资源池选本档 seed 建的那个**：池名以 `loadtest-` 开头，id 是 manifest 的 `pool_id`。然后在节点抽屉里取安装命令。
2. 在每台节点机上清掉上一档的身份，再执行新的安装命令。安装器发现已有身份会沿用它，而上一档的身份在新库里已经不存在：

   ```bash
   sudo systemctl stop pandora-native || true
   sudo rm -f /etc/pandora-native/identity.json /etc/pandora-native/identity.json.enrollment.pending.json
   # 粘贴后台给的安装命令
   ```

3. 核对：pdnd 日志里出现「用户已同步 总数=<该档用户数>」；后台节点列表里这两个节点在线。

## 8. 每次 30 分钟稳态（例：`5k-r1`）

时间轴，T 为开始时刻。`P=/root/lt-results/5k-r1`（面板机），`R=~/lt-results/5k-r1`（压测机）：

| 时刻 | 面板机（root，`/root/lt`） | 压测机 |
|---|---|---|
| T−5m | `./sample-procs.sh $P/procs.csv 5 &`；`./sample-cgroup.sh $P/cgroup.csv 60 &`（两个都全程后台跑） | 启动 nodes（见下），198 个节点在 60 秒内错开起跑并进入稳态 |
| T−1m | `./pgstat.sh reset`；`./snapshot-mem.sh $P before` | |
| T | | 启动 users（见下），时长 30 分钟 |
| T+15m | `./grab-pprof.sh $P 30` | |
| T+20m | | `burst` 一次 |
| T+30m | `./pgstat.sh export $P 50`；`./snapshot-mem.sh $P after`；停两个采样器（`kill %1 %2`） | users 自动结束；nodes 再跑 1 分钟后结束 |
| T+31m | 存 nginx 日志：`cp /var/log/nginx/*access*.log $P/`；存网关日志：`cp /var/log/aegis/*.log $P/` | |

压测机上的命令（`M` 是本档的 manifest）：

```bash
R=~/lt-results/5k-r1; M=~/lt-results/5k-seed/lt-manifest.json; mkdir -p $R
T=<T 的 unix 秒>   # 两台机器约定的稳态起点；-steady-* 让报告另出 [T, T+30m) 内的分位数
export LOADTEST_ADMIN_EMAIL=<ADMIN_EMAIL> LOADTEST_ADMIN_PASSWORD='<管理员口令>'

# 198 个模拟节点：签名通道 + UniProxy + 每节点一条 SSE；-strict 让签名失败与 5xx 反映在退出码上
# 节拍按新 pdnd（-node-behavior current，缺省）；与旧版 pdnd 时期的数据对照时加 -node-behavior legacy（见 8.1）
~/loadtest nodes -manifest $M -node-url https://<PANEL_DOMAIN> -stagger 60s -duration 37m \
  -steady-start $T -steady-dur 30m -out $R -strict > $R/nodes.log 2>&1 &

# 用户混合流量（T 时启动）。订阅每人每 30 分钟一次：-sub-interval 按 manifest 人数自动折成各档的总速率
~/loadtest users -manifest $M -public-url https://<PANEL_DOMAIN> \
  -admin-url https://<PANEL_DOMAIN>/<ADMIN_PATH> \
  -duration 30m -sub-interval 30m -portal-rate 5 -admin-rate 0.5 -login-rate 0.05 \
  -portal-users 200 -steady-start $T -steady-dur 30m -out $R -strict > $R/users.log 2>&1 &

# T+20m：改一个用户（设备数覆盖 + 换用户组），触发 200 个节点经事件流重拉
~/loadtest burst -manifest $M -admin-url https://<PANEL_DOMAIN>/<ADMIN_PATH> -count 1 -out $R
```

### 8.1 速率

| 档 | `-sub-interval 30m` 折出的订阅速率 | 每凭据每小时 | 订阅凭据上限 |
|---|---|---|---|
| 5k | 2.8 次/秒 | 2 | 60 |
| 10k | 5.6 次/秒 | 2 | 60 |
| 15k | 8.3 次/秒 | 2 | 60 |

- 这是「测余量」的激进速率：真实客户端常见 1–24 小时更新一次。
- 门户 5 次/秒（200 个活跃用户各自复用登录令牌）、后台 0.5 次/秒、重新登录 0.05 次/秒，各档相同。
- users 开跑前会按速率逐维预估面板各个限流（每 IP、每 /24、每账号、订阅每小时、登录、后台每 IP），每维打一行 ok 或 WARN。出现 WARN 先调速率，再开跑。
- 后台每 IP 每分钟限 240 次；要压更高，给 `-admin-ips` 多个虚构地址。
- 节点侧稳态的请求量不用配，由节拍决定：拉取 15 秒、上报 60 秒由面板经 `base_config` 下发；心跳 30 秒、换钥检查 10 分钟是 pdnd 写死的。
- `-node-behavior` 选模拟哪一代 pdnd，稳态每节点每分钟的请求数如下（单池、无配置变更）：

  | 端点 | current（缺省） | legacy |
  |---|---|---|
  | `GET /v1/nodes/effective-config` | 4，几乎全是 204：带已应用版本，仍是当前版 | 4，每次 200 全量 |
  | `GET /v1/nodes/config-signing-key` | 0.1：10 分钟一次，验签失败时立刻补一次 | 4：每次拉配置前都问 |
  | `POST /v1/nodes/config/report` | ≈0：switched、health_passed 各报一次，面板收下即停，配置版本变了再报 | 8：每轮两条都重报 |
  | `GET /api/v1/server/UniProxy/user` | 4，ETag，稳态几乎全是 304 | 4 |
  | `POST /v1/nodes/heartbeat` | 2 | 2 |
  | `POST /api/v1/server/UniProxy/push`、`alive` | 各 1 | 各 1 |
  | **合计** | **≈12.1** | **≈24** |
  | 200 个节点 | ≈40 req/s | ≈80 req/s |

  - current 的拉取、上报、心跳三条节拍都是定时器：每拍处理完再按 ±10% 随机抖动排下一拍，实际周期是「处理耗时 + 抖动后的间隔」，面板慢的时候请求数会低于上表；换钥检查的间隔同样抖动。抖动的随机源由 `-seed` 派生，同一种子下每个节点每条节拍的间隔序列可复现。
  - legacy 原样保留改版前的模拟器：固定周期、没有抖动，只为和 legacy 时期的实测（例如 `5k-r1`）对照。
  - 核对实测：`nodes.txt` 末尾的「per node per minute」表按稳态窗口把各端点折成每节点每分钟，并按状态码拆开（生效配置的 200 与 204 分开列），可以直接和上表比。JSON 里对应 `per_unit` 与各端点的 `per_unit_per_min`、`per_unit_per_min_by_code`。

### 8.2 场景顺序

| 顺序 | 场景 | 开始前 |
|---|---|---|
| 1 | `idle`：面板 + 两台真 pdnd + 采集，不跑压测工具，采 30 分钟 | 新装后在后台手工建一个资源池、两个节点，按 7.3 第 2 步接入 |
| 2 | `5k-r1` → `5k-r2` | 7.1 重装、7.2 造数（`-users 5000 -label 5k`）、7.3 接入；r2 只重跑第 8 节 |
| 3 | `10k-r1` → `10k-r2` | 同上（`-users 10000 -label 10k`） |
| 4 | `15k-r1` → `15k-r2` | 同上（`-users 15000 -label 15k`） |
| 5 | `15k-24h` | 不重装，沿用 15k 的库与 manifest（同一档） |

- 每档跑两次：共享型 CPU 有邻居干扰，两次结果差得远就再跑一次。
- 每档两次跑完，按 9.4 判断要不要补放开上限的对照轮。

### 8.3 15k 档 24 小时混合（`15k-24h`）

- 和第 8 节相同，只是 nodes 的 `-duration` 用 `24h10m`，users 的 `-duration` 用 `24h`。
- 订阅速率二选一，并在 `env.txt` 里注明用了哪套：
  - **余量版**（默认，与各档稳态一致）：`-sub-interval 30m`
  - **贴近真实版**（可选）：`-sub-interval 6h`，15k 人折合约 0.69 次/秒。拿到老系统的真实拉取间隔后，直接替换这个值。
- burst 每 2 小时一次：

  ```bash
  ~/loadtest burst ... -count 12 -interval 2h
  ```

  次数为偶数，用户会回到原状。
- 面板机：
  - `sample-procs.sh` 和 `sample-cgroup.sh` 全程跑；
  - `snapshot-mem.sh` 每小时打一次，标签用 `h01`…`h24`；
  - `pgstat.sh export` 在第 1、12、24 小时各导一次；
  - `grab-pprof.sh` 在第 1、12、24 小时各抓一次，比较 heap 有没有持续增长。
- 默认 `-traffic-mib 8` 跑 24 小时，每个在线用户约 12GB。seed 默认每人 1024GB，不会触顶；改了 `-traffic-gb` 要相应调小 `-traffic-mib`。

## 9. 结果怎么读

### 9.1 产物

- 压测机：
  - `nodes.json/.txt`、`users.json/.txt`、`users-warmup.json/.txt`、`burst.json`、`burst-http.json/.txt`、各自的 `.log`
- 面板机：
  - `procs.csv`、`cgroup.csv`
  - `pgstat-*-{total,mean,calls}.csv`
  - `pg-memory-*.txt`、`valkey-*.txt`
  - `*-{cpu,heap,allocs,goroutine}-*.pprof`
  - nginx 与网关日志

JSON 里每个端点都有 count、QPS、p50/p95/p99/max（毫秒）、错误码分布、自定义标签，以及 10 秒一格的时间线。

- 节点侧的标签：`sig_fail`、`auth_fail`、`etag_304`、`unchanged_204`（生效配置回 204：节点手上已是当前版）、`trigger:stream`、`phase:*`。
- 每节点每分钟各端点的请求数：`nodes.txt` 末尾的「per node per minute」表（给了 `-steady-*` 就只算稳态窗口内，否则按整个运行区间折，只作参考）。
- burst 的尖峰：在 nodes.json 的时间线里看，按 burst.json 的 `trigger_unix_ms` 对齐。

### 9.2 及格线（照原四条，逐条对照）

| 及格线 | 数据来源 | 怎么判 |
|---|---|---|
| 节点接口 p99 < 300ms | `nodes.json` 里每个 `node:` 端点 `steady.p99_ms`（稳态窗口内；顶层 `p99_ms` 含起跑与收尾齐射，只作参考） | 每个端点都要过，不只看合计 |
| 15k 档 CPU 平均 < 50% | `procs.csv` 的 `_system` 行：`cpu_pct` 平均 ÷（核数 × 100） | 只算 T 到 T+30m 的稳态 |
| 整机内存留 25% 余量，24 小时内不持续上涨 | `procs.csv` 的 `_system` 行：`rss_kb` = MemTotal − MemAvailable，`pswpin` / `pswpout`（累计换入换出页数）；24h 档各进程 `pss_kb` 的趋势；pprof heap 对比 | 峰值 ≤ 75% MemTotal；稳态内 Δpswpin 或 Δpswpout 持续增长（不是个位数的偶发）即不及格，与 MemTotal − MemAvailable 一起列；24 小时的线性趋势不显著为正 |
| 零 5xx | nodes / users / burst 三份 JSON 的 `server_5xx`；nginx access log 里 status ≥ 500 的行数 | 都必须是 0；`transport:*`（连接错误、超时）单列说明 |

### 9.3 必报观测项（不是及格线）

**15k 档稳态时三个网关的 `nr_throttled` 增量**：每次 15k 运行（r1、r2、24h）都要报。

- 数据来源：`cgroup.csv` 里 T 与 T+30m 两行做差（24h 档按小时分段）。
- 每个网关报三个数：
  - `Δnr_throttled`
  - `Δnr_throttled / Δnr_periods`：被节流的周期占比
  - `Δthrottled_usec`
- 同时看 `mem_max_events`、`oom_kill` 有没有增加（撞 MemoryMax）。

其他档也照算，写进结果表，供判断是否撞上限。

### 9.4 撞上限时的对照轮

判据：某档 r1 / r2 里出现下面任一情况，就算这一档「撞上限」：

- **撞 CPUQuota**：`cgroup.csv` 里某网关的 `nr_throttled` 在稳态内持续逐格增长，而不是偶发的个位数。
- **撞 MemoryMax**：`mem_max_events` 或 `oom_kill` 增加。
- **连接池等待明显**：T+15m 的 goroutine profile 里，大量 goroutine 停在取连接上：

  ```bash
  go tool pprof -top -focus 'pgxpool.*Acquire|puddle' <网关>-goroutine-<时间>.pprof
  ```

撞了 CPU 或内存上限：只对撞了的网关临时放开，在同一档补一轮 `<档>-r1-uncapped`（同一个库、同一份 manifest，流程照第 8 节），跑完立刻还原：

```bash
sudo systemctl set-property --runtime aegis-public.service CPUQuota= MemoryMax=infinity   # 只改运行时，重启机器即失效
systemctl show aegis-public.service -p CPUQuotaPerSecUSec -p MemoryMax                     # 确认已放开
# ……对照轮……
sudo systemctl revert aegis-public.service && sudo systemctl daemon-reload                  # 还原随包上限
systemctl show aegis-public.service -p CPUQuotaPerSecUSec -p MemoryMax                     # 应回到 600ms 与 256M；没回到就 systemctl restart aegis-public
```

撞了连接池：池上限由 `.env` 的 `AEGIS_*_DB_MAX_CONNS` 配置，但总量受 PG `max_connections=60` 约束（算式见 `.env.example`），这一轮不做对照。只在结果里写「撞连接池上限」并附 goroutine 证据；要不要为对照调大上限由总协调定。

结果一律写成「<网关> 在 <档> 撞 <哪个上限>，放开后 <指标> 为 …」，不写成「机器不够」。

### 9.5 定位

SQL 热点看 `pgstat-*-total.csv` 前十，结合 pprof 的 CPU 火焰图定位到具体的接口和查询。

## 10. 收尾与删机

1. 面板机（删机就可以跳过；保留机器则必须做）：
   - `./nginx-realip.sh disable`：信任表还原为不信任任何代理的默认文件
   - `./pgstat.sh disable --yes`
   - 清空 `.env` 里三个 `*_PPROF_ADDR`，然后 `systemctl restart aegis-public aegis-admin aegis-node`
   - 放开过上限的网关确认已 `systemctl revert`
2. 把两台机器的 `lt-results` 拉回本机，打包后存到仓库目录之外。
3. 在 Vultr 控制台删除四台机器，删 `<PANEL_DOMAIN>` 的 DNS 记录。
4. 测试用的管理员口令作废。

## 11. 1 万用户、1000 节点与静默场景

整机标准（用户 2026-10-07 定，10-08 升档）：1 万用户、1000 节点；静默（0 活跃用户、1000 节点在线上报）整机已用内存 ≤ 1 GiB（`free` 的 used，不含 cache），静默时面板 + 数据库 ≤ 单核 30%；负载下的及格线与第 9.2 节、prod-retest skill 相同。

### 11.1 造数：1000 节点、1 万用户

选了「全走真实网关、只并行」，没有改成批量接口或超级用户直写库：

| 路径 | 结论 |
|---|---|
| 批量接口 | 面板没有批量建服务器 / 节点 / 签令牌的接口（只有节点状态批量改，退役用的就是它），需要的话得改产品代码，不在压测工具范围 |
| 超级用户 SQL 种子 | 绕开 RLS、状态机触发器、审计，测出来的不是真实开通路径；工具规则也要求造数经运行角色与真实网关，不选 |
| 并行会话（选用） | 后台的限流是每 IP 每分钟 240 次，瓶颈在「每个来源一个会话、请求间隔 260ms」，不在面板。`-admin-workers N` 开 N 个会话，各自登录、各自节流、各占一个虚构来源 IP（198.51.100.1 起，经 `-ip-headers`，缺省 `X-Real-IP`），同一套网关、权限、审计，吞吐是单会话的 N 倍 |

```bash
cd /root/lt && mkdir -p /root/lt-results/10k-seed
set -a; . /opt/aegispanel/deploy/.env; . /opt/aegispanel/deploy/release-artifact.env; set +a
export LOADTEST_ADMIN_PASSWORD='<管理员口令>'
./loadtest seed \
  -admin-base http://127.0.0.1:9001 -node-base http://127.0.0.1:9003 -public-base http://127.0.0.1:9000 \
  -admin-email <ADMIN_EMAIL> -users 10000 -nodes 1000 -label 10k -admin-workers 8 \
  -out /root/lt-results/10k-seed/lt-manifest.json | tee /root/lt-results/10k-seed/seed.log
```

- **直连回环网关**：seed 跑在面板机上，三个地址取 `.env` 的 `AEGIS_ADMIN_ADDR`、`AEGIS_NODE_ADDR`、`AEGIS_PUBLIC_ADDR`（缺省 9001、9003、9000；管理网关在根路径，不带后台前缀）。面板应用只认 `X-Real-IP`，不经 nginx 就不用动信任表，8 个来源 IP 各算各的限流。
  - 要经 nginx（`https://<PANEL_DOMAIN>/<ADMIN_PATH>`）则必须 `nginx-realip.sh enable <LOADGEN_IP> <PANEL_IP>`，让 nginx 采信面板机自己的地址发来的 `X-Real-IP`，否则 8 个会话在 nginx 眼里是同一个来源，登录与后台每 IP 限流（12/分、300/分）照旧卡住。
- **耗时（估算，真机基线时校正）**：4000 个后台请求 ÷ 8 会话 × 260ms ≈ 2.2 分钟；接入 1000 个节点（每个带自己所在服务器的虚构地址）与 1 万用户的 SQL 批量写入（5k 档实测约 11 秒）合计预计 5 到 6 分钟。`-admin-workers 1` 保持旧行为，约 25 分钟。
  - 开工前 seed 会估算建节点阶段耗时，超过 15 分钟（接入令牌 30 分钟有效）直接拒绝并提示加会话数。
  - 会话数上限 32：后台登录接口按路由整体限流（`AEGIS_RL_AUTH_PER_MIN`，缺省 10/分），会话太多光登录就要排队。
- **可重复执行**：`-retire-previous`（缺省开）先把上一批用户的订阅转 expired，再**吊销上一批节点的有效接入身份**（后台不允许退役仍有有效身份的节点，这是旧版重复造数会 409 的原因），再把节点 active → draining → retired。每一步只处理还没处理的，中途失败原样重跑即可；不 DELETE，追加写表照旧。
  - 这条路径靠 GitHub 冒烟（空库）只验到「没有上一批」；带上一批的重跑，第一次在真库上走是 1000 节点基线前的预演，出错先看 `seed.log` 的 `retire_previous` 一段。
  - 想要干净库仍然是第 7.1 节重装数据基座（要用户同意，会删库）。
- 造出的是虚构数据：邮箱 `@loadtest.invalid`、节点与目录以 `loadtest-` 开头、服务器地址在 203.0.113.0/24（1000 台循环使用 254 个地址，每个地址约 4 台）。

### 11.2 模拟节点：单机撑 1000 个

一台 2c4g 压测机跑 `loadtest nodes -nodes 1000`，节拍以 pdnd 当前代码为准（`pdnd/node/node.go`、`pdnd/panel/*`）：拉取 15 秒（面板经 base_config 下发，兜底 60 秒）、上报 60 秒、心跳 30 秒、换钥检查 10 分钟，各带 ±10% 抖动；每个节点一条 SSE（面板每 20 秒一行保活）、两条 TLS 连接（签名通道一条、UniProxy + 事件流共用一条 h2）。稳态每节点每分钟约 12.1 个请求，1000 个节点约 200 req/s（第 8.1 节的表按节点数线性放大）。

```bash
~/loadtest nodes -manifest $M -node-url https://<PANEL_DOMAIN> -stagger 180s -duration 40m \
  -steady-start $T -steady-dur 30m -out $R -strict > $R/nodes.log 2>&1 &
```

- **起跑错开用 180 秒**（200 节点用 60 秒）：每个节点起跑要拉两份全量名单（REST 一份、事件流一份），1 万用户时每份约 1 MB。看进度行 `started=1000 streams=1000` 到齐再进稳态窗口。
- **资源实测**（`nodesim/scale_unix_test.go`，`go test ./tools/loadtest/nodesim -run TestScale1000Nodes -v`）：1000 个节点、1 万用户，假面板在另一个子进程里（TLS + HTTP/2、真实节拍与保活间隔），父进程 CPU 与内存只属于 nodesim：

  | 平台 | 稳态 CPU | 起跑段 CPU | 峰值 RSS | 存活堆 | 请求 | p99 |
  |---|---|---|---|---|---|---|
  | macOS arm64 本机（10 核） | 约 0.1 核 | 约 17 核·秒（起跑后 45 秒内） | 约 350 MiB | 约 150 MiB | 约 264 req/s | 约 20ms |

  - 2c4g 压测机（Linux amd64）的实测数由 CI 的 job summary 给出（测试会把一行数写进 `GITHUB_STEP_SUMMARY`），首轮真机基线再校正；测试里的哨兵是稳态 ≤ 1 核、峰值 RSS ≤ 1.5 GiB，只防出大问题，不是及格线。
  - 优化过两处内存：节点只记「可能连在自己身上」的用户 id（发给面板的请求不变，每节点省 80 KB，千节点省约 80 MiB），事件流读缓冲从 64 KiB 降到 8 KiB。
  - 测试里把 Go 客户端宣告的 HTTP/2 最大帧压到 16 KiB：Go 服务端会按客户端宣告的 1 MiB 切帧，把每条连接的读缓冲撑到 1 MiB（千节点 2 GB）；生产里前面是 nginx，按 8 KiB 切块，没有这个问题。真机上看 RSS 若远高于上表，先怀疑这一条。
  - 压测机本身：2c4g 的压测机跑 1000 节点 + 另一台跑 1 万用户（users），两个进程不要放同一台。

### 11.3 静默场景

静默 = **只起模拟节点**，不起 users、不做 burst；0 个活跃用户，但节点照常拉取、上报（在线比例缺省 0.3，与 5k-r4 静默同口径，每个节点仍有少量 push / alive）。

| 时刻 | 面板机（root） | 压测机 |
|---|---|---|
| T−8m | | `scripts/run-quiet.sh $M https://<PANEL_DOMAIN> $T 1000`（节点起跑，180 秒内错开） |
| T−20s | `scripts/quiet-collect.sh $P $T 15`（自己睡到 T−20 秒取内存快照，后台起 `vmstat 5`） | |
| T | 读第一份 CPU 快照（`cpu-A`） | 稳态窗口开始 |
| T+15m | 读第二份（`cpu-B`），取第二份内存快照，停 `vmstat` | 节点再跑约 2 分钟结束 |

```bash
# 面板机（root），P 是结果目录，T 是约定的 unix 秒（两边同一个值）
setsid -f /root/lt/quiet-collect.sh $P $T 15 > $P.out 2>&1 < /dev/null
# 压测机
setsid -f ~/run-quiet.sh $M https://<PANEL_DOMAIN> $T 1000 $R > /dev/null 2>&1 < /dev/null
# 采集结束后，在任何有 loadtest 的机器上判定
loadtest quiet-report -dir $P            # 加 -strict 则任一标准不达标退出码非 0
```

- **采样口径**（与 5k-r4 一致，刻意很轻——`sample-procs.sh` / `sample-pgact.sh` 自身占 2–9 个百分点，会把静默 CPU 抬过线，静默期间不要开）：
  - CPU：窗口首尾各读一次 `/proc/stat`、全部 `/proc/<pid>/stat`、`system.slice` 下各单元 `cpu.stat`，做差；判定用 cgroup 的 `usage_usec`（含已退出的子进程，最准），面板 + 数据库 = aegis-public + aegis-admin + aegis-node + postgres + valkey，nginx / docker / containerd 单列。单核 = 100，标准 ≤ 30。
  - 内存：窗口外首尾各取一次 `MemTotal − MemAvailable`（等于 `free` 的 used，不含 cache），取较大者对 1024 MiB；同时列 PSS、swap 与窗口内换页数（有换页就说明已用数被 swap 掩盖）。
  - 同时兼容 Docker 数据基座（`containers.txt` 把容器 cgroup 翻成名字）与直装布局（`postgresql@*-main.service`、`valkey-server.service`）。
- 用 5k-r4 的原始快照（`ops-local/vultr-test2/5k-r4/quiet/panel`）喂 `quiet-report`，面板 + 数据库 15.90、含 nginx 18.39、整机忙 22.12、已用内存 918 MiB，与当时人工算的一致。
- 出成绩单时，静默一节单列这几项：面板 + 数据库 CPU、各部分拆分、整机已用内存、swap、节点侧的 QPS / 错误数（`nodes.txt`）、aegis-node 的 `nr_throttled`。

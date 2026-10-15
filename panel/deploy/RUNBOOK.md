# 排障手册

面向自己部署 Pandora Panel 的运维。按症状分章，每章四段：怎么确认 → 常见原因 → 处理 → 什么时候升级处理。

- 面板是一个二进制 `/opt/pandora/bin/pandora`，跑成两个服务：`pandora-serve`（门户 127.0.0.1:9000、后台 127.0.0.1:9001、节点 gRPC :9443）与 `pandora-worker`（后台任务与定时任务，asynq 队列在 Redis 里）。
- 用 Docker Compose 部署的，运维命令对照与 compose 特有的问题及恢复步骤见第 14 章。
- 节点证书的设计与 DNS 凭据怎么配，见 [docs/node-certificates.md](../../docs/node-certificates.md)；节点端的部署，见 [pdnd/release/README.md](../../pdnd/release/README.md)；节点机自身的症状（端口被占、入站起不来、UDP 缓冲告警、接入装到一半）见第 12 章。
- 文中的 `<面板地址>`、`<后台前缀>`、`<订单号>`、`example.test` 都是占位符，换成你自己的。

## 约定

**先只读，后动手。** 每章的「怎么确认」只看状态、日志和库，不改任何东西。「处理」里会改状态的步骤标了 **【写】**：

- 改库之前先备份：`sudo systemctl start pandora-backup.service`，再看 `journalctl -u pandora-backup -n 20` 里的 `backup complete: <路径>`。
- 先想清楚影响面：重启 `pandora-serve` 会断开正在进行的请求、实时推送和全部节点的 gRPC 会话（节点几秒内自己重连）；重启 PostgreSQL 或 Redis 期间整个面板不可用。
- 不手改订单、支付、账本、审计这几类表。它们有守卫触发器和只追加约束，绕过去会让账对不上。

**口令不进命令行。** `/etc/pandora/secrets.env`（服务的连接串、主密钥、JWT 密钥）与 `/etc/pandora/deploy.env`（部署脚本用的超级用户口令、迁移连接串、告警与备份设置）都只给 root 读，不要 `cat` 它们；口令只经标准输入或 0600 文件传，不要写成命令参数（会进 `ps` 和 shell 历史）。

**路径。**

| 是什么 | 在哪 |
|---|---|
| 二进制与脚本 | `/opt/pandora/bin/pandora`；备份、校验、恢复、巡检、证书脚本在 `/opt/pandora/deploy/` |
| 配置 | 非秘密 `/etc/pandora/config.yaml`（`root:pandora` 0640）；服务的秘密 `/etc/pandora/secrets.env`；部署脚本的变量 `/etc/pandora/deploy.env`（root 0600） |
| 日志 | `/var/log/pandora/serve.log`、`worker.log`（JSON，进程自己按 config 的 `log.*` 滚动）；巡检 `/var/log/pandora/health.log` |
| 备份 | `/var/backups/pandora/`（config 的 `backup.dir`，`pandora backup dir` 打印它）；异地 WebDAV 的配置 `/etc/pandora/backup-webdav.json` |
| 面板 HTTPS | 证书 `/etc/pandora/tls/live/`，续期结论 `/var/lib/pandora/tls/status` |

进库（超级用户）：`sudo /opt/pandora/deploy/psql.sh`。它从 `/etc/pandora/deploy.env` 逐键读端口、库名与口令，口令只经环境变量交给 psql。为了少打字，先在 root shell 里定义：

```bash
pg() { /opt/pandora/deploy/psql.sh -X "$@"; }
```

下文的 `pg -c "…"` 都指它。

管理员命令（`pandora admin …`）要用服务的身份和秘密跑，统一这样包一层（下文写作 `pandora-as-service admin …`）：

```bash
sudo systemd-run --pipe --quiet --uid=pandora -p EnvironmentFile=/etc/pandora/secrets.env /opt/pandora/bin/pandora admin list
```

PostgreSQL 与 Redis 是发行版的系统服务：`postgresql@18-main` 和 `redis-server`。

## 0. 先看全貌（只读）

```bash
systemctl --failed
systemctl status pandora-serve pandora-worker nginx --no-pager
# 就绪探针：门户与后台都会再 ping 一次 PostgreSQL 与 Redis；200 是 ok，503 是依赖不通
curl -s -m 5 http://127.0.0.1:9000/readyz; echo; curl -s -m 5 http://127.0.0.1:9001/readyz; echo
# 数据库与缓存：都应是 active (running)
systemctl status postgresql@18-main redis-server --no-pager
systemctl list-timers 'pandora-*'
sudo /opt/pandora/deploy/edge-tls.sh status
```

日志在哪：

| 来源 | 位置 |
|---|---|
| serve、worker | `/var/log/pandora/serve.log`、`worker.log`；启停与崩溃看 `journalctl -u pandora-serve`、`journalctl -u pandora-worker` |
| nginx | `/var/log/nginx/` 下面板站点的 access 与 error 日志 |
| PostgreSQL | `/var/log/postgresql/postgresql-18-main.log`：慢语句、锁等待、检查点；启停看 `journalctl -u postgresql@18-main` |
| Redis | `journalctl -u redis-server` 与 `/var/log/redis/redis-server.log` |
| 证书续期 | `journalctl -u pandora-tls-renew`、结论文件 `/var/lib/pandora/tls/status` |
| 备份 | `journalctl -u pandora-backup` |
| 巡检 | `/var/log/pandora/health.log`、`journalctl -u pandora-health` |
| 节点端（节点机上） | `journalctl -u pandora-native` |

每个请求都带 `req_id`（响应头 `X-Request-ID`，错误响应体里也有）。用户截图里的编号直接拿来查：

```bash
sudo grep -h '<req_id>' /var/log/pandora/serve.log /var/log/pandora/worker.log
```

后台「工作台 › 仪表盘」的系统状态卡片能看到：备份新鲜度与异地备份有没有配、库体积与连接数、节点在线数、卡住的支付回调数、通知的排队与失败数。

## 巡检告警索引

`deploy/healthcheck.sh`（`pandora-health.timer` 每 10 分钟一次）每类告警对应一章：

| 告警文案 | 去哪一章 |
|---|---|
| `服务 pandora-serve 状态为 …`、`服务 pandora-worker 状态为 …` | [1. 面板 5xx / 服务起不来](#1-面板-5xx--服务起不来) |
| `public 端口 9000 探活返回 …`、`admin 端口 9001 探活返回 …`（/readyz） | [1](#1-面板-5xx--服务起不来) |
| `读不出 Redis 连接串`、`Redis 连不上` | [8. 数据库或 Redis 连不上、连接满、慢](#8-数据库或-redis-连不上连接满慢) |
| `读不出 worker 心跳`、`worker 没有心跳`、`读不出定时任务调度器心跳`、`定时任务调度器没有心跳` | [1](#1-面板-5xx--服务起不来)（worker 一节） |
| `数据库连不上` | [8](#8-数据库或-redis-连不上连接满慢)（库进程本身挂了也在这章） |
| `/ 已用 N%`、`/tmp 已用 N%` | [9. 磁盘满](#9-磁盘满) |
| `读不出备份目录`、`备份目录 … 里没有任何备份`、`最新备份已经是 N 小时前的`、`最新备份只有 N 字节，疑似空文件` | [10. 备份、校验与恢复](#10-备份校验与恢复) |
| `取不到 https://… 在用的证书`、`读不出 https://… 证书的有效期`、`https://… 的证书已过期`、`https://… 的证书还剩 N 小时到期`、`HTTPS 证书续期出错：…`、`证书续期 timer 超过 36 小时没跑` | [4. 面板 HTTPS 证书续期失败](#4-面板-https-证书续期失败) |
| `有 N 条通知排队超过 30 分钟没发出去`、`最近六小时有 N 条通知发送失败` | [7. 通知积压](#7-通知积压) |
| `过去 30 分钟没有任何节点上报心跳（7 天内曾有 N 个在报）` | [3. 节点离线或不上报心跳](#3-节点离线或不上报心跳) |
| `通知积压查询失败或超时（每条查询限 10 秒）`、`通知发送失败查询失败或超时（每条查询限 10 秒）`、`节点心跳（最近 30 分钟）查询失败或超时（每条查询限 10 秒）`、`节点心跳（7 天内）查询失败或超时（每条查询限 10 秒）` | [8](#8-数据库或-redis-连不上连接满慢)（锁等待、DDL 或 VACUUM FULL 卡住某张表时会出现） |

另外两个告警源也指到第 4 章：续期单元 `pandora-tls-renew.service` 记为 failed，以及 Telegram 上的 `潘多拉面板 HTTPS 证书（…）：…`。

**巡检怎么跑。**

- `healthcheck.sh` 与 `pandora-health.service` / `pandora-health.timer` 随安装器装好，首装与升级都 `enable --now`。看它在不在跑：`systemctl list-timers pandora-health.timer`。
- 以 root 跑、只读；唯一写的是 `/var/log/pandora/health.log`。没问题只追一行 `OK`；有问题单元记为 failed（`systemctl --failed` 看得见），`deploy.env` 设了 `PANDORA_ALERT_TG_TOKEN` 与 `PANDORA_ALERT_TG_CHAT` 时再推一条 Telegram（令牌经 curl 的标准输入传，不进命令行）。
- 查库直接以 `postgres` 经 `127.0.0.1:POSTGRES_PORT` 连（口令取 `deploy.env`），连库 5 秒放弃、每条查询限 10 秒；第一条 `SELECT 1` 失败后其余查库项跳过，只记一条「数据库连不上」。
- Redis 连接串取 `secrets.env` 的 `PANDORA_REDIS_URL`；worker 心跳看 asynq 写在 Redis 里的服务器与调度器心跳（每 5 秒一次，10 秒过期）。
- 首装后到配好备份之前，会有一条「备份目录里没有任何备份」的告警，这是真告警：去第 10 章把备份配好。

---

## 1. 面板 5xx / 服务起不来

### 怎么确认

- 先看状态码是谁回的：
  - **502**：nginx 连不上 `pandora-serve`，进程没起来或卡死了。
  - **503**：两种可能。一是 nginx 限速；二是面板自己说依赖不可用（就绪检查失败、并发名额满）。
  - **504**：面板没在时限内回，多半是库慢，转第 8 章。
  - **500**：面板内部错误，响应体里有 `req_id`。
- 看进程和日志：

  ```bash
  systemctl status pandora-serve --no-pager          # Active、最近一次退出码、有没有 oom-kill
  journalctl -u pandora-serve -n 50 --no-pager      # 启动失败的原因（配置校验、连库、连 Redis）
  sudo tail -n 200 /var/log/pandora/serve.log | grep -E '"level":"(error|warn)"'
  ```

### 常见原因

| 现象 | 原因 |
|---|---|
| `journalctl -u pandora-serve` 里是配置错误 | `config.yaml` 或 `secrets.env` 不合法，原因写在报错里。生产（`env: production`）要求 `public_base_url` 是 `https://域名` 或 `https://公网IPv4`、不带端口和路径，HTTP 只绑回环，节点 gRPC 配了 `grpc.node.public_host`；秘密写进 `config.yaml` 也会拒绝启动 |
| 报错是连库或连 Redis | 依赖没起来或连接串不对，转第 8 章 |
| `systemctl status` 里有 `oom-kill` | 撞了单元的内存上限（`MemoryMax`），systemd 隔 5 秒重启 |
| 进程 active，`/readyz` 却超时 | 卡死。先看 `serve.log` 最后的错误，再重启 |
| nginx 本身不在跑 | `nginx -t` 看配置；证书文件缺失见第 4 章 |

**worker 一节。** 后台任务（通知派发、支付查单、到期、续费、证书签发、保留期清理……）都在 `pandora-worker` 里跑。它停了，网页照常能打开，但这些事都不再发生。巡检看的是 asynq 在 Redis 里的心跳：

- `worker 没有心跳`：worker 进程不在、卡住，或连不上 Redis。先 `systemctl status pandora-worker`，再看 `journalctl -u pandora-worker -n 50` 与 `worker.log`。
- `定时任务调度器没有心跳`：worker 在处理队列，但定时任务的调度器没起来，到期、续费、清理都不会排进队列。看 `worker.log` 里调度器的报错。
- `读不出 worker 心跳` / `读不出定时任务调度器心跳`：Redis 能 PING 通，但读那两个键报错（多半是有人手工改过同名键），转第 8 章的 Redis 一节。

### 处理

1. 依赖先行：`systemctl status postgresql@18-main redis-server --no-pager`，两个单元应为 `active (running)`。
   - **【写】** 没起来：`systemctl start postgresql@18-main redis-server`，起不来看第 8 章。
2. 按报错改 `config.yaml` 或 `secrets.env`。改之前先复制一份：`cp -p /etc/pandora/config.yaml /etc/pandora/config.yaml.bak.$(date +%s)`。
3. **【写】** `systemctl restart pandora-serve`（或 `pandora-worker`），再用第 0 节的探针确认。重启 serve 会断开全部节点的 gRPC 会话，节点几秒内自己重连并重新对齐配置与名单。
4. 卡死的进程，重启前先抓现场：config 里配了只绑回环的 pprof 地址时，从那个端口取 goroutine 转储。

### 什么时候升级处理

依赖都正常、配置也没报错，500 仍然持续出现，或者进程反复 OOM：带上 `req_id`、对应的日志行（先去掉邮箱、IP 等个人信息）和所装发布包的版本，提给开发。

---

## 2. 管理员进不来

### 怎么确认

按用户看到的现象对号：

| 现象 | 去看 |
|---|---|
| 后台地址打不开、404，或者落到了门户首页 | 前缀不对。`sudo /opt/pandora/deploy/admin-url.sh` 重新打印完整地址；地址要以 `/` 结尾 |
| 浏览器提示证书不安全 | 面板在用自签证书，转第 4 章 |
| 提示「邮箱或密码不正确」 | 下面的审计查询 |
| 429，或「请求太频繁」 | 登录限速：面板按 IP 每分钟 `ratelimit.auth_per_min` 次（缺省 10），nginx 另有边缘限速 |
| 口令对了也进不去，提示稍后再试 | 同一账号 15 分钟内失败 10 次，暂停 15 分钟（期间口令正确也拒） |
| 能登录，保存时全部失败、提示只读 | 降级开关 `admin.writes` 被关了，到「系统 › 安全与运维 › 降级开关」打开 |
| 在门户用管理员账号登录被拒 | 设计如此：管理员账号不能登录门户，请走后台地址 |
| 登录后隔一阵被踢回登录页 | 后台会话 7 天、空闲 2 小时过期 |

「邮箱或密码不正确」对外是同一句话，背后有几种情况。审计里记了真实原因（同一来源 10 分钟内只记一条）：

```bash
pg -c "SELECT occurred_at, data->>'reason' AS reason FROM audit_events WHERE action = 'user.login_failed' AND data->>'audience' = 'admin' ORDER BY occurred_at DESC LIMIT 20;"
```

`reason` 的取值：`invalid_credentials`（不存在或口令错）、`account_inactive`（停用）、`not_admin`（不是管理员）。

列出全部管理员及其状态（只读）：`pandora-as-service admin list`。

### 常见原因

- 前缀记错或被改过（`deploy.env` 的 `PANDORA_ADMIN_PATH`，nginx 按它渲染）。
- 站点在 Cloudflare 后面，但没配真实 IP 信任表：所有人共用 Cloudflare 的几个出口 IP，限速很快就满。启用方法见 `deploy/update-cloudflare-realip.sh` 的说明。
- 唯一的管理员忘了口令，或者被取消了管理员。

### 处理

- 限速与失败暂停：等它过期，不要手工删 Redis 里的键。
- **【写】** 重置口令。会吊销这个账号的全部会话，并清掉登录失败暂停。口令只经标准输入：

  ```bash
  read -rsp '新密码：' p && echo && printf '%s\n' "$p" | sudo systemd-run --pipe --quiet --uid=pandora \
    -p EnvironmentFile=/etc/pandora/secrets.env /opt/pandora/bin/pandora admin reset-password --email <管理员邮箱> --password-stdin; unset p
  ```

- **【写】** 一个管理员都没有了（`pandora admin has-admin` 退出 3；退出 1 是连不上库或配置错，不等于没有）：同样的写法，子命令换成 `create --email <邮箱> --password-stdin`。
- **【写】** 账号在、只是不是管理员了：`pandora-as-service admin grant --email <邮箱>`（会吊销它的门户会话）。

### 什么时候升级处理

审计里查不到对应的失败记录，而 `admin list` 显示账号正常：带上登录请求的 `req_id` 和 `serve.log` 里的对应行，提给开发。

---

## 3. 节点离线或不上报心跳

节点端 `pandora-native` 用一次性令牌接入后，拿面板 CA 签的客户端证书，经 gRPC（`:9443`，面板直接终结 mTLS，不经 nginx）和面板保持一条长连接的会话：建流先发 Hello，面板对齐后回 Welcome 并下发配置与用户名单，之后节点按 Welcome 给的间隔报心跳、流量与机器指标。面板把心跳写进服务器的 `last_heartbeat_at`；超过 90 秒没有心跳的服务器算离线。

### 怎么确认

- 后台「网络 › 节点与服务器」：服务器页看在线、最近心跳、节点端版本；节点页看每个节点的运行状态（运行中 / 降级 / 生效失败）与原因。服务器详情里能看到接入身份（证书序号、到期时间、是否吊销）。
- 库里看心跳（只看就绪与排空中的服务器）：

  ```bash
  pg -c "SELECT name, status, agent_version, last_heartbeat_at, now() - last_heartbeat_at AS ago FROM servers WHERE deleted_at IS NULL AND status IN ('ready','draining') ORDER BY last_heartbeat_at NULLS FIRST LIMIT 50;"
  ```

- 面板这一侧的会话日志（`serve.log`，JSON，按 `node_id` 或服务器 id 过滤）：

  ```bash
  sudo grep -E '节点会话已建立|被同一服务器的新流取代|建流复核发现身份已失效|建流对齐超时|未认证的节点|校验节点身份失败|节点接入被拒|节点接入失败|记录心跳失败' /var/log/pandora/serve.log | tail -n 30
  ```

- 节点机上：

  ```bash
  systemctl status pandora-native --no-pager
  journalctl -u pandora-native -n 100 --no-pager
  pandora-native --version
  # 从节点机测到面板 gRPC 端口的 TCP 连通（证书是面板自己的 CA 签的，不要用浏览器式的校验去测）
  timeout 5 bash -c '</dev/tcp/<面板 gRPC 主机>/9443' && echo reachable
  ```

对用户的影响：

- 从没上报过心跳的节点不进订阅；心跳过期的节点在订阅里降级（同一份订阅里还有新鲜节点时不下发它）。
- 心跳只说明「节点和面板之间的会话」。节点内核可能照常在转发，所以离线不一定等于用户连不上；节点有落盘缓存，面板暂时不可达时照常用上一份配置和名单服务。

### 常见原因

**全部节点同时掉线**（巡检告警就是这一种），先查面板：

| 原因 | 怎么认 |
|---|---|
| `pandora-serve` 没在跑或刚重启 | 第 0 节；重启后节点几秒内应自己重连 |
| 9443 被防火墙或安全组挡了，或 `grpc.node.public_host` 改过、与节点配置里的面板地址对不上 | 节点机上连 9443 超时；节点日志里连接失败 |
| 库不可用 | 会话建不起来（建流对齐要读库），转第 8 章 |
| 面板 CA 或服务端证书出问题 | `serve.log` 里有 `续签节点 gRPC 服务端证书失败` |

**个别节点掉线**，查节点：

| 原因 | 怎么认 |
|---|---|
| `pandora-native` 停了或反复崩溃 | `systemctl status` 与 `journalctl` |
| 节点在报心跳，只是入站有问题（后台显示「生效失败」「降级」） | 见第 12 章 |
| 接入身份被吊销、过期，或服务器被删、退役 | 面板日志 `建流复核发现身份已失效` / `未认证的节点`；节点收到 Tombstone 后停止重连，日志说面板明确拒绝了这个节点 |
| 同一台服务器又接入了一次（重装） | 面板日志 `被同一服务器的新流取代`：旧身份被新身份取代，旧进程该停 |
| 节点机时钟偏差太大 | TLS 握手失败（证书「尚未生效」或「已过期」）；节点机上 `timedatectl` 看 `System clock synchronized` |

### 处理

1. 面板侧原因按对应章节修。
2. **【写】** 节点进程：`systemctl restart pandora-native`。会断开这个节点上的用户连接。
3. **【写】** 时钟：启用时间同步，例如 `timedatectl set-ntp true`，或者装 chrony。
4. **【写】** 身份失效：在后台这台服务器的详情里签发一键安装令牌，在节点机上执行生成的安装命令，重新接入（见第 12.4 节）。
5. 手工改库（不经后台）改了名单或配置相关的表（订阅、配额、流量包、用户状态、节点池、用户组、节点配置、服务器）：面板不会马上下发。会话管理器靠「纪元」发现变化，手工 SQL 不动纪元，要等下一次纪元变化（后台任何一次相关的改动都会触发），Redis 读不到纪元时每 15 秒全量比对一次。急的话 **【写】** `systemctl restart pandora-serve`，节点重连时全量重新对齐。能在后台改的，一律在后台改。

### 什么时候升级处理

面板侧探针正常、9443 通、节点进程在跑、时钟也对，会话仍然建不起来：带上服务器 id、`serve.log` 里含这个 id 的行，以及节点上最近 100 行 `journalctl -u pandora-native`（里面没有私钥），提给开发。

---

## 4. 面板 HTTPS 证书续期失败

证书由 `deploy/edge-tls.sh` 管理，nginx 只读 `/etc/pandora/tls/live/`。`pandora-tls-renew.timer` 每天 03 点和 15 点各跑一次 `edge-tls.sh renew`（随机延后最多 1 小时），结论写进 `/var/lib/pandora/tls/status`。这里只管面板自己的 HTTPS；节点入站证书见第 5 章，节点与面板之间的 gRPC 证书由面板自己的 CA 签，不归这里。

### 怎么确认

```bash
sudo /opt/pandora/deploy/edge-tls.sh status     # 证书来源、到期时间、签发者、上次检查的结论
cat /var/lib/pandora/tls/status                  # RESULT=ok|warn|error，MESSAGE 是原因
systemctl status pandora-tls-renew.service --no-pager
systemctl list-timers pandora-tls-renew.timer
journalctl -u pandora-tls-renew -n 100 --no-pager
# 从外面看 nginx 实际下发的证书
echo | openssl s_client -connect <面板地址>:443 -servername <面板域名> 2>/dev/null | openssl x509 -noout -issuer -enddate
```

`status` 的「证书来源」决定往哪查：

| 来源 | 是什么 | 谁续期 |
|---|---|---|
| `lego` | 公网 IP 的 Let's Encrypt 证书，约 6 天 | 本 timer，过半就续（约剩 3 天） |
| `certbot` | 域名的 Let's Encrypt 证书，90 天 | Debian 自带的 `certbot.timer`；本 timer 只负责证书换了就 reload，快到期时补续一次。`status` 多一行「续期校验」 |
| `selfsigned` | 申请失败时的自签兜底 | 本 timer 每次都重试申请正规证书，成功就无缝换上 |
| `custom` | 运维自己放的证书 | 不替换、不续期，自己管 |

### 常见原因（按 MESSAGE 或告警文案）

| 文案 | 原因 |
|---|---|
| `lego 续期失败（输出见上）：确认 80 端口从公网可达` | 80 被安全组或防火墙挡了，或者这个 IP 已经不是本机的公网地址 |
| `仍在用自签证书：…` | 申请正规证书失败，冒号后面是原因。最常见的还是 80 不通；域名证书还要求域名解析到本机 |
| `certbot 续期失败（报错见 journalctl -u pandora-tls-renew 与 /var/log/letsencrypt/letsencrypt.log）` | 看那两处日志 |
| 「续期校验」显示 `webroot 还指着别处` 或 `找不到续期配置` | certbot 的续期配置没走面板的校验目录 `/var/www/pandora-acme`，续期会 404。`renew` 每次会自动改正（改前备份到 `/var/backups/pandora-edge/letsencrypt-renewal-*`），修不好的原因记进 status |
| `证书已更新但 nginx -t 不通过，没有 reload` | nginx 配置坏了，`nginx -t` 看哪一行 |
| `在用的证书不覆盖面板地址 …（对外地址改过？）` | `config.yaml` 的 `public_base_url` 换了主机 |
| `证书还剩 N 小时到期，续期没成功`、`证书已过期` | 上面几种原因拖到了临界点 |
| 巡检：`取不到 https://… 在用的证书` | nginx 没在 443 上提供 HTTPS |
| 巡检：`证书续期 timer 超过 36 小时没跑` | timer 没启用，或者机器关过机 |

### 影响

证书过期或退回自签时：浏览器提示不安全；校验证书的支付渠道可能推不进回调（第 6 章）。节点的 gRPC 会话不受影响（它用面板自己的 CA）。

### 处理

1. 先确认 80 端口从公网可达。在另一台机器上执行 `curl -sI http://<面板地址>/.well-known/acme-challenge/x`：应回 404。超时说明被挡了；308 说明 nginx 配置不是本面板的模板。
2. **【写】** 修好之后立即续一次。证书没换就不 reload：

   ```bash
   sudo systemctl start pandora-tls-renew.service      # 等价于 edge-tls.sh renew
   sudo /opt/pandora/deploy/edge-tls.sh status
   ```

3. **【写】** 来源是 `selfsigned`：`sudo /opt/pandora/deploy/edge-tls.sh issue`。
4. **【写】** timer 没启用：`systemctl enable --now pandora-tls-renew.timer`。
5. **【写】** 对外地址改过：`sudo /opt/pandora/deploy/edge-tls.sh setup`。它会重新渲染 nginx，`nginx -t` 不过就换回原配置（原配置备份在 `/var/backups/pandora-edge/`）。
6. 域名证书想先验证续期能不能成：`sudo certbot renew --dry-run --cert-name <域名>`。它走 Let's Encrypt 测试环境，不换证书。
7. 不要反复手动 `issue`。Let's Encrypt 有频率限制，撞上了只能等。

### 什么时候升级处理

80 从公网可达、域名解析也对，`lego` 或 `certbot` 仍然失败：带上 `journalctl -u pandora-tls-renew` 的输出（里面没有私钥）和 `edge-tls.sh status`，提给开发。

---

## 5. 节点证书签发失败（面板集中签发）

后台「网络 › 证书」签发的节点入站证书，由 worker 里的证书巡检每分钟推进一轮，走 DNS-01。设计与 DNS 凭据的最小权限配法见 `docs/node-certificates.md`。

### 怎么确认

- 「网络 › 证书 › 证书列表」：状态、最近错误、下次续期；点开一行能看到签发记录与错误详情。「DNS 凭据」页看每个凭据的校验结论。
- 库里：

  ```bash
  pg -c "SELECT name, status, paused_reason, consecutive_failures, next_attempt_at, last_error_code, left(last_error, 160) AS last_error, not_after FROM certificates ORDER BY updated_at DESC;"
  pg -c "SELECT name, provider, zone, verify_status, verified_at, left(verify_error, 160) AS verify_error FROM dns_credentials ORDER BY name;"
  ```

- 日志：`sudo grep -E '证书巡检|证书签发出错|证书已签发|lego' /var/log/pandora/worker.log | tail -n 30`。

### 常见原因（按错误码 `last_error_code`）

| 错误码 | 含义 | 面板怎么处理 |
|---|---|---|
| `credential_rejected` | 服务商明确拒绝了凭据：令牌被吊销、过期，或者看不到这个域名 | 证书停在「DNS 凭据失效」，不碰 CA。凭据改好、重新校验通过后自动恢复 |
| `dns_api_unavailable` | 服务商 API 连不上 | 稍后再试。检查出口网络，以及 `HTTPS_PROXY` 一类环境变量 |
| `dns_provider_error` | 写 TXT 记录时服务商报错，多半是权限不够 | 按退避重试 |
| `dns_propagation_timeout` | TXT 记录写了，但查不到 | 用了 CNAME 委派时，检查 `_acme-challenge` 那条 CNAME |
| `rate_limited` / `rate_limited_local` | CA 回了 429，或面板按自己的签发流水算出会超限额 | 按退避等，不要手动「立即续期」 |
| `acme_account_error` | ACME 账号出错 | 检查「ACME 设置」里的邮箱、EAB |
| `acme_error` | 其他 ACME 错误 | 看签发记录里的详情 |
| `interrupted` | 签发中途 worker 重启 | 租约过期后自动接着做 |

连续失败多次会暂停自动重试（状态「已暂停」），避开 Let's Encrypt 的验证失败上限。

### 处理（都在后台做，录入秘密和删除要近期重认证）

1. 凭据问题：「DNS 凭据」里编辑并「重新校验」。
2. **【写】** 修好原因之后，对「已暂停」的证书点「恢复」。
3. **【写】** 「立即续期」就是换私钥重签，每点一次都占一次 CA 额度。限流期间不要点。

### 什么时候升级处理

凭据校验通过、DNS 记录也查得到，`acme_error` 还反复出现：带上签发记录里的错误详情（不含秘密），提给开发。

---

## 6. 支付回调没到账 / 订单卡住

支付渠道把回调推到 `https://<面板地址>/v1/webhooks/payments/<渠道编码>`（serve 收）。回调丢了也有兜底：worker 每分钟跑一轮主动查单，向渠道查未付的单，查到已付就和回调走同一条路入账。

回调地址的前缀可以单独设：后台「商业 › 订单与收款 › 支付渠道」的「回调地址」卡片（`payment.notify_base_url`，只收 https）。留空 = 用面板地址。单独设的域名要自己解析到面板、并由前面的反向代理或 CDN 终止 TLS 后转给面板（`render-nginx.sh` 只生成面板地址这一个 server，不会替你加这个域名）。从面板地址改成单独前缀（或改回面板地址）时，之前建的 CoinPayments 发票仍能按原地址验签入账；在两个单独前缀之间直接切换时，用旧前缀建、还没回调的 CoinPayments 发票会验签失败，靠 worker 的主动查单补记；渠道卡片上显示的回调地址就是现在生效的那个。

加密货币渠道（BTCPay Server、CoinPayments）：
- BTCPay 的回调不随发票下发，要在 BTCPay 商店「Settings › Webhooks」里新建一个 Webhook：地址填渠道卡片上的回调地址，事件至少勾选 Invoice settled / expired / invalid，密钥与后台渠道里填的 Webhook 密钥一致。只有「已结算」且不是在 BTCPay 里手动标记的发票才自动入账；手动标记、部分付款都不自动入账，按下面「处理」里的线下办法核对。
- BTCPay 商店「Settings › Checkout」里的付款容差（Payment tolerance）必须保持 0：大于 0 时少付但在容差内的发票也会直接变成「已结算」，回查到的发票金额仍是全额，面板没法从金额上看出少付。面板对「已结算」但附加状态是部分付款（PaidPartial）的发票按待定处理、不入账，这只是兜底，容差仍要设 0。
- 给面板单独建一个 BTCPay 商店，不要在同一个商店里再接 POS、Pay Button 或别的系统：那些发票的回调也会发到面板，面板找不到对应的支付会回 4xx / 5xx，BTCPay 会一直重投并把 Webhook 标成失败。面板入账时还会核对发票号必须是这张支付建单时存下的那张，别的发票即使金额和单号对得上也不会入账。
- CoinPayments 的回调地址随每张发票下发，不用在商户后台配。商户后台「接受部分付款」与「少付容差」请保持关闭（0）：开了以后少付的发票也会变成 completed，面板发现实收低于应收会按待定处理、不入账。

### 怎么确认

- 后台「商业 › 订单与收款 › 订单」，找到这张单，看状态和支付尝试。
- 日志里找回调与查单：

  ```bash
  sudo grep -E '支付回调已处理|支付回调入账失败' /var/log/pandora/serve.log | grep '<订单号>' | tail
  sudo grep -E 'payment query patrol done|巡检查单未成|查单结果与支付意图金额不符' /var/log/pandora/worker.log | tail -n 5
  ```

- 库里看这张单和收到的回调：

  ```bash
  pg -v no='<订单号>' <<'SQL'
  SELECT order_no, status, total_amount, currency, expires_at, paid_at FROM orders WHERE order_no = :'no';
  SELECT received_at, processing_status, left(processing_error, 160) AS error
    FROM payment_events WHERE raw_payload::text LIKE '%' || :'no' || '%' ORDER BY received_at;
  SQL
  ```

- 全局看有没有卡住的回调（收到超过 1 分钟还没处理）：

  ```bash
  pg -c "SELECT processing_status, count(*), min(received_at) FROM payment_events WHERE processing_status = 'pending' AND received_at < now() - interval '1 minute' GROUP BY 1;"
  ```

### 常见原因

| 原因 | 怎么认 |
|---|---|
| 渠道连不上回调地址：证书自签或过期、防火墙或 WAF 拦截、对外地址改过 | 一条回调都没有。从外网 `curl -sS -o /dev/null -w '%{http_code}\n' https://<面板地址>/v1/webhooks/payments/<渠道编码>` 应回 4xx（报文不全），超时或 TLS 报错就是这一类 |
| 商户密钥不对 | 回调验签失败，不入账 |
| BTCPay Webhook 密钥不一致、Webhook 没建或没勾事件 | BTCPay 商店的 Webhook 投递记录里是 401，或者根本没有投递；面板这边验签失败不入账 |
| CoinPayments 回调时间戳偏差超过 5 分钟、回调地址被代理改写 | 回调验签失败；面板机器的时间要用 NTP 校准，回调前缀改过的话核对发票建出时的地址 |
| 付款时订单已经取消或过期；一张单付了两笔 | 订单状态是 cancelled / expired，这笔钱进了「挂账」 |
| 金额或币种对不上 | `查单结果与支付意图金额不符，未入账`，或回调的 `processing_error` 写明 |
| worker 没在跑 | 主动查单不跑，转第 1 章 worker 一节 |

### 处理

1. 回调进不来的原因按上表修（证书见第 4 章）。修好之前，主动查单会继续补记。
2. **【写】** 订单详情里点「向渠道查单」。它只把渠道用商户密钥确认过的那笔记上，金额币种不符照样拒绝，可以重复点。
3. **【写】** 商户密钥：在「商业 › 订单与收款 › 支付渠道」里改，留空表示不改。
4. **【写】** 钱进了挂账：到「挂账」标签页核对后「转入余额」，要近期重认证。
5. 不要用 SQL 改订单、支付、账本表。

### 什么时候升级处理

同一个入账错误反复出现；「向渠道查单」报金额不符，而渠道后台显示金额是对的；账本对不上。带上订单号、`req_id` 和对应的日志行，提给开发。订单号和用户信息不要贴到公开的地方。

---

## 7. 通知积压

业务里「决定要通知」只是往投递表里插一行，worker 每 30 秒派发一轮。每条投递最多试 5 次，用完转成 `failed`，之后不再自动重发。

### 怎么确认

- 后台仪表盘的系统状态卡片：排队、重试中、累计失败。
- 库里按状态和错误汇总：

  ```bash
  pg -c "SELECT channel, status, count(*), min(created_at) AS oldest FROM notification_deliveries WHERE status IN ('queued','failed') GROUP BY 1, 2 ORDER BY 1, 2;"
  pg -c "SELECT left(error_message, 120) AS error, count(*) FROM notification_deliveries WHERE status IN ('queued','failed') AND error_message IS NOT NULL AND created_at > now() - interval '6 hours' GROUP BY 1 ORDER BY 2 DESC LIMIT 10;"
  ```

- 日志：`sudo grep -E 'notify: (发送失败|尝试用尽|模板缺失|收件地址格式不正确)' /var/log/pandora/worker.log | tail`。

### 常见原因

| 原因 | 怎么认 |
|---|---|
| SMTP 不通、口令改了、发信被服务商限流 | `error_message` |
| 降级开关 `notify.email` 关着 | 邮件只排队不发，这是开关的设计。打开后按原顺序继续发 |
| worker 没在跑 | 转第 1 章 worker 一节 |
| 库慢 | 转第 8 章 |

收件人不能发、渠道没配置的会记成 `suppressed`，不是失败，也不算积压。

### 处理

1. 到后台通知渠道的设置里修好配置，用「测试发送」确认能发出去。
2. **【写】** 检查「系统 › 安全与运维 › 降级开关」里的 `notify.email`。
3. 修好之后，排队中的会自动发完。已经 `failed` 的不会重发，不要直接改库。

### 什么时候升级处理

渠道测试发送成功，队列仍然不动：带上 `worker.log` 里那几行，提给开发。

---

## 8. 数据库或 Redis 连不上、连接满、慢

### 怎么确认

- 现象：第 0 节的就绪探针回 503；日志里有 `too many clients`（SQLSTATE 53300）、`canceling statement due to statement timeout`（57014）或 `context deadline exceeded`；nginx 有 504。
- 库进程本身：

  ```bash
  systemctl status postgresql@18-main --no-pager
  tail -n 50 /var/log/postgresql/postgresql-18-main.log       # 慢语句、锁等待、OOM、磁盘满
  journalctl -u postgresql@18-main --since '30 min ago' --no-pager
  ```

- 连接与长事务：

  ```bash
  pg -c "SELECT usename, state, count(*) FROM pg_stat_activity WHERE backend_type = 'client backend' GROUP BY 1, 2 ORDER BY 3 DESC;"
  pg -c "SHOW max_connections;"
  pg -c "SELECT pid, usename, state, now() - xact_start AS xact_age, wait_event_type, left(query, 80) AS query FROM pg_stat_activity WHERE xact_start < now() - interval '1 minute' ORDER BY xact_start;"
  pg -c "SELECT pid, pg_blocking_pids(pid) AS blocked_by, left(query, 80) AS query FROM pg_stat_activity WHERE cardinality(pg_blocking_pids(pid)) > 0;"
  ```

  serve 与 worker 以 `pandora_app` 连，迁移以 `pandora_owner` 连。连接预算：`max_connections`（`/etc/postgresql/18/main/conf.d/pandora.conf`）要放得下 serve 的池（config `database.max_conns`，缺省 30）、worker 的池、超级用户保留与维护余量。

- Redis：

  ```bash
  systemctl status redis-server --no-pager
  journalctl -u redis-server -n 50 --no-pager
  ```

  Redis 只绑回环、有口令；连接串在 `secrets.env` 的 `PANDORA_REDIS_URL`。会话、限流计数、asynq 队列与实时推送都在 Redis 里，它配了 `noeviction` 与 AOF：内存满时写入报错而不是丢键。

### 常见原因

| 原因 | 怎么认 |
|---|---|
| PostgreSQL 单元挂了或反复重启 | `systemctl status postgresql@18-main`；日志里有 OOM 或磁盘满（转第 9 章） |
| 某个会话 `idle in transaction`，或者手工跑的语句占着锁 | 第三、四条查询 |
| 改大了连接池，或多开了实例，总数超出 `max_connections` | 第一条查询各用户的连接数 |
| 保留期清理积压，表越来越大、查询越来越慢 | `worker.log` 里有清理失败 |
| Redis 没起来、口令改了 | `Redis 连不上`；serve 与 worker 都会报连 Redis 失败 |
| Redis 内存满 | 日志 `OOM command not allowed when used memory > 'maxmemory'`：会话写不进、任务排不进队。先查是什么占的（`MEMORY STATS`），再评估调高 `maxmemory` |

**自动拉起被杀掉的 PostgreSQL。** PostgreSQL 的 drop-in 写了 `OOMPolicy=continue`：被 OOM 杀掉的多半是某个后端连接，单元不停，postmaster 自己做崩溃恢复。postmaster 本身真被杀了（`kill -9`、OOM 杀到它本身），drop-in 的 `ExecStopPost` 跑 `deploy/pg-revive.sh`：systemd 说 postmaster 是被信号杀掉的、且这个单元没有 `systemctl stop`/`restart` 在处理，就排一个 5 秒后的 `systemctl start`（`journalctl -t pandora-pg-revive` 有记录）；60 秒内再死一次就不再拉。**要让一个被杀掉的库保持停着**，先 `systemctl stop pandora-pg-revive-18-main.timer` 取消排好的拉起（5 秒窗口内），之后再 `systemctl stop postgresql@18-main`。有意停库（`systemctl stop`、`pg_ctlcluster 18 main stop`）不会被拉回来。

### 处理

1. **【写】** 先取消、再终止。取消只停当前语句，终止会断开连接、回滚它的事务：

   ```bash
   pg -c "SELECT pg_cancel_backend(<pid>);"
   pg -c "SELECT pg_terminate_backend(<pid>);"   # 取消不管用时
   ```

   只动确认过的那个 pid。不要终止迁移会话，处理迁移见第 11 章。
2. **【写】** 重启库：`systemctl restart postgresql@18-main`；重启 Redis：`systemctl restart redis-server`（AOF 落盘，会话与队列不丢）。期间整个面板不可用，serve 与 worker 会自动重连。
3. **【写】** 调连接预算：数据库参数改发布包里的 `deploy/postgresql-pandora.conf` 再重跑安装器（它整份覆盖机器上那份）；只改机器上那份，下次安装会被覆盖。

### 什么时候升级处理

同一条业务语句反复慢，或者保留期清理一直失败：带上 PostgreSQL 日志里那条语句（先去掉其中的个人信息）和库体积，提给开发。

---

## 9. 磁盘满

### 怎么确认

```bash
df -h / /tmp
du -xsh /var/log/pandora /var/log/nginx /var/log/postgresql /var/lib/redis /var/backups/pandora /var/backups/pandora-edge /var/lib/postgresql 2>/dev/null
journalctl --disk-usage
pg -c "SELECT pg_size_pretty(pg_database_size(current_database()));"
pg -c "SELECT relname, pg_size_pretty(pg_total_relation_size(relid)) AS size FROM pg_catalog.pg_statio_user_tables ORDER BY pg_total_relation_size(relid) DESC LIMIT 15;"
```

后果：库写不进去，所有写操作失败，PostgreSQL 可能直接停掉；Redis 的 AOF 写不进去后拒绝写入；备份、证书续期也会失败。

### 常见原因

- **备份堆积。** `backup-postgres.sh` 只按 `deploy.env` 的 `PANDORA_BACKUP_RETENTION_DAYS` 清理自己生成的 `pandora-postgres-*.dump.age`（连同 `.sha256`、`.seal`）。安装器升级前留的 `pre-upgrade-*.dump` 不会自动清。
- **`/tmp` 满。** 它常是一块小的 tmpfs；根分区却还有空间。
- **日志。** serve 与 worker 的日志由进程按 config 的 `log.*` 滚动；`health.log` 由 logrotate（`/etc/logrotate.d/pandora`）每周轮转；nginx、journal、PostgreSQL 与 Redis 的日志按发行版设置。
- **库变大。** 节点上报的流量、指标、在线记录靠 worker 的保留期清理（每天凌晨）。worker 停了很久，这些表会一直涨。

### 处理

1. **【写】** 删旧的升级前备份：先确认有一份更新的 `pandora-postgres-*.dump.age` 通过了校验（第 10 章），异地也有副本，再删 `pre-upgrade-*.dump`。
2. **【写】** `journalctl --vacuum-size=200M`。
3. 不要手动删 PostgreSQL 数据目录里的任何文件，包括 `pg_wal`；也不要删 Redis 的 AOF 文件。
4. 库本身太大：确认 worker 在跑、`worker.log` 里没有清理失败；积压会在之后几轮里分批清掉。

### 什么时候升级处理

清理之后某张表仍然持续猛涨：带上上面的表大小列表，提给开发。

---

## 10. 备份、校验与恢复

`pandora-backup.timer` 每天 03:17 UTC 附近（随机延后最多 45 分钟）以 root 跑 `deploy/backup-postgres.sh`：

1. `pg_dump`（custom 格式，带属主与权限），边导出边让 `pg_restore --list` 核一遍目录；
2. 用 `age` 加密成 `pandora-postgres-<时间>.dump.age`，先发布校验文件 `.sha256` 再发布归档（都不覆盖已有文件）；
3. `pandora backup seal-local` 写本地封条 `.seal`（用这台安装的 age 私钥派生的签名），封不上就撤掉这一份、记为失败；
4. `/etc/pandora/backup-webdav.json` 在时，`pandora backup webdav-upload` 把归档、校验文件和签名清单传到 WebDAV，再写可信检查点。传失败时本地这一份留着（已封条、可恢复），但这次备份记为失败；
5. 可选的 `PANDORA_BACKUP_REMOTE_HOOK`；最后按 `PANDORA_BACKUP_RETENTION_DAYS` 清本地旧备份。

只备份 PostgreSQL。Redis 不备份：会话丢了只需重新登录，任务丢了靠对账与定时任务补。

### 怎么确认

```bash
systemctl status pandora-backup.service --no-pager
systemctl list-timers pandora-backup.timer
journalctl -u pandora-backup -n 100 --no-pager
ls -lt "$(sudo /opt/pandora/bin/pandora backup dir)" | head
```

后台仪表盘的系统状态卡片也会显示：最新一份的时间、缺校验文件的份数、异地备份有没有配。

### 常见原因

- **timer 没启用。** 安装器装了 `pandora-backup.service/.timer`，但要你确认私钥另存之后再启用；新装的机器上「一份备份都没有」多半是这个。
- `deploy.env` 的 `PANDORA_BACKUP_AGE_RECIPIENT` 还是占位符，或 `PANDORA_BACKUP_AGE_IDENTITY` 指的私钥不在、权限不是 root 0600、与收件人对不上：脚本拒绝生成明文备份或写不出封条，直接失败。
- `postgresql@18-main` 没在跑，或 `deploy.env` 的 `POSTGRES_SUPER_PASSWORD` 与库里 `postgres` 的口令对不上（报 `password authentication failed`）。
- 巡检报 `读不出备份目录`：`pandora backup dir` 失败，多半是 `config.yaml` 的 `backup.dir` 不是规范的绝对路径。改了 `backup.dir` 还要同步改 `pandora-backup.service` 的 `ReadWritePaths`。
- 磁盘满（第 9 章）。
- `offsite WebDAV upload failed; the sealed local backup … is kept`：异地上传失败，原因只报阶段名（`probe_failed`、`archive_failed`、`manifest_failed`、`checkpoint_replication_failed` 等），看 `journalctl -u pandora-backup`。

### 处理

1. 按日志里的原因修 `deploy.env` 或环境。
2. **【写】** 启用每日备份：`systemctl enable --now pandora-backup.timer`。
3. **【写】** 立即补一份：`systemctl start pandora-backup.service`，再看 `journalctl -u pandora-backup`。
4. 校验最新一份（以 root 跑，读 `/etc/pandora/deploy.env`）：

   ```bash
   sudo /opt/pandora/deploy/verify-backup.sh /var/backups/pandora/pandora-postgres-<时间>.dump.age
   ```

   - 它先核这份备份的来路，再核 sha256、解密和归档目录，通过时输出 `backup verified:`。来路两种：
     - 本机写的备份：封条 `.seal`，留存期内哪一份都能核、能恢复；
     - 从 WebDAV 取回的备份：签名清单 `pandora-postgres-<时间>.manifest.json` 加 WebDAV 之外的可信检查点（`deploy.env` 的 `PANDORA_BACKUP_MANIFEST_PUBLIC_KEY`、`PANDORA_BACKUP_TRUSTED_CHECKPOINT`），只认最新一份（防替换、防回滚）。
   - 两样都没有报 `refusing an archive of unknown origin`；封条对不上报 `local backup seal verification failed`：这份不是本安装写出的，或者归档、校验文件被改过，不要用脚本恢复它（见下面「恢复来路不明的备份」）。
   - 封条用的就是解密私钥：换过私钥的备份核不过。私钥要另外保存一份（离开这台机器），重装后放回原处。
   - 本次调用设 `PANDORA_BACKUP_VERIFY_RESTORE=1` 会在临时库里完整恢复一遍，需要和库差不多大的空闲磁盘。
5. 异地备份：照 `deploy/backup-webdav.example.json` 写 `/etc/pandora/backup-webdav.json`（root 0600），口令放它指的 0600 文件里；签名密钥用 `sudo /opt/pandora/bin/pandora backup init-signing-key <种子文件>` 生成，打印出的公钥另存到面板机之外。

### 从备份恢复

`restore-postgres.sh` 只恢复本安装写出的备份（封条或签名清单核得过），保留属主与权限。顺序是：核来路与完整性 → 读归档要的角色（只认 `pandora_owner`、`pandora_app`、`postgres`，缺的建成 NOLOGIN）→ 在临时库里用同样的参数完整演练一遍 → 才动目标库。

先恢复到一个新库核对（不碰线上）：

```bash
sudo env PANDORA_RESTORE_CONFIRM=RESTORE:pandora_recovery /opt/pandora/deploy/restore-postgres.sh \
  --archive /var/backups/pandora/pandora-postgres-<时间>.dump.age --target-db pandora_recovery
```

**【写】** 覆盖线上库（`config.yaml` 指向的那个库）要多两道确认，脚本会先停住并屏蔽 `pandora-serve`、`pandora-worker`、`pandora-backup`、`pandora-health`，关上库的连接闸门，恢复成功才放开；中途失败保持停服与闸门关闭，提示照实说（旧库已删时会说它没了）：

```bash
sudo env PANDORA_RESTORE_CONFIRM=RESTORE:pandora \
  PANDORA_RESTORE_EXISTING_CONFIRM=OVERWRITE_EXISTING:pandora \
  PANDORA_RESTORE_PRODUCTION_CONFIRM=OVERWRITE_CONFIGURED_DATABASE:pandora \
  /opt/pandora/deploy/restore-postgres.sh --archive /var/backups/pandora/pandora-postgres-<时间>.dump.age --target-db pandora
```

恢复完、起服务之前：库级的设置不在归档里，按脚本最后的 NOTE 重新配置数据库角色（安装器的角色步骤），备份比当前二进制旧时再跑 `/opt/pandora/bin/pandora migrate up`，然后 `systemctl start pandora-serve pandora-worker`。Redis 里的会话还指着旧数据，用户需要重新登录。

### 恢复来路不明的备份

封条与签名清单都核不过（别的机器写的、私钥换过、文件被改过）的归档，脚本一律拒绝，提示里写着本节。只在你确认它的来历时手工做，而且只恢复到一个新库里查看或取数据，不要直接顶替线上库：

1. 准备一个 root 0700 的工作目录，用写这份备份时的 age 私钥解密：`age --decrypt --identity <私钥> <归档> > <工作目录>/plain.dump`（明文落盘，用完立即删掉）。
2. 先只看目录：`pg_restore --list <工作目录>/plain.dump | head`，确认是 PostgreSQL 的 custom 格式、对象是你预期的。
3. 建一个新库，不带属主与权限地恢复进去：`createdb --template=template0 --encoding=UTF8 --owner=pandora_owner pandora_unknown`，再 `pg_restore -d pandora_unknown --no-owner --no-privileges --exit-on-error <工作目录>/plain.dump`（以超级用户连，口令只经 `PGPASSWORD`）。
4. 在这个库里核对数据、取出需要的部分；要让面板改用它，先找开发评估（角色、权限、迁移版本都要重配）。
5. 删掉明文：`shred -u <工作目录>/plain.dump`。

### 什么时候升级处理

备份能生成，但 `verify-backup.sh` 校验不过；或者恢复演练失败。

---

## 11. 迁移失败

安装器首装与升级都跑 `pandora migrate up`（goose 版本化迁移，以 `deploy.env` 的 `PANDORA_DATABASE_MIGRATION_URL`，即属主角色 `pandora_owner` 连），失败即停，不起服务。

### 怎么确认

```bash
sudo systemd-run --pipe --quiet --uid=pandora -p EnvironmentFile=/etc/pandora/secrets.env \
  -p EnvironmentFile=/etc/pandora/deploy.env /opt/pandora/bin/pandora migrate status
```

`status` 列出每个迁移版本与是否已执行。安装器的输出里有失败的那个版本与 PostgreSQL 的报错。

### 处理

- 先按报错修好原因（约束不满足、锁等待超时、磁盘满），再重跑安装器。
- 每个迁移都有 Down：**【写】** 确需回退一步时用 `pandora migrate down`（同上的包法），一次只退一个版本；回退之前先备份，回退之后用旧版本的发布包。
- 迁移半途失败、库的状态说不清时，从升级前的备份恢复（第 10 章），不要手工改 goose 的版本表。

### 什么时候升级处理

迁移本身报错（约束不满足、守卫拒绝），或 Down 被拒：带上完整输出和 `migrate status`，提给开发。

---

## 12. 节点机侧（pdnd）

面板这头看到的症状是「节点显示 降级 / 生效失败」，原因在节点机上：端口被占、入站起不来、UDP 缓冲偏小、接入装到一半。节点失联、心跳进不来看第 3 章。本章命令在节点机上以 root 跑，日志用 `journalctl -u pandora-native` 看。

### 怎么确认

节点自己会把运行状态随 gRPC 会话报给面板，状态只有 `running` 和 `degraded`，原因是一个机器可读串：

| 原因（`runtime_reason`） | 后台节点列表显示 | 意思 |
|---|---|---|
| `port_in_use:<端口>/<tcp\|udp>:<节点 id>` | 生效失败：端口 … 被节点「…」占用 | 同一面板的另一个节点占着这个端口 |
| `port_in_use:<端口>/<tcp\|udp>:other` | 生效失败：端口 … 被本机其他服务占用 | 别的面板的节点，或本机其他进程 |
| `not_started` | 生效失败：入站没有起来 | 入站没起来（首个配置就没装上、回滚也失败，或刚重启） |
| `config_apply_failed` | 生效失败：新配置装不上，仍在用旧配置服务 | 新配置装不上，上一版仍在服务 |
| `serving_cached_config` | 降级：用缓存服务 | 面板还没确认，节点正用落盘缓存的配置服务（面板不可达时的正常降级，先看第 3 章） |

库里看（只读）：

```bash
pg -c "SELECT name, runtime_status, runtime_reason, runtime_state_at FROM nodes WHERE runtime_status = 'degraded' ORDER BY runtime_state_at DESC LIMIT 50;"
```

节点机上：

```bash
journalctl -u pandora-native -n 200 --no-pager | grep -E '节点降级运行|新配置应用失败|入站已就绪|重试之前装不上的配置|UDP 缓冲偏小'
ss -lntup | grep -E ':<端口>\b'     # 谁占着这个端口
```

### 12.1 端口被占

- **原因**：同一台机器上另一个入站、别的服务（nginx、旧的代理进程）已经 bind 了这个端口和协议（TCP 和 UDP 分开算）。后台有同机端口门禁：同一台服务器上两个节点配同一端口，保存时就会被拒；这条故障多半是机器上有后台管不到的进程。
- **处理**：
  1. `ss -lntup` 找出占用者。
  2. **【写】** 停掉那个进程，或在后台把这个节点的端口改到空闲的端口。
  3. 不用重启 pdnd：配置装不上时节点会自己按退避重试，端口释放后下一轮就会装上，原因随之清空。
  4. 想马上生效：**【写】** `systemctl restart pandora-native`，会断开这台节点上的用户连接。

### 12.2 入站起不来（`not_started` 或 `config_apply_failed`）

- **看日志里的 `err`**：`address already in use` 转 12.1；证书读不进来，证书由面板集中签发的转第 5 章；其它报错带着整段日志提给开发。
- **`新配置应用失败，已恢复上一版本`**：新版装不上，上一版在服务，用户不受影响。原因修好后自动重试，不用手动回滚。
- **面板明确拒绝这个节点（身份吊销、服务器被删）时**，节点不会用落盘缓存起服务：转第 3 章「身份失效」。

### 12.3 UDP 缓冲告警

日志里每个 hy2 / TUIC 入站启动时出一条「QUIC 入站的 UDP 缓冲偏小 …」。

- **原因**：Linux 缺省 `rmem_max` / `wmem_max` 约 208KB，quic-go 要 8MB 拿不到，单连接大流量时整批丢包。pdnd 以无特权用户运行，不能也不该给 unit 加 `CAP_NET_ADMIN` 去强制放大。
- 面板的节点安装脚本已经写了 `/etc/sysctl.d/90-pandora-native.conf`（只调大不调小）。看到告警说明这台是手工安装、装在容器里（`/proc/sys` 只读），或者 sysctl 在 pdnd 启动之后才生效。
- **处理**：`sysctl net.core.rmem_max net.core.wmem_max` 看当前值；**【写】** 照 `pdnd/release/README.md` 写 sysctl 并 `sysctl -p`，再 `systemctl restart pandora-native`；容器里在宿主机改。

### 12.4 接入装到一半卡住

后台服务器详情签发的一键安装令牌给出的命令是 `curl -fsSL '<面板地址>/pdnd/install.sh' | sudo bash -s -- --panel '<gRPC 主机>:9443' --ca-sha256 'sha256:<指纹>' --token '<令牌>'`。脚本的顺序是：校验参数 → 下载二进制并核对校验和 → 用新二进制 `pandora-native enroll`（按 CA 指纹钉住面板，令牌经 stdin 交给它，经 gRPC 换来客户端证书）→ 写配置与 unit → 校验 → 换上并重启服务。

- **接入之前失败**（参数、下载、校验和、`enroll` 被拒）：机器上原有的安装没动。网络中断造成的 `enroll` 失败，用同一条命令重跑即可（同一令牌原样重发，面板幂等地返回同一张证书）。令牌过期或已用过（面板日志 `接入被拒绝：令牌无效、已用或已过期`）就在后台重新签发。
- **接入之后失败**（校验不过、服务起不来）：新身份已经取代了旧身份。处理掉报错后，重新签发令牌重跑安装命令。
- **重装、升级**：直接用新令牌重跑安装命令，新身份取代旧身份。
- 指纹不符：命令里的 `--ca-sha256` 与面板 CA 不一致，或者 `--panel` 指到了别的服务，按后台给的命令原样复制。连不上 9443：防火墙放行，或 `config.yaml` 的 `grpc.node.public_host` 不是节点能连到的地址。
- 身份目录 `/var/lib/pandora-native/identity/` 属 `pandora` 用户、0700，里面的 `key.pem` 是私钥，不要贴出来。

### 什么时候升级处理

- 端口确实空闲，节点仍长时间报 `port_in_use` 或 `not_started`：带上节点 id、`ss -lntup` 输出和最近 200 行 `journalctl -u pandora-native`。
- 同一令牌重跑 `enroll` 仍然失败、或新令牌也被拒：带上安装脚本的完整输出（里面不含令牌）和面板 `serve.log` 里含这台服务器 id 的行。

---

## 13. 系统服务的约束

- `pandora-serve`、`pandora-worker` 以专用用户 `pandora` 跑，单元带 `MemoryMax` 与相应的 `GOMEMLIMIT`、常规沙箱加固；被 OOM 杀掉后 systemd 自己拉起。
- `pandora-backup`、`pandora-health`、`pandora-tls-renew` 以 root 跑，但都在沙箱里：备份只写备份目录与 WebDAV 检查点目录，巡检只写 `/var/log/pandora`，续期只写证书相关的目录。备份单元的系统调用过滤不许改文件属主，备份目录由安装器建成 `root:pandora` 0750（面板的系统状态页要列目录），文件本身都是 0600。
- PostgreSQL 与 Redis 的加固（systemd drop-in、Redis 配置块）由安装器写，开关是 `deploy.env` 的 `PANDORA_SYSTEMD_HARDENING`；被挡的系统调用一律返回 EPERM。想临时去掉某项排查：删掉对应的 drop-in，再 `systemctl daemon-reload && systemctl restart <单元>`，下次安装会再加回来；真有功能被挡住，带着 `journalctl -u <单元> -n 50` 报上来。

---

## 14. Docker / Compose 部署

适用于通过 `panel/deploy/docker/compose.yaml`（含 1Panel 编排导入）部署的环境。
组件分为 8 个容器：`postgres`、`redis`、`migrate`（初始化与迁移）、`serve`、`worker`、`nginx`、只持有网络命名空间与发布端口的 `netns`（serve 与 nginx 都挂在它上面）以及轻量级运维容器 `ops`（`profiles: ["ops"]`）。

### 命令对照表

| 运维场景 | 裸机命令（systemd） | Docker Compose 对应命令 |
|---|---|---|
| 查看核心状态 | `systemctl status pandora-serve pandora-worker` | `docker compose ps` |
| 查看运行日志 | `journalctl -u pandora-serve` / `tail -f serve.log` | `docker compose logs --tail 200 serve` |
| 重启核心服务 | `systemctl restart pandora-serve` | `docker compose restart serve`（nginx 随之重启） |
| 超管进库排查 | `/opt/pandora/deploy/psql.sh` | `docker compose run --rm ops psql`（单条加 `-T`：`docker compose run --rm -T ops psql -tAc '…'`） |
| 管理员命令行 | `pandora-as-service admin …` | `docker compose exec serve /opt/pandora/bin/pandora admin …` |
| 立即加密备份 | `systemctl start pandora-backup.service` | `docker compose run --rm -T ops backup` |
| 校验归档文件 | `/opt/pandora/deploy/verify-backup.sh <文件>` | `docker compose run --rm -T ops verify <文件>` |
| 重载边缘 Nginx | `nginx -t && systemctl reload nginx` | `docker compose exec nginx nginx -s reload` |

---

### 14.1 migrate 容器退出码非 0

启动或升级时 `docker compose ps` 显示 `migrate` 服务状态为 `Exited (1)` 或其他非 0 状态，`serve` 与 `worker` 未启动。

#### 怎么确认
```bash
docker compose logs migrate
```

#### 常见原因
- **密码不满足安全复杂度**：`.env` 中的密码（`POSTGRES_SUPER_PASSWORD`、`PANDORA_OWNER_DB_PASSWORD`、`PANDORA_APP_DB_PASSWORD`、`PANDORA_REDIS_PASSWORD`）须至少包含 32 个 URL 安全字符（`[A-Za-z0-9_-]`）。
- **修改了 `.env` 中的超级用户口令但数据库已初始化**：PostgreSQL 官方镜像的 `POSTGRES_PASSWORD` 环境变量**仅在首次初始化数据卷时生效**。若数据库已存在而后修改了 `.env` 中的 `POSTGRES_SUPER_PASSWORD`，容器启动时会报 `password authentication failed`。
- **迁移本身报错**：Goose 迁移执行阶段数据库报错或前置依赖检查失败。

#### 处理
1. 若为口令不合规，在 `.env` 中修正后重新执行 `docker compose up -d`。
2. 若改动了超级用户口令导致验证失败：
   - 方式 A：临时在 `.env` 中改回原本初始化时使用的旧口令。
   - 方式 B：真要换成新口令：先在 `.env` 里把 `POSTGRES_SUPER_PASSWORD` 改回旧口令，再把新口令经标准输入交给 psql 改库里的口令（口令不进命令行参数、不进 shell 历史；新口令同样只用 32 位以上 `[A-Za-z0-9_-]`，不会破坏引号）：
     ```bash
     read -rsp '新超级用户口令：' np && echo
     printf "ALTER ROLE postgres PASSWORD '%s';\n" "$np" | docker compose run --rm -T ops psql -X -v ON_ERROR_STOP=1
     unset np
     ```
     成功后在 `.env` 中把 `POSTGRES_SUPER_PASSWORD` 改成新口令，重新 `docker compose up -d`。
3. 若为迁移 SQL 报错，按第 11 章排查原因。

#### 什么时候升级处理
修复环境与口令后，Goose 迁移仍旧报约束冲突、表锁死或内部脚本报错，带上 `docker compose logs migrate` 提给开发。

---

### 14.2 nginx 容器起不来或处于 unhealthy 状态

`docker compose ps` 中 `nginx` 处于 `Restarting`，或持续 `unhealthy`。

#### 怎么确认
```bash
docker compose logs nginx
```

#### 常见原因
- `PANDORA_PUBLIC_BASE_URL` 格式不合规（必须形如 `https://<域名或公网IPv4>`，不能包含端口与路径，且末尾不带斜杠）。
- `PANDORA_ADMIN_PATH` 未配置或仍保持占位符 `CHANGE_ME`（必须为 20~64 位 URL 安全字符）。
- `PANDORA_REALIP_FROM` 填入了无效的 IP/CIDR 网段，或在未识别网关环境下解析失败。
- 证书文件不匹配：挂载的 `fullchain.pem` 与 `privkey.pem` 密钥对不匹配或损坏。
- 健康检查探测失败：nginx 的健康检查要求探测 `http://127.0.0.1:9000/readyz`（门户）与 `http://127.0.0.1:9001/readyz`（后台）；若两个接口任一返回非 200，说明背后的 `serve` 进程未就绪、或者其连接 PostgreSQL / Redis 异常。

#### 处理
1. 检查并修正 `.env` 中的相关环境变量配置。
2. 若证书损坏，临时清空 `./tls` 目录中的文件，重启 nginx 让其生成自签证书恢复启动。
3. 查看核心服务日志排查后端就绪状况：`docker compose logs serve`。

#### 什么时候升级处理
环境变量与证书确认完全无误，但 Nginx 启动前检查仍报渲染崩溃，带着 `docker compose logs nginx` 反馈。

---

### 14.3 重建 serve 后门户打不开

对 `serve` 执行了 `docker compose up -d --force-recreate serve` 或升级镜像后，外部访问 80/443 或 9080 端口报连接被重置或 502/拒绝连接。

#### 怎么确认
```bash
docker compose ps
docker compose logs nginx
```

#### 常见原因
`serve` 与 `nginx` 都通过 `network_mode: "service:netns"` 加入 `netns` 容器的网络命名空间，宿主端口也发布在 `netns` 上。重建或就地拉起 `serve`、`nginx` 都不换命名空间；只有 `netns` 被重建（改了它的配置或镜像版本）时，另外两个要跟着重建才能接上——`docker compose up -d` 会按依赖一起处理，手工只重建了 `netns` 时就会出现这种情况。

#### 处理
**【写】** 重建挂在 netns 上的两个容器，让它们接入当前的命名空间：
```bash
docker compose up -d --force-recreate serve nginx
```

#### 什么时候升级处理
重建 nginx 后依然无法连接，排查 Docker Engine 的 bridge 网络驱动状态。

---

### 14.4 来源真实 IP 异常

面板管理后台中记录的管理员登录 IP、客户端订阅请求或接口限流全被识别为同一个 `172.x.x.x` 地址（Docker 内部容器网关 IP）。

#### 怎么确认
在后台「审计日志」或「登录记录」中观察记录的 IP 是否为容器网段内网 IP。

#### 常见原因
- 前面已有前置反代（如 1Panel OpenResty / Caddy），但 `.env` 中遗漏配置了 `PANDORA_REALIP_FROM=gateway`。
- 前置反代未按要求覆写 `X-Real-IP` 请求头，或直接透传了客户端原样请求头。
- **严重误配**：在 80/443 直面公网开放的情况下，错误地配置了 `PANDORA_REALIP_FROM=gateway`。这会导致外部公网 IPv4/IPv6 流量经由宿主 `docker-proxy` 转发时全部显示为网关 IP，使恶意客户端可通过伪造头部绕过安全限制。

#### 处理
1. 确认前置反代拓扑：
   - 方式一（无前置反代，面板自身直出 80/443）：`.env` 中的 `PANDORA_REALIP_FROM` **必须留空**（或仅填 Cloudflare 官方回源网段）。
   - 方式二（前置反代接入，面板 80/443 不对外）：`.env` 中配置 `PANDORA_REALIP_FROM=gateway`，且 `PANDORA_HTTP_PUBLISH=127.0.0.1:`、`PANDORA_HTTPS_PUBLISH=127.0.0.1:`。
2. 核对前置反代配置，务必包含：
   ```nginx
   proxy_set_header X-Real-IP $remote_addr;
   ```
3. 修改配置后重启边缘代理：`docker compose restart nginx`。

#### 什么时候升级处理
拓扑与头部配置核对无误但来源 IP 仍识别错误，提给开发排查 Nginx 提取逻辑。

---

### 14.5 数据库灾难恢复（正式库恢复流程）

在容器环境下，`pandora-ops` 镜像内部封锁了直接原地覆盖正在运行中的生产数据库 `pandora` 的功能（裸机脚本的直接覆盖需要宿主 systemctl 停服联锁）。**数据恢复必须通过「恢复至临时库 → 停服务重命名库 → 重跑 migrate 授权」三步完成**。

#### 场景 A：从日常加密归档恢复（`.dump.age`）

**【写】数据恢复完整流程：**

1. 将备份文件先完整恢复到新建的临时库 `pandora_restore`（ops 脚本内部会核验本地封条、校验完整性并在沙箱临时库校验后再恢复）：
   ```bash
   docker compose run --rm -T \
     -e PANDORA_RESTORE_CONFIRM=RESTORE:pandora_restore \
     ops restore --archive /var/backups/pandora/pandora-postgres-<时间戳>.dump.age --target-db pandora_restore
   ```
2. 停止可能写入数据库的业务服务容器：
   ```bash
   docker compose stop nginx serve worker
   ```
3. 以超级用户连入数据库，将损坏的生产库重命名归档，并将新恢复的库重命名为正式库：
   ```bash
   D=$(date +%Y%m%d%H%M%S)
   docker compose run --rm -T ops psql -d postgres -c "ALTER DATABASE pandora RENAME TO pandora_replaced_$D;"
   docker compose run --rm -T ops psql -d postgres -c "ALTER DATABASE pandora_restore RENAME TO pandora;"
   ```
4. 重新启动服务：
   ```bash
   docker compose up -d
   ```
   *说明：启动时 `migrate` 会首先执行，核验新库的 `pandora_owner` / `pandora_app` 角色及权限收敛，随后拉起 `serve` 与 `worker`。*
5. 验证面板后台与业务数据无误后，手工清理旧库：
   ```bash
   docker compose run --rm -T ops psql -d postgres -c "DROP DATABASE pandora_replaced_$D;"
   ```

---

#### 场景 B：从升级前自动转储恢复（`pre-upgrade-*.dump`）

在面板升级过程中，若迁移中断或升级后发现重大业务问题需要回退，备份卷中已自动保存了一份未加密的 custom 格式逻辑转储（`/var/backups/pandora/pre-upgrade-<时间戳>.dump`）。

**【写】升级转储恢复流程：**

1. 停止运行容器并由 ops 建立临时恢复库并倒入数据：
   ```bash
   docker compose stop nginx serve worker

   # 通过 ops 容器内的 pg 工具链执行恢复（连接回环 PostgreSQL）
   docker compose run --rm -T --entrypoint sh ops -c '
     export PGHOST=127.0.0.1 PGUSER=postgres PGPASSWORD="$POSTGRES_SUPER_PASSWORD"
     createdb -O pandora_owner pandora_restore
     pg_restore -d pandora_restore /var/backups/pandora/pre-upgrade-<时间戳>.dump
   '
   ```
2. 切换数据库名：
   ```bash
   D=$(date +%Y%m%d%H%M%S)
   docker compose run --rm -T ops psql -d postgres -c "ALTER DATABASE pandora RENAME TO pandora_replaced_$D;"
   docker compose run --rm -T ops psql -d postgres -c "ALTER DATABASE pandora_restore RENAME TO pandora;"
   ```
3. 在 `.env` 中将 `PANDORA_VERSION` 换回原本的旧版本号，并拉起对应版本的容器：
   ```bash
   docker compose up -d
   ```
4. 验证一切正常后，手工清理被替换的损坏库：
   ```bash
   docker compose run --rm -T ops psql -d postgres -c "DROP DATABASE pandora_replaced_$D;"
   ```

---

### 14.6 磁盘空间管理与清理

- **容器日志**：`compose.yaml` 中所有服务均统一配置了 `json-file` 日志驱动，单容器日志上限为 `10m`，最多保留 `5` 个历史文件（最大占用约 50MB/服务），无需配置宿主 logrotate。
- **备份归档清理**：日常生成的加密备份（`/var/backups/pandora/*.dump.age`）会在每日执行 `ops backup` 时，根据 `.env` 中的 `PANDORA_BACKUP_RETENTION_DAYS` 自动清理超期文件。
- **升级转储文件保留**：升级前自动生成的 `pre-upgrade-*.dump` 转储属于关键历史快照，**系统不会自动清理**。若长期运行积累了较多升级转储，可通过 ops 容器确认后按需删除已过期的升级文件：
  ```bash
  docker compose run --rm -T --entrypoint sh ops -c 'rm -f /var/backups/pandora/pre-upgrade-2026*.dump'
  ```

---

## 15. 订阅域名打不开

后台「节点 → 订阅」页的「订阅域名」管哪些用户拿到哪个域名的链接；nginx 只认 `/etc/pandora/deploy.env` 里 `PANDORA_SUB_DOMAINS` 登记过的订阅域名，且这些域名上只放行订阅拉取（`GET /<12位前缀>/<令牌>`）与门户探测接口 `/v1/sub-probe`（回 204），其余路径一律 404。两边要一致：后台填了、部署没登记 → 链接打不开。

### 怎么确认

```bash
# 根路径应回 404（说明 443 块已生效、证书可用）
curl -s -o /dev/null -w '%{http_code}\n' https://<订阅域名>/
# 用自己的一条订阅链接（换成订阅域名）应回 200
curl -s -o /dev/null -w '%{http_code}\n' 'https://<订阅域名>/<前缀>/<令牌>'
# 门户探测接口应回 204（门户「我的套餐」页靠它判断这个域名连不连得上）
curl -s -o /dev/null -w '%{http_code}\n' https://<订阅域名>/v1/sub-probe
# 检查 nginx 生成的站点配置中该域名的 server 块个数：2 = 80 与 443 都有（证书已就绪），1 = 只有 80（缺证书）
grep -c 'server_name <订阅域名>;' /etc/nginx/conf.d/pandora.conf
# 重新应用查看是否有缺证书的告警
sudo /opt/pandora/deploy/edge-tls.sh apply
```

拉取链接不要用 `curl -I` 判断：它发 HEAD，拉取接口不处理 HEAD（会回 404）；`/v1/sub-probe` 的 HEAD 回 204。

### 常见原因

- **`deploy.env` 没登记或拼错**：未在 `PANDORA_SUB_DOMAINS` 中登记的域名，80 端口回 444（默认阻断未知 Host），443 落到面板主站的 server 块上、客户端报证书与域名不匹配。
- **证书没放或文件名不对**：证书目录必须在 `/etc/pandora/tls/sub/<订阅域名>/`，文件必须叫 `fullchain.pem` 与 `privkey.pem`（缺证书时脚本只渲染 80，443 连不上）。
- **证书续期后没 reload**：certbot 或其它工具续期后未重载 nginx（certbot 申请时须带 `--deploy-hook 'systemctl reload nginx'`）。
- **DNS 没解析过来**：订阅域名的 A / AAAA 记录未解析到面板所在服务器的公网 IP。
- **注意**：订阅域名的证书目前不自动申请与续期（`edge-tls.sh` 只管面板主域名），需要运维自己管理。
- **后台显示的「有效权重」= 基础权重 × 分数**：分数在 0.1–3 之间，随时间（6 小时半衰期）回落到 1。经该域名的成功拉取和门户探测成功会升高它；门户探测失败会降低它；探测的成功和失败都要同一小时里至少 3 个不同账号报同一域名才算，且每个域名每小时探测带来的升降各不超过 0.5。某个域名分数明显低于 1，说明有不少用户的浏览器连不上它：多半是 DNS、证书或被墙；一直是 1 说明最近没人经它拉取，也没人探测（门户用户只探测自己所在组的域名，要有生效中的订阅）。

### 处理

新加一个订阅域名（以 `sub-a.example.com` 为例）：

1. **DNS 解析**：将 `sub-a.example.com` 解析到面板服务器公网 IP。
2. **【写】登记域名**：编辑 `/etc/pandora/deploy.env`，追加或修改 `PANDORA_SUB_DOMAINS`（多个以逗号分隔）：
   ```bash
   PANDORA_SUB_DOMAINS=sub-a.example.com,sub-b.example.com
   ```
3. **【写】先渲染出 80**（供 ACME HTTP-01 校验）：
   ```bash
   sudo /opt/pandora/deploy/edge-tls.sh apply
   ```
   此时该域名仅有 80 端口，且已配置 `/.well-known/acme-challenge/` 目录指向 `/var/www/pandora-acme`。
4. **【写】申请并放置证书**：
   - 方式一（使用 certbot webroot 申请并软链接）：
     ```bash
     sudo certbot certonly --webroot -w /var/www/pandora-acme -d sub-a.example.com --deploy-hook 'systemctl reload nginx'
     sudo install -d -m 0755 /etc/pandora/tls/sub
     sudo ln -sfn /etc/letsencrypt/live/sub-a.example.com /etc/pandora/tls/sub/sub-a.example.com
     ```
   - 方式二（自备证书）：
     ```bash
     sudo install -d -m 0755 /etc/pandora/tls/sub/sub-a.example.com
     # 将 fullchain.pem 与 privkey.pem 放入该目录，私钥权限收紧
     sudo chmod 0600 /etc/pandora/tls/sub/sub-a.example.com/privkey.pem
     ```
5. **【写】重新渲染生效**：
   ```bash
   sudo /opt/pandora/deploy/edge-tls.sh apply
   ```
   此时检测到证书存在，将同时渲染 443 块并重载 nginx。
6. **后台分配**：在面板后台「节点 → 订阅」中将域名加进对应的订阅域名组。

### 什么时候升级处理

- `deploy.env` 已正确配置、证书文件存在且 `nginx -t` 报错无法定位。
- 订阅链接持续返回 502/503（面板上游服务 `pandora-serve` 异常）。带上 `pandora.conf`、`journalctl -u pandora-serve -n 50` 和 `curl -v` 输出提给开发。

# 排障手册

面向自己部署 Pandora Panel 的运维。按症状分章，每章四段：怎么确认 → 常见原因 → 处理 → 什么时候升级处理。

- 面板是一个二进制 `/opt/pandora/bin/pandora`，跑成两个服务：`pandora-serve`（门户 127.0.0.1:9000、后台 127.0.0.1:9001、节点 gRPC :9443）与 `pandora-worker`（后台任务与定时任务，asynq 队列在 Redis 里）。
- 节点入站证书怎么放、怎么续期，见 [docs/node-certificates.md](../../docs/node-certificates.md)；节点端的部署，见 [pdnd/release/README.md](../../pdnd/release/README.md)；节点机自身的症状（端口被占、入站起不来、UDP 缓冲告警、接入装到一半）见第 11 章。
- 文中的 `<面板地址>`、`<后台前缀>`、`<订单号>`、`example.test` 都是占位符，换成你自己的。
- 章号固定，删章不顺延（脚本提示、开发规则与别的文档按章号引用），所以没有第 13 章。

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
| `读不出 Redis 连接串`、`Redis 连不上` | [7. 数据库或 Redis 连不上、连接满、慢](#7-数据库或-redis-连不上连接满慢) |
| `读不出 worker 心跳`、`worker 没有心跳`、`读不出定时任务调度器心跳`、`定时任务调度器没有心跳` | [1](#1-面板-5xx--服务起不来)（worker 一节） |
| `数据库连不上` | [7](#7-数据库或-redis-连不上连接满慢)（库进程本身挂了也在这章） |
| `/ 已用 N%`、`/tmp 已用 N%` | [8. 磁盘满](#8-磁盘满) |
| `读不出备份目录`、`备份目录 … 里没有任何备份`、`最新备份已经是 N 小时前的`、`最新备份只有 N 字节，疑似空文件` | [9. 备份、校验与恢复](#9-备份校验与恢复) |
| `取不到 https://… 在用的证书`、`读不出 https://… 证书的有效期`、`https://… 的证书已过期`、`https://… 的证书还剩 N 小时到期`、`HTTPS 证书续期出错：…`、`证书续期 timer 超过 36 小时没跑` | [4. 面板 HTTPS 证书续期失败](#4-面板-https-证书续期失败) |
| `有 N 条通知排队超过 30 分钟没发出去`、`最近六小时有 N 条通知发送失败` | [6. 通知积压](#6-通知积压) |
| `过去 30 分钟没有任何节点上报心跳（7 天内曾有 N 个在报）` | [3. 节点离线或不上报心跳](#3-节点离线或不上报心跳) |
| `通知积压查询失败或超时（每条查询限 10 秒）`、`通知发送失败查询失败或超时（每条查询限 10 秒）`、`节点心跳（最近 30 分钟）查询失败或超时（每条查询限 10 秒）`、`节点心跳（7 天内）查询失败或超时（每条查询限 10 秒）` | [7](#7-数据库或-redis-连不上连接满慢)（锁等待、DDL 或 VACUUM FULL 卡住某张表时会出现） |

另外两个告警源也指到第 4 章：续期单元 `pandora-tls-renew.service` 记为 failed，以及 Telegram 上的 `潘多拉面板 HTTPS 证书（…）：…`。

面板自己的数据巡检（worker 的 `report:integrity`，每 30 分钟）不走 `healthcheck.sh`：告警发到后台配置的 Telegram 管理员群组，文案以 `数据巡检发现异常` 开头，后台仪表盘「要处理」出红色的「数据巡检」格，见 [16. 数据巡检告警](#16-数据巡检告警)。

后台仪表盘「要处理」出「证书异常」格（节点证书申请失败或快到期）同样不走 `healthcheck.sh`，见 [11.5 节点证书申请失败](#115-节点证书申请失败cert_pending-久不消失cert_expired仪表盘证书异常)。

**巡检怎么跑。**

- `healthcheck.sh` 与 `pandora-health.service` / `pandora-health.timer` 随安装器装好，首装与升级都 `enable --now`。看它在不在跑：`systemctl list-timers pandora-health.timer`。
- 以 root 跑、只读；唯一写的是 `/var/log/pandora/health.log`。没问题只追一行 `OK`；有问题单元记为 failed（`systemctl --failed` 看得见），`deploy.env` 设了 `PANDORA_ALERT_TG_TOKEN` 与 `PANDORA_ALERT_TG_CHAT` 时再推一条 Telegram（令牌经 curl 的标准输入传，不进命令行）。
- 查库直接以 `postgres` 经 `127.0.0.1:POSTGRES_PORT` 连（口令取 `deploy.env`），连库 5 秒放弃、每条查询限 10 秒；第一条 `SELECT 1` 失败后其余查库项跳过，只记一条「数据库连不上」。
- Redis 连接串取 `secrets.env` 的 `PANDORA_REDIS_URL`；worker 心跳看 asynq 写在 Redis 里的服务器与调度器心跳（每 5 秒一次，10 秒过期）。
- 首装后到配好备份之前，会有一条「备份目录里没有任何备份」的告警，这是真告警：去第 9 章把备份配好。

---

## 1. 面板 5xx / 服务起不来

### 怎么确认

- 先看状态码是谁回的：
  - **502**：nginx 连不上 `pandora-serve`，进程没起来或卡死了。API 路径（`/v1/…`、后台前缀下的 `/v1/…`）回的是 JSON 信封 `service_unavailable`，`request_id` 是 nginx 的（在 `/var/log/nginx/pandora-access.log` 里按 `request_id=` 查），面板日志里没有这条。
  - **503**：面板自己说依赖不可用（就绪检查失败、并发名额满），信封 `service_unavailable`，`request_id` 在面板日志里查得到。nginx 的限速与连接数超限回 **429**（主域名 API 路径是 `rate_limited` 信封，订阅域名只回状态码），不再是 503。
  - **504**：两种来源。信封里的 `request_id` 在面板日志里查得到，是面板自己的请求超时（`timeout`，`http.public.request_timeout` / `http.admin.request_timeout`，缺省 25 秒）；查不到、只在 nginx 访问日志里，是 nginx 等上游超时（`proxy_read_timeout`）。多半是库慢，转第 7 章。
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
| 报错是连库或连 Redis | 依赖没起来或连接串不对，转第 7 章 |
| `systemctl status` 里有 `oom-kill` | 撞了单元的内存上限（`MemoryMax`），systemd 隔 5 秒重启 |
| 进程 active，`/readyz` 却超时 | 卡死。先看 `serve.log` 最后的错误，再重启 |
| nginx 本身不在跑 | `nginx -t` 看配置；证书文件缺失见第 4 章 |

**worker 一节。** 后台任务（通知派发、支付查单、到期、续费、证书签发、保留期清理……）都在 `pandora-worker` 里跑。它停了，网页照常能打开，但这些事都不再发生。巡检看的是 asynq 在 Redis 里的心跳：

- `worker 没有心跳`：worker 进程不在、卡住，或连不上 Redis。先 `systemctl status pandora-worker`，再看 `journalctl -u pandora-worker -n 50` 与 `worker.log`。
- `定时任务调度器没有心跳`：worker 在处理队列，但定时任务的调度器没起来，到期、续费、清理都不会排进队列。看 `worker.log` 里调度器的报错。
- `读不出 worker 心跳` / `读不出定时任务调度器心跳`：Redis 能 PING 通，但读那两个键报错（多半是有人手工改过同名键），转第 7 章的 Redis 一节。

### 处理

1. 依赖先行：`systemctl status postgresql@18-main redis-server --no-pager`，两个单元应为 `active (running)`。
   - **【写】** 没起来：`systemctl start postgresql@18-main redis-server`，起不来看第 7 章。
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
| 能登录，保存时全部失败、提示只读 | 降级开关 `admin.writes` 被关了，到「安全与运维 › 降级开关」打开 |
| 在门户用管理员账号登录被拒 | 设计如此：管理员账号不能登录门户，请走后台地址 |
| 登录后隔一阵被踢回登录页 | 后台会话 7 天、空闲 2 小时过期 |
| 用着用着突然掉线，审计里有 `session.refresh_reuse` | 下面「refresh 重放」 |

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

### refresh 重放（session.refresh_reuse）

面板见到一枚已经被换下的 refresh cookie 再出现（超过 30 秒宽限的上一代，或更早的任一代），就认为 cookie 落在了两个人手里，吊销整条会话并写这条审计（门户、后台同一套）。只读查最近的记录：

```bash
pg -c "SELECT occurred_at, resource_id AS user_id, data->>'audience' AS audience, data->>'session_id' AS session_id FROM audit_events WHERE action = 'session.refresh_reuse' ORDER BY occurred_at DESC LIMIT 20;"
```

- 偶尔一条、用户重新登录后不再出现：多半是误报。已知三种：轮换的响应丢了（关页、断网）而浏览器过了 30 秒才重试；服务端卡顿超过 30 秒，排队的旧请求晚到；浏览器还原了旧的 cookie（还原配置文件、同步或备份）。看同一时间前后 `serve.log` 里这个用户的 refresh 是否有 5xx 或慢请求。
- 同一用户反复出现、或伴随陌生 IP 的登录：按 cookie 被盗处理。**【写】** 重置这个账号的口令（会吊销全部会话，命令见上），并提醒用户检查设备与浏览器扩展。
- refresh 的各代由主密钥加每条会话在 Redis 里的随机种子算出：只泄漏主密钥或只泄漏 Redis 都伪造不了。两者同时疑似泄漏时按升级处理：要让全部会话失效。主密钥目前不能轮换（封存与多处 HMAC 都依赖它），不要自行替换，否则数据会读不出来。

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
| 库不可用 | 会话建不起来（建流对齐要读库），转第 7 章 |
| 面板 CA 或服务端证书出问题 | `serve.log` 里有 `续签节点 gRPC 服务端证书失败` |

**个别节点掉线**，查节点：

| 原因 | 怎么认 |
|---|---|
| `pandora-native` 停了或反复崩溃 | `systemctl status` 与 `journalctl` |
| 节点在报心跳，只是入站有问题（后台显示「生效失败」「降级」） | 见第 11 章 |
| 接入身份被吊销、过期，或服务器被删、退役 | 面板日志 `建流复核发现身份已失效` / `未认证的节点`；节点收到 Tombstone 后停止重连，日志说面板明确拒绝了这个节点 |
| 同一台服务器又接入了一次（重装） | 面板日志 `被同一服务器的新流取代`：旧身份被新身份取代，旧进程该停 |
| 节点机时钟偏差太大 | TLS 握手失败（证书「尚未生效」或「已过期」）；节点机上 `timedatectl` 看 `System clock synchronized` |

### 处理

1. 面板侧原因按对应章节修。
2. **【写】** 节点进程：`systemctl restart pandora-native`。会断开这个节点上的用户连接。
3. **【写】** 时钟：启用时间同步，例如 `timedatectl set-ntp true`，或者装 chrony。
4. **【写】** 身份失效：在后台这台服务器的详情里签发一键安装令牌，在节点机上执行生成的安装命令，重新接入（见第 11.4 节）。
5. 手工改库（不经后台）改了名单或配置相关的表（订阅、配额、用户状态、套餐线路、节点池、用户组、节点配置、服务器）：面板不会马上下发。会话管理器靠「纪元」发现变化，手工 SQL 不动纪元，要等下一次纪元变化（后台任何一次相关的改动都会触发），Redis 读不到纪元时每 15 秒全量比对一次。急的话 **【写】** `systemctl restart pandora-serve`，节点重连时全量重新对齐。能在后台改的，一律在后台改。

### 什么时候升级处理

面板侧探针正常、9443 通、节点进程在跑、时钟也对，会话仍然建不起来：带上服务器 id、`serve.log` 里含这个 id 的行，以及节点上最近 100 行 `journalctl -u pandora-native`（里面没有私钥），提给开发。

---

## 4. 面板 HTTPS 证书续期失败

证书由 `deploy/edge-tls.sh` 管理，nginx 只读 `/etc/pandora/tls/live/`。`pandora-tls-renew.timer` 每天 03 点和 15 点各跑一次 `edge-tls.sh renew`（随机延后最多 1 小时），结论写进 `/var/lib/pandora/tls/status`。这里只管面板自己的 HTTPS；节点入站证书放在节点机上（见 `docs/node-certificates.md`），节点与面板之间的 gRPC 证书由面板自己的 CA 签，不归这里。

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

证书过期或退回自签时：浏览器提示不安全；校验证书的支付渠道可能推不进回调（第 5 章）。节点的 gRPC 会话不受影响（它用面板自己的 CA）。

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

## 5. 支付回调没到账 / 订单卡住

支付渠道把回调推到 `https://<面板地址>/v1/webhooks/payments/<渠道编码>`（serve 收）。回调丢了也有兜底：worker 每分钟跑一轮主动查单，向渠道查未付的单，查到已付就和回调走同一条路入账。

回调地址的前缀可以单独设：后台「订单与收款 › 支付渠道」的「回调地址」卡片（`payment.notify_base_url`，只收 https）。留空 = 用面板地址（后台改过主域名就是主域名，见第 15 章）。单独设的域名要自己解析到面板、并由前面的反向代理或 CDN 终止 TLS 后转给面板（`render-nginx.sh` 只生成面板地址这一个 server，不会替你加这个域名）。从面板地址改成单独前缀（或改回面板地址）时，之前建的 CoinPayments 发票仍能按原地址验签入账；在两个单独前缀之间直接切换时，用旧前缀建、还没回调的 CoinPayments 发票会验签失败，靠 worker 的主动查单补记；渠道卡片上显示的回调地址就是现在生效的那个。

加密货币渠道（BTCPay Server、CoinPayments）：
- BTCPay 的回调不随发票下发，要在 BTCPay 商店「Settings › Webhooks」里新建一个 Webhook：地址填渠道卡片上的回调地址，事件至少勾选 Invoice settled / expired / invalid，密钥与后台渠道里填的 Webhook 密钥一致。只有「已结算」且不是在 BTCPay 里手动标记的发票才自动入账；手动标记、部分付款都不自动入账，按下面「处理」里的线下办法核对。
- BTCPay 商店「Settings › Checkout」里的付款容差（Payment tolerance）必须保持 0：大于 0 时少付但在容差内的发票也会直接变成「已结算」，回查到的发票金额仍是全额，面板没法从金额上看出少付。面板对「已结算」但附加状态是部分付款（PaidPartial）的发票按待定处理、不入账，这只是兜底，容差仍要设 0。
- 给面板单独建一个 BTCPay 商店，不要在同一个商店里再接 POS、Pay Button 或别的系统：那些发票的回调也会发到面板，面板找不到对应的支付会回 4xx / 5xx，BTCPay 会一直重投并把 Webhook 标成失败。面板入账时还会核对发票号必须是这张支付建单时存下的那张，别的发票即使金额和单号对得上也不会入账。
- CoinPayments 的回调地址随每张发票下发，不用在商户后台配。商户后台「接受部分付款」与「少付容差」请保持关闭（0）：开了以后少付的发票也会变成 completed，面板发现实收低于应收会按待定处理、不入账。

### 怎么确认

- 后台「订单与收款 › 订单」，找到这张单，看状态和支付尝试。
- 日志里找回调与查单：

  ```bash
  sudo grep -E '支付回调已处理|支付回调入账失败' /var/log/pandora/serve.log | grep '<订单号>' | tail
  sudo grep -E 'payment query patrol done|巡检查单未成|查单结果与支付意图金额不符' /var/log/pandora/worker.log | tail -n 5
  ```

- 库里看这张单和收到的回调：

  ```bash
  pg -v no='<订单号>' <<'SQL'
  SELECT order_no, status, total_amount, expires_at, paid_at FROM orders WHERE order_no = :'no';
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
3. **【写】** 商户密钥：在「订单与收款 › 支付渠道」里改，留空表示不改。
4. **【写】** 钱进了挂账：到「挂账」标签页核对后「转入余额」，要近期重认证。
5. 不要用 SQL 改订单、支付、账本表。

### 什么时候升级处理

同一个入账错误反复出现；「向渠道查单」报金额不符，而渠道后台显示金额是对的；账本对不上。带上订单号、`req_id` 和对应的日志行，提给开发。订单号和用户信息不要贴到公开的地方。

---

## 6. 通知积压

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
| 库慢 | 转第 7 章 |

收件人不能发、渠道没配置的会记成 `suppressed`，不是失败，也不算积压。

### 处理

1. 到后台通知渠道的设置里修好配置，用「测试发送」确认能发出去。
2. **【写】** 检查「安全与运维 › 降级开关」里的 `notify.email`。
3. 修好之后，排队中的会自动发完。已经 `failed` 的不会重发，不要直接改库。

### 什么时候升级处理

渠道测试发送成功，队列仍然不动：带上 `worker.log` 里那几行，提给开发。

---

## 7. 数据库或 Redis 连不上、连接满、慢

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
| PostgreSQL 单元挂了或反复重启 | `systemctl status postgresql@18-main`；日志里有 OOM 或磁盘满（转第 8 章） |
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

   只动确认过的那个 pid。不要终止迁移会话，处理迁移见第 10 章。
2. **【写】** 重启库：`systemctl restart postgresql@18-main`；重启 Redis：`systemctl restart redis-server`（AOF 落盘，会话与队列不丢）。期间整个面板不可用，serve 与 worker 会自动重连。
3. **【写】** 调连接预算：数据库参数改发布包里的 `deploy/postgresql-pandora.conf` 再重跑安装器（它整份覆盖机器上那份）；只改机器上那份，下次安装会被覆盖。

### 什么时候升级处理

同一条业务语句反复慢，或者保留期清理一直失败：带上 PostgreSQL 日志里那条语句（先去掉其中的个人信息）和库体积，提给开发。

---

## 8. 磁盘满

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

1. **【写】** 删旧的升级前备份：先确认有一份更新的 `pandora-postgres-*.dump.age` 通过了校验（第 9 章），异地也有副本，再删 `pre-upgrade-*.dump`。
2. **【写】** `journalctl --vacuum-size=200M`。
3. 不要手动删 PostgreSQL 数据目录里的任何文件，包括 `pg_wal`；也不要删 Redis 的 AOF 文件。
4. 库本身太大：确认 worker 在跑、`worker.log` 里没有清理失败；积压会在之后几轮里分批清掉。

### 什么时候升级处理

清理之后某张表仍然持续猛涨：带上上面的表大小列表，提给开发。

---

## 9. 备份、校验与恢复

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
- 磁盘满（第 8 章）。
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

## 10. 迁移失败

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
- 迁移半途失败、库的状态说不清时，从升级前的备份恢复（第 9 章），不要手工改 goose 的版本表。

### 什么时候升级处理

迁移本身报错（约束不满足、守卫拒绝），或 Down 被拒：带上完整输出和 `migrate status`，提给开发。

---

## 11. 节点机侧（pdnd）

面板这头看到的症状是「节点显示 降级 / 生效失败 / 等待证书」，原因在节点机上：端口被占、入站起不来、证书申请失败、UDP 缓冲偏小、接入装到一半。节点失联、心跳进不来看第 3 章。本章命令在节点机上以 root 跑，日志用 `journalctl -u pandora-native` 看。

### 怎么确认

节点自己会把运行状态随 gRPC 会话报给面板，状态有 `running`、`degraded` 和 `down`，原因是一个机器可读串：

| 原因（`runtime_reason`） | 后台节点列表显示 | 意思 |
|---|---|---|
| `port_in_use:<端口>/<tcp\|udp>:<节点 id>` | 生效失败：端口 … 被节点「…」占用 | 同一面板的另一个节点占着这个端口 |
| `port_in_use:<端口>/<tcp\|udp>:other` | 生效失败：端口 … 被本机其他服务占用 | 别的面板的节点，或本机其他进程 |
| `not_started` | 生效失败：入站没有起来 | 入站没起来（首个配置就没装上、回滚也失败，或刚重启） |
| `config_apply_failed` | 生效失败：新配置装不上，仍在用旧配置服务 | 新配置装不上，上一版仍在服务 |
| `serving_cached_config` | 降级：用缓存服务 | 面板还没确认，节点正用落盘缓存的配置服务（面板不可达时的正常降级，先看第 3 章） |
| `cert_pending` | 等待证书 | 自动申请的证书还没到：新节点不起入站，原来在服务的继续用旧配置（看 11.5） |
| `cert_expired` | 证书已过期 | 在服务的证书过期了（续期一直失败），入站照常开着但客户端校验会失败（看 11.5） |

库里看（只读）：

```bash
pg -c "SELECT name, runtime_status, runtime_reason, runtime_state_at FROM nodes WHERE runtime_status = 'degraded' ORDER BY runtime_state_at DESC LIMIT 50;"
```

节点机上：

```bash
journalctl -u pandora-native -n 200 --no-pager | grep -E '节点降级运行|新配置应用失败|入站已就绪|重试之前装不上的配置|UDP 缓冲偏小|证书'
ss -lntup | grep -E ':<端口>\b'     # 谁占着这个端口
```

### 11.1 端口被占

- **原因**：同一台机器上另一个入站、别的服务（nginx、旧的代理进程）已经 bind 了这个端口和协议（TCP 和 UDP 分开算）。后台有同机端口门禁：同一台服务器上两个节点配同一端口，保存时就会被拒；这条故障多半是机器上有后台管不到的进程。
- **处理**：
  1. `ss -lntup` 找出占用者。
  2. **【写】** 停掉那个进程，或在后台把这个节点的端口改到空闲的端口。
  3. 不用重启 pdnd：配置装不上时节点会自己按退避重试，端口释放后下一轮就会装上，原因随之清空。
  4. 想马上生效：**【写】** `systemctl restart pandora-native`，会断开这台节点上的用户连接。

### 11.2 入站起不来（`not_started` 或 `config_apply_failed`）

- **看日志里的 `err`**：`address already in use` 转 11.1；手填的证书读不进来，核对节点配置的 `cert_path` / `key_path` 与文件权限（`docs/node-certificates.md` 第 2 节）；自动申请的证书看 11.5；其它报错带着整段日志提给开发。
- **`新配置应用失败，已恢复上一版本`**：新版装不上，上一版在服务，用户不受影响。原因修好后自动重试，不用手动回滚。
- **面板明确拒绝这个节点（身份吊销、服务器被删）时**，节点不会用落盘缓存起服务：转第 3 章「身份失效」。

### 11.3 UDP 缓冲告警

日志里每个 hy2 / TUIC 入站启动时出一条「QUIC 入站的 UDP 缓冲偏小 …」。

- **原因**：Linux 缺省 `rmem_max` / `wmem_max` 约 208KB，quic-go 要 8MB 拿不到，单连接大流量时整批丢包。pdnd 以无特权用户运行，不能也不该给 unit 加 `CAP_NET_ADMIN` 去强制放大。
- 面板的节点安装脚本已经写了 `/etc/sysctl.d/90-pandora-native.conf`（只调大不调小）。看到告警说明这台是手工安装、装在容器里（`/proc/sys` 只读），或者 sysctl 在 pdnd 启动之后才生效。
- **处理**：`sysctl net.core.rmem_max net.core.wmem_max` 看当前值；**【写】** 照 `pdnd/release/README.md` 写 sysctl 并 `sysctl -p`，再 `systemctl restart pandora-native`；容器里在宿主机改。

### 11.4 接入装到一半卡住

后台服务器详情签发的一键安装令牌给出的命令是 `curl -fsSL '<面板地址>/pdnd/install.sh' | sudo bash -s -- --panel '<gRPC 主机>:9443' --ca-sha256 'sha256:<指纹>' --token '<令牌>'`。脚本的顺序是：校验参数 → 下载二进制并核对校验和 → 用新二进制 `pandora-native enroll`（按 CA 指纹钉住面板，令牌经 stdin 交给它，经 gRPC 换来客户端证书）→ 写配置与 unit → 校验 → 换上并重启服务。

- **接入之前失败**（参数、下载、校验和、`enroll` 被拒）：机器上原有的安装没动。网络中断造成的 `enroll` 失败，用同一条命令重跑即可（同一令牌原样重发，面板幂等地返回同一张证书）。令牌过期或已用过（面板日志 `接入被拒绝：令牌无效、已用或已过期`）就在后台重新签发。
- **接入之后失败**（校验不过、服务起不来）：新身份已经取代了旧身份。处理掉报错后，重新签发令牌重跑安装命令。
- **重装、升级**：直接用新令牌重跑安装命令，新身份取代旧身份。
- 指纹不符：命令里的 `--ca-sha256` 与面板 CA 不一致，或者 `--panel` 指到了别的服务，按后台给的命令原样复制。连不上 9443：防火墙放行，或 `config.yaml` 的 `grpc.node.public_host` 不是节点能连到的地址。
- 身份目录 `/var/lib/pandora-native/identity/` 属 `pandora` 用户、0700，里面的 `key.pem` 是私钥，不要贴出来。

### 11.5 节点证书申请失败（`cert_pending` 久不消失、`cert_expired`、仪表盘「证书异常」）

自动申请的证书由节点端自己向 Let's Encrypt 申请（`docs/node-certificates.md` 第 1 节），验证走 HTTP-01：Let's Encrypt 从公网连节点机的 80/TCP。

**怎么确认**

- 后台节点详情右侧「证书」一行：中文原因、Let's Encrypt 的原话、下次重试时间。原因码与下表对应。
- 库里看（只读）：

  ```bash
  pg -c "SELECT name, cert_identifier, cert_not_after, cert_error_code, cert_next_attempt_at, cert_state_at FROM nodes WHERE deleted_at IS NULL AND (cert_error_code IS NOT NULL OR cert_not_after < now() + interval '14 days') ORDER BY cert_not_after NULLS FIRST LIMIT 50;"
  ```

- 节点机上：`journalctl -u pandora-native -n 500 --no-pager | grep -E '证书申请或续期失败|证书已更新'`。失败的那行带原因码与原话。

| 原因码（`cert_error_code`） | 意思 | 处理 |
|---|---|---|
| `challenge_port_busy` | 节点机上别的程序占着 80 | `ss -lntp \| grep -E ':80\b'` 找出来停掉（nginx、caddy、面板等）。每 5 分钟自动重试一次，不消耗 Let's Encrypt 额度 |
| `validation_failed` | Let's Encrypt 连不上节点机的 80，或连到了别的机器 | 防火墙、云安全组放行入站 80/TCP；域名的 A / AAAA 记录要指向这台节点机（有 AAAA 时 IPv6 也要通）；前面有 CDN（如开了代理的 Cloudflare）的关掉代理 |
| `dns_failed` | 域名解析不到 | 补 A / AAAA 记录，等生效 |
| `caa_forbidden` | 域名的 CAA 记录不允许 Let's Encrypt | CAA 里加 `0 issue "letsencrypt.org"` |
| `rejected_identifier` | Let's Encrypt 拒签这个名字 | 名字是内网地址、保留域名或被封禁：换成公网域名或公网 IP（后台改 SNI 或节点地址） |
| `rate_limited` | 触发了 Let's Encrypt 的频率限制 | 节点端按 Let's Encrypt 给的时间自动重试（「下次重试」一栏）。常见原因是同一个名字一小时内验证失败太多次：先把上面的验证问题修好，别反复重启 |
| `paused` | 同一个名字连续失败太久，被 Let's Encrypt 暂停申请 | 先把验证问题修好，再打开原话（`cert_error_detail`）里 Let's Encrypt 给的解除暂停链接，按页面操作；解除后等下一次重试或重启 pdnd |
| `acme_unreachable` | 节点机连不上 Let's Encrypt | 节点机出站 443 被拦、DNS 坏了：`curl -sI https://acme-v02.api.letsencrypt.org/directory` |
| `account_failed` | 注册 ACME 账户失败 | 多半也是出站网络问题；持续出现就升级处理 |
| `store_failed` | 证书写盘失败 | `/var/lib/pandora-native/acme/` 的属主应为 `pandora`、0700；`df -h /var/lib` 看磁盘（第 8 章） |
| `profile_unsupported` / `unknown` | 其他 | 带上原话升级处理 |

**处理**

1. 按上表修好原因。
2. 不用手动重试：节点端按「下次重试」的时间自动再申请（15 分钟起、翻倍、最长 4 小时；频率限制按 Let's Encrypt 给的时间）。证书一到立刻装上入站，后台徽标随下一拍心跳消失。
3. 想马上重试：**【写】** `systemctl restart pandora-native`。重启会清掉退避计时、立即重新申请，也会断开这台节点上的用户连接。在频率限制或暂停期间重启没用，反而会多耗一次失败额度。
4. `cert_expired`：入站还开着，但客户端校验会失败，订阅里它已经排到后面、有别的节点时被摘掉。修好续期后自动恢复。急的话先在后台把节点改成手填证书（`docs/node-certificates.md` 第 2 节），或者先停用。
5. **不要**手动删 `/var/lib/pandora-native/acme/`：账户和证书会一起丢，重新申请会多耗额度。

### 什么时候升级处理

- 端口确实空闲，节点仍长时间报 `port_in_use` 或 `not_started`：带上节点 id、`ss -lntup` 输出和最近 200 行 `journalctl -u pandora-native`。
- 证书：80 已放行、解析正确，仍连续 24 小时报 `validation_failed`，或出现 `account_failed`、`profile_unsupported`、`unknown`：带上节点 id、证书名字、`cert_error_detail` 原话和含「证书」的日志行。
- 同一令牌重跑 `enroll` 仍然失败、或新令牌也被拒：带上安装脚本的完整输出（里面不含令牌）和面板 `serve.log` 里含这台服务器 id 的行。

---

## 12. 系统服务的约束

- `pandora-serve`、`pandora-worker` 以专用用户 `pandora` 跑，单元带 `MemoryMax` 与相应的 `GOMEMLIMIT`、常规沙箱加固；被 OOM 杀掉后 systemd 自己拉起。
- `pandora-backup`、`pandora-health`、`pandora-tls-renew` 以 root 跑，但都在沙箱里：备份只写备份目录与 WebDAV 检查点目录，巡检只写 `/var/log/pandora`，续期只写证书相关的目录。备份单元的系统调用过滤不许改文件属主，备份目录由安装器建成 `root:pandora` 0750（面板的系统状态页要列目录），文件本身都是 0600。
- PostgreSQL 与 Redis 的加固（systemd drop-in、Redis 配置块）由安装器写，开关是 `deploy.env` 的 `PANDORA_SYSTEMD_HARDENING`；被挡的系统调用一律返回 EPERM。想临时去掉某项排查：删掉对应的 drop-in，再 `systemctl daemon-reload && systemctl restart <单元>`，下次安装会再加回来；真有功能被挡住，带着 `journalctl -u <单元> -n 50` 报上来。

---

## 14. 订阅域名打不开

后台「节点与服务器 › 订阅下发」页的「订阅域名」管哪些用户拿到哪个域名的链接；nginx 只认 `/etc/pandora/deploy.env` 里 `PANDORA_SUB_DOMAINS` 登记过的订阅域名，且这些域名上只放行订阅拉取（`GET /<12位前缀>/<令牌>`）与门户探测接口 `/v1/sub-probe`（回 204），其余路径一律 404。两边要一致：后台填了、部署没登记 → 链接打不开。

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
6. **后台分配**：在面板后台「节点与服务器 › 订阅下发」中将域名加进对应的订阅域名组。

### 什么时候升级处理

- `deploy.env` 已正确配置、证书文件存在且 `nginx -t` 报错无法定位。
- 订阅链接持续返回 502（连不上 `pandora-serve`）或 503（面板依赖不可用）。带上 `pandora.conf`、`journalctl -u pandora-serve -n 50` 和 `curl -v` 输出提给开发。

---

## 15. 换主域名

后台「内容与外观 › 主题与插槽 › 站点设置」的「主域名」（`site.url`）只管面板拼出去的链接：邮件里的链接、支付回调与付完回跳、订阅链接（用户没分到订阅域名组时）、门户首页的 canonical、Telegram webhook、内容页里的 `{{siteUrl}}`。留空就用安装时的地址（`config.yaml` 的 `public_base_url`）。

它**不改** nginx、面板证书，也**不改**节点连面板的地址：这三样只看 `public_base_url`，`edge-tls.sh` 与 `render-nginx.sh` 也只给它生成 server 块。所以换域名的顺序是先让新域名能打开面板，再在后台改；不要为了换域名去改 `config.yaml` 的 `public_base_url`（已装的节点、节点接入命令和面板证书都跟着它）。

保存时面板会从本机请求一次 `https://<新域名>/v1/site-config`（5 秒、不跟随跳转），打不开本面板就拒绝保存（「这个地址打不开本面板」），什么都不改；改动记审计 `site.url_changed`（带新旧值）。

### 顺序

1. **DNS**：新域名的 A / AAAA 解析到面板服务器的公网 IP。确认：`dig +short <新域名>`。
2. **【写】nginx 先放出 80**（给 Let's Encrypt 的 HTTP-01 校验用；没有这一步，新域名的 80 会落到默认 server 回 444）。新建一个 `render-nginx.sh` 不管的文件 `pandora.site-domain.conf`（文件名必须排在 `pandora.conf` 之后：`conf.d/*.conf` 按文件名顺序加载，复制过去的 443 块引用 `pandora.conf` 顶部定义的 `log_format pandora_safe`，排在前面 `nginx -t` 会报 unknown log format，而且新域名会顶替成 443 的缺省 server），内容照 `/etc/nginx/conf.d/pandora.conf` 里面板地址那个 80 的 server 块原样复制，只把 `server_name` 换成新域名：

   ```bash
   sudo grep -n 'server_name' /etc/nginx/conf.d/pandora.conf     # 找到面板地址的 80 与 443 两个块
   sudoedit /etc/nginx/conf.d/pandora.site-domain.conf             # 粘进 80 块，server_name 改成新域名
   sudo nginx -t && sudo systemctl reload nginx
   curl -sI http://<新域名>/.well-known/acme-challenge/x           # 应回 404（不是超时，也不是 444 断开）
   ```

3. **【写】证书**：

   ```bash
   sudo certbot certonly --webroot -w /var/www/pandora-acme -d <新域名> --deploy-hook 'systemctl reload nginx'
   ```

   certbot 自带的 `certbot.timer` 会续期它；`edge-tls.sh` 只管面板地址的证书，不管这一张。
4. **【写】nginx 再放出 443**：把 `pandora.conf` 里面板地址那个 443 的 server 块也复制进 `pandora.site-domain.conf`，改三处：`server_name` 换成新域名；删掉 `listen 127.0.0.1:9080;` 那一行（本机运维入口只留一个）；`ssl_certificate` / `ssl_certificate_key` 换成 `/etc/letsencrypt/live/<新域名>/fullchain.pem` 与 `privkey.pem`。后台前缀等其余内容不动。

   ```bash
   sudo nginx -t && sudo systemctl reload nginx
   curl -s https://<新域名>/v1/site-config                          # 应回 {"registration_mode":…} 这样的 JSON
   ```

   这个文件是手工维护的：升级后 `pandora.conf` 由脚本重新渲染，这一份不会跟着变。升级后对照新的 `pandora.conf` 把两个块再复制一遍。
5. **【写】人机验证的域名**（后台开了 Turnstile 或 reCAPTCHA 才做）：先到 Cloudflare Turnstile 或 Google reCAPTCHA 的控制台，把新域名加进当前 site key 的域名列表，旧域名先不删。没加的话新域名上的门户拿不到验证令牌，注册、找回密码、邮件链接登录都过不去。
6. **【写】在后台改主域名**：填 `https://<新域名>`，保存时要重新输入密码。保存成功后新发的邮件、新建的支付单、门户里复制的链接都用新域名，立即生效（worker 里最多晚 30 秒）。
7. **旧域名至少再留 7 天**，DNS、nginx 与证书都不要动：
   - 改之前建的支付单，渠道仍按建单时的回调地址推回调（易支付、CoinPayments 的回调地址随单下发）。CoinPayments 的验签、付完回跳、人机验证的来源核对在改后 30 天内同时认新旧两个域名（只认上一个，30 天后旧的也不再认）。支付单 30 分钟就过期，连着换两次最多让 CoinPayments 的单晚一点入账（回调验签不过，由 worker 主动查单补记），两次隔开 1 小时以上就不受影响。
   - 旧域名下线以后，记得到人机验证控制台把它从 site key 的域名列表里删掉。
   - 已经发出的邮件里的链接（登录链接 15 分钟内有效）、用户收藏的旧地址、客户端里的旧订阅链接，都还指向旧域名。
   - BTCPay 的回调地址是在 BTCPay 商店里手工填的（第 5 章），不会自己变：要么在 BTCPay 商店「Settings › Webhooks」里改成新域名，要么旧域名一直留着。单独设了「回调地址」前缀的，回调不受主域名影响。
8. **Telegram**：保存主域名后，面板会用现有的 webhook 密钥把 Telegram webhook 自动重新注册到 `https://<新域名>/v1/webhooks/telegram`（只在 Telegram 已启用、注册过 webhook 时做；新域名不是 https 时不注册）。失败不影响主域名的保存。确认：

   ```bash
   sudo grep 'Telegram webhook' /var/log/pandora/serve.log | tail -n 3
   # 「已重新注册到新地址」= 成功；「重新注册失败」「没有重新注册」= 没成，后面带原因
   ```

   再用自己的 Telegram 私聊 bot 发 `/start`，有回复就通了。没成功时到后台「通知与插件 › 通知渠道」的 Telegram 卡片点一次保存，会按当前主域名重新注册（这次会换一个新的 webhook 密钥）。不要用 `curl` 带 Bot Token 去查 `getWebhookInfo`：Token 会进 shell 历史和 `ps`。重新注册在保存主域名的请求里同步做（最多等 15 秒）；正好同时有人保存了 Telegram 设置（会换 webhook 密钥）时，bot 可能收不到消息，再到 Telegram 卡片点一次保存即可。

### 改回去

后台把主域名清空保存，就恢复用 `public_base_url`（不做连通核对）。改回去之后同样把刚才那个域名留 7 天。

### 什么时候升级处理

- 新域名上 `curl -s https://<新域名>/v1/site-config` 回的是本面板的 JSON，后台保存仍然报「这个地址打不开本面板」：先看 `sudo grep '主域名连通核对' /var/log/pandora/serve.log | tail -n 5` 里的原因。面板是从本机去请求新域名的：本机的 DNS 要解析到公网地址（解析成内网或回环地址会被当成不安全的出站请求拦下）、出站防火墙要放行 443。仍查不出原因，带上这几行日志提给开发。

## 16. 数据巡检告警

`pandora-worker` 每 30 分钟（每小时第 19、49 分）跑一轮定时任务 `report:integrity`，逐项检查库里约束拦不住的跨表一致性。每项检查的最新结果写进表 `integrity_checks`，后台「仪表盘 › 要处理」读这张表：有违例时出第四格「数据巡检」，标红。告警发到 Telegram 管理员群组，文案是「数据巡检发现异常 / 检查：<中文名>（<代号>）/ 违例：N 条」，规则如下：

- 某项从 0 变成非 0 时发一条；违例数比这一段上次告警时多了，再发一条；数不变或变少不发。
- 回到 0 不发。之后再出现算新的一段，从头算起。
- 没开 Telegram 或没配管理员群组时不发。巡检结果照写，配好后下一轮照发。
- 告警只带检查名和违例数，样例 id 在 `integrity_checks.sample` 里，最多 5 个，新的在前。

这些检查出现违例，都说明有 bug，或有人绕过面板手改过库，而不是用户操作造成的。处理的先后：

1. 先用下文的只读 SQL 确认，并记下样例。
2. 用户受影响的（付了钱没拿到东西、钱被冻着），先用后台的现成操作把用户补上。
3. 要改库的，只照本章给的 SQL 做：整段放进一个事务，先查后改，每条 `UPDATE` 都带上刚查到的旧值当条件，回的行数不对就 `ROLLBACK`。改之前照「约定」先备份。改库的 SQL 块（以 `BEGIN;` 开头的）不要整段灌给 psql：先用不带参数的 `pg` 进交互会话，再逐条粘贴，看清每条的输出再往下。
4. 账本交易、分录、兑换流水、预留流水这类只追加的表，不删、不改。本章给不出 SQL 的，带上确认结果升级处理。

### 怎么确认

```bash
# 每项最近一次的结果：count 违例数、sample 样例 id、failing_since 这一段从什么时候开始
pg -c "SELECT code, count, sample, checked_at, failing_since, alerted_count FROM integrity_checks ORDER BY code;"
# 巡检本身有没有在跑、哪项失败（失败的那项保留上一次的结果，其余照跑）
sudo grep -h '数据巡检' /var/log/pandora/worker.log | tail -n 20
```

- `checked_at` 停在一小时以前，说明巡检没在跑，或这一项每轮都失败：先按第 1 章查 worker 与定时任务调度器。后台「要处理」的「数据巡检」格子在某项超过 2 小时没更新时也会标红、写「巡检没在跑」。
- 日志里有「数据巡检：检查失败」，多半是查询超时（应用账号每条语句限 15 秒）：按第 7 章查锁等待与慢语句。

下面每节的 `<…>` 换成 `sample` 里的 id。

### ledger_drift 账本漂移

**是什么。** 科目的余额缓存 `ledger_accounts.balance` 和这个科目全部分录的合计对不上。借方为正的科目是 `channel_cash`、`platform_fee_expense`、`platform_promo_expense`、`suspense`，余额 = 借 − 贷；其余科目余额 = 贷 − 借。样例是科目 id。后台概览接口（`GET v1/overview`）的 `ledger_drift_accounts` 是同一口径，但是现算的。

**确认。**

```bash
pg <<'SQL'
SELECT a.id, a.account_type, a.owner_user_id, a.owner_ref, a.balance AS cached,
       CASE WHEN a.account_type IN ('channel_cash', 'platform_fee_expense', 'platform_promo_expense', 'suspense')
            THEN coalesce(sum(e.amount) FILTER (WHERE e.direction = 'debit'), 0) - coalesce(sum(e.amount) FILTER (WHERE e.direction = 'credit'), 0)
            ELSE coalesce(sum(e.amount) FILTER (WHERE e.direction = 'credit'), 0) - coalesce(sum(e.amount) FILTER (WHERE e.direction = 'debit'), 0)
       END AS from_entries
  FROM ledger_accounts a LEFT JOIN ledger_entries e ON e.account_id = a.id
 WHERE a.id = '<科目 id>'
 GROUP BY a.id;
-- 这个科目最近的交易，找是哪一笔之后对不上的
SELECT t.occurred_at, t.kind, t.source_type, t.source_id, t.actor_kind, e.direction, e.amount
  FROM ledger_entries e JOIN ledger_transactions t ON t.id = e.transaction_id
 WHERE e.account_id = '<科目 id>'
 ORDER BY t.occurred_at DESC LIMIT 20;
SQL
```

**处理。** 分录才是账，余额缓存只是它的副本。没有后台操作能改缓存。

- 先弄清是谁把缓存改坏了：看审计日志，问有没有人手跑过 SQL。
- 确认分录是对的、只是缓存坏了，就**【写】**按分录校准缓存。校准不留审计，先把科目 id、`cached`、`from_entries` 和原因记进运维记录。用户类科目（`user_balance`、`user_commission_available` 等）`from_entries` 比 `cached` 小时，往下校准等于扣用户能用的钱，万一是分录缺了就扣错了：这种情况不自己校准，升级处理。行锁挡住同时记账。用户类科目算出来是负数时，`ledger_accounts_user_nonnegative` 会拒绝，说明分录本身有问题，这时升级处理。

```sql
BEGIN;
SET LOCAL lock_timeout = '5s';
SELECT id, account_type, balance FROM ledger_accounts WHERE id = '<科目 id>' FOR UPDATE;
-- 再跑一遍上面「确认」的第一条，核对 cached 与 from_entries；下面的 <cached> 填刚看到的 cached
UPDATE ledger_accounts a
   SET balance = CASE WHEN a.account_type IN ('channel_cash', 'platform_fee_expense', 'platform_promo_expense', 'suspense')
                      THEN s.d - s.c ELSE s.c - s.d END,
       updated_at = now()
  FROM (SELECT coalesce(sum(amount) FILTER (WHERE direction = 'debit'), 0) AS d,
               coalesce(sum(amount) FILTER (WHERE direction = 'credit'), 0) AS c
          FROM ledger_entries WHERE account_id = '<科目 id>') s
 WHERE a.id = '<科目 id>' AND a.balance = <cached>;
-- 应回 UPDATE 1；不是 1 就 ROLLBACK
COMMIT;
```

- 反过来，缓存是对的、分录缺了或多了：分录只追加，不能改也不能删。升级处理，带上面两条查询的结果。

**修好之后。** 再跑「确认」的第一条，`cached = from_entries`。下一轮巡检（30 分钟内）`ledger_drift` 回到 0。

### ledger_hold_mismatch 余额冻结对不上

**是什么。** 用户的冻结科目（`user_balance_hold`）余额，不等于他名下状态为 `held` 的冻结行（`balance_holds`）的金额合计。下单用余额抵扣时冻结，付款时扣款，取消或超时时释放，每一步都在同一笔交易里同时改冻结行和冻结科目。样例是用户 id。

**确认。**

```bash
pg <<'SQL'
SELECT a.balance AS hold_account,
       (SELECT coalesce(sum(amount), 0) FROM balance_holds WHERE user_id = a.owner_user_id AND status = 'held') AS held_rows
  FROM ledger_accounts a
 WHERE a.account_type = 'user_balance_hold' AND a.owner_user_id = '<用户 id>';
SELECT o.order_no, o.status AS order_status, h.amount, h.status AS hold_status, h.held_at, h.captured_at, h.released_at
  FROM balance_holds h JOIN orders o ON o.id = h.order_id
 WHERE h.user_id = '<用户 id>'
 ORDER BY h.held_at DESC LIMIT 20;
SQL
```

如果同一个科目也在 `ledger_drift` 的样例里，先按上一节处理漂移。冻结行是 `held`、订单却已经不是待支付的，和 `order_reservation_mismatch` 是同一个根因。

**处理。** 冻结行的扣款和释放交易号有外键，指向只追加的账本交易，手补就等于手记账，所以本节不给改库 SQL，也没有后台操作能单独改冻结。带上两条查询的结果和订单号升级处理。用户因此有钱用不了的，可以先在后台用户详情里「调整余额」临时补上，原因写明「数据巡检 ledger_hold_mismatch」。升级时写明补了多少，修复时要把这笔一起算进去（先冲回，或修复时不再退还），否则用户会拿到两份。

**修好之后。** `hold_account = held_rows`，下一轮巡检回到 0。

### payment_unsettled 收款了订单却没付

**是什么。** 近 7 天入账的收款（`payments`）既没进挂账（`late_payment_cases`），订单也不是 `paid` / `fulfilled`。正常情况下，收款入账和结算订单在同一个事务里：订单不能收时，会在同一事务里开挂账案，去「订单与收款 › 待处理到账」处理。样例是收款 id。

**确认。**

```bash
pg <<'SQL'
SELECT p.id, p.paid_at, p.amount, p.provider_payment_id, o.order_no, o.kind, o.status, o.user_id
  FROM payments p JOIN orders o ON o.id = p.order_id
 WHERE p.id = '<收款 id>';
SELECT count(*) AS late_cases FROM late_payment_cases WHERE payment_id = '<收款 id>';
SELECT t.kind, t.occurred_at FROM ledger_transactions t
 WHERE t.source_type = 'order' AND t.source_id = (SELECT order_id FROM payments WHERE id = '<收款 id>')
 ORDER BY t.occurred_at;
SQL
```

**处理。** 用户付了钱，没拿到东西。订单和收款表有守卫，不手改，先用后台把用户补上：

- 充值单：在用户详情里「调整余额」，补上 `amount`。
- 新购、续费、换套餐：订阅已经存在的，在用户详情里「加时长」；没有订阅的，用「订单与收款 › 订单」的「人工开单」开给他。

原因都写明「数据巡检 payment_unsettled 收款 <收款 id>」。不要对这张订单「标记已支付」：那会再记一笔收款，钱就重复了。补完之后，带上三条查询的结果升级处理，查结算为什么没落下来。升级时写明补了什么（余额金额、天数或开的单）和审计原因：开发修复时不再给用户发放，或先冲回这笔补偿，否则用户会拿到两份。

**修好之后。** 这一项只回看收款时间在 7 天以内的，补偿不会让计数回落，要等这笔收款满 7 天滑出窗口才回到 0。数不变不会重复告警，只有新增的才会再告警。红着的这段时间里，一条滑出窗口、同时新增一条，计数不变，也不会告警：每次看 `sample` 里有没有新的 id。

### order_paid_unfulfilled 已付订单没落地

**是什么。** 订单停在 `paid`。结算时先标 `paid`，在同一事务里接着履约（开通、续期、叠加、换套餐、重置流量、充值入余额），然后转成 `fulfilled`。提交以后还停在 `paid`，说明履约那一步被绕过了，或者只写了一半。样例是订单 id。

**确认。**

```bash
pg <<'SQL'
SELECT o.order_no, o.kind, o.status, o.user_id, o.subscription_id, o.total_amount, o.paid_at, o.fulfilled_at
  FROM orders o WHERE o.id = '<订单 id>';
SELECT kind, occurred_at FROM ledger_transactions
 WHERE source_type = 'order' AND source_id = '<订单 id>' ORDER BY occurred_at;
SQL
```

**处理。** 订单状态不手改，没有「重新履约」的操作。先按订单种类用后台补上用户：充值「调整余额」；有订阅的「加时长」；新购没开出订阅的「人工开单」。原因写明「数据巡检 order_paid_unfulfilled <订单号>」。然后带上两条查询的结果升级处理。升级时写明补了什么（余额金额、天数或开的单）和审计原因：开发修复时不再给用户发放，或先冲回这笔补偿，否则用户会拿到两份。

**修好之后。** 订单由开发修到 `fulfilled` 后，下一轮巡检回到 0。修之前一直显示同一个数，不会重复告警。

### order_reservation_mismatch 订单预留没释放

**是什么。** 订单预留（`order_reservations`，管券的占用与余额冻结）和订单状态脱节了，有两种情况：

- 预留还是 `held`，订单已经不是待支付：券一直占着，冻结的余额一直冻着。
- 待支付订单的预留不是 `held`。

建单、结算、取消、超时都在同一事务里同时改订单和预留。样例是订单 id。

**确认。**

```bash
pg <<'SQL'
SELECT o.order_no, o.status AS order_status, o.user_id, r.state AS reservation_state, r.expires_at, r.captured_at, r.released_at
  FROM orders o LEFT JOIN order_reservations r ON r.order_id = o.id
 WHERE o.id = '<订单 id>';
SELECT status, amount FROM balance_holds WHERE order_id = '<订单 id>';
SELECT status FROM coupon_redemptions WHERE order_id = '<订单 id>';
SQL
```

**处理。**

- 订单是待支付、预留不是 `held`：在「订单与收款 › 订单」里「取消订单」，原因写明「数据巡检 order_reservation_mismatch」。取消报错的，升级处理。
- 预留还是 `held`、订单已关闭：释放预留要同时记余额释放的账本交易，不手改。用户急着用钱的，先在用户详情里「调整余额」临时补上冻结的那部分，原因写明「数据巡检 order_reservation_mismatch <订单号>」，然后带上三条查询的结果升级处理。开发修复时释放冻结会把这笔钱退回余额：升级时写明补了多少，修复时先冲回这笔调整，否则用户会拿到两份。

**修好之后。** 订单与预留状态一致后，下一轮巡检回到 0。

### commission_unpaid_order 佣金挂在没付的订单上

**是什么。** 近 7 天产生、没作废的佣金条目，订单却不是 `paid` / `fulfilled`。佣金只在订单结算事务里计提，订单付了就不会再变回没付，所以这笔佣金不该存在。样例是佣金条目 id。

**确认。**

```bash
pg <<'SQL'
SELECT c.id, c.status, c.review_required, c.commission_amount, c.created_at, c.order_no, o.status AS order_status
  FROM commission_entries c JOIN orders o ON o.id = c.order_id
 WHERE c.id = '<条目 id>';
SQL
```

**处理。** 先作废这笔佣金，把它从推荐人的待结佣金冲回去：

- 条目是 `pending` 且已经待复核（`review_required = t`）：直接到「营销 › 佣金与提现 › 待复核」里点「作废」，原因写明「数据巡检：订单没付」。
- 条目是 `pending` 但还没待复核：先**【写】**把它转成待复核，再到同一页去作废。

```sql
BEGIN;
SET LOCAL lock_timeout = '5s';
SELECT id, status, review_required FROM commission_entries WHERE id = '<条目 id>' FOR UPDATE;
UPDATE commission_entries
   SET review_required = true, review_reason = '数据巡检：订单没付', updated_at = now()
 WHERE id = '<条目 id>' AND status = 'pending' AND NOT review_required;
-- 应回 UPDATE 1；不是 1 就 ROLLBACK
COMMIT;
```

- 条目已经是 `available`（解冻进了可提现）：作废不了，升级处理。

无论哪种，都带上确认结果升级处理，查订单为什么没付却计了佣金。

**修好之后。** 作废后这条不再算违例，下一轮巡检回落。

### giftcard_redemption_mismatch 礼品卡兑换对不上

**是什么。** 码和兑换流水对不上，有两种情况：

- 码标了 `used`，却没有兑换流水（`gift_card_redemptions`）。
- 有兑换流水，码却不是 `used`，或者码上的 `used_by` 不是兑换人。

兑换时，改码、发放、写流水在同一事务里。兑换流水只追加，以它为准。样例是码 id。

**确认。**

```bash
pg <<'SQL'
SELECT c.id, c.code_mask, c.status, c.used_by, c.used_at, r.user_id AS redeemed_by, r.redeemed_at,
       r.balance_amount, r.traffic_bytes, r.expire_days, r.plan_id, r.subscription_id
  FROM gift_card_codes c LEFT JOIN gift_card_redemptions r ON r.code_id = c.id
 WHERE c.id = '<码 id>';
-- 余额卡有没有真的入账
SELECT kind, occurred_at FROM ledger_transactions WHERE source_type = 'gift_card_code' AND source_id = '<码 id>';
-- 有没有走过兑换（审计与兑换同事务写；加时长、加流量、重置流量、套餐卡都不记账本，只能看这条和下一条）
SELECT occurred_at, actor_id FROM audit_events
 WHERE action = 'gift_card.redeemed' AND resource_type = 'gift_card_code' AND resource_id = '<码 id>';
-- 有没有落到订阅上（开通、续期、换套餐会带上码 id）
SELECT subscription_id, event_type, occurred_at FROM subscription_events WHERE payload->>'gift_card_code_id' = '<码 id>';
SQL
```

**处理。**

- 有兑换流水，码上的状态或兑换人不对：**【写】**按流水把码改回来。

```sql
BEGIN;
SET LOCAL lock_timeout = '5s';
SELECT c.id, c.status, c.used_by, r.user_id, r.redeemed_at
  FROM gift_card_codes c JOIN gift_card_redemptions r ON r.code_id = c.id
 WHERE c.id = '<码 id>' FOR UPDATE OF c;
UPDATE gift_card_codes c
   SET status = 'used', used_by = r.user_id, used_at = r.redeemed_at, updated_at = now()
  FROM gift_card_redemptions r
 WHERE c.id = '<码 id>' AND r.code_id = c.id
   AND (c.status <> 'used' OR c.used_by IS DISTINCT FROM r.user_id);
-- 应回 UPDATE 1；不是 1 就 ROLLBACK
COMMIT;
```

- 码标了 `used`、没有兑换流水，而且上面第二、三、四条**都**查不到：用户没拿到东西，**【写】**把码放回未使用，让用户重新兑换。只要有一条查得到，奖励可能已经发了（加时长、加流量、重置流量和套餐卡不记账本），放回去会让用户再拿一份：码保持 `used`，升级处理。

```sql
BEGIN;
SET LOCAL lock_timeout = '5s';
SELECT id, status, used_by, used_at FROM gift_card_codes WHERE id = '<码 id>' FOR UPDATE;
UPDATE gift_card_codes
   SET status = 'unused', used_by = NULL, used_at = NULL, updated_at = now()
 WHERE id = '<码 id>' AND status = 'used'
   AND NOT EXISTS (SELECT 1 FROM gift_card_redemptions WHERE code_id = '<码 id>')
   AND NOT EXISTS (SELECT 1 FROM ledger_transactions WHERE source_type = 'gift_card_code' AND source_id = '<码 id>')
   AND NOT EXISTS (SELECT 1 FROM audit_events
                    WHERE action = 'gift_card.redeemed' AND resource_type = 'gift_card_code' AND resource_id = '<码 id>')
   AND NOT EXISTS (SELECT 1 FROM subscription_events WHERE payload->>'gift_card_code_id' = '<码 id>');
-- 应回 UPDATE 1；不是 1 就 ROLLBACK
COMMIT;
```


**修好之后。** 下一轮巡检回到 0。

### subscription_expire_overdue 订阅到期没停

**是什么。** 订阅的到期时间已经过去 10 分钟以上，状态还是 `active`。到期扫描 `subscription:expire` 每分钟跑一轮，落后这么多，说明任务卡了：worker 没在跑、批次一直失败，或者有行被长期锁着。停发不靠它，节点名单和订阅拉取都按到期时间现算，但订阅状态、到期事件和到期通知都停在半截。样例是订阅 id。

**确认。**

```bash
pg -c "SELECT id, user_id, status, current_period_end, now() - current_period_end AS overdue FROM subscriptions WHERE id = '<订阅 id>';"
sudo grep -h 'subscription:expire' /var/log/pandora/worker.log | tail -n 20
# 有没有长时间持锁的会话
pg -c "SELECT pid, state, now() - xact_start AS xact_age, left(query, 80) FROM pg_stat_activity WHERE xact_start < now() - interval '5 minutes' ORDER BY xact_start;"
```

**处理。** 不改库。按第 1 章让 worker 和定时任务调度器恢复。日志里扫描一直报错的，按报错查；有长事务挡着的，按第 7 章处理。扫描恢复后，积压会自己追上。

**修好之后。** 订阅转为 `expired`，下一轮巡检回到 0。

### subscription_close_overdue 过期订阅没关续费窗口

**是什么。** 订阅已经过期，并且过了 30 天续费窗口再加 10 分钟，`renewal_closed_at` 还是空的。这一步也由到期扫描做：关续费窗口、吊销旧的订阅链接。样例是订阅 id。

**确认。**

```bash
pg -c "SELECT id, user_id, status, current_period_end, renewal_closed_at FROM subscriptions WHERE id = '<订阅 id>';"
sudo grep -h 'subscription:expire' /var/log/pandora/worker.log | tail -n 20
```

**处理。** 同上一节，不改库，让到期扫描恢复。

**修好之后。** `renewal_closed_at` 有值，下一轮巡检回到 0。

### 什么时候升级处理

- 本章写了「升级处理」的情况。
- 同一项刚修好，过几轮又出现。
- 某项检查连续报「检查失败」，第 7 章也查不出锁或慢语句。

升级时带上：

- `integrity_checks` 的那一行；
- 各节「确认」SQL 的输出；
- `worker.log` 里这段时间的「数据巡检」日志；
- 出现前后有没有人手改过库；
- 已经做的临时补偿：调了多少余额、加了几天、开了哪张单，各自的审计原因。

## 17. 节点在线升级与签名密钥

后台勾选服务器点「升级到 vX」后，面板把签过名的发布清单 `pdnd-manifest.json`、签名 `pdnd-manifest.json.sig` 和本机架构的下载地址发给节点。节点进程验签、下载、核摘要后放进暂存区，root 侧助手（`pandora-native-upgrade.path` 触发的 `pandora-native upgrade-apply`）用**已安装二进制自己内置的公钥**再验一遍，留 `.prev`、换上新版、重启 `pandora-native`，180 秒内新进程连不上面板就自动装回旧版。设计见 `docs/architecture.md` 11.6。

信任根是编进 pdnd 的公钥（`pdnd/upgrade/keys/*.pub`），不是面板：面板被攻破也签不出节点肯装的包。所以**签名私钥只放两处**：维护者的密码管理器，与 GitHub 仓库 environment `release-signing` 的 secret `PDND_SIGNING_KEY`。不放面板机、节点机、仓库、CI 日志，也不长期留在维护者的电脑上。

### 怎么确认

面板机上（只读）：

```bash
# 最近的升级任务：status 是 sent / staged / succeeded / failed / rolled_back / timed_out
pg -c "SELECT s.name, u.status, u.reason_code, u.detail, u.from_version, u.target_version, u.created_at, u.finished_at FROM server_upgrades u JOIN servers s ON s.id = u.server_id ORDER BY u.created_at DESC LIMIT 20;"
# 发布包里有没有清单与签名（缺任何一个，后台就不给在线升级）
ls -l /opt/pandora/pdnd-dist/pdnd-manifest.json /opt/pandora/pdnd-dist/pdnd-manifest.json.sig
```

节点机上（只读）：

```bash
systemctl status pandora-native-upgrade.path --no-pager      # 应是 active (waiting)
journalctl -u pandora-native-upgrade -n 100 --no-pager        # 助手每一步的日志
journalctl -u pandora-native -n 200 --no-pager | grep -E '升级|暂存'
ls -l /var/lib/pandora-native/upgrade/ /var/lib/pandora-native-upgrade/ /usr/local/bin/pandora-native*
sudo cat /var/lib/pandora-native/upgrade/result 2>/dev/null   # 失败或回退的结局，报给面板后会删掉
```

原因码（`reason_code`）：

| 码 | 意思 | 先查什么 |
|---|---|---|
| `download_failed` | 下载失败：非 200、超时、超过 128 MiB、地址不是 https | 节点机能不能访问面板的 `/pdnd/` 下载地址；面板的 nginx 日志 |
| `sha256_mismatch` | 下载到的二进制和清单里的摘要不符 | 面板机上 `pdnd-dist` 里的二进制是不是被换过，`sha256sum` 对照清单 |
| `signature_invalid` | 签名验不过，或 key_id 不在节点内置的公钥里 | 下文「轮换」有没有做反；这一批节点的二进制是不是还不认新公钥 |
| `manifest_invalid` | 清单坏了或没有这台机器的架构 | 发布包是不是完整 |
| `serial_not_newer` | 目标版本不比节点上的新（含 dev 构建，build_serial 为 0） | 只升不降，属正常拒绝 |
| `busy` | 上一次升级还没走完 | 等它结束，或看助手日志 |
| `apply_failed` | 替换二进制、写确认标记或重启失败，已保持或装回旧版 | 助手日志里的具体原因（磁盘满、权限） |
| `confirm_timeout` | 新版 180 秒内没连上面板，已装回旧版 | 新版起来后的 `journalctl -u pandora-native`：配置读不了、证书、网络 |
| `rollback_failed` | 要装回旧版但 `.prev` 不可用或恢复失败 | **要人工处理**，见下文「手动回退」 |

后台的 `timed_out`（面板 13 分钟内没等到结局）多半是节点在升级途中失联：上节点机看助手日志（`journalctl -u pandora-native-upgrade`）与 `result`。另有两种情况结局也到不了面板，以节点上的助手日志为准：会话中途报出的结局在断线时丢了（结局文件报出就删）；助手刚把暂存件拷走、还没记下这次升级时机器重启（现有安装没动）。下发后节点马上掉线的，任务一直显示「升级中」，要等它超时（约 13 分钟）才能再点升级。

### 17.1 生成签名密钥（首次）

在维护者自己的电脑上、pdnd 目录里：

```bash
cd pdnd
go run ./release/signmanifest -gen /tmp/pdnd-signing   # 写出 /tmp/pdnd-signing.key（0600）与 .pub，打印 key_id
```

1. **【写】** 把 `.key` 的内容存进密码管理器（注明 key_id 与生成日期）。
2. **【写】** GitHub 仓库 › Settings › Environments 新建 `release-signing`：Deployment branches and tags 限定只允许 `v*` 标签；Required reviewers 勾上维护者本人（否则任何能推 `v*` 标签的人都能拿私钥签名）；加 secret `PDND_SIGNING_KEY`，值是 `.key` 文件的那一行。
3. **【写】** 把 `.pub` 放进 `pdnd/upgrade/keys/`（文件名如 `release-<key_id>.pub`），提交合入：从这一版起编出的 pdnd 才认这把钥。
4. 删掉本机的 `.key`：`rm -P /tmp/pdnd-signing.key`（Linux 用 `shred -u`）。

`release.yml` 推标签发版时，只有 `pdnd` 这个 job 进 `release-signing`：它只装 Go、不装 npm 依赖，只构建节点端与清单，把 secret 写进 0600 临时文件签名、用完即删，再用仓库里的公钥核一遍（私钥与 `pdnd/upgrade/keys` 对不上就在这里失败）；没有这个 secret 发版直接失败。构建全量包的 job 拿不到私钥：它重新构建节点端，清单必须与签过名的那份逐字节相同才把 `.sig` 放进包里。本地跑 `build-release.sh` 只出清单、不出签名，这样的包不能用于在线升级。

### 17.2 轮换签名密钥

节点只认自己二进制里编进去的公钥，所以必须**先让节点认新钥，再换私钥**，顺序反了，所有节点都会回 `signature_invalid`：

1. 按 17.1 生成新的一对，新 `.key` 存进密码管理器（先不动 GitHub 的 secret）。
2. **【写】** 新 `.pub` 与旧 `.pub` 一起放在 `pdnd/upgrade/keys/`，用**旧私钥**签一次发版，把全部节点升到这一版。
3. 确认全部节点都在这一版上（后台服务器卡片的版本；没升上的节点不认新钥）。
4. **【写】** 把 GitHub secret `PDND_SIGNING_KEY` 换成新私钥。下一次发版就用新钥签。
5. **【写】** 再下一次发版时把旧 `.pub` 从 `keys/` 删掉；旧私钥在密码管理器里标作废。

私钥疑似泄露时一样按这个顺序，只是第 2 步发的那一版之后立刻做 4、5，并在这期间不在后台发起在线升级。

### 17.3 手动回退

助手报 `rollback_failed`，或升级成功后才发现新版有问题，在节点机上以 root：

```bash
systemctl stop pandora-native-upgrade.path pandora-native-upgrade.service   # 先停助手，免得它同时在动
sha256sum /usr/local/bin/pandora-native /usr/local/bin/pandora-native.prev  # 记下两者，确认 .prev 是想要的旧版
```

1. **【写】** 装回 `.prev`（会断开这台节点上的用户连接）：

   ```bash
   install -m 0755 -o root -g root /usr/local/bin/pandora-native.prev /usr/local/bin/pandora-native.new
   mv -f /usr/local/bin/pandora-native.new /usr/local/bin/pandora-native
   systemctl reset-failed pandora-native; systemctl restart pandora-native
   ```

   没有 `.prev`（或它也坏了）：在后台给这台服务器重新签发令牌，在节点机上重跑接入命令，装的是面板当前发布包里的版本。接入命令会先停助手、清掉进行中的升级状态（`result` 留着），助手不会再把 `.prev` 装回来盖掉它。
2. **【写】** 清掉半截的升级状态，再把助手打开：

   ```bash
   rm -f /var/lib/pandora-native-upgrade/*
   rm -f /var/lib/pandora-native/upgrade/{request,next,next.tmp,manifest.json,manifest.json.sig,applying,healthy}
   systemctl reset-failed pandora-native-upgrade.path pandora-native-upgrade.service   # 撞过触发上限的 path 单元不清掉就起不来
   systemctl start pandora-native-upgrade.path
   ```

   `result` 留着：节点连上面板后会把结局报上去再删。
3. 看 `journalctl -u pandora-native -n 50` 连上了面板，后台这台服务器的版本回到旧版。

`journalctl -u pandora-native-upgrade` 里看到 `pandora-native.prev` 执行失败、单元反复失败（被打断后续做交给 `.prev`，它坏了就续不下去），同样按上面的步骤手动回退，或重跑接入命令。

手动回退之后，同一个版本面板仍会认为「可升级」：先修好问题、出新版本再升，不要对同一版本反复点升级。

### 17.4 节点不支持在线升级

后台显示「不支持在线升级」，是节点没在 Hello 里声明 `upgrade_v1`。它要四项都满足：发布构建（不是本地编的 dev 版）、二进制里有公钥、装在 `/usr/local/bin/pandora-native`、装了 `/etc/systemd/system/pandora-native-upgrade.path`。`journalctl -u pandora-native | grep 不支持在线升级` 会打出缺的是哪一项。早于在线升级的安装少了助手单元：在后台给这台服务器重新签发令牌、在节点机上重跑接入命令，助手单元随之装上。

助手单元只由接入命令写，在线升级只换二进制：v2026.10.17 及更早装的节点，助手单元没有加固、被打断后续做也不交给 `.prev`。要换上新单元同样重跑接入命令，核对：`systemctl cat pandora-native-upgrade.service | grep pandora-native.prev` 有输出。

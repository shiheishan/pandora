# 排障手册

面向自己部署 Pandora Panel 的运维。按症状分章，每章四段：怎么确认 → 常见原因 → 处理 → 什么时候升级处理。

- 迁移失败、回滚、从备份恢复的完整步骤在 [MIGRATION-RUNBOOK.md](MIGRATION-RUNBOOK.md)，本手册第 11 章只做分诊，细节指过去。
- 节点证书的设计与 DNS 凭据怎么配，见 [docs/node-certificates.md](../../docs/node-certificates.md)；节点端的部署漂移，见 [pdnd/release/README.md](../../pdnd/release/README.md)「排查：节点不上报心跳」；节点机自身的症状（端口被占、入站起不来、UDP 缓冲告警、接入装到一半）见第 12 章。
- 文中的 `<面板地址>`、`<后台前缀>`、`<订单号>`、`example.com` 都是占位符，换成你自己的。

## 约定

**先只读，后动手。** 每章的「怎么确认」只看状态、日志和库，不改任何东西。「处理」里会改状态的步骤标了 **【写】**：

- 改库之前先备份：`cd /opt/pandora/deploy && ./backup-postgres.sh`，记下输出的 `backup complete: <路径>`。
- 先想清楚影响面：重启网关会断开正在进行的请求和节点事件流；重启 PostgreSQL 期间整个面板不可用。
- 不手改订单、支付、账本、审计这几类表。它们有守卫触发器和只追加约束，绕过去会让账对不上。

**口令不进命令行。** `.env` 只给 root 读，不要 `cat` 它；口令只经标准输入或 0600 文件传，不要写成命令参数（会进 `ps` 和 shell 历史）。

**路径。** 安装目录是 `/opt/pandora`，`deploy/` 下是备份、校验、恢复和巡检脚本。加密备份和升级前备份在 `/var/backups/pandora/`（`.env` 的 `AEGIS_BACKUP_DIR`）。进库（超级用户，不受行级安全限制）：`cd /opt/pandora/deploy && ./psql.sh`，它以 `postgres` 经 `127.0.0.1:POSTGRES_PORT` 连。PostgreSQL 与 Valkey 是系统服务：`postgresql@18-main` 和 `valkey-server`（Debian 12、Ubuntu 22.04 上是 `redis-server`）。

## 0. 先看全貌（只读）

```bash
systemctl --failed
systemctl status aegis-public aegis-admin aegis-node nginx --no-pager
# 三个网关的存活探针：都应是 200（9000 门户、9001 后台、9003 节点）
for p in 9000 9001 9003; do printf '%s ' "$p"; curl -s -o /dev/null -w '%{http_code}\n' -m 3 "http://127.0.0.1:$p/healthz"; done
# 就绪探针：门户查库和 Valkey，后台只查库；200 是 ok，503 是依赖不通
curl -s -m 5 http://127.0.0.1:9000/readyz; echo; curl -s -m 5 http://127.0.0.1:9001/readyz; echo
# 数据库与缓存：都应是 active (running)（Debian 12、Ubuntu 22.04 上 Valkey 的单元是 redis-server）
systemctl status postgresql@18-main valkey-server --no-pager
sudo /opt/pandora/deploy/edge-tls.sh status
```

日志在哪：

| 来源 | 位置 |
|---|---|
| 三个网关 | `/var/log/aegis/public.log`、`admin.log`、`node.log`（单元把标准输出追加到这里；`journalctl -u aegis-public` 只有启停记录） |
| nginx | `/var/log/nginx/aegis-access.log`（不记路径，只记方法、状态码、耗时）、`/var/log/nginx/aegis-error.log`（只记 crit） |
| PostgreSQL | `/var/log/postgresql/postgresql-18-main.log`：超过 500 ms 的语句、锁等待、检查点都会记；启停记录看 `journalctl -u postgresql@18-main` |
| Valkey | `journalctl -u valkey-server`（Debian 12、Ubuntu 22.04 是 `redis-server`）和发行版自己的日志文件（路径见配置文件里的 `logfile`） |
| 证书续期 | `journalctl -u aegis-tls-renew`、结论文件 `/var/lib/aegispanel/tls/status` |
| 备份 | `journalctl -u aegis-backup` |
| 节点端（节点机上） | `journalctl -u pandora-native` |

网关的 5xx 都记一条 `请求失败`，带 `request_id`、`path` 和内部原因；用户截图里的 `request_id` 直接拿来 grep：

```bash
grep -h '<request_id>' /var/log/aegis/*.log
```

后台「工作台 › 仪表盘」的系统状态卡片能看到：备份新鲜度、库体积与连接数、节点在线数、卡住的支付回调数、各通知渠道的排队与失败数。

## 巡检告警索引

`deploy/healthcheck.sh` 每类告警对应一章：

| 告警文案 | 去哪一章 |
|---|---|
| `服务 aegis-xxx 状态为 …` | [1. 面板 5xx / 网关起不来](#1-面板-5xx--网关起不来) |
| `public 端口 9000 探活返回 …`、`admin 端口 9001 探活返回 …` | [1](#1-面板-5xx--网关起不来) |
| `数据库连不上` | [8. 数据库连接满或慢](#8-数据库连接满或慢)（库进程本身挂了也在这章） |
| `/ 已用 N%`、`/tmp 已用 N%` | [9. 磁盘满](#9-磁盘满) |
| `备份目录 … 里没有任何备份`、`最新备份已经是 N 小时前的`、`最新备份只有 N 字节，疑似空文件` | [10. 备份没跑或异常](#10-备份没跑或异常) |
| `取不到 https://… 在用的证书`、`读不出 … 证书的有效期`、`… 的证书已过期`、`… 的证书还剩 N 小时到期`、`HTTPS 证书续期出错：…`、`证书续期 timer 超过 36 小时没跑` | [4. 面板 HTTPS 证书续期失败](#4-面板-https-证书续期失败) |
| `有 N 条通知排队超过 30 分钟没发出去`、`最近六小时有 N 条通知发送失败` | [7. 通知积压](#7-通知积压) |
| `过去 30 分钟没有任何节点上报心跳（7 天内曾有 N 个在报）` | [3. 节点离线或不上报心跳](#3-节点离线或不上报心跳) |

另外两个告警源也指到第 4 章：续期单元 `aegis-tls-renew.service` 记为 failed，以及 Telegram 上的 `潘多拉面板 HTTPS 证书（…）：…`。

**巡检随安装器装好。** `healthcheck.sh` 与 `aegis-health.service` / `aegis-health.timer` 在发布包里，`install.sh` 首装与升级都会装上并 `enable --now aegis-health.timer`。装在没有它的老版本机器上，升级一次即可；也可以手工补：

```bash
systemctl list-timers aegis-health.timer     # 看有没有在跑
# 手工补装（在发布包的目录里执行，用包里的 deploy/ 与 deploy/systemd/）：
install -m 0755 deploy/healthcheck.sh /opt/pandora/deploy/
install -m 0644 deploy/systemd/aegis-health.service deploy/systemd/aegis-health.timer /etc/systemd/system/
systemctl daemon-reload && systemctl enable --now aegis-health.timer
```

- 每 10 分钟跑一次，启用后 10 分钟首跑。没问题只往 `/opt/pandora/logs/health.log` 追一行 `OK`；有问题单元记为 failed，`.env` 设了 `AEGIS_ALERT_TG_TOKEN` 与 `AEGIS_ALERT_TG_CHAT` 时再推一条 Telegram。
- 脚本取安装目录自己的位置（`/opt/pandora`）；查库走 `deploy/psql.sh`。
- 首装后到配好备份之前，会有一条「备份目录里没有任何备份」的告警，这是真告警：去第 10 章把备份配好。

---

## 1. 面板 5xx / 网关起不来

### 怎么确认

- 先看状态码是谁回的：
  - **502**：nginx 连不上网关，进程没起来或卡死了。
  - **503**：两种可能。一是 nginx 限速，`limit_req` 超限默认回 503；二是网关自己说依赖不可用，例如就绪检查失败、登录口令计算排队排不上。
  - **504**：网关 20 秒内没回，多半是库慢，转第 8 章。
  - **500**：网关内部错误，响应体里有 `request_id`。
- 看进程和日志：

  ```bash
  systemctl status aegis-public --no-pager          # Active、最近一次退出码、有没有 oom-kill
  tail -n 100 /var/log/aegis/public.log | grep -E '启动失败|请求失败'
  grep -o 'status=5[0-9][0-9]' /var/log/nginx/aegis-access.log | sort | uniq -c
  ```

### 常见原因

| 现象 | 原因 |
|---|---|
| 日志末尾 `启动失败: …` | `.env` 配置不合法，原因写在冒号后面。生产模式（`AEGIS_ENV=production`）要求 `AEGIS_PUBLIC_BASE_URL` 是 `https://域名` 或 `https://公网IPv4`，不带端口和路径；设了 `AEGIS_ACME_DIRECTORY_OVERRIDE` 也会拒绝启动 |
| `启动失败: 缓存不可达` | Valkey 没起来。门户和后台两个网关启动时必须连上 Valkey；节点网关连不上只告警，照常启动 |
| `启动失败` 且原因是连库 | PostgreSQL 没起来或连接串不对，转第 8 章 |
| 状态是 `activating (auto-restart)`，退出码 209/STDOUT | `/var/log/aegis` 目录不存在 |
| `systemctl status` 里有 `oom-kill` | 撞了内存上限（门户、节点网关 256M，后台 384M），systemd 每 5 秒重启一次 |
| 进程 active，探活却超时 | 卡死。先抓现场（下面），再重启 |
| nginx 本身不在跑 | `nginx -t` 看配置；证书文件缺失见第 4 章 |

### 处理

1. 依赖先行：`systemctl status postgresql@18-main valkey-server --no-pager`（Debian 12、Ubuntu 22.04 上 Valkey 是 `redis-server`），两个单元应为 `active (running)`。
   - **【写】** 没起来：`systemctl start postgresql@18-main valkey-server`，起不来看第 8 章的日志位置。
2. 按日志里的原因改 `.env`。改之前先复制一份：`cp -p .env .env.bak.$(date +%s)`。
3. **【写】** `systemctl restart aegis-public`（或对应网关），再用第 0 节的探活确认。重启会断开正在进行的请求；节点网关重启时，节点事件流会在几秒内重连。
4. 卡死的进程，重启前先抓现场：`.env` 里设了 `AEGIS_*_PPROF_ADDR`（只接受回环地址）时，从那个端口取 goroutine 转储。

### 什么时候升级处理

依赖都正常、配置也没报错，500 仍然持续出现，或者进程反复 OOM：带上 `request_id`、对应的 `请求失败` 日志行（先去掉邮箱、IP 等个人信息）和版本号，提给开发。

---

## 2. 管理员进不来

### 怎么确认

按用户看到的现象对号：

| 现象 | 去看 |
|---|---|
| 后台地址打不开、404，或者落到了门户首页 | 前缀不对。`sudo /opt/pandora/deploy/admin-url.sh` 重新打印完整地址；地址要以 `/` 结尾，`/<后台前缀>` 会 301 到 `/<后台前缀>/` |
| 浏览器提示证书不安全 | 面板在用自签证书，转第 4 章 |
| 提示「邮箱或密码不正确」 | 下面的审计查询 |
| 429，或「请求太频繁」 | 登录限速：网关按「IP + 路径」每分钟 `AEGIS_RL_AUTH_PER_MIN` 次（缺省 10），nginx 另有每 IP 每分钟 12 次的边缘限速（超了回 503） |
| 能登录，保存时全部失败、提示只读 | 降级开关 `admin.writes` 被关了，到「系统 › 安全与运维 › 降级开关」打开 |
| 在门户用管理员账号登录被拒 | 设计如此：持有后台角色的账号不能登录门户，请走后台地址 |

「邮箱或密码不正确」对外是同一句话，背后有四种情况：账号不存在、口令错误、账号停用、账号没有后台角色。审计里记了真实原因（同一来源短时间内重复失败只记一条）：

```bash
cd /opt/pandora/deploy
./psql.sh -X -c "SELECT occurred_at, after_digest->>'reason' AS reason FROM audit_events WHERE action = 'user.login_failed' AND api_domain = 'admin' ORDER BY occurred_at DESC LIMIT 20;"
```

`reason` 的取值：`invalid_credentials`（不存在或口令错）、`account_inactive`（停用）、`no_admin_role`（没有后台角色）。

列出全部管理员及其状态、角色、最近登录（只读）：

```bash
cd /opt/pandora && ( set -a && . deploy/.env && set +a && ./bin/aegis-adminctl list )
```

### 常见原因

- 前缀记错或被改过（`.env` 的 `AEGIS_ADMIN_PATH`）。
- 站点在 Cloudflare 后面，但没配真实 IP 信任表：所有人共用 Cloudflare 的几个出口 IP，限速很快就满。见仓库 README「装完」一节的 `update-cloudflare-realip.sh`。
- 唯一的管理员忘了口令，或者角色被收回了。

### 处理

- 限速：等一分钟再试，不要清 Valkey 里的限速键。
- **【写】** 重置口令。会吊销这个账号的全部会话和刷新令牌，写审计 `adminctl.password_reset`。口令只经标准输入：

  ```bash
  cd /opt/pandora && ( set -a && . deploy/.env && set +a \
    && read -rsp '新密码：' p && echo && printf '%s\n' "$p" | ./bin/aegis-adminctl reset-password --email <管理员邮箱> --password-stdin; unset p )
  ```

  `reset-password` 只对有效的管理员。
- **【写】** 一个管理员都没有了（`aegis-adminctl has-admin` 退出 3；退出 1 是连不上库或配置错，不等于没有），或者账号被停用（`account_inactive`）：同样的写法，子命令换成 `create --email <邮箱> --password-stdin`，缺省角色 `platform_admin`。邮箱已存在时，它会重新激活账号、改口令、授角色，并吊销全部会话。
- **【写】** 角色被收回（`no_admin_role`）：`aegis-adminctl grant --email <邮箱> --role <角色码>`。角色码用 `aegis-adminctl roles` 查。

### 什么时候升级处理

审计里查不到对应的失败记录，而 `list` 显示账号正常、角色在：带上登录请求的 `request_id` 和 `admin.log` 里的对应行，提给开发。

---

## 3. 节点离线或不上报心跳

### 怎么确认

- 后台「网络 › 节点与服务器 › 节点」：心跳超过 90 秒的节点显示离线。
- 库里看心跳（只看在服和排空中的节点）：

  ```bash
  ./psql.sh -X -c "SELECT name, serving_status, runtime_status, last_heartbeat_at, now() - last_heartbeat_at AS ago FROM nodes WHERE serving_status IN ('active','draining') ORDER BY last_heartbeat_at NULLS FIRST LIMIT 50;"
  ```

- 面板这一侧，节点网关的验签日志（`reason` 说明了原因）：

  ```bash
  grep -E '节点请求验签失败|节点请求验签暂不可用' /var/log/aegis/node.log | tail -n 20
  ```

- 节点机上：

  ```bash
  systemctl status pandora-native --no-pager
  journalctl -u pandora-native -n 100 --no-pager
  pandora-native --version
  curl -sS -o /dev/null -w '%{http_code}\n' https://<面板地址>/healthz   # 从节点机测到面板的 HTTPS，应为 200
  ```

对用户的影响：

- 从没上报过心跳的节点不进订阅。
- 心跳超过 10 分钟的节点降级：同一份订阅里还有新鲜节点时不下发它，全都不新鲜时照常下发。
- 心跳只说明「节点和面板之间的链路」。节点内核可能照常在转发，所以离线不一定等于用户连不上。

### 常见原因

**全部节点同时掉线**（巡检告警就是这一种），先查面板：

| 原因 | 怎么认 |
|---|---|
| 节点网关 `aegis-node` 没在跑 | 第 0 节的 9003 探活不是 200，转第 1 章 |
| 面板证书过期，或者退回了自签 | `edge-tls.sh status`。节点用 HTTPS 接入时会校验证书，自签或过期都会失败，转第 4 章 |
| 库不可用 | 验签日志是 `节点请求验签暂不可用`，节点收到 503，转第 8 章 |
| nginx 没在跑，或者 80/443 被安全组挡了 | 从节点机 curl `/healthz` 超时 |

**个别节点掉线**，查节点：

| 原因 | 怎么认 |
|---|---|
| `pandora-native` 停了或反复崩溃 | `systemctl status` 与 `journalctl` |
| 后台显示「生效失败」「降级」，端口被占、入站没起来 | 节点在报心跳，只是入站有问题，见第 12 章 |
| 二进制太旧，或者 systemd 单元和仓库不一致 | 见 `pdnd/release/README.md`「排查：节点不上报心跳」：签名通道会悄悄降级成兼容通道 |
| 节点身份被吊销或节点被删 | 面板日志的 reason 是 `身份不存在或已吊销`，节点收到 401 |
| 有人在后台点过「重签服务端令牌」 | 节点还拿着旧令牌，请求被拒 |
| 节点机时钟偏差超过 5 分钟 | 面板日志的 reason 是 `时间戳超出允许窗口`；节点机上 `timedatectl` 看 `System clock synchronized` |
| 同一台机器跑了很多节点，或压测流量没带每节点的来源头 | nginx 对节点路径按「来源 IP + 节点」限速，超了回 503；access 日志里节点请求的 503 增多 |

### 处理

1. 面板侧原因按对应章节修。
2. **【写】** 节点进程：`systemctl restart pandora-native`。会断开这个节点上的用户连接。节点有落盘缓存，面板暂时不可达时重启也能用上一份配置和用户名单继续服务。
3. **【写】** 时钟：启用时间同步，例如 `timedatectl set-ntp true`，或者装 chrony。
4. **【写】** 单元漂移：按 `pdnd/release/README.md` 用仓库那份单元覆盖，修好目录权限。
5. **【写】** 身份被吊销：在后台这个节点的详情里点「签发一键安装令牌」，30 分钟内到节点机上执行生成的安装命令，重新注册。节点详情里还有「重签服务端令牌」：点过之后旧令牌立即失效，节点要换上新令牌才能继续上报。

### 什么时候升级处理

面板侧探活正常、节点进程在跑、时钟也对，心跳仍然进不来：带上节点 id、面板 `node.log` 里含这个 id 的行，以及节点上最近 100 行 `journalctl -u pandora-native`，提给开发。

---

## 4. 面板 HTTPS 证书续期失败

证书由 `deploy/edge-tls.sh` 管理，nginx 只读 `/etc/aegispanel/tls/live/`。`aegis-tls-renew.timer` 每天 03 点和 15 点各跑一次 `edge-tls.sh renew`（随机延后最多 1 小时），结论写进 `/var/lib/aegispanel/tls/status`。

### 怎么确认

```bash
sudo /opt/pandora/deploy/edge-tls.sh status     # 证书来源、到期时间、签发者、上次检查的结论
cat /var/lib/aegispanel/tls/status                  # RESULT=ok|warn|error，MESSAGE 是原因
systemctl status aegis-tls-renew.service --no-pager
systemctl list-timers aegis-tls-renew.timer
journalctl -u aegis-tls-renew -n 100 --no-pager
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
| `certbot 续期失败（报错见 journalctl -u aegis-tls-renew 与 /var/log/letsencrypt/letsencrypt.log）` | 看那两处日志 |
| 「续期校验」显示 `webroot 还指着别处` 或 `找不到续期配置` | certbot 的续期配置没走面板的校验目录 `/var/www/aegis-acme`，续期会 404。`renew` 每次会自动改正（改前备份到 `/var/backups/pandora/letsencrypt-renewal-*`），修不好的原因记进 status |
| `证书已更新但 nginx -t 不通过，没有 reload` | nginx 配置坏了，`nginx -t` 看哪一行 |
| `在用的证书不覆盖面板地址 …（对外地址改过？）` | `.env` 的 `AEGIS_PUBLIC_BASE_URL` 换了主机 |
| `证书还剩 N 小时到期，续期没成功`、`证书已过期` | 上面几种原因拖到了临界点 |
| 巡检：`取不到 https://… 在用的证书` | nginx 没在 443 上提供 HTTPS |
| 巡检：`证书续期 timer 超过 36 小时没跑` | timer 没启用，或者机器关过机 |

### 影响

证书过期或退回自签时：

- 浏览器提示不安全；
- 节点用 HTTPS 接入面板会校验失败，节点全部离线（第 3 章）；
- 校验证书的支付渠道可能推不进回调（第 6 章）。

### 处理

1. 先确认 80 端口从公网可达。在另一台机器上执行 `curl -sI http://<面板地址>/.well-known/acme-challenge/x`：应回 404。超时说明被挡了；308 说明 nginx 配置不是本面板的模板。
2. **【写】** 修好之后立即续一次。证书没换就不 reload：

   ```bash
   sudo systemctl start aegis-tls-renew.service      # 等价于 edge-tls.sh renew
   sudo /opt/pandora/deploy/edge-tls.sh status
   ```

3. **【写】** 来源是 `selfsigned`：`sudo /opt/pandora/deploy/edge-tls.sh issue`。
4. **【写】** timer 没启用：`systemctl enable --now aegis-tls-renew.timer`。
5. **【写】** 对外地址改过：`sudo /opt/pandora/deploy/edge-tls.sh setup`。它会重新渲染 nginx，`nginx -t` 不过就换回原配置。
6. 域名证书想先验证续期能不能成：`sudo certbot renew --dry-run --cert-name <域名>`。它走 Let's Encrypt 测试环境，不换证书。
7. 不要反复手动 `issue`。Let's Encrypt 有频率限制（同一 IP 或注册域名每 7 天 50 张，完全相同的域名组合每 7 天 5 张），撞上了只能等。

### 什么时候升级处理

80 从公网可达、域名解析也对，`lego` 或 `certbot` 仍然失败：带上 `journalctl -u aegis-tls-renew` 的输出（里面没有私钥）和 `edge-tls.sh status`，提给开发。

---

## 5. 节点证书签发失败（面板集中签发）

后台「网络 › 证书」签发的节点入站证书，由 aegis-admin 里的证书巡检每 30 秒推进一轮，走 DNS-01。设计与 DNS 凭据的最小权限配法见 `docs/node-certificates.md` 第 3 节。

> 签出的证书暂时只保存在面板里，还没有下发到节点。签发失败目前不影响正在运行的节点。

### 怎么确认

- 「网络 › 证书 › 证书列表」：状态（正常 / 排队中 / 签发中 / 签发失败 / 已暂停 / DNS 凭据失效）、最近错误、下次续期。点开一行能看到最近 50 次签发记录与错误详情。
- 「网络 › 证书 › DNS 凭据」：每个凭据的校验结论。
- 库里：

  ```bash
  ./psql.sh -X -c "SELECT name, status, paused_reason, consecutive_failures, next_attempt_at, last_error_code, left(last_error, 160) AS last_error, not_after FROM certificates ORDER BY updated_at DESC;"
  ./psql.sh -X -c "SELECT name, provider, zone, verify_status, verified_at, left(verify_error, 160) AS verify_error FROM dns_credentials ORDER BY name;"
  ```

- 日志：`grep -E '证书|ACME|lego' /var/log/aegis/admin.log | tail -n 30`。

### 常见原因（按错误码 `last_error_code`）

| 错误码 | 含义 | 面板怎么处理 |
|---|---|---|
| `credential_rejected` | 服务商明确拒绝了凭据：令牌被吊销、过期，或者看不到这个域名 | 证书停在「DNS 凭据失效」，不碰 CA。凭据改好、重新校验通过后自动恢复 |
| `dns_api_unavailable` | 服务商 API 连不上 | 15 分钟后再试。检查出口网络，以及 `HTTPS_PROXY` 一类环境变量 |
| `dns_provider_error` | 写 TXT 记录时服务商报错，多半是权限不够 | 按退避重试 |
| `dns_propagation_timeout` | TXT 记录写了，但查不到 | 用了 CNAME 委派时，检查 `_acme-challenge` 那条 CNAME（文档 3.3）；环境里不要有 `LEGO_DISABLE_CNAME_SUPPORT` |
| `rate_limited` | CA 回了 429 | 按 CA 给的 Retry-After 等，不计入连续失败 |
| `rate_limited_local` | 面板按自己的签发流水算出会超 Let's Encrypt 限额，没去请求 CA | 退避到最早那张满 7 天。证书剩不到 7 天、又配了 ZeroSSL 时，自动改走 ZeroSSL |
| `acme_account_error` | ACME 账号出错 | 账号被 CA 判失效时会标记失效，下一单重新注册。检查「ACME 设置」里的邮箱、EAB |
| `acme_error` | 其他 ACME 错误 | 看签发记录里的详情 |
| `interrupted` | 签发中途进程重启 | 租约过期后自动接着做 |

其他失败按 1 → 2 → 4 小时……最长 24 小时退避。连续失败 3 次暂停自动重试（状态「已暂停」，`paused_reason=failures`），避开 Let's Encrypt「每小时 5 次验证失败」的上限。

`admin.log` 里出现 `进程环境里设了 lego 自己会读的变量…`：从环境文件里删掉 `LEGO_DEBUG_*` 等变量，原因见文档 3.8。

### 处理（都在后台做，录入秘密和删除要近期重认证）

1. 凭据问题：「DNS 凭据」里编辑并「重新校验」。
2. **【写】** 修好原因之后，对「已暂停」的证书点「恢复」。没签出过的会立刻排单；已有版本的只清掉退避，到续期时间再续。
3. **【写】** 「立即续期」就是换私钥重签，每点一次都占一次 CA 额度。限流期间不要点。

### 什么时候升级处理

凭据校验通过、DNS 记录也查得到，`acme_error` 还反复出现：带上签发记录里的错误详情（不含秘密），提给开发。

---

## 6. 支付回调没到账 / 订单卡住

支付渠道把回调推到 `https://<面板地址>/v1/webhooks/payments/<渠道编码>`。回调丢了也有兜底：aegis-public 每分钟跑一轮主动查单，从下单 5 分钟后开始，按指数退避向渠道查，直到订单过期。查到已付就和回调走同一条路入账。

### 怎么确认

- 后台「商业 › 订单与收款 › 订单」，找到这张单，看状态和支付尝试。
- 门户日志里找这张单的回调（易支付的对外单号是订单号；同一单第二次及以后发起支付时是 `订单号-序号`）：

  ```bash
  grep -E '支付回调|主动查单' /var/log/aegis/public.log | grep '<订单号>' | tail
  grep -E '主动查单巡检' /var/log/aegis/public.log | tail -n 5
  ```

  | 日志 | 含义 |
  |---|---|
  | 一条都没有 | 回调根本没进来 |
  | `支付回调验签失败` | 进来了，签名对不上，没入账（这种回调不入库） |
  | `支付回调无法解析` | 渠道编码不对，或者报文格式认不出 |
  | `支付回调处理失败` | 验签过了，入账出错，`error` 写了原因；渠道会重推 |
  | `支付回调已处理 processed=true` | 已入账 |

- 库里看这张单、它的每次支付尝试和收到的回调：

  ```bash
  ./psql.sh -X -v no='<订单号>' <<'SQL'
  SELECT order_no, kind, status, total_amount, currency, expires_at, paid_at FROM orders WHERE order_no = :'no';
  SELECT i.provider_ref, i.status, i.amount, i.created_at, i.expires_at
    FROM payment_intents i JOIN orders o ON o.id = i.order_id WHERE o.order_no = :'no' ORDER BY i.created_at;
  SELECT received_at, event_type, processing_status, left(processing_error, 160) AS error
    FROM payment_events WHERE raw_payload->>'out_trade_no' LIKE :'no' || '%' ORDER BY received_at;
  SQL
  ```

  最后一条按易支付的报文字段查；别的渠道看 `raw_payload` 里对应的单号字段。
- 全局看有没有卡住的回调（收到超过 1 分钟还没处理）：

  ```bash
  ./psql.sh -X -c "SELECT processing_status, count(*), min(received_at) FROM payment_events WHERE processing_status IN ('pending','failed') AND received_at < now() - interval '1 minute' GROUP BY 1;"
  ```

### 常见原因

| 原因 | 怎么认 |
|---|---|
| 渠道连不上回调地址：证书自签或过期、防火墙或 WAF 拦截、对外地址改过 | 日志里一条回调都没有。从外网 `curl -sS -o /dev/null -w '%{http_code}\n' https://<面板地址>/v1/webhooks/payments/<渠道编码>` 应回 4xx（报文不全），超时或 TLS 报错就是这一类 |
| 商户密钥不对 | `支付回调验签失败` |
| 付款时订单已经取消或过期；一张单付了两笔 | 订单状态是 cancelled / expired，这笔钱进了「挂账」 |
| 金额或币种对不上 | `支付回调处理失败`，`error` 里写明 |
| 订单停在 processing、预留到期没释放 | 下一节的查询 |

批量看订单状态分布和几类「卡住」的单：

```bash
./psql.sh -X -c "SELECT status, count(*) FROM orders WHERE created_at > now() - interval '7 days' GROUP BY 1 ORDER BY 2 DESC;"
./psql.sh -X -c "SELECT order_no, status, created_at, expires_at FROM orders WHERE status = 'processing' AND created_at < now() - interval '30 minutes' ORDER BY created_at LIMIT 20;"
```

### 处理

1. 回调进不来的原因按上表修（证书见第 4 章）。修好之前，主动查单会继续补记。
2. **【写】** 订单抽屉里点「向渠道查单」。它只把渠道用商户密钥确认过的那笔记上，金额币种不符照样拒绝，可以重复点。
3. **【写】** 商户密钥：在「商业 › 订单与收款 › 支付渠道」里改，留空表示不改。
4. **【写】** 钱进了挂账：到「挂账」标签页核对后「转入余额」，要近期重认证。
5. **【写】** 「手工标记已支付」只用于线下确认到账、而渠道查单也查不到的情况：按应付金额入账、立即开通，写审计。先用第 2 步。
6. 不要用 SQL 改订单、支付、账本表。

### 什么时候升级处理

- 回调日志是 `支付回调处理失败`，同一个错误反复出现；
- 「向渠道查单」报金额不符，而渠道后台显示金额是对的；
- 账本对不上：已付订单没有分录、余额方向反了。

带上订单号、`request_id` 和对应的日志行，提给开发。订单号和用户信息不要贴到公开的地方。

---

## 7. 通知积压

业务里「决定要通知」只是往队列里插一行，aegis-public 每 30 秒派发一轮。失败按 2、4、8……分钟退避（最长 64 分钟），一共试 5 次，用完转成 `failed`，之后不再自动重发。

### 怎么确认

- 后台仪表盘的系统状态卡片：各渠道的排队、重试中、累计失败。
- 库里按渠道、状态和错误汇总：

  ```bash
  ./psql.sh -X -c "SELECT channel, status, count(*), min(created_at) AS oldest FROM notification_deliveries WHERE status IN ('queued','failed') GROUP BY 1, 2 ORDER BY 1, 2;"
  ./psql.sh -X -c "SELECT channel, left(error_message, 120) AS error, count(*) FROM notification_deliveries WHERE status IN ('queued','failed') AND error_message IS NOT NULL AND created_at > now() - interval '6 hours' GROUP BY 1, 2 ORDER BY 3 DESC LIMIT 10;"
  ```

- 日志：`grep -E '通知派发失败|通知投递失败|通知模板缺失' /var/log/aegis/public.log | tail`。

### 常见原因

| 原因 | 怎么认 |
|---|---|
| SMTP 不通、口令改了、发信被服务商限流 | 邮件渠道的 `error_message` |
| Telegram bot 令牌失效 | Telegram 渠道的 `error_message` |
| 降级开关 `notify.email` 关着 | 邮件只排队不发，这是开关的设计。打开后按原顺序继续发 |
| aegis-public 没在跑 | 派发器在门户网关里，转第 1 章 |
| 库慢 | 转第 8 章 |

`渠道未配置`、`用户未绑定 Telegram` 会记成 `suppressed`，不是失败，也不算积压。

### 处理

1. 到「运营 › 通知与插件 › 通知渠道」修好配置，用「测试发送」确认能发出去。
2. **【写】** 检查「系统 › 安全与运维 › 降级开关」里的 `notify.email`。
3. 修好之后，排队中的会自动发完，不用做别的。已经 `failed` 的不会重发，产品里也没有重发入口；确实需要重发，提给开发评估，不要直接改库。

### 什么时候升级处理

渠道测试发送成功，队列仍然不动，`通知派发失败` 持续出现：带上那几行日志，提给开发。

---

## 8. 数据库连接满或慢

### 怎么确认

- 现象：第 0 节的就绪探针回 503；网关日志里有 `too many clients`（SQLSTATE 53300）、`canceling statement due to statement timeout`（57014）或 `context deadline exceeded`；nginx 有 504。
- 库进程本身：

  ```bash
  systemctl status postgresql@18-main --no-pager              # 应为 active (running)
  tail -n 50 /var/log/postgresql/postgresql-18-main.log       # 慢语句（>500 ms）、锁等待、OOM、磁盘满
  journalctl -u postgresql@18-main --since '30 min ago' --no-pager   # 启停与被 systemd 杀掉的记录
  ```

- 连接与长事务：

  ```bash
  ./psql.sh -X -c "SELECT usename, state, count(*) FROM pg_stat_activity WHERE backend_type = 'client backend' GROUP BY 1, 2 ORDER BY 3 DESC;"
  ./psql.sh -X -c "SHOW max_connections;"
  ./psql.sh -X -c "SELECT pid, usename, state, now() - xact_start AS xact_age, wait_event_type, left(query, 80) AS query FROM pg_stat_activity WHERE xact_start < now() - interval '1 minute' ORDER BY xact_start;"
  ./psql.sh -X -c "SELECT pid, pg_blocking_pids(pid) AS blocked_by, left(query, 80) AS query FROM pg_stat_activity WHERE cardinality(pg_blocking_pids(pid)) > 0;"
  ```

连接预算：`/etc/postgresql/18/main/conf.d/pandora.conf` 里 `max_connections=60` = 3 条超级用户保留 + 11 条维护余量 + 三个网关各自的池上限（缺省门户 16 = 15 + 1 条常驻监听、后台 15、节点 14）+ 节点纪元探针池外专用的 1 条连接，合计 3 + 11 + 16 + 15 + 14 + 1 = 60。缺省值与算式以 `internal/platform/config/runtime.go` 为准（`.env.example` 的连接池注释同源）。

### 常见原因

| 原因 | 怎么认 |
|---|---|
| PostgreSQL 单元挂了或反复重启 | `systemctl status postgresql@18-main`；日志里有 OOM（drop-in 限了 `MemoryMax=512M`、不许换出，见下一条与第 13 章）或磁盘满（转第 9 章） |
| 进程被 OOM 杀掉 | 见本表下面「不换出到硬盘的代价」 |
| 某个会话 `idle in transaction`，或者手工跑的语句占着锁 | 第三、四条查询 |
| 改大了 `AEGIS_*_DB_MAX_CONNS`，或者多开了网关实例，总数超出预算 | 第一条查询各用户的连接数 |
| 保留期清理积压，表越来越大、查询越来越慢 | `admin.log` 里有 `…清理失败` |
| 某条业务查询慢 | PostgreSQL 日志里同一条语句反复超过 500 ms |

**不换出到硬盘的代价。** PostgreSQL、Valkey 与三个网关的单元都带 `MemorySwapMax=0`（第 13 章）：内存紧的时候它们不会被换出去变慢，代价是内存真用尽（撞上单元的 `MemoryMax`，或整机没内存）时，由「变慢」变成「进程被 OOM 杀掉」。怎么看：

```bash
systemctl status aegis-public aegis-admin aegis-node postgresql@18-main valkey-server --no-pager | grep -iE 'oom|Result|Active'
journalctl -k --since -1h | grep -iE 'oom|killed process'            # 内核记的是哪个进程、哪个 cgroup
journalctl -u aegis-public --since -1h | grep -iE 'oom-kill|Main process exited'
```

- 网关（`Restart=on-failure`）与 Valkey（发行版单元 `Restart=always`）被杀后由 systemd 自己拉起；Valkey 不落盘，限流计数与实时推送的临时状态清零。
- PostgreSQL：drop-in 写了 `OOMPolicy=continue`，被杀的多半是某个后端连接，单元不停，postmaster 自己做崩溃恢复（断开所有连接、重放 WAL、重新接客，几秒钟，期间网关的请求会失败一下）；postmaster 本身受 `OOMScoreAdjust=-900` 保护，真被杀了或异常退出，drop-in 的 `Restart=on-failure` 在 5 秒后拉起。日志里是 `server process (PID …) was terminated by signal 9: Killed` 与 `all server processes terminated; reinitializing`。不写 `OOMPolicy=continue` 时 systemd 缺省会因为一个后端被杀停掉整个单元，库就一直停着。
- 反复被杀：先看是哪一个占得多（`systemctl status` 的 Memory 行、`ps -o rss`），再按第 8 章看连接数与慢查询；不要把 `MemorySwapMax` 去掉了事，那只是把问题换成卡顿。
- 内核 cgroup 不是 v2 或没开 swap 记账时这一条不生效，安装输出会说。


### 处理

1. **【写】** 先取消、再终止。取消只停当前语句，终止会断开连接、回滚它的事务：

   ```bash
   ./psql.sh -X -c "SELECT pg_cancel_backend(<pid>);"
   ./psql.sh -X -c "SELECT pg_terminate_backend(<pid>);"   # 取消不管用时
   ```

   只动确认过的那个 pid。不要终止迁移会话（`goose`），处理迁移见第 11 章。
2. **【写】** 重启库：`systemctl restart postgresql@18-main`。期间整个面板不可用，网关会自动重连。
3. **【写】** 调连接预算：改 `.env` 的 `AEGIS_*_DB_MAX_CONNS`，按 `.env.example` 的算式重排，再重启对应网关。改 `max_connections` 等数据库参数要改发布包里的 `deploy/postgresql-pandora.conf` 再重跑安装器：它整份覆盖 `/etc/postgresql/18/main/conf.d/pandora.conf`，文件变了才重启 PostgreSQL；只改机器上那份，下次安装会被覆盖。
4. 要看语句的累计耗时，需要 `pg_stat_statements`。用压测脚本 `tools/loadtest/scripts/pgstat.sh`（`enable --yes` / `disable --yes`）：它用 `ALTER SYSTEM` 把 `shared_preload_libraries` 写进数据目录的 `postgresql.auto.conf` 并重启库、建扩展，只在原值为空时才写。不改 `postgresql-pandora.conf`，因为 `conf.d/pandora.conf` 每次安装整份覆盖，而 `postgresql.auto.conf` 的优先级更高、重装不会冲掉它，所以开着的状态会一直留到 `disable` 为止。重启库期间网关连库失败几秒，先评估停机窗口。

### 什么时候升级处理

同一条业务语句反复慢，或者保留期清理一直失败：带上 PostgreSQL 日志里那条语句（先去掉其中的个人信息）和库体积，提给开发。

---

## 9. 磁盘满

### 怎么确认

```bash
df -h / /tmp
du -xsh /var/log/aegis /var/log/nginx /var/log/postgresql /var/backups/pandora /var/lib/postgresql 2>/dev/null
journalctl --disk-usage
./psql.sh -X -c "SELECT pg_size_pretty(pg_database_size(current_database()));"
./psql.sh -X -c "SELECT relname, pg_size_pretty(pg_total_relation_size(relid)) AS size FROM pg_catalog.pg_statio_user_tables ORDER BY pg_total_relation_size(relid) DESC LIMIT 15;"
```

后果：库写不进去，所有写操作失败，PostgreSQL 可能直接停掉；备份、证书续期也会失败。

### 常见原因

- **备份堆积。** `backup-postgres.sh` 只按 `AEGIS_BACKUP_RETENTION_DAYS`（缺省 14 天）清理自己生成的 `aegis-postgres-*.dump.age`。`install.sh` 升级前留的 `pre-upgrade-*.dump`、回滚前导出的 `rollback-*` 目录都不会自动清。
- **`/tmp` 满。** 它常是一块小的 tmpfs，在机器上构建会把它塞满；根分区却还有空间。
- **日志。** 网关日志每天轮转、单文件超过 64M 也轮转，保留 14 份；nginx、journal、PostgreSQL 与 Valkey 的日志按发行版设置。
- **库变大。** 节点上报的在线记录、探针点、流量汇总靠 aegis-admin 的保留期清理（每 10 分钟一轮）。后台网关停了很久，这些表会一直涨。
- **迁移预检。** 升级前的预检在同一个 PostgreSQL 里克隆整库，要有和库差不多大的空闲空间。

### 处理

1. **【写】** 删旧的升级前备份：先确认有一份更新的 `aegis-postgres-*.dump.age` 通过了校验（第 10 章），异地也有副本，再删 `pre-upgrade-*.dump`。
2. **【写】** `journalctl --vacuum-size=200M`。
3. 不要手动删 PostgreSQL 数据目录里的任何文件，包括 `pg_wal`。
4. 库本身太大：确认 aegis-admin 在跑，`admin.log` 里没有 `…清理失败`；积压会在之后几轮里分批清掉。

### 什么时候升级处理

清理之后某张表仍然持续猛涨：带上上面的表大小列表，提给开发。

---

## 10. 备份没跑或异常

### 怎么确认

```bash
systemctl status aegis-backup.service --no-pager
systemctl list-timers aegis-backup.timer               # 每天 03:17 UTC 附近
journalctl -u aegis-backup -n 100 --no-pager
ls -lt /var/backups/pandora | head
```

后台仪表盘的系统状态卡片也会显示：最新一份的时间、缺校验文件的份数、解密私钥有没有配好。

### 常见原因

- **timer 从没启用过。** `install.sh` 装了 `aegis-backup.service/.timer`，但不替你启用；新装的机器上「一份备份都没有」多半是这个。
- `.env` 的 `AEGIS_BACKUP_AGE_RECIPIENT` 还是占位符。脚本拒绝生成明文备份，直接失败。
- `postgresql@18-main` 没在跑，或 `.env` 的 `POSTGRES_SUPER_PASSWORD` 与库里 `postgres` 的口令对不上（报 `password authentication failed`）。
- 磁盘满（第 9 章）。
- 远端上传钩子（`AEGIS_BACKUP_REMOTE_HOOK`）失败，日志里能看到。

### 处理

1. 按日志里的原因修 `.env` 或环境。
2. **【写】** 启用每日备份：`systemctl enable --now aegis-backup.timer`。
3. **【写】** 立即补一份：`systemctl start aegis-backup.service`，再看 `journalctl -u aegis-backup`。
4. 校验最新一份。要在 `deploy/` 目录下以 root 跑，它读同目录的 `.env`（要求属 root、0600 或 0400）：

   ```bash
   cd /opt/pandora/deploy && ./verify-backup.sh /var/backups/pandora/aegis-postgres-<时间>.dump.age
   ```

   - 它先核这份备份的来路，再核 sha256、解密和归档目录，通过时输出 `backup verified:`。来路两种：
     - 本机写的备份：`backup-postgres.sh` 每份都写一个封条 `aegis-postgres-<时间>.dump.age.seal`，是用这台安装的 age 解密私钥派生的签名。留存期内哪一份都能核、能恢复；
     - 从 WebDAV 取回的备份：签名清单 `aegis-postgres-<时间>.manifest.json` 加 WebDAV 之外的可信检查点，只认最新一份（防替换、防回滚）。
   - 两样都没有报 `refusing an archive of unknown origin`；封条对不上报 `local backup seal verification failed`：这份不是本安装写出的，或者归档、校验文件被改过，不要恢复它。
   - 封条用的就是解密私钥：换过私钥（或机器重装后没用回原来那把）的备份核不过。所以私钥要另存（见第 5 条），重装后放回原处。
   - 设 `AEGIS_VERIFY_RESTORE=1` 会在临时库里完整恢复一遍，需要和库差不多大的空闲磁盘。
5. 解密私钥（`.env` 的 `AEGIS_BACKUP_AGE_IDENTITY`）缺省和备份在同一台机器上（`/opt/pandora/secrets/backup-age.key`）。机器整体丢失时备份就解不开，私钥要另外保存一份。
6. 从备份恢复见 `MIGRATION-RUNBOOK.md` 第 3 节。

### 什么时候升级处理

备份能生成，但 `verify-backup.sh` 校验不过。

---

## 11. 迁移失败或卡在预检

`install.sh` 升级和发布控制器的迁移顺序都是：停服前查无效索引 → 停服前在克隆库上预检 → 停服 → 迁移只核对预检凭据。

### 怎么确认

| 输出 | 意思 | 去哪 |
|---|---|---|
| `停服前的迁移预检没通过：服务没停，数据库没动` | 索引检查或预检失败，线上没受影响 | 本章下面 |
| `迁移失败…服务已拉回原来的版本` | 迁移失败，可能已提交了一部分 | `MIGRATION-RUNBOOK.md` 第 1 节 |
| 发布控制器的 `rollback=…` | 各种结论 | `MIGRATION-RUNBOOK.md` 第 0 节的对照表 |

查当前版本：`MIGRATION-RUNBOOK.md` 第 0 节的 `migrate.sh version` / `status`。

### 预检阶段的常见报错

| 报错 | 原因与处理 |
|---|---|
| `migration: INVALID indexes found …` | 上次 `CREATE INDEX CONCURRENTLY` 中途失败留下的无效索引。这时重跑会被 `IF NOT EXISTS` 跳过、却记成已执行，所以直接拒绝。输出里给了每个索引的 `DROP INDEX CONCURRENTLY` 语句；清理步骤见 `MIGRATION-RUNBOOK.md` 第 1 节第 5 步与第 4.3 节 |
| `migration precheck: active legacy renewals=N; release refused` | 有旧式在途续费单。没付款的走正常的取消流程；处理中或已付的逐单核对，不要用 SQL 改 |
| `migration precheck: release migration artifact is older than source` | 装的发布包比库里的版本旧，换成新的发布包 |
| `migration precheck: attestation …; production is unchanged, rerun the full precheck` | 停服后的凭据核对不过（中间有人迁移过、迁移目录变了、凭据超过六小时等），服务已自动拉回。重新发版即可 |

单独查无效索引（只读；退出码 0 没有、1 有、2 查不了）：

```bash
cd /opt/pandora/deploy
PANDORA_LOCAL_MIGRATION_APPROVED=yes GOOSE_BIN=/opt/pandora/bin/goose ./migrate.sh check-indexes
```

**预检很慢：** 克隆和演练的耗时随库大小增长（5k 规模约 15–21 秒）。它在业务时段读整库，要有和库差不多大的空闲磁盘（第 9 章）。大库请挑低峰发版。

### 处理

- 一律先按报错修好原因，再重跑安装脚本或发布控制器。
- 迁移失败后的三条出路（前滚、`rollback-to`、从备份恢复）及其前提，只看 `MIGRATION-RUNBOOK.md`，不要在这里另找办法。

### 什么时候升级处理

- 迁移本身报错，例如约束不满足、守卫拒绝（P0001）；
- `rollback-to` 被 Down 守卫拒绝。

带上完整输出和 `migrate.sh version`，提给开发。

---

## 12. 节点机侧（pdnd）

面板这头看到的症状是「节点显示 降级 / 生效失败」，原因在节点机上：端口被占、入站起不来、UDP 缓冲偏小、接入装到一半。节点失联、心跳进不来看第 3 章。本章命令在节点机上以 root 跑，日志用 `journalctl -u pandora-native` 看。

### 怎么确认

节点自己会把运行状态报给面板（随签名心跳或兼容通道的 `/status`），状态只有 `running` 和 `degraded`，原因是一个机器可读串：

| 原因（`runtime_reason`） | 后台节点列表显示 | 意思 |
|---|---|---|
| `port_in_use:<端口>/<tcp\|udp>:<节点 id>` | 生效失败：端口 … 被节点「…」占用 | 同一面板的另一个节点占着这个端口 |
| `port_in_use:<端口>/<tcp\|udp>:other` | 生效失败：端口 … 被本机其他服务占用 | 别的面板的节点，或本机其他进程 |
| `not_started` | 生效失败：入站没有起来 | 入站没起来（首个配置就没装上、回滚也失败，或刚重启） |
| `config_apply_failed` | 生效失败：新配置装不上，仍在用旧配置服务 | 新配置装不上，上一版仍在服务 |
| `serving_cached_config` | 降级：用缓存服务 | 面板还没确认，节点正用落盘缓存的配置服务（面板不可达时的正常降级，先看第 3 章） |

库里看（`./psql.sh`，只读）：

```bash
./psql.sh -X -c "SELECT name, runtime_status, runtime_reason, runtime_state_at FROM nodes WHERE runtime_status = 'degraded' ORDER BY runtime_state_at DESC LIMIT 50;"
```

最近一条生效回执的阶段（`failed`、`precheck_failed`、`health_failed`、`rolled_back` 都算失败）在表 `node_config_applications` 的 `phase` 列，后台节点详情里也能看到。

节点机上：

```bash
journalctl -u pandora-native -n 200 --no-pager | grep -E '节点降级运行|新配置应用失败|入站已就绪|重试之前装不上的配置|UDP 缓冲偏小'
ss -lntup | grep -E ':<端口>\b'     # 谁占着这个端口
```

### 12.1 端口被占

- **原因**：
  - 同一台机器上另一个 pdnd 入站、别的服务（nginx、旧的代理进程）已经 bind 了这个端口和协议（TCP 和 UDP 分开算）。原因里带节点 id 的是本面板的另一个节点；`other` 是本机其他进程或别的面板的节点，节点端不会写出别的面板的任何信息。
  - 后台有同机端口门禁：同一台服务器上两个节点配同一端口，保存时就会被拒；这条故障多半是机器上有后台管不到的进程。
- **处理**：
  1. `ss -lntup` 找出占用者。
  2. **【写】** 停掉那个进程，或在后台把这个节点的端口改到空闲的端口（同机端口先到先得）。
  3. 不用重启 pdnd：配置装不上时节点会自己重试。旧配置仍在服务时按 1、2、4 分钟退避、封顶 5 分钟；入站没起来（`not_started`）时每个拉取间隔重试一次。端口释放后下一轮重试就会装上，原因随之清空。
  4. 想马上生效：**【写】** `systemctl restart pandora-native`，会断开这台节点上的用户连接。

### 12.2 入站起不来（`not_started` 或 `config_apply_failed`）

- **看日志里的 `err`**：
  - `address already in use`：转 12.1。
  - 证书读不进来（file 模式证书的路径必须是绝对路径，且在 `/etc/pandora-native/certs` 之下，文件属主要让 `pandora` 读得到）：入站在校验阶段就失败；证书由面板集中签发的，转第 5 章。
  - 其它报错：带着整段日志提给开发。
- **`新配置应用失败，已恢复上一版本`**：新版装不上，上一版在服务，用户不受影响。原因修好后自动重试（见 12.1 第 3 点），不用手动回滚。
- **订阅里会被降级**：端口被占、入站没起来、期望版本生效失败的节点（面板的 `RuntimeFailingSQL` 口径），套餐里有别的可用节点时不下发它，没有才下发。
- **面板明确拒绝这个节点（身份吊销、节点被删）时**，节点不会用落盘缓存起服务，日志是 `面板明确拒绝了这个节点，不用落盘缓存起服务`：转第 3 章「身份被吊销」。

### 12.3 UDP 缓冲告警

日志里每个 hy2 / TUIC 入站启动时出一条：

```
QUIC 入站的 UDP 缓冲偏小（UDP 转发的出站 socket 受同一上限），单连接大流量会丢包；请把 sysctl net.core.rmem_max 与 net.core.wmem_max 设到 8388608 以上后重启 pdnd …
```

- **原因**：Linux 缺省 `rmem_max` / `wmem_max` 约 208KB（Debian 13 是 212992），quic-go 要 8MB 拿不到，单连接几百 Mbps 时一个调度停顿就灌满缓冲、整批丢包，客户端看到的是限速与重传。pdnd 以无特权用户运行，不能也不该给 unit 加 `CAP_NET_ADMIN` 去强制放大。
- **面板的节点安装脚本已经处理**：它写 `/etc/sysctl.d/90-pandora-native.conf`（`rmem_max`、`wmem_max` 各 16777216，只调大不调小）。看到告警说明这台是手工安装、装在容器里（`/proc/sys` 只读），或者 sysctl 在 pdnd 启动之后才生效。
- **处理**：
  1. `sysctl net.core.rmem_max net.core.wmem_max` 看当前值。
  2. **【写】** 手工安装的照 `pdnd/release/README.md`「systemd 冷启动」那段写 `/etc/sysctl.d/90-pandora-native.conf` 并 `sysctl -p`。
  3. **【写】** `systemctl restart pandora-native`：缓冲是入站 bind 时按当时的上限要的，改了 sysctl 要重启才生效。
  4. 容器里 `/proc/sys` 改不了：在宿主机改。

### 12.4 接入装到一半卡住

一键安装脚本的接入是三步：`enrollment begin`（在节点机生成身份、向面板申请）→ 写配置与 unit → `enrollment commit`（带二进制、配置、unit 的哈希交给面板）。中途断了，节点机上留一个日志文件 `/etc/pandora-native/identity.json.enrollment.pending.json`，面板上这次接入的状态是 `pending`、`committed`、`expired`、`aborted` 之一，节点在 `bootstrapping` 或 `bootstrap_failed`。

先看状态（节点机，root）：

```bash
pandora-native enrollment status --identity /etc/pandora-native/identity.json
```

它向面板问一次并打印 `enrollment state: …`；`committed` 时顺手把待定身份转正为 `identity.json`（写文件）。

| 状态 | 意思 | 处理 |
|---|---|---|
| `committed` | 面板已收下，只是本机没装完（commit 的响应丢了、之后一步失败） | **前滚，不要 abort**：重跑安装命令；令牌过期就在后台节点详情重新「签发一键安装令牌」。已有身份时脚本会沿用，不会再注册一次 |
| `pending` 且没过期 | 在等 commit，面板上这次接入的期限是 15 分钟与令牌有效期中较短的那个 | 重跑安装命令；确定不要了就 abort（下一行） |
| `expired` 或 `aborted` | 已经作废（`expired` 是超过了期限） | 日志文件是作废的，重跑安装命令之前先把它挪走：**【写】** `mv /etc/pandora-native/identity.json.enrollment.pending.json /root/`，再在后台签发新令牌，用新令牌重装。不挪的话 `enrollment commit` 会报 `enrollment is not pending`，换了节点名重装则报 `existing enrollment journal belongs to a different request` |

确定放弃一次还没 commit 的接入：

```bash
pandora-native enrollment abort --identity /etc/pandora-native/identity.json --reason "<原因>"
```

- **【写】** abort 只用于 begin 之后、commit 之前。面板上这次接入变 `aborted`，节点从 `bootstrapping` 回到 `bootstrap_failed`，可以重新签发令牌再装。
- 已经 `committed` 的接入 abort 会被面板拒绝（`terminal enrollment cannot be aborted`），这时走前滚。
- 安装脚本自己的失败处理：commit 之前出错，它会自己 abort 并把二进制、配置、unit 回滚到上一版；一旦面板已经 commit（或日志里记了 `commit_attempted`），它保留现场、提示按前滚恢复，不会回滚。
- 令牌错误：begin 被拒时屏幕上是 `enrollment begin rejected (HTTP 401)`，面板的原因是 `bootstrap token is invalid or expired`（令牌过期、已用完，或 `--name` 与签发令牌时填的节点名不一致）；`node is not in an enrollable lifecycle state`（409）是节点不在 `draft`、`provisioning`、`bootstrap_failed` 这几种可接入的状态。都在后台重新签发令牌。
- 身份文件的路径以安装器写的 `/etc/pandora-native/identity.json` 为准，二进制的缺省路径（`/var/lib/pandora-native/identity.json`）不是它。

### 什么时候升级处理

- 端口确实空闲，节点仍长时间报 `port_in_use` 或 `not_started`：带上节点 id、`ss -lntup` 输出和最近 200 行 `journalctl -u pandora-native`。
- 接入卡在 `committed` 与本机状态对不上、`enrollment status` 本身报错：带上它的完整输出和面板 `node.log` 里含这个节点 id 的行（日志文件里有私钥和运行令牌，不要贴）。

---

## 13. PostgreSQL 与 Valkey 的加固

PostgreSQL 与 Valkey 是发行版包装的系统服务。`install.sh` 用 systemd drop-in 与 Valkey 配置块把它们收紧（首装、升级都会做；内容没变不重启）。这一步在外来集群检查、迁移、收窄运行角色都做完之后、起网关之前：升级时网关在迁移前就停了，第一次加固重启 PostgreSQL 与 Valkey 只发生在这段停服窗口里，多几秒，不打断在线请求（Valkey 不落盘，限流计数与实时推送的临时状态会清零，与重启网关相同）。

本章不是排障，是说明这两个服务被收紧成什么样、起不来时怎么办。数据库参数（`max_connections` 等）不在这里，在 `/etc/postgresql/18/main/conf.d/pandora.conf`，改法见第 8 章。

每份 drop-in 分两段：资源约束一直在（`MemoryMax`；`MemorySwapMax=0`，不换出到硬盘，代价见第 8 章「不换出到硬盘的代价」），它们只要 cgroup、不要挂载命名空间，所以不随加固开关去掉；隔离段随开关 `PANDORA_SYSTEMD_HARDENING`。三个网关的单元自己带 `MemorySwapMax=0`。

- PostgreSQL：`/etc/systemd/system/postgresql@18-main.service.d/pandora.conf`；
- Valkey / Redis：`/etc/systemd/system/valkey-server.service.d/pandora.conf`（或 `redis-server.service.d`），外加配置文件（`/etc/valkey/valkey.conf` 或 `/etc/redis/redis.conf`）末尾 `# >>> pandora` 到 `# <<< pandora` 的一块。

| 项 | Debian 包缺省 | 加固后 |
|---|---|---|
| PG 监听 | `listen_addresses=localhost`；pg_hba 本地 peer、回环 scram | 不变。另加 `IPAddressDeny=any` 与 `IPAddressAllow=localhost`，有人把 `listen_addresses` 改成 `*` 外面也连不进 |
| PG 运行用户 | 单元以 root 起，由 pg_ctlcluster 降权，能力集完整 | `User=postgres`、能力集清空、`NoNewPrivileges` |
| PG 隔离 | 只有 `OOMScoreAdjust` | 补齐。`ProtectSystem=strict`（只放行数据、日志、socket 目录）；`PrivateTmp`、`PrivateDevices`、`ProtectHome`；`ProtectKernel*`、`ProtectProc=invisible`；`RestrictNamespaces`；系统调用拒绝清单；`MemoryMax=512M`。不加 `MemoryDenyWriteExecute`，因为超级用户会话可能用 JIT |
| PG 文件与口令 | 数据目录 700；超级用户口令只在 `.env`（0600） | 不变 |
| Valkey 监听 | 只听回环、`protected-mode yes`、有口令 | 不变。配置块把这两行钉住（bind 按版本写：Valkey 与 Redis 6.2 起 `127.0.0.1 -::1`；Ubuntu 22.04 的 Redis 6.0 不认「-」，回环有 IPv6 写 `127.0.0.1 ::1`，没有只写 `127.0.0.1`），drop-in 只放行回环 |
| Valkey 运行用户 | 单元 `User=valkey`（或 `redis`）。Debian 12 的 redis 单元留着 SETUID、SETGID、SYS_RESOURCE 能力 | 用户不变。能力集一律清空 |
| Valkey 隔离 | 看发行版：Debian 13 的 valkey 单元已经较严；Debian 12 的 redis 单元末尾把 `ProtectSystem` 改回 `true`，也没有系统调用过滤。**都缺**进程可见性、网络出口、内存上限 | drop-in 整套写全，不依赖发行版单元：`ProtectSystem=strict`、`PrivateTmp/Devices`、`ProtectProc=invisible`、`MemoryDenyWriteExecute`；系统调用允许清单 `@system-service` 去掉 `@privileged @resources`；只放行回环；`MemoryMax=160M` |
| Valkey 行为 | 危险命令可用，定期写 `dump.rdb`，没有内存上限 | 配置块补齐：禁 `FLUSHALL` 与 `FLUSHDB`；不落盘；`maxmemory 96mb allkeys-lru` |
| Valkey 口令 | `valkey.conf`（640）的 `requirepass` | 不变，口令只经 stdin 与环境写入 |

被挡的系统调用一律返回 EPERM，不会直接杀掉进程。

**起不来怎么办**：

- PostgreSQL 重启前先做一次 `CHECKPOINT` 并计时，等它在线的时长按这次实测给（30 秒加检查点耗时的三倍），慢盘不会被误判。
- 带着新 drop-in 起不来：drop-in 还原成这次之前的样子（之前有的写回，没有的删掉），再起、再核实在线。Valkey 分口令、配置块、drop-in 三步，每步改之前存一份配置，哪步起不来只撤回那一步。
- 撤回后核实服务在跑：首装与升级把服务按新版本起来之后再停下（退出码非 0），提示原因。撤回后仍没起来就照实说、停下。
- 原因是 `226/NAMESPACE`（LXC、OpenVZ 一类容器化 VPS 不支持 systemd 沙箱要的挂载命名空间）时，提示会直接说。

**开关 `PANDORA_SYSTEMD_HARDENING`**：缺省 1（开）。确认主机不支持沙箱、或者 18/main 要给别的用途（见下），用 `PANDORA_SYSTEMD_HARDENING=0` 重跑安装器：两份 drop-in 只留资源约束、去掉隔离段，记进 `.env`（只改这一行），之后的升级沿用；改回 1 再跑一次就恢复。Valkey 的配置块不受开关影响（那是命令与持久化行为，不是沙箱）。关掉后只剩 Debian 包缺省的隔离，安装器每次都会提醒。

**drop-in 作用于整个 PostgreSQL 18/main，不只是面板的库**：

- `IPAddressDeny=any` 加 `IPAddressAllow=localhost`：集群只接受回环连接。以后给它配远程备库（流复制）、从别的机器跑 `pg_basebackup` 或 `pg_dump`，都会连不上（不是密码错，是包被丢）。
- `MemoryMax=512M`：集群全部后端共用这个上限，别的库的负载也算在里面。
- 要这样用，先评估，再用开关关掉，或者自己另写 drop-in 覆盖这两项（文件名排在 `pandora.conf` 之后，如 `zz-local.conf`；安装器只管自己那个文件）。

想临时去掉某项排查：删掉对应的 drop-in，再 `systemctl daemon-reload && systemctl restart <单元>`。下次安装会再加回来（要长期关掉用开关）；真有扩展或功能被挡住，带着 `journalctl -u <单元> -n 50` 报上来。

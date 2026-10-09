# 排障手册

面向自己部署 Pandora Panel 的运维。按症状分章，每章四段：怎么确认 → 常见原因 → 处理 → 什么时候升级处理。

- 迁移失败、回滚、从备份恢复的完整步骤在 [MIGRATION-RUNBOOK.md](MIGRATION-RUNBOOK.md)，本手册第 11 章只做分诊，细节指过去。
- 节点证书的设计与 DNS 凭据怎么配，见 [docs/node-certificates.md](../../docs/node-certificates.md)；节点端的部署漂移，见 [pdnd/release/README.md](../../pdnd/release/README.md)「排查：节点不上报心跳」。
- 文中的 `<面板地址>`、`<后台前缀>`、`<订单号>`、`example.com` 都是占位符，换成你自己的。

## 约定

**先只读，后动手。** 每章的「怎么确认」只看状态、日志和库，不改任何东西。「处理」里会改状态的步骤标了 **【写】**：

- 改库之前先备份：`cd /opt/aegispanel/deploy && ./backup-postgres.sh`，记下输出的 `backup complete: <路径>`。
- 先想清楚影响面：重启网关会断开正在进行的请求和节点事件流；重启 PostgreSQL 期间整个面板不可用。
- 不手改订单、支付、账本、审计这几类表。它们有守卫触发器和只追加约束，绕过去会让账对不上。

**口令不进命令行。** `.env` 只给 root 读，不要 `cat` 它；口令只经标准输入或 0600 文件传，不要写成命令参数（会进 `ps` 和 shell 历史）。

**两种安装布局。** 下文命令按 `install.sh` 的布局写，直装机器按下表替换：

| | `install.sh`（Docker 数据基座） | `install-native.sh`（直装） |
|---|---|---|
| 安装目录 | `/opt/aegispanel` | `/opt/pandora` |
| 进库（超级用户，不受行级安全限制） | `cd /opt/aegispanel/deploy && ./psql.sh` | `runuser -u postgres -- psql -p <POSTGRES_PORT> -d <POSTGRES_DB>`（两个值在 `.env` 里） |
| PostgreSQL / Valkey | 容器 `aegis-postgres` / `aegis-valkey`（`docker ps`、`docker logs`） | systemd 单元 `postgresql@<版本>-main`、`valkey-server`（或 `redis-server`） |
| `deploy/` 下的备份、校验、恢复脚本与 `psql.sh` | 有 | 没有；升级前备份在 `/var/backups/pandora/` |

## 0. 先看全貌（只读）

```bash
systemctl --failed
systemctl status aegis-public aegis-admin aegis-node nginx --no-pager
# 三个网关的存活探针：都应是 200（9000 门户、9001 后台、9003 节点）
for p in 9000 9001 9003; do printf '%s ' "$p"; curl -s -o /dev/null -w '%{http_code}\n' -m 3 "http://127.0.0.1:$p/healthz"; done
# 就绪探针：门户查库和 Valkey，后台只查库；200 是 ok，503 是依赖不通
curl -s -m 5 http://127.0.0.1:9000/readyz; echo; curl -s -m 5 http://127.0.0.1:9001/readyz; echo
docker ps -a --filter name=aegis-
sudo /opt/aegispanel/deploy/edge-tls.sh status
```

日志在哪：

| 来源 | 位置 |
|---|---|
| 三个网关 | `/var/log/aegis/public.log`、`admin.log`、`node.log`（单元把标准输出追加到这里；`journalctl -u aegis-public` 只有启停记录） |
| nginx | `/var/log/nginx/aegis-access.log`（不记路径，只记方法、状态码、耗时）、`/var/log/nginx/aegis-error.log`（只记 crit） |
| PostgreSQL | `docker logs aegis-postgres`：超过 500 ms 的语句和锁等待都会记 |
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

**巡检要自己装。** `healthcheck.sh` 与 `aegis-health.service` / `aegis-health.timer` 目前不在发布包里，安装器也不装。要用就从源码树 `panel/deploy/` 拷过去：

```bash
install -m 0755 healthcheck.sh /opt/aegispanel/deploy/
install -m 0644 aegis-health.service aegis-health.timer /etc/systemd/system/
install -d /opt/aegispanel/logs          # health.log 写在这里
systemctl daemon-reload && systemctl enable --now aegis-health.timer
```

- 每 10 分钟跑一次。没问题只往 `/opt/aegispanel/logs/health.log` 追一行 `OK`；有问题单元记为 failed，`.env` 设了 `AEGIS_ALERT_TG_TOKEN` 与 `AEGIS_ALERT_TG_CHAT` 时再推一条 Telegram。
- 它写死了 `/opt/aegispanel` 和 Docker 版的 `psql.sh`，只适合 `install.sh` 布局。直装机器上，数据库类检查会误报「数据库连不上」。

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

1. 依赖先行：`docker ps -a --filter name=aegis-`，两个容器应为 `Up (healthy)`。
   - **【写】** 容器没起来：`cd /opt/aegispanel/deploy && docker compose up -d`。
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
| 后台地址打不开、404，或者落到了门户首页 | 前缀不对。`sudo /opt/aegispanel/deploy/admin-url.sh` 重新打印完整地址；地址要以 `/` 结尾，`/<后台前缀>` 会 301 到 `/<后台前缀>/` |
| 浏览器提示证书不安全 | 面板在用自签证书，转第 4 章 |
| 提示「邮箱或密码不正确」 | 下面的审计查询 |
| 429，或「请求太频繁」 | 登录限速：网关按「IP + 路径」每分钟 `AEGIS_RL_AUTH_PER_MIN` 次（缺省 10），nginx 另有每 IP 每分钟 12 次的边缘限速（超了回 503） |
| 能登录，保存时全部失败、提示只读 | 降级开关 `admin.writes` 被关了，到「系统 › 安全与运维 › 降级开关」打开 |
| 在门户用管理员账号登录被拒 | 设计如此：持有后台角色的账号不能登录门户，请走后台地址 |

「邮箱或密码不正确」对外是同一句话，背后有四种情况：账号不存在、口令错误、账号停用、账号没有后台角色。审计里记了真实原因（同一来源短时间内重复失败只记一条）：

```bash
cd /opt/aegispanel/deploy
./psql.sh -X -c "SELECT occurred_at, after_digest->>'reason' AS reason FROM audit_events WHERE action = 'user.login_failed' AND api_domain = 'admin' ORDER BY occurred_at DESC LIMIT 20;"
```

`reason` 的取值：`invalid_credentials`（不存在或口令错）、`account_inactive`（停用）、`no_admin_role`（没有后台角色）。

列出全部管理员及其状态、角色、最近登录（只读）：

```bash
cd /opt/aegispanel && ( set -a && . deploy/.env && set +a && ./bin/aegis-adminctl list )
```

### 常见原因

- 前缀记错或被改过（`.env` 的 `AEGIS_ADMIN_PATH`）。
- 站点在 Cloudflare 后面，但没配真实 IP 信任表：所有人共用 Cloudflare 的几个出口 IP，限速很快就满。见仓库 README「装完」一节的 `update-cloudflare-realip.sh`。
- 唯一的管理员忘了口令，或者角色被收回了。

### 处理

- 限速：等一分钟再试，不要清 Valkey 里的限速键。
- **【写】** 重置口令。会吊销这个账号的全部会话和刷新令牌，写审计 `adminctl.password_reset`。口令只经标准输入：

  ```bash
  cd /opt/aegispanel && ( set -a && . deploy/.env && set +a \
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
sudo /opt/aegispanel/deploy/edge-tls.sh status     # 证书来源、到期时间、签发者、上次检查的结论
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
| 「续期校验」显示 `webroot 还指着别处` 或 `找不到续期配置` | certbot 的续期配置没走面板的校验目录 `/var/www/aegis-acme`，续期会 404。`renew` 每次会自动改正（改前备份到 `/var/backups/aegispanel/letsencrypt-renewal-*`），修不好的原因记进 status |
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
   sudo /opt/aegispanel/deploy/edge-tls.sh status
   ```

3. **【写】** 来源是 `selfsigned`：`sudo /opt/aegispanel/deploy/edge-tls.sh issue`。
4. **【写】** timer 没启用：`systemctl enable --now aegis-tls-renew.timer`。
5. **【写】** 对外地址改过：`sudo /opt/aegispanel/deploy/edge-tls.sh setup`。它会重新渲染 nginx，`nginx -t` 不过就换回原配置。
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
  docker ps -a --filter name=aegis-postgres          # 应为 Up (healthy)
  docker logs --since 30m aegis-postgres 2>&1 | tail -n 50   # 慢语句（>500 ms）、锁等待、OOM、磁盘满
  ```

- 连接与长事务：

  ```bash
  ./psql.sh -X -c "SELECT usename, state, count(*) FROM pg_stat_activity WHERE backend_type = 'client backend' GROUP BY 1, 2 ORDER BY 3 DESC;"
  ./psql.sh -X -c "SHOW max_connections;"
  ./psql.sh -X -c "SELECT pid, usename, state, now() - xact_start AS xact_age, wait_event_type, left(query, 80) AS query FROM pg_stat_activity WHERE xact_start < now() - interval '1 minute' ORDER BY xact_start;"
  ./psql.sh -X -c "SELECT pid, pg_blocking_pids(pid) AS blocked_by, left(query, 80) AS query FROM pg_stat_activity WHERE cardinality(pg_blocking_pids(pid)) > 0;"
  ```

连接预算：compose 里 `max_connections=60` = 3 条超级用户保留 + 11 条维护余量 + 门户 1 条常驻监听 + 三个网关各自的池上限（缺省门户 16、后台 15、节点 15）。算式在 `.env.example` 的连接池注释里。

### 常见原因

| 原因 | 怎么认 |
|---|---|
| 容器挂了或反复重启 | `docker ps` 状态；`docker logs` 里有 OOM（容器限 512M）或磁盘满（转第 9 章） |
| 某个会话 `idle in transaction`，或者手工跑的语句占着锁 | 第三、四条查询 |
| 改大了 `AEGIS_*_DB_MAX_CONNS`，或者多开了网关实例，总数超出预算 | 第一条查询各用户的连接数 |
| 保留期清理积压，表越来越大、查询越来越慢 | `admin.log` 里有 `…清理失败` |
| 某条业务查询慢 | `docker logs` 里同一条语句反复超过 500 ms |

### 处理

1. **【写】** 先取消、再终止。取消只停当前语句，终止会断开连接、回滚它的事务：

   ```bash
   ./psql.sh -X -c "SELECT pg_cancel_backend(<pid>);"
   ./psql.sh -X -c "SELECT pg_terminate_backend(<pid>);"   # 取消不管用时
   ```

   只动确认过的那个 pid。不要终止迁移会话（`goose`），处理迁移见第 11 章。
2. **【写】** 重启库：`cd /opt/aegispanel/deploy && docker compose restart postgres`。期间整个面板不可用，网关会自动重连。
3. **【写】** 调连接预算：改 `.env` 的 `AEGIS_*_DB_MAX_CONNS`，按 `.env.example` 的算式重排，再重启对应网关；改 `max_connections` 要改 compose 并重启库。
4. 要看语句的累计耗时，需要 `pg_stat_statements`。开它要改 `shared_preload_libraries` 并重启库，先评估停机窗口。

### 什么时候升级处理

同一条业务语句反复慢，或者保留期清理一直失败：带上 `docker logs` 里那条语句（先去掉其中的个人信息）和库体积，提给开发。

---

## 9. 磁盘满

### 怎么确认

```bash
df -h / /tmp
du -xsh /var/log/aegis /var/log/nginx /var/backups/aegispanel /var/lib/docker 2>/dev/null
journalctl --disk-usage
docker system df
./psql.sh -X -c "SELECT pg_size_pretty(pg_database_size(current_database()));"
./psql.sh -X -c "SELECT relname, pg_size_pretty(pg_total_relation_size(relid)) AS size FROM pg_catalog.pg_statio_user_tables ORDER BY pg_total_relation_size(relid) DESC LIMIT 15;"
```

后果：库写不进去，所有写操作失败，PostgreSQL 可能直接停掉；备份、证书续期也会失败。

### 常见原因

- **备份堆积。** `backup-postgres.sh` 只按 `AEGIS_BACKUP_RETENTION_DAYS`（缺省 14 天）清理自己生成的 `aegis-postgres-*.dump.age`。`install.sh` 升级前留的 `pre-upgrade-*.dump`、回滚前导出的 `rollback-*` 目录都不会自动清。
- **`/tmp` 满。** 它常是一块小的 tmpfs，在机器上构建会把它塞满；根分区却还有空间。
- **日志。** 网关日志每天轮转、单文件超过 64M 也轮转，保留 14 份；nginx 和 journal 按发行版设置。
- **库变大。** 节点上报的在线记录、探针点、流量汇总靠 aegis-admin 的保留期清理（每 10 分钟一轮）。后台网关停了很久，这些表会一直涨。
- **迁移预检。** 升级前的预检在同一个 PostgreSQL 里克隆整库，要有和库差不多大的空闲空间。

### 处理

1. **【写】** 删旧的升级前备份：先确认有一份更新的 `aegis-postgres-*.dump.age` 通过了校验（第 10 章），异地也有副本，再删 `pre-upgrade-*.dump`。
2. **【写】** `journalctl --vacuum-size=200M`。
3. **【写】** `docker image prune` 只清没有标签的旧镜像。**不要**用 `docker system prune --volumes`，它会删掉数据卷 `aegis_pgdata`。
4. 不要手动删 PostgreSQL 数据目录里的任何文件，包括 `pg_wal`。
5. 库本身太大：确认 aegis-admin 在跑，`admin.log` 里没有 `…清理失败`；积压会在之后几轮里分批清掉。

### 什么时候升级处理

清理之后某张表仍然持续猛涨：带上上面的表大小列表，提给开发。

---

## 10. 备份没跑或异常

### 怎么确认

```bash
systemctl status aegis-backup.service --no-pager
systemctl list-timers aegis-backup.timer               # 每天 03:17 UTC 附近
journalctl -u aegis-backup -n 100 --no-pager
ls -lt /var/backups/aegispanel | head
```

后台仪表盘的系统状态卡片也会显示：最新一份的时间、缺校验文件的份数、解密私钥有没有配好。

### 常见原因

- **timer 从没启用过。** `install.sh` 装了 `aegis-backup.service/.timer`，但不替你启用；新装的机器上「一份备份都没有」多半是这个。直装布局没有加密备份单元。
- `.env` 的 `AEGIS_BACKUP_AGE_RECIPIENT` 还是占位符。脚本拒绝生成明文备份，直接失败。
- Docker 或 `aegis-postgres` 没在跑（备份单元依赖 docker）。
- 磁盘满（第 9 章）。
- 远端上传钩子（`AEGIS_BACKUP_REMOTE_HOOK`）失败，日志里能看到。

### 处理

1. 按日志里的原因修 `.env` 或环境。
2. **【写】** 启用每日备份：`systemctl enable --now aegis-backup.timer`。
3. **【写】** 立即补一份：`systemctl start aegis-backup.service`，再看 `journalctl -u aegis-backup`。
4. 校验最新一份。要在 `deploy/` 目录下以 root 跑，它读同目录的 `.env`（要求属 root、0600 或 0400）：

   ```bash
   cd /opt/aegispanel/deploy && ./verify-backup.sh /var/backups/aegispanel/aegis-postgres-<时间>.dump.age
   ```

   - 它核对 sha256、签名清单、解密和归档目录，通过时输出 `backup verified:`。
   - 签名清单 `aegis-postgres-<时间>.manifest.json` 只有配了 WebDAV 异地备份才会生成。没有时报 `signed manifest not found`；只想确认这份能解开，可以一次性设 `AEGIS_BACKUP_ALLOW_UNSIGNED_LEGACY=RESTORE_UNSIGNED:<备份文件名>` 再跑。
   - 设 `AEGIS_VERIFY_RESTORE=1` 会在临时库里完整恢复一遍，需要和库差不多大的空闲磁盘；无签名的备份不能这样做。
5. 解密私钥（`.env` 的 `AEGIS_BACKUP_AGE_IDENTITY`）缺省和备份在同一台机器上。机器整体丢失时备份就解不开，私钥要另外保存一份。
6. 从备份恢复见 `MIGRATION-RUNBOOK.md` 第 3 节。

### 什么时候升级处理

备份能生成，但 `verify-backup.sh` 校验不过。

---

## 11. 迁移失败或卡在预检

`install.sh` / `install-native.sh` 升级和发布控制器的迁移顺序都是：停服前查无效索引 → 停服前在克隆库上预检 → 停服 → 迁移只核对预检凭据。

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
| `migration precheck: docker is required` | 直装机器上没有 Docker，跑不了克隆预检，升级停在这里，服务没停 |
| `migration precheck: PostgreSQL published endpoint mismatch` | `.env` 的 `POSTGRES_PORT` 和容器实际发布的端口不一致 |
| `migration precheck: attestation …; production is unchanged, rerun the full precheck` | 停服后的凭据核对不过（中间有人迁移过、迁移目录变了、凭据超过六小时等），服务已自动拉回。重新发版即可 |

单独查无效索引（只读；退出码 0 没有、1 有、2 查不了）：

```bash
cd /opt/aegispanel/deploy
PANDORA_LOCAL_MIGRATION_APPROVED=yes GOOSE_BIN=/opt/aegispanel/bin/goose ./migrate.sh check-indexes
```

**预检很慢：** 克隆和演练的耗时随库大小增长（5k 规模约 15–21 秒）。它在业务时段读整库，要有和库差不多大的空闲磁盘（第 9 章）。大库请挑低峰发版。

### 处理

- 一律先按报错修好原因，再重跑安装脚本或发布控制器。
- 迁移失败后的三条出路（前滚、`rollback-to`、从备份恢复）及其前提，只看 `MIGRATION-RUNBOOK.md`，不要在这里另找办法。

### 什么时候升级处理

- 迁移本身报错，例如约束不满足、守卫拒绝（P0001）；
- `rollback-to` 被 Down 守卫拒绝。

带上完整输出和 `migrate.sh version`，提给开发。

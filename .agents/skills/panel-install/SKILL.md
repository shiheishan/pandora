---
name: panel-install
description: pandora 在一次性测试机上按生产方式（panel/deploy/install.sh + build-release.sh 的发布包）首装或原地升级面板，做到后台能登录、三网关 healthz 200、https 可访问为止。用户或总协调说「在测试机装面板」「升级测试机上的面板」「按生产方式部署验证」「验证 install.sh」「验证升级链」时使用。压测与 seed 不在这里，见 prod-retest。
---

# 在测试机上按生产方式装 / 升级面板

终点：**后台能登录、三网关 healthz 200、https 可访问**。再往后（压测工具、realip、seed 数据、压测）是 prod-retest 的事。

## 前提与红线

- 机器已按 test-machine skill 开通登记（别名、ssh、`/root/README.md`）。下文 `$H` 指这台机器的 ssh 别名。
- 红线（仓库公开、现场值与口令的放置、测试机不删不重装）见根 CLAUDE.md「红线」与 test-machine skill。
- 本地发现安装链的问题，记下来交总协调派任务，不在测试机上临时改仓库脚本。
- 远端起长命令（构建约 10 分钟）的写法与 1Password 签名失败的处理见根 CLAUDE.md「环境与工具坑」。

## 首装

1. **工具链**（要在机器上构建时才装）：见 test-machine「开通」的 `install-toolchain.sh`。
2. **源码**：本机 `git archive --prefix=pandora-<sha>/ <sha> > <临时目录>/p.tgz`，scp 后解到 `$H:/root/src/`。
3. **构建**：`cd /root/src/pandora-<sha>/panel && PANDORA_VERSION=vt-<sha> bash deploy/build-release.sh /root/release`。2c4g 约 10 分钟。
   - 产物：`/root/release/pandora-panel_<版本>_linux_{amd64,arm64}.tar.gz`，旁边有 `.sha256` 与 `.manifest.sha256`。
   - 约束见「坑」：只能在 Linux 上跑、要在 `panel/` 下、显式给版本号。
4. **前置包**（Debian 主仓）：`apt-get update && apt-get install -y docker.io docker-compose age nginx certbot`。ufw 不用手工放行，edge-tls.sh 在 ufw active 时自己放行 80/443。
   - 只用 IP 时证书靠 lego 4.22+（Debian 13 主仓的 certbot 4.0 不支持 IP 证书，主仓 lego 4.9 不支持 ACME profiles）：不用手装，edge-tls.sh 在 Debian 上自动启用官方 `<版本>-backports` 源并 `apt-get install -t <版本>-backports lego`。
5. **校验并解包**：
   - 在 `/root/release/` 里 `sha256sum -c *_linux_amd64.tar.gz.sha256`；
   - 解到 `/opt/pandora-release/`，进包目录执行 `sha256sum -c SHA256SUMS`；
   - **保留这个目录**：之后原地升级、重装数据基座都要再跑它的 install.sh。
6. **安装**：进包内 `deploy/`，`PANDORA_ASSUME_YES=1 ./install.sh > /root/install-1.log 2>&1`，再 `chmod 600` 这个日志（结尾会打印后台前缀）。无人值守的全部变量见 install.sh 头注释。
   - 面板默认走 HTTPS：不给 `PANDORA_PUBLIC_BASE_URL` 时用本机公网 IPv4（`https://<IP>`），自动申请 Let's Encrypt IP 证书（shortlived，约 6 天，`aegis-tls-renew.timer` 每天两次续）；要验域名就加 `PANDORA_PUBLIC_BASE_URL=https://<域名>`（certbot 域名证书）。不再需要 sslip.io。
   - 对外地址只收 `https://DNS 域名` 或 `https://公网 IPv4`；localhost、私网 IP、IPv6 字面量、带端口或路径在动手前就停。
   - 同一台机器反复重装时，为免撞 Let's Encrypt 限额（同一 IP 每 7 天 50 张）可加 `PANDORA_ACME_SERVER=https://acme-staging-v02.api.letsencrypt.org/directory` 走 staging（证书不受信任，第 8 步改用 `curl -k`）；最终验收用正式环境。
   - 证书申请失败（80 被安全组挡住等）不算安装失败：自签兜底，日志里有原因与 `edge-tls.sh issue` 补救命令。
7. **核对安装结果**：
   - 日志里「迁移版本 0 → N」，N 等于包内 `migrations/` 的最大号；
   - 三网关 `127.0.0.1:9000/9001/9003` 的 `/healthz` 都是 200（`aegis-public / admin / node` 三个服务 active）；
   - 健康巡检 timer 已启用（`install.sh` 会 `enable --now aegis-health.timer`）：`systemctl is-enabled aegis-health.timer` 为 enabled，`systemctl list-timers aegis-health.timer` 有下次运行时间；
   - 日志里「nginx 配置已渲染并生效」「已换上 Let's Encrypt 证书」「续期 timer 已启用」；`/opt/aegispanel/deploy/edge-tls.sh status` 显示证书来源 `lego`（IP）或 `certbot`（域名）。若是 `selfsigned`，按日志里的原因修好后 `edge-tls.sh issue`。
8. **https 可访问**（`<地址>` 是 IP 或域名）：不带 `-k` 的 `curl https://<地址>/healthz` 返回 200（证书受信任）；`http://<地址>/x` 跳转 308 到 https；`http://<地址>/.well-known/acme-challenge/x` 是 404 而不是跳转；`https://<地址>/<后台前缀>/` 返回 200。前缀用机器上的 `/opt/aegispanel/deploy/admin-url.sh` 取。`systemctl list-timers aegis-tls-renew.timer` 有下次运行时间。
9. **建管理员**：install.sh 首装在交互终端里会现场建第一个管理员，本 skill 的无人值守装法（`PANDORA_ASSUME_YES=1`、日志重定向）下它跳过，按下面手工建。
   - 在机器上生成随机口令写进 `/root/lt-admin-cred.txt`（0600，第 1 行邮箱、第 2 行口令），后台前缀（用 `admin-url.sh` 取）写进 `/root/lt-admin-path.txt`（0600）。
   - 建管理员的命令见 loadtest README 第 2 节第 4 步，口令经 stdin（`printf %s "$pw" | … --password-stdin`），邮箱用 `ltadmin@example.com`：它是 prod-retest 的 run-load 缺省值，要压测就别换。
   - 两个文件 scp 回 `ops-local/<目录>/`，保持 0600。
   - 用这个账号登录一次后台（浏览器或后续 seed 的自检），登得进才算「后台能登录」。
10. 在机器 `/root/README.md` 补「现状」：版本、目录（`/opt/aegispanel`、`/opt/pandora-release`、`/root/release`）、日志与口令文件位置（只写位置不写值）。

## 原地升级

场景：同一台机器上换更新的发布包，验证升级链。

1. 照首装 2、3、5 做出新版本：**新的版本号、新的解包目录**，不覆盖旧目录。
2. 进新包的 `deploy/` 再跑一次 install.sh，不用加 `PANDORA_PUBLIC_BASE_URL`：
   ```bash
   set -o pipefail
   PANDORA_ASSUME_YES=1 ./install.sh 2>&1 | while IFS= read -r l; do printf '%s %s\n' "$(date -u +%T)" "$l"; done > /root/install-2.log
   ```
   给每行打时间戳，是为了量停服时间。检测到 `/opt/aegispanel/deploy/.env` 就自动走升级：保留 `.env` 与数据，不改 `AEGIS_ENV`。
   - HTTPS：已有 `aegis.conf` 的面板照常重渲染；以前 certbot 申请的 `/etc/letsencrypt/live/<域名>` 由 edge-tls.sh 直接接管（不再申请），nginx 改读 `/etc/aegispanel/tls/live/`，并启用续期 timer。接管时 edge-tls 会把 certbot 续期配置里面板主机的 webroot 改到 ACME 目录（改前备份到 `/var/backups/aegispanel`），升级验收要核（第 6 步）。
   - 从没走过 nginx 边缘的旧面板（没有 `aegis.conf`）升级时不替人接管 80/443，收尾提示切换命令：`PANDORA_ACME=1 ./install.sh`。要从域名换成 IP（或反过来），改 `.env` 的 `AEGIS_PUBLIC_BASE_URL` 后重跑 install.sh。
3. 升级链的内部顺序与失败处置不在这里重复，见 `.claude/rules/deploy-scripts.md` 与 `panel/deploy/install-lib.sh` 的 `pandora_run_migrations`。验收时只认日志里的几个锚点：升级前备份 `/var/backups/aegispanel/pre-upgrade-<时间>.dump`（备份失败就中止，服务没停）、「预检通过（N 秒）…」（停服前，预检失败服务没停）、「停止服务后迁移」、「启动服务」。
4. **停服时间**：日志里「停止服务后迁移」到「启动服务」之间的时间戳之差（预检不在这段里）。成绩里同时写迁移条数和库大小，否则这个数没法比。
5. **迁移号核对**：日志「迁移版本 M → N」，M 等于升级前（`SELECT max(version_id) FROM goose_db_version`，用 `/opt/aegispanel/deploy/psql.sh`），N 等于新包 `migrations/` 的最大号。
6. 照首装 7、8 重新核对 healthz 与 https；再登录一次后台。接管过 certbot 证书的机器另核续期：`/opt/aegispanel/deploy/edge-tls.sh status` 的「续期校验」行应是 `webroot /var/www/aegis-acme（…）`（显示「webroot 还指着别处」或「找不到续期配置」都不合格，原因在 `/var/lib/aegispanel/tls/status`）；域名证书再跑一次 `certbot renew --dry-run` 要过。IP 证书（lego）没有这行，看 `systemctl list-timers aegis-tls-renew.timer`。
7. 升级前后各留一份 `systemctl is-active aegis-public aegis-admin aegis-node` 与版本号（后台登录页或侧栏显示的发布版本）写进现场记录。

## 坑

- **证书与 Debian 默认站点**：Debian 的 nginx 包自带默认站点，也 listen 80 default_server，会和 `aegis.conf` 抢，`nginx -t` 报 duplicate default server。edge-tls.sh 渲染前只停用发行版原样的那个链接（原文件留着，可 `ln -s ../sites-available/default` 链回）。撞上 Let's Encrypt 限额就停下报告；同一台反复重装用 `PANDORA_ACME_SERVER=<staging 地址>`，最后验收用正式环境。
- **证书**：流程在 `edge-tls.sh` 与 `rules/deploy-scripts.md`。续期结论在 `/var/lib/aegispanel/tls/status`，`journalctl -u aegis-tls-renew` 看输出。节点用 https 接入面板要正规证书：自签兜底期间节点接入会校验失败。
- **网关经 unix socket 连 PG 与 Valkey**（`deploy/run/`）：复测时确认真走了 socket：`SELECT client_addr IS NULL AS unix, count(*) FROM pg_stat_activity WHERE usename='aegis_app' GROUP BY 1`。socket 不可用时 install.sh 保持回环端口；`.env` 已是 socket 而 socket 起不来，网关起不来，install.sh 会停下。要回退到更早的发布包，先按 `.env` 末尾的注释把 `AEGIS_DATABASE_URL`、`AEGIS_REDIS_URL` 改回回环形式（口令不变）。
- **`build-release.sh` 的两个约束**：
  - 在 Linux 上、在 `panel/` 下跑；
  - 要显式给版本号：`git archive` 解出来的源码没有 `.git`，缺省版本是 `dev`，追不到提交。
- **install 日志里有后台前缀**：保存后立刻 `chmod 600`，不要 `cat` 到对话里。
- 首装对外地址不合规（私网 IP、IPv6 字面量、带端口、带路径）会在动手前就停，不会装到一半。
